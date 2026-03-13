package replay

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestValidateOutcome_EmptyJobID verifies that an outcome with an empty JobID
// is rejected with ErrEmptyJobID.
func TestValidateOutcome_EmptyJobID(t *testing.T) {
	r := NewReplayResolver(newMemStore())
	outcome := &ReplayOutcome{
		JobID:  "",
		TaskID: "task-1",
		Status: "match",
	}
	err := r.ValidateOutcome(outcome)
	if !errors.Is(err, ErrEmptyJobID) {
		t.Errorf("want ErrEmptyJobID, got %v", err)
	}
}

// TestValidateOutcome_NegativeScoreDelta verifies that a comparison with a
// negative ScoreDelta is rejected with ErrNegativeScoreDelta.
func TestValidateOutcome_NegativeScoreDelta(t *testing.T) {
	ms := newMemStore()
	r := NewReplayResolver(ms)

	// Must pre-populate a pending job so the store lookup succeeds.
	job := makeCleanJob("job-neg", "task-neg")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	outcome := &ReplayOutcome{
		JobID:  "job-neg",
		TaskID: "task-neg",
		Status: "partial_match",
		Comparisons: []CheckComparison{
			{CheckType: "go_test", Match: true},
			{CheckType: "lint", Match: false, ScoreDelta: -0.10},
		},
	}
	err := r.ValidateOutcome(outcome)
	if !errors.Is(err, ErrNegativeScoreDelta) {
		t.Errorf("want ErrNegativeScoreDelta, got %v", err)
	}
}

// TestValidateOutcome_UnknownJob verifies that an outcome referencing an
// unknown job ID is rejected with ErrJobNotFound.
func TestValidateOutcome_UnknownJob(t *testing.T) {
	r := NewReplayResolver(newMemStore())
	outcome := &ReplayOutcome{
		JobID:  "nonexistent-job",
		TaskID: "task-x",
		Status: "match",
	}
	err := r.ValidateOutcome(outcome)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("want ErrJobNotFound, got %v", err)
	}
}

// TestValidateOutcome_DuplicateTerminal_Completed verifies that an outcome
// submitted for a job that is already "completed" is rejected.
func TestValidateOutcome_DuplicateTerminal_Completed(t *testing.T) {
	ms := newMemStore()
	r := NewReplayResolver(ms)

	job := &ReplayJob{ID: "job-dup", TaskID: "task-dup", Status: "completed"}
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	outcome := &ReplayOutcome{
		JobID:  "job-dup",
		TaskID: "task-dup",
		Status: "match",
	}
	err := r.ValidateOutcome(outcome)
	if !errors.Is(err, ErrOutcomeAlreadyTerminal) {
		t.Errorf("want ErrOutcomeAlreadyTerminal, got %v", err)
	}
}

// TestValidateOutcome_DuplicateTerminal_Failed verifies that an outcome
// submitted for a job that is already "failed" is rejected.
func TestValidateOutcome_DuplicateTerminal_Failed(t *testing.T) {
	ms := newMemStore()
	r := NewReplayResolver(ms)

	job := &ReplayJob{ID: "job-fail", TaskID: "task-fail", Status: "failed"}
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	outcome := &ReplayOutcome{
		JobID:  "job-fail",
		TaskID: "task-fail",
		Status: "error",
	}
	err := r.ValidateOutcome(outcome)
	if !errors.Is(err, ErrOutcomeAlreadyTerminal) {
		t.Errorf("want ErrOutcomeAlreadyTerminal, got %v", err)
	}
}

// TestValidateOutcome_Valid verifies that a valid outcome against a pending job
// passes validation.
func TestValidateOutcome_Valid(t *testing.T) {
	ms := newMemStore()
	r := NewReplayResolver(ms)

	job := makeCleanJob("job-ok", "task-ok")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	outcome := &ReplayOutcome{
		JobID:      "job-ok",
		TaskID:     "task-ok",
		Status:     "match",
		ReplayedAt: time.Now(),
		ReplayerID: "agent-ok",
		Comparisons: []CheckComparison{
			{CheckType: "go_test", Match: true, ScoreDelta: 0.0},
		},
	}
	if err := r.ValidateOutcome(outcome); err != nil {
		t.Errorf("expected nil error for valid outcome, got %v", err)
	}
}

// TestRecordOutcome_RejectsUnknownJob verifies that RecordOutcome propagates
// the ErrJobNotFound error from ValidateOutcome before persisting anything.
func TestRecordOutcome_RejectsUnknownJob(t *testing.T) {
	ms := newMemStore()
	r := NewReplayResolver(ms)

	outcome := &ReplayOutcome{
		JobID:  "no-such-job",
		TaskID: "task-x",
		Status: "match",
	}
	err := r.RecordOutcome(outcome)
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("want ErrJobNotFound from RecordOutcome, got %v", err)
	}
	// Outcome must NOT have been persisted.
	if _, storeErr := ms.GetReplayOutcome("no-such-job"); storeErr == nil {
		t.Error("outcome was persisted despite unknown job — should not have been")
	}
}

// TestRecordOutcome_RejectsDuplicateTerminal verifies that a second outcome for
// a completed job is rejected, keeping the store in a consistent state.
func TestRecordOutcome_RejectsDuplicateTerminal(t *testing.T) {
	ms := newMemStore()
	r := NewReplayResolver(ms)

	job := makeCleanJob("job-dedup", "task-dedup")
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(job.ID, data)

	// First outcome — should succeed.
	first := &ReplayOutcome{
		JobID:      "job-dedup",
		TaskID:     "task-dedup",
		Status:     "match",
		ReplayedAt: time.Now(),
	}
	if err := r.RecordOutcome(first); err != nil {
		t.Fatalf("first RecordOutcome: %v", err)
	}

	// Second outcome — job is now "completed" → rejected.
	second := &ReplayOutcome{
		JobID:      "job-dedup",
		TaskID:     "task-dedup",
		Status:     "mismatch",
		ReplayedAt: time.Now(),
	}
	err := r.RecordOutcome(second)
	if !errors.Is(err, ErrOutcomeAlreadyTerminal) {
		t.Errorf("want ErrOutcomeAlreadyTerminal on second RecordOutcome, got %v", err)
	}
}
