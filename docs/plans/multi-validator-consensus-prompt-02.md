# Prompt 02 Plan — Open Verification Round on TaskSubmitted Recognition

**Date:** 2026-04-07
**Status:** Awaiting approval

## Objective

Wire a recognition fabric consumer that opens a `TaskVerificationRound` on every `TaskSubmitted` event. Idempotent across local/remote/replay. No voting, scoring, or finalization.

## Where the Consumer Fits

The consumer registers alongside existing consumers in the recognition fabric's wiring section of `cmd/node/main.go` (lines 1655-1698). It runs AFTER the task lifecycle consumer (which applies TaskSubmitted to the TaskManager) so the task data is available for lookup. Registration order in the fabric is not execution order — the bus dispatches to all interested consumers for each event — but the task lifecycle consumer's `Ready()` always returns true and `Consume()` always succeeds, so by the time our consumer's `Consume()` runs, the task state is available.

If the task is NOT yet in the TaskManager (startup race, or task lifecycle consumer failed), our consumer returns an error and the bus logs a warning. The recognition fabric does not retry on error, but the deterministic round ID ensures the round is eventually created on the next appearance of the event (replay, repair).

## Interaction with Existing Task Lifecycle Consumer

The task lifecycle consumer (`task_consumer.go`) applies `TaskSubmitted` to the TaskManager. Our consumer reads the task to get metadata (category, worker, poster). Both subscribe to `EventTypeTaskSubmitted`. Both run independently. Our consumer does NOT modify task state — it only reads and creates a round.

## Validator-Set Snapshot Binding

The round captures `ValidatorSetVersion` from the lifecycle reducer's current snapshot at round-open time. This matches the pattern used by `VotingRound` in `consensus/voting.go:480-484`. In bootstrap mode, `Committee = nil` means all active validators are eligible.

The consumer receives a function `func() uint64` that returns the current validator set version. This is simpler and more testable than injecting the full snapshot object — the consumer only needs the version number at this stage.

## Analyzer Policy

Hardcoded `"bootstrap_v1"` with `DiversityFloor=2`, `AcceptanceThresholdBP=6000`. Configurable in prompt 08.

## Idempotency

Primary: deterministic `RoundID` from `NewRoundID(submissionEventID)` — same event always produces the same round ID on every node.

Secondary: `Consume()` checks `store.LoadRoundBySubmissionEvent()` first. If found, returns nil (no-op).

## Interfaces Needed

The `CommitConsumer` interface takes `(ctx, *event.Event)`. The consumer needs:

1. **Task lookup** — define a minimal `TaskLookup` interface:
   ```go
   type TaskLookup interface {
       Get(taskID string) (*tasks.Task, error)
   }
   ```
   `*tasks.TaskManager` satisfies this.

2. **Validator set version** — inject `func() uint64` returning current version. In main.go: `func() uint64 { return lifecycleReducer.Snapshot().SetVersion() }`.

3. **Round store** — `taskverification.Store` from prompt 01.

4. **BadgerDB access** — Add `DB() *badger.DB` method to `store.Store` (one-line accessor). The prompt requires using the same DB instance.

## Files to Create

- `internal/recognition/task_verification_round_consumer.go`
- `internal/recognition/task_verification_round_consumer_test.go`

## Files to Modify

- `internal/taskverification/round.go` — add `OpenRound` constructor with `OpenRoundParams`
- `internal/taskverification/round_test.go` — add `OpenRound` tests
- `internal/store/store.go` — add `DB() *badger.DB` accessor (one line)
- `cmd/node/main.go` — wire consumer, create `BadgerStore` from shared DB

## Test Strategy

### round_test.go additions
- `TestOpenRound_ValidParams` — all fields populated correctly
- `TestOpenRound_DeterministicID` — same params → same round ID
- `TestOpenRound_ValidationErrors` — empty task ID, zero deadline, etc.

### task_verification_round_consumer_test.go
- `TestRoundConsumer_OpensRoundOnTaskSubmitted` — basic creation
- `TestRoundConsumer_Idempotent` — same event twice → one round
- `TestRoundConsumer_BindsValidatorSetVersion` — captures version
- `TestRoundConsumer_DeterministicRoundID` — same event → same ID
- `TestRoundConsumer_IgnoresNonTaskSubmitted` — different event types
- `TestRoundConsumer_ErrorsIfTaskMissing` — task not in store
- `TestRoundConsumer_BootstrapModeCommitteeNil` — committee is nil
- `TestRoundConsumer_RaceConditionTwoConcurrentCommits` — concurrency safety

Tests use in-memory BadgerDB (`badger.DefaultOptions("").WithInMemory(true)`) for the taskverification store, and a stub `TaskLookup` implementation.

## Dependencies

Only existing codebase packages. No new external dependencies.
