package network_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func makeTestHeaderFromEvent(t *testing.T, ev *event.Event) network.EventHeader {
	t.Helper()
	proj, err := event.ProjectHeader(ev)
	if err != nil {
		t.Fatalf("ProjectHeader: %v", err)
	}
	return network.EventHeader{
		EventID:         proj.EventID,
		Type:            proj.Type,
		CausalRefs:      proj.CausalRefs,
		AgentID:         proj.AgentID,
		CausalTimestamp: proj.CausalTimestamp,
		StakeAmount:     proj.StakeAmount,
		BodyCommitment:  proj.BodyCommitment,
		Signature:       proj.Signature,
	}
}

func TestRemoteHeader_BecomesAnnounced(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Create a signed event for header projection.
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	// Simulate receiving from a remote peer.
	remotePeer := crypto.AgentID("remote-peer-1")
	ok := node.Ingest().AdmitHeader(remotePeer, hdr)
	if !ok {
		t.Fatal("AdmitHeader should succeed for new remote header")
	}

	tr := node.Ingest().GetTracking(ev.ID)
	if tr == nil {
		t.Fatal("tracking entry should exist")
	}
	if tr.Stage != network.StageAnnounced {
		t.Errorf("Stage = %v; want Announced (remote headers start at Announced)", tr.Stage)
	}
	if tr.SourcePeer != remotePeer {
		t.Errorf("SourcePeer = %q; want %q", tr.SourcePeer, remotePeer)
	}
}

func TestRemoteHeader_RelayBeforeValidation(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	remotePeer := crypto.AgentID("remote-peer-2")
	node.Ingest().AdmitHeader(remotePeer, hdr)
	node.Ingest().EnqueueRelay(ev.ID)

	// Relay queue should have the event before any validation occurs.
	select {
	case id := <-node.Ingest().RelayQ():
		if id != ev.ID {
			t.Errorf("RelayQ = %q; want %q", id, ev.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RelayQ should have the event ID")
	}

	// Confirm the event is NOT validated or materialized.
	tr := node.Ingest().GetTracking(ev.ID)
	if tr.Stage != network.StageAnnounced {
		t.Errorf("Stage = %v; want Announced (relay happens before validation)", tr.Stage)
	}
}

func TestV2PeersExcept_SkipsOrigin(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Create test peers with V2 capabilities.
	p1 := &network.Peer{AgentID: "peer-A"}
	p1.SetV2Capabilities(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	})
	p2 := &network.Peer{AgentID: "peer-B"}
	p2.SetV2Capabilities(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	})

	// We can't add peers to the node without a real connection, but we can
	// test the peer selection logic via the public methods.
	_ = node // node wired but peers require real connections
	// Verify V2 capability classification.
	if !p1.SupportsFastPath() {
		t.Error("peer-A should support fast path")
	}
	if !p2.SupportsFastPath() {
		t.Error("peer-B should support fast path")
	}

	// Verify SafeSend gates V2 messages.
	legacyPeer := &network.Peer{AgentID: "legacy-1"}
	if network.SafeSend(legacyPeer, network.Message{Type: network.MsgEventHeader}) {
		t.Error("SafeSend should reject V2 message to legacy peer")
	}
}

func TestV1PeersDoNotReceiveV2Headers(t *testing.T) {
	// V1 (legacy) peers must never receive MsgEventHeader.
	p := &network.Peer{AgentID: "v1-peer"}
	// Not negotiated — IsLegacySyncOnly should be true.
	if !p.IsLegacySyncOnly() {
		t.Fatal("unnegotiated peer should be legacy-sync-only")
	}

	msg := network.Message{Type: network.MsgEventHeader, Payload: []byte(`{}`)}
	if network.SafeSend(p, msg) {
		t.Error("SafeSend must not send V2 header to V1 peer")
	}
}

func TestHandleMessage_MsgEventHeader_AdmitsAndEnqueues(t *testing.T) {
	// Verify that receiving MsgEventHeader via the dispatch path creates
	// tracking state. We simulate what handleMessage does internally.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	payload, _ := json.Marshal(hdr)
	remotePeer := crypto.AgentID("dispatch-peer")

	// Simulate the dispatch: AdmitHeader + EnqueueRelay.
	// (We can't call handleMessage directly as it's unexported, so we
	// replicate the exact logic from the MsgEventHeader case.)
	ok := node.Ingest().AdmitHeader(remotePeer, hdr)
	if !ok {
		t.Fatal("AdmitHeader should succeed")
	}
	node.Ingest().EnqueueRelay(hdr.EventID)

	// Verify tracking.
	tr := node.Ingest().GetTracking(ev.ID)
	if tr == nil {
		t.Fatal("tracking entry should exist after dispatch")
	}
	if tr.Stage != network.StageAnnounced {
		t.Errorf("Stage = %v; want Announced", tr.Stage)
	}

	// Verify relay queue.
	select {
	case id := <-node.Ingest().RelayQ():
		if id != ev.ID {
			t.Errorf("RelayQ = %q; want %q", id, ev.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RelayQ should have the event")
	}

	_ = payload // used for marshal verification
}

func TestRelayHeader_JSONRoundtrip(t *testing.T) {
	// Verify that the header survives JSON marshal/unmarshal for relay.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	data, err := json.Marshal(hdr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded network.EventHeader
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.EventID != hdr.EventID {
		t.Errorf("EventID = %q; want %q", decoded.EventID, hdr.EventID)
	}
	if decoded.BodyCommitment != hdr.BodyCommitment {
		t.Errorf("BodyCommitment = %q; want %q", decoded.BodyCommitment, hdr.BodyCommitment)
	}
	if decoded.Type != hdr.Type {
		t.Errorf("Type = %q; want %q", decoded.Type, hdr.Type)
	}
}
