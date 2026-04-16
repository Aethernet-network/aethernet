# F3-B Fix Workstream — Part D: Causal Prerequisite Gating

**Workstream parent**: `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (locked v3-final). Specifically §5 (Part D, invariants D-1 through D-8), §4.13 (dispatcher invariants Part D extends), §10 (success criteria), §11 (out of scope), §12 (future-consumer taxonomy).

**Integration branch**: `feat/settlement-consensus-integrity-fix` (currently at commit `fd899c6` after Part C).

**Merge constraint**: No merge to main until the full F3-B workstream passes §10 end-to-end testnet verification.

**Status**: Draft, awaiting founder sign-off.

---

## 1. DAG-reachability primitive

### 1.1 What exists today

`internal/dag/dag.go` already exposes `IsAncestor(ancestor, descendant event.EventID) (bool, error)` at line 638. Implementation: BFS from the descendant following `CausalRefs` backward, with a `visited` map to avoid cycles. Complexity: O(A) where A = number of ancestors of the descendant. Early-exit when the ancestor is found.

### 1.2 What Part D adds

Nothing to the DAG package. `IsAncestor` is sufficient for prerequisite validation. The locked design (§5.3 implementation note) says "indexed ancestor checks or equivalent" are permitted — the existing BFS is equivalent and already handles multi-hop ancestors. The cost is bounded per event because DAG events reference semantic parents only (per CLAUDE.md architectural rules), keeping the ancestor set small.

The dispatcher already holds a `DAGAnchorReader` interface that includes `IsAncestor` (defined in `internal/dispatch/anchor.go`). Part D reuses this interface for prerequisite validation without adding new DAG package dependencies.

### 1.3 Performance justification

Prerequisite validation runs once per admission (outside the BadgerDB transaction, per D-8). For AetherNet's semantic-parent DAG, a typical event has 0-2 causal refs and the ancestor chain is short (task lifecycle: Posted → Claimed → Submitted → Approved, depth ≤4). The BFS traversal on these chains is microsecond-scale. No index optimization is needed at current scale; if profiling indicates otherwise, the locked design permits adding one without changing the validation property.

---

## 2. `Prerequisites` wiring in the dispatcher

### 2.1 Changes to `dispatcher.go`

The `Admit()` flow changes from:

```
reserve → (skip prerequisites) → processing → invoke consumers
```

To:

```
reserve → check prerequisites for each consumer → validate against DAG →
  if all projected: → processing → invoke consumers
  if valid but missing: → reserved-pending-prerequisites → defer
  if any invalid (not DAG-reachable): → fail admission with diagnostic
```

### 2.2 Exact insertion points

**`createReservation()`**: Change `State: StateProcessing` to `State: StateReservedPendingPrereqs`. Store the union of all consumers' `Prerequisites(ev)` on the admission record in a new `MissingPrerequisites []event.EventID` field (§2.3).

**New method `checkPrerequisites()`**: Called after reservation, outside the transaction (D-8). For each `EventID` in the union:
1. Validate DAG-reachability via `d.dag.IsAncestor(prereq, ev.ID)`. If not reachable, return `ErrPrerequisiteForgery` with consumer name and invalid ID (D-4).
2. Check local projection by attempting `d.dag.Get(prereq)`. If the event exists in the DAG, the prerequisite is projected. If not found, it is missing — mark as deferred.

If all prerequisites are projected and valid: transition to `StateProcessing`, clear `MissingPrerequisites`, persist.
If some are valid but missing: stay in `StateReservedPendingPrereqs`, persist `MissingPrerequisites`.

**`reserveOrLoad()` case `StateReservedPendingPrereqs`**: Replace the Part C "always transition to processing" with a call to `checkPrerequisites()`. If prerequisites are now satisfied, transition and proceed. If still missing, return the record (Admit returns nil — the event is deferred, not failed).

### 2.3 New fields on `AdmissionRecord`

```go
type AdmissionRecord struct {
    // ... existing fields ...
    MissingPrerequisites []event.EventID `json:"missing_prerequisites,omitempty"`
}
```

`MissingPrerequisites` stores the EventIDs that were valid (DAG-reachable) but not yet projected locally at deferral time. Updated on each re-check: prerequisites that have since been projected are removed. When the slice is empty, the record transitions to `StateProcessing`.

---

## 3. Deferral re-attempt logic

### 3.1 Design: event-driven re-check via `NotifyProjection`

The dispatcher exposes a new method:

```go
func (d *Dispatcher) NotifyProjection(projectedEventID event.EventID)
```

Called whenever a new event is committed to the DAG (wired from the DAG's `onCommit` hook or recognition bus dispatch). The method checks the deferral index and re-attempts admission for any deferred records that were waiting on `projectedEventID`.

### 3.2 Deferral index

An in-memory index maintained by the dispatcher:

```go
type Dispatcher struct {
    // ... existing fields ...
    deferralIndex map[event.EventID][]string // prereq EventID → [admission keys waiting on it]
}
```

**Populated**: When an admission is deferred, each `EventID` in `MissingPrerequisites` is added to the index pointing to the admission key.

**Queried**: `NotifyProjection(id)` looks up `deferralIndex[id]` and re-attempts admission for each waiting key.

**Cleaned**: When a deferred admission transitions to `processing` or is removed, its entries are cleaned from the index.

**Persistence**: The index is NOT persisted — it is rebuilt from `AllAdmissions()` at startup during `Recover()`. This is safe because the index is derived from the persisted `MissingPrerequisites` field on admission records.

### 3.3 Determinism (D-2)

The deferral decision is a pure function of:
1. The consumer's `Prerequisites(ev)` return value (deterministic per consumer).
2. The DAG-reachability check (deterministic per DAG state).
3. The local DAG projection state (`dag.Get` success/failure).

No wall-clock, no ephemeral memory, no randomness. Two nodes processing the same DAG arrive at the same deferral set.

### 3.4 Wiring `NotifyProjection`

`NotifyProjection` is called from the DAG's `onCommit` hook, which already fires for every committed event (local, remote, repair, replay). The dispatcher registers a callback:

```go
// In cmd/node/main.go startup wiring
dag.SetOnCommit(func(ev *event.Event, replay bool) {
    // existing onCommit work ...
    dispatcher.NotifyProjection(ev.ID)
})
```

This ensures deferred records are re-checked whenever any new event is committed, regardless of source. The check is lightweight: one map lookup per committed event. If the committed event is not in the deferral index, no work is done.

---

## 4. Evidence event emission

### 4.1 New event type

```go
// internal/event/event.go
EventTypePrerequisiteWithholding EventType = "PrerequisiteWithholding"
```

### 4.2 Payload structure

```go
// internal/event/event.go
type PrerequisiteWithholdingPayload struct {
    Version              uint8          `json:"v"`
    StuckEventID         event.EventID  `json:"stuck_event_id"`
    StuckEventType       string         `json:"stuck_event_type"`
    MissingPrerequisites []event.EventID `json:"missing_prerequisites"`
    DeferredSinceEpoch   uint64         `json:"deferred_since_epoch"`
    CurrentEpoch         uint64         `json:"current_epoch"`
    EmittingNodeAgent    string         `json:"emitting_node_agent"`
}
```

### 4.3 Emission path

The dispatcher does NOT hold a `localpub.Publisher` directly (that would create a circular dependency: dispatch → localpub → dag → ... → dispatch). Instead, the dispatcher defines an `EvidenceEmitter` function type:

```go
type EvidenceEmitter func(ev *event.Event) error
```

Injected at construction via `SetEvidenceEmitter(fn EvidenceEmitter)`. In `cmd/node/main.go`, the emitter is wired to `publisher.Publish`. This keeps the dispatcher dependency-free from localpub.

### 4.4 Emission trigger

During `NotifyProjection` (or a periodic epoch-tick check), for each deferred record:

```go
age := currentEpoch - rec.CreatedAtEpoch
if age >= DeferralComplaintThreshold && !rec.EvidenceEmitted {
    // Emit PrerequisiteWithholding event
    rec.EvidenceEmitted = true
    // persist updated record
}
```

A new boolean field `EvidenceEmitted` on `AdmissionRecord` prevents duplicate evidence emission. Once emitted, the evidence is canonical and propagates via the DAG. The slashing consumer that acts on it is out of scope.

### 4.5 Failover threshold

```go
if age >= DeferralFailoverThreshold {
    // Single-node case: fail startup on next restart.
    // The check fires during Recover() for records that have been
    // deferred for more than DeferralFailoverThreshold epochs.
    return fmt.Errorf("dispatch: admission %s deferred for %d epochs (threshold %d); "+
        "manual intervention required", rec.Key, age, DeferralFailoverThreshold)
}
```

Network-level recovery detection (checking if other nodes are also stuck) is out of scope for Part D.

---

## 5. Epoch-based threshold tracking

### 5.1 How the dispatcher knows deferral duration

`AdmissionRecord.CreatedAtEpoch` (already present from Part C) stores the epoch at reservation time. The current epoch is obtained via `d.epochFn()`. The deferral duration is `currentEpoch - rec.CreatedAtEpoch`.

No additional epoch-tracking field is needed: `CreatedAtEpoch` serves as the deferral start time because a record is created at the moment it enters `reserved-pending-prerequisites`.

### 5.2 Threshold constants

```go
// internal/dispatch/config.go (new file)
const (
    DeferralComplaintThreshold uint64 = 30  // epochs; locked by §5.4
    DeferralFailoverThreshold  uint64 = 100 // epochs; locked by §5.4
)
```

### 5.3 When thresholds are checked

During `NotifyProjection()` calls: each re-check of a deferred record also checks the epoch thresholds. This piggybacks on the existing per-event check rather than requiring a separate periodic timer. If no events are being committed (dead network), the thresholds are checked during `Recover()` at startup.

Additionally, the failover threshold is checked during `Recover()` for records that have been deferred since before the restart.

---

## 6. Schema version mismatch handling

### 6.1 Flow on startup

During `Recover()`, for each admission record in non-terminal state:

```go
for name, status := range rec.Consumers {
    if status == ConsumerApplied {
        continue
    }
    c, ok := consumers[name]
    if !ok {
        continue // orphaned consumer
    }
    if rec.PrerequisiteSchemaVersion != c.PrerequisiteSchemaVersion() {
        return fmt.Errorf(
            "dispatch: schema version mismatch for consumer %q on admission %s: "+
            "record has version %d, consumer declares version %d. "+
            "Operator action: complete in-flight records under the old binary, "+
            "or clear non-applied local admission state (dispatch: BadgerDB prefix) "+
            "after verifying no canonical effects were committed. "+
            "No canonical ledger rollback is implied.",
            name, rec.Key, rec.PrerequisiteSchemaVersion, c.PrerequisiteSchemaVersion())
    }
}
```

### 6.2 Diagnostic surfacing

The mismatch returns an error from `Recover()`, which propagates to `cmd/node/main.go` startup and causes `os.Exit(1)` with the diagnostic in the structured log. This matches the existing pattern for startup failures (e.g., `LoadApplied` errors).

### 6.3 What "clearing" means

The diagnostic explicitly states that clearing applies to local dispatcher admission state only — the `dispatch:` BadgerDB key prefix. It does not imply clearing the canonical ledger (`txf:`, `gen:`, etc.) or any consensus-derived state. The operator runs a targeted key-prefix delete, not a full DB wipe.

---

## 7. Conformance suite extension

### 7.1 New tests in Type A template

Added to `internal/dispatch/conformance/type_a.go`:

- **`PrerequisiteDeferral`**: Consumer returns non-empty `Prerequisites` for a test event. The dispatcher defers admission. Verify the record is in `reserved-pending-prerequisites`.
- **`PrerequisiteForgeryRejected`**: Consumer returns a prerequisite EventID that is NOT a DAG ancestor of the triggering event. Verify admission fails with the forgery diagnostic.
- **`PrerequisiteSatisfiedAfterProjection`**: Consumer returns a prerequisite. First Admit defers. Then the prerequisite is projected (via `NotifyProjection`). Verify the deferred admission transitions to `processing` and Apply is invoked.

### 7.2 New tests in shared suite

- **`SchemaVersionMismatch`**: Register consumer with version 1. Create a deferred record with version 1. Change consumer to version 2. Call Recover. Assert startup abort with the diagnostic.

---

## 8. Test plan

### 8.1 Unit tests — DAG reachability (existing API, new test in `internal/dispatch/`)

- `TestPrerequisiteValidation_AncestorIsValid` — prereq that is a DAG ancestor of the event passes.
- `TestPrerequisiteValidation_NonAncestorIsRejected` — prereq not in DAG ancestor set fails with `ErrPrerequisiteForgery`.
- `TestPrerequisiteValidation_EmptyPrerequisites` — consumer returns nil; no validation needed, proceeds directly to processing.

### 8.2 Unit tests — deferral index (`internal/dispatch/`)

- `TestDeferralIndex_AddAndLookup` — add a deferred record; lookup by prereq EventID returns the admission key.
- `TestDeferralIndex_CleanOnTransition` — deferred record transitions to processing; index no longer contains its entries.
- `TestDeferralIndex_RebuildFromStore` — `AllAdmissions()` returns deferred records; `Recover()` rebuilds the index.

### 8.3 Unit tests — evidence emission (`internal/dispatch/`)

- `TestEvidenceEmission_AtComplaintThreshold` — deferral at epoch 0, current epoch 30; evidence emitted.
- `TestEvidenceEmission_BelowThreshold` — deferral at epoch 0, current epoch 29; no evidence.
- `TestEvidenceEmission_OnlyOnce` — evidence not re-emitted on subsequent checks.
- `TestEvidenceEmission_PayloadFields` — emitted event has correct stuck-event-ID, missing prereqs, epochs.

### 8.4 Unit tests — schema version (`internal/dispatch/`)

- `TestSchemaVersion_MatchPasses` — record version 1, consumer version 1; Recover succeeds.
- `TestSchemaVersion_MismatchFailsStartup` — record version 1, consumer version 2; Recover returns error with diagnostic.

### 8.5 Unit tests — failover threshold (`internal/dispatch/`)

- `TestFailoverThreshold_AtThresholdFailsStartup` — deferral at epoch 0, current epoch 100; Recover returns error.
- `TestFailoverThreshold_BelowThresholdContinues` — deferral at epoch 0, current epoch 99; Recover succeeds.

### 8.6 Integration test — full deferral flow (`internal/dispatch/`)

- `TestDispatcher_DeferralFlow_EndToEnd` — synthetic consumer with `Prerequisites` returning one EventID. First `Admit` defers. `NotifyProjection(prereqID)` triggers re-check. Second pass transitions to `processing` and invokes Apply. Assert exactly-once Apply.

### 8.7 Integration test — forgery rejection

- `TestDispatcher_ForgeryRejection` — consumer returns a non-ancestor prereq. Assert `Admit` returns `ErrPrerequisiteForgery`. Assert no admission record created.

---

## 9. Sub-commit ordering

Estimated 9 sub-commits. Each self-contained; `go test -race ./...` passes at every commit boundary.

1. **Add `EventTypePrerequisiteWithholding` + payload to `internal/event/`.**
   - New constant and payload struct.
   - Verify: `go test ./internal/event/...` passes.

2. **Add `config.go` with deferral threshold constants.**
   - `DeferralComplaintThreshold = 30`, `DeferralFailoverThreshold = 100`.
   - Verify: `go build ./internal/dispatch/...`.

3. **Add `MissingPrerequisites` and `EvidenceEmitted` fields to `AdmissionRecord`.**
   - Extend types.go. Add `EvidenceEmitter` type and `SetEvidenceEmitter` on Dispatcher.
   - Verify: `go test ./internal/dispatch/...` passes (existing tests unchanged).

4. **Add `ErrPrerequisiteForgery` + prerequisite validation logic.**
   - New file `prerequisites.go` with `checkPrerequisites()` and `validatePrereqReachability()`.
   - Unit tests for validation.
   - Verify: `go test ./internal/dispatch/...` passes.

5. **Add deferral index + `NotifyProjection` method.**
   - Deferral index map, add/remove/lookup, NotifyProjection method, rebuild during Recover.
   - Unit tests for the index.
   - Verify: `go test ./internal/dispatch/...` passes.

6. **Wire prerequisites into `Admit` and `Recover`.**
   - Modify `createReservation`, `reserveOrLoad`, `Recover` to use prerequisite checks.
   - Replace Part C "always transition to processing" with actual prerequisite gating.
   - Integration tests: full deferral flow, forgery rejection.
   - Verify: `go test -race ./internal/dispatch/...` passes.

7. **Add evidence emission + threshold checks.**
   - Wire `EvidenceEmitter` into `NotifyProjection` for complaint threshold.
   - Wire failover threshold into `Recover`.
   - Unit tests for emission and threshold behavior.
   - Verify: `go test ./internal/dispatch/...` passes.

8. **Add schema version mismatch handling to `Recover`.**
   - Mismatch detection + diagnostic error return.
   - Unit tests.
   - Verify: `go test ./internal/dispatch/...` passes.

9. **Extend conformance suite + plan document.**
   - Add PrerequisiteDeferral, ForgeryRejected, PrerequisiteSatisfiedAfterProjection tests to Type A template.
   - Add SchemaVersionMismatch test.
   - Include plan document.
   - Verify: `go test -race ./...` passes across the full repo.

---

## 10. Out of scope for Part D

- **Slashing logic for `PrerequisiteWithholding` evidence.** Follow-up workstream.
- **Network-level recovery detection at failover threshold.** Part D implements single-node fail-startup only.
- **Real consumers using `Prerequisites`.** First real consumer wired in commit 9 of §9.
- **Parts A, B, consumer wiring, cross-cutting, Part F.** Separate prompts.
- **Live testnet verification.** After full workstream ships.

---

## 11. Sign-off

This plan is in draft awaiting founder approval. Per CLAUDE.md §1, no code is written until sign-off.
