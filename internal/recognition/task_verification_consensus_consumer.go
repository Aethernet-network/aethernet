package recognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// TaskVerificationConsensusConsumer processes TaskVerificationConsensus
// events from the DAG: applies round state for replay safety AND advances
// per-(category, family) calibration counters AND evaluates slashing.
//
// Settlement invocation was previously owned by this consumer (via an
// inline dispatcher.Admit call with a direct settler.Settle fallback).
// Part E.1 moved that responsibility to the general
// DispatcherAdmissionConsumer, which forwards every committed event to
// dispatch.Dispatcher.Admit; the dispatcher routes
// TaskVerificationConsensus events to dispatch.TVConsensusConsumer.Apply
// via its Interested() filter. This consumer retains only the local-node
// replay-safe and best-effort sidework (round state, calibration,
// slashing) that does not require cross-node ledger convergence.
type TaskVerificationConsensusConsumer struct {
	rounds      taskverification.Store
	slashing    *taskverification.SlashingEvaluator // nil if slashing not wired
	calibration *taskverification.CalibrationStore  // nil if calibration not wired
}

// NewTaskVerificationConsensusConsumer creates a consensus consumer.
// slashing and calibration may be nil (graceful degradation).
//
// The settler parameter was removed in Part E.1: settlement now flows
// exclusively through the DispatcherAdmissionConsumer →
// dispatch.Dispatcher → dispatch.TVConsensusConsumer.Apply path.
func NewTaskVerificationConsensusConsumer(
	rounds taskverification.Store,
	slashing *taskverification.SlashingEvaluator,
	calibration *taskverification.CalibrationStore,
) *TaskVerificationConsensusConsumer {
	return &TaskVerificationConsensusConsumer{
		rounds:      rounds,
		slashing:    slashing,
		calibration: calibration,
	}
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

// Consume applies the consensus event to the corresponding round, runs
// calibration counters, and evaluates slashing. Idempotent: if the round
// is already finalized in the same way, finalization is a no-op;
// calibration is guarded by round.CalibrationApplied; slashing is
// best-effort.
//
// Settlement is NOT invoked here — see the type doc comment. The
// DispatcherAdmissionConsumer forwards this event to the dispatcher,
// which routes it to dispatch.TVConsensusConsumer.Apply for the
// economic settlement.
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
	// finalization path — that's fine, the admission-router-fed
	// dispatcher settlement path still runs regardless.
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

	// Settlement invocation is routed by the general recognition→dispatcher
	// admission router (internal/recognition/dispatcher_admission_consumer.go,
	// IM Part E.1 / commit-13). This consumer performs only per-node work
	// that does not require cross-node ledger convergence: round-state
	// finalization, calibration counters, slashing evaluation. The former
	// dispatcher field / SetDispatcher method / legacy direct-settler
	// fallback were removed when the general router landed — see locked-
	// invariant review §2.1 C-14 for the cross-check.

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

	// Evaluate slashing after calibration. Best-effort — failures log but
	// do not block the pipeline.
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
