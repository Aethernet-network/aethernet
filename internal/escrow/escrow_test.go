package escrow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/escrow_testhelpers"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// newFundedLedger returns a TransferLedger with agentID pre-funded.
func newFundedLedger(agentID string, amount uint64) *ledger.TransferLedger {
	tl := ledger.NewTransferLedger()
	_ = tl.FundAgent(crypto.AgentID(agentID), amount)
	return tl
}

func TestEscrow_Empty(t *testing.T) {
	tl := newFundedLedger("alice", 10_000)
	e := escrow.New(tl)
	empty, err := e.Empty(context.Background())
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	if !empty {
		t.Fatalf("fresh escrow must be empty")
	}
	if err := escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "task-empty", "alice", 1_000); err != nil {
		t.Fatalf("FundAndRegisterEscrowForTest: %v", err)
	}
	empty, err = e.Empty(context.Background())
	if err != nil {
		t.Fatalf("Empty after register: %v", err)
	}
	if empty {
		t.Fatalf("escrow must not be empty after registration")
	}
}

// TestFundAndRegisterEscrowForTest verifies the combined fund-and-register
// semantics: funds move from poster to bucket AND an EscrowEntry is created.
// Replaces the legacy TestHold, which exercised the now-deprecated Hold
// method directly. The combined helper is the canonical test entry point.
func TestFundAndRegisterEscrowForTest(t *testing.T) {
	tl := newFundedLedger("alice", 10_000)
	e := escrow.New(tl)

	if err := escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "task1", "alice", 5_000); err != nil {
		t.Fatalf("FundAndRegisterEscrowForTest: %v", err)
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

	// Alice's balance should be reduced (funds moved into bucket).
	bal, _ := tl.Balance("alice")
	if bal > 5_000 {
		t.Errorf("balance after register = %d; should be ≤ 5000", bal)
	}
}

// TestFundAndRegisterEscrowForTest_InsufficientFunds verifies that ledger
// failures propagate and leave no stale registration behind.
func TestFundAndRegisterEscrowForTest_InsufficientFunds(t *testing.T) {
	tl := newFundedLedger("alice", 100)
	e := escrow.New(tl)

	err := escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "task1", "alice", 5_000)
	if err == nil {
		t.Fatal("expected error for insufficient funds")
	}
	_, getErr := e.Get("task1")
	if !errors.Is(getErr, escrow.ErrEscrowNotFound) {
		t.Errorf("expected ErrEscrowNotFound after failed register; got %v", getErr)
	}
}

func TestRelease(t *testing.T) {
	tl := newFundedLedger("alice", 10_000)
	e := escrow.New(tl)

	_ = escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "task2", "alice", 4_000)
	if err := e.Release("task2", "bob"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	bal, _ := tl.Balance("bob")
	if bal != 4_000 {
		t.Errorf("bob balance = %d; want 4000", bal)
	}

	_, err := e.Get("task2")
	if !errors.Is(err, escrow.ErrEscrowNotFound) {
		t.Errorf("expected ErrEscrowNotFound after release; got %v", err)
	}
}

func TestRefund(t *testing.T) {
	tl := newFundedLedger("alice", 10_000)
	e := escrow.New(tl)

	_ = escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "task3", "alice", 3_000)
	if err := e.Refund("task3"); err != nil {
		t.Fatalf("Refund: %v", err)
	}

	bal, _ := tl.Balance("alice")
	if bal != 10_000 {
		t.Errorf("alice balance after refund = %d; want 10000", bal)
	}

	_, err := e.Get("task3")
	if !errors.Is(err, escrow.ErrEscrowNotFound) {
		t.Errorf("expected ErrEscrowNotFound after refund; got %v", err)
	}
}

func TestTotalEscrowed(t *testing.T) {
	tl := newFundedLedger("alice", 50_000)
	e := escrow.New(tl)

	_ = escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "taskA", "alice", 5_000)
	_ = escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "taskB", "alice", 3_000)
	_ = escrow_testhelpers.FundAndRegisterEscrowForTest(tl, e, "taskC", "alice", 2_000)

	total := e.TotalEscrowed()
	if total != 10_000 {
		t.Errorf("TotalEscrowed = %d; want 10000", total)
	}
}
