package ocs_test

// Consensus integration tests for the OCS settlement engine.
//
// These tests verify that reputation-weighted BFT consensus is correctly wired
// into the settlement path: votes accumulate, supermajority triggers finalization,
// timeouts fall back to majority decision, and vote propagation callbacks fire.

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ocs"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// registerVoter adds an agent to reg with explicit ReputationScore and
// StakedAmount so that the VotingRound weight function returns a useful value.
// weight = (rep × stake) / 10000
func registerVoter(t *testing.T, reg *identity.Registry, agentID crypto.AgentID, rep, stake uint64) {
	t.Helper()
	fp, err := identity.NewFingerprint(agentID, make([]byte, 32), nil)
	if err != nil {
		t.Fatalf("NewFingerprint(%s): %v", agentID, err)
	}
	fp.ReputationScore = rep
	fp.StakedAmount = stake
	if err := reg.Register(fp); err != nil {
		t.Fatalf("Register(%s): %v", agentID, err)
	}
}

// newConsensusRound returns a VotingRound configured with the given
// MinParticipants and backed by reg.
func newConsensusRound(reg *identity.Registry, minParticipants int) *consensus.VotingRound {
	cfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        minParticipants,
	}
	return consensus.NewVotingRound(cfg, reg)
}

// ---------------------------------------------------------------------------
// Test 1: Single-node backward compatibility
//
// When no consensus engine is set (voting == nil), ProcessVote delegates
// directly to ProcessResult — identical to the pre-consensus behaviour.
// ---------------------------------------------------------------------------

func TestConsensus_SingleNodeBackwardCompat(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)
	registerAgent(t, h.reg, "alice")
	registerAgent(t, h.reg, "bob")

	ev := newTransferEvent(t, "alice", "bob", 10_000, ocs.DefaultConfig().MinStakeRequired)
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// No consensus set — ProcessVote must settle immediately.
	err := h.eng.ProcessVote(ocs.VerificationResult{
		EventID:    ev.ID,
		Verdict:    true,
		VerifierID: "bob", // not party to the transfer (bob is recipient, check fails — wait, bob IS recipient)
		Timestamp:  time.Now(),
	})
	// bob is the recipient — self-dealing check fires.
	// Use a third-party verifier.
	if err != nil {
		// That's fine — self-dealing. Re-do with a neutral verifier.
	}

	// Re-submit and use a neutral verifier.
	h2 := newHarness(t, nil)
	fundAgent(t, h2, "alice", 100_000)
	registerAgent(t, h2.reg, "alice")
	registerAgent(t, h2.reg, "bob")
	registerAgent(t, h2.reg, "validator")

	ev2 := newTransferEvent(t, "alice", "bob", 10_000, ocs.DefaultConfig().MinStakeRequired)
	if err := h2.eng.Submit(ev2); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Without consensus: single ProcessVote call should settle immediately.
	if err := h2.eng.ProcessVote(ocs.VerificationResult{
		EventID:    ev2.ID,
		Verdict:    true,
		VerifierID: "validator",
		Timestamp:  time.Now(),
	}); err != nil {
		t.Fatalf("ProcessVote (no consensus): %v", err)
	}

	// Confirm settlement: event must be gone from pending.
	if h2.eng.IsPending(ev2.ID) {
		t.Error("event still pending after single-node ProcessVote — expected immediate settlement")
	}
}

// ---------------------------------------------------------------------------
// Test 2: Multi-node simulation — supermajority triggers settlement
//
// Three voters with equal weight. Two votes accumulate without settling.
// The third vote reaches supermajority (3/3 = 100% > 66.7%) and the event
// is settled exactly once.
// ---------------------------------------------------------------------------

func TestConsensus_MultiNodeSupermajority(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)
	registerAgent(t, h.reg, "alice")
	registerAgent(t, h.reg, "bob")

	// Register three validators with equal weight in the same registry.
	for _, vid := range []crypto.AgentID{"v1", "v2", "v3"} {
		registerVoter(t, h.reg, vid, 5000, 2000) // weight = 5000*2000/10000 = 1000
	}

	// Wire a VotingRound requiring 3 participants.
	vr := newConsensusRound(h.reg, 3)
	h.eng.SetConsensus(vr)

	ev := newTransferEvent(t, "alice", "bob", 10_000, ocs.DefaultConfig().MinStakeRequired)
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// First two votes must NOT settle.
	for _, vid := range []crypto.AgentID{"v1", "v2"} {
		if err := h.eng.ProcessVote(ocs.VerificationResult{
			EventID:    ev.ID,
			Verdict:    true,
			VerifierID: vid,
			Timestamp:  time.Now(),
		}); err != nil {
			t.Fatalf("ProcessVote(%s): %v", vid, err)
		}
		if !h.eng.IsPending(ev.ID) {
			t.Errorf("event settled prematurely after vote from %s — need supermajority", vid)
		}
	}

	// Third vote reaches supermajority → settlement.
	if err := h.eng.ProcessVote(ocs.VerificationResult{
		EventID:    ev.ID,
		Verdict:    true,
		VerifierID: "v3",
		Timestamp:  time.Now(),
	}); err != nil {
		t.Fatalf("ProcessVote(v3): %v", err)
	}

	if h.eng.IsPending(ev.ID) {
		t.Error("event still pending after 3/3 supermajority — expected settlement")
	}
}

// ---------------------------------------------------------------------------
// Test 3: Multi-node timeout — only 1 vote arrives within the deadline
//
// Consensus requires 3 participants (MinParticipants=3). Only one validator
// votes. When the OCS deadline fires, checkExpired uses vote counts:
// 1 yes, 0 no → yes majority → event settled with Verdict=true.
// ---------------------------------------------------------------------------

func TestConsensus_TimeoutWithSingleVote(t *testing.T) {
	// Use a very short verification timeout so the deadline sweep fires quickly.
	cfg := ocs.DefaultConfig()
	cfg.VerificationTimeout = 50 * time.Millisecond
	cfg.CheckInterval = 10 * time.Millisecond

	h := newHarness(t, cfg)
	fundAgent(t, h, "alice", 100_000)
	registerAgent(t, h.reg, "alice")
	registerAgent(t, h.reg, "bob")

	// Register 3 voters (MinParticipants=3) but only one will vote.
	for _, vid := range []crypto.AgentID{"v1", "v2", "v3"} {
		registerVoter(t, h.reg, vid, 5000, 2000)
	}

	vr := newConsensusRound(h.reg, 3)
	h.eng.SetConsensus(vr)

	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.eng.Stop()

	ev := newTransferEvent(t, "alice", "bob", 10_000, cfg.MinStakeRequired)
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// One yes vote — below MinParticipants threshold.
	if err := h.eng.ProcessVote(ocs.VerificationResult{
		EventID:    ev.ID,
		Verdict:    true,
		VerifierID: "v1",
		Timestamp:  time.Now(),
	}); err != nil {
		t.Fatalf("ProcessVote(v1): %v", err)
	}

	if !h.eng.IsPending(ev.ID) {
		t.Fatal("event should still be pending (supermajority not reached)")
	}

	// Wait for the deadline sweep to fire and resolve via majority decision.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !h.eng.IsPending(ev.ID) {
			return // settled — pass
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("event still pending after timeout — expected majority-based settlement")
}

// ---------------------------------------------------------------------------
// Test 4: Vote propagation — broadcast callback fires on local vote
//
// After registering a local vote that does NOT yet reach supermajority, the
// broadcastVote callback must be invoked with the correct arguments.
// ---------------------------------------------------------------------------

func TestConsensus_VotePropagation(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)
	registerAgent(t, h.reg, "alice")
	registerAgent(t, h.reg, "bob")

	// Register voters — MinParticipants=3 means first vote won't finalize.
	for _, vid := range []crypto.AgentID{"v1", "v2", "v3"} {
		registerVoter(t, h.reg, vid, 5000, 2000)
	}

	vr := newConsensusRound(h.reg, 3)
	h.eng.SetConsensus(vr)

	// Capture broadcast calls.
	type broadcastRecord struct {
		eventID event.EventID
		verdict bool
		voterID crypto.AgentID
	}
	var broadcasts []broadcastRecord
	h.eng.SetVoteBroadcaster(func(eventID event.EventID, verdict bool, voterID crypto.AgentID) {
		broadcasts = append(broadcasts, broadcastRecord{eventID, verdict, voterID})
	})

	ev := newTransferEvent(t, "alice", "bob", 10_000, ocs.DefaultConfig().MinStakeRequired)
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// First local vote — does not reach supermajority.
	if err := h.eng.ProcessVote(ocs.VerificationResult{
		EventID:    ev.ID,
		Verdict:    true,
		VerifierID: "v1",
		Timestamp:  time.Now(),
	}); err != nil {
		t.Fatalf("ProcessVote(v1): %v", err)
	}

	if len(broadcasts) != 1 {
		t.Fatalf("want 1 broadcast after first vote, got %d", len(broadcasts))
	}
	b := broadcasts[0]
	if b.eventID != ev.ID {
		t.Errorf("broadcast eventID: want %s, got %s", ev.ID, b.eventID)
	}
	if !b.verdict {
		t.Error("broadcast verdict: want true, got false")
	}
	if b.voterID != "v1" {
		t.Errorf("broadcast voterID: want v1, got %s", b.voterID)
	}

	// AcceptPeerVote (received from peer) must NOT trigger another broadcast.
	if err := h.eng.AcceptPeerVote(ev.ID, "v2", true); err != nil {
		t.Fatalf("AcceptPeerVote(v2): %v", err)
	}
	if len(broadcasts) != 1 {
		t.Errorf("AcceptPeerVote must not broadcast; got %d broadcasts (want 1)", len(broadcasts))
	}
}

// ---------------------------------------------------------------------------
// Test 5: Reputation weighting — high-rep voter overrides low-rep majority
//
// One high-rep validator (weight=1000) votes YES.
// Two low-rep validators (weight=100 each) vote NO.
// yesWeight=1000, totalWeight=1200, ratio=0.833 ≥ 0.667 → YES supermajority.
// The high-reputation validator's single YES beats two low-rep NO votes.
// ---------------------------------------------------------------------------

func TestConsensus_ReputationWeighting(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)
	registerAgent(t, h.reg, "alice")
	registerAgent(t, h.reg, "bob")

	// High-rep validator: rep=5000, stake=2000 → weight = 5000*2000/10000 = 1000
	registerVoter(t, h.reg, "high-rep", 5000, 2000)
	// Two low-rep validators: rep=500, stake=2000 → weight = 500*2000/10000 = 100
	registerVoter(t, h.reg, "low-rep-1", 500, 2000)
	registerVoter(t, h.reg, "low-rep-2", 500, 2000)

	// MinParticipants=3 so all three votes are needed for finalization.
	vr := newConsensusRound(h.reg, 3)
	h.eng.SetConsensus(vr)

	ev := newTransferEvent(t, "alice", "bob", 10_000, ocs.DefaultConfig().MinStakeRequired)
	if err := h.eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Two low-rep NO votes — still not finalized (only 2 participants).
	for _, vid := range []crypto.AgentID{"low-rep-1", "low-rep-2"} {
		if err := h.eng.ProcessVote(ocs.VerificationResult{
			EventID:    ev.ID,
			Verdict:    false, // NO vote
			VerifierID: vid,
			Timestamp:  time.Now(),
		}); err != nil {
			t.Fatalf("ProcessVote(%s): %v", vid, err)
		}
	}

	if !h.eng.IsPending(ev.ID) {
		t.Fatal("event should still be pending after 2/3 votes")
	}

	// High-rep YES vote: yesWeight=1000, noWeight=200, totalWeight=1200.
	// ratio = 1000/1200 = 0.833 ≥ 0.667 → YES supermajority → accept.
	if err := h.eng.ProcessVote(ocs.VerificationResult{
		EventID:    ev.ID,
		Verdict:    true, // YES vote
		VerifierID: "high-rep",
		Timestamp:  time.Now(),
	}); err != nil {
		t.Fatalf("ProcessVote(high-rep): %v", err)
	}

	if h.eng.IsPending(ev.ID) {
		t.Error("event still pending — high-rep YES should have reached supermajority")
	}

	// Verify the vote record shows the expected weight distribution.
	rec, err := vr.GetRecord(ev.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if !rec.Finalized {
		t.Error("VoteRecord not marked Finalized after supermajority")
	}
	// yesWeight=1000 from high-rep, totalWeight=1000+100+100=1200.
	if rec.YesWeight != 1000 {
		t.Errorf("YesWeight: want 1000, got %d", rec.YesWeight)
	}
	if rec.TotalWeight != 1200 {
		t.Errorf("TotalWeight: want 1200, got %d", rec.TotalWeight)
	}
}
