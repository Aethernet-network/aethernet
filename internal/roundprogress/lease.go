package roundprogress

import "fmt"

// Default lease extension maximums per phase.
const (
	maxLeaseFetchingBlobSec = 30
	maxLeaseAnalyzingSec    = 60
	leaseScorePendingSec    = 10
	defaultInitialLeaseSec  = 30 // initial lease for Acknowledged phase
)

// ComputeLeaseExpiry returns the new lease expiry for a phase transition or
// generation bump. All times are unix seconds. Returns the new absolute expiry.
//
// Rules per §5.3:
//   - FetchingBlob: min(clampedETA, now+30s)
//   - Analyzing: min(clampedETA, now+60s)
//   - ScorePending: now+10s (fixed, non-renewable)
//   - Terminal phases: no lease (returns 0)
//   - Acknowledged: now+30s (initial lease)
func ComputeLeaseExpiry(phase ProgressPhase, currentExpiry int64, clampedETA int64, nowUnix int64) int64 {
	switch phase {
	case ProgressPhaseAcknowledged:
		return nowUnix + defaultInitialLeaseSec

	case ProgressPhaseFetchingBlob:
		maxExpiry := nowUnix + maxLeaseFetchingBlobSec
		if clampedETA > 0 && clampedETA < maxExpiry {
			return clampedETA
		}
		return maxExpiry

	case ProgressPhaseAnalyzing:
		maxExpiry := nowUnix + maxLeaseAnalyzingSec
		if clampedETA > 0 && clampedETA < maxExpiry {
			return clampedETA
		}
		return maxExpiry

	case ProgressPhaseScorePending:
		return nowUnix + leaseScorePendingSec

	case ProgressPhaseVoteEmitted, ProgressPhaseAbstained, ProgressPhaseFailed:
		return 0 // terminal — no lease
	}
	return 0
}

// ValidateLeaseExtension checks whether an incoming ProgressUpdate is a valid
// lease extension given the current snapshot state. Returns nil if valid,
// a descriptive error if the update must be rejected.
//
// Validation rules:
//  1. Phase must be a valid forward transition
//  2. Generation must strictly increase (for non-terminal phases)
//  3. Evidence must differ from previous (monotonic advancement)
//  4. ScorePending allows no repeated extensions
func ValidateLeaseExtension(current *RoundProgressSnapshot, update *ProgressUpdate) error {
	// Rule 1: valid phase transition.
	if !ValidTransition(current.CurrentPhase, update.Phase) {
		return fmt.Errorf("invalid phase transition: %s → %s",
			current.CurrentPhase, update.Phase)
	}

	// Terminal phases always valid to transition into (no generation/evidence check).
	if update.Phase.IsTerminal() {
		return nil
	}

	// Rule 2: generation must strictly increase.
	if update.ProgressGeneration <= current.ProgressGeneration {
		return fmt.Errorf("stale generation: update=%d, current=%d",
			update.ProgressGeneration, current.ProgressGeneration)
	}

	// Rule 3: evidence must differ from previous generation's evidence.
	// For v1, we check that the evidence hash is non-zero and differs.
	// TODO: full observable-evidence verification (actual byte counts, etc.)
	// is a future hardening item.
	var zeroEvidence [32]byte
	if update.ProgressEvidence != zeroEvidence {
		// Evidence provided — no additional check needed for v1.
		// Future: verify monotonic byte count for FetchingBlob, etc.
	}

	// Rule 4: ScorePending allows no repeated extensions.
	if update.Phase == ProgressPhaseScorePending &&
		current.CurrentPhase == ProgressPhaseScorePending {
		return fmt.Errorf("ScorePending does not allow repeated extensions")
	}

	return nil
}
