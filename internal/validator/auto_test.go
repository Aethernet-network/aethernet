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
