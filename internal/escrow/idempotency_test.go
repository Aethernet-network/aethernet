package escrow

// Whitebox test for ReleaseNet idempotency (CRITICAL-3). Uses the internal
// package so it can access unexported fields (entries map, mu) to simulate a
// partially-completed disbursement: WorkerPaid=true, ValidatorPaid=false.
// The new implementation skips already-paid recipients, preventing double payment.

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// TestReleaseNet_Idempotency_WorkerAlreadyPaid verifies that when WorkerPaid is
// already set (e.g. from a prior partially-successful call), the worker is NOT
// paid again on retry. The validator and treasury still receive their shares.
func TestReleaseNet_Idempotency_WorkerAlreadyPaid(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID("poster"), 20_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	e := New(tl)
	if err := e.Hold("task1", crypto.AgentID("poster"), 10_000); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	// Simulate a prior partial success: the worker payment already went through
	// but the validator payment failed. Mark WorkerPaid=true in the entry.
	e.mu.Lock()
	e.entries["task1"].WorkerPaid = true
	e.mu.Unlock()

	// On retry, ReleaseNet should skip the worker and only pay validator+treasury.
	// The escrow bucket still has the full 10 000 (we never actually transferred
	// to worker in this test — we just set the flag to simulate a prior attempt).
	if err := e.ReleaseNet(
		"task1",
		crypto.AgentID("worker"), 9_000,
		crypto.AgentID("validator"), 800,
		crypto.AgentID("treasury"), 200,
	); err != nil {
		t.Fatalf("ReleaseNet on retry: %v", err)
	}

	// Worker should have 0: payment was flagged as already done and skipped.
	workerBal, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal != 0 {
		t.Errorf("worker balance = %d; want 0 (payment should have been skipped as already done)", workerBal)
	}

	// Validator and treasury should have received their shares.
	valBal, _ := tl.Balance(crypto.AgentID("validator"))
	if valBal != 800 {
		t.Errorf("validator balance = %d; want 800", valBal)
	}
	treaBal, _ := tl.Balance(crypto.AgentID("treasury"))
	if treaBal != 200 {
		t.Errorf("treasury balance = %d; want 200", treaBal)
	}

	// Entry should be deleted after full disbursement.
	if _, err := e.Get("task1"); err == nil {
		t.Error("escrow entry should have been deleted after ReleaseNet completed")
	}
}

// TestReleaseNet_Idempotency_FullSuccess verifies that the second call after a
// successful full ReleaseNet returns ErrEscrowNotFound — preventing double payment
// even without paid-flag state (entry is deleted after success).
func TestReleaseNet_Idempotency_FullSuccess(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID("poster2"), 20_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	e := New(tl)
	if err := e.Hold("task2", crypto.AgentID("poster2"), 10_000); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	// First call succeeds.
	if err := e.ReleaseNet(
		"task2",
		crypto.AgentID("worker2"), 9_000,
		crypto.AgentID("validator2"), 800,
		crypto.AgentID("treasury2"), 200,
	); err != nil {
		t.Fatalf("first ReleaseNet: %v", err)
	}

	// Worker should have received 9 000.
	workerBal, _ := tl.Balance(crypto.AgentID("worker2"))
	if workerBal != 9_000 {
		t.Errorf("worker balance after first call = %d; want 9000", workerBal)
	}

	// Second call (retry) should return ErrEscrowNotFound — entry was deleted.
	err := e.ReleaseNet(
		"task2",
		crypto.AgentID("worker2"), 9_000,
		crypto.AgentID("validator2"), 800,
		crypto.AgentID("treasury2"), 200,
	)
	if err == nil {
		t.Fatal("second ReleaseNet should have returned an error (entry deleted)")
	}

	// Worker balance should still be 9 000 — not double-paid.
	workerBalAfter, _ := tl.Balance(crypto.AgentID("worker2"))
	if workerBalAfter != 9_000 {
		t.Errorf("worker balance after second call = %d; want 9000 (should not be double-paid)", workerBalAfter)
	}
}
