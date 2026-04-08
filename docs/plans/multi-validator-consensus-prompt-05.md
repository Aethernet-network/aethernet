# Prompt 05 Plan — Refactor Autovalidator to Emit Votes

**Date:** 2026-04-07
**Status:** Awaiting approval

## Objective

Replace the autovalidator's unilateral scoring/settling with a MultiVoter that runs configured analyzers and emits one TaskVerificationVote per family. Old paths deprecated but not deleted.

## New Autovalidator Flow

1. `processSubmittedTasks` polls for `TaskStatusSubmitted` tasks (unchanged)
2. Evidence readiness gate (unchanged)
3. Look up `TaskVerificationRound` by submission event ID
4. If round not found or not Open → skip (round consumer hasn't created it yet, or already finalized)
5. Call `multiVoter.ScoreAndVote(ctx, round, input)` which:
   - Groups configured analyzers by family
   - For each family, checks if this validator already voted (scan round.Votes for matching validator+family)
   - Runs each analyzer with a 30s per-analyzer context timeout
   - Builds `TaskVerificationVotePayload` from output
   - Creates event with `SubmissionEventID` as semantic parent
   - Signs with `av.kp` and publishes via `av.publisher`
6. Does NOT call settleTask or RejectSubmission

## Key Design Decisions

**Setter pattern, not constructor change:** AutoValidator uses setters (SetPublisher, SetDAG, etc.). I'll add `SetMultiVoter(*MultiVoter)` following the same pattern. This avoids changing the constructor signature which would break all existing tests.

**Per-family votes from same validator:** A validator running 2 families emits 2 separate vote events. This is correct — each family's vote carries independent analytical signal.

**Aggregator fix required:** The current `ApplyVoteToRound` keys equivocation on `ValidatorID` alone. Must change to `(ValidatorID, AnalyzerFamily)`. Same validator, different family = allowed. Same validator, same family, different verdict/score = equivocation.

**Round lookup requires SubmissionEventID:** The autovalidator currently iterates tasks by status. It needs the TaskSubmitted event's ID to look up the round. The task struct stores `SubmitEventID` (added in prompt's semantic parents commit f234b6b).

**Graceful fallback:** If `multiVoter == nil`, log warning and use old path. This is a safety net during migration only.

## Aggregator Fix

Change duplicate/equivocation detection key from `ValidatorID` to `(ValidatorID, AnalyzerFamily)`:

```go
for _, existing := range round.Votes {
    if existing.ValidatorID == vote.ValidatorID && existing.AnalyzerFamily == vote.AnalyzerFamily {
        // same validator + same family → check duplicate vs equivocation
    }
}
```

## Files to Create

- `internal/autovalidator/multi_voter.go` — MultiVoter struct + ScoreAndVote
- `internal/autovalidator/multi_voter_test.go` — tests with mock analyzers/publisher

## Files to Modify

- `internal/autovalidator/auto.go` — add `multiVoter` field, `SetMultiVoter`, refactor `processSubmittedTasks`
- `internal/taskverification/aggregator.go` — fix equivocation key to (ValidatorID, AnalyzerFamily)
- `internal/taskverification/aggregator_test.go` — add multi-family tests
- `cmd/node/main.go` — construct MultiVoter, pass to autovalidator

## Test Strategy

### multi_voter_test.go
- Single family → one vote emitted
- Multiple families → one vote per family
- Already voted → skip
- Analyzer failure → continues with others
- Round not open → early return
- Vote payload fields correct

### aggregator_test.go additions
- Same validator, different families → both accepted
- Same validator, same family, different verdict → equivocation
- Same validator, same family, same verdict → duplicate no-op

### auto_test.go
- Existing tests that rely on settleTask/RejectSubmission: update to work with MultiVoter or run with nil MultiVoter (old path fallback for backward compat in test)

## Dependencies

Uses existing packages only. MultiVoter takes `[]verification.Analyzer`, `taskverification.Store`, publisher interface, `*crypto.KeyPair`, `crypto.AgentID`.
