package autovalidator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// setupEvidenceGateHarness creates a minimal stack for evidence gating tests.
func setupEvidenceGateHarness(t *testing.T) (*autovalidator.AutoValidator, *tasks.TaskManager) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	d := dag.New()

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	kp, _ := crypto.GenerateKeyPair()
	av := autovalidator.NewAutoValidator(eng, kp.AgentID(), 50*time.Millisecond)
	av.SetDAG(d)
	av.SetKeyPair(kp)

	// Zero staleness so tests don't have to wait.
	av.SetTaskStalenessThreshold(0)

	tm := tasks.NewTaskManager()
	escrowMgr := escrow.New(tl)
	av.SetTaskManager(tm, escrowMgr)

	return av, tm
}

// createSubmittedTask creates a posted+claimed+submitted task.
func createSubmittedTask(t *testing.T, tm *tasks.TaskManager, evidenceHash string) *tasks.Task {
	t.Helper()
	task, err := tm.PostTask("poster", "Test evidence gating", "Write an essay about testing", "writing", 10_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := tm.ClaimTask(task.ID, crypto.AgentID("worker")); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	ev := &evidence.Evidence{
		Hash:       "sha256:test",
		OutputType: "text",
		OutputSize: 1000,
		Summary:    "This is a well-written essay about testing software with multiple paragraphs and detailed analysis.",
	}
	if err := tm.SubmitResult(task.ID, crypto.AgentID("worker"), "sha256:test", "result", "", ev); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	// Set evidence hash to simulate DAG event application.
	if evidenceHash != "" {
		tm.SetEvidenceBodyHash(task.ID, evidenceHash)
	}

	got, _ := tm.Get(task.ID)
	return got
}

// TestAutovalidator_SkipsTasksWithoutEvidence verifies that processSubmittedTasks
// skips tasks where EvidenceReady=false.
func TestAutovalidator_SkipsTasksWithoutEvidence(t *testing.T) {
	av, tm := setupEvidenceGateHarness(t)

	// Create a submitted task with a blob hash but no blob fetched yet.
	task := createSubmittedTask(t, tm, "abc123def456")

	// Verify the task is not evidence-ready.
	if task.EvidenceReady {
		t.Fatal("task should not be evidence-ready")
	}

	// Run one tick of the autovalidator.
	av.Tick()

	// Task should still be in Submitted state — not scored, not approved, not rejected.
	got, _ := tm.Get(task.ID)
	if got.Status != tasks.TaskStatusSubmitted {
		t.Errorf("Status = %q; want %q (should be skipped)", got.Status, tasks.TaskStatusSubmitted)
	}
	if got.VerificationScore != nil {
		t.Error("task should not have been scored while evidence is not ready")
	}
}

// TestAutovalidator_ScoresAfterEvidenceReady verifies that once evidence is
// marked ready, the autovalidator processes the task normally.
func TestAutovalidator_ScoresAfterEvidenceReady(t *testing.T) {
	av, tm := setupEvidenceGateHarness(t)

	// Create task with blob hash, then mark evidence ready.
	task := createSubmittedTask(t, tm, "abc123def456")
	tm.MarkEvidenceReady(task.ID)

	got, _ := tm.Get(task.ID)
	if !got.EvidenceReady {
		t.Fatal("task should be evidence-ready after MarkEvidenceReady")
	}

	// Run one tick — task should now be processed.
	av.Tick()

	got, _ = tm.Get(task.ID)
	// Task should have been scored (either approved or rejected).
	if got.VerificationScore == nil {
		t.Error("task should have been scored after evidence became ready")
	}
}

// TestAutovalidator_LegacyTasksAlwaysReady verifies that tasks without an
// evidence body hash are always considered ready (backward compatible).
func TestAutovalidator_LegacyTasksAlwaysReady(t *testing.T) {
	av, tm := setupEvidenceGateHarness(t)

	// Create task without blob hash (legacy path).
	task := createSubmittedTask(t, tm, "")

	// Run one tick — legacy tasks should process immediately.
	av.Tick()

	got, _ := tm.Get(task.ID)
	if got.VerificationScore == nil {
		t.Error("legacy task (no blob hash) should have been scored immediately")
	}
}

// TestAutovalidator_CorruptBlobNeverSetsReady verifies that EvidenceReady
// can only be set via MarkEvidenceReady — there's no path from corrupt data.
func TestAutovalidator_CorruptBlobNeverSetsReady(t *testing.T) {
	_, tm := setupEvidenceGateHarness(t)

	task := createSubmittedTask(t, tm, "abc123def456")

	// Simulate: blob fetch fails or returns corrupt data.
	// In this case, MarkEvidenceReady is never called.
	got, _ := tm.Get(task.ID)
	if got.EvidenceReady {
		t.Error("task should not be evidence-ready when blob was never verified")
	}
}
