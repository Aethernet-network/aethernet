package event

import (
	"errors"
	"fmt"
)

// TrajectoryOutcome describes the state of work at a trajectory checkpoint.
type TrajectoryOutcome string

const (
	// OutcomeExploring means the agent is actively investigating an approach.
	OutcomeExploring TrajectoryOutcome = "exploring"
	// OutcomeDeadEnd means the current approach has been abandoned.
	OutcomeDeadEnd TrajectoryOutcome = "dead_end"
	// OutcomePivot means the agent is switching to a different approach.
	OutcomePivot TrajectoryOutcome = "pivot"
	// OutcomeConverged means the agent believes the work is complete.
	OutcomeConverged TrajectoryOutcome = "converged"
)

// validOutcomes is the set of valid TrajectoryOutcome values.
var validOutcomes = map[TrajectoryOutcome]bool{
	OutcomeExploring: true,
	OutcomeDeadEnd:   true,
	OutcomePivot:     true,
	OutcomeConverged: true,
}

// IsValidOutcome returns true if the outcome is a recognized value.
func IsValidOutcome(o TrajectoryOutcome) bool {
	return validOutcomes[o]
}

// TrajectoryCommitPayload is the payload of an EventTypeTrajectoryCommit event.
// It references a checkpoint body stored in the blobstore by content hash.
//
// Causal refs for trajectory commits are task-local:
//   - Root commit: CausalRefs = [task_claim_event_id]
//   - Child commit: CausalRefs = [task_claim_event_id, parent_commit_id]
type TrajectoryCommitPayload struct {
	// Version is the payload schema version. Must be 1 for current protocol.
	Version uint8 `json:"v"`

	// TaskID identifies the task this trajectory belongs to.
	TaskID string `json:"task_id"`

	// ParentCommitID is the EventID of the previous commit in this trajectory.
	// Empty for the root commit of a task.
	ParentCommitID string `json:"parent_commit_id,omitempty"`

	// Outcome describes the state of work at this checkpoint.
	Outcome TrajectoryOutcome `json:"outcome"`

	// CheckpointHash is the content-addressed hash of the CheckpointBody
	// stored in the blobstore. Format: hex-encoded SHA-256.
	CheckpointHash string `json:"checkpoint_hash"`

	// CheckpointSize is the byte size of the canonical CheckpointBody.
	CheckpointSize int64 `json:"checkpoint_size"`

	// ComputeCost is the self-reported compute cost in micro-AET.
	ComputeCost uint64 `json:"compute_cost"`

	// QualityScoreBP is the agent's self-assessed quality in basis points [0, 10000].
	QualityScoreBP uint32 `json:"quality_score_bp"`

	// CategoryHint classifies the type of work (e.g. "code", "research").
	CategoryHint string `json:"category_hint,omitempty"`

	// BranchID identifies the approach branch within the task trajectory.
	// Multiple branches may exist when the agent explores alternatives.
	BranchID string `json:"branch_id,omitempty"`
}

// Sentinel validation errors.
var (
	ErrTrajectoryMissingTaskID         = errors.New("trajectory: task_id is required")
	ErrTrajectoryMissingOutcome        = errors.New("trajectory: outcome is required")
	ErrTrajectoryInvalidOutcome        = errors.New("trajectory: invalid outcome value")
	ErrTrajectoryMissingCheckpointHash = errors.New("trajectory: checkpoint_hash is required")
	ErrTrajectoryInvalidCheckpointSize = errors.New("trajectory: checkpoint_size must be > 0")
	ErrTrajectoryInvalidQualityScore   = errors.New("trajectory: quality_score_bp must be in [0, 10000]")
)

// Validate checks that all required fields are present and valid.
func (p *TrajectoryCommitPayload) Validate() error {
	if p.TaskID == "" {
		return ErrTrajectoryMissingTaskID
	}
	if p.Outcome == "" {
		return ErrTrajectoryMissingOutcome
	}
	if !IsValidOutcome(p.Outcome) {
		return fmt.Errorf("%w: %q", ErrTrajectoryInvalidOutcome, p.Outcome)
	}
	if p.CheckpointHash == "" {
		return ErrTrajectoryMissingCheckpointHash
	}
	if p.CheckpointSize <= 0 {
		return ErrTrajectoryInvalidCheckpointSize
	}
	if p.QualityScoreBP > 10000 {
		return ErrTrajectoryInvalidQualityScore
	}
	return nil
}

// IsRootCommit returns true if this is the first commit in a trajectory
// (no parent commit).
func (p *TrajectoryCommitPayload) IsRootCommit() bool {
	return p.ParentCommitID == ""
}
