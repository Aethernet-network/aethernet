package replay

import (
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ShouldReplay — deterministic branches
// ---------------------------------------------------------------------------

func TestShouldReplay_ChallengedTask(t *testing.T) {
	coord := NewReplayCoordinator(DefaultReplayPolicy(), newMemStore())

	ok, reason := coord.ShouldReplay("task-ch", "agent-1", "code", 0.90, false, true, nil, 50)
	if !ok {
		t.Error("expected ShouldReplay=true for challenged task")
	}
	if reason != "challenged" {
		t.Errorf("reason: want %q, got %q", "challenged", reason)
	}
}

func TestShouldReplay_AnomalyFlaggedTask(t *testing.T) {
	coord := NewReplayCoordinator(DefaultReplayPolicy(), newMemStore())

	ok, reason := coord.ShouldReplay("task-an", "agent-1", "code", 0.90, false, false, []string{"hash_mismatch"}, 50)
	if !ok {
		t.Error("expected ShouldReplay=true for anomaly-flagged task")
	}
	if reason != "anomaly" {
		t.Errorf("reason: want %q, got %q", "anomaly", reason)
	}
}

func TestShouldReplay_LowConfidence(t *testing.T) {
	coord := NewReplayCoordinator(DefaultReplayPolicy(), newMemStore())

	// Confidence 0.30 < threshold 0.50 → should replay.
	ok, reason := coord.ShouldReplay("task-lc", "agent-1", "code", 0.30, false, false, nil, 50)
	if !ok {
		t.Error("expected ShouldReplay=true when confidence below LowConfidenceThreshold")
	}
	if reason != "low_confidence" {
		t.Errorf("reason: want %q, got %q", "low_confidence", reason)
	}
}

func TestShouldReplay_NewAgent_ProbationaryRate(t *testing.T) {
	// Set NewAgentSampleRate=1.0 so the random roll always selects.
	policy := DefaultReplayPolicy()
	policy.NewAgentSampleRate = 1.0
	policy.AlwaysReplayChallenged = false
	policy.AlwaysReplayAnomalies = false
	// Confidence above threshold so only the new-agent branch fires.
	policy.LowConfidenceThreshold = 0.0

	coord := NewReplayCoordinator(policy, newMemStore())

	ok, reason := coord.ShouldReplay("task-na", "agent-1", "code", 0.90, false, false, nil, 5) // 5 < 10
	if !ok {
		t.Error("expected ShouldReplay=true for new agent with NewAgentSampleRate=1.0")
	}
	if reason != "probation" {
		t.Errorf("reason: want %q, got %q", "probation", reason)
	}
}

func TestShouldReplay_Deduplication(t *testing.T) {
	coord := NewReplayCoordinator(DefaultReplayPolicy(), newMemStore())

	// First call: challenged → (true, "challenged").
	ok, reason := coord.ShouldReplay("task-dup", "agent-1", "code", 0.90, false, true, nil, 50)
	if !ok || reason != "challenged" {
		t.Fatalf("expected (true, challenged) on first call, got (%v, %q)", ok, reason)
	}

	// Schedule it so the taskID is marked in the dedup map.
	if _, err := coord.ScheduleReplay("task-dup", "sha256:pkt", "code", "v1", nil, reason, "agent-1"); err != nil {
		t.Fatalf("ScheduleReplay: %v", err)
	}

	// Second call: same taskID → (false, "") regardless of challenged flag.
	ok2, reason2 := coord.ShouldReplay("task-dup", "agent-1", "code", 0.90, false, true, nil, 50)
	if ok2 {
		t.Errorf("expected ShouldReplay=false on second call for same taskID, got reason=%q", reason2)
	}
}

func TestShouldReplay_ReturnsFalseWhenNoConditionsMet(t *testing.T) {
	// All sample rates zero; no deterministic triggers.
	policy := ReplayPolicy{
		SampleRate:             0.0,
		NewAgentSampleRate:     0.0,
		GenerationSampleRate:   0.0,
		AlwaysReplayChallenged: false,
		AlwaysReplayAnomalies:  false,
		LowConfidenceThreshold: 0.50,
	}
	coord := NewReplayCoordinator(policy, newMemStore())

	// High confidence, established agent, no flags.
	ok, reason := coord.ShouldReplay("task-no", "agent-1", "code", 0.90, false, false, nil, 50)
	if ok {
		t.Errorf("expected ShouldReplay=false when no conditions met, got reason=%q", reason)
	}
}

// ---------------------------------------------------------------------------
// ScheduleReplay — persistence
// ---------------------------------------------------------------------------

func TestScheduleReplay_CreatesAndPersistsJob(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	reqs := fullReqs()
	job, err := coord.ScheduleReplay("task-sched", "sha256:pkt1", "code", "v1", reqs, "challenged", "agent-1")
	if err != nil {
		t.Fatalf("ScheduleReplay: %v", err)
	}
	if job == nil {
		t.Fatal("expected non-nil ReplayJob")
	}
	if job.ID == "" {
		t.Error("expected non-empty job ID")
	}
	if job.TaskID != "task-sched" {
		t.Errorf("TaskID: want %q, got %q", "task-sched", job.TaskID)
	}
	if job.ReplayReason != "challenged" {
		t.Errorf("ReplayReason: want %q, got %q", "challenged", job.ReplayReason)
	}
	if job.Status != "pending" {
		t.Errorf("Status: want %q, got %q", "pending", job.Status)
	}

	// Verify the job was persisted in the store.
	data, err := ms.GetReplayJob(job.ID)
	if err != nil {
		t.Fatalf("GetReplayJob after ScheduleReplay: %v", err)
	}
	var persisted ReplayJob
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal persisted job: %v", err)
	}
	if persisted.ID != job.ID {
		t.Errorf("persisted ID: want %q, got %q", job.ID, persisted.ID)
	}
}

// ---------------------------------------------------------------------------
// PendingJobs — filters by status
// ---------------------------------------------------------------------------

func TestPendingJobs_ReturnsOnlyPendingJobs(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	// Schedule two jobs — both start as "pending".
	j1, err := coord.ScheduleReplay("task-p1", "sha256:a", "code", "v1", nil, "challenged", "agent-1")
	if err != nil {
		t.Fatalf("ScheduleReplay task-p1: %v", err)
	}
	_, err = coord.ScheduleReplay("task-p2", "sha256:b", "code", "v1", nil, "anomaly", "agent-2")
	if err != nil {
		t.Fatalf("ScheduleReplay task-p2: %v", err)
	}

	// Manually flip j1 to "running" in the store.
	j1.Status = "running"
	updated, err := json.Marshal(j1)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := ms.PutReplayJob(j1.ID, updated); err != nil {
		t.Fatalf("PutReplayJob update: %v", err)
	}

	pending, err := coord.PendingJobs()
	if err != nil {
		t.Fatalf("PendingJobs: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("PendingJobs: want 1, got %d", len(pending))
	}
	if len(pending) > 0 && pending[0].TaskID != "task-p2" {
		t.Errorf("PendingJobs[0].TaskID: want %q, got %q", "task-p2", pending[0].TaskID)
	}
}

// ---------------------------------------------------------------------------
// DefaultReplayPolicy — sanity check
// ---------------------------------------------------------------------------

func TestDefaultReplayPolicy_SensibleValues(t *testing.T) {
	p := DefaultReplayPolicy()

	if p.SampleRate != 0.05 {
		t.Errorf("SampleRate: want 0.05, got %v", p.SampleRate)
	}
	if p.NewAgentSampleRate != 0.25 {
		t.Errorf("NewAgentSampleRate: want 0.25, got %v", p.NewAgentSampleRate)
	}
	if p.GenerationSampleRate != 0.15 {
		t.Errorf("GenerationSampleRate: want 0.15, got %v", p.GenerationSampleRate)
	}
	if !p.AlwaysReplayChallenged {
		t.Error("AlwaysReplayChallenged: want true")
	}
	if !p.AlwaysReplayAnomalies {
		t.Error("AlwaysReplayAnomalies: want true")
	}
	if p.LowConfidenceThreshold != 0.50 {
		t.Errorf("LowConfidenceThreshold: want 0.50, got %v", p.LowConfidenceThreshold)
	}
	// The default policy must grant a meaningful submission grace period so that
	// external replay executors have time to run checks and submit results before
	// InspectionExecutor processes the job as a fallback.
	if p.SubmissionGracePeriod != 2*time.Hour {
		t.Errorf("SubmissionGracePeriod: want 2h, got %v", p.SubmissionGracePeriod)
	}
}

// ---------------------------------------------------------------------------
// SubmissionGracePeriod — ScheduleReplay sets SubmissionDeadline
// ---------------------------------------------------------------------------

// TestScheduleReplay_SetsSubmissionDeadline verifies that when
// SubmissionGracePeriod > 0, ScheduleReplay sets a SubmissionDeadline on the
// created job equal to CreatedAt + SubmissionGracePeriod.
func TestScheduleReplay_SetsSubmissionDeadline(t *testing.T) {
	policy := DefaultReplayPolicy()
	policy.SubmissionGracePeriod = 30 * time.Minute

	ms := newMemStore()
	coord := NewReplayCoordinator(policy, ms)

	before := time.Now()
	job, err := coord.ScheduleReplay("task-dl", "sha256:x", "code", "v1", nil, "spot-check", "agent-dl")
	after := time.Now()
	if err != nil {
		t.Fatalf("ScheduleReplay: %v", err)
	}

	if job.SubmissionDeadline.IsZero() {
		t.Fatal("SubmissionDeadline must be set when SubmissionGracePeriod > 0")
	}

	// Deadline must be approximately CreatedAt + 30 minutes.
	minDeadline := before.Add(30 * time.Minute)
	maxDeadline := after.Add(30 * time.Minute)
	if job.SubmissionDeadline.Before(minDeadline) || job.SubmissionDeadline.After(maxDeadline) {
		t.Errorf("SubmissionDeadline = %v; want in [%v, %v]",
			job.SubmissionDeadline, minDeadline, maxDeadline)
	}

	// Verify deadline survives round-trip through the store.
	data, err := ms.GetReplayJob(job.ID)
	if err != nil {
		t.Fatalf("GetReplayJob: %v", err)
	}
	var persisted ReplayJob
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if persisted.SubmissionDeadline.IsZero() {
		t.Error("SubmissionDeadline must survive JSON round-trip through the store")
	}
	if !persisted.SubmissionDeadline.Equal(job.SubmissionDeadline.Truncate(time.Second)) &&
		!persisted.SubmissionDeadline.Equal(job.SubmissionDeadline) {
		// Allow sub-second precision loss in JSON.
		diff := persisted.SubmissionDeadline.Sub(job.SubmissionDeadline)
		if diff < -time.Second || diff > time.Second {
			t.Errorf("SubmissionDeadline changed during round-trip: original=%v persisted=%v",
				job.SubmissionDeadline, persisted.SubmissionDeadline)
		}
	}
}

// TestScheduleReplay_ZeroGracePeriod_NoDeadline verifies that when
// SubmissionGracePeriod is zero, no SubmissionDeadline is set on the job
// (InspectionExecutor processes immediately — legacy / testnet mode).
func TestScheduleReplay_ZeroGracePeriod_NoDeadline(t *testing.T) {
	policy := DefaultReplayPolicy()
	policy.SubmissionGracePeriod = 0

	ms := newMemStore()
	coord := NewReplayCoordinator(policy, ms)

	job, err := coord.ScheduleReplay("task-no-dl", "sha256:y", "code", "v1", nil, "spot-check", "agent-no-dl")
	if err != nil {
		t.Fatalf("ScheduleReplay: %v", err)
	}
	if !job.SubmissionDeadline.IsZero() {
		t.Errorf("SubmissionDeadline = %v; want zero (no grace period configured)", job.SubmissionDeadline)
	}
}
