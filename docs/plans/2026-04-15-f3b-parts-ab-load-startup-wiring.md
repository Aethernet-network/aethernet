# F3-B Fix Workstream — Parts A+B: Startup Wiring for LoadApplied and LoadFromStore

**Workstream parent**: `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (locked v3-final). §2 (Part A, invariants A-1 through A-4), §3 (Part B, invariants B-1 through B-4), §4.12 (single mandatory replay-state registry).

**Integration branch**: `feat/settlement-consensus-integrity-fix` (currently at commit `53009e2` after Part D).

**Merge constraint**: No merge to main until the full F3-B workstream passes §10 end-to-end testnet verification.

**Status**: Draft, awaiting founder sign-off.

---

## 1. Startup ordering in `cmd/node/main.go`

### 1.1 Current state

- **Escrow construction**: line 934 (`escrow.New(tl)`), `SetStore(s)` at line 936, `SetDAGReader(d)` at line 941. `LoadFromStore` is **never called**.
- **Applicator construction**: line 1595 (`settlement.NewApplicator`), `SetStore(stack.store)` at line 1605. `LoadApplied` is **never called**.
- **Listener boundary**: `commitBus.Start()` at line 1984 is the point after which canonical events can be delivered to consumers.

### 1.2 Insertion points

**Escrow load** — insert after line 941 (`SetDAGReader`), before the reputation manager block at line 943:

```go
// Part B: load persisted escrow entries before any listener starts.
if s != nil {
    loadedEscrow, err := escrow.LoadFromStore(tl, s)
    if err != nil {
        slog.Error("startup: escrow.LoadFromStore failed", "err", err)
        os.Exit(1)
    }
    loadedEscrow.SetDAGReader(d)
    escrowMgr = loadedEscrow
}
```

Note: `escrow.LoadFromStore` is a package-level constructor that returns a `*Escrow`. When the store is present, we replace the `escrow.New(tl)` + `SetStore(s)` pattern with `LoadFromStore(tl, s)` which constructs the escrow AND populates it from persisted entries. The `SetDAGReader(d)` call is re-applied to the loaded instance.

**Applicator load** — insert after line 1605 (`SetStore`), before line 1607 (`SetFeeCollector`):

```go
// Part A: load persisted applied-set before any listener starts.
if stack.store != nil {
    if err := settlementApp.LoadApplied(stack.store.AllMeta); err != nil {
        slog.Error("startup: applicator.LoadApplied failed", "err", err)
        os.Exit(1)
    }
}
```

### 1.3 Load ordering

Both loads are independent (no dependency between escrow entries and applied-set records). They can conceptually run in parallel but run serially for simplicity — escrow loads in `buildStack` (line ~942), applicator loads in `startStack` (line ~1606). Both complete well before `commitBus.Start()` at line 1984.

---

## 2. DAG-anchor verification primitive

### 2.1 Design choice: reuse Part C's `verifyAnchor` from `internal/dispatch/anchor.go`

Part C's `verifyAnchor(dag DAGAnchorReader, storedAnchor event.EventID) error` already implements the exact logic: confirm the stored anchor is an ancestor of (or equal to) at least one current DAG tip. It is exported-by-design and its `DAGAnchorReader` interface (`Tips() + IsAncestor() + Get()`) is satisfied by `*dag.DAG`.

**Justification for reuse over extraction**: The `internal/dispatch` package is already imported by the store (via admission records). Importing `dispatch.verifyAnchor` from the applicator or escrow would create an awkward dependency direction (settlement → dispatch). Instead, extract the anchor verification to a small shared function in `internal/dispatch/anchor.go` and make it a package-level export (it already is: `verifyAnchor` is unexported but the `DAGAnchorReader` interface is exported).

**Implementation**: Export `VerifyAnchor` (capitalize) from `internal/dispatch/anchor.go`. Both the dispatcher and the A+B load paths call the same function.

### 2.2 What the anchor is

For the applicator's applied-set: the anchor is the DAG tip at the time the last applied record was written. Currently the applied-set does NOT store a DAG anchor — `PutMeta("settlement:applied:<id>", verdict)` stores only the verdict bytes. Part A adds a separate meta key `settlement:applied:__dag_anchor__` that stores the DAG tip EventID at each write. On load, this anchor is verified.

For the escrow registry: the anchor is the DAG tip at the time the last escrow entry was written/updated. The escrow store's `PutEscrow` writes JSON including all `EscrowEntry` fields. Part B adds a separate meta key `esc:__dag_anchor__` that stores the current DAG tip at each write.

### 2.3 What happens on mismatch

If the stored anchor is not an ancestor of any current DAG tip, startup fails with:
```
"startup: DAG-anchor verification failed for <subsystem>: stored anchor <hex> is not reachable from any current DAG tip; possible BadgerDB corruption — manual investigation required"
```

---

## 3. Sync semantics on writes

### 3.1 Current state

BadgerDB's `db.Update()` does NOT guarantee fsync by default. BadgerDB batches writes to the value log and WAL; a crash after `Update` returns but before the next fsync could lose the write.

### 3.2 What changes

For the applicator's applied-set write at `applicator.go:281`:
```go
_ = a.store.PutMeta(key, []byte(sp.Verdict))
```

And the DAG-anchor write added by Part A. Both must use the `SyncWrites` option. Since the `appliedStore` interface uses `PutMeta` which goes through `store.Store.PutMeta`, Part A adds a `PutMetaSync` method to the store that sets `badger.Entry.WithMeta` and calls `txn.SetEntry` with `WithSync()` semantics.

**Practical approach**: BadgerDB v4's `DefaultOptions` has `SyncWrites: false` by default. The simplest correct fix: add a `PutMetaSync(key string, value []byte) error` method that opens a transaction with `db.NewWriteBatch()` and `Flush()` with sync, OR use `txn.SetEntry(badger.NewEntry(...).WithMeta(0))` inside `db.Update` + call `db.Sync()` after the transaction.

Actually, the most straightforward approach: add `db.Sync()` after the `db.Update` call in the critical write paths. This is explicit and auditable.

For escrow writes: `store.PutEscrow` at `store.go:780` follows the same pattern. Add `db.Sync()` after the `db.Update` in `PutEscrow`.

### 3.3 Scope of Sync

Sync is added to:
1. `store.PutMeta` (used by applicator applied-set writes)
2. `store.PutEscrow` (used by escrow entry writes)
3. The new DAG-anchor writes for both subsystems

Sync is NOT added to all store writes globally (that would impact performance for non-safety-critical paths like task manager state). Only the settlement-critical paths get Sync.

**Implementation**: Add `SyncAfterWrite() error` helper to the store that calls `s.db.Sync()`. The applicator and escrow call this after their critical writes. This keeps the sync decision at the caller level rather than making all PutMeta/PutEscrow calls synchronous.

---

## 4. `StateProbe` implementations

### 4.1 Applicator applied-set

```go
// internal/settlement/projection.go (new file)

func AppliedSetProjection(a *Applicator) projections.CanonicalProjection {
    return projections.CanonicalProjection{
        Name:           "ApplicatorAppliedSet",
        Package:        "internal/settlement",
        StoreType:      "Applicator",
        Classification: projections.Canonical,
        SourceEvents:   []projections.EventType{"Settlement"},
        LiveConsumerRef:   "internal/recognition.SettlementConsumer",
        ReplayConsumerRef: "internal/settlement.Applicator",
        ObservabilitySurface: projections.Surface{
            Kind:         projections.SurfaceHealth,
            EndpointPath: "/v1/status (settlement.ApplicatorAppliedSet)",
        },
        IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/settlement.TestApplicator_EscrowLockTransfer_RegistersEntry",
        Owner:              "state-and-consensus",
        CreatedAt:          "2026-04-16",
        Subcategory:        projections.SubcategoryApplicatorApplied,
        StateProbe: func(ctx context.Context) (bool, error) {
            return a.AppliedCount() == 0, nil
        },
    }
}
```

`Applicator.AppliedCount()` is a new read-only method returning `len(a.applied)` under read lock.

### 4.2 Escrow registry

The escrow projection already exists at `internal/escrow/projection.go` (from step 2 of the reputation workstream). It registers with `SubcategoryProjection` (the default). Part B updates its `Subcategory` to `SubcategoryEscrowRegistry`.

### 4.3 IntegrationTestRef

- Applicator: `TestApplicator_EscrowLockTransfer_RegistersEntry` (already exists from Part E's commit 7).
- Escrow: `TestEscrow_HoldsOnTransferOptimistic` (already exists in `internal/integration/`, preserved by Part E).

---

## 5. Recovery path integration with dispatcher admission

### 5.1 Load ordering

All three startup loads must complete before `commitBus.Start()`:

1. `escrow.LoadFromStore(tl, s)` — in `buildStack` (~line 934)
2. `settlementApp.LoadApplied(store.AllMeta)` — in `startStack` (~line 1606)
3. `dispatcher.Recover(ctx)` — in `startStack` (after applicator and escrow load, before commitBus.Start)

No dependency between (1) and (2); they load independent state. (3) depends on both being complete because a dispatcher consumer's `RecoveryProbe` may consult the applicator or escrow state.

### 5.2 What fails startup if any load errors

Each returns an error that is logged and causes `os.Exit(1)` (matching the existing `slog.Error + os.Exit(1)` pattern used by DAG/ledger load failures at `main.go:815-822`).

---

## 6. Diagnostic messages

```
"startup: applicator.LoadApplied failed: <wrapped error>"
"startup: escrow.LoadFromStore failed: <wrapped error>"
"startup: applicator DAG-anchor verification failed: stored anchor <hex> not reachable from any current DAG tip; possible BadgerDB corruption — manual investigation required"
"startup: escrow DAG-anchor verification failed: stored anchor <hex> not reachable from any current DAG tip; possible BadgerDB corruption — manual investigation required"
```

---

## 7. Test plan

### 7.1 Part A tests (`internal/settlement/`)

- `TestLoadApplied_RestoresAppliedSet` — persist N applied records, call LoadApplied, verify `a.applied` contains all N target IDs.
- `TestLoadApplied_EmptyStoreIsOK` — no persisted records; LoadApplied returns without error; applied map is empty.
- `TestLoadApplied_ErrorPropagates` — store.AllMeta returns error; LoadApplied returns error (not swallowed).

### 7.2 Part B tests (`internal/escrow/`)

- `TestLoadFromStore_RestoresEntries` — persist entries, load, verify IsLocked returns true for each.
- `TestLoadFromStore_EmptyStoreIsOK` — no entries; returns empty Escrow without error.
- (These tests may already exist; verify and extend if needed.)

### 7.3 DAG-anchor verification tests

- `TestDAGAnchorVerification_ValidAnchor` — anchor is ancestor of tip; passes.
- `TestDAGAnchorVerification_MismatchFails` — anchor not reachable; returns error.
- (Part C's `anchor_test.go` already covers this; the new tests verify it works in the applicator/escrow context.)

### 7.4 Startup ordering test

- `TestStartupOrdering_LoadBeforeListener` — construct applicator + escrow, load, then start bus; verify load completed before any consumer invocation.

---

## 8. Sub-commit ordering

Estimated 5 sub-commits.

1. **Add `AllMeta` method to store + make `LoadApplied` return error.**
   - `store.AllMeta(prefix string) (map[string][]byte, error)` — prefix scan on `meta:`.
   - Change `LoadApplied` signature to return `error` instead of silently logging.
   - Tests for AllMeta and LoadApplied error propagation.
   - Verify: `go test ./internal/store/... ./internal/settlement/...` passes.

2. **Export `VerifyAnchor` from `internal/dispatch/anchor.go` + add DAG-anchor write/verify to applicator and escrow.**
   - Capitalize `verifyAnchor` → `VerifyAnchor` in dispatch/anchor.go.
   - Add DAG-anchor meta key writes in applicator (`settlement:applied:__dag_anchor__`) and escrow (`esc:__dag_anchor__`).
   - Add anchor verification on load in both subsystems.
   - Verify: `go test ./internal/dispatch/... ./internal/settlement/... ./internal/escrow/...` passes.

3. **Wire `LoadApplied` and `LoadFromStore` in `cmd/node/main.go`.**
   - Escrow: replace `escrow.New(tl) + SetStore(s)` with `escrow.LoadFromStore(tl, s)` when store present.
   - Applicator: call `settlementApp.LoadApplied(stack.store.AllMeta)` after SetStore.
   - Both complete before `commitBus.Start()`.
   - Verify: `go build ./cmd/node` succeeds.

4. **Add projection registry entries for applicator + update escrow subcategory.**
   - New `internal/settlement/projection.go` with `AppliedSetProjection()`.
   - Update `internal/escrow/projection.go` subcategory to `SubcategoryEscrowRegistry`.
   - Add `AppliedCount()` method on Applicator.
   - Verify: `go test ./internal/projections/...` passes.

5. **Plan document + Sync semantics + final verification.**
   - Add `Sync()` calls on critical write paths.
   - Include plan document.
   - Full repo test sweep.
   - Verify: `go test -race ./...` passes.

---

## 9. Out of scope

- Commit 9 of §9 (first consumer wiring). Separate prompt.
- Cross-cutting items. Separate prompt.
- Part F. Separate prompt.
- Live testnet verification.
- Any change to LoadApplied or LoadFromStore logic itself.

---

## 10. Sign-off

This plan is in draft awaiting founder approval.
