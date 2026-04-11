package taskverification

import "sort"

// FinalizationReason describes why a round was finalized.
type FinalizationReason string

const (
	ReasonAcceptSupermajority    FinalizationReason = "accept_supermajority"
	ReasonRejectSupermajority    FinalizationReason = "reject_supermajority"
	ReasonDeadlineExpiredDispute FinalizationReason = "deadline_expired_dispute"
)

// FinalizationDecision is the output of the Finalizer's Evaluate function.
type FinalizationDecision struct {
	ShouldFinalize bool
	Verdict        Verdict
	FinalScoreBP   uint64
	Reason         FinalizationReason
}

// FinalizerConfig holds the thresholds for finalization.
type FinalizerConfig struct {
	// BFTThresholdBP is the supermajority threshold in basis points.
	// Default 6667 = 66.67%.
	BFTThresholdBP uint64

	// AcceptanceScoreThreshold is preserved for observability but is
	// NO LONGER USED as an acceptance gate. Cross-family median scores
	// are a category error — see docs/lessons.md.
	AcceptanceScoreThreshold uint64

	// DiversityFloor is the minimum number of distinct analyzer families
	// that must contribute pass-weight for acceptance.
	DiversityFloor int

	// ParticipationFloor is the minimum number of distinct analyzer families
	// that must contribute any vote (pass/fail/abstain) for acceptance.
	// Per Grok adversarial review (April 2026).
	ParticipationFloor int

	// EnforceFailDiversity when true requires fail-side diversity for
	// rejection. Default false.
	EnforceFailDiversity bool
}

// DefaultFinalizerConfig returns production defaults.
func DefaultFinalizerConfig() FinalizerConfig {
	return FinalizerConfig{
		BFTThresholdBP:           6667,
		AcceptanceScoreThreshold: 6000, // observability only, not an acceptance gate
		DiversityFloor:           2,
		ParticipationFloor:       DefaultParticipationFloor,
		EnforceFailDiversity:     false,
	}
}

// Finalizer evaluates whether a round can finalize based on BFT supermajority,
// analyzer-family diversity, and score thresholds. It is a pure function —
// it does NOT mutate the round.
type Finalizer struct {
	config FinalizerConfig
}

// NewFinalizer creates a Finalizer with the given configuration.
func NewFinalizer(config FinalizerConfig) *Finalizer {
	return &Finalizer{config: config}
}

// Evaluate examines the round's aggregation state and returns a finalization
// decision. Does NOT mutate the round. totalActiveWeight is the total stake
// of all active validators from the validator-set snapshot.
func (f *Finalizer) Evaluate(
	round *TaskVerificationRound,
	totalActiveWeight uint64,
	now int64,
) FinalizationDecision {
	if round.State != RoundStateOpen {
		return FinalizationDecision{ShouldFinalize: false}
	}

	// BFT threshold: ceil(totalActiveWeight * BFTThresholdBP / 10000)
	threshold := ceilMulDiv(totalActiveWeight, f.config.BFTThresholdBP, 10000)

	// 1. Check accept: supermajority pass + diversity floor + participation floor.
	// Median score is NOT an acceptance gate (category error — see docs/lessons.md).
	// Score values are preserved in the consensus payload as observability metadata.
	if round.PassWeight >= threshold {
		participationFloor := f.config.ParticipationFloor
		if participationFloor == 0 {
			participationFloor = DefaultParticipationFloor
		}
		if round.DistinctPassFamilies() >= f.config.DiversityFloor &&
			round.DistinctParticipatingFamilies() >= participationFloor {
			median := MedianScore(round.Votes, VerdictPass) // observability only
			return FinalizationDecision{
				ShouldFinalize: true,
				Verdict:        VerdictPass,
				FinalScoreBP:   median,
				Reason:         ReasonAcceptSupermajority,
			}
		}
		// Supermajority pass but diversity or participation insufficient —
		// don't finalize yet, wait for more votes or deadline.
	}

	// 2. Check reject: supermajority fail.
	if round.FailWeight >= threshold {
		return FinalizationDecision{
			ShouldFinalize: true,
			Verdict:        VerdictFail,
			FinalScoreBP:   0,
			Reason:         ReasonRejectSupermajority,
		}
	}

	// 3. Check deadline expiry.
	deadline := round.DeadlineForCurrentPhase()
	if now > deadline {
		// If already extended, this is the second deadline → dispute.
		if round.ExtendedUntilUnix > 0 {
			return FinalizationDecision{
				ShouldFinalize: true,
				Verdict:        VerdictAbstain,
				FinalScoreBP:   0,
				Reason:         ReasonDeadlineExpiredDispute,
			}
		}
		// First deadline expired. Check if convergence is plausible
		// (one verdict has ≥50% weight). If so, the deadline checker
		// extends — but that's the checker's job, not the finalizer's.
		// If no convergence, finalize as dispute.
		halfWeight := totalActiveWeight / 2
		if round.PassWeight >= halfWeight || round.FailWeight >= halfWeight {
			// Convergence plausible — don't finalize, let the checker extend.
			return FinalizationDecision{ShouldFinalize: false}
		}
		// No convergence — dispute.
		return FinalizationDecision{
			ShouldFinalize: true,
			Verdict:        VerdictAbstain,
			FinalScoreBP:   0,
			Reason:         ReasonDeadlineExpiredDispute,
		}
	}

	return FinalizationDecision{ShouldFinalize: false}
}

// MedianScore computes the median ScoreBP of votes matching filterVerdict.
// Returns 0 if no votes match. For even count, returns the average of the
// two middle values (rounded down).
func MedianScore(votes []TaskVerificationVoteRecord, filterVerdict Verdict) uint64 {
	var scores []uint64
	for _, v := range votes {
		if v.Verdict == filterVerdict {
			scores = append(scores, v.ScoreBP)
		}
	}
	if len(scores) == 0 {
		return 0
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i] < scores[j] })
	n := len(scores)
	if n%2 == 1 {
		return scores[n/2]
	}
	return (scores[n/2-1] + scores[n/2]) / 2
}

// ceilMulDiv computes ceil(a * b / c) without overflow for reasonable values.
func ceilMulDiv(a, b, c uint64) uint64 {
	if c == 0 {
		return 0
	}
	product := a * b
	return (product + c - 1) / c
}
