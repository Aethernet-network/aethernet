package autovalidator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// waitForGenerationStatusChange polls until the task's GenerationStatus is NOT
// the given status (i.e. it has changed), or the deadline passes.
func waitForGenerationStatusChange(t *testing.T, tm *tasks.TaskManager, taskID, fromStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(taskID)
		if tk != nil && tk.GenerationStatus != fromStatus {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// buildStuckHeldHarness creates a completed task in stuck-held state:
//   - GenerationStatus = "held" (as if settleTask set it before ScheduleReplay failed)
//   - ReplayJobID = "" (ScheduleReplay never ran or failed)
//
// Returns the AutoValidator, TaskManager, and the stuck task's ID.
func buildStuckHeldHarness(t *testing.T) (*autovalidator.AutoValidator, *tasks.TaskManager, string) {
	t.Helper()

	posterID := crypto.AgentID("poster-sh")
	claimerID := crypto.AgentID("worker-sh")
	validatorID := crypto.AgentID("validator-sh")
	const budget = 200_000

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	gl := ledger.NewGenerationLedger()
	esc := escrow.New(tl)
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	tm := tasks.NewTaskManager()
	task, err := tm.PostTask(string(posterID), "Stuck task", "desc", "code", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:result", "done", ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	// Approve the task to reach Completed status.
	if err := tm.ApproveTask(task.ID, validatorID); err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}

	// Manually inject the stuck-held condition: set GenerationStatus="held"
	// without a ReplayJobID (simulating ScheduleReplay failure).
	if err := tm.SetGenerationStatus(task.ID, "held"); err != nil {
		t.Fatalf("SetGenerationStatus: %v", err)
	}

	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetTaskStalenessThreshold(0)

	return av, tm, task.ID
}

// TestAutoValidator_StuckHeld_ResetsGenerationStatus verifies that a completed
// task with GenerationStatus="held" and no ReplayJobID has its GenerationStatus
// reset to "" by the auto-validator's stuck-held recovery sweep.
func TestAutoValidator_StuckHeld_ResetsGenerationStatus(t *testing.T) {
	av, tm, taskID := buildStuckHeldHarness(t)

	av.Start()
	defer av.Stop()

	// Wait for the GenerationStatus to change from "held".
	waitForGenerationStatusChange(t, tm, taskID, "held", 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.GenerationStatus != "" {
		t.Errorf("GenerationStatus = %q; want empty after stuck-held recovery", tk.GenerationStatus)
	}
}

// TestAutoValidator_StuckHeld_IgnoresHealthyHeld verifies that a task with
// GenerationStatus="held" AND a non-empty ReplayJobID is NOT touched by the
// stuck-held recovery sweep (it has a legitimate pending replay job).
func TestAutoValidator_StuckHeld_IgnoresHealthyHeld(t *testing.T) {
	av, tm, taskID := buildStuckHeldHarness(t)

	// Inject a non-empty ReplayJobID to simulate a healthy held task.
	if err := tm.SetReplayStatus(taskID, "replay_pending", "job-healthy-1"); err != nil {
		t.Fatalf("SetReplayStatus: %v", err)
	}

	av.Start()
	defer av.Stop()

	// Let the autovalidator run several ticks.
	time.Sleep(200 * time.Millisecond)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	// GenerationStatus must still be "held" — recovery must not reset it.
	if tk.GenerationStatus != "held" {
		t.Errorf("GenerationStatus = %q; want %q (healthy held must not be reset)", tk.GenerationStatus, "held")
	}
}
