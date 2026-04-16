# F3-B Fix Workstream — Commit 9 of §9: Wire TaskVerificationConsensusConsumer into Dispatcher

**Workstream parent**: `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (locked v3-final). §4 (dispatcher invariants C-1 through C-16), §5 (causal-gating invariants D-1 through D-8), §6 (escrow API), §9 step 9, §10 (success criteria), §12 (Type A taxonomy).

**Integration branch**: `feat/settlement-consensus-integrity-fix` (currently at commit `4b2981b` after Parts A+B).

**Merge constraint**: No merge to main until the full F3-B workstream passes §10 end-to-end testnet verification.

**Status**: Revision 2 — §5 tightened per founder follow-up. Awaiting founder sign-off.

---

## 1. Prerequisites design

### 1.1 Minimum correct prerequisite set

`TVConsensusConsumer.Prerequisites(ev)` returns `nil` (no explicit prerequisites).

### 1.2 Reasoning

The `TaskVerificationConsensusPayload` contains `TaskID`, `WorkerID`, `PosterID`, `FinalVerdict`, `FinalScoreBP`, and all vote-weight fields inline. The settler reads `Budget` from `taskMgr.Get(payload.TaskID)`, which is populated by the task lifecycle consumer from `TaskPosted` events.

**Load-bearing invariant: DAG strict CausalRefs enforcement.** `dag.Add` requires all `CausalRefs` to be present before an event can be committed to the local DAG. The `TaskVerificationConsensus` event's causal chain traces through votes → `TaskSubmitted` → `TaskClaimed` → `TaskPosted`. Therefore, the arrival of a `TaskVerificationConsensus` event in the local DAG guarantees `TaskPosted` (and the escrow-lock `Transfer`) are also in the DAG.

The narrow race between DAG commit and task-lifecycle-consumer processing of `TaskPosted` (which populates `taskMgr`) is handled by the dispatcher's `ConsumerFailedRetryable → retry` mechanism: if `taskMgr.Get()` fails, Apply returns an error, the dispatcher marks the consumer `failed-retryable`, and the next delivery retries.

**If a future DAG-layer change weakens strict CausalRefs enforcement**, this consumer's Prerequisites must be revisited — a consumer with empty prerequisites would silently break on out-of-order arrivals. The godoc on `Prerequisites` names this invariant explicitly.

### 1.3 `PrerequisiteSchemaVersion`

Returns `1`. Semantics: "version 1 = no explicit prerequisites; correctness depends on DAG strict CausalRefs enforcement + dispatcher retry."

---

## 2. Migration plan

### 2.1 What moves

Create a thin wrapper `TVConsensusConsumer` in `internal/dispatch/` that implements `dispatch.Consumer`. Only the settlement invocation goes through the dispatcher; round state, calibration, and slashing remain in the recognition consumer.

### 2.2 File changes

| File | Change |
|------|--------|
| `internal/dispatch/tv_consensus_consumer.go` | **New**. Type A consumer wrapping the settler. |
| `internal/dispatch/tv_consensus_consumer_test.go` | **New**. Unit + conformance tests. |
| `internal/recognition/task_verification_consensus_consumer.go` | **Modified**. Settlement call replaced with `dispatcher.Admit`. |
| `cmd/node/main.go` | **Modified**. Construct dispatcher, register consumer, wire into recognition consumer. |

### 2.3 Why only settlement goes through the dispatcher

**Architectural principle**: only state that must converge byte-identical cross-node on the canonical ledger goes through the dispatcher. Round state, calibration, and slashing do not meet that bar.

- **Round state**: Local-node replay-safe. Round records are never compared cross-node for byte-identical convergence; only the ledger is. `Transition` at `internal/taskverification/round.go` is deterministic on inputs — double-finalizing produces the same final record. Does not require dispatcher mediation.

- **Calibration**: Guarded by the `CalibrationApplied bool` flag on `TaskVerificationRound` (`internal/taskverification/round.go:53-60`). The flag prevents double-application and is persisted via `SaveRound`. Semantics are equivalent to local-replay-safe idempotency, not cross-node ledger convergence.

- **Slashing**: Labeled best-effort in the recognition consumer (`task_verification_consensus_consumer.go:162-176`, comment: "Best-effort — failures log but do not block the pipeline"). Dispatcher-mediated slashing with stronger guarantees is a follow-up workstream, out of scope for commit 9.

---

## 3. RecoveryProbe design — Option B

### 3.1 Choice: Option B (co-locate with consumer Apply)

The `RecoveryProbe` checks the task's terminal status as positive evidence of settlement completion.

### 3.2 3a — Citation: `ApplyVerificationConsensusResolution`

The settler calls `ApplyVerificationConsensusResolution` as the **last step** of each verdict path:

- Accept: `verification_consensus_settler.go:203` — after worker (line 161), validator (line 177), gen-ledger (line 182-186), and treasury (line 196) payouts.
- Reject: `verification_consensus_settler.go:256` — after poster refund (line 232), validator (line 244), and treasury (line 249) payouts.
- Dispute: `verification_consensus_settler.go:314` — after worker (line 291), poster (line 299), and treasury (line 307) payouts.

Inside `ApplyVerificationConsensusResolution` (`tasks.go:976-1009`):
- Line 998/1001/1003: sets `task.Status` to `Completed/Rejected/DisputedResolved`.
- Line 1007: calls `m.persist(task)` which durably writes the task to BadgerDB.

**Crash safety**: If a crash occurs between the last payout and line 1007, payouts are completed but the task is NOT terminal. `RecoveryProbe` returns `RecoveryNotStarted`, the dispatcher retries. On retry, the settler re-enters and hits the problem identified in §5 below (no idempotency on individual payout calls). **This is resolved by the C-11 refactor in §5.**

After the C-11 refactor: terminal-status transition is the last write in the atomic batch. Either the entire batch commits (including terminal status) or nothing commits. `RecoveryProbe` returning `RecoveryCompleted` on terminal status is correct.

### 3.3 3b — Every code path that sets terminal status

Grep results for assignments to `TaskStatusCompleted`, `TaskStatusRejected`, `TaskStatusDisputedResolved`:

| File:Line | Function | Path |
|-----------|----------|------|
| `tasks.go:998` | `ApplyVerificationConsensusResolution` | **Settlement flow (the one this consumer wraps)** |
| `tasks.go:1001` | `ApplyVerificationConsensusResolution` | **Settlement flow** |
| `tasks.go:1003` | `ApplyVerificationConsensusResolution` | **Settlement flow** |
| `tasks.go:937` | `ApproveTask` | Legacy single-validator approve path |
| `tasks.go:960` | `ResolveDispute` | Manual dispute resolution |
| `tasks.go:1334` | `applyTaskApproved` | Recognition consumer for `TaskApproved` events |

The three non-settlement paths (`ApproveTask`, `ResolveDispute`, `applyTaskApproved`) are legacy/manual paths from the pre-multi-validator architecture. On the live protocol, `TaskVerificationConsensus` events are the only path that reaches `ApplyVerificationConsensusResolution`. The other paths could cause `RecoveryProbe` to return `RecoveryCompleted` for a task whose settlement the dispatcher never invoked — BUT:

- `ApproveTask` requires explicit HTTP API call; it does not fire from the recognition fabric.
- `ResolveDispute` requires explicit HTTP API call.
- `applyTaskApproved` fires from legacy `TaskApproved` DAG events, which are not emitted by the multi-validator pipeline.

**Assessment**: In the multi-validator architecture, only `ApplyVerificationConsensusResolution` (the settlement flow) sets terminal status for tasks that pass through the verification-consensus pipeline. The legacy paths cannot interfere because they require different event types or explicit API calls. The probe is correct.

---

## 4. Cross-cutting §8.1 integration

`escrow.ReleaseNet` (`escrow.go:186-248`) already sets `WorkerPaid/ValidatorPaid/TreasuryPaid` flags after each successful transfer and persists them via `e.persist(entry)`. This IS §8.1.

However: **the settler's accept/reject/dispute paths do NOT use `ReleaseNet`.** They call `TransferFromBucket` directly. The `ReleaseNet` paid flags only protect the legacy task-settler closure path in `cmd/node/main.go`. The settler's direct `TransferFromBucket` calls have no per-transfer idempotency guard.

This is the motivation for the C-11 refactor in §5.

---

## 5. C-11 resolution — per-transfer paid-flag idempotency via extended `ReleaseNet`

### 5.1 The problem

The settler's accept path performs 4+ individual `TransferFromBucket` calls (worker, validators, gen-ledger, treasury) plus a task-status transition. None are in a single transaction. A crash mid-settlement and a retry would double-pay because `TransferFromBucket` generates unique synthetic EventIDs per call (counter-based), so the retry creates NEW ledger entries rather than finding the old ones.

`escrow.ReleaseNet` already solves this for a 3-recipient distribution via per-transfer paid flags (`WorkerPaid`, `ValidatorPaid`, `TreasuryPaid`). The refactor extends this mechanism to cover the full v4.1 economic distribution.

### 5.2 Why not raw `db.Update` wrapping

The transfer ledger is an in-memory `map[EventID]*TransferEntry` protected by a mutex. Wrapping multiple `TransferFromBucket` calls in a single `db.Update` only atomicizes the BadgerDB persistence — the in-memory mutations are still independent. A crash after an in-memory mutation but before `db.Update` commit leaves inconsistent state. Restructuring the ledger to be fully BadgerDB-transactional is a much larger refactor outside this workstream's scope.

### 5.3 Refactor: Shape B — distribute inside `ReleaseNet` with extended paid tracking

**Choice**: Shape B. The validator pool and gen-ledger distributions happen inside `ReleaseNet` itself, not in a subsequent step. No holding bucket. No intermediate account. All transfers go directly from the escrow bucket to final recipients, each guarded by a per-recipient paid flag persisted on the `EscrowEntry`.

**Gen-ledger routing decision**: Gen-ledger royalties (2% on accept) are treated as a fourth recipient class in the extended `ReleaseNet`. They are NOT absorbed into treasury. The v4.1 decomposition — 73% worker / 23% validators / 2% generation / 2% treasury — is preserved visibly at the ledger level. Each recipient account's balance change is traceable to its specific line in the model.

### 5.4 Data structure changes

Extend `EscrowEntry` with per-recipient paid tracking:

```go
type EscrowEntry struct {
    // ... existing fields ...

    // Settlement paid tracking — per-recipient idempotency guards.
    // Set after each successful transfer; persisted via store.PutEscrow.
    // On retry, already-paid recipients are skipped (CRITICAL-3).
    WorkerPaid    bool `json:"worker_paid"`
    ValidatorPaid bool `json:"validator_paid"`
    TreasuryPaid  bool `json:"treasury_paid"`

    // Extended paid tracking for the full v4.1 distribution.
    // ValidatorsPaid tracks individual validator payouts by AgentID.
    // GenLedgerPaid tracks gen-ledger royalty payouts by recipient AgentID.
    // Both maps are nil until the first payout attempt.
    ValidatorsPaid map[string]bool `json:"validators_paid,omitempty"`
    GenLedgerPaid  map[string]bool `json:"gen_ledger_paid,omitempty"`
}
```

`WorkerPaid`, `ValidatorPaid`, `TreasuryPaid` already exist on `EscrowEntry`. The new fields `ValidatorsPaid` and `GenLedgerPaid` extend the pattern to N-recipient distributions.

### 5.5 Extended `ReleaseNet` — new method `ReleaseSettlement`

Rather than modifying the existing `ReleaseNet` signature (which would break all existing callers), add a new method:

```go
func (e *Escrow) ReleaseSettlement(
    taskID string,
    worker     crypto.AgentID, workerAmount uint64,
    validators map[crypto.AgentID]uint64,  // per-validator Q-weighted payouts
    genRecipients map[crypto.AgentID]uint64, // gen-ledger royalty payouts
    treasury   crypto.AgentID, treasuryAmount uint64,
) error
```

**Implementation flow** (inside `ReleaseSettlement`):

1. **Worker** — guarded by `entry.WorkerPaid`:
   ```
   if !entry.WorkerPaid {
       TransferFromBucket(escrow, worker, workerAmount)
       entry.WorkerPaid = true; persist(entry)
   }
   ```

2. **Validators** — guarded by `entry.ValidatorsPaid[validatorID]`:
   ```
   for validatorID, amount := range validators {
       if entry.ValidatorsPaid == nil { entry.ValidatorsPaid = map... }
       if !entry.ValidatorsPaid[string(validatorID)] {
           TransferFromBucket(escrow, validatorID, amount)
           entry.ValidatorsPaid[string(validatorID)] = true; persist(entry)
       }
   }
   ```

3. **Gen-ledger** — guarded by `entry.GenLedgerPaid[recipientID]`:
   ```
   for recipientID, amount := range genRecipients {
       if entry.GenLedgerPaid == nil { entry.GenLedgerPaid = map... }
       if !entry.GenLedgerPaid[string(recipientID)] {
           TransferFromBucket(escrow, recipientID, amount)
           entry.GenLedgerPaid[string(recipientID)] = true; persist(entry)
       }
   }
   ```

4. **Treasury** — guarded by `entry.TreasuryPaid`:
   ```
   if !entry.TreasuryPaid && treasuryAmount > 0 {
       TransferFromBucket(escrow, treasury, treasuryAmount)
       entry.TreasuryPaid = true; persist(entry)
   }
   ```

5. **Cleanup** — after all transfers: delete entry from memory and store.

Each persist writes the full `EscrowEntry` (including all paid flags and maps) to BadgerDB via `store.PutEscrow`. A crash at ANY point between transfers leaves a partially-paid entry; retry skips already-paid recipients and resumes from the next unpaid one.

### 5.6 Idempotency guarantees on retry

**Crash between worker and first validator**: `WorkerPaid=true` persisted; retry skips worker, pays validators. No double-pay.

**Crash between second and third validator**: `ValidatorsPaid["v1"]=true, ValidatorsPaid["v2"]=true` persisted; retry skips v1 and v2, pays v3. No double-pay.

**Crash after all transfers but before entry deletion**: all paid flags true; retry enters `ReleaseSettlement`, all guards skip, entry deleted. No double-pay.

**Crash after entry deletion**: `Get(taskID)` returns `ErrEscrowNotFound`; settler's idempotency check (`task.Status == terminal`) returns `AlreadyApplied`. No action.

### 5.7 Settler refactor

**`settleAccept`**: replaces 4+ direct `TransferFromBucket` calls with:

```go
validators := s.computeValidatorPayouts(round, escrowBucket, validatorPool)
genRecipients := s.computeGenLedgerPayouts(payload, genPool)
return s.escrowMgr.ReleaseSettlement(
    payload.TaskID,
    workerID, workerAmount,
    validators,
    genRecipients,
    s.treasuryID, treasuryAmount,
)
```

`computeValidatorPayouts` extracts the Q-weighted computation from `distributeByQuality` without executing transfers. Returns `map[crypto.AgentID]uint64`. `computeGenLedgerPayouts` uses `s.genLedger.Calculate(...)` to get recipients and amounts. Returns `map[crypto.AgentID]uint64`.

**`settleReject`**: `ReleaseSettlement` with worker slot = poster (refund), validators = agreeing fail-voters, genRecipients = nil, treasury.

**`settleDispute`**: `ReleaseSettlement` with worker slot = worker (half), validators = nil (dispute has no validator payouts; all non-worker portions go to treasury), genRecipients = nil, treasury = remaining.

Poster refund in dispute goes through a separate `TransferFromBucket` guarded by a new `PosterRefundPaid` flag on `EscrowEntry`:
```go
PosterRefundPaid bool `json:"poster_refund_paid,omitempty"`
```

### 5.8 v4.1 decomposition preservation

At every intermediate step, each recipient's balance change traces to the model:

| Step | Accept | Reject | Dispute |
|------|--------|--------|---------|
| Worker | +73% budget | — | +36.5% |
| Poster | — | +73% budget (refund) | +36.5% |
| Each validator | +Q-weighted share of 23% | +Q-weighted share of 23% | — |
| Gen-ledger recipients | +shares of 2% | — | — |
| Treasury | +2% (+ rounding) | +4% (2% protocol + 2% redirected gen) | +27% |
| **Total** | **100%** | **100%** | **100%** |

No intermediate holding buckets. All transfers go directly from `escrow:<taskID>` to the final recipient. The escrow bucket balance decreases monotonically. At completion, escrow bucket balance = 0.

---

## 6. PrerequisiteSchemaVersion

Version `1`. Semantics: "no explicit prerequisites; correctness depends on DAG strict CausalRefs enforcement + dispatcher failed-retryable retry."

---

## 7. Conformance suite integration

`internal/dispatch/tv_consensus_consumer_test.go`:

```go
func TestTVConsensusConsumer_Conformance(t *testing.T) {
    conformance.RunTypeAConformance(t, func() (dispatch.Consumer, func()) {
        c := newTestTVConsensusConsumer(t)
        return c, func() {}
    })
}
```

The factory constructs a consumer with in-memory test doubles (mock task manager, mock escrow, mock transfer ledger).

---

## 8. Test plan

### 8.1 Unit tests

- `TestTVConsensusConsumer_Name` — returns `"tv_consensus_settlement"`.
- `TestTVConsensusConsumer_Type` — returns `TypeA`.
- `TestTVConsensusConsumer_Interested` — true for `EventTypeTaskVerificationConsensus`.
- `TestTVConsensusConsumer_Prerequisites` — returns nil.
- `TestTVConsensusConsumer_PrerequisiteSchemaVersion` — returns 1.
- `TestTVConsensusConsumer_Apply_AcceptPath` — task + escrow + consensus event; asserts balances.
- `TestTVConsensusConsumer_Apply_Idempotent` — second Apply is no-op (task terminal).
- `TestTVConsensusConsumer_RecoveryProbe_Completed` — task terminal → RecoveryCompleted.
- `TestTVConsensusConsumer_RecoveryProbe_NotStarted` — task not terminal → RecoveryNotStarted.

### 8.2 Integration test

- `TestDispatcherFlow_TVConsensusConsumer` — full admission flow via Admit.

### 8.3 Conformance

- Type A template (7 tests) against real implementation.

---

## 9. Sub-commit ordering

Estimated 7 sub-commits.

1. **Add `ValidatorsPaid`, `GenLedgerPaid`, `PosterRefundPaid` fields to `EscrowEntry` + `ReleaseSettlement` method.**
   - Extend `EscrowEntry` with per-recipient paid maps.
   - Implement `ReleaseSettlement` with the full idempotency-guard flow from §5.5.
   - Unit tests for crash-safe semantics: partial-pay recovery, all-paid no-op, entry deletion.
   - Verify: `go test ./internal/escrow/...`.

2. **Refactor settler accept/reject/dispute paths to use `ReleaseSettlement`.**
   - Extract `computeValidatorPayouts` and `computeGenLedgerPayouts` from the settler.
   - Replace direct `TransferFromBucket` calls with `ReleaseSettlement` invocations.
   - Tests verify identical ledger outcomes vs old path.
   - Verify: `go test ./internal/settlement/...`.

3. **Create `internal/dispatch/tv_consensus_consumer.go`.**
   - Type definition, all 7 interface methods.
   - Godoc on `Prerequisites` names the DAG strict-enforcement invariant.
   - Verify: `go build ./internal/dispatch/...`.

4. **Add `TVConsensusConsumer` unit tests + conformance.**
   - All tests from §8.1 + Type A conformance template.
   - Verify: `go test ./internal/dispatch/...`.

5. **Wire dispatcher + consumer registration in `cmd/node/main.go`.**
   - Instantiate dispatcher, register consumer, wire `Recover`, wire `NotifyProjection`.
   - Modify recognition consumer to route settlement through `dispatcher.Admit`.
   - Verify: `go build ./cmd/node`.

6. **Lessons entry + plan document.**
   - `docs/lessons.md`: "only ApplyVerificationConsensusResolution sets terminal status on multi-validator-pipeline tasks" assumption.
   - Include plan document.
   - Full repo test sweep.
   - Verify: `go test -race ./...`.

7. **(If needed) Fix any test breakage from the recognition consumer migration.**
   - Tests in `internal/recognition/` that depended on the old direct-invocation path.
   - Verify: `go test -race ./...`.

---

## 10. Out of scope

- Cross-cutting §8.2 (synthetic transfer relabeling). Separate prompt.
- Part F (historical task annotation). Separate prompt.
- Other consumer registrations. Separate workstreams.
- Live testnet verification.

---

## 11. Sign-off

Revised with follow-up resolutions. Awaiting founder approval.
