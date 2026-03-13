package replay

import (
	"context"
	"strings"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ReplayExecutor is the interface for executing a single replay job.
// Implementations range from InspectionExecutor (testnet, material-only
// assessment) to future TEE-attested or sandbox-isolated executors.
type ReplayExecutor interface {
	Execute(ctx context.Context, job *ReplayJob) (*ReplayOutcome, error)
}

// TaskDetailsProvider is the interface the ReplayRunner uses to look up task
// metadata for ProcessReplayOutcome. It is defined here to avoid importing
// internal/tasks from internal/replay.
type TaskDetailsProvider interface {
	// GetReplayDetails returns the claimer agent ID, original result hash,
	// task title, budget-weighted verified value (budget × score.Overall),
	// and whether the task is generation-eligible.
	// Returns a non-nil error when the task is not found.
	GetReplayDetails(taskID string) (agentID, resultHash, title string, verifiedValue uint64, generationEligible bool, err error)
}

// InspectionExecutor assesses a replay job's replay material and produces an
// outcome without actually re-executing checks. It is the default executor for
// testnet nodes where a full execution sandbox is not available.
//
// For jobs whose material satisfies AssessReplayability the outcome notes that
// execution is pending (material present, no execution engine). For jobs with
// insufficient material the outcome lists the missing fields.
//
// Both cases produce Status "error", which causes the resolver to mark the job
// as "failed" and the enforcer to apply the "no_action" verdict — giving the
// worker the benefit of the doubt when replay cannot be performed.
type InspectionExecutor struct{}

// NewInspectionExecutor returns an InspectionExecutor.
func NewInspectionExecutor() *InspectionExecutor { return &InspectionExecutor{} }

// Execute implements ReplayExecutor.
func (e *InspectionExecutor) Execute(_ context.Context, job *ReplayJob) (*ReplayOutcome, error) {
	reqs := jobToReplayRequirements(job)
	assessment := verification.AssessReplayability(reqs, job.Category, job.PolicyVersion)

	var notes string
	if !assessment.Replayable {
		notes = "not replayable: missing fields: " + strings.Join(assessment.MissingFields, ", ")
	} else {
		notes = "execution-pending: material present but no execution engine"
	}

	return &ReplayOutcome{
		JobID:      job.ID,
		TaskID:     job.TaskID,
		Status:     "error",
		ReplayedAt: time.Now(),
		ReplayerID: "inspection-executor",
		Notes:      notes,
	}, nil
}

// jobToReplayRequirements converts the fields stored on a ReplayJob back into
// a verification.ReplayRequirements for use with AssessReplayability.
func jobToReplayRequirements(job *ReplayJob) *verification.ReplayRequirements {
	return &verification.ReplayRequirements{
		SourceSnapshotHash:      job.SourceSnapshotHash,
		AcceptanceContractHash:  job.AcceptanceContractHash,
		RequiredChecks:          job.ChecksToReplay,
		EnvironmentManifestHash: job.EnvironmentManifestHash,
		ToolchainManifestHash:   job.ToolchainManifestHash,
		CommandSpecs:            job.CommandSpecs,
		ArtifactRefs:            job.ArtifactRefs,
	}
}
