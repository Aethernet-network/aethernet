package replay

import "time"

// ReputationSignal carries the reputation impact of a completed replay.
// It is produced by OutcomeToReputationSignal and is ready to be consumed by
// the reputation manager when live replay integration is wired in.
type ReputationSignal struct {
	// AgentID is the agent whose reputation is affected.
	AgentID string

	// TaskID is the marketplace task the replay belongs to.
	TaskID string

	// SignalType classifies the signal:
	//   "replay_match"     — replay confirmed original; positive signal
	//   "replay_mismatch"  — replay disagreed with original; negative signal
	//   "replay_anomaly"   — anomaly indicators detected; negative signal
	SignalType string

	// SeverityScore is in [0.0, 1.0]. 0.0 for a clean match; higher values
	// indicate stronger negative signals.
	SeverityScore float64

	// Category is the task category (e.g. "code", "research").
	Category string

	// Timestamp is when the signal was produced.
	Timestamp time.Time
}

// OutcomeToReputationSignal maps a ReplayOutcome to a ReputationSignal for
// the given agent and task category.
//
// Signal priority:
//  1. anomaly flags present → "replay_anomaly"   (severity: 0.7 + 0.1 per flag, capped 1.0)
//  2. mismatches present    → "replay_mismatch"  (severity: 0.3 per mismatch, capped 1.0)
//  3. no mismatches or flags → "replay_match"    (severity: 0.0, positive signal)
func OutcomeToReputationSignal(outcome *ReplayOutcome, agentID string, category string) *ReputationSignal {
	sig := &ReputationSignal{
		AgentID:   agentID,
		TaskID:    outcome.TaskID,
		Category:  category,
		Timestamp: time.Now(),
	}

	mc := outcome.MismatchCount()

	switch {
	case len(outcome.AnomalyFlags) > 0:
		sig.SignalType = "replay_anomaly"
		sig.SeverityScore = 0.7 + float64(len(outcome.AnomalyFlags))*0.1
		if sig.SeverityScore > 1.0 {
			sig.SeverityScore = 1.0
		}

	case mc > 0:
		sig.SignalType = "replay_mismatch"
		sig.SeverityScore = float64(mc) * 0.3
		if sig.SeverityScore > 1.0 {
			sig.SeverityScore = 1.0
		}

	default:
		sig.SignalType = "replay_match"
		sig.SeverityScore = 0.0
	}

	return sig
}
