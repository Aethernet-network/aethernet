# Prompt 09 Plan — Hardening, Slashing, Cleanup, and Full Testnet Verification

**Date:** 2026-04-08
**Status:** Awaiting approval

## Objective

Final prompt. Conservative slashing, deprecated code deletion, and 15-gate non-negotiable end-to-end testnet verification. After this, compound verification is architecturally true and observed working.

## Slashing Model

### Soft Slashing (Reputation)
- **Trigger**: Validator's vote deviated from final consensus verdict
- **Action**: Reduce reputation via `ReputationStore.RecordVote(agreed=false)`
- **Effect**: Lower AgreementRate → lower Q score → lower fee share in future settlements
- **No stake impact**: reputation only
- **Skipped during calibration**: via `CalibrationStore.IsCalibrated` check

### Hard Slashing (Stake)
- **Trigger 1**: Equivocation (same validator, same family, conflicting votes) — detected in prompt 03's aggregator
- **Trigger 2**: Systematic divergence (agreement rate < 30% over 50+ votes after calibration)
- **Action**: Emit `SlashingChallenge` DAG event → challenge window (60s on testnet) → if unchallenged, apply via existing `SlashEngine.Slash()`
- **Evidence**: The conflicting vote event IDs (equivocation) or reputation history (systematic divergence)
- **Semantic parent**: The `TaskVerificationConsensus` event of the triggering round

### Challenge Window
- 60 seconds on testnet (configurable)
- Processed by the deadline checker's existing periodic scan pattern
- If a counter-evidence event arrives proving the challenge was incorrect, the slash is cancelled
- For prompt 09: the challenge window exists but counter-evidence is not implemented (future work). Slashing challenges auto-apply after the window.

## Cleanup Plan

### Delete from autovalidator/auto.go
- The legacy path in `processSubmittedTasks` (lines 457-646) — everything after `// LEGACY PATH`
- `verifyTaskSettlement` function (lines 1123-1127) — trivial stub
- The `multiVoter == nil` check becomes a fatal error instead of fallback

### Keep in settlement/applicator.go
- `applyTaskSettlement`, `SetTaskSettler`, `taskSettler` interface — these are minimal dispatch code still needed for the OCS path on Transfer/Generation events. The guard added in prompt 07 stays as defense-in-depth.

### Test updates
- Existing auto_test.go tests that rely on the legacy path (all 7 current tests) need updating to wire a MultiVoter. Tests that exercise dispute resolution, claim timeout, and stop idempotency remain but are adapted.
- Tests for `verifyTaskSettlement` are deleted.

## SlashingEvaluator

```go
type SlashingEvaluator struct {
    reputation  *ValidatorReputationStore
    calibration *CalibrationStore
    policy      SlashingPolicy
}
```

`EvaluateRound(round)` returns `[]SlashingAction`:
1. Skip if all (category, family) pairs in this round are uncalibrated
2. For each vote that disagreed with consensus: soft slash action
3. For equivocation flags: hard slash action with the two conflicting event references
4. Check systematic divergence: if voter's agreement rate < 30% over 50+ votes for this (family, category) AND calibrated: hard slash action

## Testnet Verification

All 15 gates from the supplemental are non-negotiable. The plan is:
1. Build on EC2 from clean state
2. Deploy to all 5 nodes with per-node analyzer configs
3. Verify startup on all nodes
4. Baseline grant consensus test
5. Start worker on Mac mini (or simulate locally)
6. Post research task
7. Observe full pipeline on logs
8. Verify diversity floor met
9. Verify worker balance
10. Verify economic totals
11. Verify reputation updates
12. Verify no old-path activity
13. Run second task for stability
14. Document all observations with timestamps
15. Make the statement with evidence

## Files to Create

- `internal/taskverification/slashing.go` + test
- `internal/event/slashing_challenge.go` (event type + payload only)

## Files to Modify

- `internal/autovalidator/auto.go` — delete legacy path, make multiVoter mandatory
- `internal/autovalidator/auto_test.go` — update tests to wire MultiVoter
- `internal/recognition/task_verification_consensus_consumer.go` — invoke slashing evaluator
- `internal/event/event.go` — add EventTypeSlashingChallenge constant
- `cmd/node/main.go` — wire slashing evaluator
- `docs/lessons.md` — final lessons

## Dependencies

Uses existing `validator.SlashEngine` for hard slashing. No new external dependencies.
