package ocs

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// registerVoter adds a voter to the identity registry with given rep and stake.
func registerVoter(t *testing.T, reg *identity.Registry, id crypto.AgentID, rep, stake uint64) {
	t.Helper()
	fp := &identity.CapabilityFingerprint{
		AgentID:              id,
		PublicKey:            make([]byte, 32),
		ReputationScore:      rep,
		StakedAmount:         stake,
		OptimisticTrustLimit: 1000,
		Capabilities:         []identity.Capability{},
		FirstSeen:            time.Now(),
		LastActive:           time.Now(),
		FingerprintVersion:   1,
	}
	_ = reg.Register(fp)
}

func TestFinalizationHandler_FiresOnSupermajority(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	// Register 3 voters with equal weight.
	registerVoter(t, reg, "v1", 5000, 2000)
	registerVoter(t, reg, "v2", 5000, 2000)
	registerVoter(t, reg, "v3", 5000, 2000)

	// Fund sender so OCS balance check passes.
	_ = tl.FundAgent("alice", 100_000)

	eng := NewEngine(DefaultConfig(), tl, gl, reg)

	// Wire consensus with MinParticipants=3.
	votingCfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        3,
	}
	vr := consensus.NewVotingRound(votingCfg, reg)
	eng.SetConsensus(vr)

	// Track finalization handler calls.
	var handlerCalled atomic.Int64
	var handledTarget event.EventID
	var handledVerdict bool
	eng.SetFinalizationHandler(func(targetID event.EventID, verdict bool, verifiedValue uint64, finalOrder uint64) {
		handlerCalled.Add(1)
		handledTarget = targetID
		handledVerdict = verdict
	})

	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer eng.Stop()

	// Create and submit a transfer event.
	payload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 1000, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nil, payload, "alice", nil, 1000)
	if err := eng.Submit(ev); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Vote from 3 voters — should trigger finalization.
	for _, voter := range []crypto.AgentID{"v1", "v2", "v3"} {
		_ = eng.ProcessVote(VerificationResult{
			EventID:    ev.ID,
			VerifierID: voter,
			Verdict:    true,
			Timestamp:  time.Now(),
		})
	}

	if handlerCalled.Load() == 0 {
		t.Fatal("finalization handler was NOT called after supermajority")
	}
	if handledTarget != ev.ID {
		t.Fatalf("handler target: got %s, want %s", handledTarget, ev.ID)
	}
	if !handledVerdict {
		t.Fatal("handler verdict should be true (all yes votes)")
	}
}

func TestFinalizationHandler_NotCalledBeforeQuorum(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	registerVoter(t, reg, "v1", 5000, 2000)
	registerVoter(t, reg, "v2", 5000, 2000)
	registerVoter(t, reg, "v3", 5000, 2000)
	_ = tl.FundAgent("alice", 100_000)

	eng := NewEngine(DefaultConfig(), tl, gl, reg)
	votingCfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        3,
	}
	vr := consensus.NewVotingRound(votingCfg, reg)
	eng.SetConsensus(vr)

	var handlerCalled atomic.Int64
	eng.SetFinalizationHandler(func(_ event.EventID, _ bool, _ uint64, _ uint64) {
		handlerCalled.Add(1)
	})

	_ = eng.Start()
	defer eng.Stop()

	payload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 500, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nil, payload, "alice", nil, 1000)
	_ = eng.Submit(ev)

	// Only 2 of 3 votes — should NOT trigger finalization.
	_ = eng.ProcessVote(VerificationResult{EventID: ev.ID, VerifierID: "v1", Verdict: true, Timestamp: time.Now()})
	_ = eng.ProcessVote(VerificationResult{EventID: ev.ID, VerifierID: "v2", Verdict: true, Timestamp: time.Now()})

	if handlerCalled.Load() != 0 {
		t.Fatal("finalization handler should NOT be called before quorum")
	}
}

func TestFinalizationHandler_CalledOnceNotTwice(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	registerVoter(t, reg, "v1", 5000, 2000)
	registerVoter(t, reg, "v2", 5000, 2000)
	registerVoter(t, reg, "v3", 5000, 2000)
	_ = tl.FundAgent("alice", 100_000)

	eng := NewEngine(DefaultConfig(), tl, gl, reg)
	votingCfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        3,
	}
	vr := consensus.NewVotingRound(votingCfg, reg)
	eng.SetConsensus(vr)

	var handlerCalls atomic.Int64
	eng.SetFinalizationHandler(func(_ event.EventID, _ bool, _ uint64, _ uint64) {
		handlerCalls.Add(1)
	})

	_ = eng.Start()
	defer eng.Stop()

	payload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 300, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nil, payload, "alice", nil, 1000)
	_ = eng.Submit(ev)

	// 3 votes trigger finalization.
	_ = eng.ProcessVote(VerificationResult{EventID: ev.ID, VerifierID: "v1", Verdict: true, Timestamp: time.Now()})
	_ = eng.ProcessVote(VerificationResult{EventID: ev.ID, VerifierID: "v2", Verdict: true, Timestamp: time.Now()})
	_ = eng.ProcessVote(VerificationResult{EventID: ev.ID, VerifierID: "v3", Verdict: true, Timestamp: time.Now()})

	// The 3rd vote triggers finalization. The handler should fire exactly once.
	// ProcessResult marks the event as processed (idempotent), so subsequent
	// votes for the same event are silently skipped by processVoteInternal.
	if handlerCalls.Load() != 1 {
		t.Fatalf("finalization handler should fire exactly once, got %d", handlerCalls.Load())
	}

	// Try to vote again (AcceptPeerVote path) — should not trigger handler.
	_ = eng.AcceptPeerVote(ev.ID, "v1", true)
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler should still be 1 after duplicate peer vote, got %d", handlerCalls.Load())
	}
}

func TestFinalizationHandler_PeerVotePathAlsoTriggers(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	registerVoter(t, reg, "v1", 5000, 2000)
	registerVoter(t, reg, "v2", 5000, 2000)
	registerVoter(t, reg, "v3", 5000, 2000)
	_ = tl.FundAgent("alice", 100_000)

	eng := NewEngine(DefaultConfig(), tl, gl, reg)
	votingCfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        3,
	}
	vr := consensus.NewVotingRound(votingCfg, reg)
	eng.SetConsensus(vr)

	var handlerCalled atomic.Int64
	eng.SetFinalizationHandler(func(_ event.EventID, _ bool, _ uint64, _ uint64) {
		handlerCalled.Add(1)
	})

	_ = eng.Start()
	defer eng.Stop()

	payload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 200, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nil, payload, "alice", nil, 1000)
	_ = eng.Submit(ev)

	// First two votes via local path.
	_ = eng.ProcessVote(VerificationResult{EventID: ev.ID, VerifierID: "v1", Verdict: true, Timestamp: time.Now()})
	_ = eng.ProcessVote(VerificationResult{EventID: ev.ID, VerifierID: "v2", Verdict: true, Timestamp: time.Now()})

	// Third vote via peer path — should trigger finalization.
	_ = eng.AcceptPeerVote(ev.ID, "v3", true)

	if handlerCalled.Load() != 1 {
		t.Fatalf("handler should fire when peer vote triggers finalization, got %d", handlerCalled.Load())
	}
}
