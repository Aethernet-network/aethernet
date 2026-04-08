# Prompt 07 Plan — Settlement with v4.1 Economics

**Date:** 2026-04-07
**Status:** Approved

## Objective

Wire settlement to process TaskVerificationConsensus events: accept → release escrow to worker (with fees), reject → refund to poster, dispute → 50/50 split. Tasks reach terminal state.

## Settlement Logic Location

New file `internal/settlement/verification_consensus_settler.go` — a focused settler for verification consensus outcomes. Not extending the existing `Applicator` (which handles OCS settlements for Transfer/Generation/TaskSettlement events). The two settlement paths are distinct: the old path processes `TaskSettlement` events via OCS BFT; the new path processes `TaskVerificationConsensus` events from the verification round system.

## Escrow Mechanics

- **Accept**: `escrow.ReleaseNet(taskID, worker, net, "", validatorAmt, treasury, treasuryAmt)` — same fee structure as existing settler: 10bp fee, 80% to validator, 20% to treasury
- **Reject**: `escrow.Refund(taskID)` — full amount back to poster
- **Dispute**: Two-step: `escrow.Refund(taskID)` to return full amount to poster, then `ledger.TransferFromBucket(posterID, workerID, halfAmount)` to give half to worker. Net effect: 50% to each. This avoids needing a new escrow method.

Actually, that's wrong — Refund returns to poster, then I'd need to take half from the poster. That assumes poster has the balance. Simpler:

- **Dispute**: Use `escrow.Release(taskID, workerID)` to give full to worker, then `ledger.TransferFromBucket(workerID, posterID, halfAmount)` to return half to poster. But this requires worker to have balance for the return...

Actually the cleanest: compute half amounts, then use the transfer ledger directly from the escrow bucket `"escrow:<taskID>"`:
1. `ledger.TransferFromBucket("escrow:<taskID>", workerID, workerHalf)`
2. `ledger.TransferFromBucket("escrow:<taskID>", posterID, posterHalf)`

This is what the ledger supports — direct transfers from the escrow bucket to any agent. The escrow entry tracks the state.

## Task State Transitions

New constants: `TaskStatusRejected = "rejected"`, `TaskStatusDisputedResolved = "disputed_resolved"`.

New method on TaskManager:
```go
func (m *TaskManager) ApplyVerificationConsensus(taskID string, verdict string) error
```
- "pass" → Submitted → Completed
- "fail" → Submitted → Rejected (new terminal state)
- "abstain" → Submitted → DisputedResolved (new terminal state)

Idempotent: already in terminal state → no-op.

## Idempotency

The settler tracks applied round IDs in a `map[string]struct{}` (keyed by RoundID, not EventID). Multiple consensus events for the same round from different validators are deduplicated. The map is rebuilt from task state on startup (any task in terminal state with a matching round has already been settled).

## Old Path Neutralization

In the existing task settler function (`cmd/node/main.go:1586`), add a guard: if `taskMgr.Get(taskID)` returns a task already in `Completed`, `Rejected`, or `DisputedResolved`, return nil (the new path already settled it). This prevents double-settlement without breaking compilation.

## Consensus Consumer Update

Add `settler` field to `TaskVerificationConsensusConsumer`. After applying round state, invoke `settler.Settle(ctx, payload)`. Idempotent.

## Files to Create

- `internal/settlement/verification_consensus_settler.go`
- `internal/settlement/verification_consensus_settler_test.go`

## Files to Modify

- `internal/tasks/tasks.go` — add Rejected + DisputedResolved states, ApplyVerificationConsensus method
- `internal/recognition/task_verification_consensus_consumer.go` — add settler field + invocation
- `cmd/node/main.go` — construct settler, pass to consumer, add guard in old path

## Test Strategy

### verification_consensus_settler_test.go
- Accept releases escrow with fees, task → Completed
- Reject refunds to poster, task → Rejected
- Dispute splits 50/50, task → DisputedResolved
- Idempotent on double-apply
- Unknown task → error

### tasks.go tests
- ApplyVerificationConsensus state transitions
- Idempotent on terminal state

## Dependencies

Existing packages only. Uses `escrow.Escrow`, `ledger.TransferLedger`, `tasks.TaskManager`, `fees.CalculateFee`.
