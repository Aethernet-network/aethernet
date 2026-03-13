package replay

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeTaskMgr implements taskReplayInterface for tests.
type fakeTaskMgr struct {
	status    map[string]string
	jobID     map[string]string
	genStatus map[string]string
	setErr    error
}

func newFakeTaskMgr() *fakeTaskMgr {
	return &fakeTaskMgr{
		status:    make(map[string]string),
		jobID:     make(map[string]string),
		genStatus: make(map[string]string),
	}
}

func (f *fakeTaskMgr) SetReplayStatus(taskID, status, jobID string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.status[taskID] = status
	f.jobID[taskID] = jobID
	return nil
}

func (f *fakeTaskMgr) SetGenerationStatus(taskID, status string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.genStatus[taskID] = status
	return nil
}

// fakeGenTrigger implements generationTrigger for tests.
type fakeGenTrigger struct {
	calls []genCall
	err   error
}

type genCall struct {
	taskID, agentID, resultHash, title string
	value                               uint64
}

func (g *fakeGenTrigger) RecordGeneration(taskID, agentID, resultHash, title string, value uint64) error {
	g.calls = append(g.calls, genCall{taskID: taskID, agentID: agentID, resultHash: resultHash, title: title, value: value})
	return g.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeEnforcer(t *testing.T) (*ReplayEnforcer, *fakeTaskMgr, *fakeGenTrigger, *memStore) {
	t.Helper()
	ms := newMemStore()
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enf := NewReplayEnforcer(tm, resolver, gen)
	return enf, tm, gen, ms
}

func makeCompleteOutcome(jobID, taskID, status string, comparisons []CheckComparison, anomalyFlags []string) *ReplayOutcome {
	return &ReplayOutcome{
		JobID:        jobID,
		TaskID:       taskID,
		Status:       status,
		Comparisons:  comparisons,
		AnomalyFlags: anomalyFlags,
		ReplayedAt:   time.Now(),
		ReplayerID:   "test-replayer",
	}
}

// ---------------------------------------------------------------------------
// ProcessReplayOutcome — verdict processing
// ---------------------------------------------------------------------------

// TestEnforcer_CleanMatch_ReplayComplete verifies that a clean match produces
// "replay_complete" status and triggers the generation credit.
func TestEnforcer_CleanMatch_ReplayComplete(t *testing.T) {
	enf, tm, gen, ms := makeEnforcer(t)

	// Pre-populate a job so RecordOutcome can update it.
	job := makeCleanJob("job-ef-1", "task-ef-1")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	outcome := makeCompleteOutcome("job-ef-1", "task-ef-1", "match",
		[]CheckComparison{
			{CheckType: "go_test", Match: true},
			{CheckType: "lint", Match: true},
		}, nil)

	verdict, err := enf.ProcessReplayOutcome(outcome, "agent-1", "sha256:hash1", "Build tests", 8_000)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if verdict.Action != "no_action" {
		t.Errorf("verdict.Action = %q; want %q", verdict.Action, "no_action")
	}
	if tm.status["task-ef-1"] != "replay_complete" {
		t.Errorf("task status = %q; want %q", tm.status["task-ef-1"], "replay_complete")
	}
	if tm.genStatus["task-ef-1"] != "recognized" {
		t.Errorf("generation status = %q; want %q", tm.genStatus["task-ef-1"], "recognized")
	}
	// Generation credit should have been released.
	if len(gen.calls) != 1 {
		t.Fatalf("generation trigger calls = %d; want 1", len(gen.calls))
	}
	if gen.calls[0].taskID != "task-ef-1" {
		t.Errorf("gen.taskID = %q; want %q", gen.calls[0].taskID, "task-ef-1")
	}
	if gen.calls[0].value != 8_000 {
		t.Errorf("gen.value = %d; want 8000", gen.calls[0].value)
	}
}

// TestEnforcer_MultipleMismatches_ReplayDisputed verifies that
// "open_challenge" verdict sets "replay_disputed" and withholds generation.
func TestEnforcer_MultipleMismatches_ReplayDisputed(t *testing.T) {
	enf, tm, gen, ms := makeEnforcer(t)

	job := makeCleanJob("job-ef-2", "task-ef-2")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	outcome := makeCompleteOutcome("job-ef-2", "task-ef-2", "partial_match",
		[]CheckComparison{
			{CheckType: "go_test", Match: false, ScoreDelta: 0.10},
			{CheckType: "lint", Match: false, ScoreDelta: 0.12},
		}, nil)

	verdict, err := enf.ProcessReplayOutcome(outcome, "agent-2", "sha256:hash2", "Run linter", 5_000)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if verdict.Action != "open_challenge" {
		t.Errorf("verdict.Action = %q; want %q", verdict.Action, "open_challenge")
	}
	if tm.status["task-ef-2"] != "replay_disputed" {
		t.Errorf("task status = %q; want %q", tm.status["task-ef-2"], "replay_disputed")
	}
	if tm.genStatus["task-ef-2"] != "denied" {
		t.Errorf("generation status = %q; want %q", tm.genStatus["task-ef-2"], "denied")
	}
	// Generation credit must NOT be released for a disputed task.
	if len(gen.calls) != 0 {
		t.Errorf("generation trigger should not be called for disputed tasks, got %d calls", len(gen.calls))
	}
}

// TestEnforcer_SlashRecommended_ReplayDisputed verifies that
// "slash_recommended" also results in "replay_disputed".
func TestEnforcer_SlashRecommended_ReplayDisputed(t *testing.T) {
	enf, tm, gen, ms := makeEnforcer(t)

	job := makeCleanJob("job-ef-3", "task-ef-3")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	// status "mismatch" + anomaly flag → severity ≥ 0.9 → slash_recommended
	outcome := makeCompleteOutcome("job-ef-3", "task-ef-3", "mismatch",
		[]CheckComparison{
			{CheckType: "go_test", Match: false},
			{CheckType: "lint", Match: false},
		}, []string{"hash_divergence"})

	verdict, err := enf.ProcessReplayOutcome(outcome, "agent-3", "sha256:hash3", "Check hashes", 4_000)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if verdict.Action != "slash_recommended" {
		t.Errorf("verdict.Action = %q; want %q", verdict.Action, "slash_recommended")
	}
	if tm.status["task-ef-3"] != "replay_disputed" {
		t.Errorf("task status = %q; want %q", tm.status["task-ef-3"], "replay_disputed")
	}
	if tm.genStatus["task-ef-3"] != "denied" {
		t.Errorf("generation status = %q; want %q", tm.genStatus["task-ef-3"], "denied")
	}
	if len(gen.calls) != 0 {
		t.Errorf("generation trigger should not fire for slash_recommended, got %d calls", len(gen.calls))
	}
}

// TestEnforcer_FlagForReview_ReplayComplete verifies that "flag_for_review"
// results in "replay_complete" and triggers generation.
func TestEnforcer_FlagForReview_ReplayComplete(t *testing.T) {
	enf, tm, gen, ms := makeEnforcer(t)

	job := makeCleanJob("job-ef-4", "task-ef-4")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	// single small mismatch → flag_for_review
	outcome := makeCompleteOutcome("job-ef-4", "task-ef-4", "partial_match",
		[]CheckComparison{
			{CheckType: "go_test", Match: true},
			{CheckType: "lint", Match: false, ScoreDelta: 0.08},
		}, nil)

	verdict, err := enf.ProcessReplayOutcome(outcome, "agent-4", "sha256:hash4", "Lint check", 3_000)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if verdict.Action != "flag_for_review" {
		t.Errorf("verdict.Action = %q; want %q", verdict.Action, "flag_for_review")
	}
	if tm.status["task-ef-4"] != "replay_complete" {
		t.Errorf("task status = %q; want %q", tm.status["task-ef-4"], "replay_complete")
	}
	if tm.genStatus["task-ef-4"] != "recognized" {
		t.Errorf("generation status = %q; want %q", tm.genStatus["task-ef-4"], "recognized")
	}
	if len(gen.calls) != 1 {
		t.Errorf("generation trigger calls = %d; want 1 for flag_for_review", len(gen.calls))
	}
}

// TestEnforcer_NilOutcome_Error verifies that a nil outcome returns an error.
func TestEnforcer_NilOutcome_Error(t *testing.T) {
	enf, _, _, _ := makeEnforcer(t)
	_, err := enf.ProcessReplayOutcome(nil, "agent-x", "", "", 0)
	if err == nil {
		t.Error("expected error for nil outcome")
	}
}

// TestEnforcer_SetReplayStatusError_Propagates verifies that a
// SetReplayStatus failure is returned to the caller.
func TestEnforcer_SetReplayStatusError_Propagates(t *testing.T) {
	ms := newMemStore()
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	sentinelErr := errors.New("db full")
	tm.setErr = sentinelErr
	enf := NewReplayEnforcer(tm, resolver, nil)

	job := makeCleanJob("job-ef-5", "task-ef-5")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	outcome := makeCompleteOutcome("job-ef-5", "task-ef-5", "match",
		[]CheckComparison{{CheckType: "go_test", Match: true}}, nil)

	_, err := enf.ProcessReplayOutcome(outcome, "agent-5", "", "", 0)
	if !errors.Is(err, sentinelErr) {
		t.Errorf("expected sentinelErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers used only in this file
// ---------------------------------------------------------------------------

func makeCleanJob(jobID, taskID string) *ReplayJob {
	return &ReplayJob{
		ID:     jobID,
		TaskID: taskID,
		Status: "pending",
	}
}
