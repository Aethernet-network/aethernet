package validator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/validator"
)

// TestAutoValidator_ProcessesPending verifies that AutoValidator polls the OCS
// engine and settles pending items within one tick interval.
func TestAutoValidator_ProcessesPending(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer eng.Stop()

	// Fund the sender so the OCS balance check passes.
	senderID := crypto.AgentID("alice")
	if err := tl.FundAgent(senderID, 100_000); err != nil {
		t.Fatalf("fund sender: %v", err)
	}

	// Build and submit a Transfer event. The OCS engine does not require a
	// cryptographic signature — that is enforced by the API server layer.
	payload := event.TransferPayload{
		FromAgent: "alice",
		ToAgent:   "bob",
		Amount:    1_000,
		Currency:  "AET",
	}
	ev, err := event.New(event.EventTypeTransfer, nil, payload, "alice", nil, 1_000)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := eng.Submit(ev); err != nil {
		t.Fatalf("submit event: %v", err)
	}

	if count := eng.PendingCount(); count != 1 {
		t.Fatalf("expected 1 pending item before auto-validation, got %d", count)
	}

	// The auto-validator's ID must be different from alice and bob to pass the
	// OCS anti-self-dealing guard.
	validatorID := crypto.AgentID("testnet-validator")
	av := validator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.Start()
	defer av.Stop()

	// Wait up to 2 s for the pending count to reach 0.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eng.PendingCount() == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if count := eng.PendingCount(); count != 0 {
		t.Fatalf("expected 0 pending items after auto-validation, got %d", count)
	}
}

// TestAutoValidator_FeeOnTaskSettlement verifies that when the auto-validator
// approves a submitted task:
//   - the worker receives budget - fee (not the full budget)
//   - feeCollector.Stats reports the collected fee
//
// Budget: 1_000_000 micro-AET → fee = 1_000 (0.1%) → worker receives 999_000.
func TestAutoValidator_FeeOnTaskSettlement(t *testing.T) {
	const budget = 1_000_000
	expectedFee := fees.CalculateFee(budget)   // 100
	expectedNet := budget - expectedFee         // 999_900

	posterID := crypto.AgentID("poster")
	claimerID := crypto.AgentID("worker")
	validatorID := crypto.AgentID("testnet-validator")
	treasuryID := crypto.AgentID("treasury")

	// Set up ledger, escrow, and fee collector.
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent poster: %v", err)
	}
	esc := escrow.New(tl)
	fc := fees.NewCollector(tl)

	// Set up task manager: post, hold escrow, claim, submit.
	tm := tasks.NewTaskManager()
	task, err := tm.PostTask(string(posterID), "Test task", "desc", "research", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	// The result note must: (a) be ≥100 bytes (text completeness minimum) and
	// (b) contain keywords from the task title/description so the evidence verifier
	// scores it above PassThreshold (0.60). "Test task" + "desc" contribute the
	// words "test", "task", "desc" (>3 chars), all of which appear in the note below.
	resultNote := "This test task has been completed successfully. The task description (desc) was analyzed and implemented with care. Results are verified and ready for review."
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:abc", resultNote, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	// Build an OCS engine (required by AutoValidator constructor).
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer eng.Stop()

	av := validator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetFeeCollector(fc, treasuryID)
	av.SetTaskStalenessThreshold(0) // process immediately, no 10s wait
	av.Start()
	defer av.Stop()

	// Wait for the auto-validator to process the submitted task.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(task.ID)
		if tk != nil && tk.Status == tasks.TaskStatusCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	tk, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task after settlement: %v", err)
	}
	if tk.Status != tasks.TaskStatusCompleted {
		t.Fatalf("task status = %q; want %q", tk.Status, tasks.TaskStatusCompleted)
	}

	// Worker should have received netAmount (budget - fee), not the full budget.
	workerBal, _ := tl.Balance(claimerID)
	if workerBal != expectedNet {
		t.Errorf("worker balance = %d; want %d (budget %d - fee %d)",
			workerBal, expectedNet, budget, expectedFee)
	}
	if workerBal >= budget {
		t.Errorf("worker received full budget %d; fee was not deducted", budget)
	}

	// Fee collector should have recorded the fee.
	collected, _, _ := fc.Stats()
	if collected == 0 {
		t.Error("feeCollector.totalCollected = 0; expected fee to be collected")
	}
	if collected != expectedFee {
		t.Errorf("feeCollector.totalCollected = %d; want %d", collected, expectedFee)
	}
}

// TestAutoValidator_StopIsIdempotent verifies that calling Stop multiple times
// does not panic (uses sync.Once internally).
func TestAutoValidator_StopIsIdempotent(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer eng.Stop()

	av := validator.NewAutoValidator(eng, "testnet-validator", time.Second)
	av.Start()
	av.Stop()
	av.Stop() // must not panic
}

// goodResultNote is a result note that scores >= PassThreshold (0.60) when the
// task title is "Test task" and description is "desc".
// - Keywords >3 chars from title/desc: "test", "task", "desc" → Relevance = 1.0
// - OutputSize = len(goodResultNote) ≥ 100 → Completeness = 1.0 (for 1M budget)
// - Quality = 0.5 (base only)
// Overall = 1.0×0.3 + 1.0×0.4 + 0.5×0.3 = 0.85 ✓
const goodResultNote = "This test task has been completed successfully. The task description (desc) was analyzed and implemented with care. Results are verified and ready for review."

// newEngineForTest creates a minimal OCS engine used by tests that need an AutoValidator.
func newEngineForTest(t *testing.T, tl *ledger.TransferLedger) *ocs.Engine {
	t.Helper()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)
	return eng
}

// TestAutoValidator_DisputeResolutionApprove verifies that when a dispute is
// raised on a task whose evidence scores well, the auto-validator releases funds
// to the worker (not the poster) after the review timeout.
func TestAutoValidator_DisputeResolutionApprove(t *testing.T) {
	const budget = 1_000_000
	posterID := crypto.AgentID("poster-dispute-approve")
	claimerID := crypto.AgentID("worker-dispute-approve")
	validatorID := crypto.AgentID("testnet-validator")

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	esc := escrow.New(tl)
	tm := tasks.NewTaskManager()

	task, err := tm.PostTask(string(posterID), "Test task", "desc", "research", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	// Submit evidence that will score >= 0.60 so dispute resolves in worker's favour.
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:abc", goodResultNote, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	// Poster disputes the task.
	if err := tm.DisputeTask(task.ID, posterID); err != nil {
		t.Fatalf("DisputeTask: %v", err)
	}

	eng := newEngineForTest(t, tl)
	av := validator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetDisputeReviewTimeout(0) // resolve immediately in test
	av.Start()
	defer av.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(task.ID)
		if tk != nil && tk.Status == tasks.TaskStatusCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	tk, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.Status != tasks.TaskStatusCompleted {
		t.Fatalf("task status = %q; want %q (good evidence should resolve in worker's favour)", tk.Status, tasks.TaskStatusCompleted)
	}
	// Worker should have received the net amount (full budget minus fee).
	workerBal, _ := tl.Balance(claimerID)
	expectedNet := budget - fees.CalculateFee(budget)
	if workerBal != expectedNet {
		t.Errorf("worker balance = %d; want %d after dispute approval", workerBal, expectedNet)
	}
	// Poster's escrow should be gone (funds moved to worker).
	posterBal, _ := tl.Balance(posterID)
	if posterBal != 0 {
		t.Errorf("poster balance = %d; want 0 (funds should be with worker, not refunded)", posterBal)
	}
}

// TestAutoValidator_DisputeResolutionReject verifies that when a dispute is
// raised on a task whose evidence scores poorly, the auto-validator refunds
// the poster and penalises the worker's reputation.
func TestAutoValidator_DisputeResolutionReject(t *testing.T) {
	const budget = 1_000_000
	posterID := crypto.AgentID("poster-dispute-reject")
	claimerID := crypto.AgentID("worker-dispute-reject")
	validatorID := crypto.AgentID("testnet-validator")

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	esc := escrow.New(tl)
	rm := reputation.NewReputationManager()
	tm := tasks.NewTaskManager()

	task, err := tm.PostTask(string(posterID), "Test task", "desc", "research", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	// Submit minimal evidence that scores < 0.60 → dispute resolves in poster's favour.
	// "bad" is 3 chars (≤3) so it won't match any task keywords, and length < 100.
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:abc", "bad", ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}
	if err := tm.DisputeTask(task.ID, posterID); err != nil {
		t.Fatalf("DisputeTask: %v", err)
	}

	eng := newEngineForTest(t, tl)
	av := validator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetReputationManager(rm)
	av.SetDisputeReviewTimeout(0) // resolve immediately
	av.Start()
	defer av.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(task.ID)
		if tk != nil && tk.Status == tasks.TaskStatusCancelled {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	tk, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.Status != tasks.TaskStatusCancelled {
		t.Fatalf("task status = %q; want %q (bad evidence should cancel and refund)", tk.Status, tasks.TaskStatusCancelled)
	}
	// Poster should have been refunded the full budget.
	posterBal, _ := tl.Balance(posterID)
	if posterBal != budget {
		t.Errorf("poster balance = %d; want %d (should be fully refunded)", posterBal, budget)
	}
	// Worker's reputation should have a failure recorded.
	rep := rm.GetReputation(claimerID)
	if rep.TotalFailed == 0 {
		t.Error("worker TotalFailed = 0; expected 1 failure from dispute rejection")
	}
}

// TestAutoValidator_ClaimTimeout verifies that when a claimed task's deadline
// passes without a submission, the auto-validator releases it back to Open and
// records a reputation failure for the claimer.
func TestAutoValidator_ClaimTimeout(t *testing.T) {
	const budget = 500_000
	posterID := crypto.AgentID("poster-claim-timeout")
	claimerID := crypto.AgentID("worker-claim-timeout")
	validatorID := crypto.AgentID("testnet-validator")

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	esc := escrow.New(tl)
	rm := reputation.NewReputationManager()
	tm := tasks.NewTaskManager()

	task, err := tm.PostTask(string(posterID), "Timeout task", "should expire", "research", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	// Claim the task — do NOT submit a result.
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	eng := newEngineForTest(t, tl)
	av := validator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetReputationManager(rm)
	// Override claim timeout to 1ms so the task expires immediately in the test.
	av.SetClaimTimeout(time.Millisecond)
	av.Start()
	defer av.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(task.ID)
		if tk != nil && tk.Status == tasks.TaskStatusOpen {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	tk, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.Status != tasks.TaskStatusOpen {
		t.Fatalf("task status = %q; want %q after claim timeout", tk.Status, tasks.TaskStatusOpen)
	}
	if tk.ClaimerID != "" {
		t.Errorf("task.ClaimerID = %q; want empty after timeout release", tk.ClaimerID)
	}

	// Poll for the reputation failure — RecordFailure runs in the same goroutine
	// tick as ReleaseTask (which triggers the status → open transition we polled
	// for above), but with the race detector enabled goroutines can preempt
	// unexpectedly. Poll up to 1 s to avoid flakiness.
	repDeadline := time.Now().Add(time.Second)
	var rep *reputation.AgentReputation
	for time.Now().Before(repDeadline) {
		rep = rm.GetReputation(claimerID)
		if rep.TotalFailed > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if rep.TotalFailed == 0 {
		t.Error("claimer TotalFailed = 0; expected 1 failure from claim timeout")
	}
	// Escrow remains intact — poster's funds stay locked for the next claimer.
	escEntry, err := esc.Get(task.ID)
	if err != nil {
		t.Fatalf("escrow Get after timeout: %v (funds should remain in escrow for next claimer)", err)
	}
	if escEntry.Amount != budget {
		t.Errorf("escrow amount = %d; want %d (full budget should remain in escrow)", escEntry.Amount, budget)
	}
}

// TestAutoValidator_GenerationLedger verifies that when the auto-validator
// approves a task, it records a Settled entry in the generation ledger and
// TotalVerifiedValue reflects the productive AI output.
func TestAutoValidator_GenerationLedger(t *testing.T) {
	const budget = 2_000_000
	posterID := crypto.AgentID("poster-gen")
	claimerID := crypto.AgentID("worker-gen")
	validatorID := crypto.AgentID("testnet-validator")

	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(posterID, budget); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	esc := escrow.New(tl)
	gl := ledger.NewGenerationLedger()
	tm := tasks.NewTaskManager()

	task, err := tm.PostTask(string(posterID), "Test task", "desc", "research", budget)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if err := esc.Hold(task.ID, posterID, budget); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := tm.ClaimTask(task.ID, claimerID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := tm.SubmitResult(task.ID, claimerID, "sha256:abc", goodResultNote, ""); err != nil {
		t.Fatalf("SubmitResult: %v", err)
	}

	tl2 := ledger.NewTransferLedger() // OCS engine doesn't need real funds
	eng := newEngineForTest(t, tl2)
	av := validator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetGenerationLedger(gl)
	av.SetTaskStalenessThreshold(0)
	av.Start()
	defer av.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(task.ID)
		if tk != nil && tk.Status == tasks.TaskStatusCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	tk, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if tk.Status != tasks.TaskStatusCompleted {
		t.Fatalf("task status = %q; want %q", tk.Status, tasks.TaskStatusCompleted)
	}

	// Generation ledger should have a Settled entry for the completed task.
	totalGen, err := gl.TotalVerifiedValue(24 * time.Hour)
	if err != nil {
		t.Fatalf("TotalVerifiedValue: %v", err)
	}
	if totalGen == 0 {
		t.Error("TotalVerifiedValue = 0; expected > 0 after task completion recorded in generation ledger")
	}
	// The verified value must not exceed the budget.
	if totalGen > budget {
		t.Errorf("TotalVerifiedValue = %d; must not exceed budget %d", totalGen, budget)
	}
	t.Logf("Generation ledger TotalVerifiedValue: %d (budget: %d, score: ~0.85)", totalGen, budget)
}
