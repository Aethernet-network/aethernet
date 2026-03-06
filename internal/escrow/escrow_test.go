package escrow_test

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// newFundedLedger returns a TransferLedger with agentID pre-funded.
func newFundedLedger(agentID string, amount uint64) *ledger.TransferLedger {
	tl := ledger.NewTransferLedger()
	_ = tl.FundAgent(crypto.AgentID(agentID), amount)
	return tl
}

func TestHold(t *testing.T) {
	tl := newFundedLedger("alice", 10_000)
	e := escrow.New(tl)

	if err := e.Hold("task1", "alice", 5_000); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	entry, err := e.Get("task1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.PosterID != "alice" {
		t.Errorf("PosterID = %q; want alice", entry.PosterID)
	}
	if entry.Amount != 5_000 {
		t.Errorf("Amount = %d; want 5000", entry.Amount)
	}

	// Alice's balance should be reduced.
	bal, _ := tl.Balance("alice")
	if bal > 5_000 {
		t.Errorf("balance after hold = %d; should be ≤ 5000", bal)
	}
}

func TestHold_InsufficientFunds(t *testing.T) {
	tl := newFundedLedger("alice", 100)
	e := escrow.New(tl)

	err := e.Hold("task1", "alice", 5_000)
	if err == nil {
		t.Fatal("expected error for insufficient funds")
	}
	// Entry should not have been created.
	_, getErr := e.Get("task1")
	if !errors.Is(getErr, escrow.ErrEscrowNotFound) {
		t.Errorf("expected ErrEscrowNotFound after failed hold; got %v", getErr)
	}
}

func TestRelease(t *testing.T) {
	tl := newFundedLedger("alice", 10_000)
	e := escrow.New(tl)

	_ = e.Hold("task2", "alice", 4_000)
	if err := e.Release("task2", "bob"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Bob should have received 4000.
	bal, _ := tl.Balance("bob")
	if bal != 4_000 {
		t.Errorf("bob balance = %d; want 4000", bal)
	}

	// Entry should be gone.
	_, err := e.Get("task2")
	if !errors.Is(err, escrow.ErrEscrowNotFound) {
		t.Errorf("expected ErrEscrowNotFound after release; got %v", err)
	}
}

func TestRefund(t *testing.T) {
	tl := newFundedLedger("alice", 10_000)
	e := escrow.New(tl)

	_ = e.Hold("task3", "alice", 3_000)
	if err := e.Refund("task3"); err != nil {
		t.Fatalf("Refund: %v", err)
	}

	// Alice should have her 3000 back (balance should be 10000 again).
	bal, _ := tl.Balance("alice")
	if bal != 10_000 {
		t.Errorf("alice balance after refund = %d; want 10000", bal)
	}

	// Entry should be gone.
	_, err := e.Get("task3")
	if !errors.Is(err, escrow.ErrEscrowNotFound) {
		t.Errorf("expected ErrEscrowNotFound after refund; got %v", err)
	}
}

func TestTotalEscrowed(t *testing.T) {
	tl := newFundedLedger("alice", 50_000)
	e := escrow.New(tl)

	_ = e.Hold("taskA", "alice", 5_000)
	_ = e.Hold("taskB", "alice", 3_000)
	_ = e.Hold("taskC", "alice", 2_000)

	total := e.TotalEscrowed()
	if total != 10_000 {
		t.Errorf("TotalEscrowed = %d; want 10000", total)
	}
}
