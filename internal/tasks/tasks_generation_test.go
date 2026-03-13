package tasks_test

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// TestSetGenerationStatus_PersistsField verifies that SetGenerationStatus
// stores the status on the task.
func TestSetGenerationStatus_PersistsField(t *testing.T) {
	m := tasks.NewTaskManager()
	task, err := m.PostTask("alice", "Analyse data", "CSV analysis", "data", 10_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	if err := m.SetGenerationStatus(task.ID, "recognized"); err != nil {
		t.Fatalf("SetGenerationStatus: %v", err)
	}

	got, err := m.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GenerationStatus != "recognized" {
		t.Errorf("GenerationStatus = %q; want %q", got.GenerationStatus, "recognized")
	}
}

// TestSetGenerationStatus_Held verifies the "held" status.
func TestSetGenerationStatus_Held(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("bob", "Research task", "research", "research", 5_000)

	if err := m.SetGenerationStatus(task.ID, "held"); err != nil {
		t.Fatalf("SetGenerationStatus: %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.GenerationStatus != "held" {
		t.Errorf("GenerationStatus = %q; want %q", got.GenerationStatus, "held")
	}
}

// TestSetGenerationStatus_HeldToDenied verifies the "held" → "denied"
// transition (replay dispute path).
func TestSetGenerationStatus_HeldToDenied(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("carol", "ML training", "train model", "code", 20_000)

	_ = m.SetGenerationStatus(task.ID, "held")
	if err := m.SetGenerationStatus(task.ID, "denied"); err != nil {
		t.Fatalf("SetGenerationStatus (denied): %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.GenerationStatus != "denied" {
		t.Errorf("GenerationStatus = %q; want %q", got.GenerationStatus, "denied")
	}
}

// TestSetGenerationStatus_HeldToRecognized verifies the "held" → "recognized"
// transition (successful replay path).
func TestSetGenerationStatus_HeldToRecognized(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("dave", "Write docs", "API docs", "writing", 8_000)

	_ = m.SetGenerationStatus(task.ID, "held")
	if err := m.SetGenerationStatus(task.ID, "recognized"); err != nil {
		t.Fatalf("SetGenerationStatus (recognized): %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.GenerationStatus != "recognized" {
		t.Errorf("GenerationStatus = %q; want %q", got.GenerationStatus, "recognized")
	}
}

// TestSetGenerationStatus_NotFound verifies ErrTaskNotFound for unknown task.
func TestSetGenerationStatus_NotFound(t *testing.T) {
	m := tasks.NewTaskManager()
	err := m.SetGenerationStatus("no-such-task", "recognized")
	if !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Errorf("expected ErrTaskNotFound, got %v", err)
	}
}

// TestGenerationStatus_DefaultEmpty verifies that new tasks have empty
// GenerationStatus.
func TestGenerationStatus_DefaultEmpty(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("eve", "Empty task", "desc", "code", 1_000)

	if task.GenerationStatus != "" {
		t.Errorf("GenerationStatus should be empty on new task, got %q", task.GenerationStatus)
	}
}

// TestGetDisputedTasks_ReturnsOnlyDisputed verifies that GetDisputedTasks only
// returns tasks with ReplayStatus "replay_disputed".
func TestGetDisputedTasks_ReturnsOnlyDisputed(t *testing.T) {
	m := tasks.NewTaskManager()

	// Create three tasks with different replay statuses.
	t1, _ := m.PostTask("poster", "Task disputed", "desc", "code", 1_000)
	t2, _ := m.PostTask("poster", "Task complete", "desc", "data", 1_000)
	t3, _ := m.PostTask("poster", "Task pending", "desc", "writing", 1_000)

	_ = m.SetReplayStatus(t1.ID, "replay_disputed", "job-d-1")
	_ = m.SetReplayStatus(t2.ID, "replay_complete", "job-c-1")
	_ = m.SetReplayStatus(t3.ID, "replay_pending", "job-p-1")

	disputed := m.GetDisputedTasks()
	if len(disputed) != 1 {
		t.Fatalf("GetDisputedTasks length = %d; want 1", len(disputed))
	}
	if disputed[0].ID != t1.ID {
		t.Errorf("GetDisputedTasks[0].ID = %q; want %q", disputed[0].ID, t1.ID)
	}
}

// TestGetDisputedTasks_EmptyWhenNone verifies an empty slice when no tasks
// are disputed.
func TestGetDisputedTasks_EmptyWhenNone(t *testing.T) {
	m := tasks.NewTaskManager()
	m.PostTask("poster", "Normal task", "desc", "code", 1_000)

	disputed := m.GetDisputedTasks()
	if len(disputed) != 0 {
		t.Errorf("GetDisputedTasks = %v; want empty", disputed)
	}
}

// TestIsCleanlyFinalized_CleanCompleted verifies that a completed task with
// no replay or generation concerns is cleanly finalized.
func TestIsCleanlyFinalized_CleanCompleted(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("poster", "Clean task", "desc", "code", 1_000)
	// Manually drive to completed via ClaimTask + SubmitResult + ApproveTask.
	_ = m.ClaimTask(task.ID, "worker-1")
	_ = m.SubmitResult(task.ID, "worker-1", "sha256:abc", "done", "")
	// Simulate approval by the auto-validator.
	_ = m.ApproveTask(task.ID, "validator")

	ok, err := m.IsCleanlyFinalized(task.ID)
	if err != nil {
		t.Fatalf("IsCleanlyFinalized: %v", err)
	}
	if !ok {
		t.Error("IsCleanlyFinalized = false; want true for clean completed task")
	}
}

// TestIsCleanlyFinalized_ReplayComplete verifies true when replay completed
// cleanly.
func TestIsCleanlyFinalized_ReplayComplete(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("poster", "Replay task", "desc", "research", 2_000)
	_ = m.ClaimTask(task.ID, "worker-2")
	_ = m.SubmitResult(task.ID, "worker-2", "sha256:def", "done", "")
	_ = m.ApproveTask(task.ID, "validator")
	_ = m.SetReplayStatus(task.ID, "replay_complete", "job-rc")
	_ = m.SetGenerationStatus(task.ID, "recognized")

	ok, err := m.IsCleanlyFinalized(task.ID)
	if err != nil {
		t.Fatalf("IsCleanlyFinalized: %v", err)
	}
	if !ok {
		t.Error("IsCleanlyFinalized = false; want true for replay_complete + recognized")
	}
}

// TestIsCleanlyFinalized_ReplayPending verifies false when replay is pending.
func TestIsCleanlyFinalized_ReplayPending(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("poster", "Pending replay task", "desc", "code", 3_000)
	_ = m.ClaimTask(task.ID, "worker-3")
	_ = m.SubmitResult(task.ID, "worker-3", "sha256:ghi", "done", "")
	_ = m.ApproveTask(task.ID, "validator")
	_ = m.SetReplayStatus(task.ID, "replay_pending", "job-rp")

	ok, err := m.IsCleanlyFinalized(task.ID)
	if err != nil {
		t.Fatalf("IsCleanlyFinalized: %v", err)
	}
	if ok {
		t.Error("IsCleanlyFinalized = true; want false for replay_pending task")
	}
}

// TestIsCleanlyFinalized_ReplayDisputed verifies false when task is disputed.
func TestIsCleanlyFinalized_ReplayDisputed(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("poster", "Disputed task", "desc", "data", 4_000)
	_ = m.ClaimTask(task.ID, "worker-4")
	_ = m.SubmitResult(task.ID, "worker-4", "sha256:jkl", "done", "")
	_ = m.ApproveTask(task.ID, "validator")
	_ = m.SetReplayStatus(task.ID, "replay_disputed", "job-rd")

	ok, err := m.IsCleanlyFinalized(task.ID)
	if err != nil {
		t.Fatalf("IsCleanlyFinalized: %v", err)
	}
	if ok {
		t.Error("IsCleanlyFinalized = true; want false for replay_disputed task")
	}
}

// TestIsCleanlyFinalized_GenerationDenied verifies false when generation was
// denied (replay mismatch).
func TestIsCleanlyFinalized_GenerationDenied(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("poster", "Denied gen task", "desc", "code", 5_000)
	_ = m.ClaimTask(task.ID, "worker-5")
	_ = m.SubmitResult(task.ID, "worker-5", "sha256:mno", "done", "")
	_ = m.ApproveTask(task.ID, "validator")
	_ = m.SetReplayStatus(task.ID, "replay_disputed", "job-gd")
	_ = m.SetGenerationStatus(task.ID, "denied")

	ok, err := m.IsCleanlyFinalized(task.ID)
	if err != nil {
		t.Fatalf("IsCleanlyFinalized: %v", err)
	}
	if ok {
		t.Error("IsCleanlyFinalized = true; want false for denied generation status")
	}
}

// TestIsCleanlyFinalized_GenerationHeld verifies false when generation is held.
func TestIsCleanlyFinalized_GenerationHeld(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("poster", "Held gen task", "desc", "research", 6_000)
	_ = m.ClaimTask(task.ID, "worker-6")
	_ = m.SubmitResult(task.ID, "worker-6", "sha256:pqr", "done", "")
	_ = m.ApproveTask(task.ID, "validator")
	_ = m.SetGenerationStatus(task.ID, "held")

	ok, err := m.IsCleanlyFinalized(task.ID)
	if err != nil {
		t.Fatalf("IsCleanlyFinalized: %v", err)
	}
	if ok {
		t.Error("IsCleanlyFinalized = true; want false for held generation status")
	}
}

// TestIsCleanlyFinalized_NotCompleted verifies false when task is not completed.
func TestIsCleanlyFinalized_NotCompleted(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("poster", "Open task", "desc", "code", 1_000)

	ok, err := m.IsCleanlyFinalized(task.ID)
	if err != nil {
		t.Fatalf("IsCleanlyFinalized: %v", err)
	}
	if ok {
		t.Error("IsCleanlyFinalized = true; want false for non-completed task")
	}
}

// TestIsCleanlyFinalized_NotFound verifies ErrTaskNotFound for unknown tasks.
func TestIsCleanlyFinalized_NotFound(t *testing.T) {
	m := tasks.NewTaskManager()
	_, err := m.IsCleanlyFinalized("no-such-task")
	if !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Errorf("expected ErrTaskNotFound, got %v", err)
	}
}
