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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
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

// Sentinel errors returned by TaskManager methods.
var (
	ErrTaskNotFound       = errors.New("tasks: task not found")
	ErrTaskAlreadyClaimed = errors.New("tasks: task already claimed")
	ErrTaskNotClaimed     = errors.New("tasks: task not claimed")
	ErrTaskNotSubmitted   = errors.New("tasks: task not submitted")
	ErrTaskNotOpen        = errors.New("tasks: task is not open")
	ErrWrongClaimer       = errors.New("tasks: not the claimer")
	ErrWrongPoster        = errors.New("tasks: not the poster")
)

// Task is a unit of work posted to the marketplace.
type Task struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Category    string     `json:"category"`
	PosterID    string     `json:"poster_id"`
	ClaimerID   string     `json:"claimer_id,omitempty"`
	Budget      uint64     `json:"budget"`
	Status      TaskStatus `json:"status"`
	ResultHash  string     `json:"result_hash,omitempty"`
	PostedAt    int64      `json:"posted_at"`
	ClaimedAt   int64      `json:"claimed_at,omitempty"`
	SubmittedAt int64      `json:"submitted_at,omitempty"`
	CompletedAt int64      `json:"completed_at,omitempty"`
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
	mu    sync.RWMutex
	tasks map[string]*Task
	store taskStore
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
// doesn't exist, ErrTaskAlreadyClaimed if already claimed, or ErrTaskNotOpen
// if the task is in any other non-open state.
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

	task.ClaimerID = string(claimerID)
	task.Status = TaskStatusClaimed
	task.ClaimedAt = time.Now().UnixNano()
	m.persist(task)
	return nil
}

// SubmitResult records the worker's result hash for a claimed task.
// Returns ErrTaskNotFound, ErrTaskNotClaimed, or ErrWrongClaimer.
func (m *TaskManager) SubmitResult(taskID string, claimerID crypto.AgentID, resultHash string) error {
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
	task.Status = TaskStatusSubmitted
	task.SubmittedAt = time.Now().UnixNano()
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
