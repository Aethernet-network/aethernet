package recognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/settlement"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// TaskVerificationConsensusConsumer processes TaskVerificationConsensus
// events from the DAG: applies round state for replay safety AND invokes
// the v4.1 economic settlement (escrow release/refund/split).
type TaskVerificationConsensusConsumer struct {
	rounds  taskverification.Store
	settler *settlement.VerificationConsensusSettler // nil if settlement not wired
}

// NewTaskVerificationConsensusConsumer creates a consensus consumer.
// settler may be nil (round state is applied but no settlement occurs).
func NewTaskVerificationConsensusConsumer(
	rounds taskverification.Store,
	settler *settlement.VerificationConsensusSettler,
) *TaskVerificationConsensusConsumer {
	return &TaskVerificationConsensusConsumer{rounds: rounds, settler: settler}
}

// Name returns the unique consumer identifier.
func (c *TaskVerificationConsensusConsumer) Name() string { return "task_verification_consensus" }

// Interested returns true for TaskVerificationConsensus events.
func (c *TaskVerificationConsensusConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskVerificationConsensus
}

// Ready always returns true — consensus events are always ready.
func (c *TaskVerificationConsensusConsumer) Ready(_ context.Context, _ *event.Event, _ ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume applies the consensus event to the corresponding round. Idempotent:
// if the round is already finalized in the same way, this is a no-op.
func (c *TaskVerificationConsensusConsumer) Consume(_ context.Context, ev *event.Event) error {
	var payload event.TaskVerificationConsensusPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("task_verification_consensus: unmarshal: %w", err)
	}

	roundID := taskverification.RoundID(payload.RoundID)
	round, err := c.rounds.LoadRound(context.Background(), roundID)
	if err != nil {
		if errors.Is(err, taskverification.ErrRoundNotFound) {
			// Round not found — might arrive during replay before the round
			// consumer creates the round. Log and return nil.
			slog.Debug("task_verification_consensus: round not found (may arrive later)",
				"round_id", payload.RoundID, "event_id", ev.ID)
			return nil
		}
		return fmt.Errorf("task_verification_consensus: load round: %w", err)
	}

	// Already finalized — idempotent.
	if round.IsTerminal() {
		return nil
	}

	// Apply finalization from the consensus event.
	verdict := parseConsensusVerdict(payload.FinalVerdict)
	targetState := consensusVerdictToState(verdict)
	if err := round.Transition(targetState, payload.FinalizationTimeUnix); err != nil {
		return nil // transition not valid — round already in terminal state
	}
	round.FinalVerdict = verdict
	round.FinalScoreBP = payload.FinalScoreBP

	if err := c.rounds.SaveRound(context.Background(), round); err != nil {
		return fmt.Errorf("task_verification_consensus: save: %w", err)
	}

	slog.Info("task_verification_consensus: round finalized from DAG event",
		"round_id", payload.RoundID,
		"task_id", payload.TaskID,
		"verdict", payload.FinalVerdict,
		"score_bp", payload.FinalScoreBP,
		"event_id", ev.ID,
	)

	// Apply v4.1 economic settlement: escrow release/refund/split.
	if c.settler != nil {
		settleResult, err := c.settler.Settle(context.Background(), &payload, round)
		if err != nil {
			slog.Warn("task_verification_consensus: settlement failed",
				"task_id", payload.TaskID, "err", err)
			// Don't fail the consumer — the round state is persisted,
			// settlement can be retried on the next consensus event replay.
		} else if settleResult.Applied {
			slog.Info("task_verification_consensus: settlement applied",
				"task_id", payload.TaskID,
				"verdict", payload.FinalVerdict,
				"worker_payout", settleResult.WorkerPayout,
				"poster_refund", settleResult.PosterRefund,
				"treasury", settleResult.TreasuryAmount,
				"total_distributed", settleResult.TotalDistributed,
			)
		}
	}

	return nil
}

func parseConsensusVerdict(s string) taskverification.Verdict {
	switch s {
	case "pass":
		return taskverification.VerdictPass
	case "fail":
		return taskverification.VerdictFail
	default:
		return taskverification.VerdictAbstain
	}
}

func consensusVerdictToState(v taskverification.Verdict) taskverification.RoundState {
	switch v {
	case taskverification.VerdictPass:
		return taskverification.RoundStateFinalizedAccept
	case taskverification.VerdictFail:
		return taskverification.RoundStateFinalizedReject
	default:
		return taskverification.RoundStateDisputed
	}
}

// Compile-time assertion.
var _ CommitConsumer = (*TaskVerificationConsensusConsumer)(nil)
