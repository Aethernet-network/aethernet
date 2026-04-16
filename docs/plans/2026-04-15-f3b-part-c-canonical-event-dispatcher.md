# F3-B Fix Workstream — Part C: CanonicalEventDispatcher Primitive

**Workstream parent**: `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (locked v3-final). Specifically §4 (Part C, invariants C-1 through C-16), §0 (locked decisions #6, #9, #10), §9 (sequencing), §10 (success criteria), §11 (out of scope), §12 (future-consumer taxonomy).

**Integration branch**: `feat/settlement-consensus-integrity-fix` (currently at commit `dfe7f7b` after Part E).

**Merge constraint**: No merge to main until the full F3-B workstream passes §10 end-to-end testnet verification.

**Status**: Draft, awaiting founder sign-off.

---

## 1. Package structure

### 1.1 New package `internal/dispatch/`

```
internal/dispatch/
  types.go           # ConsumerType enum, AdmissionState enum, per-consumer status,
                     # AdmissionRecord struct, RecoveryStatus enum
  consumer.go        # Consumer interface definition + structural validation
  dispatcher.go      # CanonicalEventDispatcher struct, Register, Admit, Recover
  admission.go       # Admission state machine (check-and-set, state transitions)
  keys.go            # BLAKE3 admission key computation, canonicalization wrapper
  anchor.go          # DAG-anchor verification primitive
  projection.go      # Projection registry entry for dispatcher admission state
  conformance/
    suite.go          # Shared conformance test scaffolding
    type_a.go         # Type A (single-event projection) 6-test template
    type_b.go         # Type B (multi-event state-machine) template
    type_c.go         # Type C (externalization) template
    type_d.go         # Type D (deadline/deferred) template
    synthetic_test.go # Part C's own synthetic consumers for self-test
  lint/
    lint.go           # No-bypass CI lint: AST walk + type-graph analysis
    lint_test.go      # TestNoBypassLint, pragma recognition, marketplace exemption
```

### 1.2 Lint location: `internal/dispatch/lint/` (new, not folded into `internal/projections/lint/`)

**Justification**: The projection lint checks that durable stores have projection-registry entries. The dispatch lint checks that canonical-event consumers are wired through the dispatcher, not directly to the fabric. These are structurally different analyses on different type graphs. Folding them together would create a single lint package that must understand both persistence patterns and dispatch patterns — violating the single-responsibility principle. Separate packages can share the pragma-parsing utility (extract to a shared `internal/lintutil/` package or inline the regex) without conflating their concerns.

The pragma prefix is `dispatch:lint` (vs `projections:lint` for the projection lint), matching the Part E exemption string already committed at `internal/marketplace/server.go:355`.

### 1.3 Store interface

The dispatcher does NOT import `internal/store` directly. It defines a narrow persistence interface:

```go
// internal/dispatch/dispatcher.go

type AdmissionStore interface {
    GetAdmission(key string) (*AdmissionRecord, error)         // ErrNotFound if absent
    PutAdmission(key string, record *AdmissionRecord) error
    AllAdmissions() ([]*AdmissionRecord, error)                 // for crash recovery scan
}
```

`internal/store/store.go` gains `PutAdmission`, `GetAdmission`, `AllAdmissions`, `DeleteAdmission` methods using key prefix `"dispatch:"`. The `Store` struct satisfies `AdmissionStore`.

---

## 2. Admission record schema

### 2.1 Go types

```go
// internal/dispatch/types.go

type AdmissionState uint8

const (
    StateAbsent                    AdmissionState = 0 // implicit: no record in DB
    StateReservedPendingPrereqs    AdmissionState = 1
    StateProcessing                AdmissionState = 2
    StateApplied                   AdmissionState = 3
    StateFailedRetryable           AdmissionState = 4
)

type PerConsumerStatus uint8

const (
    ConsumerPending         PerConsumerStatus = 0
    ConsumerApplied         PerConsumerStatus = 1
    ConsumerFailedRetryable PerConsumerStatus = 2
)

type AdmissionRecord struct {
    SchemaVersion            uint32                       `json:"schema_version"`
    State                    AdmissionState               `json:"state"`
    DAGAnchor                event.EventID                `json:"dag_anchor"`
    PrerequisiteSchemaVersion uint32                      `json:"prerequisite_schema_version"`
    Consumers                map[string]PerConsumerStatus `json:"consumers"`
    EventID                  event.EventID                `json:"event_id"`
    EventType                string                       `json:"event_type"`
    CreatedAtEpoch           uint64                       `json:"created_at_epoch"`
}
```

### 2.2 BadgerDB key prefix

```
dispatch:<blake3-hex-of-canonical-bytes>
```

Matches existing convention (colon-separated, prefix per namespace). The key is the BLAKE3 hash of the JCS-canonicalized event bytes — the same `eventCanonical` struct used by `ComputeID`, but hashed with BLAKE3 instead of SHA-256.

### 2.3 Serialization format

JSON, matching every other store namespace (`evt:`, `txf:`, `ocs:`, `esc:`, etc.). The existing `json.Marshal` / `json.Unmarshal` pattern used by `store.PutEvent`, `store.PutEscrow`, etc. is reused.

### 2.4 Schema versioning

`SchemaVersion` is set to `1` for the initial implementation. If the admission record's on-disk format changes in a future version:
1. Load code checks `SchemaVersion` and rejects unknown versions with a fail-startup diagnostic.
2. Migration is out of scope for this workstream (testnet wipe-without-ceremony is standard per §0.5).

This is distinct from `PrerequisiteSchemaVersion` (Part D's per-consumer prerequisite semantics version stored at reservation time).

---

## 3. Consumer interface

### 3.1 Go interface definition

```go
// internal/dispatch/consumer.go

type Consumer interface {
    // Name returns a unique, stable identifier for this consumer
    // (e.g., "settlement.SettlementConsumer").
    Name() string

    // Type returns the consumer's taxonomy category per §12.
    Type() ConsumerType

    // Interested reports whether this consumer should be invoked for ev.
    // Must be fast and allocation-free.
    Interested(ev *event.Event) bool

    // Apply processes a canonical event. Must commit its authoritative local
    // durable state transition atomically per C-11. Must be idempotent.
    Apply(ctx context.Context, ev *event.Event) error

    // RecoveryProbe determines whether Apply completed for ev during a
    // prior invocation that was interrupted by a crash. Must be
    // evidence-based, monotonic, and replay-safe per C-14. Must not
    // consult wall-clock, ephemeral memory, or external systems.
    RecoveryProbe(ctx context.Context, ev *event.Event) (RecoveryStatus, error)

    // Prerequisites returns the EventIDs that must be projected locally
    // before this consumer can process ev. Stubbed in Part C; implemented
    // in Part D. Return nil to indicate no prerequisites.
    Prerequisites(ev *event.Event) []event.EventID

    // PrerequisiteSchemaVersion returns the version of the prerequisite
    // semantics this consumer uses. Stubbed in Part C; implemented in
    // Part D. Return 0 for "no versioning yet".
    PrerequisiteSchemaVersion() uint32
}

type ConsumerType uint8

const (
    TypeA ConsumerType = iota + 1 // Single-event projection
    TypeB                         // Multi-event state-machine
    TypeC                         // Externalization
    TypeD                         // Deadline/deferred
)

type RecoveryStatus uint8

const (
    RecoveryNotStarted RecoveryStatus = iota
    RecoveryCompleted
)
```

### 3.2 Registration metadata

At registration time, the dispatcher performs structural validation (invariant C-8):

1. `Name()` returns a non-empty string.
2. `Type()` returns a valid `ConsumerType` (1-4).
3. The consumer implements all required interface methods (enforced by the Go type system).
4. `PrerequisiteSchemaVersion()` returns a value (0 is valid for Part C stubs).
5. No duplicate `Name()` among registered consumers.

Structural validation failure aborts startup with a diagnostic. Behavioral conformance is enforced in CI via the conformance suite (§8).

---

## 4. Five-state machine

### 4.1 Transition diagram

```
                    +-----------+
                    |  absent   |  (no record in DB)
                    +-----+-----+
                          |
                    Admit (first delivery)
                          |
                    +-----v-----+
                    | reserved- |
                    | pending-  |
                    | prereqs   |
                    +-----+-----+
                          |
               Prerequisites satisfied
                    (Part D; always
                     true in Part C)
                          |
                    +-----v-----+
                    | processing|
                    +-----+-----+
                         / \
                        /   \
              all ok   /     \  any fail
                      /       \
               +-----v-+   +--v-----------+
               | applied|   | failed-      |
               +--------+   | retryable    |
                             +------+------+
                                    |
                              Re-delivery
                              (retry failed
                               consumers only)
                                    |
                              +-----v-----+
                              | processing|
                              +-----+-----+
                                   / \
                                  ... (loop)
```

### 4.2 Transition rules

| From | To | Trigger | Atomicity |
|------|----|---------|-----------|
| absent | reserved-pending-prereqs | First Admit call; identity reserved | BadgerDB transaction (C-2) |
| reserved-pending-prereqs | processing | Prerequisites satisfied (always true in Part C) | BadgerDB transaction |
| processing | applied | All consumers reach per-consumer `applied` | BadgerDB transaction |
| processing | failed-retryable | Any consumer reaches per-consumer `failed-retryable` | BadgerDB transaction |
| failed-retryable | processing | Re-delivery; retry failed consumers | BadgerDB transaction |
| applied | (terminal) | — | — |

### 4.3 What happens outside the transaction

Per invariant C-5 (reservation transaction minimality):

- **Inside the transaction**: Read existing record (or confirm absent), write the new state, verify DAG anchor. Minimal I/O only.
- **Outside the transaction (after)**: Invoke `consumer.Apply()` for each pending consumer, invoke prerequisite checks (Part D), invoke `RecoveryProbe` (crash recovery).

### 4.4 Error handling

- **Transaction conflict** (`badger.ErrConflict`): `db.Update` handles retries internally. The dispatcher relies on this.
- **Key not found** (`badger.ErrKeyNotFound`): Normal for first admission — record is absent.
- **Any other storage error**: Dispatcher refuses to invoke consumers (C-7 fail-closed). Returns error to caller.
- **Consumer Apply error**: Transition per-consumer status to `failed-retryable`. Compute top-level state. Persist updated record.
- **Consumer Apply panic**: Recovered at the dispatch boundary. Treated as `failed-retryable`.

---

## 5. Atomicity primitive

### 5.1 Admission check-and-set

```go
func (d *Dispatcher) admit(ctx context.Context, ev *event.Event, canonicalBytes []byte) error {
    // Step 1: Compute admission key OUTSIDE the transaction (C-3, C-5).
    key := admissionKey(canonicalBytes)

    // Step 2: Atomic read-modify-write INSIDE the transaction.
    err := d.store.DB().Update(func(txn *badger.Txn) error {
        // Read existing record.
        existing, err := getAdmissionTxn(txn, key)
        if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
            return err // fail-closed (C-7)
        }

        // DAG-anchor verification (C-6).
        if existing != nil {
            if err := d.verifyAnchor(txn, existing.DAGAnchor); err != nil {
                return err
            }
        }

        // State-dependent action.
        switch {
        case existing == nil:
            // absent → reserved-pending-prereqs
            return putAdmissionTxn(txn, key, d.newReservation(ev))

        case existing.State == StateApplied:
            return nil // already applied — no-op (C-1)

        case existing.State == StateFailedRetryable:
            // Re-admit: transition to processing for failed consumers
            existing.State = StateProcessing
            return putAdmissionTxn(txn, key, existing)

        case existing.State == StateReservedPendingPrereqs:
            return nil // waiting for prerequisites (Part D)

        case existing.State == StateProcessing:
            return nil // already processing — concurrent delivery
        }
        return nil
    })
    if err != nil {
        return err
    }

    // Step 3: Outside the transaction — invoke consumers.
    return d.invokeConsumers(ctx, ev, key)
}
```

### 5.2 Canonicalization

The dispatcher reuses the existing `eventCanonical` struct from `internal/event/event.go:273` to produce canonical bytes. It then hashes with BLAKE3 (new dependency: `lukechampine.com/blake3`):

```go
// internal/dispatch/keys.go

func CanonicalizeEvent(ev *event.Event) ([]byte, error) {
    canon := event.EventCanonical(ev) // new exported helper in event package
    data, err := json.Marshal(canon)
    if err != nil { return nil, err }
    return jcs.Canonicalize(data)
}

func admissionKey(canonicalBytes []byte) string {
    sum := blake3.Sum256(canonicalBytes)
    return "dispatch:" + hex.EncodeToString(sum[:])
}
```

The `event` package gains an exported `EventCanonical(e *Event) eventCanonical` function that returns the canonical projection struct. This avoids duplicating the field selection logic.

### 5.3 DAG-anchor verification

Per C-6 and C-13:

```go
// internal/dispatch/anchor.go

type DAGAnchorReader interface {
    Tips() []event.EventID
    IsAncestor(ancestor, descendant event.EventID) (bool, error)
}

func (d *Dispatcher) verifyAnchor(currentTips []event.EventID, storedAnchor event.EventID) error {
    if storedAnchor == "" {
        return nil // first admission, no anchor stored yet
    }
    for _, tip := range currentTips {
        if tip == storedAnchor {
            return nil // anchor IS a current tip
        }
        isAnc, err := d.dag.IsAncestor(storedAnchor, tip)
        if err != nil {
            continue // tip might not be reachable; try others
        }
        if isAnc {
            return nil // anchor is ancestor of a tip — valid
        }
    }
    return ErrCorruptedAdmissionState
}
```

Note: The locked design says the verification happens inside the transaction, but `IsAncestor` requires the DAG's read lock (which is separate from BadgerDB transactions). The DAG anchor verification reads the current DAG tips before opening the BadgerDB transaction, then uses the snapshot for validation inside. This is safe because DAG tips only advance (append-only); a tip valid before the transaction is still valid after.

**Revised flow**: Read tips before transaction → open transaction → compare stored anchor against captured tips → proceed.

---

## 6. Per-(event, consumer) completion tracking

### 6.1 Data shape

```go
type AdmissionRecord struct {
    // ... (see §2.1)
    Consumers map[string]PerConsumerStatus `json:"consumers"`
}
```

The map key is `consumer.Name()`. The map is populated at reservation time with all registered consumers that return `Interested(ev) == true`, each initialized to `ConsumerPending`.

### 6.2 Top-level state computation

```go
func computeTopLevelState(consumers map[string]PerConsumerStatus) AdmissionState {
    allApplied := true
    for _, status := range consumers {
        switch status {
        case ConsumerFailedRetryable:
            return StateFailedRetryable
        case ConsumerPending:
            allApplied = false
        }
    }
    if allApplied {
        return StateApplied
    }
    return StateProcessing
}
```

### 6.3 Consumer add/remove across upgrades

If a consumer is registered in version N but not in version N+1, its per-consumer entry in existing admission records becomes orphaned. On recovery, the dispatcher ignores orphaned consumer entries (consumers not currently registered are skipped). This is safe because:

- The dispatcher only invokes currently-registered consumers.
- Orphaned entries do not affect the top-level state computation (they are excluded from the iteration).
- The admission record is local machinery (C-15), not consensus state.

If a consumer is added in version N+1 that was not present in version N, existing `applied` records are unaffected (the event was fully processed by the consumer set that existed at admission time). New deliveries of future events include the new consumer in their consumer map.

---

## 7. Crash-recovery algorithm

### 7.1 Startup sequence

Called from `cmd/node/main.go` after the dispatcher is constructed, before any event-processing goroutine starts (matching the load-before-listener pattern from Parts A/B):

```go
func (d *Dispatcher) Recover(ctx context.Context) error {
    records, err := d.store.AllAdmissions()
    if err != nil {
        return fmt.Errorf("dispatch: recovery scan failed: %w", err)
    }

    for _, rec := range records {
        switch rec.State {
        case StateReservedPendingPrereqs:
            // Re-check prerequisites (Part D). In Part C, prerequisites
            // are always satisfied, so transition to processing.
            // Part D will add the actual prerequisite re-check here.
            rec.State = StateProcessing
            if err := d.store.PutAdmission(rec.Key, rec); err != nil {
                return err
            }
            // Fall through to processing recovery below.
            fallthrough

        case StateProcessing:
            // For each consumer with status=pending, invoke RecoveryProbe.
            ev, err := d.dag.Get(rec.EventID)
            if err != nil {
                return fmt.Errorf("dispatch: recovery: event %s not in DAG: %w",
                    rec.EventID, err)
            }
            for name, status := range rec.Consumers {
                if status != ConsumerPending {
                    continue
                }
                consumer, ok := d.consumers[name]
                if !ok {
                    continue // orphaned consumer; skip
                }
                probeResult, probeErr := consumer.RecoveryProbe(ctx, ev)
                if probeErr != nil {
                    return fmt.Errorf("dispatch: recovery probe %s for event %s failed: %w",
                        name, rec.EventID, probeErr)
                }
                switch probeResult {
                case RecoveryCompleted:
                    rec.Consumers[name] = ConsumerApplied
                case RecoveryNotStarted:
                    rec.Consumers[name] = ConsumerFailedRetryable
                }
            }
            rec.State = computeTopLevelState(rec.Consumers)
            if err := d.store.PutAdmission(rec.Key, rec); err != nil {
                return err
            }

        case StateFailedRetryable:
            // No action. Next delivery re-attempts.

        case StateApplied:
            // Terminal. No action.
        }
    }
    return nil
}
```

### 7.2 Deterministic resolution

- `RecoveryCompleted` → per-consumer `applied`. **Only** when the probe finds positive evidence in durable state.
- `RecoveryNotStarted` → per-consumer `failed-retryable`. Next delivery retries.
- Probe error → fail-startup. Manual intervention required.
- Detected partial state → fail-startup (atomic-batch makes this impossible for Type A consumers; Type C consumers detect it via outbox journal inspection).

---

## 8. Conformance test suite design

### 8.1 Location and integration

`internal/dispatch/conformance/` exports test helper functions that consumer packages import and run in their own `_test.go` files. The suite runs as part of `go test ./...` — no separate CI step.

### 8.2 Type A template (6 tests)

```go
// internal/dispatch/conformance/type_a.go

func RunTypeAConformance(t *testing.T, factory func() (Consumer, cleanup)) {
    t.Run("DuplicateLiveDelivery", ...)        // Deliver same event twice; assert Apply called once
    t.Run("ReplayDelivery", ...)                // Deliver event with replay=true; assert idempotent
    t.Run("CrashRecovery", ...)                 // Apply succeeds; RecoveryProbe returns Completed
    t.Run("ConcurrentSameEvent", ...)           // Two goroutines deliver same event; assert no race
    t.Run("ContentHashDiscrimination", ...)     // Two events with different canonical bytes; assert distinct admission
    t.Run("CausalPrerequisiteDeferral", ...)    // Part D stub: consumer returns non-empty Prerequisites; assert deferral
}
```

### 8.3 Type B template (Type A + additional)

```go
func RunTypeBConformance(t *testing.T, factory func() (Consumer, cleanup)) {
    RunTypeAConformance(t, factory) // inherits all 6 baseline tests
    t.Run("StateMachineTransitions", ...)       // Multi-event sequence; assert legal transitions
    t.Run("IllegalTransitionRejected", ...)     // Out-of-order event; assert error or defer
}
```

### 8.4 Type C template (Type A + additional)

```go
func RunTypeCConformance(t *testing.T, factory func() (Consumer, cleanup)) {
    RunTypeAConformance(t, factory)
    t.Run("OutboxAtomicWrite", ...)             // Apply writes to outbox atomically
    t.Run("IdempotentSink", ...)                // External sink called twice; assert no side-effect duplication
}
```

### 8.5 Type D template (Type A + additional)

```go
func RunTypeDConformance(t *testing.T, factory func() (Consumer, cleanup)) {
    RunTypeAConformance(t, factory)
    t.Run("DeadlineBasisCanonical", ...)        // Deadline basis from canonical event, not wall-clock
}
```

### 8.6 Synthetic consumer for Part C self-test

`internal/dispatch/conformance/synthetic_test.go` defines a minimal Type A consumer with an in-memory map as its "durable store" (no actual BadgerDB needed for the conformance scaffolding test). This consumer is used to verify that the conformance suite itself works. Real consumers (settlement, reputation, etc.) run the suite against their actual implementations in their own packages.

---

## 9. No-bypass CI lint design

### 9.1 Detection approach

The lint scans the Go type graph for any type that:
1. Implements the `dispatch.Consumer` interface (by satisfying all required methods), AND
2. Is referenced in a `recognition.Bus.Register()` call or equivalent fabric-wiring pattern, WITHOUT
3. Also being referenced in a `dispatch.Dispatcher.Register()` call.

**Simplified approach** (matching the projection-lint pattern from step 3): Instead of full type-graph analysis, scan for the `dispatch.Consumer` interface satisfaction via method-set analysis on the loaded packages, then verify each satisfying type is also registered with the dispatcher. This avoids needing to trace fabric-wiring call graphs.

**Practical implementation**: Since Part C ships with zero consumers registered, the lint in its initial form checks that:
- No type in the codebase satisfies `dispatch.Consumer` without being registered with the dispatcher.
- The marketplace exemption pragma (`dispatch:lint marketplace-exempt "..."`) is recognized and the call site at `internal/marketplace/server.go:355` is not flagged.

### 9.2 Pragma format

```
// dispatch:lint <tag> "<justification ≥20 chars, ≥3 words>"
```

Parsed with regex matching the projection-lint pattern:
```go
var dispatchPragmaRE = regexp.MustCompile(`dispatch:lint\s+(\S+)\s+"([^"]*)"`)
```

Justification must be ≥20 chars and contain ≥3 whitespace-separated words, matching the projection-lint convention.

### 9.3 Integration with `go test ./...`

A `TestNoBypassDispatchLint(t *testing.T)` function in `internal/dispatch/lint/lint_test.go` is the CI gate. It calls `Check(moduleRoot)` which returns a `*Report`. `Report.HasFailures()` causes `t.Fatalf`. Runs automatically on `go test ./...`.

### 9.4 Marketplace exemption recognition

The lint scans for `dispatch:lint` pragma comments. The existing comment at `internal/marketplace/server.go:355`:
```
// dispatch:lint marketplace-exempt "marketplace binary operates own application-layer escrow; protocol-escrow integration tracked as follow-up workstream"
```
is recognized by the pragma parser and suppresses the finding for that call site.

Note: the marketplace `Hold` call does not satisfy the `dispatch.Consumer` interface (it's not a consumer at all — it's a direct escrow call). So the no-bypass lint would not flag it even without the pragma. The pragma exists for defense-in-depth and documentation, per the Part E audit's C3-2 resolution. The lint recognizes it without error.

---

## 10. Replay-state registry extension

### 10.1 Design choice: subcategory enum on `CanonicalProjection`

Add a `Subcategory` field to `CanonicalProjection`:

```go
// internal/projections/types.go

type Subcategory uint8

const (
    SubcategoryProjection       Subcategory = iota // existing projections (default)
    SubcategoryDispatchAdmission                   // dispatcher admission records
    SubcategoryEscrowRegistry                      // escrow registries
    SubcategoryApplicatorApplied                   // applicator applied-sets
)
```

**Justification**: Using a subcategory enum on the existing type (vs a new sibling type) preserves the "single mandatory inventory" invariant (C-9) without splitting the registry into multiple registries that must be cross-checked. The health check, lint, and startup verification all operate on the same `ProjectionRegistry.List()` — no new code paths.

The subcategory field is optional: existing projection entries default to `SubcategoryProjection` (zero value). New entries from Part C set the appropriate subcategory.

### 10.2 Existing entries to formalize

The escrow projection at `internal/escrow/projection.go` already registers via `projections.MustRegister`. The applicator applied-set is not yet registered (Parts A/B handle that). Part C adds:

- **Dispatcher admission state**: a new `CanonicalProjection` entry with `Subcategory: SubcategoryDispatchAdmission`, registered in `cmd/node/main.go` alongside the dispatcher construction.

### 10.3 Health check extension

No changes to the health check algorithm. The existing `HealthCheck` iterates all entries regardless of subcategory and applies the same PR-5 / StateProbe / eligibility-window logic. Subcategory is metadata for the lint and for human operators, not for the health check's decision logic.

### 10.4 Impact on step-1 health check

The step-1 health check passes unchanged because:
- New entries with `StateProbe` set to the dispatcher's "has any admission records?" function are valid Canonical entries.
- The eligibility window (3 epochs) gives the new entry time to accumulate state before PR-5 fires.
- Part C ships with zero consumers, so no admission records will exist until commit 9 wires the first consumer. The dispatcher's StateProbe returns `empty=true`, which is permitted during the eligibility window.

---

## 11. Test plan

### 11.1 Unit tests — state machine (`internal/dispatch/`)

- `TestAdmissionState_AbsentToReserved` — first delivery creates a reserved record.
- `TestAdmissionState_ReservedToProcessing` — prerequisites satisfied transitions to processing.
- `TestAdmissionState_ProcessingToApplied` — all consumers succeed transitions to applied.
- `TestAdmissionState_ProcessingToFailedRetryable` — one consumer fails transitions to failed-retryable.
- `TestAdmissionState_FailedRetryableRedelivery` — re-delivery of same event transitions failed-retryable back to processing, only re-invokes failed consumers.
- `TestAdmissionState_AppliedIsTerminal` — applied record ignores re-delivery.
- `TestAdmissionState_DuplicateDelivery` — same event delivered twice; Apply called once.

### 11.2 Unit tests — admission key (`internal/dispatch/`)

- `TestAdmissionKey_DeterministicFromCanonicalBytes` — same event produces same key.
- `TestAdmissionKey_DifferentEventsProduceDifferentKeys` — two events with different payloads produce different keys.
- `TestAdmissionKey_ContentHashNotEventID` — admission key differs from EventID (BLAKE3 vs SHA-256).

### 11.3 Unit tests — DAG-anchor verification (`internal/dispatch/`)

- `TestDAGAnchor_ValidAncestorRelationship` — stored anchor is ancestor of current tip; passes.
- `TestDAGAnchor_AnchorEqualsTip` — stored anchor IS the current tip; passes.
- `TestDAGAnchor_CorruptedAnchor` — stored anchor is not reachable from any tip; returns error.
- `TestDAGAnchor_EmptyAnchorFirstAdmission` — no stored anchor; passes (first admission).

### 11.4 Unit tests — per-(event, consumer) tracking (`internal/dispatch/`)

- `TestPerConsumer_AllAppliedAdvancesTopLevel` — all per-consumer applied → top-level applied.
- `TestPerConsumer_OneFailedCausesTopLevelFailed` — one failed-retryable → top-level failed-retryable.
- `TestPerConsumer_RetryOnlyFailedConsumers` — on re-delivery, applied consumers are not re-invoked.
- `TestPerConsumer_MultipleConsumers_PartialFailure` — two consumers; one succeeds, one fails; only the failed one retries on re-delivery.

### 11.5 Unit tests — crash recovery (`internal/dispatch/`)

- `TestRecovery_ReservedPendingToProcessing` — record in reserved-pending-prereqs transitions to processing and consumers are probed.
- `TestRecovery_Processing_ProbeCompleted` — probe returns RecoveryCompleted → per-consumer applied.
- `TestRecovery_Processing_ProbeNotStarted` — probe returns RecoveryNotStarted → per-consumer failed-retryable.
- `TestRecovery_Processing_ProbeError` — probe returns error → fail-startup.
- `TestRecovery_FailedRetryable_NoAction` — no recovery action for failed-retryable records.
- `TestRecovery_Applied_NoAction` — no recovery action for applied records.
- `TestRecovery_OrphanedConsumer_Skipped` — consumer no longer registered; orphaned entry skipped.

### 11.6 Unit tests — registration (`internal/dispatch/`)

- `TestRegister_ValidConsumer` — registration succeeds.
- `TestRegister_DuplicateName` — second registration with same Name() fails.
- `TestRegister_InvalidType` — ConsumerType 0 fails structural validation.

### 11.7 Integration test — full admission flow (`internal/dispatch/`)

- `TestDispatcher_FullAdmissionFlow_TypeA` — construct dispatcher with a synthetic Type A consumer; deliver an event via `Admit()`; assert Apply called once; assert per-consumer applied; assert top-level applied.
- `TestDispatcher_DuplicateDelivery_ExactlyOnce` — deliver same event twice; assert Apply called once.
- `TestDispatcher_FailAndRetry` — consumer fails on first delivery; re-deliver; consumer succeeds; assert final state is applied.

### 11.8 Conformance suite scaffolding test (`internal/dispatch/conformance/`)

- `TestTypeA_SyntheticConsumer` — run the 6-test Type A template against the synthetic consumer; assert all pass.
- `TestTypeB_SyntheticConsumer` — run the Type B template against a synthetic Type B consumer; assert all pass.
- `TestTypeC_SyntheticConsumer` — run Type C template.
- `TestTypeD_SyntheticConsumer` — run Type D template.

### 11.9 No-bypass lint test (`internal/dispatch/lint/`)

- `TestNoBypassDispatchLint` — runs against the full repo; asserts no violations (zero consumers registered currently; marketplace exemption recognized).
- `TestPragmaRecognition` — verifies the `dispatch:lint` pragma parser accepts valid pragmas and rejects insufficient ones (length/word count).
- `TestMarketplaceExemption` — specifically verifies the marketplace exemption at `internal/marketplace/server.go:355` is recognized without flagging.

### 11.10 Registry extension test (`internal/projections/`)

- `TestRegistry_SubcategoryField` — register a projection with subcategory; Get returns the subcategory.
- `TestRegistry_DefaultSubcategoryIsProjection` — existing entries without subcategory default to SubcategoryProjection.

---

## 12. Sub-commit ordering on the integration branch

Estimated 12 sub-commits. Each is self-contained; `go test -race ./...` passes at every commit boundary.

1. **Add BLAKE3 dependency.**
   - `go get lukechampine.com/blake3@latest`, `go mod tidy`.
   - Verify: `go build ./...` succeeds.

2. **Add `EventCanonical` export to `internal/event/`.**
   - New function `EventCanonical(e *Event) eventCanonical` in `event.go`.
   - Verify: `go test ./internal/event/...` passes.

3. **Add `Subcategory` to `internal/projections/types.go` + registry extension.**
   - New `Subcategory` field on `CanonicalProjection`, four constants, registry validation tolerates zero-value as default.
   - Verify: `go test ./internal/projections/...` passes; existing entries unchanged.

4. **Create `internal/dispatch/types.go` — all type definitions.**
   - `AdmissionState`, `PerConsumerStatus`, `AdmissionRecord`, `ConsumerType`, `RecoveryStatus`.
   - Verify: `go build ./...` succeeds.

5. **Create `internal/dispatch/consumer.go` — Consumer interface + structural validation.**
   - Interface definition, `validateConsumer()` function.
   - Verify: `go build ./...` succeeds.

6. **Create `internal/dispatch/keys.go` — canonicalization + BLAKE3 admission key.**
   - `CanonicalizeEvent()`, `admissionKey()`.
   - Unit tests for deterministic key generation.
   - Verify: `go test ./internal/dispatch/...` passes.

7. **Create `internal/dispatch/anchor.go` — DAG-anchor verification.**
   - `DAGAnchorReader` interface, `verifyAnchor()`.
   - Unit tests for anchor validation.
   - Verify: `go test ./internal/dispatch/...` passes.

8. **Add dispatcher admission store methods to `internal/store/store.go`.**
   - `PutAdmission`, `GetAdmission`, `AllAdmissions`, `DeleteAdmission` with `dispatch:` prefix.
   - Verify: `go test ./internal/store/...` passes.

9. **Create `internal/dispatch/dispatcher.go` + `admission.go` — the dispatcher core.**
   - `CanonicalEventDispatcher` struct, `Register()`, `Admit()`, `Recover()`.
   - State machine transitions, per-(event, consumer) tracking, fail-closed semantics.
   - Full unit + integration tests from §11.1-§11.7.
   - Verify: `go test -race ./internal/dispatch/...` passes.

10. **Create `internal/dispatch/projection.go` — projection registry entry.**
    - Dispatcher admission state projection entry with `SubcategoryDispatchAdmission`.
    - Verify: `go test ./internal/projections/...` passes.

11. **Create `internal/dispatch/conformance/` — four per-type templates + synthetic consumers.**
    - Templates for Type A, B, C, D.
    - Synthetic consumers and tests from §11.8.
    - Verify: `go test ./internal/dispatch/conformance/...` passes.

12. **Create `internal/dispatch/lint/` — no-bypass CI lint.**
    - AST walk, pragma parser, `TestNoBypassDispatchLint`.
    - Marketplace exemption recognition.
    - Verify: `go test ./internal/dispatch/lint/...` passes; `go test -race ./...` passes across the full repo.

### 12.1 Commit message style

Matches the Part E convention:
```
part-c(dispatch): <imperative subject>

<body explaining the why>

Ref: docs/plans/2026-04-15-f3b-part-c-canonical-event-dispatcher.md §N
Parent: docs/plans/2026-04-15-settlement-consensus-integrity-fix.md §4
```

---

## 13. Out of scope for Part C

- **No consumers are wired.** The dispatcher ships with zero consumers. First consumer wiring is commit 9 of §9 (after Parts A, B, D).
- **Part D prerequisite gating.** The admission state machine includes the `reserved-pending-prerequisites` state, but the actual prerequisite-checking logic is Part D.
- **Parts A/B startup wiring.** Applicator.LoadApplied and Escrow.LoadFromStore.
- **Cross-cutting items.** `esc:` flag fix, synthetic transfer relabeling.
- **Part F.** Historical task annotation.
- **Live testnet verification.** After full workstream ships.

---

## 14. Sign-off

This plan is in draft awaiting founder approval. Per CLAUDE.md §1, no code is written until the founder signs off. Implementation proceeds per §12 ordering on the integration branch.
