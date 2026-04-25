# Canonical Epoch Event — sub-spec

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5B — sub-spec for the canonical-epoch primitive absorbed into 5B per secondary-halt resolution.
**Status**: v2.3 — post-breakpoint-C precision patch (one-sentence layer-separation clarification; no design change). All v2.2 substance preserved. Sub-spec implementation complete across breakpoints A/B/C; ready for sub-spec completion gate.
**Branch base**: `51bce89` (F4-frozen) + uncommitted 5A.4 working tree.
**Date**: 2026-04-24

**Prior art**:
- F5 Phase 5B Plan v3: `docs/plans/implementation/f5-phase-5b-plan-v3.md` (§0 decision 2 amended to absorb this scope)
- F5 5A.2 Q-score canonicalization: `docs/architecture/q-score-canonicalization-design.md` §11 (cutoff semantics)
- Locked Reputation-and-Consensus-Integrity workstream: `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` (real-W implementation; epoch-keyed)
- Journal entry 2026-04-24 (Option 3b lock; secondary halt)

---

## 0. Decisions locked before drafting

1. **Option 3b locked by founder ruling.** New canonical DAG event type `EpochBoundary`; epoch is computed from the count of EpochBoundary ancestors of a round's canonical seal context, canonical by construction. Alternative options are out of scope: Option 1 (serialize bus) and Option 4 (emitter-trusted payload epoch) rejected; Option 2 (single-flight queue) not viable because the commit bus's live-path delivery is not canonically ordered per the dag.Add post-commit hook semantic (`internal/dag/dag.go:230-237` — hook fires outside the DAG write lock).

2. **Plan v3 §0 decision 2 amended.** F5 5B was spec'd as "5A primitives consumed, not re-designed." Implementation surfaced that the RoundCounter-as-canonical-sequence primitive is not canonical under concurrent dispatch. This sub-spec absorbs the work to close the gap.

3. **Canonical-by-construction stance.** Every part of this design must satisfy: two nodes processing the same canonical DAG content compute the same `epoch_of(R)` regardless of local admission order, worker-pool concurrency, or retry timing. The primitive the sub-spec adds must itself be free of the same defect the primitive it replaces exhibited.

4. **No counter-as-source-of-truth anywhere in the settlement path.** `epoch.RoundCounter` remains in the codebase as an observability primitive (projection registry, log lines) but its output is not load-bearing for any canonical computation. This sub-spec does not remove it; it removes its use in the canonical path.

---

## 1. Event type definition

### 1.1 Name + type registry

New canonical event type: `event.EventTypeEpochBoundary`. Registered in `internal/event/event.go` alongside existing types (`EventTypeTaskPosted`, `EventTypeTaskVerificationConsensus`, etc.). Numeric value assigned per existing enum convention — to be allocated at implementation time.

### 1.2 Payload shape

Minimal. The event's semantic content is in its identity + CausalRefs; payload exists for audit and version-gating.

```go
// EpochBoundaryPayload marks the canonical transition to a new epoch.
// One boundary per epoch-number; logical-key dedup ensures cluster
// convergence under multi-emit (§2).
//
// The boundary's canonical identity is carried by its content-hash
// EventID and its CausalRefs (which pin the canonical position).
// Payload fields are either canonical-frozen or redundant-with-content-
// hash — they do NOT introduce state the emitter can manipulate.
type EpochBoundaryPayload struct {
    Version uint8 `json:"v"` // schema version (= 1 at F5 ship); type+tag match the 18 existing canonical-payload precedent

    // Epoch is the epoch index this boundary opens. Epochs are numbered
    // starting at 1 — genesis state (no boundaries committed yet) is
    // epoch 0. EpochBoundary(1) opens epoch 1, closes epoch 0. And so
    // on. Redundant with the boundary's position in the DAG
    // (canonically checkable by CountAncestorsByType + 1 equals this
    // field); included for explicit-contract clarity and for efficient
    // payload-based routing.
    Epoch uint64 `json:"epoch"`

    // TriggerEventID is the canonical event whose commit-state caused
    // the emitter to observe the epoch threshold crossing. Always a
    // TaskVerificationConsensus event (the canonical source of
    // epoch-advancing cadence). Canonical-frozen. Included in
    // CausalRefs as well; redundant but auditable at payload level.
    TriggerEventID event.EventID `json:"trigger_event_id"`
}
```

### 1.3 CausalRefs

`EpochBoundary(N)`'s CausalRefs MUST include its `TriggerEventID` (the TaskVerificationConsensus event that crossed the threshold, per §2). Additional refs permitted if emission mechanism requires (e.g., if a rotation-primitive event is referenced — see §2 Candidate B).

Minimum CausalRefs = [TriggerEventID]. Maximum CausalRefs = implementation's choice for the selected emission mechanism, bounded by §1.4 validation rules.

### 1.4 Validation rules

Enforced at `dag.Add` admission time via the **admission-cross-check mechanism** (new `*dag.DAG` substrate capability introduced by this sub-spec; see §1.4.1 for the mechanism description). The cross-check runs synchronously during `dag.Add` as a pure function of the candidate event and canonical DAG state, rejecting invalid events before they are stored. Additional enforcement at `LogicalKeyConsumer.Apply` handles logical-key dedup (Epoch) and is a no-op for rules already verified at admission.

The validation rules:

- Payload.Version == 1 at F5 ship. Future versions require coordinated upgrade (schema change).
- Payload.Epoch >= 1 (epoch 0 has no boundary).
- Payload.Epoch MUST equal `CountAncestorsByType(TriggerEventID, EventTypeEpochBoundary) + 1`. This ties payload.Epoch to canonical DAG state: a Byzantine emitter that writes a wrong payload.Epoch produces an event whose admission-cross-check fails on every honest node, and the event never enters the canonical DAG.

  **Layer separation note**: the cross-check evaluates `CountAncestorsByType` from the perspective of `TriggerEventID`'s canonical position, NOT from the candidate EpochBoundary's position. Multiple emissions of `EpochBoundary(N)` for the same `TriggerEventID` ALL satisfy the cross-check identically — they have the same canonical count_at_trigger value (the not-yet-admitted candidate is, by definition, a descendant of the trigger, never an ancestor). The `LogicalKeyConsumer` keyed on `Payload.Epoch` (§2.2 Candidate A) is what converges multi-emit to a single canonical event. The two layers are complementary: admission enforces canonical correctness; logical-key dedup enforces uniqueness.
- Payload.TriggerEventID MUST exist in the DAG and have `Type == EventTypeTaskVerificationConsensus`.
- Payload.TriggerEventID MUST satisfy the threshold-crossing predicate (§2.1): `CountAncestorsByType(TriggerEventID, EventTypeTaskVerificationConsensus) + 1 == Payload.Epoch * RoundsPerEpoch`.
- Signature: EpochBoundary events MUST be signed by a validator seated in the canonical validator-seat snapshot effective at `TriggerEventID`'s canonical position. Binding signer eligibility to canonical DAG position rather than local wall-clock "time of emission" mirrors V-1 discipline for W selection — prevents local-clock skew from producing signatures that appear valid on emission but reference a validator-seat set not canonically active at the trigger's position.

### 1.4.1 Admission-cross-check mechanism

A new substrate capability on `*dag.DAG` enabling per-type, admission-time, canonical-state-dependent validation. Introduced by this sub-spec to close the gap between the §1.4 enforcement intent and pre-existing `dag.Add` capability (which only validated signature + causal-refs presence, not per-type payload semantics).

API:

```go
// RegisterAdmissionCrossCheck registers a validation function that runs
// synchronously during dag.Add, after signature+causal-refs verification
// and before the event is stored.
//
// RESTRICTED API. The validator MUST:
//   - Be a pure function of the candidate event and canonical DAG state.
//   - Use the provided WhileLockedReader; never call *DAG methods that
//     acquire locks (reentrancy deadlocks).
//   - Return an error to reject; nil to admit.
//   - Be fast (runs under write lock).
//   - Have no I/O, no goroutines, no side effects beyond the return value.
//
// One validator per event type; registration at startup, not reconfigurable.
//
// Use case: canonical cross-checks where payload validity depends on
// canonical DAG state at admission (e.g., EpochBoundary's Payload.Epoch
// equals CountAncestorsByType + 1). NOT for policy, rate-limiting, or
// non-canonical concerns.
func (d *DAG) RegisterAdmissionCrossCheck(
    eventType event.EventType,
    validator func(*event.Event, WhileLockedReader) error,
) error
```

`WhileLockedReader` is a new interface providing lock-free read access to canonical DAG state, including a lock-free `CountAncestorsByType` variant that operates directly on `d.events` (safe because the caller holds the write lock via `dag.Add`'s acquisition; reentrant `*DAG` method calls would deadlock).

EpochBoundary's validator implements §1.4 as a pure function of (event, reader): payload-shape checks, TriggerEventID type+existence, two CountAncestorsByType cross-checks (epoch-count-vs-payload, threshold-crossing-vs-payload), and the signer-in-canonical-validator-snapshot binding.

The mechanism is intentionally restricted-API: **only canonical-state cross-checks**, not policy. Future canonical primitives (per-epoch snapshots, reputation evidence, slashing challenges) whose validity depends on canonical state at admission can register their own validators against the same mechanism. Policy validation (rate limits, ratelimiting, etc.) and non-canonical concerns belong elsewhere — registering them via this mechanism is a halt-worthy abuse per §9.

### 1.5 Content-hash determinism

EpochBoundary's EventID is SHA-256 of the canonically-serialized event (same rule as all canonical events per `internal/event/event.go`). Verified against `eventCanonical` at `event.go:299-306`: the content-hash preimage includes `Type, CausalRefs, Payload, AgentID, CausalTimestamp, StakeAmount`. `ID` and `Signature` are excluded (comment at `event.go:297`: "Signature: circular — signing happens after hashing"); `SettlementState` is excluded (mutable post-creation metadata).

Two validators emitting `EpochBoundary(N)` for the same `TriggerEventID` produce **canonically-equivalent payloads** (Version, Epoch, TriggerEventID all identical by construction), but **non-identical content-hashes** — the emitter's `AgentID` is in the preimage and differs per emitter. Each validator's emission is a distinct canonical event with a distinct EventID.

Logical-key dedup on `Epoch` (§2) collapses the set: the first arrival at each node admits canonically; subsequent arrivals are silently dropped. Downstream consumers reference `EpochBoundary(N)` by its **canonical presence in the ancestor set** (via `CountAncestorsByType`), not by a specific content-hash. That indirection is load-bearing — code paths that hard-code an expected EpochBoundary EventID are incorrect.

---

## 2. Emission mechanism

The emitting logic must be canonical-trigger-conditioned and produce at most one canonical EpochBoundary per epoch-number regardless of how many nodes observe the trigger. Three candidates evaluated during sub-spec drafting; **Candidate A locked at v1 architect review per §2.5**. B and C retained as design-rejected alternatives for audit trail.

### 2.1 Canonical trigger condition (shared across candidates)

For any TVConsensus event E, define:

```
canonical_tvc_rank(E) = CountAncestorsByType(E, EventTypeTaskVerificationConsensus) + 1
```

(the +1 accounts for E itself). `canonical_tvc_rank(E)` is canonical: two nodes computing it against the same DAG content produce the same value.

**Trigger**: node observes that a just-committed TVConsensus event E satisfies `canonical_tvc_rank(E) == N * RoundsPerEpoch` for some integer N >= 1. The node is then eligible to emit `EpochBoundary(N)` with `TriggerEventID = E.ID`, `Epoch = N`.

`RoundsPerEpoch = 1000` per locked plan §5.4 (5A.2 §11.4). Constant at F5 ship; future tuning requires coordinated upgrade.

The trigger condition itself is canonical. What varies across candidates is **who emits** when the trigger fires.

### 2.2 Candidate A — Every-node symmetric emission with logical-key dedup

Every node running an `EpochBoundaryEmitter` recognition-consumer observes every committed TVConsensus event. On commit of event E satisfying §2.1's threshold, the emitter calls `localpub.Publisher.Publish(EpochBoundaryPayload{Epoch: N, TriggerEventID: E.ID})`.

Cluster behavior: every node emits its own EpochBoundary(N) event. Content-hashes differ only by signer. `TVConsensusLogicalKeyConsumer`-style logical-key dedup (F4B pattern) keyed on `Epoch` admits the first arrival and silently drops subsequent duplicates. Cross-node convergence: every node ultimately has the same single canonical EpochBoundary(N) in its DAG (the first one admitted, content-addressed by its hash).

**Pros:**
- Fully symmetric — no privileged emitter.
- Self-healing — if one emitter fails (crashes, out of sync), any other fills in.
- Matches F4B's LogicalKeyConsumer pattern directly; proven infrastructure.
- Low complexity — single recognition consumer + single logical-key consumer for admission dedup.

**Cons:**
- Multi-emit noise: up to `N_validators` events per epoch boundary hit the network before dedup collapses them. At `N = 7`-ish validators and one boundary per ~1000 rounds, this is negligible load.
- The EpochBoundary's content-hash varies per emitter (signer field in signature) — logical-key dedup absorbs the variance; the canonical EpochBoundary(N) is whichever byte-identical variant arrives first. Downstream consumers must be careful not to assume a specific content-hash; they read EpochBoundary(N) by CausalRef or by count-of-type.

### 2.3 Candidate B — Designated-validator rotation with symmetric fallback

For upcoming `EpochBoundary(N)`, a designated validator is computed: `designated(N) = validators[N % len(validators)]` where `validators` is the canonical active-validator-seat snapshot at the trigger event's canonical position. Designated validator has primary emission responsibility; it emits within a bounded grace window (e.g., 30 seconds after trigger condition holds in its local DAG view). If it does not emit within the grace window, Candidate A behavior kicks in: any validator may emit.

**Pros:**
- Under honest-majority, reduces multi-emit to one event per boundary (vs. N in Candidate A).
- Even spreads emission workload across validator set.

**Cons:**
- Requires a canonical `designated(N)` primitive keyed on validator-seat snapshot at trigger position. Tie-in with existing validator-seat snapshot machinery — adds surface area.
- Grace-window timing is local (wall-clock) — if the designated validator emits within grace but its emission arrives at other nodes AFTER their grace window expires, they may still emit. Reduces to Candidate A's multi-emit anyway.
- Fallback semantic adds control-flow complexity to the emitter logic.

### 2.4 Candidate C — Pure derivation, no EpochBoundary event

**Alternative surfaced during drafting.** Observed that `epoch_of(R) = canonical_tvc_rank(R.canonical_seal_context) / RoundsPerEpoch` is canonical by construction — no event needed. The ancestor-counting primitive is the source of truth; no intermediate artifact required.

**Pros:**
- Less new code — no new event type, no emitter, no logical-key consumer.
- One primitive (`CountAncestorsByType(R.sealContext, TaskVerificationConsensus)`) already suffices.
- Eliminates emission-mechanism discussion entirely.
- Canonical semantic is identical.

**Cons vs. Option 3b:**
- No explicit artifact — future workstreams that need a canonical reference point (per-epoch snapshots, reputation deltas, epoch-aware slashing evidence packets, audit tooling, light-client proofs) have nothing to CausalRef. Pure ancestor-counting forces every consumer to re-implement the count primitive — code duplication + Principle 6 violation.
- Ancestor-counting traversal scales with total TVConsensus count, not with epoch count. At 1M finalized TVConsensus events (~1000 epochs), counting TVConsensus ancestors is 1000x more work than counting EpochBoundary ancestors. Mitigable via caching but architecturally non-trivial.
- Future epoch-coupled events (e.g., per-epoch snapshots, per-epoch reputation deltas) have no canonical point to reference as their parent in the DAG.

**This candidate diverges from the founder-locked Option 3b.** Flagged here for architect sight; not the default recommendation. Included because the sub-spec's drafting obligation is to surface alternatives honestly — not to predetermine.

### 2.5 Decision (architect-confirmed, sub-spec v1 review)

**Candidate A locked.** Matches existing F4B logical-key infrastructure; simplest; no new privileged primitives; self-healing. Multi-emit load is negligible (O(validators) per epoch, where epochs are O(days)). The logical-key consumer for EpochBoundary is a small, well-scoped addition.

Candidate B's complexity is not justified by multi-emit reduction; the grace-window fallback reduces to Candidate A behavior under adversarial timing anyway. Candidate C loses the explicit-artifact benefit that motivates Option 3b — founder-locked per journal 2026-04-24. Neither will be implemented; both are retained in this document as design-rejected alternatives for audit trail.

---

## 3. Ancestor-counting primitive

### 3.1 Signature

New method on `dag.AnchorReader`:

```go
// CountAncestorsByType counts the canonical ancestors of `descendant`
// whose event type equals `eventType`. The count does NOT include
// `descendant` itself.
//
// Canonical: two nodes with identical DAG content compute identical
// counts for identical (descendant, eventType) arguments. The count is
// a pure function of DAG topology.
//
// Returns ErrEventNotFound if `descendant` is not locally materialized.
// Caller converts the signal to Status=StatusDeferred in the
// derivation context (same pattern as IsAncestor).
//
// Returns (0, nil) if `descendant` exists but has no ancestors of the
// requested type. Not an error.
CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error)
```

**All-or-defer semantic (ChatGPT v1 review)**: `CountAncestorsByType` is all-or-defer: if any traversed ancestor needed to determine the count is not locally materialized, it returns `ErrEventNotFound`; callers MUST defer rather than use a partial count. Partial-count returns would produce a smaller-than-canonical answer on one node, diverging from canonical behavior on nodes with complete materialization — exactly the D-1 violation the primitive is designed to prevent. Analogy to `IsAncestor` on the same interface: both surface materialization lag as an explicit deferral signal rather than a wrong answer.

### 3.2 Implementation

BFS from `descendant` over CausalRefs. Count events whose `Type == eventType`. Terminates when no unvisited ancestors remain.

Worst-case runtime: O(|canonical ancestors of descendant|). For a mature chain (millions of events, millions of ancestors), this is meaningful. Mitigation options deferred to implementation time:
- In-memory LRU cache keyed by `(descendant, eventType)`. Cache values are canonical (pure function of DAG state, invalidated only by rollback which is out of scope). Bounded by cache size.
- Amortized computation via per-event type-count projection: `dag.Add` maintains a running total-by-type; `CountAncestorsByType` reads from the projection (requires ancestor-subset projection, not just global total — same scoping concern as RoundCounter).

Sub-spec does not mandate an optimization; implementation selects based on benchmarked need.

### 3.3 Derivation-package interface extension

`internal/settlement/derivation/inputs.go`'s local `AnchorReader` interface adds the new method. `*dag.DAG` satisfies both the local interface and any future consolidated `dag.AnchorReader`.

```go
type AnchorReader interface {
    IsAncestor(ancestor, descendant event.EventID) (bool, error)
    Get(id event.EventID) (*event.Event, error)
    CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error)
}
```

Satisfies §2.1 DerivationInputs contract clause (b) — deterministic replayable lookup at cutoff.

---

## 4. epoch_of(R) semantic

### 4.1 Canonical definition

```
epoch_of(R) = CountAncestorsByType(R.CanonicalSealContext, EventTypeEpochBoundary)
```

Where:
- `R.CanonicalSealContext` is the canonical field on `TaskVerificationRound` populated by the finalizing consumer at terminal transition (per the prior halt's Option A ruling).
- `CountAncestorsByType` is the primitive defined in §3.

Canonical by construction: two nodes computing `epoch_of(R)` against the same DAG content compute the same value.

### 4.2 Epoch numbering convention

- Pre-first-EpochBoundary (genesis state): `epoch_of(R) = 0`. No boundary ancestors.
- After `EpochBoundary(1)` is canonically ancestor of R.sealContext: `epoch_of(R) = 1`.
- After N EpochBoundary events are ancestors: `epoch_of(R) = N`.

EpochBoundary events are numbered `1, 2, 3, ...` — there is no `EpochBoundary(0)` in the DAG. Epoch 0 is the implicit "before any boundaries" epoch.

### 4.3 Replacement of RoundCounter-based formula

Plan v3 §2.3 step 1 cited `epoch_of(R) = ⌊R.RoundCounter / RoundsPerEpoch⌋` per 5A.2 §11.4. That formula is **abandoned** for the canonical-path use. `epoch_of(R)` in the derivation function is computed ONLY via §4.1.

The `RoundCounter` primitive remains in `internal/epoch/` as an observability utility. Its output is used by projection-registry and log lines (`cmd/node/main.go:1980, :2079`). No canonical path reads it.

### 4.4 Cutoff-epoch for round R

```
cutoff_epoch(R) = max(epoch_of(R) - 1, 0)
```

Per 5A.2 §11.5: `cutoff_epoch_for(round R) = epoch_of(R) - 1`. For `epoch_of(R) = 0` (genesis), `cutoff_epoch - 1` underflows in uint64; the `max` clamps to 0. Pre-activation, `CanonicalWProjection` and `CanonicalQualityProjection` stub implementations ignore the epoch argument, so the clamp is benign. Post-activation, `epoch_of(R) >= 1` always (post-first-EpochBoundary), so the clamp is never triggered.

See §7 for bootstrap semantics.

---

## 5. Coordination with locked Reputation-and-Consensus-Integrity workstream

### 5.1 Touchpoint summary

The locked workstream's real-W implementation is epoch-keyed (per §5.4: `RoundsPerEpoch = 1000 LOCKED`). Snapshots are emitted at epoch boundaries (per locked plan §5.2). The workstream assumes epoch boundaries are well-defined canonical markers that snapshots can be keyed on.

This sub-spec defines **what those markers ARE**: canonical EpochBoundary events. Locked plan §5.2's phrase "at an epoch boundary" is operationalized concretely by the EpochBoundary event type defined in §1 of this sub-spec; every occurrence of "epoch boundary" in the locked plan resolves to an `EventTypeEpochBoundary` event in the canonical DAG. The locked workstream's snapshot emission machinery must be consistent with:

1. **Epoch-boundary observability**: snapshot emitters need to detect canonical epoch advances. Candidate: the snapshot emitter is a commit-bus consumer that fires on `EventTypeEpochBoundary` commits (analogous to RoundCountConsumer's interest in TVConsensus). Snapshot N is emitted on commit of EpochBoundary(N).

2. **Snapshot causal-parent**: snapshot N's CausalRefs include EpochBoundary(N). The boundary is canonically prior to the snapshot; the snapshot's content depends on canonical state up to the boundary's canonical position.

3. **W lookup cutoff epoch**: `CanonicalWProjection.Lookup(..., epoch)` reads snapshot(epoch). Snapshot existence at any epoch < current_epoch is guaranteed by the commit-bus wiring (snapshot commits before subsequent settlement reads).

4. **RoundsPerEpoch cadence agreement**: F5's EpochBoundary emission trigger (§2.1) uses `RoundsPerEpoch = 1000`. The locked workstream's internal use of RoundsPerEpoch must match. Any future change to the cadence is coordinated; divergence is a halt-worthy regression in both packages.

### 5.2 What the locked workstream author must confirm before this sub-spec ships

- Snapshot emission is gated on EpochBoundary commits (not on RoundCounter state or local timers).
- Snapshot content at epoch N uses canonical state at-or-before EpochBoundary(N)'s canonical position.
- `W.Real.Lookup(v, ..., cutoff_epoch)` reads snapshot(cutoff_epoch) and does not consult local runtime state.
- RoundsPerEpoch constant alignment.
- Locked workstream's snapshot/read path treats epoch 0 as the implicit pre-boundary base case; stub-W/stub-quality behavior at NeutralBP during epoch 0 is acceptable for the workstream's economic model.

**Coordination complete**: all 5 items confirmed COMPATIBLE against `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` at v2.1 review (see §13.1 v2.1 change log for per-item verdict).

### 5.3 What this sub-spec does NOT obligate the locked workstream to do

- This sub-spec does NOT require the locked workstream to emit EpochBoundary events itself. EpochBoundary emission is owned by F5's new EpochBoundaryEmitter (§2). The locked workstream CONSUMES the boundary.
- This sub-spec does NOT constrain the locked workstream's snapshot-content format — only the WHEN (on EpochBoundary commit) and the WHICH-canonical-state (up-to-and-including the boundary's canonical position).
- This sub-spec does NOT change the locked workstream's V-1 selection mechanism. `ReputationActivationEventID` const-flip forward note (per `internal/settlement/derivation/FORWARD_NOTES.md` §1) remains a separate open architectural question, not blocking this sub-spec.

**V-1 stub-to-real discontinuity**: at `ReputationActivation`, W's value for a given validator transitions discontinuously from the stub's `NeutralBP` to the locked workstream's cold-start value (per locked plan §6: Bootstrap W = T0 = 7000). Both values are canonical at their respective canonical positions; the discontinuity is inherent to the V-1 activation pattern (the activation event IS the semantic cutover point) and is accepted as a design property, not a defect. Every correct node computes the same pre-activation value (NeutralBP = 10000) and the same post-activation value (per locked plan §6 cold-start formula) for the same validator — no cross-node divergence at the discontinuity.

### 5.4 Reusable infrastructure note (informational, not coordination-blocking)

The admission-cross-check mechanism (§1.4.1) introduced by this sub-spec is available for future canonical primitives whose payload validity depends on canonical DAG state at admission. Candidate consumers from the locked workstream's roadmap and adjacent workstreams:

- **Per-epoch snapshots**: snapshot N's payload could carry a content-hash claim that must equal a canonical projection at EpochBoundary(N)'s canonical position. Cross-check at admission.
- **Reputation evidence packets**: validity might depend on canonical predecessor presence + canonical-frozen reputation values at a specific anchor.
- **Slashing challenges**: validity depends on canonical equivocation evidence existing in the DAG at a specific position.
- **Future protocol-parameter changes**: canonical-anchored parameter updates whose payload claims must match canonical state at a specified canonical position.

Each future user registers its own validator at startup; the substrate enforces the restricted-API discipline (pure function, lock-free reader, no I/O). This is **flagged for awareness, not blocking for this sub-spec**: the locked workstream owner does not need to commit to using the mechanism; they may continue with their own validation discipline if it satisfies the same canonicality property. The infrastructure exists if useful.

Mis-use of the mechanism (registering policy validators, runtime-state-coupled checks, or non-canonical concerns) is a halt-worthy regression per §9.

---

## 6. 5A.2 §11 cutoff-semantics verification

### 6.1 The 5A.2 claim

From `docs/architecture/q-score-canonicalization-design.md` §11.5:

> "cutoff_epoch_for(round R) = epoch_of(R) - 1 ... Round R settling in epoch E uses snapshot(E-1)."

And §11.4:

> "R.RoundCounter is deterministic from canonical state (it's the monotone canonical round number, derivable from the canonical TVConsensus event stream)."

The second claim was wrong under the current concurrent-dispatch model (the secondary halt's finding). This sub-spec replaces the RoundCounter basis with the EpochBoundary-ancestor basis. The first claim (`cutoff_epoch = epoch_of(R) - 1`) is preserved but with the new canonical definition of `epoch_of(R)`.

### 6.2 End-of-epoch marker semantic

EpochBoundary(N) is emitted canonically at the N*RoundsPerEpoch-th TVConsensus event's commit. By construction, EpochBoundary(N) is the **end-of-epoch-N-minus-one** marker AND the **start-of-epoch-N** marker: it closes the prior epoch (1000 TVConsensus events preceding) and opens the new one.

Snapshot(N-1) is emitted causally after EpochBoundary(N-1)'s commit (per §5.1 point 2). So for any round R with `epoch_of(R) = E >= 1`:
- R has EpochBoundary(1), EpochBoundary(2), ..., EpochBoundary(E) as canonical ancestors.
- cutoff_epoch(R) = E - 1.
- Snapshot(E-1) exists as a canonical artifact causally after EpochBoundary(E-1) and canonically before EpochBoundary(E).
- `W.Real.Lookup(..., cutoff_epoch=E-1)` reads snapshot(E-1). Canonical.

The 5A.2 §11.5 convention is preserved.

### 6.3 First-round-of-epoch boundary interaction (5A.2 §11.6 reference)

5A.2 §11.6 discussed the boundary case where R is the first round of a new epoch. Under the EpochBoundary-ancestor semantic:
- R is the first round of epoch E means R's sealContext has EpochBoundary(E) as an ancestor, but no rounds finalized later in epoch E are yet committed.
- `epoch_of(R) = E`, `cutoff_epoch(R) = E - 1`, snapshot(E-1) is the lookup target.
- Same-epoch-ancestor exclusion from 5A.3 §7.2 holds automatically: ancestors at epoch E (same as R's) have not yet contributed to snapshot(E-1); they are excluded from gen-ledger weighting.

No change to 5A.2 §11.6's behavioral semantic. Sub-spec preserves.

### 6.4 Orthogonality with Fix A canonical_cutoff_anchor nil/non-nil semantic

EpochBoundary is orthogonal to Fix A's `canonical_cutoff_anchor` nil/non-nil semantic (F5 5A.4.a schema v1.1). Fix A's nil semantic is governed by `ReputationActivation` canonical-ancestor check per F5 5A.2 §7.2 (V-1 invariant). EpochBoundary changes cutoff **epoch** basis (replacing RoundCounter) but does NOT change cutoff **anchor** nil/non-nil semantic. These are distinct canonical handles serving distinct purposes:

- `cutoff_epoch` (this sub-spec §4.4): what epoch's snapshot `W.Real.Lookup` reads.
- `canonical_cutoff_anchor` (schema Fix A): nil iff `ReputationActivation` NOT canonical-ancestor of R; non-nil = snapshot encoding.

An implementer working on F5 5B must NOT conflate the two. The cutoff epoch computed here feeds the W/Quality projection lookups; the cutoff anchor emitted on PayoutRecord provenance is a separate canonical handle governed by V-1 activation semantics. Both coexist; neither supersedes the other.

---

## 7. Genesis / bootstrap

### 7.1 Pre-first-EpochBoundary state

At chain genesis, no EpochBoundary events exist. `epoch_of(R) = 0` for every round R in the first 1000 TaskVerificationConsensus events.

### 7.2 Settlement behavior in epoch 0

F5 ships with stub-W and stub-quality (both return NeutralBP regardless of epoch). DeriveSettlement:
- Computes `epoch_of(R) = 0`.
- Computes `cutoff_epoch(R) = max(0 - 1, 0) = 0`.
- Calls `stubW.Lookup(..., 0)` → returns NeutralBP.
- Calls `stubQuality.Lookup(..., 0)` → returns NeutralBP.
- Produces correct pool shares (integer-path deterministic).

Genesis rounds settle canonically with NeutralBP validator weights. Consistent with current F5-shipped semantics.

### 7.3 First EpochBoundary emission

When the 1000-th TVConsensus event E is canonically committed (`canonical_tvc_rank(E) == 1000`), every node running the EpochBoundaryEmitter (per §2 Candidate A) detects the threshold and emits EpochBoundary(1) with TriggerEventID=E.ID. Logical-key consumer deduplicates to one canonical EpochBoundary(1). Cross-node convergence achieved.

### 7.4 Replay / bootstrap node

A fresh node joining the cluster performs genesis replay (per `internal/recognition/replay.go`). Events are replayed in topological order; EpochBoundary events are canonically included in the DAG and committed to the fresh node's store. By the time replay completes and live events start, all canonical EpochBoundary(1)..EpochBoundary(N_current) are committed. `epoch_of(R)` is correctly computable for all finalized rounds.

### 7.5 What NOT to emit at genesis

Per §4.2, EpochBoundary(0) does not exist. The genesis state is "no boundaries"; epoch 0 is implicit. A protocol that emitted EpochBoundary(0) at genesis would be adding a canonical event with nothing to trigger on and no semantic purpose. Rejected.

---

## 8. R.EpochAtFinalization field source

### 8.1 Prior halt resolution (Option A) — unchanged structurally

The prior halt's Option A added two fields to `TaskVerificationRound`:
- `CanonicalSealContext event.EventID`
- `EpochAtFinalization uint64`

Both are canonical-frozen at terminal transition. The field shapes stand.

### 8.2 What changes: the source of EpochAtFinalization

Per prior halt's plan: `EpochAtFinalization = RoundCounter.Total() / RoundsPerEpoch` at the finalizing consumer's moment of Apply.

Per this sub-spec: **EpochAtFinalization is derived from canonical DAG state**, not from RoundCounter.

```go
// In the finalizing consumer (internal/recognition/task_verification_consensus_consumer.go),
// at the moment of round.Transition(terminalState, ts):
round.CanonicalSealContext = ev.ID
round.EpochAtFinalization, err = dagReader.CountAncestorsByType(ev.ID, event.EventTypeEpochBoundary)
if err != nil { ... }  // ErrEventNotFound on materialization lag — consumer handles via F3-B deferral
```

The consumer needs access to a dag.AnchorReader (specifically the CountAncestorsByType method). Wiring: `NewTaskVerificationConsensusConsumer` takes an additional `dagReader AnchorReader` parameter at construction time; `cmd/node/main.go` wires `stack.dag` as the implementation.

The `dagReader` passed to the finalizing consumer MUST be backed by the same canonical DAG view used by activation checks and settlement ancestry reads. Shadow caches, stale wrappers, or local-only views are not permitted in this path — consistency with canonical-state queries elsewhere is load-bearing for cross-node byte-equality at round finalization. A wrapper that silently returns stale counts (or worse, returns "unknown" when the canonical reader would return a precise count) breaks the D-1 guarantee that the finalization path exists to uphold.

### 8.3 Canonical guarantee

Two nodes finalizing the same round R (same canonical-TVConsensus event ID) compute the same `EpochAtFinalization` because both run `CountAncestorsByType(ev.ID, EventTypeEpochBoundary)` against DAGs with the same canonical content. No concurrency defect; no counter state.

### 8.4 Materialization-lag semantic

If `CountAncestorsByType` returns `ErrEventNotFound` (the sealContext's ancestors aren't all locally materialized yet), the finalizing consumer defers per F3-B causal-prerequisite-gating pattern. On retry after materialization catches up, the count returns the canonical value.

The field is populated exactly once per round, atomically with the terminal transition. No drift window.

### 8.5 Downstream usage

`DeriveSettlement` at `internal/settlement/derivation/derive.go` reads:
- `round.CanonicalSealContext` — for V-1 ActivationCheck (§2.3 step 3).
- `round.EpochAtFinalization` — for `cutoff_epoch = max(epoch - 1, 0)` per §4.4.

No other derivation-path reads of RoundCounter. No drift.

---

## 9. Halt-and-surface triggers for sub-spec implementation

Same discipline as 5A sub-phases. Any of these during sub-spec implementation → halt:

- **CountAncestorsByType returns inconsistent counts across nodes** for the same (descendant, eventType) against canonically-equivalent DAG content. Implies a canonicality bug in the BFS or CausalRefs handling.
- **EpochBoundary logical-key dedup fails** (two canonical EpochBoundary(N) events admitted with different content-hashes). Implies dedup key derivation or admission-gate bug.
- **Trigger condition race**: a node emits EpochBoundary(N) before canonical_tvc_rank reaches N*RoundsPerEpoch per its local view, and the emitted event is admitted. Implies emitter logic is reading non-canonical state.
- **Reputation workstream coordination reveals incompatibility**: locked workstream's snapshot emission cadence or cutoff-epoch semantic conflicts with this sub-spec's EpochBoundary definition. Implies one or both designs need revision; halt and coordinate.
- **5A.2 §11 cutoff semantic breaks**: implementation-time testing shows `cutoff_epoch = epoch_of(R) - 1` produces a value that doesn't align with the snapshot's canonical content. Implies §4 or §6 has a bug.
- **Performance cliff**: `CountAncestorsByType` p99 latency exceeds **1ms at representative chain depth (10^6 canonical ancestors)** in benchmarking. Halt. Implement caching (§3.2) or projection-assisted counting before 5B testnet verification. The 1ms p99 target is load-bearing for 5B's per-round settlement pipeline: DeriveSettlement calls CountAncestorsByType at least twice per round (once for epoch_of, once via the finalizing consumer), and the whole settlement path has a 30-second consensus expiry budget that must leave headroom for DAG BFS + W/quality lookups + record construction + applicator transfers. Benchmarking happens at implementation time; architect may adjust the target based on measured baseline.
- **Payload validation at admission layer fails canonicality check**: two honest nodes both attempt to emit EpochBoundary(N) with the correct trigger, and one's event fails validation while the other's succeeds. Implies §1.4 validation rules have a non-canonical branch.
- **Admission-cross-check validator mis-implemented**: the validator function passed to `RegisterAdmissionCrossCheck` violates the restricted-API discipline — re-acquires `*DAG` locks (deadlock or panic), reads runtime/non-canonical state, performs I/O, spawns goroutines, or has side effects beyond the return value. Halt; validator must be a pure canonical function of (event, WhileLockedReader). The mechanism is for canonical-state cross-checks only (§1.4.1 restricted-API discipline); abuse for policy validation, rate-limiting, or non-canonical concerns is itself a halt-worthy regression.

---

## 10. Scope boundaries

### 10.1 What this sub-spec decides

- EpochBoundary event type exists as a new canonical event (§1).
- Payload shape is fixed at v1 (§1.2).
- Admission validation rules (§1.4).
- **Emission mechanism: Candidate A** (every-node symmetric emission with logical-key dedup, §2.2, architect-confirmed at sub-spec v1 review).
- Ancestor-counting primitive `CountAncestorsByType` (§3).
- `epoch_of(R)` = EpochBoundary-ancestor count (§4.1).
- `cutoff_epoch` formula (§4.4).
- Bootstrap / genesis behavior (§7).
- R.EpochAtFinalization source change (§8.2).

### 10.2 What this sub-spec does NOT decide

- **Implementation-level caching strategy for CountAncestorsByType** — implementation discretion at performance-tuning time, subject to the §9 latency gate.
- **Locked reputation workstream's internal snapshot-content format** — sub-spec only defines the WHEN/WHICH-canonical-state; content is workstream's surface.
- **Whether to retire `epoch.RoundCounter` entirely** — out of scope. The counter remains for observability; canonical path no longer reads it. A future workstream may retire it; F5 does not.
- **Fork / rollback semantics for EpochBoundary events** — inherited from general DAG fork/rollback discipline; no special case.
- **EpochBoundary payload versioning beyond v1** — future schema changes handled by standard protocol-upgrade discipline.

### 10.3 Out of scope for F5

- Multi-epoch-per-round or fractional-epoch computation (F5 cadence is 1 epoch = 1000 rounds integer-only).
- Epoch-aware rollback or state-replay semantics beyond existing DAG replay.
- Per-epoch reward pool adjustments or parameter rotation (single-parameter discipline at F5 ship).

---

## 11. Risks and dependencies

**Primary risk**: emission mechanism selection yields multi-emit noise or dedup-failure modes unanticipated at sub-spec time. Mitigation: §2's candidates are all logical-key-dedup-compatible; F4B's proven infrastructure absorbs multi-emit. Halt trigger at §9 catches dedup failures.

**Secondary risk**: CountAncestorsByType becomes a latency bottleneck at mainnet scale. Mitigation: §3.2 optimization options + §9 halt trigger at implementation benchmarking time.

**Tertiary risk**: Reputation workstream's snapshot emission semantic is incompatible with this sub-spec's EpochBoundary-gated approach. Mitigation: §5.2 pre-ship coordination requirement; architect circulates sub-spec to workstream owner.

**Dependency on F4B LogicalKeyConsumer pattern**: EpochBoundary admission relies on the logical-key dedup infrastructure proven in F4B. If F4B regresses, EpochBoundary dedup fails. Mitigation: F4B is frozen; F5 inherits.

**Dependency on existing dag package**: CountAncestorsByType is a new method on *dag.DAG. Implementation is a mechanical BFS addition; no semantic change to existing DAG operations.

**Dependency on recognition fabric**: the EpochBoundaryEmitter is a recognition consumer. Standard commit-bus wiring.

**Testnet impact**: cluster wipe is already planned at F5 merge; EpochBoundary events begin accumulating from genesis on the fresh testnet. No backfill migration.

---

## 12. Meta-observation — hidden-error candidates for canonical-epoch design

### 12.1 Primary candidate: the emitter or consumer reads local admission state instead of canonical DAG state for the trigger condition

**This is the exact pattern this sub-spec fixes** (RoundCounter was local-admission-order dependent). If the `EpochBoundaryEmitter` triggers on "my node just saw TVConsensus E and now my local count == N * RoundsPerEpoch" instead of calling `CountAncestorsByType(E.ID, EventTypeTaskVerificationConsensus)` against the canonical DAG, the original defect is re-introduced.

The §2.1 trigger condition and §1.4 cross-check are the structural defenses. The hidden error would be a subtle implementation that bypasses them "for performance" or "simplicity."

**Grep-level implementation test**: ensure the emitter and the logical-key consumer never read `round.RoundCounter`, local counters, or any non-canonical projection to decide emission or admission. They MUST read canonical DAG state only via `CountAncestorsByType`. Grep the emitter source files for any local-counter reads; verify zero hits.

**Payload forgery sub-case**: a Byzantine emitter that writes a wrong `Payload.Epoch` (e.g., emits EpochBoundary(2) when only 1999 TVConsensus ancestors exist) is caught at admission by §1.4's validation rule `Payload.Epoch == CountAncestorsByType(TriggerEventID, EventTypeEpochBoundary) + 1`. Every admitting node verifies by running the primitive themselves; a mismatch between payload.Epoch and the canonical count rejects the event. The §1.4 validation is load-bearing — dropping or weakening it reintroduces Byzantine trust in payload.Epoch. Forgery is a narrower sub-case of the primary pattern: both are defeated by "compute the canonical answer yourself, never trust the counter."

### 12.2 Secondary candidate: CountAncestorsByType cache delivers stale counts

If §3.2's optimization-time caching is introduced carelessly, a stale cache entry could return the wrong count even though canonical state has advanced. The cache is canonical IF invalidated on every DAG.Add that could affect a cached entry's count — which is a complex invalidation discipline. Safer default: no cache; compute fresh each time. Caching added only with benchmarked need + invalidation proof.

### 12.3 Tertiary candidate: genesis bootstrap produces EpochBoundary(0)

§4.2 explicitly forbids EpochBoundary(0). An implementation that emits it at genesis (e.g., "here's the epoch 0 snapshot marker") breaks §4's numbering convention. Validation rule §1.4 (Payload.Epoch >= 1) catches this at admission.

### 12.4 Quaternary candidate: designated-validator trigger (Candidate B) introduces wall-clock coupling

Candidate B's grace-window fallback relies on wall-clock timing. If the grace window is interpreted as canonical state (e.g., "after 30 wall-clock seconds, anyone emits"), that's V-1 coupling to local time. Implementation MUST treat grace as non-canonical observation only (the canonical trigger condition is still canonical_tvc_rank == threshold; grace gates WHO emits but not WHETHER the canonical threshold holds).

(Moot under current spec — Candidate A is locked per §2.5 — but retained because the pattern generalizes: any future "designated emitter with timeout" mechanism must keep wall-clock strictly out of canonical decision paths.)

### 12.5 Quinary candidate: stacked canonical-deferral queries amplify into retry storms

5B settlement now depends on multiple canonical-state queries that correctly defer on missing state:

- `CountAncestorsByType` for epoch (this sub-spec).
- `IsAncestor` for V-1 W activation (5A.2 §7.2).
- `ReadAtAnchor` for gen-ledger ancestry (5A.3 §2.2).
- Future: reputation snapshot lookup.

All defer on `ErrEventNotFound`. Semantically correct. But operationally, if the retry scheduler is naive (e.g., immediate retry on any defer signal), multiple defer cascades across consumers can produce pathological retry churn — cascading failures under partial materialization lag.

This is not a design invalidation; it is an operational-risk elevation. Implementation MUST pay attention to retry-scheduler discipline: exponential backoff, coordinated retry across stacked deferrals, and scheduler-level bounds on retry depth. Testnet verification of materialization-lag scenarios (per F5 5B §7 criterion 11b genesis-replay) must exercise stacked-defer cases, not just single-query defer.

Flagged as the highest-priority operational risk for 5B implementation beyond the structural hidden-error classes above.

### 12.6 Implementation discovery-tax predictions

Implementation-time alertness items predicted during Gate sub-spec v1 multi-AI review (Grok). Not design changes; forward-looking watch items for sub-spec implementation:

- **(i) Logical-key dedup key subtlety** — the EpochBoundary logical-key consumer MUST use the F4B pattern of keying on `Epoch` (not full content-hash). If keyed on content-hash, multi-emit never collapses (different signers produce different hashes per §1.5). Easy to get wrong on first pass because content-hash dedup is the natural default.

- **(ii) CountAncestorsByType performance cliff on genesis replay** — genesis replay of a mature chain will call the primitive many times for every round whose `EpochAtFinalization` is being computed. The LRU cache deferred per §3.2 will be added here. Without the cache, replay hits O(total history) worst case per call.

- **(iii) TriggerEventID rollback edge case** — rare fork scenario where a TVConsensus event satisfying the epoch threshold is later rolled back via DAG fork resolution. The cross-check (§1.4) must still hold on the final canonical DAG; test that the EpochBoundary emitted from the rolled-back TriggerEventID is also invalidated, or that the logical-key dedup correctly transitions to a replacement EpochBoundary(N).

---

## 13. Sign-off sequence

Mirrors F5 5A sub-phase lifecycle discipline:

1. **CC drafts sub-spec v1.** ✅ Complete.
2. **Architect reviews v1 in full** before multi-AI send. ✅ Complete (four-item review applied).
3. **Multi-AI review** (Grok + ChatGPT) with architect-supplied prompts. ✅ Complete (nine-item review applied → v2).
4. **Reputation-workstream owner coordinates** per §5.2's 5 confirmation items. ✅ Complete (all 5 items COMPATIBLE against `docs/plans/2026-04-12-reputation-and-consensus-integrity.md`; 2 documentation-precision items applied in-place → v2.1).
5. **Revisions → v3** — not needed; v2.1 substance preserved through v2.3 precision patches.
6. **Architect final read.** ✅ Complete (3 residue fixes → v2.1; admission-cross-check halt → v2.2; layer-separation precision → v2.3).
7. **Founder approval.** ✅ Complete (v2.1 founder-approved; v2.2 + v2.3 are architect-locked precision patches not requiring founder rerun).
8. **Sub-spec implementation** ✅ Complete across breakpoints A (event type + dag primitive + interface), B (emitter + LK consumer + admission validator + cmd/node wiring), C (round fields + finalizing-consumer wiring + integration tests).
9. **Sub-spec completion gate** ⏳ In progress. Report at `docs/plans/implementation/canonical-epoch-event-completion-gate-report.md`.
10. **5B breakpoint-2 resumes** with canonical-epoch primitive in place. ⏳ Next.

### 13.1 Change log

- **v1 (2026-04-24)** — initial draft.
- **v1 review-applied (2026-04-24)** — architect review of v1 applied four items: §1.5 content-hash precision (verified against `event.go:299-306`: Signature excluded, AgentID included; different emitters produce distinct content-hashes, logical-key dedup on Epoch converges; downstream consumers reference via CountAncestorsByType, not content-hash); §2.5 Candidate A confirmed (B and C design-rejected, retained as audit trail); §9 performance gate concretized (1ms p99 @ 10^6 ancestors); §13 reordered to place reputation-workstream coordination before v2 revisions so incompatibility drives v2, not post-approval churn.
- **v2 (2026-04-24)** — Gate sub-spec v1 multi-AI review (Grok + ChatGPT) applied 9 changes: **structural / load-bearing (5)** — §3.1 all-or-defer sentence (ChatGPT: prevents partial-count implementation), §1.4 signer canonical-position binding (ChatGPT: replaces wall-clock "time of emission" with validator-seat-snapshot-at-trigger-canonical-position discipline matching V-1 W selection), §5.2 fifth reputation-workstream confirmation item (ChatGPT: epoch 0 / stub-W/stub-quality acceptability), §8.2 consumer-DAG-view discipline (ChatGPT: same canonical reader as activation + settlement ancestry, no shadow caches), new §6.4 Fix A orthogonality (ChatGPT: cutoff_epoch and canonical_cutoff_anchor are distinct canonical handles, implementer must not conflate); **hidden-error restructuring (2)** — §12.1 reframed to lead with local-admission-state-vs-canonical-DAG pattern (Grok: this is the exact defect the sub-spec exists to fix; payload-forgery retained as sub-case), new §12.6 stacked-canonical-deferral retry-storm amplification (ChatGPT: operational-risk elevation, highest-priority operational risk; testnet §7 criterion 11b must exercise stacked defers); **implementation-alertness (2)** — §2.4 Candidate C rejection strengthened with concrete future-use-case list (Grok: snapshots, reputation deltas, slashing evidence, audit tooling, light-client proofs), new §12.7 Grok discovery-tax predictions (logical-key-on-Epoch-not-content-hash, CountAncestorsByType cache at genesis replay, TriggerEventID rollback edge case). Status header updated to v2; §13 sign-off marked step 3 complete, step 4 ⏳.
- **v2.1 (2026-04-24)** — Post-coordination documentation precision patch. Reputation-workstream coordination per §5.2 closed with all 5 items COMPATIBLE against `docs/plans/2026-04-12-reputation-and-consensus-integrity.md`: (1) snapshot emission gated — sub-spec v2 provides missing specification, locked plan §5.2-§5.3 compatible; (2) snapshot content canonical-state-bounded — locked plan SN-2/SN-3 stronger than ask; (3) W.Real.Lookup purity — locked plan §4.1 W-1 invariant stronger than ask; (4) RoundsPerEpoch=1000 — locked plan §5.4 + §16 exact match; (5) epoch 0 acceptability — V-1 activation-discontinuity pattern, inherent and accepted. Two documentation-precision items folded in-place (avoid churning to v3 for docs-only): §5.1 now states that locked plan §5.2's phrase "at an epoch boundary" is operationalized concretely by this sub-spec's EventTypeEpochBoundary events; §5.3 adds a paragraph acknowledging the stub-to-real W value discontinuity at ReputationActivation (NeutralBP → locked plan §6 Bootstrap W=T0=7000) as an accepted V-1 pattern property, not a defect — every correct node computes the same pre- and post-activation values. Status header updated to v2.1; §13 sign-off marked step 4 ✅ Complete, step 5 "v3 not needed; v2.1 stands"; step 6 architect final read ⏳ Next.
- **v2.1 residue-fixes (2026-04-24)** — Architect final read of v2.1 applied three residue fixes: (1) §2 preamble "Three candidates. Architect decision required." → "Three candidates evaluated during sub-spec drafting; Candidate A locked at v1 architect review per §2.5." stale language since v1 architect review; (2) §5.2 "Action required: architect circulates this sub-spec..." → "Coordination complete: all 5 items confirmed COMPATIBLE..." stale since v2.1 coordination closure; (3) §12.3 deleted — after §12.1's v2 reframe (leading with local-admission-state-vs-canonical-DAG pattern), §12.3's narrower statement of the same pattern was an exact duplicate. Remaining sections renumbered: former §12.4 → §12.3 (genesis bootstrap produces EpochBoundary(0)), former §12.5 → §12.4 (Candidate B wall-clock coupling), former §12.6 → §12.5 (stacked canonical-deferral retry storms), former §12.7 → §12.6 (implementation discovery-tax predictions). Historical §12.6/§12.7 references in the v2 change-log entry above are preserved — they document v2's content at time of v2, not v2.1's renumbered layout. No body cross-references to old §12.X numbers. Pure cleanup; no new architectural decisions. v2.1 is architect-approved pending founder approval.
- **v2.2 (2026-04-24)** — Implementation-time architectural patch. During breakpoint-B (LogicalKeyConsumer wiring) implementation, surveyed `dag.Add` and surfaced that the substrate has no per-type payload-validation hook — yet sub-spec v2.1 §1.4 stated validation is "Enforced at dag.Add admission time." Real gap. Halted. Architect locked Option A: add a restricted-API admission-cross-check mechanism to `*dag.DAG` (canonical-state cross-checks only; not generic policy validation). Patches: §1.4 intro rewritten to reference the new mechanism; new §1.4.1 "Admission-cross-check mechanism" describes the API + restricted-discipline + EpochBoundary as the first user; new §5.4 "Reusable infrastructure note (informational, not coordination-blocking)" lists candidate future users (per-epoch snapshots, reputation evidence, slashing challenges, protocol-parameter changes) and confirms the mechanism is available but not blocking for the locked workstream; new §9 halt trigger added: "Admission-cross-check validator mis-implemented (reentrant lock, non-pure, I/O, runtime state) — halt; validator must be pure canonical function." Plan v3 §0 decision 2 ("5A primitives consumed, not re-designed") amended again — F5 5B absorbs the substrate change required to make §1.4 enforceable. Status header bumped to v2.2; sign-off resumes at architect approval of v2.2 substance. Implementation proceeds at breakpoint B with the new mechanism per architect direction.
- **v2.3 (2026-04-24)** — Post-breakpoint-C precision patch (no design change). At breakpoint C, integration test 4 (`TestBoundary_MultiEmit_BothAdmittedByCrossCheck`) surfaced an implementer-intuition trap: the natural assumption was that once one EpochBoundary(N) emission lands, subsequent emissions for the same trigger fail the cross-check. In fact, the cross-check evaluates `CountAncestorsByType(TriggerEventID, EpochBoundary)` from the trigger's perspective — and newly-emitted boundaries are descendants of the trigger, never ancestors — so all emissions for the same trigger see the same canonical count_at_trigger value and ALL pass admission. The LogicalKeyConsumer keyed on `Payload.Epoch` is what converges multi-emit to one canonical event. Both layers are necessary: admission enforces canonical math; LK dedup enforces uniqueness. The behavior was already correctly specified across §1.4 + §2.2; this v2.3 patch adds an explicit "Layer separation note" sentence to §1.4's epoch-count cross-check bullet so future implementers see the layer separation upfront and don't make the same first-pass assumption. One-sentence precision; no §13 sign-off rerun needed.

---

**End of Canonical Epoch Event sub-spec v2.3 — post-implementation precision patch. Sub-spec implementation complete across breakpoints A/B/C. Ready for sub-spec completion gate.**
