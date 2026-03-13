package autovalidator_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/replay"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// richResultNote is a verbose result that passes the evidence quality threshold.
const richResultNote = "The CSV analysis research task has been completed. Data analysis was performed on the dataset. Results include statistical summaries, trend analysis, and comprehensive research findings."

// buildAVHarnessWithChecks creates the same stack as buildAVHarness, but
// the task is posted with explicit RequiredChecks in the AcceptanceContract.
// The store is also returned so callers can look up replay jobs.
func buildAVHarnessWithChecks(t *testing.T, budget uint64, requiredChecks []string) (
	av *autovalidator.AutoValidator,
	tm *tasks.TaskManager,
	taskID string,
) {
	t.Helper()

	posterID := crypto.AgentID("poster-rc")
	claimerID := crypto.AgentID("worker-rc")
	validatorID := crypto.AgentID("testnet-validator")

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

	tm = tasks.NewTaskManager()

	trueVal := true
	task, err := tm.PostTask(string(posterID), "Analyse dataset", richResultNote, "research", budget, tasks.PostTaskOpts{
		GenerationEligible: &trueVal,
		RequiredChecks:     requiredChecks,
	})
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:csvdata", richResultNote, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	av = autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetGenerationLedger(gl)
	av.SetTaskStalenessThreshold(0)

	return av, tm, task.ID
}

// TestAutoValidator_ReplayJob_HasChecksToReplay verifies that when a task
// has RequiredChecks set in its AcceptanceContract, the scheduled replay job
// has its ChecksToReplay field populated from those checks.
func TestAutoValidator_ReplayJob_HasChecksToReplay(t *testing.T) {
	const budget = 500_000
	requiredChecks := []string{"has_output", "hash_valid"}

	av, tm, taskID := buildAVHarnessWithChecks(t, budget, requiredChecks)

	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(alwaysReplayPolicy(), s)
	av.SetReplayCoordinator(coord)

	av.Start()
	defer av.Stop()

	waitForReplayStatus(t, tm, taskID, 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.ReplayJobID == "" {
		t.Fatal("ReplayJobID must be non-empty after replay scheduling")
	}

	// Fetch the replay job from the store and verify ChecksToReplay.
	jobData, err := s.GetReplayJob(tk.ReplayJobID)
	if err != nil {
		t.Fatalf("GetReplayJob(%s): %v", tk.ReplayJobID, err)
	}
	var job replay.ReplayJob
	if err := json.Unmarshal(jobData, &job); err != nil {
		t.Fatalf("unmarshal job: %v", err)
	}

	if len(job.ChecksToReplay) != len(requiredChecks) {
		t.Fatalf("ChecksToReplay length = %d; want %d", len(job.ChecksToReplay), len(requiredChecks))
	}
	for i, want := range requiredChecks {
		if job.ChecksToReplay[i] != want {
			t.Errorf("ChecksToReplay[%d] = %q; want %q", i, job.ChecksToReplay[i], want)
		}
	}
}

// TestAutoValidator_ReplayJob_HasAcceptanceContractHash verifies that when a
// task has a non-empty SpecHash committed in its AcceptanceContract, the replay
// job is scheduled with a non-empty context (SpecHash is in the reqs passed to
// ScheduleReplay even though the job struct does not carry it directly).
// This test verifies the scheduling proceeds without error and the job exists.
func TestAutoValidator_ReplayJob_HasAcceptanceContractHash(t *testing.T) {
	const budget = 500_000

	av, tm, taskID := buildAVHarnessWithChecks(t, budget, []string{"lint"})

	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(alwaysReplayPolicy(), s)
	av.SetReplayCoordinator(coord)

	av.Start()
	defer av.Stop()

	waitForReplayStatus(t, tm, taskID, 2*time.Second)

	tk, err := tm.Get(taskID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.ReplayJobID == "" {
		t.Fatal("ReplayJobID must be set")
	}

	// Verify task.Contract.SpecHash was computed (non-empty) — this is what
	// gets threaded into AcceptanceContractHash of the ReplayRequirements.
	if tk.Contract.SpecHash == "" {
		t.Error("Contract.SpecHash should be non-empty for tasks with RequiredChecks")
	}
}

// TestAutoValidator_AgentTaskCount_UsesRegistry verifies that when the identity
// registry is wired, the real TasksCompleted count is used by ShouldReplay instead
// of the hardcoded zero.
//
// Setup: experienced claimer with 15 completed tasks in the registry.
// Policy: NewAgentSampleRate=1.0 (probationary rate) but SampleRate=0.0
// Expected outcome: since count=15 > 10, the probationary branch is skipped
// and the baseline SampleRate=0.0 applies → no replay is scheduled.
// If count were hardcoded to 0, probation would trigger every time → replay_pending.
func TestAutoValidator_AgentTaskCount_UsesRegistry(t *testing.T) {
	const budget = 500_000
	claimerID := crypto.AgentID("worker-experienced")
	posterID := crypto.AgentID("poster-experienced")
	validatorID := crypto.AgentID("testnet-validator")

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

	// Register the claimer so RecordTaskCompletion does not fail.
	if err := reg.Register(&identity.CapabilityFingerprint{
		AgentID:     claimerID,
		DisplayName: "experienced-worker",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Credit 15 completed tasks so agentTaskCount > 10 in the probation check.
	for i := 0; i < 15; i++ {
		if err := reg.RecordTaskCompletion(claimerID, 10_000, "research"); err != nil {
			t.Fatalf("RecordTaskCompletion: %v", err)
		}
	}

	tm := tasks.NewTaskManager()
	trueVal := true
	task, err := tm.PostTask(string(posterID), "Analyse dataset", richResultNote, "research", budget, tasks.PostTaskOpts{
		GenerationEligible: &trueVal,
	})
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:csvdata", richResultNote, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	// Policy: probation-only sampling (rate=1.0), zero baseline sampling.
	// An agent with count≥10 skips probation and sees only SampleRate=0.0.
	probationOnlyPolicy := replay.ReplayPolicy{
		SampleRate:             0.0,
		NewAgentSampleRate:     1.0, // 100% if in probation
		GenerationSampleRate:   0.0,
		LowConfidenceThreshold: 0.0, // never triggered by confidence
		AlwaysReplayChallenged: false,
		AlwaysReplayAnomalies:  false,
	}

	s := openTempStore(t)
	coord := replay.NewReplayCoordinator(probationOnlyPolicy, s)

	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetGenerationLedger(gl)
	av.SetTaskStalenessThreshold(0)
	av.SetReplayCoordinator(coord)
	av.SetRegistry(reg) // wire the identity registry so agentTaskCount is real

	av.Start()
	defer av.Stop()

	// Wait for the task to complete (may or may not schedule replay).
	waitForCompleted(t, tm, task.ID, 2*time.Second)
	// Give the auto-validator one more tick to potentially call ScheduleReplay.
	time.Sleep(100 * time.Millisecond)

	tk, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	// With count=15 (experienced agent), probation is skipped, SampleRate=0.0 →
	// no replay should be scheduled.
	if tk.ReplayStatus != "" {
		t.Errorf("ReplayStatus = %q; want empty (experienced agent, probation skipped)", tk.ReplayStatus)
	}
}
