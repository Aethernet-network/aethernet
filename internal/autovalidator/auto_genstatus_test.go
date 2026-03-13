package autovalidator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/replay"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// waitForGenerationStatus polls until the task's GenerationStatus matches
// wantStatus or the deadline passes. Callers should check the field after this
// returns to get a useful error message.
func waitForGenerationStatus(t *testing.T, tm *tasks.TaskManager, taskID, wantStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(taskID)
		if tk != nil && tk.GenerationStatus == wantStatus {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAutoValidator_GenerationStatus_RecognizedOnDirectSettle verifies that
// when a generation-eligible task passes verification with no replay scheduled,
// GenerationStatus is set to "recognized" after the generation credit is issued.
func TestAutoValidator_GenerationStatus_RecognizedOnDirectSettle(t *testing.T) {
	const budget = 500_000
	claimerID := crypto.AgentID("worker")

	av, tm, gl, taskID := buildAVHarness(t, budget, true /* genEligible */)

	// Never-replay policy: settleTask runs with holdGeneration=false, so
	// RecordTaskGeneration is called and GenerationStatus becomes "recognized".
	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(neverReplayPolicy(), s)
	av.SetReplayCoordinator(coord)

	av.Start()
	defer av.Stop()

	waitForCompleted(t, tm, taskID, 2*time.Second)
	waitForGeneration(gl, claimerID, 2*time.Second)
	waitForGenerationStatus(t, tm, taskID, "recognized", 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.GenerationStatus != "recognized" {
		t.Errorf("GenerationStatus = %q; want %q", tk.GenerationStatus, "recognized")
	}
}

// TestAutoValidator_GenerationStatus_HeldOnReplayScheduled verifies that when
// the replay coordinator selects a generation-eligible task for replay,
// GenerationStatus is set to "held" (credit withheld pending replay outcome).
func TestAutoValidator_GenerationStatus_HeldOnReplayScheduled(t *testing.T) {
	const budget = 500_000

	av, tm, _, taskID := buildAVHarness(t, budget, true /* genEligible */)

	// Always-replay policy: settleTask runs with holdGeneration=true for
	// generation-eligible tasks → GenerationStatus becomes "held".
	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(alwaysReplayPolicy(), s)
	av.SetReplayCoordinator(coord)

	av.Start()
	defer av.Stop()

	// Wait for replay scheduling (which happens after settleTask, so
	// GenerationStatus must already be "held" by this point).
	waitForReplayStatus(t, tm, taskID, 2*time.Second)
	waitForGenerationStatus(t, tm, taskID, "held", 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.GenerationStatus != "held" {
		t.Errorf("GenerationStatus = %q; want %q", tk.GenerationStatus, "held")
	}
}

// TestAutoValidator_GenerationStatus_EmptyForNonGenEligible verifies that a
// task that is NOT generation-eligible has an empty GenerationStatus after
// settlement (the generation block is never entered).
func TestAutoValidator_GenerationStatus_EmptyForNonGenEligible(t *testing.T) {
	const budget = 500_000

	av, tm, _, taskID := buildAVHarness(t, budget, false /* genEligible = false */)

	// Never-replay policy so we get a clean direct settlement.
	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(neverReplayPolicy(), s)
	av.SetReplayCoordinator(coord)

	av.Start()
	defer av.Stop()

	waitForCompleted(t, tm, taskID, 2*time.Second)
	// Give the auto-validator one tick to possibly set generation status.
	time.Sleep(100 * time.Millisecond)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.GenerationStatus != "" {
		t.Errorf("GenerationStatus = %q; want empty for non-generation-eligible task", tk.GenerationStatus)
	}
}
