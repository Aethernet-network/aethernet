package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/trajectory"
)

type trajectoryCommitRequest struct {
	ParentCommitID         string            `json:"parent_commit_id,omitempty"`
	Outcome                string            `json:"outcome"`
	ApproachDescription    string            `json:"approach_description"`
	Parameters             map[string]string `json:"parameters,omitempty"`
	EvidenceSnippet        string            `json:"evidence_snippet,omitempty"`
	ErrorDetail            string            `json:"error_detail,omitempty"`
	IntermediateOutputHash string            `json:"intermediate_output_hash,omitempty"`
	ComputeCost            uint64            `json:"compute_cost"`
	QualityScoreBP         uint32            `json:"quality_score_bp"`
	CategoryHint           string            `json:"category_hint,omitempty"`
	BranchID               string            `json:"branch_id,omitempty"`
}

// handleTrajectoryCommit handles POST /v1/tasks/{id}/trajectory/commit.
// Stores the checkpoint blob, constructs the lean payload, and emits
// the trajectory commit event through the existing Fast Path pipeline.
func (s *Server) handleTrajectoryCommit(w http.ResponseWriter, r *http.Request) {
	if s.trajService == nil {
		writeError(w, http.StatusNotImplemented, "trajectory service not enabled")
		return
	}
	if s.checkTxIDSeen(w, r) {
		return
	}

	taskID := r.PathValue("id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task ID is required")
		return
	}

	// Auth: use the verified signer identity.
	actorID := getAuthAgent(r)
	if actorID == "" {
		actorID = s.agentID
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	var req trajectoryCommitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	resp, err := s.trajService.EmitCommit(r.Context(), actorID, trajectory.CommitRequest{
		TaskID:                 taskID,
		ParentCommitID:         req.ParentCommitID,
		Outcome:                event.TrajectoryOutcome(req.Outcome),
		ApproachDescription:    req.ApproachDescription,
		Parameters:             req.Parameters,
		EvidenceSnippet:        req.EvidenceSnippet,
		ErrorDetail:            req.ErrorDetail,
		IntermediateOutputHash: req.IntermediateOutputHash,
		ComputeCost:            req.ComputeCost,
		QualityScoreBP:         req.QualityScoreBP,
		CategoryHint:           req.CategoryHint,
		BranchID:               req.BranchID,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, trajectory.ErrTrajectoryDisabled) {
			status = http.StatusNotImplemented
		} else if errors.Is(err, trajectory.ErrNotClaimer) {
			status = http.StatusForbidden
		} else if errors.Is(err, trajectory.ErrRateLimitExceeded) || errors.Is(err, trajectory.ErrPerTaskLimitExceeded) {
			status = http.StatusTooManyRequests
		} else if errors.Is(err, trajectory.ErrCheckpointTooLarge) || errors.Is(err, trajectory.ErrPayloadTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

// handleGetTrajectories handles GET /v1/tasks/{id}/trajectories.
// Returns the trajectory commit tree for a task, optionally including
// checkpoint bodies from the blobstore.
func (s *Server) handleGetTrajectories(w http.ResponseWriter, r *http.Request) {
	if s.trajService == nil {
		writeError(w, http.StatusNotImplemented, "trajectory service not enabled")
		return
	}

	taskID := r.PathValue("id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task ID is required")
		return
	}

	includeBodies := r.URL.Query().Get("include_bodies") == "true"
	branchID := r.URL.Query().Get("branch_id")
	limit := trajectory.MaxTrajectoryLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > trajectory.MaxTrajectoryLimit {
		limit = trajectory.MaxTrajectoryLimit
	}

	resp, err := s.trajService.GetTrajectories(r.Context(), taskID, includeBodies, branchID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}
