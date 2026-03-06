package tasks_test

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// ---------------------------------------------------------------------------
// TestPostTask
// ---------------------------------------------------------------------------

func TestPostTask(t *testing.T) {
	m := tasks.NewTaskManager()
	task, err := m.PostTask("alice", "Write a poem", "Haiku about Go", "creative", 5_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if task.ID == "" {
		t.Error("ID must not be empty")
	}
	if task.Title != "Write a poem" {
		t.Errorf("Title = %q; want %q", task.Title, "Write a poem")
	}
	if task.PosterID != "alice" {
		t.Errorf("PosterID = %q; want %q", task.PosterID, "alice")
	}
	if task.Budget != 5_000 {
		t.Errorf("Budget = %d; want 5000", task.Budget)
	}
	if task.Status != tasks.TaskStatusOpen {
		t.Errorf("Status = %q; want %q", task.Status, tasks.TaskStatusOpen)
	}
	if task.PostedAt == 0 {
		t.Error("PostedAt must be non-zero")
	}
}

func TestPostTask_EmptyTitle(t *testing.T) {
	m := tasks.NewTaskManager()
	_, err := m.PostTask("alice", "", "desc", "cat", 5_000)
	if err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestPostTask_ZeroBudget(t *testing.T) {
	m := tasks.NewTaskManager()
	_, err := m.PostTask("alice", "Do something", "desc", "cat", 0)
	if err == nil {
		t.Fatal("expected error for zero budget")
	}
}

func TestPostTask_EmptyPosterID(t *testing.T) {
	m := tasks.NewTaskManager()
	_, err := m.PostTask("", "Do something", "desc", "cat", 5_000)
	if err == nil {
		t.Fatal("expected error for empty poster_id")
	}
}

// ---------------------------------------------------------------------------
// TestClaimTask
// ---------------------------------------------------------------------------

func TestClaimTask(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Classify images", "", "ml", 10_000)

	if err := m.ClaimTask(task.ID, "bob"); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.Status != tasks.TaskStatusClaimed {
		t.Errorf("Status = %q; want claimed", got.Status)
	}
	if got.ClaimerID != "bob" {
		t.Errorf("ClaimerID = %q; want bob", got.ClaimerID)
	}
	if got.ClaimedAt == 0 {
		t.Error("ClaimedAt must be non-zero after claiming")
	}
}

func TestClaimTask_NotFound(t *testing.T) {
	m := tasks.NewTaskManager()
	err := m.ClaimTask("nonexistent", "bob")
	if !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Errorf("expected ErrTaskNotFound; got %v", err)
	}
}

func TestClaimTask_AlreadyClaimed(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Do work", "", "general", 1_000)
	_ = m.ClaimTask(task.ID, "bob")

	err := m.ClaimTask(task.ID, "charlie")
	if !errors.Is(err, tasks.ErrTaskAlreadyClaimed) {
		t.Errorf("expected ErrTaskAlreadyClaimed; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestSubmitResult
// ---------------------------------------------------------------------------

func TestSubmitResult(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Summarize text", "", "nlp", 2_000)
	_ = m.ClaimTask(task.ID, "bob")

	if err := m.SubmitResult(task.ID, "bob", "sha256:abc123", "", ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.Status != tasks.TaskStatusSubmitted {
		t.Errorf("Status = %q; want submitted", got.Status)
	}
	if got.ResultHash != "sha256:abc123" {
		t.Errorf("ResultHash = %q; want sha256:abc123", got.ResultHash)
	}
	if got.SubmittedAt == 0 {
		t.Error("SubmittedAt must be non-zero after submitting")
	}
}

func TestSubmitResult_WrongClaimer(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Translate", "", "nlp", 3_000)
	_ = m.ClaimTask(task.ID, "bob")

	err := m.SubmitResult(task.ID, "charlie", "hash", "", "")
	if !errors.Is(err, tasks.ErrWrongClaimer) {
		t.Errorf("expected ErrWrongClaimer; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestApproveTask
// ---------------------------------------------------------------------------

func TestApproveTask(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Generate code", "", "code", 8_000)
	_ = m.ClaimTask(task.ID, "bob")
	_ = m.SubmitResult(task.ID, "bob", "sha256:result", "", "")

	if err := m.ApproveTask(task.ID, "alice"); err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.Status != tasks.TaskStatusCompleted {
		t.Errorf("Status = %q; want completed", got.Status)
	}
	if got.CompletedAt == 0 {
		t.Error("CompletedAt must be non-zero after approval")
	}
}

func TestApproveTask_NotSubmitted(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Build model", "", "ml", 5_000)
	_ = m.ClaimTask(task.ID, "bob")

	// Task is claimed, not submitted
	err := m.ApproveTask(task.ID, "alice")
	if !errors.Is(err, tasks.ErrTaskNotSubmitted) {
		t.Errorf("expected ErrTaskNotSubmitted; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestDisputeTask
// ---------------------------------------------------------------------------

func TestDisputeTask(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Run inference", "", "ml", 4_000)
	_ = m.ClaimTask(task.ID, "bob")
	_ = m.SubmitResult(task.ID, "bob", "hash", "", "")

	if err := m.DisputeTask(task.ID, "alice"); err != nil {
		t.Fatalf("DisputeTask: %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.Status != tasks.TaskStatusDisputed {
		t.Errorf("Status = %q; want disputed", got.Status)
	}
}

func TestDisputeTask_WrongPoster(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Evaluate model", "", "ml", 3_000)
	_ = m.ClaimTask(task.ID, "bob")
	_ = m.SubmitResult(task.ID, "bob", "hash", "", "")

	err := m.DisputeTask(task.ID, "charlie")
	if !errors.Is(err, tasks.ErrWrongPoster) {
		t.Errorf("expected ErrWrongPoster; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCancelTask
// ---------------------------------------------------------------------------

func TestCancelTask(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Fine-tune model", "", "ml", 20_000)

	if err := m.CancelTask(task.ID, "alice"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.Status != tasks.TaskStatusCancelled {
		t.Errorf("Status = %q; want cancelled", got.Status)
	}
}

func TestCancelTask_NotPoster(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("alice", "Label data", "", "data", 1_000)

	err := m.CancelTask(task.ID, "bob")
	if !errors.Is(err, tasks.ErrWrongPoster) {
		t.Errorf("expected ErrWrongPoster; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestStats
// ---------------------------------------------------------------------------

func TestStats(t *testing.T) {
	m := tasks.NewTaskManager()

	// Post 3 tasks
	t1, _ := m.PostTask("alice", "Task 1", "", "cat", 1_000)
	t2, _ := m.PostTask("alice", "Task 2", "", "cat", 2_000)
	t3, _ := m.PostTask("alice", "Task 3", "", "cat", 3_000)

	// Claim t2, submit, complete
	_ = m.ClaimTask(t2.ID, "bob")
	_ = m.SubmitResult(t2.ID, "bob", "hash", "", "")
	_ = m.ApproveTask(t2.ID, "alice")

	// Cancel t3
	_ = m.CancelTask(t3.ID, "alice")

	// t1 stays open; t2 completed; t3 cancelled
	s := m.Stats()
	if s.TotalTasks != 3 {
		t.Errorf("TotalTasks = %d; want 3", s.TotalTasks)
	}
	if s.OpenTasks != 1 {
		t.Errorf("OpenTasks = %d; want 1", s.OpenTasks)
	}
	if s.CompletedTasks != 1 {
		t.Errorf("CompletedTasks = %d; want 1", s.CompletedTasks)
	}
	if s.CancelledTasks != 1 {
		t.Errorf("CancelledTasks = %d; want 1", s.CancelledTasks)
	}
	// TotalBudget = 1000 + 2000 + 3000 = 6000
	if s.TotalBudget != 6_000 {
		t.Errorf("TotalBudget = %d; want 6000", s.TotalBudget)
	}

	_ = t1 // suppress unused warning
}
