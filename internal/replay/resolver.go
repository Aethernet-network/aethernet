package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// Sentinel errors returned by ValidateOutcome.
var (
	// ErrEmptyJobID is returned when the outcome's JobID field is empty.
	ErrEmptyJobID = errors.New("replay: outcome job_id must not be empty")

	// ErrNegativeScoreDelta is returned when any CheckComparison has a
	// negative ScoreDelta (score deltas must be ≥ 0).
	ErrNegativeScoreDelta = errors.New("replay: score_delta must be non-negative")

	// ErrJobNotFound is returned when the outcome references a job that
	// does not exist in the store.
	ErrJobNotFound = errors.New("replay: job not found")

	// ErrOutcomeAlreadyTerminal is returned when an outcome is submitted for
	// a job that has already reached a terminal state ("completed" or "failed").
	ErrOutcomeAlreadyTerminal = errors.New("replay: job is already in a terminal state")
)

// ReplayResolver records completed ReplayOutcomes and evaluates them against
// the original verification to produce a ReplayVerdict.
type ReplayResolver struct {
	store replayStore
}

// NewReplayResolver returns a ReplayResolver backed by store.
func NewReplayResolver(store replayStore) *ReplayResolver {
	return &ReplayResolver{store: store}
}

// ValidateOutcome checks that outcome is structurally valid and consistent
// with the stored job state. It returns a descriptive error when any of the
// following conditions are met:
//   - outcome.JobID is empty
//   - any CheckComparison has a negative ScoreDelta
//   - the job referenced by outcome.JobID does not exist in the store
//   - the job is already in a terminal state ("completed" or "failed")
func (r *ReplayResolver) ValidateOutcome(outcome *ReplayOutcome) error {
	if outcome.JobID == "" {
		return ErrEmptyJobID
	}

	for i, c := range outcome.Comparisons {
		if c.ScoreDelta < 0 {
			return fmt.Errorf("%w: comparison[%d] check_type=%q delta=%.4f",
				ErrNegativeScoreDelta, i, c.CheckType, c.ScoreDelta)
		}
	}

	jobData, err := r.store.GetReplayJob(outcome.JobID)
	if err != nil {
		return fmt.Errorf("%w: job_id=%s: %v", ErrJobNotFound, outcome.JobID, err)
	}

	var job ReplayJob
	if err := json.Unmarshal(jobData, &job); err != nil {
		return fmt.Errorf("replay: unmarshal job for validation: %w", err)
	}

	if job.Status == "completed" || job.Status == "failed" {
		return fmt.Errorf("%w: job_id=%s status=%s", ErrOutcomeAlreadyTerminal, outcome.JobID, job.Status)
	}

	return nil
}

// RecordOutcome validates, persists the outcome and updates the corresponding
// ReplayJob's status to "completed", or "failed" if outcome.Status is "error".
func (r *ReplayResolver) RecordOutcome(outcome *ReplayOutcome) error {
	if err := r.ValidateOutcome(outcome); err != nil {
		return err
	}

	// Persist the outcome first (per CLAUDE.md: persist before memory update).
	data, err := json.Marshal(outcome)
	if err != nil {
		slog.Error("replay: marshal outcome", "job_id", outcome.JobID, "err", err)
		return err
	}
	if err := r.store.PutReplayOutcome(outcome.JobID, data); err != nil {
		slog.Error("replay: persist outcome", "job_id", outcome.JobID, "err", err)
		return err
	}

	// Fetch and update the corresponding ReplayJob status.
	jobData, err := r.store.GetReplayJob(outcome.JobID)
	if err != nil {
		slog.Error("replay: get job for status update", "job_id", outcome.JobID, "err", err)
		return err
	}
	var job ReplayJob
	if err := json.Unmarshal(jobData, &job); err != nil {
		slog.Error("replay: unmarshal job", "job_id", outcome.JobID, "err", err)
		return err
	}

	if outcome.Status == "error" {
		job.Status = "failed"
	} else {
		job.Status = "completed"
	}

	updatedData, err := json.Marshal(&job)
	if err != nil {
		slog.Error("replay: marshal updated job", "job_id", outcome.JobID, "err", err)
		return err
	}
	if err := r.store.PutReplayJob(job.ID, updatedData); err != nil {
		slog.Error("replay: persist updated job", "job_id", outcome.JobID, "err", err)
		return err
	}
	return nil
}

// EvaluateOutcome translates a ReplayOutcome into a ReplayVerdict that
// describes the recommended action and severity of any detected disagreement.
//
// Evaluation priority:
//  1. outcome.Status == "match" and no anomaly flags → no_action, severity 0.0
//  2. outcome.Status == "partial_match":
//     - MismatchCount == 1 AND max ScoreDelta < 0.15 → flag_for_review, 0.3
//     - MismatchCount > 1 OR any ScoreDelta > 0.3   → open_challenge, 0.6
//     - else (single mismatch with moderate delta)  → flag_for_review, 0.3
//  3. outcome.Status == "mismatch" → open_challenge, severity 0.8
//  4. AnomalyFlags: +0.1 per flag, capped at 1.0
//  5. SeverityScore >= 0.9 → upgrade Action to "slash_recommended"
func (r *ReplayResolver) EvaluateOutcome(outcome *ReplayOutcome) *verification.ReplayVerdict {
	mc := outcome.MismatchCount()
	verdict := &verification.ReplayVerdict{
		MismatchCount: mc,
		AnomalyFlags:  outcome.AnomalyFlags,
	}

	switch outcome.Status {
	case "match":
		verdict.Action = "no_action"
		verdict.Reason = "all checks reproduced successfully"
		verdict.SeverityScore = 0.0

	case "partial_match":
		maxDelta := maxScoreDelta(outcome)
		if mc == 1 && maxDelta < 0.15 {
			verdict.Action = "flag_for_review"
			verdict.Reason = "single minor check mismatch"
			verdict.SeverityScore = 0.3
		} else if mc > 1 || maxDelta > 0.3 {
			verdict.Action = "open_challenge"
			verdict.Reason = "multiple check mismatches or significant score delta"
			verdict.SeverityScore = 0.6
		} else {
			// mc == 1 and 0.15 <= maxDelta <= 0.3: moderate single mismatch.
			verdict.Action = "flag_for_review"
			verdict.Reason = "single check mismatch with moderate delta"
			verdict.SeverityScore = 0.3
		}

	case "mismatch":
		verdict.Action = "open_challenge"
		verdict.Reason = "majority of checks disagree with original"
		verdict.SeverityScore = 0.8

	default:
		// Unknown or "error" status: no verdict action.
		verdict.Action = "no_action"
		verdict.Reason = "no actionable replay result"
		verdict.SeverityScore = 0.0
	}

	// Anomaly flags add severity regardless of the status-based verdict.
	for range outcome.AnomalyFlags {
		verdict.SeverityScore += 0.1
		if verdict.SeverityScore >= 1.0 {
			verdict.SeverityScore = 1.0
			break
		}
	}

	// Critically high severity upgrades the action to slash_recommended.
	if verdict.SeverityScore >= 0.9 {
		verdict.Action = "slash_recommended"
	}

	return verdict
}

// maxScoreDelta returns the largest ScoreDelta across all comparisons in the outcome.
func maxScoreDelta(outcome *ReplayOutcome) float64 {
	var max float64
	for _, c := range outcome.Comparisons {
		if c.ScoreDelta > max {
			max = c.ScoreDelta
		}
	}
	return max
}
