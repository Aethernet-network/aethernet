# Prompt 03 Plan — TaskVerificationVote Event Type and Recognition Path

**Date:** 2026-04-07
**Status:** Awaiting approval

## Objective

Add the `TaskVerificationVote` DAG event type, its vote aggregation into rounds, and both async (recognition fabric) and sync (syncHandler) ingestion paths. No scoring, no finalization, no analyzer registry.

## Event Payload Structure

Defined in `internal/event/event.go` alongside other payload types (no separate file — the codebase keeps all event payloads in event.go):

```go
type TaskVerificationVotePayload struct {
    Version              uint8             `json:"v"`
    RoundID              string            `json:"round_id"`
    TaskID               string            `json:"task_id"`
    SubmissionEventID    string            `json:"submission_event_id"`
    ValidatorID          string            `json:"validator_id"`
    Verdict              string            `json:"verdict"` // "pass" | "fail" | "abstain"
    ScoreBP              uint64            `json:"score_bp"`
    ScoreBreakdown       map[string]uint64 `json:"score_breakdown,omitempty"`
    AnalyzerFamily       string            `json:"analyzer_family"`
    AnalyzerVersion      string            `json:"analyzer_version"`
    PolicyVersion        string            `json:"policy_version"`
    AnalysisArtifactHash string            `json:"analysis_artifact_hash,omitempty"`
    TimestampUnix        int64             `json:"timestamp_unix"`
}
```

Uses plain `string` for IDs (matching existing payload patterns like `VerificationVotePayload`). The `Version` field follows the existing `uint8` pattern.

## Signing and Verification

Standard `crypto.SignEvent(ev, kp)` pattern. The event's `AgentID` is the validator's hex-encoded public key. `VerifyEvent(ev)` validates the signature. No changes to signing infrastructure.

## Fast Path Admission

**No changes needed.** The Fast Path's `AdmitHeader()` in `internal/network/ingest.go` admits ALL event types without an allowlist. The new event type flows through the existing pipeline automatically.

## Semantic Parent

`CausalRefs = []event.EventID{submissionEventID}` — the TaskSubmitted event is the only parent. This follows the lesson from `docs/lessons.md` and matches the existing `VerificationVote` pattern (target event as sole parent).

## Recognition Consumer

`TaskVerificationVoteConsumer` subscribes to `EventTypeTaskVerificationVote` and applies votes to rounds.

**Ready() logic:** Checks if the round exists in the store. If not, defers with prerequisite key `"tv_round:" + roundID`. The round consumer (prompt 02) should eventually create the round, but we need to signal this key.

**Solution:** Add a prerequisite signal in the round consumer's Consume(): after successfully saving a round, signal `"tv_round:" + roundID`. This uses the same Activator pattern as the TaskLifecycleConsumer signaling task_metadata.

**Consume() logic:**
1. Decode payload
2. Load round from store
3. Look up validator stake via injected weight lookup
4. Build VoteRecord
5. Call `ApplyVoteToRound(round, record)` — aggregator function
6. Save round
7. Log vote applied

## Synchronous syncHandler Path

Per the lesson from commit 9989475: consensus-critical events need synchronous handling alongside the async fabric. A new case in the syncHandler switch for `EventTypeTaskVerificationVote` that:
1. Decodes the payload
2. Loads the round
3. Looks up stake
4. Calls `ApplyVoteToRound` 
5. Saves the round

Both paths are idempotent via the aggregator's duplicate detection.

## Aggregator

Separated into `internal/taskverification/aggregator.go` for testability.

`ApplyVoteToRound(round, record)` returns `AggregationResult`:
- Checks round is Open
- Detects duplicates (same validator, same verdict+score → no-op)
- Detects equivocation (same validator, different verdict or score → error + flag)
- Updates weight counters and ParticipatingFamilies
- Returns applied/duplicate/equivocation status

## Validator Weight Lookup

Injected as `func(crypto.AgentID) (weight uint64, eligible bool)`. In main.go, wraps `lifecycleReducer.Snapshot().VoteWeightByKey(agentID)`. Matches the existing `ValidatorSetSource` pattern from `consensus/voting.go`.

## Deferred Activation for Votes Before Round

The vote consumer defers if the round doesn't exist yet. The round consumer signals `"tv_round:" + roundID` after creating a round. This requires wiring the Activator into the round consumer (same pattern as prompt 02's task_metadata signaling).

## Files to Create

- `internal/taskverification/aggregator.go`
- `internal/taskverification/aggregator_test.go`
- `internal/recognition/task_verification_vote_consumer.go`
- `internal/recognition/task_verification_vote_consumer_test.go`

## Files to Modify

- `internal/event/event.go` — add EventTypeTaskVerificationVote constant + TaskVerificationVotePayload
- `cmd/node/main.go` — add syncHandler case + wire vote consumer + signal from round consumer
- `internal/recognition/task_verification_round_consumer.go` — add Activator + signal round creation

## Test Strategy

### aggregator_test.go
- Pass/fail/abstain votes update correct weights
- Duplicate identical vote is no-op
- Equivocation (different verdict or score) returns error
- Round not open → error
- Multiple families tracked correctly
- Stake weighting accumulates correctly
- Post-finalization vote recording

### task_verification_vote_consumer_test.go
- Valid vote applied to round
- Idempotent duplicate
- Equivocation detected and logged
- Ineligible validator dropped
- Round not yet open → deferred
- Post-finalization vote recorded as audit
- Stake weighting correct
- Multiple families tracked

### event.go payload tests (in event_test.go or inline)
- Payload validates correctly
- Invalid verdict/score rejected
- Required fields checked

## Dependencies

Only existing packages. No new external dependencies.
