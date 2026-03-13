package replay

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/canary"
)

// storeCanaryJob serialises a ReplayJob and stores it in ms. A missing job causes
// RecordOutcome to fail, so all canary enforcer tests must pre-populate jobs.
func storeCanaryJob(ms *memStore, jobID, taskID string) {
	job := &ReplayJob{ID: jobID, TaskID: taskID, Status: "pending"}
	data, _ := json.Marshal(job)
	_ = ms.PutReplayJob(jobID, data)
}

// ---------------------------------------------------------------------------
// Canary stubs for enforcer tests
// ---------------------------------------------------------------------------

// stubCanaryLookup implements canaryEvalSource for enforcer tests.
type stubCanaryLookup struct {
	index map[string]*canary.CanaryTask // taskID → CanaryTask
}

func newStubCanaryLookup() *stubCanaryLookup {
	return &stubCanaryLookup{index: make(map[string]*canary.CanaryTask)}
}

func (s *stubCanaryLookup) register(taskID string, c *canary.CanaryTask) {
	s.index[taskID] = c
}

func (s *stubCanaryLookup) GetCanaryByTaskID(taskID string) (*canary.CanaryTask, error) {
	c, ok := s.index[taskID]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

// capturingEval implements canaryEvalRecorder and records all Evaluate calls.
type capturingEval struct {
	signals []*canary.CalibrationSignal
}

func (e *capturingEval) Evaluate(
	c *canary.CanaryTask,
	actorID, role string,
	observedPass bool,
	observedChecks map[string]bool,
	observedOutput string,
) *canary.CalibrationSignal {
	sig := &canary.CalibrationSignal{
		CanaryID:     c.ID,
		ActorID:      actorID,
		ActorRole:    role,
		ExpectedPass: c.ExpectedPass,
		ObservedPass: observedPass,
		Timestamp:    time.Now(),
	}
	if c.ExpectedPass == observedPass {
		sig.Correctness = canary.CorrectnessCorrect
	} else {
		sig.Correctness = canary.CorrectnessIncorrect
		if !c.ExpectedPass && observedPass {
			sig.Severity = 1.0
		} else {
			sig.Severity = 0.7
		}
	}
	e.signals = append(e.signals, sig)
	return sig
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestEnforcer_NilCanaryEval_NoEffect verifies that ProcessReplayOutcome
// proceeds normally when SetCanaryEvaluator has not been called.
func TestEnforcer_NilCanaryEval_NoEffect(t *testing.T) {
	taskMgr := newFakeTaskMgr()
	s := newMemStore()
	resolver := NewReplayResolver(s)
	enforcer := NewReplayEnforcer(taskMgr, resolver, nil)
	// No SetCanaryEvaluator — should not panic.

	storeCanaryJob(s, "job-1", "task-1")
	outcome := &ReplayOutcome{
		JobID:      "job-1",
		TaskID:     "task-1",
		Status:     "match",
		ReplayerID: "replayer-1",
		ReplayedAt: time.Now(),
	}
	verdict, vErr := enforcer.ProcessReplayOutcome(outcome, "agent-1", "hash-1", "title-1", 1000, false)
	if vErr != nil {
		t.Fatalf("ProcessReplayOutcome: %v", vErr)
	}
	if verdict == nil {
		t.Fatal("expected non-nil verdict")
	}
}

// TestEnforcer_CanaryMatch_CorrectSignal verifies that when a replay outcome
// has Status="match" for a known-good canary, the emitted calibration signal
// has ObservedPass=true and role=RoleReplaySubmitter.
func TestEnforcer_CanaryMatch_CorrectSignal(t *testing.T) {
	taskMgr := newFakeTaskMgr()
	s := newMemStore()
	resolver := NewReplayResolver(s)
	enforcer := NewReplayEnforcer(taskMgr, resolver, nil)

	lookup := newStubCanaryLookup()
	eval := &capturingEval{}
	enforcer.SetCanaryEvaluator(lookup, eval)

	// Register a known-good canary linked to task-canary-1.
	c := canary.NewCanaryTask("code", canary.TypeKnownGood, true, nil, "gt-hash")
	c.TaskID = "task-canary-1"
	lookup.register("task-canary-1", c)

	storeCanaryJob(s, "job-c1", "task-canary-1")
	outcome := &ReplayOutcome{
		JobID:      "job-c1",
		TaskID:     "task-canary-1",
		Status:     "match", // correct
		ReplayerID: "replayer-agent",
		ReplayedAt: time.Now(),
	}
	_, err := enforcer.ProcessReplayOutcome(outcome, "replayer-agent", "hash-1", "title-1", 1000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if len(eval.signals) != 1 {
		t.Fatalf("expected 1 calibration signal, got %d", len(eval.signals))
	}
	sig := eval.signals[0]
	if sig.ObservedPass != true {
		t.Errorf("expected ObservedPass=true for 'match' status, got false")
	}
	if sig.ActorRole != canary.RoleReplaySubmitter {
		t.Errorf("expected role=%q, got %q", canary.RoleReplaySubmitter, sig.ActorRole)
	}
	if sig.ActorID != "replayer-agent" {
		t.Errorf("expected actor_id=%q, got %q", "replayer-agent", sig.ActorID)
	}
}

// TestEnforcer_CanaryMismatch_HighSeverity verifies that when a replay outcome
// has Status="mismatch" for a known-good canary (expectedPass=true), the
// calibration signal has ObservedPass=false and severity=0.7 (missed good work).
func TestEnforcer_CanaryMismatch_HighSeverity(t *testing.T) {
	taskMgr := newFakeTaskMgr()
	s := newMemStore()
	resolver := NewReplayResolver(s)
	enforcer := NewReplayEnforcer(taskMgr, resolver, nil)

	lookup := newStubCanaryLookup()
	eval := &capturingEval{}
	enforcer.SetCanaryEvaluator(lookup, eval)

	c := canary.NewCanaryTask("code", canary.TypeKnownGood, true, nil, "gt-hash-2")
	c.TaskID = "task-canary-2"
	lookup.register("task-canary-2", c)

	storeCanaryJob(s, "job-c2", "task-canary-2")
	outcome := &ReplayOutcome{
		JobID:      "job-c2",
		TaskID:     "task-canary-2",
		Status:     "mismatch", // observedPass=false
		ReplayerID: "replayer-agent",
		ReplayedAt: time.Now(),
	}
	_, err := enforcer.ProcessReplayOutcome(outcome, "replayer-agent", "hash-2", "title-2", 1000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if len(eval.signals) != 1 {
		t.Fatalf("expected 1 calibration signal, got %d", len(eval.signals))
	}
	sig := eval.signals[0]
	if sig.ObservedPass != false {
		t.Errorf("expected ObservedPass=false for 'mismatch' status")
	}
	// expectedPass=true, observedPass=false → severity=0.7 (missed good work).
	if sig.Severity != 0.7 {
		t.Errorf("expected severity=0.7 for missed good work, got %f", sig.Severity)
	}
}

// TestEnforcer_KnownBadApproved_Severity1 verifies that when a known-bad
// canary (expectedPass=false) is replayed with Status="match" (observedPass=true),
// the calibration signal has severity=1.0 (approved bad work — most dangerous).
func TestEnforcer_KnownBadApproved_Severity1(t *testing.T) {
	taskMgr := newFakeTaskMgr()
	s := newMemStore()
	resolver := NewReplayResolver(s)
	enforcer := NewReplayEnforcer(taskMgr, resolver, nil)

	lookup := newStubCanaryLookup()
	eval := &capturingEval{}
	enforcer.SetCanaryEvaluator(lookup, eval)

	c := canary.NewCanaryTask("code", canary.TypeKnownBad, false, nil, "gt-hash-bad")
	c.TaskID = "task-canary-3"
	lookup.register("task-canary-3", c)

	storeCanaryJob(s, "job-c3", "task-canary-3")
	outcome := &ReplayOutcome{
		JobID:      "job-c3",
		TaskID:     "task-canary-3",
		Status:     "match", // observedPass=true — but expectedPass=false!
		ReplayerID: "replayer-agent",
		ReplayedAt: time.Now(),
	}
	_, err := enforcer.ProcessReplayOutcome(outcome, "replayer-agent", "hash-3", "title-3", 1000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if len(eval.signals) != 1 {
		t.Fatalf("expected 1 calibration signal, got %d", len(eval.signals))
	}
	sig := eval.signals[0]
	if sig.Severity != 1.0 {
		t.Errorf("expected severity=1.0 for approved bad work, got %f", sig.Severity)
	}
	if sig.Correctness != canary.CorrectnessIncorrect {
		t.Errorf("expected correctness=incorrect, got %s", sig.Correctness)
	}
}

// TestEnforcer_NonCanaryTask_NoSignal verifies that when a task has no canary
// record, no calibration signal is emitted.
func TestEnforcer_NonCanaryTask_NoSignal(t *testing.T) {
	taskMgr := newFakeTaskMgr()
	s := newMemStore()
	resolver := NewReplayResolver(s)
	enforcer := NewReplayEnforcer(taskMgr, resolver, nil)

	lookup := newStubCanaryLookup() // empty — no canary records
	eval := &capturingEval{}
	enforcer.SetCanaryEvaluator(lookup, eval)

	storeCanaryJob(s, "job-noc", "task-not-a-canary")
	outcome := &ReplayOutcome{
		JobID:      "job-noc",
		TaskID:     "task-not-a-canary",
		Status:     "match",
		ReplayerID: "replayer-agent",
		ReplayedAt: time.Now(),
	}
	_, err := enforcer.ProcessReplayOutcome(outcome, "replayer-agent", "hash-x", "title-x", 1000, false)
	if err != nil {
		t.Fatalf("ProcessReplayOutcome: %v", err)
	}
	if len(eval.signals) != 0 {
		t.Errorf("expected 0 calibration signals for non-canary task, got %d", len(eval.signals))
	}
}
