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
	"github.com/Aethernet-network/aethernet/internal/replay"
	"github.com/Aethernet-network/aethernet/internal/store"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// openTempStore opens a BadgerDB-backed store in a temporary directory.
// The store is closed automatically when the test ends.
func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// alwaysReplayPolicy returns a ReplayPolicy that schedules every task for
// replay deterministically (no randomness needed: LowConfidenceThreshold=1.0
// triggers replay whenever confidence < 1.0, which is always).
func alwaysReplayPolicy() replay.ReplayPolicy {
	return replay.ReplayPolicy{
		SampleRate:             0.0, // random sampling disabled
		NewAgentSampleRate:     0.0,
		GenerationSampleRate:   0.0,
		LowConfidenceThreshold: 1.0, // always below threshold
	}
}

// neverReplayPolicy returns a policy that never schedules replay,
// useful for verifying backward-compat behaviour.
func neverReplayPolicy() replay.ReplayPolicy {
	return replay.ReplayPolicy{
		SampleRate:             0.0,
		NewAgentSampleRate:     0.0,
		GenerationSampleRate:   0.0,
		LowConfidenceThreshold: 0.0, // confidence always ≥ threshold
		AlwaysReplayChallenged: false,
		AlwaysReplayAnomalies:  false,
	}
}

// buildAVHarness creates the minimal stack for testing AutoValidator
// task settlement. The task is generation-eligible by default.
func buildAVHarness(t *testing.T, budget uint64, genEligible bool) (
	av *autovalidator.AutoValidator,
	tm *tasks.TaskManager,
	gl *ledger.GenerationLedger,
	taskID string,
) {
	t.Helper()

	posterID := crypto.AgentID("poster")
	claimerID := crypto.AgentID("worker")
	validatorID := crypto.AgentID("testnet-validator")

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	gl = ledger.NewGenerationLedger()
	esc := escrow.New(tl)
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	tm = tasks.NewTaskManager()

	falseVal := false
	trueVal := true
	var opts tasks.PostTaskOpts
	if genEligible {
		opts.GenerationEligible = &trueVal
	} else {
		opts.GenerationEligible = &falseVal
	}

	task, err := tm.PostTask(string(posterID), "Analyse dataset", "CSV analysis research task", "research", budget, opts)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	// Rich result note that passes the evidence verifier threshold.
	resultNote := "The CSV analysis research task has been completed. Data analysis was performed on the dataset. Results include statistical summaries, trend analysis, and comprehensive research findings."
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:csvdata", resultNote, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	av = autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetGenerationLedger(gl)
	av.SetTaskStalenessThreshold(0)

	return av, tm, gl, task.ID
}

// waitForCompleted polls until the task reaches Completed status or the
// deadline passes.
func waitForCompleted(t *testing.T, tm *tasks.TaskManager, taskID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(taskID)
		if tk != nil && tk.Status == tasks.TaskStatusCompleted {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	tk, _ := tm.Get(taskID)
	status := tasks.TaskStatus("unknown")
	if tk != nil {
		status = tk.Status
	}
	t.Fatalf("task did not reach Completed within %v; status = %q", timeout, status)
}

// waitForReplayStatus polls until the task's ReplayStatus is non-empty or the
// deadline passes. Used to avoid a race between settleTask completing and
// ScheduleReplay persisting the replay job ID.
func waitForReplayStatus(t *testing.T, tm *tasks.TaskManager, taskID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(taskID)
		if tk != nil && tk.ReplayStatus != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Not fatal — caller's assertion will fail with a useful message.
}

// waitForGeneration polls until the generation ledger has at least one entry
// for agentID or the deadline passes. Used because ApproveTask (which makes the
// task Completed) runs before RecordTaskGeneration in settleTask, creating a
// window where the task is Completed but the ledger is still empty.
func waitForGeneration(gl *ledger.GenerationLedger, agentID crypto.AgentID, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, _ := gl.GenerationHistory(agentID, 1, 0)
		if len(entries) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAutoValidator_GenerationHeld_WhenReplaySelectedAndGenerationEligible
// verifies that when the replay coordinator selects a generation-eligible task
// for replay, the generation ledger entry is withheld (holdGeneration=true).
func TestAutoValidator_GenerationHeld_WhenReplaySelectedAndGenerationEligible(t *testing.T) {
	const budget = 500_000
	claimerID := crypto.AgentID("worker")

	av, tm, gl, taskID := buildAVHarness(t, budget, true /* genEligible */)

	// Wire a coordinator that always schedules replay.
	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(alwaysReplayPolicy(), s)
	av.SetReplayCoordinator(coord)

	av.Start()
	defer av.Stop()

	// Wait for replay status to be set — ScheduleReplay runs AFTER settleTask,
	// so we must wait beyond Completed state.
	waitForReplayStatus(t, tm, taskID, 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.Status != tasks.TaskStatusCompleted {
		t.Fatalf("task status = %q; want Completed", tk.Status)
	}

	// Generation ledger must be empty — the entry is held pending replay.
	entries, err := gl.GenerationHistory(claimerID, 10, 0)
	if err != nil {
		t.Fatalf("GenerationHistory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("generation ledger has %d entries; want 0 (held pending replay)", len(entries))
	}

	// Replay fields must be set.
	if tk.ReplayStatus != "replay_pending" {
		t.Errorf("ReplayStatus = %q; want %q", tk.ReplayStatus, "replay_pending")
	}
	if tk.ReplayJobID == "" {
		t.Error("ReplayJobID must be non-empty after scheduling")
	}
}

// TestAutoValidator_GenerationNotHeld_WhenReplaySelectedButNotGenEligible
// verifies that when a replay is scheduled for a non-generation-eligible task,
// settlement (including generation ledger) still proceeds normally.
func TestAutoValidator_GenerationNotHeld_WhenReplaySelectedButNotGenEligible(t *testing.T) {
	const budget = 500_000
	claimerID := crypto.AgentID("worker")

	av, tm, gl, taskID := buildAVHarness(t, budget, false /* not genEligible */)

	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(alwaysReplayPolicy(), s)
	av.SetReplayCoordinator(coord)

	av.Start()
	defer av.Stop()

	// Wait for replay status to be set.
	waitForReplayStatus(t, tm, taskID, 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.Status != tasks.TaskStatusCompleted {
		t.Fatalf("task status = %q; want Completed", tk.Status)
	}

	// For non-generation-eligible tasks the generation ledger still records.
	// Wait for the generation entry — ApproveTask runs before RecordTaskGeneration.
	waitForGeneration(gl, claimerID, 2*time.Second)
	entries, err := gl.GenerationHistory(claimerID, 10, 0)
	if err != nil {
		t.Fatalf("GenerationHistory: %v", err)
	}
	if len(entries) == 0 {
		t.Error("generation ledger is empty; expected entry for non-eligible task with replay")
	}

	// Replay is still scheduled even for non-eligible tasks.
	if tk.ReplayStatus != "replay_pending" {
		t.Errorf("ReplayStatus = %q; want %q", tk.ReplayStatus, "replay_pending")
	}
}

// TestAutoValidator_NoReplayCoordinator_BackwardCompat verifies that when no
// replay coordinator is wired, settlement is unchanged: generation ledger is
// populated, ReplayStatus is empty.
func TestAutoValidator_NoReplayCoordinator_BackwardCompat(t *testing.T) {
	const budget = 500_000
	claimerID := crypto.AgentID("worker")

	av, tm, gl, taskID := buildAVHarness(t, budget, true /* genEligible */)
	// No coordinator wired.

	av.Start()
	defer av.Stop()

	// Wait for the generation entry — ApproveTask runs before RecordTaskGeneration.
	waitForGeneration(gl, claimerID, 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.Status != tasks.TaskStatusCompleted {
		t.Fatalf("task status = %q; want Completed", tk.Status)
	}

	// Generation ledger must have an entry when no coordinator is set.
	entries, err := gl.GenerationHistory(claimerID, 10, 0)
	if err != nil {
		t.Fatalf("GenerationHistory: %v", err)
	}
	if len(entries) == 0 {
		t.Error("generation ledger is empty; expected entry when no replay coordinator")
	}

	if tk.ReplayStatus != "" {
		t.Errorf("ReplayStatus = %q; want empty when no coordinator", tk.ReplayStatus)
	}
}
