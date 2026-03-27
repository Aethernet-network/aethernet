// Package trajectory implements the trajectory commit service for AetherNet.
//
// The service handles checkpoint body storage, lean payload construction,
// auth enforcement (claimer-only), rate limiting, and event emission through
// the existing Fast Path local submission pipeline.
//
// Layer: Application — may import Core Protocol and Infrastructure packages.
package trajectory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// TrajectoryConfig holds tunable parameters for the trajectory service.
type TrajectoryConfig struct {
	Enabled                       bool
	MaxCommitsPerTask             int
	MaxCommitsPerMinute           int
	MaxCheckpointBodySize         int64
	MaxCommitPayloadSize          int64
	FetchCheckpointBodiesByDefault bool
	IndexTrajectoryBlobs          bool
}

// DefaultTrajectoryConfig returns production-ready defaults.
func DefaultTrajectoryConfig() TrajectoryConfig {
	return TrajectoryConfig{
		Enabled:                       true,
		MaxCommitsPerTask:             100,
		MaxCommitsPerMinute:           10,
		MaxCheckpointBodySize:         1 << 20, // 1 MiB
		MaxCommitPayloadSize:          64 << 10, // 64 KiB
		FetchCheckpointBodiesByDefault: false,
		IndexTrajectoryBlobs:          true,
	}
}

// Service errors.
var (
	ErrTrajectoryDisabled   = errors.New("trajectory: service disabled")
	ErrNotClaimer           = errors.New("trajectory: only the task claimer may emit trajectory commits")
	ErrTaskNotClaimed       = errors.New("trajectory: task is not in claimed state")
	ErrPerTaskLimitExceeded = errors.New("trajectory: per-task commit limit exceeded")
	ErrRateLimitExceeded    = errors.New("trajectory: per-agent rate limit exceeded")
	ErrCheckpointTooLarge   = errors.New("trajectory: checkpoint body too large")
	ErrPayloadTooLarge      = errors.New("trajectory: commit payload too large")
	ErrParentNotFound       = errors.New("trajectory: parent commit not found in DAG")
	ErrClaimEventMissing    = errors.New("trajectory: task has no claim event ID")
)

// Service manages trajectory commit emission. Safe for concurrent use.
type Service struct {
	config  TrajectoryConfig
	dag     *dag.DAG
	blob    blobstore.Store
	node    *network.Node
	taskMgr *tasks.TaskManager
	kp      *crypto.KeyPair

	// Per-task commit count.
	taskCommits map[string]int
	// Per-agent per-minute rate tracking.
	agentRate   map[crypto.AgentID]*rateWindow
	mu          sync.Mutex
}

type rateWindow struct {
	count    int
	windowAt int64 // Unix minute
}

// NewService creates a trajectory commit service.
func NewService(
	config TrajectoryConfig,
	d *dag.DAG,
	blob blobstore.Store,
	node *network.Node,
	taskMgr *tasks.TaskManager,
	kp *crypto.KeyPair,
) *Service {
	return &Service{
		config:      config,
		dag:         d,
		blob:        blob,
		node:        node,
		taskMgr:     taskMgr,
		kp:          kp,
		taskCommits: make(map[string]int),
		agentRate:   make(map[crypto.AgentID]*rateWindow),
	}
}

// CommitRequest is the input for EmitCommit.
type CommitRequest struct {
	TaskID                 string
	ParentCommitID         string
	Outcome                event.TrajectoryOutcome
	ApproachDescription    string
	Parameters             map[string]string
	EvidenceSnippet        string
	ErrorDetail            string
	IntermediateOutputHash string
	ComputeCost            uint64
	QualityScore           float64
	CategoryHint           string
	BranchID               string
}

// CommitResponse is the output of EmitCommit.
type CommitResponse struct {
	EventID        event.EventID `json:"event_id"`
	CheckpointHash string        `json:"checkpoint_hash"`
	CheckpointSize int64         `json:"checkpoint_size"`
	TaskID         string        `json:"task_id"`
}

// EmitCommit stores the checkpoint blob, constructs the lean payload,
// creates the event with task-local causal refs, signs it, adds it to
// the DAG, and submits it via Fast Path local submission.
func (s *Service) EmitCommit(ctx context.Context, actorID crypto.AgentID, req CommitRequest) (*CommitResponse, error) {
	if !s.config.Enabled {
		return nil, ErrTrajectoryDisabled
	}

	// Verify the actor is the task claimer.
	task, err := s.taskMgr.Get(req.TaskID)
	if err != nil {
		return nil, fmt.Errorf("trajectory: %w", err)
	}
	if task.Status != tasks.TaskStatusClaimed && task.Status != tasks.TaskStatusSubmitted {
		return nil, ErrTaskNotClaimed
	}
	if crypto.AgentID(task.ClaimerID) != actorID {
		return nil, ErrNotClaimer
	}
	if task.ClaimEventID == "" {
		return nil, ErrClaimEventMissing
	}

	// Enforce per-task commit limit.
	s.mu.Lock()
	if s.taskCommits[req.TaskID] >= s.config.MaxCommitsPerTask {
		s.mu.Unlock()
		return nil, ErrPerTaskLimitExceeded
	}

	// Enforce per-agent per-minute rate limit.
	now := time.Now().Unix()
	minute := now / 60
	rw, ok := s.agentRate[actorID]
	if !ok || rw.windowAt != minute {
		s.agentRate[actorID] = &rateWindow{count: 1, windowAt: minute}
	} else {
		if rw.count >= s.config.MaxCommitsPerMinute {
			s.mu.Unlock()
			return nil, ErrRateLimitExceeded
		}
		rw.count++
	}
	s.mu.Unlock()

	// Build and store checkpoint body.
	body := CheckpointBody{
		ApproachDescription:    req.ApproachDescription,
		Parameters:             req.Parameters,
		EvidenceSnippet:        req.EvidenceSnippet,
		ErrorDetail:            req.ErrorDetail,
		IntermediateOutputHash: req.IntermediateOutputHash,
	}

	canonical, err := CanonicalCheckpointBytes(body)
	if err != nil {
		return nil, fmt.Errorf("trajectory: canonicalize: %w", err)
	}
	if int64(len(canonical)) > s.config.MaxCheckpointBodySize {
		return nil, fmt.Errorf("%w: %d bytes > %d limit", ErrCheckpointTooLarge, len(canonical), s.config.MaxCheckpointBodySize)
	}

	blobHash, blobSize, err := s.blob.Put(ctx, canonical)
	if err != nil {
		return nil, fmt.Errorf("trajectory: store blob: %w", err)
	}

	// Construct lean payload.
	payload := event.TrajectoryCommitPayload{
		TaskID:         req.TaskID,
		ParentCommitID: req.ParentCommitID,
		Outcome:        req.Outcome,
		CheckpointHash: blobHash,
		CheckpointSize: blobSize,
		ComputeCost:    req.ComputeCost,
		QualityScore:   req.QualityScore,
		CategoryHint:   req.CategoryHint,
		BranchID:       req.BranchID,
	}

	if err := payload.Validate(); err != nil {
		return nil, fmt.Errorf("trajectory: %w", err)
	}

	// Check payload size.
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("trajectory: marshal payload: %w", err)
	}
	if int64(len(payloadBytes)) > s.config.MaxCommitPayloadSize {
		return nil, fmt.Errorf("%w: %d bytes > %d limit", ErrPayloadTooLarge, len(payloadBytes), s.config.MaxCommitPayloadSize)
	}

	// Construct task-local causal refs.
	claimEventID := event.EventID(task.ClaimEventID)
	var causalRefs []event.EventID
	priorTS := make(map[event.EventID]uint64)

	if req.ParentCommitID == "" {
		// Root commit: refs = [task_claim_event_id]
		causalRefs = []event.EventID{claimEventID}
		if ev, err := s.dag.Get(claimEventID); err == nil {
			priorTS[claimEventID] = ev.CausalTimestamp
		}
	} else {
		// Child commit: refs = [task_claim_event_id, parent_commit_id]
		parentID := event.EventID(req.ParentCommitID)
		if _, err := s.dag.Get(parentID); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrParentNotFound, req.ParentCommitID)
		}
		causalRefs = []event.EventID{claimEventID, parentID}
		if ev, err := s.dag.Get(claimEventID); err == nil {
			priorTS[claimEventID] = ev.CausalTimestamp
		}
		if ev, err := s.dag.Get(parentID); err == nil {
			priorTS[parentID] = ev.CausalTimestamp
		}
	}

	// Create event.
	ev, err := event.New(event.EventTypeTrajectoryCommit, causalRefs, payload, string(actorID), priorTS, 0)
	if err != nil {
		return nil, fmt.Errorf("trajectory: create event: %w", err)
	}

	// Sign with the node's keypair.
	if err := crypto.SignEvent(ev, s.kp); err != nil {
		return nil, fmt.Errorf("trajectory: sign event: %w", err)
	}

	// Add to DAG.
	if err := s.dag.Add(ev); err != nil {
		return nil, fmt.Errorf("trajectory: dag.Add: %w", err)
	}

	// Submit via Fast Path local submission.
	if s.node != nil {
		_ = s.node.SubmitLocalEvent(ev)
		_ = s.node.Broadcast(ev)
	}

	// Update per-task commit count.
	s.mu.Lock()
	s.taskCommits[req.TaskID]++
	s.mu.Unlock()

	slog.Info("trajectory: commit emitted",
		"event_id", ev.ID, "task_id", req.TaskID,
		"outcome", req.Outcome, "checkpoint_hash", blobHash)

	return &CommitResponse{
		EventID:        ev.ID,
		CheckpointHash: blobHash,
		CheckpointSize: blobSize,
		TaskID:         req.TaskID,
	}, nil
}

// TaskMgr returns the task manager used by this service.
func (s *Service) TaskMgr() *tasks.TaskManager { return s.taskMgr }

// CommitCount returns the number of commits emitted for a task.
func (s *Service) CommitCount(taskID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskCommits[taskID]
}

// GetTrajectories retrieves trajectory commits for a task from the DAG.
// Results are sorted deterministically by (CausalTimestamp, EventID).
// When includeBodies is true, checkpoint bodies are fetched from the blobstore.
// Missing blobs do not fail the response — the commit is returned with
// BodyAvailable=false.
func (s *Service) GetTrajectories(ctx context.Context, taskID string, includeBodies bool, branchID string, limit int) (*TreeResponse, error) {
	if limit <= 0 || limit > MaxTrajectoryLimit {
		limit = MaxTrajectoryLimit
	}

	// Scan the DAG for trajectory commits matching this task.
	all := s.dag.All()
	var nodes []CommitWithBody

	for _, ev := range all {
		if ev.Type != event.EventTypeTrajectoryCommit {
			continue
		}
		node := CommitNodeFromEvent(ev)
		if node == nil || node.TaskID != taskID {
			continue
		}
		if branchID != "" && node.BranchID != branchID {
			continue
		}
		nodes = append(nodes, CommitWithBody{CommitNode: *node})
	}

	// Deterministic sort: (CausalTimestamp ASC, EventID ASC).
	sortCommits(nodes)

	// Apply limit.
	if len(nodes) > limit {
		nodes = nodes[:limit]
	}

	// Optionally fetch bodies.
	if includeBodies {
		for i := range nodes {
			hash := nodes[i].CheckpointHash
			if hash == "" {
				continue
			}
			data, err := s.blob.Get(ctx, hash)
			if err != nil {
				// Missing blob — not a fatal error.
				nodes[i].BodyAvailable = false
				continue
			}
			var body CheckpointBody
			if err := json.Unmarshal(data, &body); err != nil {
				nodes[i].BodyAvailable = false
				continue
			}
			nodes[i].CheckpointBody = &body
			nodes[i].BodyAvailable = true
		}
	}

	return &TreeResponse{
		TaskID:  taskID,
		Count:   len(nodes),
		Commits: nodes,
	}, nil
}

// sortCommits sorts commits deterministically by (CausalTimestamp, EventID).
func sortCommits(nodes []CommitWithBody) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].CausalTimestamp != nodes[j].CausalTimestamp {
			return nodes[i].CausalTimestamp < nodes[j].CausalTimestamp
		}
		return nodes[i].EventID < nodes[j].EventID
	})
}
