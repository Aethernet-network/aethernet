package trajectory

import (
	"encoding/json"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// MaxTrajectoryLimit is the maximum number of commits returned in a single query.
const MaxTrajectoryLimit = 500

// CommitNode is a single trajectory commit in the response.
type CommitNode struct {
	EventID        event.EventID              `json:"event_id"`
	TaskID         string                     `json:"task_id"`
	ParentCommitID string                     `json:"parent_commit_id,omitempty"`
	Outcome        event.TrajectoryOutcome    `json:"outcome"`
	CheckpointHash string                     `json:"checkpoint_hash"`
	CheckpointSize int64                      `json:"checkpoint_size"`
	ComputeCost    uint64                     `json:"compute_cost"`
	QualityScore   float64                    `json:"quality_score"`
	CategoryHint   string                     `json:"category_hint,omitempty"`
	BranchID       string                     `json:"branch_id,omitempty"`
	CausalTimestamp uint64                    `json:"causal_timestamp"`
	AgentID        string                     `json:"agent_id"`
}

// CommitWithBody extends CommitNode with the optional checkpoint body.
type CommitWithBody struct {
	CommitNode
	// CheckpointBody is the decoded checkpoint content. Nil when
	// include_bodies=false or when the blob is unavailable.
	CheckpointBody *CheckpointBody `json:"checkpoint_body,omitempty"`
	// BodyAvailable indicates whether the blob was found in the blobstore.
	BodyAvailable bool `json:"body_available"`
}

// TreeResponse is the response for GET /v1/tasks/{id}/trajectories.
type TreeResponse struct {
	TaskID  string           `json:"task_id"`
	Count   int              `json:"count"`
	Commits []CommitWithBody `json:"commits"`
}

// CommitNodeFromEvent extracts a CommitNode from a DAG event of type
// TrajectoryCommit. Returns nil if the event is not a trajectory commit
// or if the payload cannot be decoded.
func CommitNodeFromEvent(ev *event.Event) *CommitNode {
	if ev.Type != event.EventTypeTrajectoryCommit {
		return nil
	}
	var payload event.TrajectoryCommitPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return nil
	}
	return &CommitNode{
		EventID:         ev.ID,
		TaskID:          payload.TaskID,
		ParentCommitID:  payload.ParentCommitID,
		Outcome:         payload.Outcome,
		CheckpointHash:  payload.CheckpointHash,
		CheckpointSize:  payload.CheckpointSize,
		ComputeCost:     payload.ComputeCost,
		QualityScore:    payload.QualityScore,
		CategoryHint:    payload.CategoryHint,
		BranchID:        payload.BranchID,
		CausalTimestamp: ev.CausalTimestamp,
		AgentID:         ev.AgentID,
	}
}
