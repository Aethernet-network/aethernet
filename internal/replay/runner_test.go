package replay

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// fakeExecutor implements ReplayExecutor for tests.
type fakeExecutor struct {
	outcome *ReplayOutcome
	err     error
}

func (f *fakeExecutor) Execute(_ context.Context, job *ReplayJob) (*ReplayOutcome, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.outcome != nil {
		return f.outcome, nil
	}
	// Default: return a match outcome.
	return &ReplayOutcome{
		JobID:      job.ID,
		TaskID:     job.TaskID,
		Status:     "match",
		ReplayedAt: time.Now(),
		ReplayerID: "fake-executor",
	}, nil
}

// fakeDetailsProvider implements TaskDetailsProvider for tests.
type fakeDetailsProvider struct {
	agentID            string
	resultHash         string
	title              string
	verifiedValue      uint64
	generationEligible bool
	err                error
}

func (f *fakeDetailsProvider) GetReplayDetails(taskID string) (string, string, string, uint64, bool, error) {
	return f.agentID, f.resultHash, f.title, f.verifiedValue, f.generationEligible, f.err
}

// TestReplayRunner_ProcessesPendingJob verifies that the runner loads a pending
// job, runs the executor, and submits the outcome to the enforcer.
func TestReplayRunner_ProcessesPendingJob(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	// Pre-populate a pending job in the store.
	job := makeCleanJob("job-rr-1", "task-rr-1")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enf := NewReplayEnforcer(tm, resolver, gen)

	details := &fakeDetailsProvider{
		agentID:            "worker-1",
		generationEligible: true,
		verifiedValue:      5_000,
	}
	ex := &fakeExecutor{} // returns match outcome by default

	runner := NewReplayRunner(coord, ex, enf, details, time.Second)
	runner.processOnce()

	// The job should now be completed (enforcer calls RecordOutcome → completed).
	allData, err := ms.AllReplayJobs()
	if err != nil {
		t.Fatalf("AllReplayJobs: %v", err)
	}
	var updated ReplayJob
	if err := json.Unmarshal(allData[job.ID], &updated); err != nil {
		t.Fatalf("unmarshal job: %v", err)
	}
	if updated.Status != "completed" {
		t.Errorf("job.Status = %q; want %q", updated.Status, "completed")
	}

	// Task's replay status should be set to replay_complete.
	if tm.status["task-rr-1"] != "replay_complete" {
		t.Errorf("task status = %q; want %q", tm.status["task-rr-1"], "replay_complete")
	}
	// Generation credit should be released (generationEligible=true, match).
	if tm.genStatus["task-rr-1"] != "recognized" {
		t.Errorf("gen status = %q; want %q", tm.genStatus["task-rr-1"], "recognized")
	}
	if len(gen.calls) != 1 {
		t.Errorf("gen calls = %d; want 1", len(gen.calls))
	}
}

// TestReplayRunner_NoPendingJobs verifies that processOnce is a no-op when
// no pending jobs exist.
func TestReplayRunner_NoPendingJobs(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	enf := NewReplayEnforcer(tm, resolver, nil)
	ex := &fakeExecutor{}

	runner := NewReplayRunner(coord, ex, enf, nil, time.Second)
	runner.processOnce() // must not panic or error

	if len(tm.status) != 0 {
		t.Errorf("unexpected task status updates: %v", tm.status)
	}
}

// TestReplayRunner_ExecutorError_Skips verifies that when the executor returns
// an error, the job is skipped (no enforcer call, no status change).
func TestReplayRunner_ExecutorError_Skips(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	job := makeCleanJob("job-rr-2", "task-rr-2")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	enf := NewReplayEnforcer(tm, resolver, nil)

	ex := &fakeExecutor{err: context.DeadlineExceeded}
	runner := NewReplayRunner(coord, ex, enf, nil, time.Second)
	runner.processOnce()

	// Job should still be pending (executor failed, no outcome submitted).
	allData, _ := ms.AllReplayJobs()
	var remaining ReplayJob
	_ = json.Unmarshal(allData[job.ID], &remaining)
	if remaining.Status != "pending" {
		t.Errorf("job.Status = %q; want %q (executor error should not modify job)", remaining.Status, "pending")
	}
	// No task status changes.
	if len(tm.status) != 0 {
		t.Errorf("unexpected task status updates: %v", tm.status)
	}
}

// TestReplayRunner_NilTaskDetails_ZeroValues verifies that when taskDetails is
// nil, the runner calls the enforcer with zero agentID/resultHash/title and
// generationEligible=false. The job is still processed and completed.
func TestReplayRunner_NilTaskDetails_ZeroValues(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	job := makeCleanJob("job-rr-3", "task-rr-3")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enf := NewReplayEnforcer(tm, resolver, gen)
	ex := &fakeExecutor{} // returns match

	// taskDetails = nil → generationEligible=false → gen trigger not called
	runner := NewReplayRunner(coord, ex, enf, nil, time.Second)
	runner.processOnce()

	if tm.status["task-rr-3"] != "replay_complete" {
		t.Errorf("task status = %q; want %q", tm.status["task-rr-3"], "replay_complete")
	}
	// generationEligible=false → no gen call
	if len(gen.calls) != 0 {
		t.Errorf("gen calls = %d; want 0 (generationEligible=false when taskDetails nil)", len(gen.calls))
	}
}

// ---------------------------------------------------------------------------
// Submission-window / grace-period tests
// ---------------------------------------------------------------------------

// TestReplayRunner_SkipsJobWithinSubmissionWindow verifies that a job whose
// SubmissionDeadline has not yet passed is NOT processed by the runner.
// External replay executors still have time to submit real check results.
func TestReplayRunner_SkipsJobWithinSubmissionWindow(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	job := makeCleanJob("job-grace-skip", "task-grace-skip")
	// Deadline is 10 minutes from now — well within the window.
	job.SubmissionDeadline = time.Now().Add(10 * time.Minute)
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	enf := NewReplayEnforcer(tm, resolver, nil)
	ex := &fakeExecutor{} // would produce "match" if called — must NOT be called

	runner := NewReplayRunner(coord, ex, enf, nil, time.Second)
	runner.processOnce()

	// Job must still be "pending" — the runner skipped it.
	allData, err := ms.AllReplayJobs()
	if err != nil {
		t.Fatalf("AllReplayJobs: %v", err)
	}
	var remaining ReplayJob
	_ = json.Unmarshal(allData[job.ID], &remaining)
	if remaining.Status != "pending" {
		t.Errorf("job.Status = %q; want %q — runner must not process job within submission window",
			remaining.Status, "pending")
	}
	// No task state changes.
	if len(tm.status) != 0 {
		t.Errorf("unexpected task status updates: %v", tm.status)
	}
}

// TestReplayRunner_ProcessesJobAfterSubmissionDeadline verifies that once a
// job's SubmissionDeadline has passed, the runner treats it as fallback-eligible
// and processes it through InspectionExecutor.
func TestReplayRunner_ProcessesJobAfterSubmissionDeadline(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	job := makeCleanJob("job-grace-expired", "task-grace-expired")
	// Deadline expired 1 minute ago — fallback is eligible.
	job.SubmissionDeadline = time.Now().Add(-1 * time.Minute)
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	enf := NewReplayEnforcer(tm, resolver, nil)
	ex := &fakeExecutor{} // returns "match" → job marked completed

	runner := NewReplayRunner(coord, ex, enf, nil, time.Second)
	runner.processOnce()

	// Job should now be completed via fallback inspection.
	allData, err := ms.AllReplayJobs()
	if err != nil {
		t.Fatalf("AllReplayJobs: %v", err)
	}
	var updated ReplayJob
	_ = json.Unmarshal(allData[job.ID], &updated)
	if updated.Status != "completed" {
		t.Errorf("job.Status = %q; want %q — runner must process expired-deadline job as fallback",
			updated.Status, "completed")
	}
	if tm.status["task-grace-expired"] != "replay_complete" {
		t.Errorf("task status = %q; want %q", tm.status["task-grace-expired"], "replay_complete")
	}
}

// TestReplayRunner_ZeroDeadline_ProcessesImmediately verifies that jobs with
// a zero SubmissionDeadline (legacy / InspectionExecutor-only mode) are still
// processed immediately — backward compatibility is preserved.
func TestReplayRunner_ZeroDeadline_ProcessesImmediately(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)

	// makeCleanJob produces a job with zero SubmissionDeadline.
	job := makeCleanJob("job-grace-zero", "task-grace-zero")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	enf := NewReplayEnforcer(tm, resolver, nil)
	ex := &fakeExecutor{}

	runner := NewReplayRunner(coord, ex, enf, nil, time.Second)
	runner.processOnce()

	allData, _ := ms.AllReplayJobs()
	var updated ReplayJob
	_ = json.Unmarshal(allData[job.ID], &updated)
	if updated.Status != "completed" {
		t.Errorf("job.Status = %q; want %q — zero-deadline job must be processed immediately",
			updated.Status, "completed")
	}
}

// TestReplayRunner_StartStop verifies that Start/Stop do not deadlock or panic.
func TestReplayRunner_StartStop(t *testing.T) {
	ms := newMemStore()
	coord := NewReplayCoordinator(DefaultReplayPolicy(), ms)
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	enf := NewReplayEnforcer(tm, resolver, nil)
	ex := &fakeExecutor{}

	runner := NewReplayRunner(coord, ex, enf, nil, 10*time.Millisecond)
	runner.Start()
	time.Sleep(25 * time.Millisecond)
	runner.Stop()
	// Idempotent Stop.
	runner.Stop()
}
