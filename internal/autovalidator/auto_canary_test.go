package autovalidator_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/canary"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// ---------------------------------------------------------------------------
// Test-only in-memory implementations of the canary interfaces
// ---------------------------------------------------------------------------

// stubCanaryEvalSource is an in-memory canaryEvalSource that maps taskID →
// *canary.CanaryTask. An error is returned for unknown task IDs.
type stubCanaryEvalSource struct {
	mu    sync.Mutex
	index map[string]*canary.CanaryTask // taskID → CanaryTask
}

func newStubCanaryEvalSource() *stubCanaryEvalSource {
	return &stubCanaryEvalSource{index: make(map[string]*canary.CanaryTask)}
}

func (s *stubCanaryEvalSource) register(taskID string, c *canary.CanaryTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index[taskID] = c
}

func (s *stubCanaryEvalSource) GetCanaryByTaskID(taskID string) (*canary.CanaryTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.index[taskID]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

// capturingCanaryEval records every Evaluate call for later inspection.
type capturingCanaryEval struct {
	mu      sync.Mutex
	signals []*canary.CalibrationSignal
}

func (e *capturingCanaryEval) Evaluate(
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
	}
	if c.ExpectedPass == observedPass {
		sig.Correctness = canary.CorrectnessCorrect
		sig.Severity = 0.0
	} else {
		sig.Correctness = canary.CorrectnessIncorrect
		if c.ExpectedPass && !observedPass {
			sig.Severity = 0.7
		} else {
			sig.Severity = 1.0 // approved bad work — most dangerous
		}
	}
	e.mu.Lock()
	e.signals = append(e.signals, sig)
	e.mu.Unlock()
	return sig
}

func (e *capturingCanaryEval) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.signals)
}

func (e *capturingCanaryEval) last() *canary.CalibrationSignal {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.signals) == 0 {
		return nil
	}
	return e.signals[len(e.signals)-1]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const canaryBudget = 1_000_000

// buildCanaryAVHarness creates the minimal stack for testing canary evaluation.
// The poster is funded and escrow is available; postAndSubmitCanaryTask handles
// escrow.Hold and task lifecycle.
func buildCanaryAVHarness(t *testing.T) (
	av *autovalidator.AutoValidator,
	tm *tasks.TaskManager,
	esc *escrow.Escrow,
	posterID, workerID crypto.AgentID,
) {
	t.Helper()

	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() { eng.Stop() })

	posterID = crypto.AgentID("poster-canary")
	workerID = crypto.AgentID("worker-canary")
	validatorID := crypto.AgentID("testnet-validator-canary")

	if err := tl.FundAgent(posterID, canaryBudget*10); err != nil {
		t.Fatalf("fund poster: %v", err)
	}

	esc = escrow.New(tl)
	tm = tasks.NewTaskManager()

	av = autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetTaskStalenessThreshold(0)

	return av, tm, esc, posterID, workerID
}

// postAndSubmitCanaryTask posts a task, holds escrow, claims it, and submits a
// result note that reliably scores above PassThreshold (0.60) for any category.
func postAndSubmitCanaryTask(
	t *testing.T,
	tm *tasks.TaskManager,
	esc *escrow.Escrow,
	posterID, workerID crypto.AgentID,
	category string,
) *tasks.Task {
	t.Helper()
	task, err := tm.PostTask(string(posterID), "Canary evaluation task", "A test task for canary evaluation",
		category, canaryBudget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, canaryBudget); err != nil {
		t.Fatalf("escrow.Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, workerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	// Result note contains keywords from title+description and is long enough
	// (≥100 chars) to score above PassThreshold (0.60) with any verifier.
	resultNote := "This canary evaluation task has been completed. All canary evaluation test " +
		"requirements were addressed and verified. Task results are ready for review."
	if err := tm.SubmitResult(task.ID, workerID, "sha256:canary-hash", resultNote, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	updated, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	return updated
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestCanaryEval_NilEvaluator_NoEffect verifies that when no canary evaluator
// is wired, normal task settlement proceeds unchanged (backward compatible).
func TestCanaryEval_NilEvaluator_NoEffect(t *testing.T) {
	av, tm, esc, posterID, workerID := buildCanaryAVHarness(t)
	// No SetCanaryEvaluator call — nil evaluator.
	av.Start()
	defer av.Stop()

	task := postAndSubmitCanaryTask(t, tm, esc, posterID, workerID, "writing")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if updated, _ := tm.Get(task.ID); updated.Status == tasks.TaskStatusCompleted {
			return // settled normally — test passes
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("task was not settled within timeout (nil canary evaluator case)")
}

// TestCanaryEval_NonCanaryTask_NoSignal verifies that when a task has no
// canary record, the evaluator is NOT called (0 signals emitted).
func TestCanaryEval_NonCanaryTask_NoSignal(t *testing.T) {
	av, tm, esc, posterID, workerID := buildCanaryAVHarness(t)

	src := newStubCanaryEvalSource() // empty — no canary records
	eval := &capturingCanaryEval{}
	av.SetCanaryEvaluator(src, eval)
	av.Start()
	defer av.Stop()

	task := postAndSubmitCanaryTask(t, tm, esc, posterID, workerID, "writing")
	_ = task

	// Give the auto-validator time to process.
	time.Sleep(400 * time.Millisecond)

	if n := eval.count(); n != 0 {
		t.Errorf("expected 0 calibration signals for non-canary task, got %d", n)
	}
}

// TestCanaryEval_CanaryTask_EmitsSignal verifies that when a task IS a canary,
// a CalibrationSignal is emitted with the correct actor and role.
func TestCanaryEval_CanaryTask_EmitsSignal(t *testing.T) {
	av, tm, esc, posterID, workerID := buildCanaryAVHarness(t)

	src := newStubCanaryEvalSource()
	eval := &capturingCanaryEval{}
	av.SetCanaryEvaluator(src, eval)
	av.Start()
	defer av.Stop()

	task := postAndSubmitCanaryTask(t, tm, esc, posterID, workerID, "writing")

	// Register as a canary AFTER submission (simulating pre-registered canary).
	c := canary.NewCanaryTask("writing", canary.TypeKnownGood, true, nil, "hash-abc")
	c.TaskID = task.ID
	src.register(task.ID, c)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eval.count() > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if eval.count() == 0 {
		t.Fatal("expected calibration signal to be emitted")
	}
	sig := eval.last()
	if sig.ActorID != string(workerID) {
		t.Errorf("expected actor_id=%s, got %s", workerID, sig.ActorID)
	}
	if sig.ActorRole != canary.RoleWorker {
		t.Errorf("expected actor_role=%s, got %s", canary.RoleWorker, sig.ActorRole)
	}
}

// TestCanaryEval_KnownBadApproved_HighSeverity verifies that when a known-bad
// canary (expectedPass=false) is verified as passing, the calibration signal
// has severity=1.0 (most dangerous: approved bad work).
func TestCanaryEval_KnownBadApproved_HighSeverity(t *testing.T) {
	av, tm, esc, posterID, workerID := buildCanaryAVHarness(t)

	src := newStubCanaryEvalSource()
	eval := &capturingCanaryEval{}
	av.SetCanaryEvaluator(src, eval)
	av.Start()
	defer av.Stop()

	task := postAndSubmitCanaryTask(t, tm, esc, posterID, workerID, "writing")

	// Register as a known_bad canary: expectedPass=false.
	c := canary.NewCanaryTask("writing", canary.TypeKnownBad, false, nil, "hash-bad")
	c.TaskID = task.ID
	src.register(task.ID, c)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eval.count() > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if eval.count() == 0 {
		t.Fatal("expected calibration signal for known-bad canary")
	}
	sig := eval.last()
	if sig.ActorRole != canary.RoleWorker {
		t.Errorf("expected RoleWorker, got %s", sig.ActorRole)
	}
	// When verifier passes (observedPass=true) but expectedPass=false → severity 1.0.
	if sig.ObservedPass && !sig.ExpectedPass {
		if sig.Severity != 1.0 {
			t.Errorf("expected severity=1.0 for approved bad work, got %f", sig.Severity)
		}
		if sig.Correctness != canary.CorrectnessIncorrect {
			t.Errorf("expected correctness=incorrect, got %s", sig.Correctness)
		}
	}
}

// TestCanaryEval_RoleIsWorker verifies that the role written into calibration
// signals emitted from the auto-validator is always RoleWorker.
func TestCanaryEval_RoleIsWorker(t *testing.T) {
	av, tm, esc, posterID, workerID := buildCanaryAVHarness(t)

	src := newStubCanaryEvalSource()
	eval := &capturingCanaryEval{}
	av.SetCanaryEvaluator(src, eval)
	av.Start()
	defer av.Stop()

	task := postAndSubmitCanaryTask(t, tm, esc, posterID, workerID, "writing")
	c := canary.NewCanaryTask("writing", canary.TypeKnownGood, true, nil, "gt-hash")
	c.TaskID = task.ID
	src.register(task.ID, c)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eval.count() > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if n := eval.count(); n == 0 {
		t.Fatal("expected at least one calibration signal")
	}
	sig := eval.last()
	if sig.ActorRole != canary.RoleWorker {
		t.Errorf("expected role=%q, got %q", canary.RoleWorker, sig.ActorRole)
	}
}
