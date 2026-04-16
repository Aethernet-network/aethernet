# F3-B Fix Workstream — Part E: Escrow API Hardening

**Workstream parent**: `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (locked v3-final, commit `1a7f096`). Specifically §6 (Part E), §9 (sequencing), §10 (end-to-end success criteria), §11 (out of scope).

**Audit precondition**: `docs/audits/2026-04-15-escrow-hold-callers-audit.md` (approved by founder; Category 3 resolutions recorded: C3-1 REMOVE, C3-2 OUT-OF-SCOPE WITH LINT EXEMPTION).

**Integration branch**: `feat/settlement-consensus-integrity-fix` (created from `main@ccbffc8`, currently active).

**Merge constraint**: No merge to main until the entire F3-B workstream (Parts A, B, C, D, E, F + cross-cutting) passes §10 end-to-end testnet verification. Part E lands as commits on the integration branch only.

**Status**: Draft, awaiting founder sign-off. Per CLAUDE.md §1: sign-off required before any code is written.

---

## Scope of Part E

Part E replaces `escrow.Hold` — which conflates "register escrow metadata" + "move funds from poster to bucket" — with two semantically distinct primitives:

1. `RegisterEscrow(taskID, poster, amount, fundingTransferRef)` — metadata only, validated against the DAG. Lives in the production `internal/escrow/` package. Called from code paths where a canonical `Transfer` event has already moved the funds (the F1 double-debit root cause path + two catch-up paths).

2. `FundAndRegisterEscrowForTest(tl, esc, taskID, poster, amount)` — combined fund-and-register. Lives in a separate Go module `internal/escrow_testhelpers/` that production code cannot import. Test fixtures, unit tests, and integration tests use it where they need combined behavior without a DAG or protocol client.

`Hold` itself is removed from the production `internal/escrow/` package when the last caller migrates. The single exception is the marketplace binary's application-layer escrow (`internal/marketplace/server.go:355`), which retains an equivalent combined-fund-and-register call with a documented lint exemption per the audit's C3-2 resolution.

Part E closes F1 (the double-debit) on the applicator path and eliminates the same duplicate on the two verification-consensus catch-up paths. Part E does **not** close F3-B by itself; F3-B requires Parts C and D. Part E is a precondition for Parts C and D because the dispatcher's atomic-batch settlement invariant (C-11, C-12) assumes all consumer-side ledger writes are already canonical-only, not doubled by legacy `Hold` calls.

---

## 1. Module structure

### 1.1 Files created

```
internal/escrow_testhelpers/
├── go.mod                     # module github.com/Aethernet-network/aethernet/internal/escrow_testhelpers
├── helpers.go                 # FundAndRegisterEscrowForTest + MockDAG helper
└── helpers_test.go            # exercises the helper against a real in-memory ledger

go.work                        # repo root: lists both the main module and the test-helpers module
```

### 1.2 `go.work` at repo root

A Go workspace file is required so that the main module's tests, when run from the repo root with `go test ./...`, can resolve imports of `internal/escrow_testhelpers` without requiring the test-helpers module to be tagged/published. The workspace file is:

```
go 1.22

use (
    .
    ./internal/escrow_testhelpers
)
```

Workspace mode is opt-in at build time: `go build ./cmd/node` from the repo root with `go.work` present still builds only what `cmd/node`'s module graph imports. Since the main module's `go.mod` does **not** declare a dependency on `internal/escrow_testhelpers`, the production binary's dependency tree never includes it — verified by §8 below.

The workspace file is committed to the repo. `.gitignore` is updated to commit `go.work` but still ignore `go.work.sum` per Go tooling conventions (matching standard Go workspace usage: `go.work` is source of truth, `go.work.sum` is regenerable).

### 1.3 `internal/escrow_testhelpers/go.mod`

```
module github.com/Aethernet-network/aethernet/internal/escrow_testhelpers

go 1.22

require (
    github.com/Aethernet-network/aethernet v0.0.0
)

replace github.com/Aethernet-network/aethernet => ../..
```

The `replace` directive points at the parent. The test-helpers module imports main-module packages (`internal/escrow`, `internal/ledger`, `internal/event`, `internal/crypto`) freely. The main module never imports the test-helpers module — this is the structural constraint that makes the boundary robust against regression.

### 1.4 Why separate module, not build tags

Per v3-final §6.1: "The separate-module approach is structurally stronger than a build tag. A build tag is a CI-pipeline convention; a separate module is a Go module-system constraint requiring explicit dependency declaration."

Enforcement is both at the Go toolchain level (production `go.mod` has no dependency on `internal/escrow_testhelpers`, so `go list -deps ./cmd/node` returns a dependency tree that excludes it by construction) and at the CI level (§8 static-analysis check fails the build if any production package imports the test-helpers module path).

---

## 2. `RegisterEscrow` API design

### 2.1 Signature and semantics

```go
// internal/escrow/escrow.go

// RegisterEscrow records an EscrowEntry for taskID without moving funds.
// The canonical Transfer event referenced by fundingTransferRef must already
// be projected in the DAG and must match the amount, poster, and escrow bucket
// of the registration. If validation fails, RegisterEscrow returns an error
// classifiable as a prerequisite failure (see ErrFundingTransferNotProjected
// and ErrFundingTransferMismatch).
func (e *Escrow) RegisterEscrow(
    taskID string,
    poster crypto.AgentID,
    amount uint64,
    fundingTransferRef event.EventID,
) error
```

Behavior:

1. Load the `*event.Event` identified by `fundingTransferRef` via the injected `DAGReader`.
2. If the event is absent, return `ErrFundingTransferNotProjected` (wrapped with context).
3. Decode the event's payload as `event.TransferPayload`. If the event type is not `EventTypeTransfer`, return `ErrFundingTransferWrongType`.
4. Validate the transfer matches the registration:
   - `payload.Reason == "escrow-lock"`
   - `payload.FromAgent == string(poster)`
   - `payload.ToAgent == "escrow:" + taskID`
   - `payload.Amount == amount`
   - `payload.TaskID == taskID`
   If any check fails, return `ErrFundingTransferMismatch` with a diagnostic identifying the first mismatching field.
5. Under `e.mu.Lock()`: if an entry already exists for `taskID`, return `nil` (idempotent — registration after registration is a no-op; this matches the "peer node catch-up" contract from the audit's C1-2/C1-3).
6. Persist the new `EscrowEntry` with `FundingTransferRef` populated, via `e.persist(entry)` after releasing `e.mu` (consistent with the existing lock discipline at `escrow.go:134–136`).
7. Do **not** call `e.ledger.TransferFromBucket` under any code path. That is the F1 duplicate debit.

### 2.2 `DAGReader` interface

A minimal interface is defined inside `internal/escrow/` to avoid coupling the package to `*dag.DAG` directly:

```go
// internal/escrow/dag_reader.go

type DAGReader interface {
    Get(id event.EventID) (*event.Event, error)
}
```

`*dag.DAG` satisfies this signature today via `dag.go:405`. Test code injects a mock (`escrow_testhelpers.MockDAG`, §3.3) or a hand-rolled in-file mock depending on same-package vs external-package test (§4).

### 2.3 Injection via setter

```go
// internal/escrow/escrow.go

// SetDAGReader attaches the DAG reader used by RegisterEscrow for funding-transfer
// validation. Must be called before RegisterEscrow is invoked; RegisterEscrow
// returns ErrDAGReaderNotConfigured if called with no reader attached.
func (e *Escrow) SetDAGReader(r DAGReader) {
    e.dagReader = r
}
```

The setter pattern matches the existing `SetStore` at `escrow.go:69`. Wiring is added in `cmd/node/main.go` immediately after the DAG and escrow manager are constructed, before any goroutine that could admit events is started. The setter is idempotent — calling it twice replaces the reader.

### 2.4 `EscrowEntry.FundingTransferRef`

A new field is added to `EscrowEntry`:

```go
type EscrowEntry struct {
    TaskID              string         `json:"task_id"`
    PosterID            crypto.AgentID `json:"poster_id"`
    Amount              uint64         `json:"amount"`
    FundingTransferRef  event.EventID  `json:"funding_transfer_ref"`
    WorkerPaid          bool           `json:"worker_paid"`
    ValidatorPaid       bool           `json:"validator_paid"`
    TreasuryPaid        bool           `json:"treasury_paid"`
}
```

`FundingTransferRef` carries the `EventID` of the canonical Transfer that funded this escrow. It is recorded by `RegisterEscrow` and by the test-helper path (§3). It is non-empty for every entry created on or after the Part E cutover.

Existing persisted escrow entries (pre-Part E) have this field empty when loaded via `LoadFromStore`. On the target testnet the DB is wiped per v3-final §0.5 ("testnet wipe-without-ceremony is standard"), so pre-existing entries are not a live concern. Loading code tolerates `FundingTransferRef == ""` as "pre-Part E legacy" and does not fail.

### 2.5 Error sentinels

```go
var (
    ErrDAGReaderNotConfigured     = errors.New("escrow: DAG reader not configured")
    ErrFundingTransferNotProjected = errors.New("escrow: funding transfer not projected in DAG")
    ErrFundingTransferWrongType    = errors.New("escrow: funding transfer event is not a Transfer")
    ErrFundingTransferMismatch     = errors.New("escrow: funding transfer does not match registration")
)
```

Callers at the Category 1 sites distinguish `ErrFundingTransferNotProjected` from the other errors: the "not projected" case is a prerequisite failure the caller handles via Part D's deferral path (once Part D lands); the other cases are hard failures (malformed/malicious event, misconfigured registration).

### 2.6 What `RegisterEscrow` does **not** do

- Does not call `TransferFromBucket` under any condition.
- Does not emit any DAG event. The canonical Transfer already exists; `RegisterEscrow` only records its identity in local metadata.
- Does not take a context argument in this version. Future evolution may add one; Part E keeps the signature minimal to match the existing escrow-package idiom.

---

## 3. `FundAndRegisterEscrowForTest` test-helpers module

### 3.1 What the helper does

```go
// internal/escrow_testhelpers/helpers.go

package escrow_testhelpers

import (
    "github.com/Aethernet-network/aethernet/internal/crypto"
    "github.com/Aethernet-network/aethernet/internal/escrow"
    "github.com/Aethernet-network/aethernet/internal/event"
    "github.com/Aethernet-network/aethernet/internal/ledger"
)

// FundAndRegisterEscrowForTest performs the legacy Hold behavior — transfer
// funds from poster to the escrow bucket, then register metadata — in one call.
// Test fixtures and integration tests needing combined behavior without a DAG
// call this helper. Production code cannot import this module.
func FundAndRegisterEscrowForTest(
    tl *ledger.TransferLedger,
    esc *escrow.Escrow,
    taskID string,
    poster crypto.AgentID,
    amount uint64,
) error {
    // 1) Move funds via the ledger (the legacy Hold side effect).
    if err := tl.TransferFromBucket(poster, crypto.AgentID("escrow:"+taskID), amount); err != nil {
        return err
    }
    // 2) Fabricate a stand-in funding-transfer EventID.
    ref := event.EventID("test-funding:" + taskID)
    // 3) Register via a test-only path that bypasses DAG validation.
    return esc.RegisterEscrowForTest(taskID, poster, amount, ref)
}
```

The helper composes `TransferFromBucket` (the funds-moving primitive already in `*ledger.TransferLedger`) with a minimal test-only registration. The test-only registration is a separate method on `*escrow.Escrow` that does not consult the DAG:

```go
// internal/escrow/escrow.go

// RegisterEscrowForTest records an EscrowEntry without DAG validation.
// Only callable from the test-helpers module and from same-package tests.
// Production code cannot use this method — not enforced by compiler (the method
// is exported because the test-helpers module is a separate module), but
// enforced by the no-bypass CI lint (§8) which flags any production-package
// import or call.
func (e *Escrow) RegisterEscrowForTest(
    taskID string,
    poster crypto.AgentID,
    amount uint64,
    fundingTransferRef event.EventID,
) error
```

**Why a separate method, not a shared internal helper**: keeping two named entry points (`RegisterEscrow` the production method, `RegisterEscrowForTest` the test-only method) makes the two code paths grep-visible. The CI lint (§8) pattern-matches on the `RegisterEscrowForTest` identifier in production-package imports and fails the build, matching the no-bypass lint pattern from v3-final §4.11.

### 3.2 Same-package test option chosen: Option B (with method rename)

Per the audit §Module-boundary complexity, three options were offered for the 10 same-package tests in `internal/escrow/escrow_test.go` and `internal/escrow/idempotency_test.go`. Part E adopts a variant of **Option B**:

- The private funds-moving primitive already exists: it's `ledger.TransferLedger.TransferFromBucket`.
- The registration primitive becomes `RegisterEscrow` (DAG-validated) for production and `RegisterEscrowForTest` (no DAG) for tests.
- Same-package tests in `internal/escrow/*_test.go` call `RegisterEscrowForTest` directly. They do not import the test-helpers module (same package = no import). They exercise registration behavior — idempotency, store persistence, deletion on release/refund — against the same underlying state the production code mutates.
- Tests that need the "transfer + register" combined call compose `tl.TransferFromBucket(...)` and `esc.RegisterEscrowForTest(...)` inline, or call `FundAndRegisterEscrowForTest` if they are external-package tests.

**Why not Option A (move same-package tests out)**: moving escrow tests out of the `internal/escrow/` package loses same-package access to unexported fields (e.g., `entries`, `store`, internal locking invariants the tests currently verify). Same-package tests of `Hold` are replaced 1:1 with same-package tests of `RegisterEscrowForTest` — same scope of access, same invariants verified, just against the renamed primitive.

**Why not Option C (split the package)**: Option C introduces a `internal/escrow/internal/core/` sub-package, which changes import paths across the codebase and creates a new package boundary that Part E does not otherwise need. Reserved for a future refactor if warranted; out of scope for Part E.

### 3.3 `MockDAG` for same-package tests that need `RegisterEscrow` (not the `ForTest` variant)

Some same-package tests will want to exercise `RegisterEscrow` itself — the DAG-validated path — to verify the validation logic. These tests construct a minimal in-file mock:

```go
// internal/escrow/escrow_test.go (test file)

type stubDAGReader struct {
    events map[event.EventID]*event.Event
}

func (s *stubDAGReader) Get(id event.EventID) (*event.Event, error) {
    if e, ok := s.events[id]; ok {
        return e, nil
    }
    return nil, errors.New("not found")
}
```

External-package tests and integration tests can import a shared mock from the test-helpers module:

```go
// internal/escrow_testhelpers/helpers.go

type MockDAG struct {
    Events map[event.EventID]*event.Event
}

func (m *MockDAG) Get(id event.EventID) (*event.Event, error) { /* ... */ }
```

---

## 4. Category 1 migration plan (per call site)

The audit classifies three production call sites. Each migrates to `RegisterEscrow` with a specific `fundingTransferRef` source.

### 4.1 C1-1 — `internal/settlement/applicator.go:310`

**Current**:
```go
if err := a.escrow.Hold(tp.TaskID, crypto.AgentID(tp.FromAgent), tp.Amount); err != nil {
```

**After**:
```go
if err := a.escrow.RegisterEscrow(tp.TaskID, crypto.AgentID(tp.FromAgent), tp.Amount, targetID); err != nil {
```

- `targetID` is already in scope at `applicator.go:287` as the `targetID event.EventID` parameter to `applyTransfer`. It is the canonical Transfer event's ID — exactly what `fundingTransferRef` requires.
- The surrounding `if !a.escrow.IsLocked(tp.TaskID)` guard at line 309 is kept. `RegisterEscrow` is also idempotent (§2.1 step 5), so the guard becomes defense-in-depth; retained to match the existing code pattern.
- The `slog.Warn` on failure is retained. Errors are logged and not propagated — matching current behavior. A future tightening may propagate, but Part E preserves behavior on the error path.
- Because the canonical Transfer is present in the DAG (the applicator is processing the `Settlement` event for that same Transfer — reached `applyTransfer` because `RecordFromSync(target)` at :291 succeeded), the DAG validation in `RegisterEscrow` is guaranteed to find the event. `ErrFundingTransferNotProjected` is structurally unreachable on this path.

### 4.2 C1-2 — `internal/settlement/verification_consensus_settler.go:102`

**Current**:
```go
if !s.escrowMgr.IsLocked(payload.TaskID) {
    if holdErr := s.escrowMgr.Hold(payload.TaskID, crypto.AgentID(task.PosterID), task.Budget); holdErr != nil {
```

**After**:
```go
if !s.escrowMgr.IsLocked(payload.TaskID) {
    fundingRef, lookupErr := s.lookupFundingTransfer(payload.TaskID, crypto.AgentID(task.PosterID), task.Budget)
    if lookupErr != nil {
        return result, fmt.Errorf("verification_settler: escrow catch-up funding-transfer lookup failed: %w", lookupErr)
    }
    if holdErr := s.escrowMgr.RegisterEscrow(payload.TaskID, crypto.AgentID(task.PosterID), task.Budget, fundingRef); holdErr != nil {
```

- A new method `lookupFundingTransfer(taskID string, poster crypto.AgentID, amount uint64) (event.EventID, error)` is added to `VerificationConsensusSettler`. It queries the DAG for the canonical Transfer matching `(FromAgent=poster, ToAgent="escrow:"+taskID, Reason="escrow-lock", Amount=amount)`.
- The lookup is a DAG scan constrained by a secondary index. The `internal/dag` package exposes `Get` keyed by `EventID` but no index by payload fields. For Part E, the lookup walks `dag.EventsByType(EventTypeTransfer)` (or adds such a helper if missing) filtered by the match predicate. The per-task cost is O(number-of-Transfer-events) which is bounded in practice; if this becomes a bottleneck the index can be added later.
- If the lookup returns no match, `lookupFundingTransfer` returns an error. On the pre-Part-D integration branch, this error is surfaced to the caller (as the existing code does for Hold failure). Once Part D lands, the error is converted to a prerequisite-deferral signal so the settler defers until the Transfer is projected.
- The `VerificationConsensusSettler` gains a `dag DAGReader` field (injected at construction via a constructor parameter change). `cmd/node/main.go` wires the DAG into the settler — same DAG instance wired into `Escrow.SetDAGReader`.

### 4.3 C1-3 — `cmd/node/main.go:1634`

**Current** (inside `stack.settlementApp.SetTaskSettler(...)` closure):
```go
if err := escrow.Hold(payload.TaskID, crypto.AgentID(payload.PosterID), payload.Budget); err != nil {
```

**After**:
```go
fundingRef, lookupErr := lookupEscrowLockTransfer(stack.dag, payload.TaskID, crypto.AgentID(payload.PosterID), payload.Budget)
if lookupErr != nil {
    return fmt.Errorf("task-settler: escrow catch-up funding-transfer lookup failed: %w", lookupErr)
}
if err := escrow.RegisterEscrow(payload.TaskID, crypto.AgentID(payload.PosterID), payload.Budget, fundingRef); err != nil {
```

- A free function `lookupEscrowLockTransfer(dag DAGReader, taskID string, poster crypto.AgentID, amount uint64) (event.EventID, error)` is added, shared between `cmd/node/main.go` and `internal/settlement/verification_consensus_settler.go`. Shared location: a new small file `internal/settlement/funding_transfer_lookup.go` exporting `LookupEscrowLockTransfer` with the same signature plus a DAG-scanning contract documented in the file header.
- Rationale for co-locating the helper in `internal/settlement/`: both callers are settlement-related (settlement applicator's catch-up, settlement app's task-settler closure). Placing it in `internal/escrow/` would create a new escrow→DAG dependency direction that is not needed elsewhere. Placing it in `internal/settlement/` keeps the escrow package unchanged in its dependency footprint.
- The closure at `cmd/node/main.go:1634` imports `settlement.LookupEscrowLockTransfer` and uses it directly. No behavioral change to the surrounding guard at `:1613–1622` which short-circuits when the task is already in a terminal status.

### 4.4 Category 1 dependency wiring (cmd/node/main.go)

After the DAG and escrow manager are constructed:

```go
stack.escrow.SetDAGReader(stack.dag)
```

Placed before any recognition-fabric listener is started, matching the load-before-listener ordering of Parts A/B. Fails startup with a clear diagnostic if either component is nil.

The `VerificationConsensusSettler` constructor is extended to accept the DAG reader:

```go
settler := settlement.NewVerificationConsensusSettler(
    stack.taskMgr,
    stack.escrow,
    stack.dag,           // new parameter
    stack.ledger,
    ...
)
```

---

## 5. Category 2 migration plan (24 call sites)

### 5.1 External-package tests (14 sites: C2-1 through C2-8, C2-19 through C2-24)

Each call site is a single-line swap:

**Before**:
```go
if err := esc.Hold(taskID, poster, amount); err != nil { ... }
```

**After**:
```go
if err := escrow_testhelpers.FundAndRegisterEscrowForTest(tl, esc, taskID, poster, amount); err != nil { ... }
```

Test file imports change:
```go
import (
    "github.com/Aethernet-network/aethernet/internal/escrow_testhelpers"
)
```

No semantic difference; `FundAndRegisterEscrowForTest` composes the same `TransferFromBucket` + register that the old `Hold` performed. Test harnesses must have `tl *ledger.TransferLedger` in scope; every existing caller does because they also construct `esc` via `escrow.New(tl)`.

### 5.2 Same-package tests in `internal/escrow/` (10 sites: C2-9 through C2-18)

Per §3.2 (Option B chosen), same-package tests migrate to call `RegisterEscrowForTest` directly or compose with `TransferFromBucket` where the test requires both:

**Pattern A — test asserts registration behavior only** (e.g., `TestEscrow_TotalEscrowed`):
```go
// Before
if err := esc.Hold(taskID, poster, amount); err != nil { ... }

// After
if err := esc.RegisterEscrowForTest(taskID, poster, amount, event.EventID("test-funding:"+taskID)); err != nil { ... }
```

**Pattern B — test asserts the combined fund-and-register side effect** (e.g., `TestHold`, `TestHold_InsufficientBalance`, which specifically verify the ledger-side effect of the old `Hold`):
```go
// Before
err := esc.Hold(taskID, poster, amount)

// After
// Step 1: exercise the ledger primitive directly.
err := tl.TransferFromBucket(poster, crypto.AgentID("escrow:"+taskID), amount)
if err != nil { /* assert the insufficient-balance error shape */ }
// Step 2: exercise the registration primitive.
err = esc.RegisterEscrowForTest(taskID, poster, amount, event.EventID("test-funding:"+taskID))
```

For tests that specifically verify the combined rollback behavior (old `Hold` rolled back the entry when `TransferFromBucket` failed), the semantics change: `RegisterEscrowForTest` never calls `TransferFromBucket`, so there's no rollback to verify. These tests are rewritten as two independent assertions: (1) `TransferFromBucket` returns the expected error on insufficient balance, (2) `RegisterEscrowForTest` succeeds unconditionally on the metadata-only path. Both assertions are still worth preserving — they're what the new architecture guarantees.

**`TestHold_IdempotentUnderDuplicate` (C2-17) and `TestHold_DistinctTasks` (C2-18)** rename to `TestRegisterEscrowForTest_IdempotentUnderDuplicate` and `TestRegisterEscrowForTest_DistinctTasks`. Test bodies swap `Hold` for `RegisterEscrowForTest` in both lines. The idempotency behavior is preserved by `RegisterEscrowForTest` (step 5 of §2.1: existing entry → no-op, return nil).

### 5.3 `TestHold` → rename and restructure

The test named `TestHold` at `internal/escrow/escrow_test.go:46` is renamed to `TestRegisterEscrowForTest_MetadataAndBucket`. Its body is split as described in §5.2 Pattern B: first exercise the ledger primitive to verify the bucket moves, then exercise the registration primitive to verify the entry appears. Net test coverage is preserved; the distinction between "funds moved" and "metadata registered" is surfaced by name.

---

## 6. C2-8 projection-lint coupling preservation

The step-3 projection-lint CI check (commit `c20e6df`, `internal/projections/lint/`) verifies that every `CanonicalProjection` entry's `IntegrationTestRef` field resolves to a real test function by scanning test-package symbol tables. The escrow projection at `internal/escrow/projection.go:27` references:

```
github.com/Aethernet-network/aethernet/internal/integration.TestEscrow_HoldsOnTransferOptimistic
```

The test function lives at `internal/integration/projection_escrow_test.go:33` (audit entry C2-8). Its body calls `esc.Hold(...)`.

### 6.1 Requirement

After Part E migration:

- The **test function name** `TestEscrow_HoldsOnTransferOptimistic` is preserved unchanged.
- The **import path** `github.com/Aethernet-network/aethernet/internal/integration` is preserved unchanged.
- The **test body** is migrated per §5.1 to call `escrow_testhelpers.FundAndRegisterEscrowForTest(...)` instead of `esc.Hold(...)`.
- The `IntegrationTestRef` field in `escrow.Projection()` at `internal/escrow/projection.go:27` remains unchanged — the symbol path it references still exists because only the body changed.

### 6.2 Verification

After the C2-8 migration, `go test ./internal/projections/lint/...` passes. The lint check iterates all registered projections and resolves each `IntegrationTestRef` via reflection/AST scan; the scan finds `TestEscrow_HoldsOnTransferOptimistic` at its original path, succeeds.

### 6.3 Commit sequencing constraint

The C2-8 migration commit MUST NOT rename the test function or move the file. Both are defensible operations in isolation; doing either in the Part E window without updating `IntegrationTestRef` breaks the build. If either rename becomes necessary during implementation, it is a separate commit with a matching update to `projection.go:27`, landed atomically.

---

## 7. C3-1 removal plan (`internal/api/server.go:1507` test fallback)

Per the audit's founder resolution: **REMOVE the fallback entirely.**

### 7.1 Change

Lines 1499–1511 become:

```go
// Escrow the budget via canonical protocol event. Protocol client is required.
if s.protoClient == nil {
    writeError(w, http.StatusInternalServerError, "api: protocol client not configured")
    return
}
if _, err := s.protoClient.SubmitEscrowLock(crypto.AgentID(posterID), task.ID, req.Budget); err != nil {
    writeError(w, http.StatusBadRequest, err.Error())
    return
}
```

The `else` branch that called `s.escrowMgr.Hold(...)` is removed. If a caller reaches `handlePostTask` on a server constructed without `SetProtocolClient`, it gets a 500 with a clear diagnostic.

### 7.2 Test migration

Any test in `internal/api/` that constructs an `api.Server` without calling `SetProtocolClient` and exercises `handlePostTask` will fail after the removal. Each such test is updated in the same commit as the removal:

**Option 1 — wire a real protocol client**: if the test can tolerate a real `protocol.NewClient(...)` wired against an in-memory node, use that. Matches how production is configured.

**Option 2 — wire a test double**: inject a `protocolClientStub` implementing the interface `protoClient` satisfies (currently `*protocol.Client`; if the interface is not extracted, extract it in this commit as a minimal addition: `type protocolClient interface { SubmitEscrowLock(...) (..., error) }`).

**Option 3 — restructure the test to not exercise `handlePostTask`**: if the test's purpose is something else (e.g., response-shape validation on a different handler), re-target it at the specific handler it meant to test.

Each affected test is handled in the same commit as the C3-1 removal. If any test cannot be cleanly migrated, per the audit: "surface it as a separate item for founder decision rather than reintroducing a fallback under a different name." Part E implementation pauses for founder decision rather than patching around the constraint.

### 7.3 Discovery procedure

Before making the change, run:

```bash
grep -rn "api.NewServer\|apiSrv := " internal/api/ --include="*_test.go"
```

and inspect each hit to check whether the test exercises `handlePostTask` and whether it wires a protocol client. The result of this inspection is a concrete migration list appended to the plan when implementation begins. (The plan does not pre-enumerate the list because it is discovery output, not a design decision.)

---

## 8. C3-2 lint exemption for `internal/marketplace/server.go:355`

Per the audit's founder resolution: **OUT-OF-SCOPE WITH EXPLICIT LINT EXEMPTION.**

### 8.1 Change

The call site at `internal/marketplace/server.go:355` is left functionally unchanged but receives a structured suppression comment directly above:

```go
if req.Budget > 0 && req.PosterID != "" {
    // dispatch:lint marketplace-exempt "marketplace binary operates own application-layer escrow; protocol-escrow integration tracked as follow-up workstream"
    if err := s.escrowMgr.Hold(task.ID, crypto.AgentID(req.PosterID), req.Budget); err != nil {
        slog.Warn("marketplace: escrow hold failed", "task_id", task.ID, "err", err)
    }
}
```

### 8.2 Exemption string contract

The exact comment (single line, immediately above the `Hold` call) is:

```
// dispatch:lint marketplace-exempt "marketplace binary operates own application-layer escrow; protocol-escrow integration tracked as follow-up workstream"
```

Properties verified by the no-bypass lint (to be built in Part C, not Part E — see §10):

- Prefix `dispatch:lint marketplace-exempt`
- Quoted justification string length ≥20 characters
- Quoted justification string contains ≥3 words
- Placement: the comment is on the line immediately preceding the `Hold` call, no blank line between.

### 8.3 No-bypass lint status during Part E

The no-bypass CI lint is built in Part C, not Part E. During the Part E window, the exemption comment is inert — it has no enforcer yet. Its presence is nonetheless committed at Part E time so that when Part C's lint lands, the exemption is already in place and the lint passes on first run.

### 8.4 `Hold` symbol retention

Because C3-2 retains a call to `esc.Hold(...)`, the `Hold` method cannot be removed from the `internal/escrow/` package at Part E close. It survives as a documented sanctioned path for the marketplace binary only. Its godoc is updated to call out the restriction:

```go
// Hold is the combined fund-and-register primitive retained for the marketplace
// binary's application-layer escrow only. Production protocol code (applicator,
// verification-consensus settler, task-settler closure) uses RegisterEscrow
// instead. See docs/audits/2026-04-15-escrow-hold-callers-audit.md C3-2 and the
// dispatch:lint marketplace-exempt pragma at internal/marketplace/server.go.
func (e *Escrow) Hold(...) error
```

When the marketplace-escrow-integration follow-up workstream lands, `Hold` is removed.

---

## 9. Build and CI verification

### 9.1 `go list -deps ./cmd/node` check

A new CI step runs:

```bash
go list -deps ./cmd/node | grep -q '^github.com/Aethernet-network/aethernet/internal/escrow_testhelpers$'
if [ $? -eq 0 ]; then
    echo "ERROR: production binary depends on test-helpers module"
    exit 1
fi
```

This enforces v3-final invariant E-1 ("`FundAndRegisterEscrowForTest` lives in a Go module not imported by production. CI verifies the production binary's dependency tree excludes it.").

The check is added to `.github/workflows/ci.yml` (or the equivalent CI config for this repo; implementation discovers the actual file path). It runs in addition to `go test -race ./...`.

### 9.2 Static analysis — production import of test-helpers path

A second CI check greps production packages for the test-helpers import path:

```bash
# Check every production .go file (excluding _test.go and excluding internal/escrow_testhelpers itself)
find . -name '*.go' -not -name '*_test.go' -not -path './internal/escrow_testhelpers/*' \
    -exec grep -l '"github.com/Aethernet-network/aethernet/internal/escrow_testhelpers"' {} + | \
    tee /tmp/violators
if [ -s /tmp/violators ]; then
    echo "ERROR: production files import the test-helpers module:"
    cat /tmp/violators
    exit 1
fi
```

This is the CI-level defense matching v3-final §6.1 ("CI verifies with two checks: (1) `go list -deps ./cmd/node` does not include the test-helpers module. (2) Static analysis fails the build if any production package imports the test-helpers module path.").

### 9.3 `RegisterEscrowForTest` call-site check

A third CI check flags any production-package call to `RegisterEscrowForTest`:

```bash
find . -name '*.go' -not -name '*_test.go' -not -path './internal/escrow_testhelpers/*' \
    -exec grep -l 'RegisterEscrowForTest' {} + | tee /tmp/test_method_callers
if [ -s /tmp/test_method_callers ]; then
    echo "ERROR: production files call RegisterEscrowForTest:"
    cat /tmp/test_method_callers
    exit 1
fi
```

This catches the "someone called the test-only method from production code" failure mode, matching v3-final's no-bypass principle.

### 9.4 No-bypass CI lint status

The full no-bypass CI lint from v3-final §4.11 (static analysis on the type graph, flagging consumers wired to the fabric without going through `dispatch.Register`) is Part C's scope, not Part E's. Part E adds only the three `grep`-based checks above, which are tactical precursors.

---

## 10. Test plan (unit + integration; no testnet at Part E close)

### 10.1 Unit tests added or modified

In `internal/escrow/`:

- `TestRegisterEscrow_RejectsMissingFundingRef` — construct an `Escrow` with a stub `DAGReader` returning "not found" for the reference; assert `RegisterEscrow` returns a wrapped `ErrFundingTransferNotProjected`.
- `TestRegisterEscrow_RejectsWrongEventType` — stub returns a non-Transfer event (e.g., `EventTypeTaskPosted`); assert `ErrFundingTransferWrongType`.
- `TestRegisterEscrow_RejectsMismatchedAmount` — stub returns a Transfer with wrong `Amount`; assert `ErrFundingTransferMismatch` with diagnostic identifying `amount`.
- `TestRegisterEscrow_RejectsMismatchedPoster` — same shape, wrong `FromAgent`; assert mismatch on `poster`.
- `TestRegisterEscrow_RejectsMismatchedTaskID` — wrong `TaskID` in payload; assert mismatch on `task_id`.
- `TestRegisterEscrow_RejectsMismatchedReason` — Transfer with `Reason != "escrow-lock"`; assert mismatch on `reason`.
- `TestRegisterEscrow_HappyPath` — stub returns a valid Transfer matching all fields; assert `nil` and entry stored with `FundingTransferRef` set.
- `TestRegisterEscrow_Idempotent` — call twice; assert second call is a no-op and the entry is unchanged.
- `TestRegisterEscrow_WithoutDAGReader` — call with no reader attached; assert `ErrDAGReaderNotConfigured`.
- `TestRegisterEscrow_DoesNotMoveFunds` — call `RegisterEscrow`; assert poster balance in `*TransferLedger` is unchanged and bucket balance is zero.

Existing tests migrated per §5: every `esc.Hold(...)` call swaps per Pattern A or B. Renamed tests get their new names.

In `internal/escrow_testhelpers/`:

- `TestFundAndRegisterEscrowForTest_MovesFunds` — verify poster balance decreases by `amount` and bucket balance increases by `amount`.
- `TestFundAndRegisterEscrowForTest_RegistersEntry` — verify `esc.IsLocked(taskID)` returns true after the call.
- `TestFundAndRegisterEscrowForTest_PropagatesLedgerError` — stub the ledger to return `ErrInsufficientBalance`; assert the helper returns the wrapped error.

In `internal/settlement/`:

- `TestApplicator_EscrowLockTransfer_RegistersEntry` — integration-style: feed a canonical `Transfer` event with `Reason=escrow-lock` through `Applicator.Apply`; assert `esc.IsLocked(taskID)` is true AND assert `tl.Balance(poster)` reflects exactly one transfer (not two).
- `TestVerificationConsensusSettler_EscrowCatchUp_UsesRegisterEscrow` — deliver a `TaskVerificationConsensus` event on a node where the Transfer IS projected but the escrow is NOT locked; assert the settler calls `RegisterEscrow` with the correct `fundingTransferRef` and does not double-debit.
- `TestLookupEscrowLockTransfer_FindsMatchingTransfer` — given a DAG with a matching Transfer, the lookup returns its `EventID`.
- `TestLookupEscrowLockTransfer_ReturnsErrorOnMismatch` — given a DAG with no matching Transfer, returns an error the caller classifies as a prerequisite failure.

In `internal/api/`:

- Existing tests that depended on the `s.protoClient == nil` fallback are migrated per §7.2.
- A new `TestHandlePostTask_RequiresProtocolClient` asserts a 500 is returned when the server is constructed without a wired client.

### 10.2 Integration tests

The existing integration tests in `internal/integration/` are migrated per §5.1. No new integration-test behavior is required for Part E itself; the post-Part-E integration surface is verified at end-of-workstream testnet verification per v3-final §10.

### 10.3 Race check

`go test -race ./...` runs clean across all packages after every Part E commit. Any new race (from the added `dagReader` field's lock discipline, for instance) blocks the commit until resolved.

### 10.4 No testnet at Part E close

Part E does **not** trigger testnet verification by itself. Per v3-final §6 and §9: "No merge to main until end-to-end testnet verification per §10 passes." Testnet runs after Parts C, D, A, B, F, and cross-cutting items land, against the full integration branch.

Part E is considered complete when:
1. All Category 1, 2, 3 migrations are committed.
2. `go test -race ./...` passes.
3. All CI checks in §9 pass.
4. The no-bypass lint exemption comment at `internal/marketplace/server.go:355` is in place.
5. `Hold` godoc is updated to call out the marketplace-only retention.
6. The integration branch compiles, tests, and vets clean.

At this point, Part C implementation begins on the same branch.

---

## 11. Sub-commit ordering on the integration branch

Twelve sub-commits land in this order. Each is self-contained, tests pass at every commit boundary, and the integration branch is buildable at every commit.

1. **Add `go.work` at repo root + `internal/escrow_testhelpers/` module skeleton.**
   - Files: `go.work`, `internal/escrow_testhelpers/go.mod`, `internal/escrow_testhelpers/helpers.go` (empty `package escrow_testhelpers` declaration only).
   - Verify: `go build ./...` succeeds; `go list -m all` shows the workspace including the new module.

2. **Add `DAGReader` interface + `dagReader` field + `SetDAGReader` setter on `*Escrow`; add `FundingTransferRef` to `EscrowEntry`.**
   - Files: `internal/escrow/escrow.go`, `internal/escrow/dag_reader.go` (new).
   - Verify: `go test ./internal/escrow/...` passes (existing tests unchanged; new field tolerated as empty).

3. **Add `RegisterEscrow` method with DAG validation + error sentinels.**
   - Files: `internal/escrow/escrow.go`.
   - Verify: new unit tests for `RegisterEscrow` (§10.1) pass; existing `Hold` tests still pass.

4. **Add `RegisterEscrowForTest` method on `*Escrow`.**
   - Files: `internal/escrow/escrow.go`.
   - Verify: same-package tests can exercise it; production callers still use `Hold` (no production call site has migrated yet).

5. **Implement `FundAndRegisterEscrowForTest` + `MockDAG` in test-helpers module.**
   - Files: `internal/escrow_testhelpers/helpers.go`, `internal/escrow_testhelpers/helpers_test.go`.
   - Verify: `go test ./internal/escrow_testhelpers/...` passes.

6. **Add `LookupEscrowLockTransfer` helper in `internal/settlement/`.**
   - Files: `internal/settlement/funding_transfer_lookup.go`, plus a test file.
   - Verify: `TestLookupEscrowLockTransfer_*` tests pass.

7. **Migrate C1-1 (applicator.go:310) to `RegisterEscrow`.**
   - Files: `internal/settlement/applicator.go`, plus any applicator tests that assert escrow side effects.
   - Verify: `TestApplicator_EscrowLockTransfer_RegistersEntry` passes; ledger accounting asserts one debit, not two.

8. **Migrate C1-2 (verification_consensus_settler.go:102) + wire DAG into the settler.**
   - Files: `internal/settlement/verification_consensus_settler.go`, `cmd/node/main.go` (constructor wiring).
   - Verify: `TestVerificationConsensusSettler_EscrowCatchUp_UsesRegisterEscrow` passes.

9. **Migrate C1-3 (cmd/node/main.go:1634) + `Escrow.SetDAGReader` wiring.**
   - Files: `cmd/node/main.go`.
   - Verify: `go build ./cmd/node` succeeds; manual inspection confirms the settler closure calls `RegisterEscrow` via the helper.

10. **Migrate Category 2 external-package tests (14 sites: C2-1 through C2-8, C2-19 through C2-24).**
    - Files: `internal/settlement/verification_consensus_settler_test.go`, `internal/integration/e2e_test.go`, `internal/integration/projection_escrow_test.go`, `internal/autovalidator/auto_recovery_test.go`, `internal/autovalidator/auto_test.go`.
    - Special attention: `projection_escrow_test.go:33` — function name and file path preserved per §6.
    - Verify: `go test -race ./...` passes; `go test ./internal/projections/lint/...` still resolves the `IntegrationTestRef`.

11. **Migrate Category 2 same-package tests (10 sites: C2-9 through C2-18) + rename `TestHold*` tests per §5.3.**
    - Files: `internal/escrow/escrow_test.go`, `internal/escrow/idempotency_test.go`.
    - Verify: `go test ./internal/escrow/...` passes; net test coverage unchanged.

12. **Remove C3-1 fallback + annotate C3-2 lint exemption + update `Hold` godoc.**
    - Files: `internal/api/server.go` (remove lines 1505–1511, replace with the nil-client check per §7.1), `internal/marketplace/server.go` (add exemption comment above the `Hold` call), `internal/escrow/escrow.go` (`Hold` godoc update), affected `internal/api/*_test.go` files per §7.2.
    - Verify: full test suite passes; `go build ./...` clean; `go list -deps ./cmd/node` check (§9.1) passes; static-analysis checks (§9.2, §9.3) pass.

After commit 12, Part E is complete. The integration branch is ready for Part C to begin.

### 11.1 Commit message style

Each commit follows the existing repo convention: conventional-commit prefix, short imperative subject, body explaining the why, and a trailing reference to this plan:

```
part-e(escrow): add RegisterEscrow with DAG-validated funding reference

RegisterEscrow records an EscrowEntry for taskID without moving funds.
The canonical Transfer referenced by fundingTransferRef is validated
against the DAG; mismatch/missing errors are classified for Part D's
prerequisite-deferral path.

Ref: docs/plans/2026-04-15-f3b-part-e-escrow-hardening.md §2
Parent: docs/plans/2026-04-15-settlement-consensus-integrity-fix.md §6
```

### 11.2 Revisiting the plan during implementation

Per CLAUDE.md §1: "If implementation reveals the plan was wrong, STOP, update the plan, and re-confirm." Any discovery during sub-commit work that invalidates a design decision above pauses implementation and surfaces the issue to the founder before proceeding.

---

## 12. Not in scope for Part E

Explicit non-goals, to prevent scope creep:

- **Part D prerequisite gating.** `RegisterEscrow` returns `ErrFundingTransferNotProjected` on missing references; Part E callers treat this as an error. Part D's deferral handling (reserved-pending-prerequisites state, evidence-based escalation) is implemented after Part C lands.
- **Part C dispatcher integration.** `RegisterEscrow` is called directly by the applicator and settler during Part E. It is not wired through the `CanonicalEventDispatcher` until Part C's dispatcher primitive exists.
- **Part A / Part B startup wiring.** `Escrow.LoadFromStore` and `Applicator.LoadApplied` are wired in the later steps of the workstream.
- **No-bypass CI lint (full static-analysis version).** Part E adds three grep-based CI checks (§9.1–§9.3). The full type-graph static-analysis lint is Part C §4.11.
- **Marketplace escrow integration.** C3-2 retains the combined behavior with a lint exemption; the actual marketplace/protocol-escrow integration is a follow-up workstream queued between the challenge path and data ingestion.
- **Testnet verification.** Per §10.4, Part E does not deploy to testnet on its own. End-to-end verification happens after the full workstream ships.

---

## 13. Sign-off

This plan is in draft awaiting founder approval. Per CLAUDE.md §1, no code is written until the founder signs off on the design above. Implementation proceeds only after sign-off, executing the 12 sub-commits in §11 order on the `feat/settlement-consensus-integrity-fix` branch, with merges to main deferred until the full workstream passes §10 end-to-end testnet verification.
