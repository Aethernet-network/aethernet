package taskverification

import (
	"context"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// SlashingType distinguishes reputation-only vs stake-affecting slashing.
type SlashingType int

const (
	SlashingTypeSoft SlashingType = iota // reputation penalty only
	SlashingTypeHard                     // stake slash after challenge window
)

func (s SlashingType) String() string {
	switch s {
	case SlashingTypeSoft:
		return "soft"
	case SlashingTypeHard:
		return "hard"
	default:
		return "unknown"
	}
}

// SlashingAction describes a slashing decision to be acted on.
type SlashingAction struct {
	ValidatorID       crypto.AgentID
	Type              SlashingType
	Reason            string
	StakePenaltyBP    uint64 // basis points of stake (hard slash only)
	ReputationPenalty int    // points (soft slash only)
}

// SlashingPolicy configures slashing thresholds.
type SlashingPolicy struct {
	SoftSlashingEnabled               bool
	HardSlashingEnabled               bool
	EquivocationAutoSlash             bool   // equivocation → immediate hard slash
	SystematicDivergenceThreshold     float64 // agreement rate below this → hard slash
	SystematicDivergenceMinVotes      int     // minimum votes before systematic divergence check
	EquivocationStakePenaltyBP        uint64  // stake basis points for equivocation
	SystematicDivergenceStakePenaltyBP uint64 // stake basis points for systematic divergence
	ChallengeWindowSeconds            int64
}

// DefaultSlashingPolicy returns conservative testnet defaults.
func DefaultSlashingPolicy() SlashingPolicy {
	return SlashingPolicy{
		SoftSlashingEnabled:                true,
		HardSlashingEnabled:                true,
		EquivocationAutoSlash:              true,
		SystematicDivergenceThreshold:      0.30,
		SystematicDivergenceMinVotes:       50,
		EquivocationStakePenaltyBP:         3000, // 30%
		SystematicDivergenceStakePenaltyBP: 1000, // 10%
		ChallengeWindowSeconds:             60,
	}
}

// SlashingEvaluator examines finalized rounds and produces slashing actions.
type SlashingEvaluator struct {
	policy      SlashingPolicy
	reputation  *ValidatorReputationStore
	calibration *CalibrationStore
}

// NewSlashingEvaluator creates an evaluator with the given policy.
func NewSlashingEvaluator(
	policy SlashingPolicy,
	reputation *ValidatorReputationStore,
	calibration *CalibrationStore,
) *SlashingEvaluator {
	return &SlashingEvaluator{
		policy:      policy,
		reputation:  reputation,
		calibration: calibration,
	}
}

// EvaluateRound examines a finalized round and returns slashing actions.
// Does NOT apply the slashes — returns evaluations for the caller to act on.
func (e *SlashingEvaluator) EvaluateRound(
	ctx context.Context,
	round *TaskVerificationRound,
) []SlashingAction {
	if !round.IsTerminal() {
		return nil
	}

	var actions []SlashingAction

	for _, vote := range round.Votes {
		family := verification.FamilyID(vote.AnalyzerFamily)

		// Skip if this (category, family) is still in calibration.
		if e.calibration != nil {
			calibrated, err := e.calibration.IsCalibrated(ctx, round.Category, family)
			if err != nil || !calibrated {
				continue
			}
		}

		// Determine if this vote agreed with consensus.
		agreed := vote.Verdict == round.FinalVerdict

		// Soft slashing: reputation penalty for deviation.
		if e.policy.SoftSlashingEnabled && !agreed && vote.Verdict != VerdictAbstain {
			actions = append(actions, SlashingAction{
				ValidatorID:       vote.ValidatorID,
				Type:              SlashingTypeSoft,
				Reason:            "deviation from consensus",
				ReputationPenalty: 1,
			})
		}

		// Hard slashing: systematic divergence check.
		if e.policy.HardSlashingEnabled && e.reputation != nil && !agreed {
			rep, err := e.reputation.Get(ctx, vote.ValidatorID, family, round.Category)
			if err == nil && rep.TotalVotes >= uint64(e.policy.SystematicDivergenceMinVotes) {
				rate := rep.AgreementRate()
				if rate < e.policy.SystematicDivergenceThreshold {
					actions = append(actions, SlashingAction{
						ValidatorID:    vote.ValidatorID,
						Type:           SlashingTypeHard,
						Reason:         "systematic divergence: agreement rate below threshold",
						StakePenaltyBP: e.policy.SystematicDivergenceStakePenaltyBP,
					})
					slog.Warn("slashing: systematic divergence detected",
						"validator_id", vote.ValidatorID,
						"family", vote.AnalyzerFamily,
						"category", round.Category,
						"agreement_rate", rate,
						"threshold", e.policy.SystematicDivergenceThreshold,
						"total_votes", rep.TotalVotes,
					)
				}
			}
		}
	}

	// Hard slashing: equivocation (detected during vote aggregation, flagged
	// in the round's vote records by the aggregator).
	if e.policy.HardSlashingEnabled && e.policy.EquivocationAutoSlash {
		equivocators := detectEquivocators(round)
		for _, vid := range equivocators {
			actions = append(actions, SlashingAction{
				ValidatorID:    vid,
				Type:           SlashingTypeHard,
				Reason:         "equivocation: conflicting votes for same family",
				StakePenaltyBP: e.policy.EquivocationStakePenaltyBP,
			})
		}
	}

	return actions
}

// detectEquivocators scans a round's votes for validators who appear
// multiple times with the same family but different verdicts/scores.
// (The aggregator prevents this at ingestion time, but this is a
// defense-in-depth audit check for the slashing evaluator.)
func detectEquivocators(round *TaskVerificationRound) []crypto.AgentID {
	type key struct {
		validator crypto.AgentID
		family    string
	}
	type seen struct {
		verdict Verdict
		score   uint64
	}
	first := make(map[key]seen)
	var equivocators []crypto.AgentID
	equivSet := make(map[crypto.AgentID]struct{})

	for _, v := range round.Votes {
		k := key{validator: v.ValidatorID, family: v.AnalyzerFamily}
		if prev, ok := first[k]; ok {
			if prev.verdict != v.Verdict || prev.score != v.ScoreBP {
				if _, already := equivSet[v.ValidatorID]; !already {
					equivocators = append(equivocators, v.ValidatorID)
					equivSet[v.ValidatorID] = struct{}{}
				}
			}
		} else {
			first[k] = seen{verdict: v.Verdict, score: v.ScoreBP}
		}
	}
	return equivocators
}
