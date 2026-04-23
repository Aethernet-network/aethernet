# Locked-invariant review — F4A

**Status**: F4A step 8 — completed 2026-04-22 against `feat/selection-consistency-fix` @ `432a266`.
**Plan reference**: F4 plan v2 §4 (Part C).
**Purpose**: Catalogue every F3-B locked invariant, confirm which stay locked, document the refinements F4 introduces, and surface coupling to F4A FINDINGs that F4B implementation must respect.

This is a reference document. F4B implementers read this first to know what they may and may not touch.

---

## 1. Framing

F3-B locked its invariants under the model assumption "**one canonical event per round**." Part F retry on 2026-04-22 demonstrated empirically that this assumption is false on the live testnet — multiple validators independently emit `TaskVerificationConsensus` events for the same `RoundID`, and the per-task-mutex-wins-locally selection rule causes per-node settlement divergence (see `docs/plans/implementation/selection-race-characterization.md`).

This document re-examines every F3-B lock under the empirical reality "**multiple canonical events per logical key are routine**." Most locks are sound and stay locked; a small number need refinement; a small number have new companions.

This is **not** an invitation to unlock everything. The architectural diff is bounded and explicit.

---

## 2. Invariants that stay locked (no change)

Each line: invariant identifier, brief restatement, why it remains sound under the empirical model.

### LoadApplied (A-1 .. A-4)

- **A-1 load-before-listener.** `Applicator.LoadApplied` runs before any code path that can deliver canonical events to consumers. Sound — orthogonal to multi-emit; load order is the same regardless of how many events for a key exist.
- **A-2 fail-startup-on-error.** `LoadApplied` returns an error → `os.Exit(1)`. Sound.
- **A-3 sync-on-write.** Applied set is fsync'd. Sound.
- **A-4 DAG-anchor.** Every applied entry references the canonical event. Sound — under multi-emit, each applied entry references the specific event whose Apply fired; F4B's logical-key admission will swap "the specific event" for "the (consumer, key) pair that fired" but the anchor invariant is preserved.

### Escrow (B-1 .. B-4)

Same shape as LoadApplied for escrow. Sound. F4B does not touch the escrow surface.

### Dispatcher core that stays locked

- **C-2 atomic reservation.** Check-and-set on admission record creation is atomic via the dispatcher's `RunInTransaction` boundary. Sound.
- **C-4 five-state lifecycle.** `StateReservedPendingPrereqs` → `StateProcessing` → (`StateApplied` | `StateFailedRetryable` | `StatePartialFailure`). No new states added. Sound — winner-event elimination means we do not need a `superseded` state.
- **C-5 reservation transaction minimality.** Reservation transaction touches only the admission record. Sound.
- **C-6 DAG-anchored every admission.** Every admission record carries `EventID` of the event that triggered it. Sound; for logical-key admissions in F4B, the EventID is the FIRST event that triggered admission for that key — observation events that follow share the same admission record.
- **C-7 storage-error fail-closed.** Any non-`not-found` storage error from the admission store causes the dispatcher to refuse invocation. Sound.
- **C-8 conformance in CI.** Behavioral conformance suite runs in CI. Sound. **Extended** by F4A step 2's replay-conformance template (`internal/dispatch/conformance/replay_path.go`).
- **C-9 single mandatory replay-state registry.** Sound.
- **C-10 no-bypass.** Canonical-event consumers must register through the dispatcher, not directly with the recognition fabric. Sound. **Extended** by F4A step 9's no-bypass CI lint verification.
- **C-11 atomic local commit.** Authoritative local state mutation happens within a single durable commit. Sound. F4B logical-key Apply runs within the same atomic-batch boundary.
- **C-12 per-(event, consumer) completion.** For content-hash consumers, exactly-once Apply per (event, consumer). Sound — preserved unchanged for content-hash. F4B introduces a NEW invariant for logical-key consumers (per-(consumer, key) completion, see §3 below) that complements rather than replacing C-12.
- **C-13 DAG anchor identity.** `EventID` recorded in admission record matches the event that triggered admission. Sound.
- **C-14 recovery probes evidence-based, monotonic, replay-safe.** `RecoveryProbe` consults canonical state to determine whether a prior crash left settlement complete. Sound. F4B logical-key consumers will need their own RecoveryProbe shape (per-key not per-event), but the invariant is preserved.
- **C-15 admission state is non-canonical node-local.** Sound — and load-bearing for F4A FINDING #8 (`dag-tips-unsorted`): the per-node anchor choice landing in admission records is acceptable BECAUSE C-15 holds.
- **C-16 future-consumer inheritance.** New consumers register before the dispatcher starts admitting events; no admission state pre-exists for them. Sound. F4B logical-key consumers register the same way.

### Causal prerequisite gating (D-1 .. D-8)

All sound. Logical-key admission does not change prerequisite semantics — a logical-key consumer with declared prerequisites still defers admission until prerequisites are satisfied.

### F3-B §4.5 atomic-batch forward-only settlement

**Sound. Preserved unchanged.** Winner-event elimination (the F4 mechanism) means there are no `superseded` pre-commit transitions; forward-only therefore remains strict. This is the principle 5 anchor of the entire workstream.

### Serialization-1

> "For any canonical event, there exists exactly one valid canonical serialized byte representation."

Sound. F4 does not change canonical serialization.

---

## 3. Invariants that require refinement (the F4 architectural diff)

### 3.1 C-3' (refined from C-3) — admission key varies by consumer type

**Original C-3**: Admission keys derive from BLAKE3 of canonical-serialized event bytes.

**Empirical problem**: Content-hash admission treats distinct canonical events for the same logical key as distinct admission records. This is the root cause of the F4 selection-race bug — TVConsensus events with different `FinalVerdict` for the same `RoundID` get different admission keys, get different admission records, get applied independently, and per-node first-event-past-the-mutex wins.

**Refinement**: Consumers declare admission strategy at registration:

| Strategy | AdmissionKey derivation | Used by |
|---|---|---|
| **Content-hash** (default) | `BLAKE3(canonical-serialized event bytes)` | Every consumer today (Type A/B/C/D) |
| **Logical-key** (new, opt-in) | Consumer-declared logical-key projection of event payload | Future Type E consumers (TVConsensus, Settlement) |

Each consumer declares strategy at registration. The dispatcher validates declarations structurally at registration time — malformed declarations fail registration before any event is processed.

**C-3' (locked statement)**: Every consumer declares admission-key strategy at registration: content-hash (default) or logical-key. Logical-key consumers implement `LogicalKeyConsumer` (see §3.4). The dispatcher validates declarations structurally at registration; malformed declarations fail structural validation.

### 3.2 Serialization-2 (new companion to Serialization-1)

> **Serialization-2**: For logical-key-admitted event types, the canonical outcome is derived from underlying canonical state (votes, attestations) that is itself deterministic and cluster-uniform. The event's payload fields describing outcome (e.g., `FinalVerdict`, `Attestations`) are **advisory**; consumers MUST NOT derive canonical state from them.

This is the structural fix for the F4 selection race. The bug existed because consumers derived canonical state from `payload.FinalVerdict` (the triggering event's payload), which is non-cluster-uniform under multi-emit. After Serialization-2, canonical state is derived from `roundState.Votes` (the canonical underlying votes in the DAG), which IS cluster-uniform.

### 3.3 C-17 (new) — advisory outcome fields

> **C-17**: Logical-key-admitted event payloads may carry advisory outcome fields for diagnostics and backward compatibility. Canonical state derivation is from canonical underlying state only.

This is the consumer-side enforcement of Serialization-2. The dispatcher does not (and cannot) prevent a consumer from reading `payload.FinalVerdict` — but the conformance template MUST include a check that logical-key consumers do not derive canonical state from advisory fields. Tracked for F4B test scope.

### 3.4 LogicalKeyConsumer — the new primitive

Replaces v1's `SelectionRule`. Simpler: no winner selection, no tiebreaker, no `superseded` state.

```go
// LogicalKeyConsumer is a dispatcher consumer that handles events
// admitted by logical-key (e.g., RoundID, TargetEventID) rather than
// content-hash. Multiple byte-distinct canonical events can exist in
// the DAG for the same logical key; this consumer treats them as
// readiness signals and derives canonical outcome from underlying
// state that is cluster-uniform.
type LogicalKeyConsumer interface {
    Key(ev *event.Event) (LogicalKey, error)
    IsComplete(roundState RoundState) (bool, error)
    DeriveOutcome(roundState RoundState) (Outcome, error)
    Apply(ctx context.Context, key LogicalKey, outcome Outcome) error
}
```

Critical properties:

- **Apply is per-key, not per-event.** Fires exactly once per logical-key value, regardless of how many events for that key exist.
- **`IsComplete` is deterministic on canonical state.** Same votes on all nodes → same completeness determination.
- **No `superseded` transitions.** `IsComplete` is defined so further events cannot change `Outcome`; there is no case where a post-Apply event would change the outcome.
- **Forward-only settlement preserved.** Atomic-batch per C-11 unchanged.

### 3.5 Per-(consumer, key) completion (new for Type E)

Companion to C-12. For logical-key consumers, the exactly-once boundary is `(consumer, key)` not `(consumer, event)`. The admission store's primary key for logical-key admissions is `(consumer_name, logical_key)`.

This is a NEW shape for the admission record's key space. F4B's persistence-layer changes must accommodate it.

---

## 4. Coupling to F4A FINDINGs — F4B implementation MUST address

This is the founder-mandated addition: F4B's dispatcher admission-surface changes touch the same `internal/store/store.go` admission decode path that F4A FINDINGs #5 and #6 surfaced gaps in. Folding the gates into F4B (rather than scheduling them as separate hardening) keeps the change set coherent.

### 4.1 FINDING #5 — admission-schema-no-gate

**Surface**: `internal/store/store.go` `GetAdmission` and `AllAdmissions`.
**Issue**: `AdmissionRecord.SchemaVersion uint32` is persisted but never validated on read. A record with `SchemaVersion: 999` round-trips opaquely.

**F4B requirement**: F4B introduces logical-key admission, which adds new fields to `AdmissionRecord` and bumps `SchemaVersion`. F4B must:

1. Define `AdmissionCurrentVersion` constant in `internal/dispatch/types.go` (or wherever `AdmissionRecord` lives).
2. In `store.GetAdmission`: after JSON unmarshal, check `if rec.SchemaVersion > AdmissionCurrentVersion { return nil, ErrSchemaTooNew }`.
3. In `store.AllAdmissions`: same check, applied per-record. Decision: skip-with-warn (operator-visible) or fail-loudly (refuse startup) — F4B picks one and documents it. **Recommendation**: fail-loudly on Recover() path (operator runs an older binary against a newer-store), skip-with-warn during normal operation (rare race window).
4. Define `ErrSchemaTooNew` in the store package or dispatch package — must be testable via `errors.Is`.

This MUST be in F4B's first persistence-layer commit. Skipping it means F4B introduces a wider divergence vector than F4 closes: nodes running mixed binaries silently mis-decode each other's admission records.

### 4.2 FINDING #6 — admission-state-no-gate

**Surface**: `internal/store/store.go` admission decode path; `internal/dispatch/types.go` `AdmissionState.String()`.
**Issue**: Hand-crafted JSON with `state: 99` round-trips through the store; `String()` returns "unknown".

**F4B requirement**: Same change site as #5. F4B must:

1. After unmarshal in `GetAdmission`/`AllAdmissions`, validate `rec.State` is one of the known `AdmissionState` enum values.
2. Unknown state → `ErrUnknownAdmissionState` (or fold into `ErrSchemaTooNew` if the version field would have caught it — reasonable since unknown states are typically introduced with version bumps).

Bundle with #5 in the same commit. Same review surface, same test surface.

### 4.3 Persistence-layer test pattern

F4A step 5's `internal/store/store_extended_test.go` provides table-driven precedents for both checks:

- `TestAdmission_UnknownSchemaVersion_RoundTripsOpaquely` (currently passing — documents the bug; F4B inverts to assert ErrSchemaTooNew is returned)
- `TestAdmission_UnknownStateValue_RoundTripsOpaquely` (same — F4B inverts)

F4B's first persistence-layer commit should flip these tests from "documents the bug" to "asserts the gate" in the same commit that adds the gate.

---

## 5. What this refinement does NOT do

- Does not introduce reversible settlement. **Forward-only preserved.**
- Does not change F3-B's per-(event, consumer) completion semantics for content-hash consumers.
- Does not change the atomic-batch invariant (C-11).
- Does not change DAG-anchor verification (C-13).
- Does not change causal prerequisite gating (D-1 .. D-8).
- Does not affect Type A/B/C/D consumer taxonomy structurally. **Type E (logical-key) added.**
- Does not unlock §4.5 (atomic-batch forward-only settlement). That remains the principle 5 anchor.

---

## 6. Glossary

- **Content-hash admission**: F3-B's default admission strategy — `AdmissionKey = BLAKE3(canonical event bytes)`.
- **Logical-key admission**: F4B's new strategy — `AdmissionKey = consumer-declared projection of event payload (e.g., RoundID)`.
- **Type E consumer**: a consumer that uses logical-key admission and implements `LogicalKeyConsumer`. New in F4B.
- **Cluster-uniform**: a value that is the same on every correct node given the same DAG state.
- **Advisory field**: an event payload field that may differ across multi-emit events for the same logical key; consumers MUST NOT derive canonical state from it.
- **Sealed outcome**: a `RoundState` configuration where `IsComplete` returns true — no future event arrival can change the derived `Outcome`.

---

**End of locked-invariant review v1 (F4A).**
