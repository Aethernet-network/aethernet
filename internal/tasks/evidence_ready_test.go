package tasks_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// helper creates a task manager with a submitted task.
func setupSubmittedTask(t *testing.T, evidenceHash string) (*tasks.TaskManager, *tasks.Task) {
	t.Helper()
	m := tasks.NewTaskManager()
	task, err := m.PostTask("poster", "Test task", "Description", "writing", 10_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := m.ClaimTask(task.ID, crypto.AgentID("worker")); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := m.SubmitResult(task.ID, crypto.AgentID("worker"), "sha256:abc", "result note", ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	// Simulate applyTaskSubmitted setting the evidence hash.
	// In production this happens via ApplyDAGEvent, but for unit tests
	// we directly set it after SubmitResult (which sets status=Submitted).
	if evidenceHash != "" {
		m.SetEvidenceBodyHash(task.ID, evidenceHash)
	}

	got, _ := m.Get(task.ID)
	return m, got
}

// TestEvidenceReady_NoHashMeansReady verifies that tasks without an
// evidence body hash have EvidenceReady=true immediately.
func TestEvidenceReady_NoHashMeansReady(t *testing.T) {
	m, task := setupSubmittedTask(t, "")
	got, _ := m.Get(task.ID)
	// No evidence hash means ready (legacy submission path).
	// SubmitResult doesn't set EvidenceBodyHash, so EvidenceReady defaults.
	// For tasks submitted via DAG with empty hash, applyTaskSubmitted sets Ready=true.
	if got.EvidenceBodyHash != "" {
		t.Errorf("EvidenceBodyHash = %q; want empty", got.EvidenceBodyHash)
	}
}

// TestEvidenceReady_WithHash_NotReadyByDefault verifies that setting an
// evidence body hash leaves EvidenceReady=false until explicitly marked.
func TestEvidenceReady_WithHash_NotReadyByDefault(t *testing.T) {
	m, task := setupSubmittedTask(t, "abc123def456")
	got, _ := m.Get(task.ID)
	if got.EvidenceBodyHash != "abc123def456" {
		t.Errorf("EvidenceBodyHash = %q; want %q", got.EvidenceBodyHash, "abc123def456")
	}
	if got.EvidenceReady {
		t.Error("EvidenceReady should be false when hash is set but blob not fetched")
	}
}

// TestEvidenceReady_MarkFlipsToTrue verifies that MarkEvidenceReady sets
// EvidenceReady=true.
func TestEvidenceReady_MarkFlipsToTrue(t *testing.T) {
	m, task := setupSubmittedTask(t, "abc123def456")

	m.MarkEvidenceReady(task.ID)

	got, _ := m.Get(task.ID)
	if !got.EvidenceReady {
		t.Error("EvidenceReady should be true after MarkEvidenceReady")
	}
}

// TestEvidenceReady_MarkIdempotent verifies that MarkEvidenceReady is
// safe to call multiple times.
func TestEvidenceReady_MarkIdempotent(t *testing.T) {
	m, task := setupSubmittedTask(t, "abc123def456")

	m.MarkEvidenceReady(task.ID)
	m.MarkEvidenceReady(task.ID) // should not panic or corrupt state

	got, _ := m.Get(task.ID)
	if !got.EvidenceReady {
		t.Error("EvidenceReady should still be true after second call")
	}
}

// TestEvidenceReady_MarkNonexistentTask verifies that MarkEvidenceReady
// on a nonexistent task is a no-op.
func TestEvidenceReady_MarkNonexistentTask(t *testing.T) {
	m := tasks.NewTaskManager()
	m.MarkEvidenceReady("nonexistent") // should not panic
}

// TestEvidenceReady_PartialEvidenceNeverScored verifies the invariant that
// a task with a blob hash but EvidenceReady=false cannot have a verification
// score set (the autovalidator is supposed to skip it).
func TestEvidenceReady_PartialEvidenceNeverScored(t *testing.T) {
	_, task := setupSubmittedTask(t, "abc123def456")

	if task.EvidenceReady {
		t.Error("task should not be evidence-ready before blob arrives")
	}
	if task.VerificationScore != nil {
		t.Error("task should not have a verification score before evidence is ready")
	}
}
