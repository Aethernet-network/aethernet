package staking

// Whitebox test for Unstake atomicity (CRITICAL-2). Uses the internal package
// so it can access unexported fields (stakes map, mu) to set up the test
// scenario: in-memory stake exists but the staking-pool ledger bucket has zero
// balance, which causes the ledger credit to fail. The new implementation
// aborts without modifying in-memory state; the old one would decrement the
// stake and then silently log the ledger failure, creating an inconsistent state.

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// TestUnstake_Atomicity_LedgerFailure verifies that if the ledger credit fails
// during Unstake, the in-memory stake is left unchanged (no silent fund loss).
func TestUnstake_Atomicity_LedgerFailure(t *testing.T) {
	// Build a funded ledger. Alice has 10 000 µAET but the staking-pool bucket
	// has ZERO balance (we never called Stake, so the pool was never filled).
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID("alice"), 10_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	sm := NewStakeManager()
	sm.SetTransferLedger(tl)

	// Directly set alice's in-memory stake to 5 000 WITHOUT going through Stake()
	// so that the staking-pool has no corresponding balance. This replicates an
	// inconsistent state (or the pre-fix Unstake where memory was modified before
	// the ledger credit attempt, leaving pool empty when credit was retried).
	sm.mu.Lock()
	sm.stakes[crypto.AgentID("alice")] = 5_000
	sm.mu.Unlock()

	// Attempt to unstake. The staking-pool has 0 balance so the ledger credit
	// will fail. The new code must abort with no state change and return false.
	result := sm.Unstake(crypto.AgentID("alice"), 5_000)
	if result {
		t.Fatal("Unstake returned true, but ledger credit should have failed (pool has 0 balance)")
	}

	// Alice's in-memory stake must be unchanged at 5 000.
	if got := sm.StakedAmount(crypto.AgentID("alice")); got != 5_000 {
		t.Errorf("stake after failed unstake = %d; want 5000 (stake should be unchanged)", got)
	}

	// Alice's ledger balance must also be unchanged at 10 000 (no tokens moved).
	bal, err := tl.Balance(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 10_000 {
		t.Errorf("alice balance after failed unstake = %d; want 10000 (no tokens moved)", bal)
	}
}

// TestUnstake_Atomicity_Success verifies that a normal unstake (pool has funds)
// still works correctly: stake decremented, ledger credited.
func TestUnstake_Atomicity_Success(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID("bob"), 10_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	sm := NewStakeManager()
	sm.SetTransferLedger(tl)

	// Stake normally — this fills the staking-pool bucket.
	if err := sm.Stake(crypto.AgentID("bob"), 4_000); err != nil {
		t.Fatalf("Stake: %v", err)
	}
	if got := sm.StakedAmount(crypto.AgentID("bob")); got != 4_000 {
		t.Fatalf("stake after Stake = %d; want 4000", got)
	}

	// Unstake — pool has the funds, so credit should succeed.
	if !sm.Unstake(crypto.AgentID("bob"), 4_000) {
		t.Fatal("Unstake returned false; expected true (pool is funded)")
	}

	// Stake should be zero now.
	if got := sm.StakedAmount(crypto.AgentID("bob")); got != 0 {
		t.Errorf("stake after successful unstake = %d; want 0", got)
	}

	// Bob's balance should be restored to 10 000.
	bal, err := tl.Balance(crypto.AgentID("bob"))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 10_000 {
		t.Errorf("bob balance after unstake = %d; want 10000", bal)
	}
}
