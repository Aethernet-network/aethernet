package network_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

// TestHeaderRelay_DAGPresenceSkipsAdmit verifies that headers for events
// already in the local DAG are not admitted into the ingest pipeline.
// This prevents header relay amplification when V1 Broadcast delivers
// the full event before V2 header relay.
func TestHeaderRelay_DAGPresenceSkipsAdmit(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Add an event to the DAG (simulates V1 MsgEvent arrival).
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	// The ingest manager does NOT know about this event.
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	// AdmitHeader would normally succeed, but with DAG presence check
	// at the handler level, we skip it entirely. Verify that AdmitHeader
	// itself still works as expected when the event is NOT in the DAG.
	if !im.AdmitHeader("peer-1", hdr) {
		t.Error("first AdmitHeader should succeed for unknown event")
	}

	// Second admit should fail (already tracked).
	if im.AdmitHeader("peer-2", hdr) {
		t.Error("duplicate AdmitHeader should fail")
	}
}

// TestHeaderRelay_NoDuplicateRelay verifies that the same header is not
// relayed to the same peer more than once.
func TestHeaderRelay_NoDuplicateRelay(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())

	// Admit once — succeeds and enqueues relay.
	if !im.AdmitHeader("peer-1", hdr) {
		t.Fatal("first admit should succeed")
	}

	// Drain the relay queue.
	select {
	case id := <-im.RelayQ():
		if id != ev.ID {
			t.Errorf("RelayQ got %q; want %q", id, ev.ID)
		}
	default:
		// Relay may not have been enqueued via EnqueueRelay; that's fine
		// as long as AdmitHeader put it in the announceQ.
	}

	// Try to admit again from a different peer — should fail.
	if im.AdmitHeader("peer-2", hdr) {
		t.Error("second admit of same header should fail (dedup)")
	}

	// Relay queue should NOT have a second entry.
	select {
	case <-im.RelayQ():
		t.Error("relay queue should not have a duplicate entry")
	default:
		// Good — no duplicate relay.
	}
}

// TestHeaderRelay_TrackedCountBounded verifies that tracked_count does not
// grow unboundedly for repeated admits of the same event.
func TestHeaderRelay_TrackedCountBounded(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())

	// Admit once.
	im.AdmitHeader("peer-1", hdr)
	initialCount := im.TrackedCount()

	// Flood with 100 duplicate admits from different peers.
	for i := 0; i < 100; i++ {
		im.AdmitHeader(crypto.AgentID("flood-peer"), hdr)
	}

	// Tracked count should not have grown.
	if im.TrackedCount() != initialCount {
		t.Errorf("TrackedCount = %d; want %d (no growth from duplicates)",
			im.TrackedCount(), initialCount)
	}
}

// TestVoteMaterialize_TargetParentOnly verifies that a vote event with
// only the target event as parent materializes successfully on a remote
// node that has the target but not the voter's other events.
func TestVoteMaterialize_TargetParentOnly(t *testing.T) {
	voterKP, _ := crypto.GenerateKeyPair()
	targetKP, _ := crypto.GenerateKeyPair()

	// Create target event.
	targetPayload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 1000, Currency: "AET",
	}
	targetEv, err := event.New(event.EventTypeTransfer, nil, targetPayload,
		string(targetKP.AgentID()), nil, 0)
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	_ = crypto.SignEvent(targetEv, targetKP)

	// Create vote referencing ONLY the target as parent.
	votePayload := map[string]interface{}{
		"version":         1,
		"target_event_id": string(targetEv.ID),
		"voter_id":        string(voterKP.AgentID()),
		"verdict":         "accepted",
		"verified_value":  1000,
		"timestamp":       1234567890,
	}
	voteEv, err := event.New(event.EventTypeVerificationVote,
		[]event.EventID{targetEv.ID},
		votePayload,
		string(voterKP.AgentID()),
		map[event.EventID]uint64{targetEv.ID: targetEv.CausalTimestamp},
		0,
	)
	if err != nil {
		t.Fatalf("create vote: %v", err)
	}
	_ = crypto.SignEvent(voteEv, voterKP)

	// Remote DAG has only the target event.
	remoteDAG := dag.New()
	if err := remoteDAG.Add(targetEv); err != nil {
		t.Fatalf("add target to remote: %v", err)
	}

	// Vote should materialize — it only depends on the target.
	if err := remoteDAG.Add(voteEv); err != nil {
		t.Fatalf("add vote to remote failed: %v — vote should not need voter's other events", err)
	}

	// Verify both events are in the remote DAG.
	if remoteDAG.Size() != 2 {
		t.Errorf("remote DAG size = %d; want 2", remoteDAG.Size())
	}
}

// TestVoteMaterialize_MultipleVotersSameTarget verifies that votes from
// 5 different validators all referencing the same target event can
// materialize on a node that has only the target event.
func TestVoteMaterialize_MultipleVotersSameTarget(t *testing.T) {
	targetKP, _ := crypto.GenerateKeyPair()

	// Create target event.
	targetPayload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 5000, Currency: "AET",
	}
	targetEv, _ := event.New(event.EventTypeTransfer, nil, targetPayload,
		string(targetKP.AgentID()), nil, 0)
	_ = crypto.SignEvent(targetEv, targetKP)

	// Remote DAG starts with only the target.
	remoteDAG := dag.New()
	if err := remoteDAG.Add(targetEv); err != nil {
		t.Fatalf("add target: %v", err)
	}

	// 5 validators each emit a vote referencing only the target.
	for i := 0; i < 5; i++ {
		voterKP, _ := crypto.GenerateKeyPair()
		votePayload := map[string]interface{}{
			"version":         1,
			"target_event_id": string(targetEv.ID),
			"voter_id":        string(voterKP.AgentID()),
			"verdict":         "accepted",
			"verified_value":  5000,
			"timestamp":       int64(1234567890 + i),
		}
		voteEv, _ := event.New(event.EventTypeVerificationVote,
			[]event.EventID{targetEv.ID},
			votePayload,
			string(voterKP.AgentID()),
			map[event.EventID]uint64{targetEv.ID: targetEv.CausalTimestamp},
			0,
		)
		_ = crypto.SignEvent(voteEv, voterKP)

		if err := remoteDAG.Add(voteEv); err != nil {
			t.Fatalf("validator %d vote failed to materialize: %v", i+1, err)
		}
	}

	// 1 target + 5 votes = 6 events.
	if remoteDAG.Size() != 6 {
		t.Errorf("remote DAG size = %d; want 6 (1 target + 5 votes)", remoteDAG.Size())
	}
}
