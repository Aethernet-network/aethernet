package replay

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// slashExecutor test double
// ---------------------------------------------------------------------------

type captureSlash struct {
	calls  []slashCall
	retErr error
}

type slashCall struct {
	validatorID string
	offense     string
}

func (c *captureSlash) Slash(validatorID string, offense string) (uint64, error) {
	c.calls = append(c.calls, slashCall{validatorID: validatorID, offense: offense})
	return 5_000_000, c.retErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeDisputedOutcome returns a mismatch ReplayOutcome for tests that need a
// slash_recommended or open_challenge verdict. Both checks show Match=false
// and ScoreDelta=1.0 to exceed the adversarial threshold.
func makeDisputedOutcome(jobID, taskID string) *ReplayOutcome {
	return &ReplayOutcome{
		JobID:  jobID,
		TaskID: taskID,
		Status: "mismatch",
		// Both checks failed (Match=false, ScoreDelta=1.0) → adversarial verdict.
		Comparisons: []CheckComparison{
			{CheckType: "go_test", Match: false, ScoreDelta: 1.0},
			{CheckType: "lint", Match: false, ScoreDelta: 1.0},
		},
		ReplayerID: "worker-slash-test",
		ReplayedAt: time.Now(),
	}
}

// ---------------------------------------------------------------------------
// TestReplayEnforcer_SlashExecutor_CalledOnSlashRecommended
// ---------------------------------------------------------------------------

// TestReplayEnforcer_SlashExecutor_CalledOnSlashRecommended verifies that when
// a "slash_recommended" verdict is produced and a slashExecutor is wired, the
// enforcer calls Slash with the worker's ID and "fraudulent_approval".
func TestReplayEnforcer_SlashExecutor_CalledOnSlashRecommended(t *testing.T) {
	enf, tm, _, ms := makeEnforcer(t)

	// Pre-store the outcome's job so RecordOutcome can find it.
	job := makeCleanJob("job-slash-1", "task-slash-1")
	storeJob(t, ms, job)

	cs := &captureSlash{}
	enf.SetSlashExecutor(cs)

	outcome := makeDisputedOutcome("job-slash-1", "task-slash-1")
	verdict, err := enf.ProcessReplayOutcome(outcome, "worker-slash-test", "hash1", "Title", 1_000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}

	if verdict.Action != "open_challenge" && verdict.Action != "slash_recommended" {
		t.Fatalf("verdict.Action = %q; want open_challenge or slash_recommended", verdict.Action)
	}

	if verdict.Action == "slash_recommended" {
		if len(cs.calls) != 1 {
			t.Fatalf("Slash calls = %d; want 1 for slash_recommended", len(cs.calls))
		}
		if cs.calls[0].validatorID != "worker-slash-test" {
			t.Errorf("Slash validatorID = %q; want %q", cs.calls[0].validatorID, "worker-slash-test")
		}
		if cs.calls[0].offense != "fraudulent_approval" {
			t.Errorf("Slash offense = %q; want %q", cs.calls[0].offense, "fraudulent_approval")
		}
	}

	// Regardless: task must be replay_disputed (state change committed before slash).
	if tm.status["task-slash-1"] != "replay_disputed" {
		t.Errorf("task status = %q; want %q", tm.status["task-slash-1"], "replay_disputed")
	}
}

// ---------------------------------------------------------------------------
// TestReplayEnforcer_SlashExecutor_NotCalledOnNoAction
// ---------------------------------------------------------------------------

// TestReplayEnforcer_SlashExecutor_NotCalledOnNoAction verifies that when the
// verdict is "no_action" (match outcome), Slash is NOT called.
func TestReplayEnforcer_SlashExecutor_NotCalledOnNoAction(t *testing.T) {
	enf, tm, _, ms := makeEnforcer(t)

	job := makeCleanJob("job-slash-2", "task-slash-2")
	storeJob(t, ms, job)

	cs := &captureSlash{}
	enf.SetSlashExecutor(cs)

	// Match outcome → no_action verdict.
	outcome := makeCompleteOutcome("job-slash-2", "task-slash-2", "match",
		[]CheckComparison{
			{CheckType: "go_test", Match: true, ScoreDelta: 0.0},
		}, nil)
	outcome.ReplayerID = "worker-slash-2"

	_, err := enf.ProcessReplayOutcome(outcome, "worker-slash-2", "hash2", "Title", 1_000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}

	if len(cs.calls) != 0 {
		t.Errorf("Slash calls = %d; want 0 for no_action verdict", len(cs.calls))
	}
	if tm.status["task-slash-2"] != "replay_complete" {
		t.Errorf("task status = %q; want replay_complete", tm.status["task-slash-2"])
	}
}

// ---------------------------------------------------------------------------
// TestReplayEnforcer_SlashExecutor_NilWhenNotWired
// ---------------------------------------------------------------------------

// TestReplayEnforcer_NilSlashExecutor_NoEffect verifies that when no
// slashExecutor is wired, disputed verdicts are still processed without panic.
func TestReplayEnforcer_NilSlashExecutor_NoEffect(t *testing.T) {
	enf, tm, _, ms := makeEnforcer(t)
	// No SetSlashExecutor call.

	job := makeCleanJob("job-slash-3", "task-slash-3")
	storeJob(t, ms, job)

	outcome := makeDisputedOutcome("job-slash-3", "task-slash-3")
	_, err := enf.ProcessReplayOutcome(outcome, "worker-slash-3", "hash3", "Title", 1_000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	// Task status set regardless of slash.
	if tm.status["task-slash-3"] != "replay_disputed" {
		t.Errorf("task status = %q; want replay_disputed", tm.status["task-slash-3"])
	}
}

// ---------------------------------------------------------------------------
// TestReplayEnforcer_SlashExecutor_ErrorIsNonFatal
// ---------------------------------------------------------------------------

// TestReplayEnforcer_SlashExecutor_ErrorIsNonFatal verifies that a slash error
// does not cause ProcessReplayOutcome to return an error — the task state
// change was already committed.
func TestReplayEnforcer_SlashExecutor_ErrorIsNonFatal(t *testing.T) {
	enf, tm, _, ms := makeEnforcer(t)

	job := makeCleanJob("job-slash-4", "task-slash-4")
	storeJob(t, ms, job)

	cs := &captureSlash{retErr: errors.New("slash engine unavailable")}
	enf.SetSlashExecutor(cs)

	outcome := makeDisputedOutcome("job-slash-4", "task-slash-4")
	verdict, err := enf.ProcessReplayOutcome(outcome, "worker-slash-4", "hash4", "Title", 1_000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome returned error; want nil (slash error is non-fatal): %v", err)
	}
	if verdict == nil {
		t.Fatal("verdict must not be nil")
	}
	// Task state must still be updated despite the slash error.
	if tm.status["task-slash-4"] != "replay_disputed" {
		t.Errorf("task status = %q; want replay_disputed", tm.status["task-slash-4"])
	}
}
