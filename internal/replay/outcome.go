package replay

import "time"

// CheckComparison records the comparison result for a single check between
// the original evidence and the replayed execution.
type CheckComparison struct {
	// CheckType is the logical check name (e.g. "go_test", "lint").
	CheckType string

	// OriginalHash is the content-addressed hash of the original artifact.
	OriginalHash string

	// ReplayedHash is the content-addressed hash of the replayed artifact.
	ReplayedHash string

	// Match is true when OriginalHash == ReplayedHash.
	Match bool

	// ScoreDelta is the absolute difference between the original and replayed
	// numeric scores for this check (0.0 if not computed or not applicable).
	ScoreDelta float64

	// Detail provides human-readable context for a mismatch.
	Detail string
}

// ReplayOutcome is the result of executing a ReplayJob.
type ReplayOutcome struct {
	// JobID is the ID of the ReplayJob this outcome belongs to.
	JobID string

	// TaskID is copied from the ReplayJob for convenience.
	TaskID string

	// Status summarises the overall replay result:
	//   "match"         — all checks reproduced exactly
	//   "partial_match" — some checks matched, some did not
	//   "mismatch"      — the majority of checks disagree with the original
	//   "error"         — the replay could not be executed
	Status string

	// Comparisons holds one entry per check that was replayed.
	Comparisons []CheckComparison

	// AnomalyFlags lists any anomaly indicators detected during replay
	// (e.g. "timing_anomaly", "environment_drift").
	AnomalyFlags []string

	// ReplayedAt is when the replay completed.
	ReplayedAt time.Time

	// ReplayerID is the identifier of the agent or system that performed the replay.
	ReplayerID string

	// Notes is an optional human-readable summary of the replay run.
	Notes string
}

// HasMismatch returns true if any CheckComparison has Match == false.
func (o *ReplayOutcome) HasMismatch() bool {
	for _, c := range o.Comparisons {
		if !c.Match {
			return true
		}
	}
	return false
}

// MismatchCount returns the number of CheckComparisons where Match is false.
func (o *ReplayOutcome) MismatchCount() int {
	count := 0
	for _, c := range o.Comparisons {
		if !c.Match {
			count++
		}
	}
	return count
}
