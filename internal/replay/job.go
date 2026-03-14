package replay

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ReplayJob represents a request to independently re-execute one or more
// checks from a submitted evidence packet, to verify that the worker's
// claimed outputs are reproducible.
type ReplayJob struct {
	// ID is a deterministic content-addressed identifier derived from
	// TaskID, PacketHash, and CreatedAt.
	ID string

	// TaskID is the marketplace task this replay job belongs to.
	TaskID string

	// PacketHash is the hash of the original verification packet.
	PacketHash string

	// Category is the task category (e.g. "code", "research").
	Category string

	// PolicyVersion is the verification policy in effect (e.g. "v1").
	PolicyVersion string

	// SourceSnapshotHash is copied from ReplayRequirements.SourceSnapshotHash.
	SourceSnapshotHash string

	// AcceptanceContractHash is copied from ReplayRequirements.AcceptanceContractHash.
	// It is the hash of the AcceptanceContract that governed what "pass" means
	// for this task, enabling the executor to verify the contract commitment.
	AcceptanceContractHash string

	// ChecksToReplay is copied from ReplayRequirements.RequiredChecks.
	ChecksToReplay []string

	// EnvironmentManifestHash is copied from ReplayRequirements.EnvironmentManifestHash.
	EnvironmentManifestHash string

	// ToolchainManifestHash is copied from ReplayRequirements.ToolchainManifestHash.
	ToolchainManifestHash string

	// CommandSpecs is copied from ReplayRequirements.CommandSpecs.
	CommandSpecs []verification.CommandSpec

	// ArtifactRefs is copied from ReplayRequirements.ArtifactRefs.
	ArtifactRefs []verification.ArtifactRef

	// MachineReadableResults holds the original claimed structured check output
	// keyed by check type (e.g. "go_test" → {"passed": 42, "failed": 0}).
	// Copied from ReplayRequirements.MachineReadableResults when present.
	// Used by ComparisonExecutor to compare numeric/structured fields
	// against replay-submitted results.
	MachineReadableResults map[string]interface{} `json:"machine_readable_results,omitempty"`

	// ReplayReason describes why this replay was requested
	// (e.g. "spot-check", "dispute", "audit").
	ReplayReason string

	// AgentID is the agent that submitted the original evidence.
	AgentID string

	// CreatedAt is the time the replay job was created.
	CreatedAt time.Time

	// SubmissionDeadline is the time after which the job becomes eligible for
	// fallback processing by InspectionExecutor. External replay executors must
	// submit results before this deadline.
	//
	// Zero value means no deadline is configured — the ReplayRunner processes
	// the job immediately (legacy / InspectionExecutor-only mode).
	// Set by ReplayCoordinator.ScheduleReplay when SubmissionGracePeriod > 0.
	SubmissionDeadline time.Time `json:"submission_deadline,omitempty"`

	// Status is the current lifecycle state of the job:
	// "pending", "running", "completed", or "failed".
	Status string

	// Calibration explanation fields — populated by ScheduleReplay from the
	// most recent CalibrationDecision at scheduling time.
	// Omitted (zero/empty) when no calibration source is configured.
	//
	// Note: ScheduleReplay copies from lastCalibDecision on a best-effort
	// basis. In concurrent environments, the decision corresponds to the last
	// ShouldReplay call, which is normally the one for this task.

	// CalibrationReason is the reason string produced by the scrutiny
	// adjustment (e.g. "weak_calibration_3x", "calibration_disabled").
	CalibrationReason string `json:"calibration_reason,omitempty"`

	// CalibrationAdjustedRate is the effective sample rate that was used for
	// the random roll that decided this task should be replayed.
	CalibrationAdjustedRate float64 `json:"calibration_adjusted_rate,omitempty"`

	// CalibrationSampleCount is the number of calibration signals available
	// for the actor at the time of the scrutiny decision.
	CalibrationSampleCount int `json:"calibration_sample_count,omitempty"`

	// CalibrationAccuracy is the smoothed accuracy value observed for the
	// actor in this task's category at the time of the scrutiny decision.
	CalibrationAccuracy float64 `json:"calibration_accuracy,omitempty"`
}

// NewReplayJob constructs a ReplayJob from a verification packet's replay
// requirements. Fields are copied directly from reqs; ChecksToReplay is set
// from reqs.RequiredChecks. Status is initialised to "pending".
//
// The job ID is computed as the hex-encoded SHA-256 of the concatenation of
// taskID, packetHash, and createdAt.UnixNano(), giving a deterministic
// content-addressed identifier.
func NewReplayJob(
	taskID, packetHash, category, policyVersion, agentID, replayReason string,
	reqs *verification.ReplayRequirements,
	createdAt time.Time,
) *ReplayJob {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s%s%d", taskID, packetHash, createdAt.UnixNano())))
	id := fmt.Sprintf("%x", h)

	job := &ReplayJob{
		ID:            id,
		TaskID:        taskID,
		PacketHash:    packetHash,
		Category:      category,
		PolicyVersion: policyVersion,
		AgentID:       agentID,
		ReplayReason:  replayReason,
		CreatedAt:     createdAt,
		Status:        "pending",
	}

	if reqs != nil {
		job.SourceSnapshotHash = reqs.SourceSnapshotHash
		job.AcceptanceContractHash = reqs.AcceptanceContractHash
		job.ChecksToReplay = reqs.RequiredChecks
		job.EnvironmentManifestHash = reqs.EnvironmentManifestHash
		job.ToolchainManifestHash = reqs.ToolchainManifestHash
		job.CommandSpecs = reqs.CommandSpecs
		job.ArtifactRefs = reqs.ArtifactRefs
		job.MachineReadableResults = reqs.MachineReadableResults
	}

	return job
}
