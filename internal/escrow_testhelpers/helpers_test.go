package escrow_testhelpers_test

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/escrow_testhelpers"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

func TestFundAndRegisterEscrowForTest_MovesFunds(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent("poster", 1000); err != nil {
		t.Fatalf("fund: %v", err)
	}
	esc := escrow.New(tl)

	if err := escrow_testhelpers.FundAndRegisterEscrowForTest(tl, esc, "task1", crypto.AgentID("poster"), 100); err != nil {
		t.Fatalf("helper: %v", err)
	}
	bal, _ := tl.Balance(crypto.AgentID("poster"))
	if bal != 900 {
		t.Errorf("poster balance: got %d want 900", bal)
	}
	bucketBal, _ := tl.Balance(crypto.AgentID("escrow:task1"))
	if bucketBal != 100 {
		t.Errorf("bucket balance: got %d want 100", bucketBal)
	}
}

func TestFundAndRegisterEscrowForTest_RegistersEntry(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent("poster", 1000); err != nil {
		t.Fatalf("fund: %v", err)
	}
	esc := escrow.New(tl)

	if err := escrow_testhelpers.FundAndRegisterEscrowForTest(tl, esc, "task1", crypto.AgentID("poster"), 100); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if !esc.IsLocked("task1") {
		t.Error("IsLocked returned false after helper")
	}
}

func TestFundAndRegisterEscrowForTest_PropagatesLedgerError(t *testing.T) {
	tl := ledger.NewTransferLedger()
	// Seed only 50, attempt to escrow 100 → insufficient balance.
	if err := tl.FundAgent("poster", 50); err != nil {
		t.Fatalf("fund: %v", err)
	}
	esc := escrow.New(tl)

	err := escrow_testhelpers.FundAndRegisterEscrowForTest(tl, esc, "task1", crypto.AgentID("poster"), 100)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Errorf("want wrapped ErrInsufficientBalance, got %v", err)
	}
	if esc.IsLocked("task1") {
		t.Error("entry registered despite ledger failure")
	}
}
