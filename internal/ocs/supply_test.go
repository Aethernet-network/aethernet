package ocs_test

// Supply invariant tests for the OCS settlement engine.
//
// AetherNet's hard rule: total tokens in circulation must equal the genesis
// allocation at all times. These tests verify that:
//
//  1. OCS fee collection (Fix 2): CollectFeeFromRecipient moves tokens from
//     the recipient's balance to the validator and treasury — no new tokens
//     are minted. Total supply is unchanged after settlement.
//
//  2. OCS slash path (Fix 3): slashed stake is moved from the staking-pool
//     bucket to the treasury — no new tokens are minted. Total supply is
//     unchanged after a failed verification.

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/staking"
)

// balOf returns the current spendable balance for agentID (0 on error).
func balOf(h *testHarness, agentID string) uint64 {
	bal, _ := h.tl.Balance(crypto.AgentID(agentID))
	return bal
}

// TestSupplyInvariant_TransferWithFees verifies that fee collection during OCS
// transfer settlement does not create new tokens. The fee is deducted from the
// recipient's settled balance and redistributed to the validator and treasury.
// Total supply = genesis throughout.
func TestSupplyInvariant_TransferWithFees(t *testing.T) {
	const (
		genesis     = uint64(100_000) // only alice is funded at genesis
		transferAmt = uint64(50_000)  // alice → bob
	)

	const (
		alice     = "supply-alice"
		bob       = "supply-bob"
		validator = "supply-validator"
		treasury  = "supply-treasury"
	)

	h := newHarness(t, nil)

	// Genesis: only alice has funds. Total supply = genesis.
	fundAgent(t, h, alice, genesis)

	// Wire fee collector backed by the same transfer ledger.
	fc := fees.NewCollector(h.tl)
	h.eng.SetEconomics(fc, nil, crypto.AgentID(treasury))

	// Submit transfer alice → bob.
	ev := newTransferEvent(t, alice, bob, transferAmt, ocs.DefaultConfig().MinStakeRequired)
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Settle with a positive verdict; verifier is a third-party agent.
	res := ocs.VerificationResult{
		EventID:    ev.ID,
		Verdict:    true,
		VerifierID: crypto.AgentID(validator),
		Timestamp:  time.Now(),
	}
	if err := h.eng.ProcessResult(res); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	aliceBal := balOf(h, alice)
	bobBal   := balOf(h, bob)
	valBal   := balOf(h, validator)
	trsBal   := balOf(h, treasury)
	total    := aliceBal + bobBal + valBal + trsBal

	if total != genesis {
		t.Errorf("supply invariant violated: genesis=%d, total=%d (alice=%d bob=%d validator=%d treasury=%d)",
			genesis, total, aliceBal, bobBal, valBal, trsBal)
	}

	// Verify individual balances are arithmetically consistent.
	fee        := fees.CalculateFee(transferAmt) // 50_000 * 10 / 10_000 = 50
	wantAlice  := genesis - transferAmt           // 50_000
	wantBob    := transferAmt - fee               // 49_950
	if aliceBal != wantAlice {
		t.Errorf("alice balance = %d, want %d", aliceBal, wantAlice)
	}
	if bobBal != wantBob {
		t.Errorf("bob balance = %d, want %d", bobBal, wantBob)
	}
	if valBal+trsBal != fee {
		t.Errorf("validator+treasury = %d, want fee=%d", valBal+trsBal, fee)
	}
}

// TestSupplyInvariant_SlashToTreasury verifies that slashed stake is moved from
// the staking-pool bucket to the treasury rather than minted. When a Transfer
// verdict is negative the sender's full stake is slashed and the tokens are
// transferred from staking-pool → treasury. Total supply is unchanged.
func TestSupplyInvariant_SlashToTreasury(t *testing.T) {
	const (
		aliceFunds = uint64(200_000) // alice's initial balance
		stakeAmt   = uint64(50_000)  // alice stakes 50 AET
		transferAmt = uint64(30_000) // attempted transfer (will be rejected)
	)

	const (
		alice    = "slash-alice"
		bob      = "slash-bob"
		treasury = "slash-treasury"
	)

	h := newHarness(t, nil)

	// Genesis: fund alice. Total supply = aliceFunds.
	fundAgent(t, h, alice, aliceFunds)

	// Wire stake manager with the transfer ledger so Stake() debits alice's
	// balance into the staking-pool bucket (supply-safe movement, not a mint).
	sm := staking.NewStakeManager()
	sm.SetTransferLedger(h.tl)

	// Wire economics: fee collector (no fees on Adjusted path) + stake manager.
	fc := fees.NewCollector(h.tl)
	h.eng.SetEconomics(fc, sm, crypto.AgentID(treasury))

	// Alice stakes: debit alice's balance, credit staking-pool bucket.
	if err := sm.Stake(crypto.AgentID(alice), stakeAmt); err != nil {
		t.Fatalf("Stake: %v", err)
	}

	// Verify supply is still conserved after staking (tokens just moved).
	aliceBalAfterStake := balOf(h, alice)
	poolBalAfterStake  := balOf(h, "staking-pool")
	if aliceBalAfterStake+poolBalAfterStake != aliceFunds {
		t.Errorf("post-stake supply mismatch: alice=%d pool=%d want total=%d",
			aliceBalAfterStake, poolBalAfterStake, aliceFunds)
	}

	// Submit a transfer that will be rejected (fraudulent claim).
	ev := newTransferEvent(t, alice, bob, transferAmt, sm.StakedAmount(crypto.AgentID(alice)))
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Verdict false: transfer is adjusted (reversed) and alice's stake is slashed.
	res := ocs.VerificationResult{
		EventID:   ev.ID,
		Verdict:   false,
		Reason:    "fraudulent transfer",
		Timestamp: time.Now(),
	}
	if err := h.eng.ProcessResult(res); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	aliceBal := balOf(h, alice)
	bobBal   := balOf(h, bob)
	poolBal  := balOf(h, "staking-pool")
	trsBal   := balOf(h, treasury)
	total    := aliceBal + bobBal + poolBal + trsBal

	if total != aliceFunds {
		t.Errorf("supply invariant violated after slash: genesis=%d, total=%d (alice=%d bob=%d pool=%d treasury=%d)",
			aliceFunds, total, aliceBal, bobBal, poolBal, trsBal)
	}

	// Sanity: the transfer was reversed so bob received nothing.
	if bobBal != 0 {
		t.Errorf("bob balance = %d, want 0 (transfer was adjusted)", bobBal)
	}
	// Sanity: the slashed stake should now be in treasury.
	if trsBal != stakeAmt {
		t.Errorf("treasury balance = %d, want %d (full slash)", trsBal, stakeAmt)
	}
}
