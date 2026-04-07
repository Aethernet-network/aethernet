package recognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// ValidatorWeightLookup resolves a validator's stake weight for voting.
// Returns (weight, eligible). Eligible is false if the validator is not
// in the current seat snapshot.
type ValidatorWeightLookup interface {
	VoteWeight(agentID crypto.AgentID) (weight uint64, eligible bool)
}

// ValidatorWeightFunc adapts a function to the ValidatorWeightLookup interface.
type ValidatorWeightFunc func(crypto.AgentID) (uint64, bool)

func (f ValidatorWeightFunc) VoteWeight(id crypto.AgentID) (uint64, bool) { return f(id) }

// TaskVerificationVoteConsumer is a CommitConsumer that ingests
// TaskVerificationVote DAG events and applies them to open
// TaskVerificationRound records via the idempotent ApplyVoteToRound
// aggregator.
//
// If the round for the vote has not yet been created (because the round
// consumer hasn't processed the TaskSubmitted event yet), the vote is
// deferred with a prerequisite key. The round consumer signals this key
// after creating the round.
type TaskVerificationVoteConsumer struct {
	rounds    taskverification.Store
	weightFn  ValidatorWeightLookup
}

// NewTaskVerificationVoteConsumer creates a consumer that applies votes to
// verification rounds.
func NewTaskVerificationVoteConsumer(
	rounds taskverification.Store,
	weightFn ValidatorWeightLookup,
) *TaskVerificationVoteConsumer {
	return &TaskVerificationVoteConsumer{
		rounds:   rounds,
		weightFn: weightFn,
	}
}

// Name returns the unique consumer identifier.
func (c *TaskVerificationVoteConsumer) Name() string { return "task_verification_vote" }

// Interested returns true for TaskVerificationVote events.
func (c *TaskVerificationVoteConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskVerificationVote
}

// Ready checks that the target round exists. If not, defers with a
// prerequisite key that the round consumer signals after round creation.
func (c *TaskVerificationVoteConsumer) Ready(_ context.Context, ev *event.Event, _ ReadModel) (bool, string, error) {
	var payload struct {
		RoundID string `json:"round_id"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil || payload.RoundID == "" {
		return true, "", nil // malformed — let Consume handle the error
	}

	_, err := c.rounds.LoadRound(context.Background(), taskverification.RoundID(payload.RoundID))
	if err != nil {
		prereq := PrerequisiteKeyTVRound(payload.RoundID)
		return false, prereq, nil
	}
	return true, "", nil
}

// Consume applies a TaskVerificationVote to the corresponding round.
// Idempotent: duplicate identical votes are no-ops.
func (c *TaskVerificationVoteConsumer) Consume(ctx context.Context, ev *event.Event) error {
	var payload event.TaskVerificationVotePayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("task_verification_vote: unmarshal: %w", err)
	}

	roundID := taskverification.RoundID(payload.RoundID)
	round, err := c.rounds.LoadRound(ctx, roundID)
	if err != nil {
		if errors.Is(err, taskverification.ErrRoundNotFound) {
			slog.Warn("task_verification_vote: round not found (unexpected after Ready check)",
				"round_id", payload.RoundID, "event_id", ev.ID)
			return nil
		}
		return fmt.Errorf("task_verification_vote: load round: %w", err)
	}

	// Look up validator stake.
	validatorID := crypto.AgentID(payload.ValidatorID)
	stake, eligible := c.weightFn.VoteWeight(validatorID)
	if !eligible {
		slog.Info("task_verification_vote: ineligible validator, vote dropped",
			"round_id", payload.RoundID,
			"validator_id", payload.ValidatorID,
			"event_id", ev.ID)
		return nil
	}

	verdict, err := parseVerdict(payload.Verdict)
	if err != nil {
		slog.Warn("task_verification_vote: invalid verdict",
			"round_id", payload.RoundID, "verdict", payload.Verdict, "event_id", ev.ID)
		return nil
	}

	record := taskverification.TaskVerificationVoteRecord{
		ValidatorID:          validatorID,
		Verdict:              verdict,
		ScoreBP:              payload.ScoreBP,
		ScoreBreakdown:       payload.ScoreBreakdown,
		AnalyzerFamily:       payload.AnalyzerFamily,
		AnalyzerVersion:      payload.AnalyzerVersion,
		PolicyVersion:        payload.PolicyVersion,
		AnalysisArtifactHash: payload.AnalysisArtifactHash,
		Stake:                stake,
		TimestampUnix:        payload.TimestampUnix,
	}

	// Post-finalization: record for audit only.
	if round.IsTerminal() {
		taskverification.RecordPostFinalizationVote(round, record)
		if saveErr := c.rounds.SaveRound(ctx, round); saveErr != nil {
			return fmt.Errorf("task_verification_vote: save post-finalization: %w", saveErr)
		}
		slog.Info("task_verification_vote: post-finalization vote recorded (audit only)",
			"round_id", payload.RoundID,
			"validator_id", payload.ValidatorID,
			"event_id", ev.ID)
		return nil
	}

	result, err := taskverification.ApplyVoteToRound(round, record)
	if err != nil {
		if errors.Is(err, taskverification.ErrEquivocation) {
			slog.Warn("task_verification_vote: EQUIVOCATION DETECTED",
				"round_id", payload.RoundID,
				"validator_id", payload.ValidatorID,
				"event_id", ev.ID,
				"err", err)
			// Record for audit despite equivocation.
			return nil
		}
		return fmt.Errorf("task_verification_vote: apply: %w", err)
	}

	if result.DuplicateVote {
		return nil // idempotent no-op
	}

	if err := c.rounds.SaveRound(ctx, round); err != nil {
		return fmt.Errorf("task_verification_vote: save: %w", err)
	}

	slog.Info("task_verification_vote: vote applied",
		"round_id", payload.RoundID,
		"validator_id", payload.ValidatorID,
		"verdict", payload.Verdict,
		"score_bp", payload.ScoreBP,
		"analyzer_family", payload.AnalyzerFamily,
		"pass_weight", round.PassWeight,
		"fail_weight", round.FailWeight,
		"distinct_families", round.DistinctPassFamilies(),
		"event_id", ev.ID,
	)
	return nil
}

// parseVerdict converts a string verdict to the taskverification.Verdict enum.
func parseVerdict(s string) (taskverification.Verdict, error) {
	switch s {
	case "pass":
		return taskverification.VerdictPass, nil
	case "fail":
		return taskverification.VerdictFail, nil
	case "abstain":
		return taskverification.VerdictAbstain, nil
	default:
		return 0, fmt.Errorf("unknown verdict: %q", s)
	}
}

// Compile-time assertion.
var _ CommitConsumer = (*TaskVerificationVoteConsumer)(nil)
