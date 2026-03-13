package tasks_test

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// TestSetReplayStatus_PersistsFields verifies that SetReplayStatus stores both
// the status and the job ID on the task.
func TestSetReplayStatus_PersistsFields(t *testing.T) {
	m := tasks.NewTaskManager()
	task, err := m.PostTask("alice", "Run tests", "go test ./...", "code", 10_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	if err := m.SetReplayStatus(task.ID, "replay_pending", "job-abc-123"); err != nil {
		t.Fatalf("SetReplayStatus: %v", err)
	}

	got, err := m.Get(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ReplayStatus != "replay_pending" {
		t.Errorf("ReplayStatus = %q; want %q", got.ReplayStatus, "replay_pending")
	}
	if got.ReplayJobID != "job-abc-123" {
		t.Errorf("ReplayJobID = %q; want %q", got.ReplayJobID, "job-abc-123")
	}
}

// TestSetReplayStatus_UpdatesToComplete verifies that the status transitions
// from "replay_pending" to "replay_complete".
func TestSetReplayStatus_UpdatesToComplete(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("bob", "Analyse data", "CSV analysis", "data", 5_000)

	_ = m.SetReplayStatus(task.ID, "replay_pending", "job-1")
	if err := m.SetReplayStatus(task.ID, "replay_complete", "job-1"); err != nil {
		t.Fatalf("SetReplayStatus (complete): %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.ReplayStatus != "replay_complete" {
		t.Errorf("ReplayStatus = %q; want %q", got.ReplayStatus, "replay_complete")
	}
}

// TestSetReplayStatus_UpdatesToDisputed verifies the "replay_disputed" status.
func TestSetReplayStatus_UpdatesToDisputed(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("carol", "Write docs", "API docs", "writing", 7_000)

	_ = m.SetReplayStatus(task.ID, "replay_pending", "job-2")
	if err := m.SetReplayStatus(task.ID, "replay_disputed", "job-2"); err != nil {
		t.Fatalf("SetReplayStatus (disputed): %v", err)
	}

	got, _ := m.Get(task.ID)
	if got.ReplayStatus != "replay_disputed" {
		t.Errorf("ReplayStatus = %q; want %q", got.ReplayStatus, "replay_disputed")
	}
}

// TestSetReplayStatus_NotFound verifies that ErrTaskNotFound is returned for
// an unknown task ID.
func TestSetReplayStatus_NotFound(t *testing.T) {
	m := tasks.NewTaskManager()
	err := m.SetReplayStatus("nonexistent-id", "replay_pending", "job-x")
	if !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Errorf("expected ErrTaskNotFound, got %v", err)
	}
}

// TestReplayFields_DefaultEmpty verifies that new tasks have empty replay fields.
func TestReplayFields_DefaultEmpty(t *testing.T) {
	m := tasks.NewTaskManager()
	task, _ := m.PostTask("dave", "Test task", "desc", "code", 1_000)

	if task.ReplayStatus != "" {
		t.Errorf("ReplayStatus should be empty on new task, got %q", task.ReplayStatus)
	}
	if task.ReplayJobID != "" {
		t.Errorf("ReplayJobID should be empty on new task, got %q", task.ReplayJobID)
	}
}
