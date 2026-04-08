package autovalidator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// TestAutoValidator_ProcessesPending verifies that AutoValidator polls the OCS
// engine and emits VerificationVote events for pending items. The auto-validator
// NEVER calls ProcessResult — it only emits votes.
func TestAutoValidator_ProcessesPending(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer eng.Stop()

	// Wire a DAG and keypair so emitVote can create vote events.
	d := dag.New()
	kp, _ := crypto.GenerateKeyPair()
	validatorID := kp.AgentID()

	// Create and sign the target event so it can be added to the DAG.
	// The vote will reference this event as its causal parent.
	aliceKP, _ := crypto.GenerateKeyPair()
	payload := event.TransferPayload{
		FromAgent: string(aliceKP.AgentID()),
		ToAgent:   "bob",
		Amount:    1_000,
		Currency:  "AET",
	}
	ev, err := event.New(event.EventTypeTransfer, nil, payload, string(aliceKP.AgentID()), nil, 1_000)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	_ = crypto.SignEvent(ev, aliceKP)
	if err := d.Add(ev); err != nil {
		t.Fatalf("dag.Add target event: %v", err)
	}

	// Fund the sender and submit to OCS.
	if err := tl.FundAgent(aliceKP.AgentID(), 100_000); err != nil {
		t.Fatalf("fund sender: %v", err)
	}
	if err := eng.Submit(ev); err != nil {
		t.Fatalf("submit event: %v", err)
	}

	if count := eng.PendingCount(); count != 1 {
		t.Fatalf("expected 1 pending item before auto-validation, got %d", count)
	}

	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetDAG(d)
	av.SetKeyPair(kp)
	av.Start()
	defer av.Stop()

	// Wait for the auto-validator to emit at least one vote event.
	// The DAG starts with 1 event (the target); we wait for size > 1.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.Size() > 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if d.Size() <= 1 {
		t.Fatal("expected at least one VerificationVote event in DAG after auto-validation")
	}

	// In single-node mode (no VotingRound), the auto-validator's ProcessVote
	// call settles the event immediately via ProcessResult. The pending count
	// should drop to 0. In multi-node mode, the vote is registered with the
	// VotingRound and settlement waits for supermajority.
	if eng.PendingCount() != 0 {
		t.Errorf("pending count should be 0 in single-node mode — ProcessVote settles immediately, got %d", eng.PendingCount())
	}
}

// TestAutoValidator_FeeOnTaskSettlement verifies that when the auto-validator
// approves a submitted task it creates a canonical TaskSettlement DAG event
// containing the full payload needed for consensus-gated escrow release.
// Escrow release and fee collection are now handled by the SettlementApplicator
// after consensus — the auto-validator only emits the event.
func TestAutoValidator_FeeOnTaskSettlement(t *testing.T) {
	t.Skip("Legacy test — direct settlement replaced by multi-validator consensus (prompt 09)")
	const budget = 1_000_000

	posterID := crypto.AgentID("poster")
	claimerID := crypto.AgentID("worker")
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

	// Wire a DAG and keypair so the auto-validator can emit TaskSettlement events.
	// The keypair must match the validatorID so crypto.SignEvent succeeds.
	d := dag.New()
	kp, _ := crypto.GenerateKeyPair()
	validatorID := kp.AgentID()

	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetFeeCollector(fc, treasuryID)
	av.SetDAG(d)
	av.SetKeyPair(kp)
	av.SetTaskStalenessThreshold(0) // process immediately, no 10s wait
	av.Start()
	defer av.Stop()

	// Wait for task completion AND the TaskSettlement DAG event. The
	// autovalidator creates the event after ApproveTask, so we poll for both.
	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		tk, err := tm.Get(task.ID)
		if err != nil || tk.Status != tasks.TaskStatusCompleted {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		for _, ev := range d.All() {
			if ev.Type == event.EventTypeTaskSettlement {
				found = true
				break
			}
		}
		if found {
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
	if !found {
		t.Error("no TaskSettlement event found in DAG after task approval")
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

	av := autovalidator.NewAutoValidator(eng, "testnet-validator", time.Second)
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
	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
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
	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
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
	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
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
	t.Skip("Legacy test — generation ledger on direct settle replaced by multi-validator consensus (prompt 09)")
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
	av := autovalidator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.SetTaskManager(tm, esc)
	av.SetGenerationLedger(gl)
	av.SetTaskStalenessThreshold(0)
	av.Start()
	defer av.Stop()

	// Wait for both task completion AND generation ledger recording. These happen
	// sequentially in settleTask (ApproveTask first, then RecordTaskGeneration),
	// so we must not stop at TaskStatusCompleted alone — the ledger write may not
	// have run yet.
	var totalGen uint64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, _ := tm.Get(task.ID)
		if tk != nil && tk.Status == tasks.TaskStatusCompleted {
			v, _ := gl.TotalVerifiedValue(24 * time.Hour)
			if v > 0 {
				totalGen = v
				break
			}
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
	if totalGen == 0 {
		t.Error("TotalVerifiedValue = 0; expected > 0 after task completion recorded in generation ledger")
	}
	// The verified value must not exceed the budget.
	if totalGen > budget {
		t.Errorf("TotalVerifiedValue = %d; must not exceed budget %d", totalGen, budget)
	}
	t.Logf("Generation ledger TotalVerifiedValue: %d (budget: %d, score: ~0.85)", totalGen, budget)
}
