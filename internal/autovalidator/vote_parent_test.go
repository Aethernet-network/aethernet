package autovalidator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
)

// TestVoteEmission_ReferencesOnlyTargetEvent verifies that emitted vote events
// reference only the target event as their causal parent, not the voter's full
// DAG tip set. This ensures votes materialize on remote nodes as soon as the
// target event is available, without waiting for the voter's other recent events.
func TestVoteEmission_ReferencesOnlyTargetEvent(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	d := dag.New()

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	kp, _ := crypto.GenerateKeyPair()
	av := autovalidator.NewAutoValidator(eng, kp.AgentID(), 50*time.Millisecond)
	av.SetDAG(d)
	av.SetKeyPair(kp)

	// Fund alice so the transfer can pass balance check.
	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("fund: %v", err)
	}

	// Create a transfer event (the target to be voted on).
	payload := event.TransferPayload{
		Version: 1, FromAgent: "alice", ToAgent: "bob",
		Amount: 5_000, Currency: "AET",
	}
	transferEv, err := event.New(event.EventTypeTransfer, nil, payload, string(kp.AgentID()), nil, 1000)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	_ = crypto.SignEvent(transferEv, kp)
	if err := d.Add(transferEv); err != nil {
		t.Fatalf("dag.Add transfer: %v", err)
	}

	// Add several other events to the DAG to create multiple tips.
	// These simulate other validators' events that would appear as DAG tips.
	for i := 0; i < 5; i++ {
		otherKP, _ := crypto.GenerateKeyPair()
		otherPayload := event.TransferPayload{
			Version: 1, FromAgent: "alice", ToAgent: "charlie",
			Amount: 1, Currency: "AET",
		}
		otherEv, _ := event.New(event.EventTypeTransfer, nil, otherPayload, string(otherKP.AgentID()), nil, 0)
		_ = crypto.SignEvent(otherEv, otherKP)
		_ = d.Add(otherEv)
	}

	// Verify there are multiple DAG tips.
	tips := d.Tips()
	if len(tips) < 3 {
		t.Fatalf("expected multiple DAG tips; got %d", len(tips))
	}

	// Submit the transfer to OCS and run one tick.
	if err := eng.Submit(transferEv); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	av.Tick()

	// Find the vote event in the DAG.
	all := d.All()
	var voteEv *event.Event
	for _, ev := range all {
		if ev.Type == event.EventTypeVerificationVote {
			voteEv = ev
			break
		}
	}
	if voteEv == nil {
		t.Fatal("expected a vote event in the DAG")
	}

	// The vote should reference ONLY the target event, not all tips.
	if len(voteEv.CausalRefs) != 1 {
		t.Errorf("vote CausalRefs = %d; want 1 (only target event)", len(voteEv.CausalRefs))
	}
	if len(voteEv.CausalRefs) > 0 && voteEv.CausalRefs[0] != transferEv.ID {
		t.Errorf("vote CausalRefs[0] = %q; want target event %q", voteEv.CausalRefs[0], transferEv.ID)
	}
}

// TestVoteEmission_MaterializesOnRemoteWithOnlyTarget verifies that a vote
// event can be added to a remote node's DAG that has the target event but
// NOT the voter's other recent events.
func TestVoteEmission_MaterializesOnRemoteWithOnlyTarget(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	localDAG := dag.New()

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	kp, _ := crypto.GenerateKeyPair()
	av := autovalidator.NewAutoValidator(eng, kp.AgentID(), 50*time.Millisecond)
	av.SetDAG(localDAG)
	av.SetKeyPair(kp)
	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("fund: %v", err)
	}

	// Create target event on local DAG.
	payload := event.TransferPayload{
		Version: 1, FromAgent: "alice", ToAgent: "bob",
		Amount: 5_000, Currency: "AET",
	}
	transferEv, _ := event.New(event.EventTypeTransfer, nil, payload, string(kp.AgentID()), nil, 1000)
	_ = crypto.SignEvent(transferEv, kp)
	_ = localDAG.Add(transferEv)

	// Add other local-only events (not shared with remote).
	for i := 0; i < 3; i++ {
		otherKP, _ := crypto.GenerateKeyPair()
		otherPayload := event.TransferPayload{
			Version: 1, FromAgent: "alice", ToAgent: "dave",
			Amount: 1, Currency: "AET",
		}
		otherEv, _ := event.New(event.EventTypeTransfer, nil, otherPayload, string(otherKP.AgentID()), nil, 0)
		_ = crypto.SignEvent(otherEv, otherKP)
		_ = localDAG.Add(otherEv)
	}

	// Emit vote.
	if err := eng.Submit(transferEv); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	av.Tick()

	// Find the vote.
	var voteEv *event.Event
	for _, ev := range localDAG.All() {
		if ev.Type == event.EventTypeVerificationVote {
			voteEv = ev
			break
		}
	}
	if voteEv == nil {
		t.Fatal("vote event not found")
	}

	// Create a "remote" DAG that has ONLY the target event (not the voter's
	// other events). The vote should materialize because it only references
	// the target event as a parent.
	remoteDAG := dag.New()
	if err := remoteDAG.Add(transferEv); err != nil {
		t.Fatalf("remote dag.Add target: %v", err)
	}

	// Try to add the vote to the remote DAG.
	if err := remoteDAG.Add(voteEv); err != nil {
		t.Fatalf("remote dag.Add vote failed: %v — vote should materialize with only target event present", err)
	}
}
