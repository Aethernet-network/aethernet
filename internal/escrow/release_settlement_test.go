package escrow

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

func setupSettlementTest(t *testing.T, budget uint64) (*Escrow, *ledger.TransferLedger) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	_ = tl.FundAgent("poster", budget*2)
	e := New(tl)
	// Move funds into escrow bucket (simulates canonical Transfer).
	_ = tl.TransferFromBucket("poster", crypto.AgentID("escrow:task-1"), budget)
	_ = e.RegisterEscrowForTest("task-1", "poster", budget, event.EventID("test-funding:task-1"))
	return e, tl
}

func TestReleaseSettlement_AcceptPath(t *testing.T) {
	budget := uint64(10000)
	e, tl := setupSettlementTest(t, budget)

	workerAmount := budget * 7300 / 10000   // 7300
	validatorPool := budget * 2300 / 10000  // 2300
	genPool := budget * 200 / 10000         // 200
	treasuryAmount := budget - workerAmount - validatorPool - genPool // 200

	validators := map[crypto.AgentID]uint64{
		"validator-1": validatorPool / 2,
		"validator-2": validatorPool - validatorPool/2,
	}
	genRecipients := map[crypto.AgentID]uint64{
		"gen-recipient": genPool,
	}

	err := e.ReleaseSettlement("task-1",
		"worker", workerAmount,
		"poster", 0, // no poster refund on accept
		validators,
		genRecipients,
		"treasury", treasuryAmount,
	)
	if err != nil {
		t.Fatalf("ReleaseSettlement: %v", err)
	}

	// Verify balances.
	assertBalance(t, tl, "worker", workerAmount)
	assertBalance(t, tl, "validator-1", validatorPool/2)
	assertBalance(t, tl, "validator-2", validatorPool-validatorPool/2)
	assertBalance(t, tl, "gen-recipient", genPool)
	assertBalance(t, tl, "treasury", treasuryAmount)

	// Escrow bucket should be empty.
	bucketBal, _ := tl.Balance(crypto.AgentID("escrow:task-1"))
	if bucketBal != 0 {
		t.Errorf("escrow bucket balance: got %d want 0", bucketBal)
	}

	// Sum must equal budget.
	total := workerAmount + validatorPool/2 + (validatorPool - validatorPool/2) + genPool + treasuryAmount
	if total != budget {
		t.Errorf("total distributed %d != budget %d", total, budget)
	}

	// Entry should be deleted.
	if e.IsLocked("task-1") {
		t.Error("escrow entry should be deleted after settlement")
	}
}

func TestReleaseSettlement_RejectPath(t *testing.T) {
	budget := uint64(10000)
	e, tl := setupSettlementTest(t, budget)

	posterRefund := budget * 7300 / 10000
	validatorPool := budget * 2300 / 10000
	treasuryAmount := budget - posterRefund - validatorPool

	validators := map[crypto.AgentID]uint64{"validator-1": validatorPool}

	err := e.ReleaseSettlement("task-1",
		"worker", 0, // no worker payout on reject
		"poster", posterRefund,
		validators,
		nil, // no gen-ledger on reject
		"treasury", treasuryAmount,
	)
	if err != nil {
		t.Fatalf("ReleaseSettlement: %v", err)
	}

	assertBalance(t, tl, "poster", budget*2-budget+posterRefund) // original - escrowed + refund
	assertBalance(t, tl, "validator-1", validatorPool)
	assertBalance(t, tl, "treasury", treasuryAmount)
}

func TestReleaseSettlement_DisputePath(t *testing.T) {
	budget := uint64(10000)
	e, tl := setupSettlementTest(t, budget)

	workerPortion := budget * 7300 / 10000
	workerHalf := workerPortion / 2
	posterHalf := workerPortion - workerHalf
	treasuryAmount := budget - workerPortion

	err := e.ReleaseSettlement("task-1",
		"worker", workerHalf,
		"poster", posterHalf,
		nil, // no validator payouts on dispute
		nil, // no gen-ledger on dispute
		"treasury", treasuryAmount,
	)
	if err != nil {
		t.Fatalf("ReleaseSettlement: %v", err)
	}

	assertBalance(t, tl, "worker", workerHalf)
	total := workerHalf + posterHalf + treasuryAmount
	if total != budget {
		t.Errorf("total distributed %d != budget %d", total, budget)
	}
}

func TestReleaseSettlement_CrashAfterWorker_RetrySkipsWorker(t *testing.T) {
	budget := uint64(10000)
	e, tl := setupSettlementTest(t, budget)

	// Simulate crash after worker paid: set WorkerPaid=true manually.
	e.mu.Lock()
	entry := e.entries["task-1"]
	entry.WorkerPaid = true
	e.mu.Unlock()

	workerAmount := uint64(7300)
	treasuryAmount := budget - workerAmount

	// Retry: worker should be skipped.
	err := e.ReleaseSettlement("task-1",
		"worker", workerAmount,
		"poster", 0,
		nil, nil,
		"treasury", treasuryAmount,
	)
	if err != nil {
		t.Fatalf("ReleaseSettlement retry: %v", err)
	}

	// Worker should have 0 (transfer was not actually made in this test,
	// flag was set manually to simulate crash-after-persist).
	workerBal, _ := tl.Balance(crypto.AgentID("worker"))
	if workerBal != 0 {
		t.Errorf("worker balance on retry: got %d want 0 (skipped)", workerBal)
	}
}

func TestReleaseSettlement_CrashAfterSomeValidators_RetrySkipsPaid(t *testing.T) {
	budget := uint64(10000)
	e, tl := setupSettlementTest(t, budget)

	// Simulate: v1 paid, v2 not paid yet.
	e.mu.Lock()
	entry := e.entries["task-1"]
	entry.ValidatorsPaid = map[string]bool{"validator-1": true}
	e.mu.Unlock()

	validators := map[crypto.AgentID]uint64{
		"validator-1": 1000,
		"validator-2": 1300,
	}
	treasuryAmount := budget - 1000 - 1300 - 7300 // remaining

	err := e.ReleaseSettlement("task-1",
		"worker", 7300,
		"poster", 0,
		validators,
		nil,
		"treasury", treasuryAmount,
	)
	if err != nil {
		t.Fatalf("ReleaseSettlement retry: %v", err)
	}

	// v1 should have 0 (skipped on retry). v2 should have 1300.
	v1Bal, _ := tl.Balance(crypto.AgentID("validator-1"))
	if v1Bal != 0 {
		t.Errorf("validator-1 on retry: got %d want 0", v1Bal)
	}
	v2Bal, _ := tl.Balance(crypto.AgentID("validator-2"))
	if v2Bal != 1300 {
		t.Errorf("validator-2 on retry: got %d want 1300", v2Bal)
	}
}

func TestReleaseSettlement_AllPaid_NoOp(t *testing.T) {
	budget := uint64(10000)
	e, _ := setupSettlementTest(t, budget)

	// Mark everything as paid.
	e.mu.Lock()
	entry := e.entries["task-1"]
	entry.WorkerPaid = true
	entry.TreasuryPaid = true
	entry.ValidatorsPaid = map[string]bool{"v1": true}
	e.mu.Unlock()

	err := e.ReleaseSettlement("task-1",
		"worker", 7300,
		"poster", 0,
		map[crypto.AgentID]uint64{"v1": 2300},
		nil,
		"treasury", 400,
	)
	if err != nil {
		t.Fatalf("ReleaseSettlement all-paid: %v", err)
	}

	// Entry should be deleted.
	if e.IsLocked("task-1") {
		t.Error("entry should be deleted after all-paid no-op")
	}
}

func TestReleaseSettlement_NotFound(t *testing.T) {
	tl := ledger.NewTransferLedger()
	e := New(tl)

	err := e.ReleaseSettlement("nonexistent",
		"worker", 100, "poster", 0, nil, nil, "treasury", 50)
	if err == nil {
		t.Fatal("expected ErrEscrowNotFound")
	}
}

func assertBalance(t *testing.T, tl *ledger.TransferLedger, agent crypto.AgentID, want uint64) {
	t.Helper()
	got, _ := tl.Balance(agent)
	if got != want {
		t.Errorf("balance of %s: got %d want %d", agent, got, want)
	}
}
