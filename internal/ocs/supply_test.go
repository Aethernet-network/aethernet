package ocs_test

// Supply invariant tests have moved to the SettlementApplicator (internal/settlement)
// which is the single authority for fee collection, staking, and ledger mutation.
// The OCS engine no longer handles economic side effects.
//
// This file retains basic pending-queue supply tests.

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ocs"
)

// TestSupplyInvariant_SubmitDoesNotMutateBalance verifies that Submit does NOT
// create a ledger entry or debit the sender's balance.
func TestSupplyInvariant_SubmitDoesNotMutateBalance(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)

	balBefore := balOf(h, "alice")

	ev := newTransferEvent(t, "alice", "bob", 50_000, ocs.DefaultConfig().MinStakeRequired)
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	balAfter := balOf(h, "alice")
	if balAfter != balBefore {
		t.Errorf("alice balance changed after Submit: before=%d after=%d; Submit must not mutate ledger",
			balBefore, balAfter)
	}
}

func balOf(h *testHarness, agentID string) uint64 {
	bal, _ := h.tl.Balance(crypto.AgentID(agentID))
	return bal
}
