package replay

import (
	"errors"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

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
