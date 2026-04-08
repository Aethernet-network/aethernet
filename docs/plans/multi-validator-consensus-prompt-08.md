# Prompt 08 Plan — Calibration, Reputation, Observability + Q-Weighted Distribution

**Date:** 2026-04-08
**Status:** Awaiting approval

## Objective

Add calibration mode, validator reputation tracking, observability metrics, AND upgrade the settler from even-split to Q-weighted validator distribution. Also wire the GenerationLedgerCalculator's qualityFn to reputation-based Q.

## Calibration Store

BadgerDB-backed under `cal:` prefix. Key format: `cal:<category>:<family_id>` → uint64 counter (little-endian bytes). Incremented on each round finalization for that (category, family). Threshold default 100, per-category and per-family overrides configurable.

Calibration mode affects: slashing (disabled during calibration, prompt 09) and reputation (votes still recorded). Does NOT affect consensus weight — votes count normally during calibration.

## Validator Reputation (Vote Agreement)

New store in `internal/taskverification/reputation.go` — separate from the existing `internal/reputation/` which tracks WORKER task completion. This tracks VALIDATOR vote agreement with consensus.

BadgerDB key: `tvr:<validator_id>:<family_id>:<category>` → JSON-serialized `ValidatorReputation`.

Core metric: `AgreementRate() = AgreeingVotes / TotalVotes` (0.0 for no history → treated as 1.0 neutral by Q calculation).

Updated on each round finalization: for each vote in the round, compare verdict to final consensus, increment agreeing or deviating counter.

## Q-Weighted Validator Distribution

Replace `distributeEvenly` calls in `settleAccept` and `settleReject` with `distributeByQuality`:

```go
func distributeByQuality(
    transfer *ledger.TransferLedger,
    from crypto.AgentID,
    recipients []crypto.AgentID,
    pool uint64,
    qScoreFn func(crypto.AgentID) float64,
) map[crypto.AgentID]uint64
```

Q score per validator: `Q(v) = AgreementRate(v, family, category)`. New validators (no history) get Q=1.0 (neutral). If ALL validators have Q=0 (impossible in practice but defend against it), fall back to even-split.

Weights: each validator's share = `Q(v) / ΣQ(all)` × pool. Remainder to last for determinism.

## GenerationLedger qualityFn

Replace the hardcoded `func(_ event.EventID) float64 { return 1.0 }` with a function that looks up the ancestor event's agent, queries the reputation store for their agreement rate, and returns it. New agents with no history return 1.0 (neutral).

## Metrics

Use the existing `metrics.Registry` from `internal/metrics`. Define a `VerificationMetrics` struct registered at startup alongside the existing `AetherNetMetrics`.

Counters: rounds opened/finalized (by outcome), votes emitted/applied/duplicate/equivocation, analyzer errors/timeouts.
Histograms: round duration, analyzer duration.
Gauges: open rounds, calibration progress.

## Structured Logging

Audit existing log lines in all verification components. Ensure consistent structured fields via `slog.With()` at consumer construction time.

## Files to Create

- `internal/taskverification/calibration.go` + test
- `internal/taskverification/reputation.go` + test
- `internal/taskverification/verification_metrics.go` (no test needed — metrics are integration-tested)

## Files to Modify

- `internal/settlement/verification_consensus_settler.go` — replace distributeEvenly with distributeByQuality
- `internal/settlement/verification_consensus_settler_test.go` — add Q-weighted tests
- `internal/recognition/task_verification_consensus_consumer.go` — update reputation on finalization, increment calibration
- `cmd/node/main.go` — wire calibration, reputation, metrics, Q-weighted settler

## Test Strategy

### calibration_test.go
- Increment persists, IsCalibrated below/at threshold, category/family overrides, concurrent increments

### reputation_test.go
- Record agreement/deviation, AgreementRate computation, persistence, list by validator

### settler Q-weighted tests (additions to existing file)
- All validators neutral Q (Q=1.0) → equals even-split
- One high-Q, one low-Q → high gets more
- All validators zero Q → fallback to even-split
- Single validator → gets full pool

## Dependencies

Existing packages only. Uses `internal/metrics`, `internal/taskverification`, `internal/settlement`.
