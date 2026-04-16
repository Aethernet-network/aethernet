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
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// TaskVerificationConsensusConsumer processes TaskVerificationConsensus
// events from the DAG: applies round state for replay safety AND invokes
// the v4.1 economic settlement (escrow release/refund/split) AND advances
// per-(category, family) calibration counters.
// DispatcherAdmitter is the subset of dispatch.Dispatcher used by the
// consensus consumer to route settlement through the dispatcher's
// exactly-once admission. Avoids importing the dispatch package directly.
type DispatcherAdmitter interface {
	Admit(ctx context.Context, ev *event.Event) error
}

type TaskVerificationConsensusConsumer struct {
	rounds      taskverification.Store
	settler     *settlement.VerificationConsensusSettler // nil if settlement not wired
	dispatcher  DispatcherAdmitter                      // nil → direct settler call (pre-commit-9 compat)
	slashing    *taskverification.SlashingEvaluator      // nil if slashing not wired
	calibration *taskverification.CalibrationStore       // nil if calibration not wired
}

// NewTaskVerificationConsensusConsumer creates a consensus consumer.
// settler, slashing, and calibration may be nil (graceful degradation).
func NewTaskVerificationConsensusConsumer(
	rounds taskverification.Store,
	settler *settlement.VerificationConsensusSettler,
	slashing *taskverification.SlashingEvaluator,
	calibration *taskverification.CalibrationStore,
) *TaskVerificationConsensusConsumer {
	return &TaskVerificationConsensusConsumer{
		rounds:      rounds,
		settler:     settler,
		slashing:    slashing,
		calibration: calibration,
	}
}

// SetDispatcher wires the dispatcher for exactly-once settlement
// mediation. When set, settlement invocations route through
// dispatcher.Admit instead of calling settler.Settle directly.
func (c *TaskVerificationConsensusConsumer) SetDispatcher(d DispatcherAdmitter) {
	c.dispatcher = d
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

	// Apply finalization to the round if not already terminal.
	// The round may already be finalized by the vote consumer's inline
	// finalization path — that's fine, settlement still needs to run.
	if !round.IsTerminal() {
		verdict := parseConsensusVerdict(payload.FinalVerdict)
		targetState := consensusVerdictToState(verdict)
		if err := round.Transition(targetState, payload.FinalizationTimeUnix); err == nil {
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
		}
	}

	// Apply v4.1 economic settlement via the dispatcher's exactly-once
	// admission (commit 9 of §9). The dispatcher mediates settlement
	// invocation through TVConsensusConsumer.Apply; this consumer only
	// routes the event. Round state, calibration, and slashing remain
	// here because they do not require cross-node ledger convergence.
	if c.dispatcher != nil {
		if err := c.dispatcher.Admit(context.Background(), ev); err != nil {
			slog.Warn("task_verification_consensus: dispatcher admission failed",
				"task_id", payload.TaskID, "err", err)
		}
	} else if c.settler != nil {
		settleResult, err := c.settler.Settle(context.Background(), &payload, round)
		if err != nil {
			slog.Warn("task_verification_consensus: settlement failed",
				"task_id", payload.TaskID, "err", err)
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

	// Apply calibration counters once per round per distinct analyzer family
	// that contributed any vote. Idempotency-guarded by round.CalibrationApplied
	// so a replay does not double-count. Must run BEFORE slashing so that
	// SlashingEvaluator.EvaluateRound reads the post-increment calibration
	// state when deciding whether a (category, family) tuple is calibrated.
	// Per step-2 plan §D2.
	if c.calibration != nil && !round.CalibrationApplied {
		allSucceeded := true
		seen := make(map[string]struct{}, len(round.Votes))
		for _, vote := range round.Votes {
			fam := vote.AnalyzerFamily
			if fam == "" {
				continue
			}
			if _, already := seen[fam]; already {
				continue
			}
			seen[fam] = struct{}{}
			if _, err := c.calibration.Increment(context.Background(), round.Category, verification.FamilyID(fam)); err != nil {
				slog.Warn("task_verification_consensus: calibration increment failed",
					"round_id", payload.RoundID,
					"category", round.Category,
					"family", fam,
					"err", err,
				)
				// Don't set CalibrationApplied; next replay retries.
				// Note: partially-applied increments before the failure will
				// double-count on retry, since Increment is non-idempotent.
				// This is within §8's conservative margin; noted for future
				// hardening.
				allSucceeded = false
				break
			}
		}
		if allSucceeded {
			round.CalibrationApplied = true
			if err := c.rounds.SaveRound(context.Background(), round); err != nil {
				slog.Warn("task_verification_consensus: save round after calibration failed",
					"round_id", payload.RoundID, "err", err)
			}
		}
	}

	// Evaluate slashing after settlement and calibration. Best-effort —
	// failures log but do not block the pipeline.
	if c.slashing != nil {
		actions := c.slashing.EvaluateRound(context.Background(), round)
		for _, action := range actions {
			slog.Info("task_verification_consensus: slashing action",
				"round_id", payload.RoundID,
				"validator_id", action.ValidatorID,
				"type", action.Type,
				"reason", action.Reason,
				"stake_penalty_bp", action.StakePenaltyBP,
				"reputation_penalty", action.ReputationPenalty,
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
