package staking_test

// Security tests for the staking package.
//
// These tests verify CRITICAL-1.2: Phantom Staking — a failed ledger debit
// must not silently produce recorded collateral.

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/staking"
)

// TestStake_FailedLedgerDebitReturnsError verifies that when a TransferLedger
// is wired in and the debit fails (insufficient balance), Stake() returns an
// error and leaves the staked amount unchanged (CRITICAL-1.2).
func TestStake_FailedLedgerDebitReturnsError(t *testing.T) {
	sm := staking.NewStakeManager()

	// Wire a real TransferLedger without funding the agent — any debit will fail.
	tl := ledger.NewTransferLedger()
	sm.SetTransferLedger(tl)

	id := crypto.AgentID("broke-agent")
	err := sm.Stake(id, 1_000)
	if err == nil {
		t.Fatal("Stake with insufficient balance: expected error, got nil")
	}

	// No collateral must be recorded when the debit fails.
	if got := sm.StakedAmount(id); got != 0 {
		t.Errorf("stake recorded despite failed debit: StakedAmount = %d, want 0", got)
	}
}

// TestStake_SucceedsWhenFunded verifies the positive path: when the agent has
// sufficient balance, Stake() returns nil and records the staked amount.
func TestStake_SucceedsWhenFunded(t *testing.T) {
	sm := staking.NewStakeManager()

	tl := ledger.NewTransferLedger()
	sm.SetTransferLedger(tl)

	id := crypto.AgentID("funded-agent")
	if err := tl.FundAgent(id, 5_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	if err := sm.Stake(id, 1_000); err != nil {
		t.Fatalf("Stake(1000) with sufficient balance: %v", err)
	}
	if got := sm.StakedAmount(id); got != 1_000 {
		t.Errorf("StakedAmount = %d, want 1000", got)
	}
}
