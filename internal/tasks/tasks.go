// Package tasks implements the AetherNet task marketplace data model.
//
// Tasks represent units of work posted by agents who need compute, inference,
// or other AI-related services. The lifecycle is:
//
//	Open → Claimed → Submitted → Completed
//	Open → Cancelled (by poster)
//	Submitted → Disputed (by poster)
package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusOpen      TaskStatus = "open"
	TaskStatusClaimed   TaskStatus = "claimed"
	TaskStatusSubmitted TaskStatus = "submitted"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusDisputed  TaskStatus = "disputed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// DefaultClaimDeadline is the testnet claim timeout. When a task is claimed,
// the claimer must submit work within this window or the auto-validator
// releases the task back to open and penalises the claimer's reputation.
// On mainnet this would be 24 hours; testnet uses 10 minutes so test cycles
// complete quickly without waiting for the full period.
const DefaultClaimDeadline = 10 * time.Minute

// MinTaskBudget is the minimum budget (in micro-AET) for a task posted via
// the HTTP API. This ensures fees are non-trivial and guards against spam.
// 100,000 µAET = 0.1 AET.
const MinTaskBudget = uint64(100_000)

// MaxCompletedAge is the maximum time a completed or cancelled task stays in
// memory before being archived. Tasks older than this are evicted from the
// in-memory map on the hourly cleanup pass; they remain in the persistence
// store and can be recovered on restart.
const MaxCompletedAge = 7 * 24 * time.Hour

// Sentinel errors returned by TaskManager methods.
var (
	ErrTaskNotFound       = errors.New("tasks: task not found")
	ErrTaskAlreadyClaimed = errors.New("tasks: task already claimed")
	ErrTaskNotClaimed     = errors.New("tasks: task not claimed")
	ErrTaskNotSubmitted   = errors.New("tasks: task not submitted")
	ErrTaskNotOpen        = errors.New("tasks: task is not open")
	ErrWrongClaimer       = errors.New("tasks: not the claimer")
	ErrWrongPoster        = errors.New("tasks: not the poster")
	ErrNotClaimer         = errors.New("tasks: caller is not the task claimer")
	ErrSelfClaim          = errors.New("tasks: poster cannot claim their own task")
	ErrTaskAlreadyRouted  = errors.New("tasks: task already routed to another agent")
)

// Task is a unit of work posted to the marketplace.
type Task struct {
	ID                string           `json:"id"`
	Title             string           `json:"title"`
	Description       string           `json:"description"`
	Category          string           `json:"category"`
	PosterID          string           `json:"poster_id"`
	ClaimerID         string           `json:"claimer_id,omitempty"`
	Budget            uint64           `json:"budget"`
	Status            TaskStatus       `json:"status"`
	ResultHash        string           `json:"result_hash,omitempty"`
	ResultNote        string           `json:"result_note,omitempty"`
	ResultURI         string           `json:"result_uri,omitempty"`
	VerificationScore *evidence.Score  `json:"verification_score,omitempty"`
	PostedAt          int64            `json:"posted_at"`
	ClaimedAt         int64            `json:"claimed_at,omitempty"`
	ClaimDeadline     int64            `json:"claim_deadline,omitempty"`  // unix-nano; 0 means no deadline set
	SubmittedAt       int64            `json:"submitted_at,omitempty"`
	DisputedAt        int64            `json:"disputed_at,omitempty"`     // unix-nano; set when poster disputes
	CompletedAt       int64            `json:"completed_at,omitempty"`
	// Task chain fields — set when this task is part of a subtask hierarchy.
	Tags         []string `json:"tags,omitempty"`
	ParentTaskID string   `json:"parent_task_id,omitempty"`
	SubtaskIDs   []string `json:"subtask_ids,omitempty"`
	IsSubtask    bool     `json:"is_subtask,omitempty"`
	// Routing fields — set by the autonomous task router.
	// RoutedTo is the AgentID the router has assigned this task to. When set,
	// only that agent may claim the task; other agents must wait until the
	// router assignment times out (60 seconds after posting).
	RoutedTo string `json:"routed_to,omitempty"`
	RoutedAt int64  `json:"routed_at,omitempty"`
}

// taskStore is the subset of store.Store used by TaskManager.
// Defining a local interface breaks the import cycle: store imports tasks for
// type information while tasks uses only this interface, not store directly.
type taskStore interface {
	PutTask(id string, data []byte) error
	GetTask(id string) ([]byte, error)
	AllTasks() (map[string][]byte, error)
}

// Stats holds aggregate marketplace statistics.
type Stats struct {
	TotalTasks     int    `json:"total_tasks"`
	OpenTasks      int    `json:"open_tasks"`
	ClaimedTasks   int    `json:"claimed_tasks"`
	SubmittedTasks int    `json:"submitted_tasks"`
	CompletedTasks int    `json:"completed_tasks"`
	DisputedTasks  int    `json:"disputed_tasks"`
	CancelledTasks int    `json:"cancelled_tasks"`
	TotalBudget    uint64 `json:"total_budget"`
}

// TaskManager manages the task marketplace lifecycle.
// It is safe for concurrent use by multiple goroutines.
type TaskManager struct {
	mu     sync.RWMutex
	tasks  map[string]*Task
	store  taskStore
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTaskManager returns a new empty TaskManager.
func NewTaskManager() *TaskManager {
	return &TaskManager{tasks: make(map[string]*Task)}
}

// SetStore attaches a persistence backend. After this call mutations
// write through to the store on every change.
func (m *TaskManager) SetStore(s taskStore) {
	m.store = s
}

// Start launches the background cleanup goroutine that archives old completed
// and cancelled tasks from the in-memory map to prevent unbounded growth.
// Tasks are persisted before eviction so they survive node restarts.
// Call Stop when the node is shutting down. Multiple Start calls are idempotent.
func (m *TaskManager) Start() {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return // already running
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel
	m.mu.Unlock()
	go m.cleanupLoop(ctx)
}

// Stop shuts down the background cleanup goroutine. It is a no-op if Start
// was not called or has already been stopped.
func (m *TaskManager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cleanupLoop runs the hourly archive sweep until ctx is cancelled.
func (m *TaskManager) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.archiveCompleted()
		}
	}
}

// archiveCompleted evicts completed or cancelled tasks older than MaxCompletedAge
// from the in-memory map. The tasks are already persisted to the store, so
// they can be recovered on the next restart via LoadFromStore.
// Must be called without m.mu held (acquires write lock internally).
func (m *TaskManager) archiveCompleted() {
	cutoff := time.Now().Add(-MaxCompletedAge)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, task := range m.tasks {
		if task.CompletedAt == 0 {
			continue // not yet completed/cancelled
		}
		if task.Status != TaskStatusCompleted && task.Status != TaskStatusCancelled {
			continue
		}
		if time.Unix(0, task.CompletedAt).Before(cutoff) {
			delete(m.tasks, id)
		}
	}
}

// LoadFromStore reconstructs tasks from a persisted store.
func (m *TaskManager) LoadFromStore(s taskStore) error {
	all, err := s.AllTasks()
	if err != nil {
		return fmt.Errorf("tasks: load from store: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, data := range all {
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			return fmt.Errorf("tasks: unmarshal task: %w", err)
		}
		m.tasks[task.ID] = &task
	}
	return nil
}

// PostTask creates a new task in Open state.
// Returns an error when title is empty, budget is 0, or posterID is empty.
func (m *TaskManager) PostTask(posterID, title, description, category string, budget uint64) (*Task, error) {
	if title == "" {
		return nil, fmt.Errorf("tasks: title required")
	}
	if budget == 0 {
		return nil, fmt.Errorf("tasks: budget must be > 0")
	}
	if posterID == "" {
		return nil, fmt.Errorf("tasks: poster_id required")
	}

	now := time.Now()
	task := &Task{
		ID:          generateTaskID(posterID, title, now),
		Title:       title,
		Description: description,
		Category:    category,
		PosterID:    posterID,
		Budget:      budget,
		Status:      TaskStatusOpen,
		PostedAt:    now.UnixNano(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[task.ID] = task
	m.persist(task)
	return task, nil
}

// ClaimTask assigns a task to claimerID. Returns ErrTaskNotFound if the task
// doesn't exist, ErrTaskAlreadyClaimed if already claimed, ErrTaskNotOpen
// if the task is in any other non-open state, or ErrSelfClaim if the claimer
// is the same agent that posted the task.
func (m *TaskManager) ClaimTask(taskID string, claimerID crypto.AgentID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	switch task.Status {
	case TaskStatusOpen:
		// OK to claim
	case TaskStatusClaimed:
		return fmt.Errorf("%w: %s", ErrTaskAlreadyClaimed, taskID)
	default:
		return fmt.Errorf("%w: %s (status: %s)", ErrTaskNotOpen, taskID, task.Status)
	}

	// C4: prevent poster from claiming their own task.
	if task.PosterID == string(claimerID) {
		return fmt.Errorf("%w: %s", ErrSelfClaim, taskID)
	}

	task.ClaimerID = string(claimerID)
	task.Status = TaskStatusClaimed
	now := time.Now()
	task.ClaimedAt = now.UnixNano()
	task.ClaimDeadline = now.Add(DefaultClaimDeadline).UnixNano()
	m.persist(task)
	return nil
}

// SetRoutedTo marks a task as assigned to a specific agent by the autonomous
// router. The task remains Open — the agent must still call ClaimTask to start
// work. Returns ErrTaskNotFound if absent, ErrTaskNotOpen if the task is not
// in Open state, or ErrTaskAlreadyRouted if routed to a different agent.
func (m *TaskManager) SetRoutedTo(taskID, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != TaskStatusOpen {
		return fmt.Errorf("%w: %s (status: %s)", ErrTaskNotOpen, taskID, task.Status)
	}
	if task.RoutedTo != "" && task.RoutedTo != agentID {
		return fmt.Errorf("%w: task %s already routed to %s", ErrTaskAlreadyRouted, taskID, task.RoutedTo)
	}

	task.RoutedTo = agentID
	task.RoutedAt = time.Now().UnixNano()
	m.persist(task)
	return nil
}

// SubmitResult records the worker's result for a claimed task.
// resultNote is a human-readable summary of the work; resultURI is an optional
// URI pointing to the full output. Returns ErrTaskNotFound, ErrTaskNotClaimed,
// or ErrWrongClaimer.
func (m *TaskManager) SubmitResult(taskID string, claimerID crypto.AgentID, resultHash, resultNote, resultURI string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != TaskStatusClaimed {
		return fmt.Errorf("%w: %s (status: %s)", ErrTaskNotClaimed, taskID, task.Status)
	}
	if task.ClaimerID != string(claimerID) {
		return fmt.Errorf("%w: %s", ErrWrongClaimer, taskID)
	}

	task.ResultHash = resultHash
	task.ResultNote = resultNote
	task.ResultURI = resultURI
	task.Status = TaskStatusSubmitted
	task.SubmittedAt = time.Now().UnixNano()
	m.persist(task)
	return nil
}

// SetVerificationScore stores the evidence quality score on a task.
// Returns ErrTaskNotFound when the task doesn't exist.
func (m *TaskManager) SetVerificationScore(taskID string, score *evidence.Score) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	task.VerificationScore = score
	m.persist(task)
	return nil
}

// ApproveTask marks a submitted task as completed. The approverID may be the
// original poster or an auto-validator. Returns ErrTaskNotFound or
// ErrTaskNotSubmitted.
func (m *TaskManager) ApproveTask(taskID string, approverID crypto.AgentID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != TaskStatusSubmitted {
		return fmt.Errorf("%w: %s (status: %s)", ErrTaskNotSubmitted, taskID, task.Status)
	}

	task.Status = TaskStatusCompleted
	task.CompletedAt = time.Now().UnixNano()
	m.persist(task)
	return nil
}

// ResolveDispute finalises a disputed task. approve=true sets status Completed
// (work accepted, escrow released to worker by caller); approve=false sets status
// Cancelled (work rejected, escrow refunded to poster by caller).
// Returns ErrTaskNotFound when absent, or an error when the task is not in Disputed state.
func (m *TaskManager) ResolveDispute(taskID string, resolverID crypto.AgentID, approve bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != TaskStatusDisputed {
		return fmt.Errorf("tasks: task %s is not in disputed state (status: %s)", taskID, task.Status)
	}

	if approve {
		task.Status = TaskStatusCompleted
		task.CompletedAt = time.Now().UnixNano()
	} else {
		task.Status = TaskStatusCancelled // rejected by dispute resolution
	}
	m.persist(task)
	return nil
}

// DisputeTask moves a submitted task into Disputed state. Only the poster may
// dispute. Returns ErrTaskNotFound, ErrTaskNotSubmitted, or ErrWrongPoster.
func (m *TaskManager) DisputeTask(taskID string, posterID crypto.AgentID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != TaskStatusSubmitted {
		return fmt.Errorf("%w: %s (status: %s)", ErrTaskNotSubmitted, taskID, task.Status)
	}
	if task.PosterID != string(posterID) {
		return fmt.Errorf("%w: %s", ErrWrongPoster, taskID)
	}

	task.Status = TaskStatusDisputed
	task.DisputedAt = time.Now().UnixNano()
	m.persist(task)
	return nil
}

// CancelTask cancels an open task. Only the poster may cancel an open task.
// Returns ErrTaskNotFound, ErrTaskNotOpen, or ErrWrongPoster.
func (m *TaskManager) CancelTask(taskID string, posterID crypto.AgentID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != TaskStatusOpen {
		return fmt.Errorf("%w: %s (status: %s)", ErrTaskNotOpen, taskID, task.Status)
	}
	if task.PosterID != string(posterID) {
		return fmt.Errorf("%w: %s", ErrWrongPoster, taskID)
	}

	task.Status = TaskStatusCancelled
	m.persist(task)
	return nil
}

// ReleaseTask resets a claimed task back to Open state when the claimer's
// deadline has passed without a submission. It clears the claimer identity and
// timestamps so the task can be claimed by another agent. The escrow bucket is
// intentionally left intact — the poster's funds remain locked for the task.
//
// Returns the former claimer's agent ID (for reputation penalty) and
// ErrTaskNotFound when the task doesn't exist. Returns an error if the task
// is not in Claimed status.
func (m *TaskManager) ReleaseTask(taskID string) (formerClaimerID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if task.Status != TaskStatusClaimed {
		return "", fmt.Errorf("tasks: task %s is not claimed (status: %s)", taskID, task.Status)
	}

	formerClaimerID = task.ClaimerID
	task.Status = TaskStatusOpen
	task.ClaimerID = ""
	task.ClaimedAt = 0
	task.ClaimDeadline = 0
	m.persist(task)
	return formerClaimerID, nil
}

// Get returns a copy of the task by ID. Returns ErrTaskNotFound when absent.
func (m *TaskManager) Get(taskID string) (*Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	cp := *task
	return &cp, nil
}

// Search returns tasks filtered by status and/or category, ordered by PostedAt
// descending (most recent first). Pass empty status to match all statuses.
// Pass limit=0 to return all matching tasks.
func (m *TaskManager) Search(status TaskStatus, category string, limit int) []*Task {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*Task
	for _, task := range m.tasks {
		if status != "" && task.Status != status {
			continue
		}
		if category != "" && task.Category != category {
			continue
		}
		cp := *task
		results = append(results, &cp)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].PostedAt > results[j].PostedAt
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

// OpenTasks returns all tasks in Open status, ordered by PostedAt descending.
func (m *TaskManager) OpenTasks(limit int) []*Task {
	return m.Search(TaskStatusOpen, "", limit)
}

// AgentTasks returns all tasks where agentID is either the poster or claimer,
// ordered by PostedAt descending.
func (m *TaskManager) AgentTasks(agentID string) []*Task {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*Task
	for _, task := range m.tasks {
		if task.PosterID == agentID || task.ClaimerID == agentID {
			cp := *task
			results = append(results, &cp)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].PostedAt > results[j].PostedAt
	})
	return results
}

// Stats returns aggregate marketplace statistics.
func (m *TaskManager) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var s Stats
	s.TotalTasks = len(m.tasks)
	for _, task := range m.tasks {
		s.TotalBudget += task.Budget
		switch task.Status {
		case TaskStatusOpen:
			s.OpenTasks++
		case TaskStatusClaimed:
			s.ClaimedTasks++
		case TaskStatusSubmitted:
			s.SubmittedTasks++
		case TaskStatusCompleted:
			s.CompletedTasks++
		case TaskStatusDisputed:
			s.DisputedTasks++
		case TaskStatusCancelled:
			s.CancelledTasks++
		}
	}
	return s
}

// CreateSubtask creates a child task on behalf of the claimer of an existing
// claimed task. The subtask budget is deducted from the parent's remaining
// budget. Only the current claimer of the parent task may create subtasks.
//
// Returns ErrTaskNotFound if the parent does not exist, ErrTaskNotClaimed if
// the parent is not in Claimed state, ErrNotClaimer if claimerID does not
// match the parent's claimer, or an error when the requested budget exceeds
// the parent's available budget.
func (m *TaskManager) CreateSubtask(parentTaskID string, claimerID crypto.AgentID, title, description, category string, budget uint64) (*Task, error) {
	if title == "" {
		return nil, fmt.Errorf("tasks: title required")
	}
	if budget == 0 {
		return nil, fmt.Errorf("tasks: budget must be > 0")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	parent, ok := m.tasks[parentTaskID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, parentTaskID)
	}
	if parent.Status != TaskStatusClaimed {
		return nil, fmt.Errorf("%w: %s (status: %s)", ErrTaskNotClaimed, parentTaskID, parent.Status)
	}
	if parent.ClaimerID != string(claimerID) {
		return nil, fmt.Errorf("%w: %s", ErrNotClaimer, parentTaskID)
	}
	if budget > parent.Budget {
		return nil, fmt.Errorf("tasks: subtask budget %d exceeds parent remaining budget %d", budget, parent.Budget)
	}

	now := time.Now()
	subtask := &Task{
		ID:           m.generateID(parentTaskID),
		Title:        title,
		Description:  description,
		Category:     category,
		PosterID:     string(claimerID),
		Budget:       budget,
		Status:       TaskStatusOpen,
		PostedAt:     now.UnixNano(),
		ParentTaskID: parentTaskID,
		IsSubtask:    true,
	}

	// Deduct subtask budget from parent and register the relationship.
	parent.Budget -= budget
	parent.SubtaskIDs = append(parent.SubtaskIDs, subtask.ID)

	m.tasks[subtask.ID] = subtask
	m.persist(subtask)
	m.persist(parent)
	return subtask, nil
}

// AllSubtasksComplete reports whether all subtasks of the given task have
// reached Completed status. Returns (true, nil) when the task has no
// subtasks (vacuously complete). Returns ErrTaskNotFound when the task does
// not exist.
func (m *TaskManager) AllSubtasksComplete(taskID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if len(task.SubtaskIDs) == 0 {
		return true, nil
	}
	for _, subID := range task.SubtaskIDs {
		sub, ok := m.tasks[subID]
		if !ok || sub.Status != TaskStatusCompleted {
			return false, nil
		}
	}
	return true, nil
}

// generateID creates a unique ID for a subtask based on the parent task ID,
// current nanosecond time, and the current task count. Returns 32 hex chars
// (16 bytes of SHA-256).
func (m *TaskManager) generateID(prefix string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", prefix, time.Now().UnixNano(), len(m.tasks))))
	return hex.EncodeToString(h[:16])
}

// persist serialises the task to the store. Must be called with m.mu held (write lock).
func (m *TaskManager) persist(task *Task) {
	if m.store == nil {
		return
	}
	data, err := json.Marshal(task)
	if err != nil {
		return
	}
	_ = m.store.PutTask(task.ID, data)
}

// generateTaskID creates a unique task ID from poster, title, and nanosecond time.
// Returns 32 hex characters (16 bytes of SHA-256).
func generateTaskID(posterID, title string, t time.Time) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", posterID, title, t.UnixNano())))
	return hex.EncodeToString(h[:16])
}
