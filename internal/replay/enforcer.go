package replay

import (
	"errors"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/canary"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// slashExecutor applies an economic penalty to a validator identified by ID.
// *validator.SlashEngine satisfies this interface via an adapter in cmd/node
// — keeping the replay package free of the validator package import.
type slashExecutor interface {
	// Slash applies the named offense to the validator. Returns the slash
	// amount (µAET) and any error. Errors are logged but non-fatal — the
	// replay outcome state change has already been committed.
	Slash(validatorID string, offense string) (slashAmount uint64, err error)
}

// canaryEvalSource is the interface for canary task lookup by protocol task ID.
// *canary.CanaryManager satisfies this interface without importing the canary
// package back into replay (the import goes one-way: replay → canary).
type canaryEvalSource interface {
	GetCanaryByTaskID(taskID string) (*canary.CanaryTask, error)
}

// canaryEvalRecorder is the interface for computing and persisting calibration
// signals. *canary.Evaluator satisfies this interface. observedOutput is the
// raw output text; empty string is accepted when not available (replay path
// typically has no output text — backward compatible).
type canaryEvalRecorder interface {
	Evaluate(c *canary.CanaryTask, actorID, role string, observedPass bool, observedChecks map[string]bool, observedOutput string) *canary.CalibrationSignal
}

// taskReplayInterface is the subset of *tasks.TaskManager used by ReplayEnforcer.
// Defining a local interface avoids an import cycle between internal/replay and
// internal/tasks.
type taskReplayInterface interface {
	// SetReplayStatus updates the task's ReplayStatus and ReplayJobID and
	// persists the change.
	SetReplayStatus(taskID string, status string, jobID string) error
	// SetGenerationStatus updates the generation credit status for a task and
	// persists the change. status should be one of "", "recognized", "held",
	// or "denied".
	SetGenerationStatus(taskID string, status string) error
}

// generationTrigger is called by ReplayEnforcer when a replay confirms the
// original work and a generation credit can be released. It receives the same
// parameters that would have been passed to
// ledger.GenerationLedger.RecordTaskGeneration.
type generationTrigger interface {
	RecordGeneration(taskID, agentID, resultHash, title string, value uint64) error
}

// ReplayEnforcer maps completed ReplayOutcomes to concrete task state changes.
// It delegates outcome recording and evaluation to a ReplayResolver, then
// updates the task's ReplayStatus and optionally triggers a held generation
// ledger entry.
type ReplayEnforcer struct {
	taskMgr    taskReplayInterface
	resolver   *ReplayResolver
	genTrigger generationTrigger // optional; nil when generation hold not in use
	slashExec  slashExecutor     // optional; nil when slashing not configured

	// canaryLookup and canaryEval are optional. When both are non-nil, replay
	// outcomes for canary tasks emit a CalibrationSignal with
	// role=RoleReplaySubmitter. Evaluation is observational — it does not
	// change the task state or verdict.
	canaryLookup canaryEvalSource
	canaryEval   canaryEvalRecorder
}

// NewReplayEnforcer returns a ReplayEnforcer. genTrigger may be nil if
// generation-hold semantics are not required.
func NewReplayEnforcer(
	taskMgr taskReplayInterface,
	resolver *ReplayResolver,
	genTrigger generationTrigger,
) *ReplayEnforcer {
	return &ReplayEnforcer{
		taskMgr:    taskMgr,
		resolver:   resolver,
		genTrigger: genTrigger,
	}
}

// SetCanaryEvaluator wires canary evaluation into the replay enforcer. When
// both src and rec are non-nil, replay outcomes whose task IDs map to a canary
// record emit a CalibrationSignal with role=RoleReplaySubmitter and
// observedPass=(outcome.Status=="match"). Evaluation is observational — it
// does not affect the replay verdict, task status, or generation credit.
// When not called (default), no canary evaluation occurs — backward compatible.
func (e *ReplayEnforcer) SetCanaryEvaluator(src canaryEvalSource, rec canaryEvalRecorder) {
	e.canaryLookup = src
	e.canaryEval = rec
}

// SetSlashExecutor wires a SlashEngine adapter so the enforcer applies an
// economic penalty to the original worker when a "slash_recommended" verdict
// is reached. Slash errors are logged but non-fatal — the task state change
// has already been committed before Slash is called.
// When not called (default), no slashing occurs — backward compatible.
func (e *ReplayEnforcer) SetSlashExecutor(se slashExecutor) {
	e.slashExec = se
}

// ProcessReplayOutcome records outcome, evaluates the verdict, and applies the
// resulting task state change:
//
//   - "no_action" or "flag_for_review" → "replay_complete"
//     If generationEligible and genTrigger is set, RecordGeneration is called
//     so the held generation credit (if any) is released.
//   - "open_challenge" or "slash_recommended" → "replay_disputed"
//     Generation credit is permanently withheld.
//
// generationEligible must be true for the task's AcceptanceContract to allow
// generation ledger entries. When false, SetGenerationStatus and
// RecordGeneration are skipped regardless of the verdict — ensuring
// non-generation-eligible tasks never write generation ledger entries via
// the replay path.
//
// Returns the evaluated ReplayVerdict and any persistence error.
// Returns a non-nil error immediately if outcome is nil.
func (e *ReplayEnforcer) ProcessReplayOutcome(
	outcome *ReplayOutcome,
	agentID, resultHash, title string,
	verifiedValue uint64,
	generationEligible bool,
) (*verification.ReplayVerdict, error) {
	if outcome == nil {
		return nil, errors.New("enforcer: nil outcome")
	}

	if err := e.resolver.RecordOutcome(outcome); err != nil {
		slog.Error("enforcer: record outcome failed", "task_id", outcome.TaskID, "err", err)
		return nil, err
	}

	verdict := e.resolver.EvaluateOutcome(outcome)

	var newStatus string
	var newGenStatus string
	switch verdict.Action {
	case "no_action", "flag_for_review":
		newStatus = "replay_complete"
		newGenStatus = "recognized"
	case "open_challenge", "slash_recommended":
		newStatus = "replay_disputed"
		newGenStatus = "denied"
	default:
		newStatus = "replay_complete"
		newGenStatus = "recognized"
	}

	if err := e.taskMgr.SetReplayStatus(outcome.TaskID, newStatus, outcome.JobID); err != nil {
		slog.Error("enforcer: set replay status", "task_id", outcome.TaskID, "status", newStatus, "err", err)
		return verdict, err
	}

	// Apply economic consequences for adversarial outcomes.
	// "slash_recommended": execute slash immediately (fraudulent approval).
	// "open_challenge":    slash is deferred pending dispute resolution.
	switch verdict.Action {
	case "slash_recommended":
		if e.slashExec != nil && outcome.ReplayerID != "" {
			slashAmt, slashErr := e.slashExec.Slash(outcome.ReplayerID, "fraudulent_approval")
			if slashErr != nil {
				slog.Error("enforcer: slash failed",
					"task_id", outcome.TaskID, "agent_id", outcome.ReplayerID, "err", slashErr)
			} else {
				slog.Info("enforcer: slash applied",
					"task_id", outcome.TaskID, "agent_id", outcome.ReplayerID, "slash_amount", slashAmt)
			}
		}
	case "open_challenge":
		slog.Info("enforcer: challenge opened, slash deferred pending dispute resolution",
			"task_id", outcome.TaskID, "agent_id", outcome.ReplayerID)
	}

	// Canary calibration: if this task is a protocol-internal canary, emit a
	// calibration signal for the replay submitter. Evaluation is observational
	// and does not change the task state or generation credit.
	if e.canaryLookup != nil && e.canaryEval != nil {
		if c, err := e.canaryLookup.GetCanaryByTaskID(outcome.TaskID); err == nil {
			observedPass := outcome.Status == "match"
			// observedOutput is empty for the replay path — replay submitters
			// provide structured check results, not free-form text. The truth
			// model falls back to verifier-only scoring gracefully.
			sig := e.canaryEval.Evaluate(c, outcome.ReplayerID, canary.RoleReplaySubmitter, observedPass, nil, "")
			slog.Info("canary: replay calibration signal emitted",
				"signal_id", sig.ID,
				"task_id", outcome.TaskID,
				"canary_id", c.ID,
				"actor_id", outcome.ReplayerID,
				"correctness", sig.Correctness,
				"severity", sig.Severity,
				"computed_by", sig.ComputedBy,
			)
		}
	}

	// Update the generation credit status and release held credit only for
	// generation-eligible tasks. Non-eligible tasks must never write generation
	// ledger entries — even on replay_complete.
	if generationEligible {
		if err := e.taskMgr.SetGenerationStatus(outcome.TaskID, newGenStatus); err != nil {
			slog.Error("enforcer: set generation status", "task_id", outcome.TaskID, "gen_status", newGenStatus, "err", err)
			// Non-fatal: replay status has already been updated.
		}
		if newStatus == "replay_complete" && e.genTrigger != nil {
			if err := e.genTrigger.RecordGeneration(outcome.TaskID, agentID, resultHash, title, verifiedValue); err != nil {
				slog.Error("enforcer: generation trigger failed",
					"task_id", outcome.TaskID, "agent_id", agentID, "err", err)
				// Non-fatal: the task status has already been updated.
			}
		}
	}

	slog.Info("enforcer: outcome processed",
		"task_id", outcome.TaskID,
		"verdict_action", verdict.Action,
		"new_status", newStatus,
		"generation_eligible", generationEligible,
		"severity", verdict.SeverityScore)

	return verdict, nil
}
