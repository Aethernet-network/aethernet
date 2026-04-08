# Prompt 06 Plan — Round Finalization with BFT Consensus and Diversity Floor

**Date:** 2026-04-07
**Status:** Awaiting approval

## Objective

Implement finalization logic for TaskVerificationRounds: accept (BFT supermajority + diversity floor + score threshold), reject (BFT supermajority), dispute (deadline expiry). Emit TaskVerificationConsensus DAG events. No settlement yet.

## Finalization Trigger Model

Two triggers:
1. **Event-driven**: the vote consumer invokes the finalizer after every vote application. If the vote tips the round to supermajority, finalization happens immediately.
2. **Periodic**: the deadline checker scans open rounds every 5 seconds. Handles deadline expiry and extension.

Both triggers are idempotent — calling finalize on an already-finalized round is a no-op.

## BFT Supermajority Calculation

`threshold = ceil(totalActiveWeight * 6667 / 10000)` — same 66.67% as existing consensus. `totalActiveWeight` comes from `lifecycleReducer.Snapshot().ActiveWeight()`.

## Diversity Floor Enforcement

Acceptance requires `round.DistinctPassFamilies() >= DiversityFloor` (default 2). Rejection does NOT require diversity by default (`EnforceFailDiversity=false`).

## Median Score

Computed from passing votes' ScoreBP values. Odd count → middle value. Even count → average of two middle values (rounded down).

## Which Validator Emits the Consensus Event

The first validator whose local round meets finalization criteria. Each validator runs its own finalizer. The round tracks whether finalization has been applied locally via the State field. Duplicate consensus events from different validators for the same round are handled by the consensus consumer's idempotency (round already finalized → no-op).

## Deadline Extension

Original deadline expired + one verdict has ≥50% weight but <supermajority → extend once. Extended deadline expired → dispute. Extension tracked via `ExtendedUntilUnix`.

## Late Votes

Already handled by prompt 03's `RecordPostFinalizationVote` — appended for audit, no state change.

## Vote Consumer Changes

Add `finalizer`, `publisher`, `kp`, `validatorID`, and `totalActiveWeightFn` to the consumer. After applying a vote and saving, call `finalizer.Evaluate`. If finalization triggers, apply to round, save, emit consensus event.

The consensus event emission follows the Settlement event pattern from main.go:1700-1714: build payload, create event with semantic parent (SubmissionEventID), sign, publish.

## Consensus Consumer

Subscribes to `EventTypeTaskVerificationConsensus`. Loads the round, applies finalization if not already finalized. Critical for replay safety.

## Files to Create

- `internal/taskverification/finalizer.go`
- `internal/taskverification/finalizer_test.go`
- `internal/taskverification/deadline_checker.go`
- `internal/taskverification/deadline_checker_test.go`
- `internal/recognition/task_verification_consensus_consumer.go`
- `internal/recognition/task_verification_consensus_consumer_test.go`

## Files to Modify

- `internal/recognition/task_verification_vote_consumer.go` — add finalizer invocation + consensus event emission
- `cmd/node/main.go` — wire finalizer, deadline checker, consensus consumer

## Test Strategy

### finalizer_test.go
- Accept: supermajority + diversity + score threshold → accept
- Accept insufficient weight → no finalization
- Accept insufficient diversity → no finalization
- Accept insufficient score → no finalization
- Reject supermajority → reject
- Deadline expired → dispute
- Already finalized → no-op
- Median score: odd, even, empty

### deadline_checker_test.go
- Expired round → finalizes as dispute
- Convergent round → extends
- Already extended → finalizes
- Not expired → no action
- Start/Stop (goroutine safety)

### consensus_consumer_test.go
- Applies finalization to open round
- Idempotent on already-finalized round
- Replay: cold round + consensus event → finalized

## Dependencies

Only existing packages. No new external dependencies.
