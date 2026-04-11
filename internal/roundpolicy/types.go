// Package roundpolicy provides the generic progress-aware round timing
// evaluator. It decides WHEN to finalize, not WHAT the verdict is.
//
// The evaluator knows about terminal counts, active leases, and timing.
// It knows nothing about tasks, analyzers, diversity floors, or score
// thresholds. Task-specific verdict logic stays in the taskverification
// finalizer.
package roundpolicy

// WaitDecision is the output of the round policy evaluator.
type WaitDecision uint8

const (
	// WaitDecisionFinalizeNow — all terminal or outcome mathematically secured.
	WaitDecisionFinalizeNow WaitDecision = iota

	// WaitDecisionWaitForActive — validators have active non-stale leases.
	// Check again when leases expire or new votes arrive.
	WaitDecisionWaitForActive

	// WaitDecisionWaitForBackstop — no active progress, durable votes
	// insufficient for finalization. Backstop is the last hope.
	WaitDecisionWaitForBackstop

	// WaitDecisionExpired — backstop reached without sufficient votes.
	// Round should be finalized as Expired → dispute resolution.
	WaitDecisionExpired
)

// String returns a human-readable name.
func (d WaitDecision) String() string {
	switch d {
	case WaitDecisionFinalizeNow:
		return "FinalizeNow"
	case WaitDecisionWaitForActive:
		return "WaitForActive"
	case WaitDecisionWaitForBackstop:
		return "WaitForBackstop"
	case WaitDecisionExpired:
		return "Expired"
	default:
		return "Unknown"
	}
}

// RoundState is the input to the evaluator. The caller (deadline checker)
// builds this from the round's durable votes and progress snapshots.
type RoundState struct {
	// TotalValidators is the number of validators in the active set
	// (or committee) for this round.
	TotalValidators int

	// VotedCount is the number of validators that have emitted a durable
	// TaskVerificationVote on the DAG.
	VotedCount int

	// TerminalCount is VotedCount + validators with signed terminal progress
	// (Abstained or Failed in their progress snapshot). These validators are
	// "done" — the round should not wait for them. Signed-abstain does NOT
	// add vote weight; it only means "stop waiting."
	TerminalCount int

	// ActiveLeaseCount is the number of non-terminal validators with a
	// valid (non-stale) progress lease. These validators are actively
	// working and the round should wait for them (bounded by lease expiry).
	ActiveLeaseCount int

	// StaleCount is the number of non-terminal validators with stale or
	// absent progress. Their weight is uncounted — they are neither terminal
	// nor active. The round may finalize without them IF durable votes
	// satisfy BFT supermajority of the full set.
	StaleCount int

	// MaxLeaseExpiryUnix is the latest LeaseExpiryUnix among active leases.
	// Only meaningful when ActiveLeaseCount > 0.
	MaxLeaseExpiryUnix int64

	// OutcomeSecured is true when the current durable votes satisfy the
	// finalization criteria (BFT threshold + diversity floor + median score)
	// such that remaining validators cannot change the outcome. This is
	// computed by the caller using task-specific verdict logic.
	OutcomeSecured bool

	// BackstopUnix is the absolute backstop time (round open + 5 minutes).
	// After this point, the round expires regardless of progress.
	BackstopUnix int64
}
