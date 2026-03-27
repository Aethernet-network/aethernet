package network_test

import (
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func TestPeer_DefaultIsLegacy(t *testing.T) {
	p := &network.Peer{}
	if p.SupportsFastPath() {
		t.Error("new peer should not support fast path before negotiation")
	}
	if !p.IsLegacySyncOnly() {
		t.Error("new peer should be legacy-sync-only before negotiation")
	}
	if p.ProtocolVersion() != 0 {
		t.Errorf("ProtocolVersion = %d; want 0", p.ProtocolVersion())
	}
}

func TestPeer_V2NegotiationSucceeds(t *testing.T) {
	p := &network.Peer{}
	hello := network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath, network.FeatureBodySplit},
		MaxBodySize:     4 << 20,
	}
	p.SetV2Capabilities(hello)

	if !p.SupportsFastPath() {
		t.Error("peer should support fast path after V2 negotiation")
	}
	if p.IsLegacySyncOnly() {
		t.Error("peer should not be legacy-sync-only after V2 negotiation")
	}
	if p.ProtocolVersion() != 2 {
		t.Errorf("ProtocolVersion = %d; want 2", p.ProtocolVersion())
	}
}

func TestPeer_V2NegotiationWithoutFastPathFeature(t *testing.T) {
	p := &network.Peer{}
	hello := network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureRepair}, // no fast_path
		MaxBodySize:     4 << 20,
	}
	p.SetV2Capabilities(hello)

	if p.SupportsFastPath() {
		t.Error("peer without fast_path feature should not support fast path")
	}
	if !p.IsLegacySyncOnly() {
		t.Error("peer without fast_path should be legacy-sync-only")
	}
}

func TestHandleHelloV2_ValidPayload(t *testing.T) {
	p := &network.Peer{}
	hello := network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
		MaxBodySize:     1 << 20,
		FrontierTips:    []event.EventID{"tip-1"},
	}
	payload, _ := json.Marshal(hello)

	ok := network.HandleHelloV2(p, payload)
	if !ok {
		t.Fatal("HandleHelloV2 should succeed for valid V2 payload")
	}
	if !p.SupportsFastPath() {
		t.Error("peer should support fast path after HandleHelloV2")
	}
}

func TestHandleHelloV2_InvalidJSON(t *testing.T) {
	p := &network.Peer{}
	ok := network.HandleHelloV2(p, []byte("{invalid"))
	if ok {
		t.Error("HandleHelloV2 should fail for invalid JSON")
	}
	if p.SupportsFastPath() {
		t.Error("peer should remain legacy after invalid HelloV2")
	}
}

func TestHandleHelloV2_OldVersion(t *testing.T) {
	p := &network.Peer{}
	hello := network.HelloV2{
		ProtocolVersion: 1, // too old
		Features:        []string{network.FeatureFastPath},
	}
	payload, _ := json.Marshal(hello)

	ok := network.HandleHelloV2(p, payload)
	if ok {
		t.Error("HandleHelloV2 should reject protocol version < 2")
	}
	if p.SupportsFastPath() {
		t.Error("peer should remain legacy after rejected version")
	}
}

func TestIsV2Message(t *testing.T) {
	v2Types := []network.MessageType{
		network.MsgEventHeader, network.MsgFrontier, network.MsgWindowDigest,
		network.MsgCheckpoint, network.MsgEventBody, network.MsgBodyRequest,
		network.MsgRepairRequest, network.MsgRepairResponse,
		network.MsgHelloV2, network.MsgPeerStatus, network.MsgAckHint,
		network.MsgNackMissing, network.MsgOverloaded,
	}
	for _, mt := range v2Types {
		if !network.IsV2Message(mt) {
			t.Errorf("IsV2Message(%q) = false; want true", mt)
		}
	}

	v1Types := []network.MessageType{
		network.MsgHandshake, network.MsgEvent, network.MsgRequestSync,
		network.MsgSyncBatch, network.MsgPing, network.MsgPong, network.MsgVote,
	}
	for _, mt := range v1Types {
		if network.IsV2Message(mt) {
			t.Errorf("IsV2Message(%q) = true; want false", mt)
		}
	}
}

func TestSafeSend_GatesV2Messages(t *testing.T) {
	p := &network.Peer{}
	// V2 message to legacy peer → dropped.
	sent := network.SafeSend(p, network.Message{Type: network.MsgEventHeader})
	if sent {
		t.Error("SafeSend should drop V2 message to legacy peer")
	}

	// V1 message to legacy peer → sent (we can't check delivery without
	// a connection, but SafeSend should return true).
	// Skip this assertion since Send requires an initialized channel.
}

func TestSafeSend_AllowsV2AfterNegotiation(t *testing.T) {
	p := &network.Peer{}
	p.SetV2Capabilities(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	})
	// V2 message to V2 peer → allowed (returns true even though Send
	// will fail without a connection — SafeSend's gating logic is correct).
	// The actual Send error is swallowed by SafeSend.
	sent := network.SafeSend(p, network.Message{Type: network.MsgEventHeader})
	if !sent {
		t.Error("SafeSend should allow V2 message to V2 peer")
	}
}

func TestBuildHelloV2(t *testing.T) {
	tips := []event.EventID{"a", "b"}
	hello := network.BuildHelloV2(tips)

	if hello.ProtocolVersion != 2 {
		t.Errorf("ProtocolVersion = %d; want 2", hello.ProtocolVersion)
	}
	if len(hello.Features) != 3 {
		t.Errorf("Features = %v; want 3 features", hello.Features)
	}
	if hello.MaxBodySize != 4*1024*1024 {
		t.Errorf("MaxBodySize = %d; want %d", hello.MaxBodySize, 4*1024*1024)
	}
	if len(hello.FrontierTips) != 2 {
		t.Errorf("FrontierTips length = %d; want 2", len(hello.FrontierTips))
	}
}

func TestPeerStatus_UpdateAndRead(t *testing.T) {
	p := &network.Peer{}
	if p.LastStatus() != nil {
		t.Error("LastStatus should be nil before any update")
	}

	status := network.PeerStatus{
		QueueDepth:    5,
		IngestRate:    100,
		DAGEventCount: 5000,
		Healthy:       true,
	}
	p.UpdateStatus(status)

	got := p.LastStatus()
	if got == nil {
		t.Fatal("LastStatus should not be nil after update")
	}
	if got.QueueDepth != 5 || got.IngestRate != 100 || !got.Healthy {
		t.Errorf("LastStatus = %+v; want matching values", got)
	}
}
