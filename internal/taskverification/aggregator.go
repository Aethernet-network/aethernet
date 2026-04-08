package taskverification

import (
	"errors"
	"fmt"
)

// Sentinel errors for vote aggregation.
var (
	// ErrRoundNotOpen is returned when a vote is applied to a round that
	// has already finalized, expired, or been disputed.
	ErrRoundNotOpen = errors.New("taskverification: round is not open")

	// ErrEquivocation is returned when a validator emits two votes for the
	// same round with conflicting verdict or score. This is a slashable
	// offense (slashing logic is in prompt 09).
	ErrEquivocation = errors.New("taskverification: equivocation detected")
)

// AggregationResult captures the outcome of applying a vote to a round.
type AggregationResult struct {
	Applied              bool
	DuplicateVote        bool
	EquivocationDetected bool
	NewPassWeight        uint64
	NewFailWeight        uint64
	NewAbstainWeight     uint64
}

// ApplyVoteToRound aggregates a vote into a round's state. The function is
// idempotent: identical duplicate votes are no-ops.
//
// Returns:
//   - Applied=true if the vote was newly recorded
//   - DuplicateVote=true if the vote was already recorded with identical verdict+score
//   - EquivocationDetected=true + ErrEquivocation if the validator previously voted
//     with a different verdict or score (slashable offense)
//   - ErrRoundNotOpen if the round is not in Open state
func ApplyVoteToRound(
	round *TaskVerificationRound,
	vote TaskVerificationVoteRecord,
) (AggregationResult, error) {
	if round.State != RoundStateOpen {
		return AggregationResult{}, ErrRoundNotOpen
	}

	// Check for duplicate or equivocation. Keyed on (ValidatorID, AnalyzerFamily)
	// because a validator running multiple families correctly emits one vote per
	// family. Equivocation is same validator + same family with different verdict
	// or score.
	for _, existing := range round.Votes {
		if existing.ValidatorID == vote.ValidatorID && existing.AnalyzerFamily == vote.AnalyzerFamily {
			if existing.Verdict == vote.Verdict && existing.ScoreBP == vote.ScoreBP {
				return AggregationResult{DuplicateVote: true}, nil
			}
			return AggregationResult{EquivocationDetected: true},
				fmt.Errorf("%w: validator %s family %s voted %s/%d then %s/%d for round %s",
					ErrEquivocation,
					vote.ValidatorID, vote.AnalyzerFamily,
					existing.Verdict, existing.ScoreBP,
					vote.Verdict, vote.ScoreBP,
					round.RoundID,
				)
		}
	}

	// Record the vote.
	round.Votes = append(round.Votes, vote)

	// Update weight counters.
	switch vote.Verdict {
	case VerdictPass:
		round.PassWeight += vote.Stake
		// Track analyzer family participation (pass-weight only).
		if round.ParticipatingFamilies == nil {
			round.ParticipatingFamilies = make(map[string]uint64)
		}
		if vote.AnalyzerFamily != "" {
			round.ParticipatingFamilies[vote.AnalyzerFamily] += vote.Stake
		}
	case VerdictFail:
		round.FailWeight += vote.Stake
	case VerdictAbstain:
		round.AbstainWeight += vote.Stake
	}

	return AggregationResult{
		Applied:          true,
		NewPassWeight:    round.PassWeight,
		NewFailWeight:    round.FailWeight,
		NewAbstainWeight: round.AbstainWeight,
	}, nil
}

// RecordPostFinalizationVote appends a vote to a finalized round for audit
// purposes without modifying aggregation state. Used when votes arrive after
// the round has already finalized (e.g., due to network partition healing).
func RecordPostFinalizationVote(
	round *TaskVerificationRound,
	vote TaskVerificationVoteRecord,
) {
	round.Votes = append(round.Votes, vote)
}
