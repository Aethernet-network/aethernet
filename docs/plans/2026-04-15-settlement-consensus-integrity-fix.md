# Settlement Consensus Integrity Fix — Locked Design v3-final

**Workstream**: F3-B fix.

**Status**: Locked v3-final. Synthesis of v2 + Grok round 2 + ChatGPT round 2 + ChatGPT round 3 textual tightenings. ChatGPT round 3 explicitly marked v3 as "architecturally sound and ready for founder approval after minor text tightening" with eight specific edits, none requiring redesign. All eight applied below. No further multi-AI review rounds before implementation. Founder approval recorded; Claude Code implements per §9 sequencing.

**Configurable thresholds**: `DeferralComplaintThreshold = 30 epochs`, `DeferralFailoverThreshold = 100 epochs`. Tune from observed testnet behavior.

**What changed from v3** (one paragraph for orientation): C-11 narrowed from "all consumer effects in one transaction" to "authoritative local durable commit," with settlement called out specifically; conformance templates run in CI, not at startup (startup performs structural validation only); prerequisite-forgery defense restated as a property-based DAG-reachability rule rather than a specific full-traversal implementation; slashing language replaced with "evidence-based escalation" since slashing is out of scope per §11; C-1 wording clarified to "exactly-once successful application"; DAG anchor identity defined plainly; admission state explicitly declared as node-local machinery, not consensus state; schema-version mismatch operator path clarified to exclude any canonical ledger rollback.

---

## 0. Decisions locked, not open for re-debate

(Carried forward from v2 §0 and v3 §0.)

1. F3-B is a strict principle 5 violation. Non-negotiable to fix.
2. Forward-only. No rollback of historical divergent state. The 2026-04-11 first-observed accept verdict's cross-node convergence gap is recorded in `docs/lessons.md` as historical; the three known divergent tasks (`a2b588c8…`, `b2f96181…`, `52c5b97a…`) are annotated per Part F.
3. F1 and F3-B fixes ship together as one workstream.
4. Reputation Step 4 paused until this workstream ships and verifies end-to-end on testnet.
5. Testnet wipe-without-ceremony is the standard.
6. All parts ship together. No partial deploys. No interim release candidates. **No merge to main until end-to-end testnet verification passes.**
7. Architectural choice: Direction 3 — `CanonicalEventDispatcher` primitive between fabric and consumers.
8. **Atomic-batch settlement.** All ledger mutations for a single canonical settlement event execute within a single BadgerDB transaction. Partial-application is structurally impossible.
9. **Single mandatory replay-state registry.** All replay-derived persisted node state registers in one mandatory startup inventory. Subcategories may exist internally; the enforcement surface is singular.
10. **No-bypass CI/lint rule.** No canonical-event consumer may be wired directly to the fabric or local-publish path. CI fails the build on bypass wiring.

---

## 1. The load-bearing framing

The bug has three layers and the fix has six parts plus cross-cutting items.

**Layer 1 — F1 double-debit.** `internal/settlement/applicator.go:306–315` invokes `escrow.Hold` after the canonical Transfer event has already moved funds. `escrow.Hold` conflates "register metadata" + "transfer funds" so funds get moved twice.

**Layer 2 — F3-B double-settlement race.** `TaskVerificationConsensusConsumer` receives the same canonical event via two paths on producer nodes (local publish + recognition fabric). The current `round.IsTerminal()` guard fails to deduplicate. Settler runs twice on producer nodes, once on peer nodes, divergent ledger state.

**Layer 3 — restart-and-replay state loss.** `Applicator.LoadApplied` and `Escrow.LoadFromStore` exist but are never called at startup.

**Layer 4 — causal prerequisite gating.** Even with exactly-once admission, settlement may fire on a node before its causally prior canonical events are projected locally. Closed by Part D.

The six parts:

- **Part A**: Wire `Applicator.LoadApplied` at startup.
- **Part B**: Wire `Escrow.LoadFromStore` at startup.
- **Part C**: Build the `CanonicalEventDispatcher` primitive (Direction 3).
- **Part D**: Causal prerequisite gating with DAG-validated `Prerequisites`.
- **Part E**: Escrow API hardening with module-boundary quarantine.
- **Part F**: Historical task annotation, machine-consumable.

Plus cross-cutting items: `esc:` flag fix, synthetic transfer relabeling, "Settled to Settled" warning verification.

---

## 2. Part A — `Applicator.LoadApplied` startup wiring

### 2.1 What changes

In `cmd/node/main.go`, after the applicator is constructed and **before any goroutine that listens to the recognition fabric is started**:

```go
if err := applicator.LoadApplied(ctx); err != nil {
    return fmt.Errorf("startup: applicator.LoadApplied failed: %w", err)
}
```

Followed by DAG-tip verification per §4.6.

### 2.2 Invariants

**A-1 (Load-before-listener).** `LoadApplied` completes successfully before any code path that can deliver canonical events to consumers begins execution.

**A-2 (Fail-startup on error).** `LoadApplied` returning an error causes node startup to fail with a clear diagnostic.

**A-3 (Sync semantics on writes).** Every write to the applied-set is committed with BadgerDB's Sync option enabled.

**A-4 (DAG-anchored verification).** After `LoadApplied` completes, the node verifies that the loaded applied-set's DAG anchor matches the persisted DAG tip. Mismatch fails startup. Closes the corruption-attack surface.

### 2.3 Verification

- Restart test: stop a node mid-flow after several settlements have applied; restart; assert previously-applied settlements are not re-applied and the node converges with peers.
- Crash-during-load test: kill a node mid-`LoadApplied`; restart; assert node either completes the load successfully or fails-startup, never silently boots with partial state.
- Corruption test: write garbage entries to the applied-set BadgerDB prefix; restart; assert startup fails with the DAG-anchor mismatch diagnostic.

---

## 3. Part B — `Escrow.LoadFromStore` startup wiring

### 3.1 What changes

Paired with Part A in `cmd/node/main.go`:

```go
if err := escrow.LoadFromStore(ctx); err != nil {
    return fmt.Errorf("startup: escrow.LoadFromStore failed: %w", err)
}
```

Same load-before-listener ordering, same Sync semantics, same DAG-anchor verification.

### 3.2 Invariants

**B-1 through B-4** mirror A-1 through A-4 for the escrow registry.

### 3.3 Verification

Same shape as Part A's tests, applied to the escrow registry.

---

## 4. Part C — `CanonicalEventDispatcher` primitive

### 4.1 The architectural shape

A new shared package `internal/dispatch/` provides the `CanonicalEventDispatcher` primitive. The dispatcher sits between the recognition fabric and all canonical-event consumers. Consumers register with the dispatcher; the dispatcher owns the durable admission state; the dispatcher invokes each consumer exactly once per canonical event regardless of how many delivery paths the fabric used.

The fabric continues to deliver via local-publish and recognition-fabric paths as it does today. The dispatcher absorbs the duplication at a single architectural choke point.

### 4.2 The admission-state machine

Five states, persisted in BadgerDB:

```
absent  → reserved-pending-prerequisites  → processing  → applied
                                                       \→ failed-retryable
```

- **absent**: no record exists. The event has not been admitted.
- **reserved-pending-prerequisites**: identity reserved but causal prerequisites not yet projected locally. Distinct from `processing` because the event is waiting, not invoking consumers. Crash recovery treats this state as "not suspicious — re-check prerequisites on restart."
- **processing**: prerequisites satisfied and dispatcher is invoking consumers. Persisted before consumer invocation begins. Crash recovery treats this state as "suspicious — invoke `RecoveryProbe` for each consumer."
- **applied**: all registered consumers reached per-consumer `applied` status. Top-level transition is computed from per-consumer states.
- **failed-retryable**: at least one consumer in per-consumer `failed-retryable`. Eligible for re-admission on next delivery; per-consumer status preserves which consumers succeeded.

### 4.3 The admission key — content-hash, not event-ID

The dispatcher's admission key is the BLAKE3 hash of the canonical-serialized event bytes:

```
dispatch-admission:<blake3(canonicalize(event))>
```

**Canonicalization happens before the admission transaction is opened.** The transaction uses the precomputed key:

```go
key := dispatchKey(canonicalize(event))  // outside transaction
err := db.Update(func(txn *badger.Txn) error {
    // Inside transaction: only key usage and minimal record I/O.
})
```

Content-hash keys close the byte-identical-event-with-different-serialization attack: two events with any byte difference produce different keys; two events with identical canonical bytes produce identical keys regardless of transport metadata.

### 4.4 Canonical serialization uniqueness

**Invariant Serialization-1.** For any canonical event, there exists exactly one valid canonical serialized byte representation. Semantically identical events with different canonical bytes are forbidden by the serialization layer.

This is a property of the protocol's canonicalization layer, not the dispatcher. The dispatcher relies on it explicitly so that any future violation is caught as a protocol-level invariant breach rather than a dispatcher bug.

### 4.5 Atomic-batch settlement

The settlement consumer executes all of its ledger mutations for a single canonical event within a single BadgerDB transaction. Either all writes commit or none do. Partial-application is structurally impossible for the settlement consumer.

Constraint: the atomic-batch must remain bounded in size. A single settlement event involves a fixed small number of ledger writes. This is well within BadgerDB transaction limits. Other consumer types satisfy their atomic-commit invariant differently; see C-11 below.

### 4.6 DAG-anchor verification on every admission

Every admission check-and-set verifies the dispatcher's stored DAG anchor against the live DAG tip read from persisted DAG state, not just on startup:

```go
storedAnchor := state.DAGAnchor
liveAnchor := dag.CurrentTipID(txn)
if !validAnchorRelationship(storedAnchor, liveAnchor) {
    return errCorruptedAdmissionState
}
```

`validAnchorRelationship` confirms the stored anchor is an ancestor of (or equal to) the live tip. Closes the live-node corruption attack surface.

The DAG anchor itself is defined plainly in C-13 below.

### 4.7 Per-(event, consumer) completion tracking

Identity reservation is per-event. Completion is per-(event, consumer). The admission record stores a map of consumer-name to per-consumer status:

```
admission-record {
    state: <one of: reserved-pending-prerequisites | processing | applied | failed-retryable>
    dag-anchor: <canonical content-addressed identifier of DAG tip at reservation>
    prerequisite-schema-version: <version per D-6>
    consumers: {
        "settlement.SettlementConsumer": <per-consumer status>
        "reputation.EvidenceConsumer": <per-consumer status>
        ...
    }
}

per-consumer status: <one of: pending | applied | failed-retryable>
```

The admission record's top-level state advances to `applied` only when every consumer in the map has per-consumer status `applied`. Any consumer in `failed-retryable` causes top-level `failed-retryable`. Per-consumer recovery probes operate independently on per-consumer status, so transient failure in one consumer does not require re-running consumers that already succeeded.

Multi-consumer admission semantics: when the dispatcher invokes consumers for an event, it iterates the consumer map. Successful consumers transition to per-consumer `applied`; failing consumers transition to per-consumer `failed-retryable`. Top-level state is computed from per-consumer states. On the next delivery for the same event, only consumers in `failed-retryable` are re-invoked; consumers in `applied` are skipped.

This closes the reputation Step 4 desync surface: settlement-applied + evidence-missing is structurally impossible. Either both consumers reach per-consumer `applied`, or one stays in `failed-retryable` and the next delivery re-runs the failed consumer until success.

### 4.8 Crash-recovery semantics

A node that crashes mid-admission may leave a record in `reserved-pending-prerequisites` or `processing`. On restart:

**For records in `reserved-pending-prerequisites`:** Re-check prerequisites. If still missing, leave in `reserved-pending-prerequisites`. If now satisfied, transition to `processing` and proceed.

**For records in `processing`:** For each consumer with per-consumer status `pending`, call the consumer's `RecoveryProbe(ctx, event) (status RecoveryStatus, err error)`.

- The probe may return `completed` only if downstream completion is positively evidenced by durable canonical or projection state. Absence of evidence is not evidence of absence.
- The probe must not consult wall-clock time, ephemeral memory, or external systems of record. It must be replay-safe.
- `completed` → per-consumer status `applied`.
- `not-started` → per-consumer status `failed-retryable`; next admission re-attempts.
- Probe error or detected partial state (which atomic-batch should make impossible) → fail-startup with manual-intervention diagnostic.

**For records in `failed-retryable`:** No restart action. Next delivery re-attempts admission for consumers in `failed-retryable` per-consumer status.

### 4.9 Storage-error fail-closed

If the BadgerDB transaction returns any error other than `ErrKeyNotFound`, the dispatcher refuses to invoke the consumer. Stalled is recoverable; silent skip is not.

### 4.10 Conformance test suite — CI-enforced, not startup-enforced

Every consumer registering with the dispatcher must pass a shared conformance test suite **in CI**, before the binary is built. The dispatcher's `Register(consumer)` API at startup performs **structural validation only**: declared type, schema version, required interfaces present (`Prerequisites`, `RecoveryProbe`, `PrerequisiteSchemaVersion`, outbox hooks where applicable per type), registry metadata complete. Structural validation failure aborts startup with a diagnostic.

The full behavioral suite is too expensive and too brittle to run at every node startup. CI is the enforcement surface for behavioral correctness; startup is the enforcement surface for structural correctness. Both layers are mandatory; neither substitutes for the other.

Per-consumer-type templates in CI:

- **Type A (single-event projection)**: 6 baseline tests (duplicate live delivery, replay delivery, crash recovery, concurrent same-event delivery, content-hash discrimination, causal-prerequisite deferral).
- **Type B (multi-event state-machine)**: Type A tests + state-machine transition tests + multi-event coordination tests.
- **Type C (externalization)**: Type A tests + outbox-pattern tests + idempotent-sink tests.
- **Type D (deadline/deferred)**: Type A tests + deadline-basis-canonical tests.

Conformance suite lives in `internal/dispatch/conformance/`. Each consumer's package imports the suite and runs it in CI against the consumer's actual implementation.

### 4.11 No-bypass CI/lint rule

A CI check fails the build if any canonical-event consumer is wired directly to the recognition fabric or local-publish path instead of registered through the dispatcher. The check uses static analysis on the type graph: any type that implements the consumer interface and subscribes to fabric events without going through `dispatch.Register` triggers the failure.

The lint runs as part of the standard `go test ./...` invocation, not as a separate optional check. Same enforcement model as the projection-registry lint from step 3 of the reputation workstream.

### 4.12 Single mandatory replay-state registry

The projection registry (`internal/projections/`) is extended to cover all replay-derived persisted node state, not just state projections. Subcategories: projections, dispatcher admission records, escrow registries, applicator applied-sets. All four register in the same mandatory inventory.

The startup health check from step 1 of the reputation workstream is extended to verify all registered state is loaded successfully and DAG-anchored where applicable.

### 4.13 Invariants

**C-1 (Exactly-once successful application).** For every canonical event, each registered consumer reaches per-consumer `applied` status at most once across the lifetime of the node. Retries of consumers in `failed-retryable` are permitted until success; consumers already in per-consumer `applied` are never re-invoked.

**C-2 (Atomic reservation).** The check-and-set at admission is atomic.

**C-3 (Content-hash identity).** Admission keys derive from BLAKE3 of canonical-serialized event bytes. Canonicalization is performed before the transaction; key usage is inside.

**C-4 (Five-state lifecycle).** Admission state is `absent | reserved-pending-prerequisites | processing | applied | failed-retryable`. Boolean-only state is forbidden.

**C-5 (Reservation transaction minimality).** The admission transaction performs only the read-and-write of the admission record. Settlement, prerequisite checks, and recovery probes run outside the transaction.

**C-6 (DAG-anchored, every admission).** Every admission check-and-set verifies the stored anchor against the live DAG tip, not just at startup.

**C-7 (Storage-error fail-closed).** Any BadgerDB error other than `ErrKeyNotFound` causes the dispatcher to refuse invocation.

**C-8 (Conformance in CI; structural validation at startup).** Behavioral conformance suites run in CI per consumer type. Startup performs structural validation: type declared, schema version present, required interfaces present, metadata complete. Structural validation failure aborts startup. Behavioral suite failure fails the build.

**C-9 (Single mandatory replay-state registry).** All replay-derived persisted node state registers in the projection registry's extended inventory. Subcategories may exist; enforcement is singular.

**C-10 (No-bypass).** No canonical-event consumer is wired directly to the fabric. CI lint enforces.

**C-11 (Atomic local commit).** For any dispatcher consumer, the consumer's authoritative local durable state transition for a single canonical event must commit atomically. The settlement consumer satisfies this by executing all ledger mutations within a single BadgerDB transaction. Type C externalization consumers satisfy this by atomically writing to a local outbox/journal — external side effects are not made transactional with BadgerDB. Each consumer type's atomic-commit shape is defined in §12.

**C-12 (Per-(event, consumer) completion).** Identity reservation is per-event; completion is per-(event, consumer). Top-level admission state is computed from per-consumer states.

**C-13 (DAG anchor identity).** The stored DAG anchor in replay-derived records is the canonical content-addressed identifier of the node's persisted DAG tip at reservation/load time. It is not an ad-hoc hash of mutable local metadata. The same identity scheme is used uniformly across dispatcher admission records, applicator applied-sets, and escrow registries.

**C-14 (Recovery probes are evidence-based, monotonic, replay-safe).** Probes return `completed` only with positive evidence in durable state. Absence of evidence is not completion. Probes may not consult wall-clock, ephemeral memory, or external systems.

**C-15 (Admission state is non-canonical node-local machinery).** Dispatcher admission records are replay-derived local machinery used to guarantee deterministic projection. They are not consensus objects and need not match byte-for-byte across nodes; only resulting projected canonical effects must converge cross-node.

**C-16 (Future-consumer inheritance).** Future canonical-event consumers register with the dispatcher per the §12 type taxonomy and inherit admission semantics structurally.

**Invariant Serialization-1 (re-stated for visibility).** Canonical serialization is unique. Two semantically-equivalent canonical events with different byte representations are forbidden by the serialization layer.

---

## 5. Part D — Causal prerequisite gating

### 5.1 The structural hole

Even with exactly-once admission, settlement may fire on a node before its causally prior canonical events are projected locally. Principle 5 requires deterministic projection from the DAG, which requires consumers to either process events in causal order or defer until prerequisites are satisfied.

### 5.2 What changes

The dispatcher's admission path checks causal-readiness before invoking consumers. For every canonical event admitted:

1. Call the consumer's `Prerequisites(event) []EventID`.
2. Validate the returned `EventID`s against the DAG (per D-4 below).
3. Verify every required predecessor is projected in local state.
4. If all prerequisites projected: transition to `processing`, invoke consumer.
5. If any prerequisite missing: transition to `reserved-pending-prerequisites`, defer.
6. Re-attempt admission when any new event projection completes that might satisfy missing prerequisites.

### 5.3 Prerequisite forgery prevention

`Prerequisites` is consumer-defined; its return value is protocol-validated by the dispatcher. Every `EventID` returned must be a valid causal prerequisite of the triggering event, derivable from the DAG.

The validation property (not the implementation):

**D-4 (Prerequisite forgery rejected).** Every prerequisite returned by a consumer must be validated as DAG-reachable from the triggering event and consistent with the event's declared semantic dependency class. Validation may be implemented using indexed ancestor checks or equivalent DAG proofs; full ancestor traversal on every admission is not required. A consumer that returns an `EventID` not satisfying the property is structurally rejected — the dispatcher does not defer; it fails admission with a diagnostic identifying the consumer and the invalid `EventID`.

Closes the dispatcher self-stall blind spot: a byzantine producer cannot craft an event whose prerequisites are impossible because the dispatcher validates the prerequisites against the DAG before deferring. Forged prerequisites fail-fast; honest events with unmet prerequisites defer and eventually proceed.

Implementation note (non-binding): the most efficient path is likely an indexed DAG-ancestor map maintained by the canonicalization layer, with prerequisite-validation being a lookup against the index rather than a traversal. Implementation chooses the path; the property is what locks.

### 5.4 Evidence-based deferral escalation

The v2 hard 100-epoch deferral bound was gameable by sustained byzantine prerequisite withholding. v3-final replaces it with evidence emission:

When a node has held an event in `reserved-pending-prerequisites` for more than `DeferralComplaintThreshold` epochs (locked at 30), the node emits a `PrerequisiteWithholding` evidence event identifying the missing prerequisites and the node's stuck admission record. The evidence is canonical and propagates via the DAG.

When a node has held an event for more than `DeferralFailoverThreshold` epochs (locked at 100), the node may fail-startup if it appears to be the only node in this state, or trigger network-level recovery if the failure is widespread. Threshold values are configurable in `internal/dispatch/config.go`; units are canonical epochs from the round counter primitive.

**D-5 (Evidence-based escalation).** Nodes stuck in `reserved-pending-prerequisites` beyond `DeferralComplaintThreshold` emit canonical `PrerequisiteWithholding` evidence. Penalty/slashing semantics are out of scope for this workstream and are implemented by a follow-up consumer (see §11). The emitted evidence is designed for later consumption by a slashing/challenge-path consumer.

### 5.5 Prerequisite semantics versioning

Consumer registration includes a `PrerequisiteSchemaVersion uint32` field. The admission record stores this version at reservation time. On replay or recovery under upgraded consumer logic, the dispatcher refuses to advance an admission record whose stored version differs from the current consumer's version, and instead emits a diagnostic.

**D-6 (Prerequisite semantics versioned).** Consumer registration declares `PrerequisiteSchemaVersion`. Admission records store this version. Upgrade-time mismatch triggers operator-action diagnostic, not silent reinterpretation.

**D-6 refinement (operator-action path).** On schema-version mismatch, startup aborts with a diagnostic identifying the in-flight admission records. Operator resolution consists of completing those records under the old binary or clearing only non-applied local admission machinery after verifying no canonical effects were committed. **No canonical ledger rollback is implied.** "Clearing" applies only to the local dispatcher admission state, never to the canonical ledger or any other consensus-derived state.

### 5.6 Invariants

**D-1 (Causal-readiness gate).** A canonical settlement-triggering event may only invoke side-effecting consumer logic when all causally required predecessor events are projected in local state.

**D-2 (Deterministic deferral).** Events with unsatisfied prerequisites are deferred in a replay-deterministic way. Deferral decisions are a pure function of local projection state at the time of admission.

**D-3 (No arrival-order dependence).** Correctness of consumer invocation may not depend on whether prerequisite events or the triggering event arrived first.

**D-4 (Prerequisite forgery rejected).** As stated in §5.3.

**D-5 (Evidence-based escalation).** As stated in §5.4. Slashing is out of scope; only evidence emission is in scope for this workstream.

**D-6 (Prerequisite semantics versioned).** As stated in §5.5, including the operator-action path refinement.

**D-7 (Deferral bounds in canonical units).** `DeferralComplaintThreshold` and `DeferralFailoverThreshold` are expressed in canonical epochs (from the round counter), not wall-clock units.

**D-8 (Prerequisites checked outside reservation transaction).** Prerequisite checks do not execute inside the admission BadgerDB transaction. The transaction reserves identity; prerequisites are checked between reservation and consumer invocation.

### 5.7 Verification

- Test: deliver a `TaskVerificationConsensus` event before its `TaskPosted` is projected. Assert `reserved-pending-prerequisites`. Project `TaskPosted`. Assert admission re-attempts and consumer is invoked.
- Test: deliver an event with a forged prerequisite (not DAG-reachable from the triggering event). Assert dispatcher fails admission with diagnostic, does not defer.
- Test: defer an event for `DeferralComplaintThreshold` epochs; assert `PrerequisiteWithholding` evidence is emitted.
- Test: register a consumer with `PrerequisiteSchemaVersion: 1`. Reserve an admission. Re-register with version 2. Assert dispatcher refuses to advance the version-1 admission and emits the operator-action diagnostic.
- Test: deliver events out of order across a partition simulation; assert all nodes converge on identical final ledger state regardless of arrival order.

---

## 6. Part E — Escrow API hardening

### 6.1 The structural change

`escrow.Hold` is replaced in production by `RegisterEscrow`. The combined fund-and-register helper (formerly `HoldForTest`) is renamed `FundAndRegisterEscrowForTest` and lives in a separate Go module structurally not importable from production packages.

```go
// internal/escrow/escrow.go (production package)
func (e *Escrow) RegisterEscrow(taskID, poster string, amount uint64, fundingTransferRef EventID) error {
    // Metadata only. Records the canonical Transfer that funded the escrow.
    // Validates fundingTransferRef against the DAG.
    // Does not move funds under any code path.
}
```

```go
// internal/escrow_testhelpers/helpers.go (separate Go module)
package escrow_testhelpers

func FundAndRegisterEscrowForTest(...) error {
    // Combined: transfer + register. Test-only.
    // Production packages cannot import this module.
}
```

The separate-module approach is structurally stronger than a build tag. A build tag is a CI-pipeline convention; a separate module is a Go module-system constraint requiring explicit dependency declaration. The production binary's `go.mod` does not list the test-helpers module, so production code cannot import it.

CI verifies with two checks:
1. `go list -deps ./cmd/node` does not include the test-helpers module.
2. Static analysis fails the build if any production package imports the test-helpers module path.

### 6.2 Funding-evidence reference, validated

`RegisterEscrow` records and validates the `EventID` of the canonical Transfer that funded the escrow:

- `fundingTransferRef` must resolve to a canonical `Transfer` event in the DAG.
- The `Transfer` event's amount and counterparties must match the escrow registration's amount and poster.
- If validation fails synchronously (the `Transfer` is not yet projected locally), `RegisterEscrow` returns an error; the calling consumer treats this as a prerequisite failure and is deferred per Part D.
- If validation succeeds, registration completes.

### 6.3 Repo-wide audit of `Hold` callers

Pre-implementation work item: audit every existing call site of `escrow.Hold`. For each, classify as:

- **Production-must-switch-to-RegisterEscrow**: production paths where the canonical Transfer has already moved funds. Switch the call.
- **Test-must-switch-to-FundAndRegisterEscrowForTest**: test fixtures and harnesses needing combined behavior. Switch the call and move to the test-helpers module.
- **Unknown**: requires investigation. Block until classified.

The audit produces a checklist that implementation cannot proceed without.

### 6.4 Invariants

**E-1 (Production cannot call combined helper).** `FundAndRegisterEscrowForTest` lives in a Go module not imported by production. CI verifies the production binary's dependency tree excludes it.

**E-2 (RegisterEscrow records and validates funding).** Every `RegisterEscrow` call records the `EventID` of the canonical Transfer and validates it against the DAG.

**E-3 (No ambiguous convenience).** Production settlement and applicator code calls only `RegisterEscrow`. Module boundary enforces.

**E-4 (Funding validation).** `RegisterEscrow` rejects nonexistent or non-Transfer references. Synchronous when possible; otherwise via Part D prerequisite gating.

### 6.5 Verification

- Build: `go list -deps ./cmd/node` does not include the test-helpers module.
- Static analysis: production package imports of test-helpers module path fail the build.
- Funding-evidence: run a fresh task on testnet, query escrow registry, assert `FundingTransferRef` is non-empty and matches the `Transfer` event in the DAG.
- Validation test: call `RegisterEscrow` with nonexistent `EventID`. Assert error.

---

## 7. Part F — Historical task annotation, machine-consumable

### 7.1 What changes

A two-file annotation pair:

- `docs/historical/divergent-tasks.md` — human-facing markdown describing the three pre-fix divergent tasks with context and reasoning.
- `docs/historical/divergent-tasks.json` — machine-consumable JSON for analytics and observability tooling.

JSON schema:

```json
{
  "schema_version": 1,
  "anomalies": [
    {
      "task_id": "52c5b97a...",
      "anomaly_type": "cross-node-ledger-divergence",
      "first_observed": "2026-04-11T17:52:30Z",
      "audit_reference": "docs/audits/2026-04-XX-poster-fee-cross-node-consistency.md",
      "notes": "First-observed accept verdict. Cross-node ledger state divergence on settlement application."
    }
  ]
}
```

### 7.2 Invariants

**F-1 (Pre-fix anomaly identifiability).** The three divergent tasks are queryably identifiable as pre-fix anomalies in observability and analytics tooling. The annotation does not modify canonical state.

**F-2 (Machine-consumable).** The annotation is loadable by analytics tooling via the JSON file, not just human-readable in the markdown.

**F-3 (Off-canonical).** The annotation lives outside the node binary and outside any on-chain consumer's import path. On-chain consumers cannot import or derive logic from this annotation.

---

## 8. Cross-cutting items

### 8.1 `esc:` disbursement flag fix

In the settlement applicator, after payouts complete (within the atomic-batch transaction from §4.5), update the `esc:<taskID>` metadata entry to set `WorkerPaid`, `ValidatorPaid`, `TreasuryPaid` flags.

### 8.2 Synthetic transfer relabeling

Route synthetic `TransferFromBucket` entries through a dedicated path that sets correct metadata (`IsGenesis=false`, memo describing actual transfer purpose).

### 8.3 "Cannot transition from Settled to Settled" warning verification

After Parts A–F ship, verify via log inspection that this warning is absent on accept-path settlements.

### 8.4 Stage 3 vote-ingestion asymmetry — out of scope

Recorded as a follow-up audit, queued after this workstream and reputation Step 4 ship.

### 8.5 Content-hash identity surface

The content-hash identity decision from §4.3 is part of this workstream's cross-cutting correctness surface. Any future workstream that touches the canonicalization layer must verify Invariant Serialization-1 still holds.

---

## 9. Implementation sequencing within the workstream

All parts implemented on a single integration branch `feat/settlement-consensus-integrity-fix`. **No merge to main until end-to-end testnet verification per §10 passes.**

Internal commit ordering:

1. Repo-wide `escrow.Hold` caller audit (Part E precondition). Output is a checklist; no code changes.
2. Part E: `RegisterEscrow` with funding validation, `FundAndRegisterEscrowForTest` quarantined to separate Go module, build/CI assertions.
3. Update production callers from `Hold` to `RegisterEscrow` per the audit checklist.
4. Part C: `internal/dispatch/` package skeleton with five-state machine, content-hash keys, per-(event, consumer) completion tracking.
5. Part C: dispatcher implementation, conformance test suite per consumer type (CI), structural validation at startup, registry integration, no-bypass CI lint.
6. Part D: causal-prerequisite gating with DAG-validated prerequisites, evidence-based deferral escalation, schema versioning.
7. Part A: `Applicator.LoadApplied` wiring with DAG-anchor verification.
8. Part B: `Escrow.LoadFromStore` wiring with DAG-anchor verification.
9. Wire `TaskVerificationConsensusConsumer` into the dispatcher as a Type A consumer. Implement `Prerequisites`, `RecoveryProbe`, `PrerequisiteSchemaVersion`. Run conformance suite in CI and structural validation at startup.
10. Cross-cutting items: `esc:` flag fix (within atomic-batch), synthetic transfer relabeling.
11. Part F: historical task annotation document and JSON.
12. End-to-end testnet verification per §10.

---

## 10. End-to-end success criteria

All of the following must be true before this workstream is marked complete and reputation Step 4 resumes:

1. `go test -race ./...` passes with zero failures across the full repo.
2. Every consumer registered with the dispatcher passes the type-specific conformance suite **in CI**.
3. Every consumer passes structural validation at startup.
4. Build verification: production binary's dependency tree excludes the test-helpers module.
5. No-bypass CI lint passes: no canonical consumer is wired directly to the fabric.
6. Live testnet wipe-and-redeploy: post 10 back-to-back accept-path tasks, verify on every node:
   - Worker balance increased by exactly task_budget × 0.73 per task.
   - Validator pool balance increased by exactly task_budget × 0.23 per task.
   - Generation ledger balance increased by exactly task_budget × 0.02 per task.
   - Treasury balance increased by exactly task_budget × 0.02 per task.
   - Escrow bucket is empty post-settlement.
   - All 5 nodes show byte-identical full ledger state for each task across all four sink accounts.
7. Live testnet restart test: post a task, stop one node mid-flow, restart it, verify on restart no double-application and that the node converges with the rest of the fabric.
8. Live testnet replay test: simulate a producer-node restart between local-publish and consensus-finalization, verify the dispatcher correctly deduplicates the redelivered event on restart.
9. Live testnet partition test: induce a temporary partition during which one node misses a `TaskPosted` event, verify on partition heal that the node defers the corresponding `TaskVerificationConsensus` until the prerequisite arrives, and converges correctly.
10. Live testnet corruption test: write garbage entries to a node's admission-state BadgerDB prefix, attempt restart, verify the node fails startup with the DAG-anchor mismatch diagnostic.
11. Live testnet content-hash test: post a task with byte-identical event payloads delivered via two paths with subtly different transport metadata; verify the dispatcher recognizes them as the same canonical event and admits exactly once.
12. Live testnet prerequisite-forgery test: deliver an event with a forged prerequisite (not DAG-reachable); verify dispatcher fails admission with diagnostic, does not defer.
13. Live testnet deferral-escalation test: defer an event for `DeferralComplaintThreshold` epochs by withholding prerequisites; verify `PrerequisiteWithholding` evidence is emitted.
14. The "cannot transition from Settled to Settled" warning is absent from accept-path logs.
15. `esc:` metadata flags reflect actual disbursements after settlement.
16. Synthetic `txf:bucket:` entries are correctly labeled.
17. The `FundAndRegisterEscrowForTest` symbol does not appear in `grep` of the production binary or `go list -deps ./cmd/node`.
18. The historical task annotation document and JSON exist and identify the three pre-fix divergent tasks; the JSON validates against its schema.
19. Founder approval recorded on locked v3-final.

---

## 11. Out of scope

- Reputation Step 4 evidence store. Resumes after this workstream ships and verifies.
- Trajectory integration. Sequenced after reputation Step 4.
- Challenge path. Sequenced after trajectory integration.
- **Slashing logic for `PrerequisiteWithholding` evidence.** The evidence event is emitted by Part D; the slashing consumer is a follow-up workstream.
- Stage 3 vote-ingestion asymmetry. Separate follow-up audit.
- Reconciliation of historical divergent state on the three pre-fix tasks. Forward-only; annotation only.
- Validator onboarding economics. Different workstream.
- Mainnet planning.

---

## 12. Future-consumer taxonomy

- **Type A — Single-event projection consumer.** Uses dispatcher admission only. Authoritative local durable commit is a single BadgerDB transaction (per C-11). Reputation Step 4 evidence store is Type A. Settlement consumer is Type A.
- **Type B — Multi-event state-machine consumer.** Uses dispatcher admission plus an explicit state-machine table defining legal transitions across multiple canonical events. **Invariant 8.2: multi-event consumers must define legal transition tables explicitly. Exactly-once admission per event does not substitute for a state machine.** Authoritative local commit per event is atomic; cross-event state is governed by the state machine. Challenge path is Type B.
- **Type C — Externalization consumer.** Uses dispatcher admission plus a local outbox/journal pattern with idempotent external sink. **Invariant 8.1: external sinks are never the authority. The dispatcher guarantees exactly-once admission into local durable node state only. External publication must be driven from a local outbox/journal and be idempotent.** The C-11 atomic-commit invariant is satisfied by atomic write to the outbox; external write is non-transactional with respect to BadgerDB. Trajectory integration and data ingestion are Type C.
- **Type D — Deadline/deferred consumer.** Records canonical deadline basis in admission state; timers are not canonical truth. Dispute deadline consumers are Type D.

---

## 13. Sign-off conditions

This is locked v3-final. The next gate is founder approval. After founder approval, Claude Code implements per §9 sequencing. The integration branch does not merge to main until §10 end-to-end testnet verification passes. Reputation Step 4 resumes after merge.

If implementation discovers a structural issue not anticipated in this design, work pauses and the design is revised before implementation continues. No silent deviations from the locked design.
