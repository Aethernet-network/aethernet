# Canonical Event Selection Consistency + Verification Discipline — Workstream Plan v2

**Workstream**: F4 — Selection Consistency Fix

**Status**: Plan v2. Synthesis of v1 + Grok review + ChatGPT review. Ready for founder approval. Not yet Claude-Code plan-mode reviewed.

**Trigger**: Part F retry Phase C-sanity on 2026-04-22 discovered cross-node settlement-verdict divergence on a 5-node testnet. Root cause is a class of "selection-race" bugs where multiple validators independently emit semantically-distinct canonical events for the same logical key; per-node first-event-past-the-mutex wins; nodes apply different settlements; ledger forks.

**Source documents**:
- `docs/plans/implementation/selection-race-characterization.md`
- `docs/plans/implementation/multi-emit-bug-class-audit.md`
- `docs/audits/2026-04-22-codebase-quality-audit.md`
- `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (F3-B)

**Scope**: bug-class-level structural fix + verification discipline that catches this class going forward + foundation test coverage for the trust-model layer + explicit audit of F3-B locked invariants for refinement. Explicitly out of scope: mainnet operational readiness, rolling-upgrade protocol, security posture audit, `cmd/node/main.go` decomposition, Step 4 (reputation).

**Duration estimate**: 7–9 weeks. Revised from v1 based on honest scoping of Part A infrastructure + Part B foundations + Part D refactor across 5 consumer sites + fresh testnet verification iterations. Not set aspirationally.

**What changed from v1**:
1. `CrossNodeDivergence` demoted from canonical event to non-canonical metric + alert + CLI.
2. Winner-event abstraction eliminated. `SelectionRule` replaced with simpler `LogicalKeyConsumer` interface (`Key`, `IsComplete`, `DeriveOutcome`) deriving outcome from canonical underlying state.
3. `superseded` state and `SelectionConflict` event eliminated (consequence of #2).
4. Selection window replaced with `IsComplete` completeness rule grounded in canonical vote weights.
5. `RoundState` and `Outcome` given concrete typed definitions.
6. Duration revised 4–6 → 7–9 weeks.
7. Part F partial-failure analysis scoped to 1 day targeted enumeration.
8. Performance non-regression gate added.
9. `cmd/node/main.go` registration audit added as mandatory pre-Part-D step.
10. CI lint implementation tasks made explicit (not assumed).
11. Peer discovery for monitoring hook specified.
12. Integer-migration merge conflict checklist + combined-branch re-verification added.
13. Advisory-field handling specified as invariant.
14. Map-iteration audit prioritization specified.
15. `design-principles.md` + `CLAUDE.md` updates required in Part G.
16. Internal phasing F4A/F4B/F4C with stop-go gates (one founder-approved workstream, three internal gates).

---

## 0. Decisions locked before implementation

Not open for re-debate during implementation:

1. **Absorb Tier 0 + Tier 1 in this workstream.** Fix + verification discipline + foundation tests ship together. The pattern of "fix narrowly → verification gap ships → next bug class ships unnoticed" ends here.

2. **Forward-only. No historical divergence reconciliation.** Historical divergences (50 AET stake state, 277K µAET Phase C-sanity) remain as fossils in the wiped testnet's historical evidence. Fresh testnet has fresh genesis.

3. **Testnet wiped and restarted from genesis** before this workstream's verification phase begins. Decision made 2026-04-22.

4. **Integer-migration workstream merges inside this workstream's final phase**, after end-to-end testnet verification passes.

5. **Fix mechanism**: outcome-derived-from-canonical-state (votes/attestations) via a new `LogicalKeyConsumer` interface. No winner-event selection. Architect-session locked this direction after multi-AI review; implementation details are open for plan-mode.

6. **No merge to main until end-to-end testnet verification per §11 passes.**

7. **Reputation Step 4 remains paused** until this workstream ships and verifies.

8. **Observer artifacts are NOT canonical events in this workstream.** Cross-node divergence, performance anomalies, selection-conflict-like conditions surface via metrics/alerts/CLI. Canonical protocol surface stays minimal.

9. **Internal phasing** — single founder-approved workstream, three internal stop-go gates:
   - **F4A**: verification infrastructure + foundation tests + replay wiring + map-iteration audit + cmd/node registration audit.
   - **F4B**: LogicalKeyConsumer protocol fix + consumer migrations.
   - **F4C**: integer-migration merge + fresh testnet verification + main merge.

   Each internal gate requires architect-session review before the next phase begins. Founder sees one workstream; execution respects internal stop-go discipline.

---

## 1. Load-bearing framing

The bug-class root cause is that consumers derive canonical state from a single event's payload field when that field was computed from per-node-local data (vote-receive order, attestation-set timing). The fix derives canonical state from canonical underlying events (votes, attestations) that are themselves cluster-uniform.

**Design principle enshrined**: *"For consensus-representation events with multi-emitter potential, the event is a readiness signal, not a source of truth. Canonical outcome is derived from underlying canonical state."*

Seven parts plus cross-cutting items:

- **Part A** — Verification discipline infrastructure (cross-node byte-equality test, replay-conformance template, non-canonical monitoring).
- **Part B** — Foundation-layer test coverage (jcs, store, schema-migration doc).
- **Part C** — Locked-invariant review and refinement (F3-B invariants audit).
- **Part D** — LogicalKeyConsumer protocol fix (5 consumer migrations).
- **Part E** — Map-iteration non-determinism audit and remediation.
- **Part F** — Partial-failure blast-radius analysis (1-day targeted).
- **Part G** — Integer-migration cutover and merge.

Cross-cutting: replay path wiring (`SourceReplay` + `replayHistoricalToBusConsumers()`), schema-version enforcement verification, CI lint implementation, cmd/node registration audit.

---

## 2. Part A — Verification discipline infrastructure (F4A phase)

### 2.1 What changes

**2.1.1 Cross-node ledger byte-equality integration test.**

New package `internal/verification/cross_node/`. Test harness:
- Spins up 3+ nodes in-process.
- Runs configurable corpus of tasks/events.
- After each canonical event settles, queries each node's full ledger state.
- Asserts byte-equality across all nodes: per-agent balances, escrow, treasury, validator stakes, admission records.
- Fails with per-node diff on any mismatch.

Test-layer assertion of "same DAG → same projected state" invariant.

**2.1.2 Replay-path conformance test template.**

New template in `internal/dispatch/conformance/`: "consumer added to populated DAG receives historical events correctly."

Flow:
- Initialize node with DAG history pre-populated in store.
- Register new dispatcher consumer against this populated-DAG state.
- Run startup replay sequence (including the new `replayHistoricalToBusConsumers()` pass per §8.1).
- Assert consumer's `Apply` fires for every historical event it's `Interested()` in.
- Assert idempotency on re-run.

Every dispatcher consumer passes this template in CI via the no-bypass lint.

**2.1.3 Non-canonical cross-node monitoring.**

New package `internal/monitoring/cross_node_invariants/`. This is NOT a canonical-event emitter. It is pure observation surfacing through:

- **Prometheus metric** `aethernet_cross_node_ledger_divergence` — gauge reporting magnitude of observed divergence across known peers.
- **CLI command** `aet invariants check` — operator-facing on-demand check, prints per-node state comparison and divergence if any.
- **Structured log** at `Warn` level when divergence exceeds threshold, with per-node snapshots for diagnosis.
- **Alertable**: the metric can be scraped by standard alerting infrastructure.

Peer discovery: the monitor reads the cluster's peer list from the existing `internal/network/discovery` subsystem. The list is already maintained for fastpath routing; this adds a read-only consumer.

Design rationale: divergence is an observation, not a protocol action. Protocolizing observations pollutes the canonical surface with observer artifacts and can create recursive concerns (the observer's emissions themselves subject to selection-race-class bugs). Keep observation layer and protocol layer distinct.

### 2.2 Invariants

**A-1**: Cross-node byte-equality integration test runs in CI on every PR. Any per-node ledger-state deviation for any corpus fails the build.

**A-2**: Every dispatcher consumer has a replay-conformance test in its package's CI configuration. Adding a new consumer without passing replay-conformance fails the no-bypass CI lint.

**A-3**: Monitoring subsystem is observer-only. No canonical state mutation. Divergence surfaces via Prometheus metric + CLI + logs only.

**A-4**: Monitor uses existing peer discovery from `internal/network/discovery`. No new peer-configuration surface.

### 2.3 Verification

- `go test -race ./internal/verification/cross_node/...` passes on 3-node in-process cluster with 10-task mixed-verdict corpus including intentional vote-weight ties.
- `go test -race ./internal/dispatch/conformance/...` passes new replay template against every existing dispatcher consumer.
- Monitor runs in 5-node cluster for ≥ 1 hour, zero false-positive divergence alerts, correct detection when divergence intentionally injected via test-only hook.

---

## 3. Part B — Foundation-layer test coverage (F4A phase)

### 3.1 What changes

**3.1.1 `internal/jcs` unit tests.**

Current: 130 LoC, 0 tests. This package is the canonical JSON serialization underlying all content-addressing.

Add tests:
- **Determinism**: `jcs.Marshal(X)` identical bytes across 100 calls.
- **Platform stability**: determinism on amd64 + arm64 (reuse Part D cross-arch corpus infrastructure).
- **Canonical-form properties**: sorted keys, normalized numbers, no whitespace, UTF-8 NFC.
- **Round-trip**: `jcs.Unmarshal(jcs.Marshal(X)) == X` for every canonical event type.
- **Edge cases**: empty objects, nested nulls, number precision edges.
- **Negative tests**: non-canonical inputs re-canonicalized identically.

Target: test ratio ≥ 2.0.

**3.1.2 `internal/store` unit tests.**

Current: 1,569 LoC, 326 LoC tests (0.21 ratio).

Add tests:
- **Transaction atomicity** under error injection.
- **Recovery from corruption** (injected corrupted keys/values).
- **AdmissionStore round-trip** matching dispatcher state machine.
- **Meta-store** round-trip.
- **Concurrent access** multi-goroutine consistency.
- **Schema migration** forward/backward per §3.1.3.

Target: test ratio ≥ 0.5.

**3.1.3 Schema/version migration discipline document.**

Produce `docs/architecture/schema-migration-discipline.md`:
- Enumerate every persisted record type + current schema version.
- Document migration path for each type (forward/backward).
- Identify record types missing version fields that need them.
- Policy for introducing schema change (new version, dual-read, cutover, removal).

Documentation + light code review. Actual migrations are future work; this establishes discipline.

### 3.2 Invariants

**B-1**: `internal/jcs` test ratio ≥ 2.0. `go test -race -count=100 ./internal/jcs/` passes (100x determinism stress).

**B-2**: `internal/store` test ratio ≥ 0.5.

**B-3**: Every persisted record type enumerated in `schema-migration-discipline.md` with schema version and migration policy.

### 3.3 Verification

- Numeric gates met.
- Identical output across 100 jcs test runs.
- Document reviewed; every production persisted-record type covered.

---

## 4. Part C — Locked-invariant review and refinement (F4A phase)

### 4.1 Framing

F3-B locked invariants under the model assumption "one canonical event per round." That assumption is empirically false. This section re-examines the locks that depend on that assumption.

Not an invitation to unlock everything. Most F3-B locks are sound and stay locked. A small number need refinement. Purpose is explicit and documented.

### 4.2 Invariants that stay locked (and why)

All of the following stay locked — enumerated so the architectural diff is explicit:

- A-1 through A-4 (LoadApplied: load-before-listener, fail-startup-on-error, sync, DAG-anchor): Sound.
- B-1 through B-4 (same shape for escrow): Sound.
- C-2 (atomic reservation): Sound.
- C-4 (five-state lifecycle): Sound. No new states added (consequence of winner-event elimination).
- C-5 (reservation transaction minimality): Sound.
- C-6 (DAG-anchored every admission): Sound.
- C-7 (storage-error fail-closed): Sound.
- C-8 (conformance in CI): Sound. Extended by Part A's replay-conformance template.
- C-9 (single mandatory replay-state registry): Sound.
- C-10 (no-bypass): Sound. Extended by Part A's replay-conformance requirement.
- C-11 (atomic local commit): Sound.
- C-12 (per-(event, consumer) completion): Sound.
- C-13 (DAG anchor identity): Sound.
- C-14 (recovery probes evidence-based, monotonic, replay-safe): Sound.
- C-15 (admission state is non-canonical node-local): Sound.
- C-16 (future-consumer inheritance): Sound.
- D-1 through D-8 (causal prerequisite gating): Sound.
- **§4.5 (atomic-batch forward-only settlement)**: Sound. **Preserved unchanged.** Winner-event elimination means no `superseded` pre-commit transitions are needed, so forward-only remains strict.

### 4.3 Invariants that require refinement

**C-3 refinement — admission key varies by consumer type.**

Original: "Admission keys derive from BLAKE3 of canonical-serialized event bytes."

Problem: content-hash admission treats distinct canonical events for the same logical key as distinct admission records. Correct for one-off events. Wrong for consensus-representation events where multiple byte-distinct events for the same logical key should coordinate into one canonical settlement.

Refinement: consumers declare admission strategy at registration:

- **Content-hash admission** (default): `AdmissionKey = BLAKE3(canonical-serialized event bytes)`. Every consumer today uses this.
- **Logical-key admission** (new, opt-in): `AdmissionKey = consumer-declared logical-key projection of event payload` (e.g., `RoundID` for TVConsensus, `TargetEventID` for Settlement).

Each consumer declares strategy at registration. Dispatcher enforces: logical-key consumers additionally implement `LogicalKeyConsumer` interface (§4.4 below).

**C-3' (refined)**: every consumer declares admission-key strategy at registration: content-hash (default) or logical-key. Logical-key consumers implement `LogicalKeyConsumer`. Dispatcher validates declarations structurally at registration; malformed declarations fail structural validation.

**Invariant Serialization-1 companion — Serialization-2 (new).**

Original (Serialization-1): "For any canonical event, there exists exactly one valid canonical serialized byte representation."

Companion: **Serialization-2 (new)** — For logical-key-admitted event types, canonical outcome is derived from underlying canonical state (votes, attestations) that is itself deterministic and cluster-uniform. The event's payload fields describing outcome (e.g., `FinalVerdict`) are advisory; consumers must not derive canonical state from them.

### 4.4 LogicalKeyConsumer — the new primitive

Replaces v1's SelectionRule. Simpler: no winner selection, no tiebreaker, no `superseded` state.

```go
// LogicalKeyConsumer is a dispatcher consumer that handles events
// admitted by logical-key (e.g., RoundID, TargetEventID) rather than
// content-hash. Multiple byte-distinct canonical events can exist in
// the DAG for the same logical key; this consumer treats them as
// readiness signals and derives canonical outcome from underlying
// state that is cluster-uniform.
type LogicalKeyConsumer interface {
    // Key extracts the logical-key value from an event.
    Key(ev *event.Event) (LogicalKey, error)

    // IsComplete returns true when enough evidence exists in the
    // round state to derive a canonical outcome that no future event
    // arrival can change. Must be a pure deterministic function of
    // canonical state. Must not consult wall-clock, ephemeral memory,
    // or external systems.
    //
    // For TVConsensus: returns true when pass_weight or fail_weight
    // has crossed the supermajority threshold AND the gap cannot be
    // closed by remaining possible votes (i.e., the outcome is
    // mathematically sealed).
    //
    // For Settlement: returns true when the attestation set has
    // reached supermajority and no further attestations can change
    // the outcome.
    IsComplete(roundState RoundState) (bool, error)

    // DeriveOutcome computes the canonical outcome from round state.
    // Must be pure deterministic. For TVConsensus: the verdict
    // (accept/reject) derived from vote-weighted consensus. For
    // Settlement: the attestation-derived outcome.
    DeriveOutcome(roundState RoundState) (Outcome, error)

    // Apply invokes the consumer's authoritative local state
    // transition. Called once per logical key, after IsComplete
    // returns true. Receives the derived Outcome, not the triggering
    // event's payload.
    Apply(ctx context.Context, key LogicalKey, outcome Outcome) error
}
```

Concrete typed supporting structs:

```go
// RoundState holds the canonical state the consumer needs to
// compute completeness and outcome. Separate structs per consumer
// type if needed; for now TVConsensus and Settlement can share.
type RoundState struct {
    LogicalKey       LogicalKey
    Votes            []*event.Event  // canonical Vote events for this round
    Attestations     []*event.Event  // canonical attestations, where applicable
    ObservedEvents   []*event.Event  // all logical-key-matched events observed
    Epoch            uint64
    // Additional fields added as consumer types require, with
    // explicit typed shape — never interface{}.
}

// Outcome is the derived canonical result. Concrete type per
// consumer; shared type for consumers with similar outcome shapes.
type Outcome struct {
    Verdict          Verdict           // "accept" | "reject" | ...
    ScoreBP          uint32
    ParticipatingIDs []crypto.AgentID  // validators whose votes/attestations counted
    // Consumer-specific fields added with explicit shape.
}
```

### 4.5 How the dispatcher handles logical-key admission

Per-event flow:

1. Event arrives via bus → dispatcher.Admit.
2. Dispatcher checks consumer's admission strategy.
3. For content-hash consumers: existing flow (§F3-B). Unchanged.
4. For logical-key consumers:
   a. Compute `key, err := consumer.Key(ev)`.
   b. Record the event under `(consumer, key)` in admission store (not as a settled admission — as an observation).
   c. Query canonical round state for `key` (Votes, Attestations, etc. — read from DAG).
   d. Call `consumer.IsComplete(roundState)`.
   e. If not complete: record observation, return. No Apply invocation.
   f. If complete AND consumer has not yet applied for this key: compute `outcome, err := consumer.DeriveOutcome(roundState)`. Invoke `consumer.Apply(ctx, key, outcome)` within an atomic batch per C-11.
   g. Mark `(consumer, key)` as applied in admission store. Future events for this key are observed but do not trigger Apply.

Critical properties:

- **Apply invocation is per-key, not per-event.** A logical-key consumer's Apply fires exactly once per logical-key value, regardless of how many events for that key exist.
- **`IsComplete` is deterministic on canonical state.** Same votes on all nodes → same completeness determination. Nodes may reach `IsComplete=true` at different wall-clock moments based on event arrival order, but they all reach it with the same Outcome.
- **No `superseded` transitions.** Since Apply fires only after completeness is reached AND completeness is defined so further events cannot change Outcome, there is no case where a post-Apply event would change the outcome.
- **Forward-only settlement preserved.** Atomic-batch per C-11 is unchanged. Apply commits once; no rollback needed.

### 4.6 Advisory-field handling

Event payloads may contain fields that describe outcome (e.g., `FinalVerdict` on TVConsensus, `Attestations` on Settlement). Under logical-key admission, these fields become **advisory**:

- Emitters MAY populate them (diagnostic, backward compatibility).
- Consumers MUST NOT derive canonical state from them.
- Consumers MUST derive canonical state from `roundState` (Votes, Attestations in the DAG).

**C-17 (new)**: logical-key-admitted event payloads may carry advisory outcome fields. Canonical state derivation is from canonical underlying state only. Advisory fields are for diagnostics and backward compatibility.

### 4.7 Completeness rule for TVConsensus (example)

Concrete shape of `IsComplete` for TVConsensus:

```go
func (c *TVConsensusLogicalKeyConsumer) IsComplete(rs RoundState) (bool, error) {
    passWeight, failWeight := weightsFrom(rs.Votes)
    maxRemaining := maxPossibleRemainingWeight(rs)
    threshold := supermajorityThreshold(rs)

    // Complete iff the outcome is mathematically sealed:
    // either pass has crossed threshold and cannot be overtaken,
    // or fail has crossed threshold and cannot be overtaken.
    passSealed := passWeight >= threshold && passWeight > failWeight + maxRemaining
    failSealed := failWeight >= threshold && failWeight > passWeight + maxRemaining

    return passSealed || failSealed, nil
}
```

This is an early-termination rule used in many consensus protocols. Cluster-uniform because it operates on canonical vote set.

For Settlement: analogous logic on attestation set.

### 4.8 What this refinement does NOT do

- Does not introduce reversible settlement. Forward-only preserved.
- Does not change F3-B's per-(event, consumer) completion semantics for content-hash consumers.
- Does not change the atomic-batch invariant.
- Does not change DAG-anchor verification.
- Does not change causal prerequisite gating.
- Does not affect Type A/B/C/D consumer taxonomy structurally. Type E (§13) added.
- Does not add any new canonical events.

---

## 5. Part D — LogicalKeyConsumer protocol fix (F4B phase)

### 5.1 Prerequisite: cmd/node registration audit

**BEFORE any Part D code changes**, produce `docs/audits/2026-04-XX-cmd-node-registration-audit.md`:

- Enumerate every consumer registration in `cmd/node/main.go`.
- Document current admission strategy (all content-hash today).
- Document startup ordering dependencies (e.g., LoadApplied before listener start per F3-B A-1).
- Document every point where consumer-type-specific wiring is performed.
- Identify migration paths for the 5 consumers changing to logical-key admission.
- Flag any registrations whose order or dependency is load-bearing for this migration.

This audit produces the migration checklist. Part D does not proceed without it.

Rationale: cmd/node/main.go is the highest-regression-risk surface in the codebase (3,469 LoC, 0.06 test ratio, previous startup-ordering bug lived there). Changes to registrations need explicit audit.

### 5.2 What changes

5 HIGH-risk emission sites become logical-key-admitted via `LogicalKeyConsumer`:

**5.2.1 `EventTypeTaskVerificationConsensus`** — logical-key = `RoundID`.
- `Key`: extract `RoundID` from payload.
- `IsComplete`: cluster-uniform supermajority seal on `round.Votes`.
- `DeriveOutcome`: verdict from vote-weighted consensus.
- `Apply`: invoke settler with derived outcome.

`FinalVerdict` field in payload becomes advisory (populated by emitter for diagnostics, ignored by consumer for canonical state).

Unifies vote-path and deadline-path emits under single logical-key admission.

**5.2.2 `EventTypeSettlement`** — logical-key = `TargetEventID`.
- `Key`: extract `TargetEventID`.
- `IsComplete`: attestation-set supermajority seal.
- `DeriveOutcome`: outcome from canonical attestations.
- `Apply`: invoke settlement applicator.

`Attestations` field in payload becomes advisory.

**5.2.3 `EventTypeTaskSettlement`** (autovalidator path) — logical-key = `TaskID`.
- `Key`: extract `TaskID`.
- `IsComplete`: derived from the task's verification round outcome (itself logical-key-admitted, so cluster-uniform).
- `DeriveOutcome`: derive from round's canonical outcome.
- `Apply`: perform task settlement.

**5.2.4 SettlementConsumer refactor.**

`internal/recognition/settlement_consumer.go` no longer admits events directly. Instead subscribes to the dispatcher's logical-key-admitted Settlement consumer's outcomes.

### 5.3 Invariants

**D-1 (new)**: TaskVerificationConsensus, Settlement, TaskSettlement events are logical-key-admitted per C-3'. Admission keys are `RoundID`, `TargetEventID`, `TaskID` respectively.

**D-2 (new)**: Canonical outcome for these three event types is derived from underlying canonical state (votes, attestations), not from the triggering event's payload outcome fields.

**D-3 (new)**: The `FinalVerdict` field in TVConsensus payloads and `Attestations` field in Settlement payloads are advisory. Consumers do not derive canonical state from them.

**D-4 (new)**: Full regression verification: `internal/integration/` test suite and Part A's cross-node byte-equality harness pass. All pre-existing settlement tests pass; new multi-emit-with-ties tests pass.

**D-5 (new)**: Performance non-regression: dispatcher throughput on 5-node testnet under representative load shows no material regression vs pre-Part-D baseline. Baseline measured pre-Part-D; gate is "no measurable increase in per-event dispatch latency median or p99."

### 5.4 Verification

- cmd/node registration audit complete and reviewed.
- `go test -race ./internal/dispatch/...` passes.
- `go test -race ./internal/recognition/...` passes.
- `go test -race ./internal/taskverification/...` passes.
- Part A cross-node byte-equality harness passes with vote-weight-tie corpus.
- Replay-conformance template passes for refactored consumers.
- Performance regression gate passes.

---

## 6. Part E — Map-iteration non-determinism audit (F4A phase)

### 6.1 What changes

Priority-ordered audit of all 508 `range over map` callsites in production code.

**Priority 1 (blocking Part D)**: callsites in logical-key consumers, dispatcher core, settlement. These are the exact places where non-determinism would re-introduce divergence under the new LogicalKeyConsumer design. Fix before Part D begins.

**Priority 2 (parallel with Part B)**: callsites in recognition bus, store, network hot paths. Fix in parallel with foundation test work.

**Priority 3 (lightweight)**: remaining callsites. Classify but don't fix unless Unsafe-without-sort. Schedule any Unsafe-inherently to follow-on workstreams.

For each callsite, classify:
- **Safe**: iteration is order-independent.
- **Unsafe-without-sort**: must be sorted before use. Fix: sort map keys before iterating.
- **Unsafe-inherently**: order dependency can't be fixed by sorting. Fix: restructure (scheduled).

Produce `docs/audits/2026-04-XX-map-iteration-determinism.md`.

### 6.2 CI lint

**NEW ENFORCEMENT WORK**: CI lint that fails the build if a production `range over map` is added without:
- an accompanying classification comment (`// safe: order-independent`), OR
- an explicit sort before iteration.

This lint does not exist today. Part E includes implementation as a `golangci-lint` custom check or equivalent.

### 6.3 Invariants

**E-1 (new)**: Every `range over map` in production code is classified as Safe or sort-fixed. Unsafe-inherently instances documented in `docs/architecture/known-map-iteration-dependencies.md` with explicit reasoning.

**E-2 (new)**: CI lint operational; adding a production `range over map` without classification or sort fails the build.

### 6.4 Verification

- Priority 1 callsites fixed before Part D begins.
- Audit document complete.
- CI lint operational; verified by introducing deliberate violation and observing build failure.

---

## 7. Part F — Partial-failure blast-radius analysis (F4A phase)

### 7.1 What changes

**Scope**: 1 day of targeted enumeration. Not an open-ended documentation exercise.

Focus on the new logical-key paths + dispatcher/store boundaries (the surfaces this workstream introduces). Broader subsystem-boundary analysis is a follow-on workstream (F7 or similar).

Produce `docs/architecture/partial-failure-analysis-f4.md`:

For each boundary touched by F4:
1. What can fail.
2. Observable symptoms.
3. Recovery path (automatic / operator-assisted / manual).
4. Known gaps.

Boundaries:
- LogicalKeyConsumer: what happens if `IsComplete` returns true but `DeriveOutcome` errors?
- Dispatcher ↔ store: what happens if admission-store write succeeds but Apply crashes?
- Replay path: what happens if `replayHistoricalToBusConsumers()` partially completes then crashes?
- Advisory field handling: what happens if a consumer accidentally reads advisory fields?

For identified gaps with reasonable fix cost: implement in Part F. Larger gaps: document and schedule.

### 7.2 Invariants

**F-1 (new)**: Every F4-introduced subsystem boundary has documented failure mode with symptoms + recovery.

**F-2 (new)**: Gaps either fixed in this workstream or scheduled with named owning workstream.

### 7.3 Verification

- Document produced. Architect-session reviewed.
- Fixes shipped in workstream pass individual tests.
- Scheduled gaps have named owning workstreams.

---

## 8. Cross-cutting items

### 8.1 Replay path wiring

Fix the `SourceReplay` / `LoadFromStore` architectural gap identified in Part F retry Path B.

Add `replayHistoricalToBusConsumers()` in `cmd/node/main.go` (or dedicated file):

```go
// Called after SetOnCommit is wired, before live ingestion begins.
// Walks the DAG and emits each event through the bus with
// source=SourceReplay, replay=true. Per-consumer MarkRecognizedOnce
// ensures idempotency for previously-recognized events.
func replayHistoricalToBusConsumers(ctx context.Context, ...) error {
    // Implementation.
}
```

Invariant: after this function returns, every bus consumer has observed every DAG event matching its `Interested()` filter, at least once.

Part A's replay-conformance template validates this for every consumer.

### 8.2 Schema-version enforcement verification

Walk every `SchemaVersion` check in the codebase. Verify:
- Every event type with a version field has the check invoked at admission.
- Version mismatches fail loudly, not silent reinterpretation.
- Version history documented in `schema-migration-discipline.md`.

### 8.3 No-bypass CI lint verification

**NEW ENFORCEMENT WORK**: audit that F3-B's C-10 no-bypass lint (which should fail the build if a canonical consumer is wired directly to the fabric instead of through the dispatcher) is actually operational. If it isn't, implement it. If it is, verify by introducing a deliberate violation and observing build failure.

### 8.4 cmd/node registration audit

Produced as prerequisite for Part D per §5.1.

### 8.5 design-principles.md and CLAUDE.md updates

**REQUIRED IN PART G**: update load-bearing docs with F4's new primitives:

- `docs/design-principles.md`: add principle or section describing "outcome derived from canonical underlying state, not from triggering event's payload." New Type E consumer taxonomy. Serialization-2 invariant.
- `CLAUDE.md`: update with F4 context for future architect sessions, including the LogicalKeyConsumer pattern and logical-key admission.

Not optional. New primitives must be reflected in the load-bearing docs or they drift.

---

## 9. Part G — Integer-migration cutover and merge (F4C phase)

### 9.1 What changes

After F4A and F4B phases ship on `feat/selection-consistency-fix`:

**9.1.1 Merge conflict resolution checklist.**

Both branches touch:
- Settlement (`internal/settlement/`)
- Dispatcher consumers (`internal/dispatch/`)
- Activation consumer (`internal/dispatch/integer_migration_activation_consumer.go`)
- Store meta (`internal/store/`)

Produce explicit checklist before merge begins:
- Every file changed by both branches.
- Every function signature changed by either branch.
- Every new type introduced by either branch.
- Resolution approach per conflict (which branch's version wins; where merging is required).

Merge execution uses the checklist. Any off-checklist conflict stops the merge for architect review.

**9.1.2 Combined-branch re-verification.**

After merge:
- Full test suite: `go test -race -count=3 ./...`
- Part A cross-node byte-equality harness on combined branch.
- Integration tests re-run.

**9.1.3 Fresh testnet deploy and verification.**

Freshly-wiped testnet (per §0.3).

Deploy combined binary `selection-fix-integer-migration-<commit>`.

Run:
- Phase C-sanity equivalent: 5 tasks including intentional vote-weight ties. Cross-node byte-equality asserted.
- Phase D equivalent: emit integer-migration activation event. Observe admission → Apply → flag flip on all 5 nodes.
- Phase E equivalent: 10-task post-activation corpus. Cross-node byte-equality asserted; zero shadow_delta (post-activation).
- Phase F equivalent: single-node restart; verify startup-load restores integer-canonical mode.
- Full 19-criterion F3-B success matrix plus Part A's cross-node byte-equality criterion.

If all gates pass, merge `feat/selection-consistency-fix` to `main`. Tag release.

**9.1.4 Documentation updates.**

Per §8.5: update `design-principles.md` + `CLAUDE.md`. Commit updates to main after merge.

### 9.2 Invariants

**G-1**: All Part A–F invariants satisfied before merge begins.

**G-2**: Merge conflict checklist complete before merge execution.

**G-3**: Combined-branch verification passes before testnet deploy.

**G-4**: Fresh-testnet verification passes all gates including intentional-tie vote-weight corpus.

**G-5**: No silent regressions — all F3-B invariants re-verified post-merge: specifically C-3' (refined), C-11 (unchanged), D-1 through D-8 (unchanged).

**G-6**: Documentation updates (design-principles.md, CLAUDE.md) committed to main post-merge.

---

## 10. Implementation sequencing within the workstream

Single integration branch `feat/selection-consistency-fix`. No merge to main until §11 passes.

**F4A phase (verification + foundations + cross-cutting):**

1. Part A.1: Cross-node byte-equality test harness.
2. Part A.2: Replay-conformance template.
3. Part A.3: Non-canonical monitoring (metric + CLI + alert).
4. Part B.1: `internal/jcs` unit tests.
5. Part B.2: `internal/store` unit tests.
6. Part B.3: Schema-migration discipline doc.
7. Part C: Locked-invariant review document committed.
8. Cross-cutting 8.3: No-bypass lint verification/implementation.
9. Cross-cutting 8.1: Replay path wiring (`replayHistoricalToBusConsumers`).
10. Part E Priority 1: Map-iteration fixes in consumers/dispatcher/settlement.
11. Part E Priority 2-3: Rest of map-iteration audit.
12. Part E CI lint implementation.

**Internal gate F4A→F4B: architect session reviews F4A before F4B begins.**

**F4B phase (protocol fix):**

13. Cross-cutting 8.4: cmd/node registration audit.
14. Part D.0: `LogicalKeyConsumer` interface + dispatcher extensions.
15. Part D.1: TVConsensus migration to logical-key admission.
16. Part D.2: Settlement migration.
17. Part D.3: TaskSettlement migration.
18. Part D.4: SettlementConsumer refactor.
19. Part D.5: Integration tests under Part A harness.
20. Part F: Partial-failure analysis + targeted fixes.
21. Cross-cutting 8.2: Schema-version enforcement verification.

**Internal gate F4B→F4C: architect session reviews F4B before F4C begins.**

**F4C phase (integer merge + testnet + main merge):**

22. Part G.1: Merge conflict checklist.
23. Part G.2: Integer-migration branch merge.
24. Part G.3: Combined-branch verification.
25. Part G.4: Fresh testnet deploy + verification.
26. Part G.5: Documentation updates.
27. Part G.6: Merge to main.

---

## 11. End-to-end success criteria

All must be true before merge to main:

1. `go test -race -count=3 ./...` passes with zero failures across full repo.
2. `go test -race -count=100 ./internal/jcs/` passes (determinism stress).
3. Every dispatcher consumer passes type-specific conformance suite including new replay-conformance template.
4. Every dispatcher consumer passes structural validation at startup.
5. No-bypass CI lint operational and passing: no canonical consumer wired directly to fabric.
6. Map-iteration CI lint operational and passing: no production `range over map` without classification or sort.
7. `internal/jcs` test ratio ≥ 2.0.
8. `internal/store` test ratio ≥ 0.5.
9. Map-iteration audit complete; every callsite classified.
10. Schema-migration discipline document complete; every persisted-record type covered.
11. Partial-failure analysis document complete; gaps either fixed or scheduled.
12. Locked-invariant review document complete; every F3-B invariant retained-with-reasoning or refined-with-reasoning.
13. cmd/node registration audit complete and reviewed.
14. Performance non-regression gate passes: no material dispatcher latency regression vs pre-Part-D baseline.
15. Live freshly-wiped 5-node testnet verification:
    - 10 back-to-back accept-path tasks: cross-node byte-equality on all ledger state.
    - 10 back-to-back reject-path tasks: cross-node byte-equality.
    - 10 tasks with intentionally-constructed vote-weight ties: cross-node byte-equality.
    - 10 mixed accept/reject tasks: cross-node byte-equality.
16. Live testnet replay test: restart one node with populated DAG, verify all bus consumers fire for historical events via replay path.
17. Live testnet continuous-monitoring test: run cluster ≥ 1 hour, zero false-positive divergence alerts, correct detection when divergence intentionally injected.
18. Live testnet content-hash test (from F3-B §10-11): byte-identical events via two paths deduplicate correctly.
19. Live testnet prerequisite-forgery test (from F3-B §10-12): forged prerequisites fail-fast.
20. Live testnet deferral-escalation test (from F3-B §10-13): `PrerequisiteWithholding` emits per spec.
21. Integer-migration activation: 5/5 nodes apply cleanly; flags flip cluster-wide; post-activation 10-task corpus shows zero `shadow_delta` and cross-node byte-equality.
22. Integer-migration restart test: single-node restart, flags restored from store, cluster converges.
23. Combined-branch merge conflict checklist complete; all conflicts resolved per checklist; no off-checklist conflicts.
24. `design-principles.md` and `CLAUDE.md` updated with F4 primitives; committed to main post-merge.
25. Founder approval on locked v2 (post multi-AI review).

---

## 12. Out of scope

Named with owning follow-on workstream:

- **`cmd/node/main.go` decomposition** → Workstream F5.
- **`internal/api/server.go` decomposition** → Workstream F5 or F6.
- **Security posture audit** → Workstream F7 (separate architect session).
- **Rolling-upgrade protocol design** → Workstream F8.
- **Operator runbook** → Workstream F9.
- **SDK test suite expansion** → Workstream F10.
- **Float-path excision** (legacy settlement path removal) → Workstream F11, scheduled after integer migration stable ≥ 1 month post-merge.
- **Reputation Step 4 evidence store** → Resumes after this workstream merges.
- **Trajectory integration** → Sequenced after Reputation Step 4.
- **Challenge path** → Sequenced after trajectory.
- **Slashing logic for `PrerequisiteWithholding`** → Follow-on workstream.
- **Post-commit selection-conflict handling** → Not needed under the winner-less design; if future analysis proves otherwise, dedicated architect session.
- **Canonical-event treatment for observer-layer artifacts** (CrossNodeDivergence, etc.) → Not needed; if future analysis argues otherwise, dedicated architect session.
- **Reconciliation of Part F retry's historical divergent state** → Forward-only per §0.2.
- **Mainnet planning** → Separate workstream.

---

## 13. Future-consumer taxonomy extensions

F3-B §12 defined Type A/B/C/D. F4 extends:

- **Type A (single-event projection)**: unchanged. Content-hash admission.
- **Type B (multi-event state-machine)**: unchanged. Content-hash admission.
- **Type C (externalization)**: unchanged. Content-hash admission.
- **Type D (deadline/deferred)**: unchanged. Content-hash admission.
- **Type E — Consensus-representation consumer (NEW)**: Logical-key admission. Implements `LogicalKeyConsumer` interface (`Key`, `IsComplete`, `DeriveOutcome`, `Apply`). Canonical outcome derived from round state (votes, attestations), not from triggering event's payload. Apply fires once per logical-key value. No winner selection, no superseded state. TVConsensus, Settlement, TaskSettlement consumers are Type E.

**Type E invariants**:
- Must declare logical-key admission at registration.
- Must implement `LogicalKeyConsumer` with all four methods.
- `IsComplete` must be pure deterministic on canonical state.
- `DeriveOutcome` must be pure deterministic on canonical state.
- Apply fires exactly once per logical-key value.
- No canonical state derived from triggering event's advisory fields.

---

## 14. Sign-off conditions

Plan v2 sign-off sequence:

1. **Architect-session review** (this step, complete) — v2 produced from v1 + Grok review + ChatGPT review.
2. **Founder approval** of plan v2.
3. **Claude Code plan-mode review** before implementation begins.
4. **Implementation** per §10 sequencing.
5. **F4A internal gate**: architect review after F4A phase complete.
6. **F4B internal gate**: architect review after F4B phase complete.
7. **F4C execution**: integer-migration merge + testnet + main merge.

If implementation discovers a structural issue not anticipated in this design, work pauses and architect session revises before continuing. No silent deviations from locked plan.

---

**End of Plan v2.**