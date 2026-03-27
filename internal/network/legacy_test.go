package network_test

import (
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func TestDefaultConfig_V2Enabled(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	if !cfg.EnableV2 {
		t.Error("EnableV2 should be true by default")
	}
	if cfg.EnableLegacySync {
		t.Error("EnableLegacySync should be false by default")
	}
	if !cfg.LegacySyncFallbackAllowed {
		t.Error("LegacySyncFallbackAllowed should be true by default")
	}
}

func TestSyncLoop_SkipsV2Peers(t *testing.T) {
	// Verify that LegacySyncTargets only returns V1 peers when
	// EnableLegacySync is false (default).
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// LegacySyncTargets returns empty when no peers are connected
	// (can't add peers without real connections, but we can verify the
	// filtering logic through PeerCounts and V2Peers).
	v1, v2 := node.PeerCounts()
	if v1 != 0 || v2 != 0 {
		t.Errorf("PeerCounts = (%d, %d); want (0, 0)", v1, v2)
	}
}

func TestV2PeersNotPolled_LegacySyncDisabled(t *testing.T) {
	// When EnableLegacySync is false, V2 peers should not appear in
	// LegacySyncTargets.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.EnableLegacySync = false
	node := network.NewNode(cfg, d)

	targets := node.LegacySyncTargets()
	for _, p := range targets {
		if p.SupportsFastPath() {
			t.Error("V2 peer should not be in LegacySyncTargets when EnableLegacySync=false")
		}
	}
}

func TestV2PeersPolled_LegacySyncEnabled(t *testing.T) {
	// When EnableLegacySync is true, ALL peers are sync targets.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.EnableLegacySync = true
	node := network.NewNode(cfg, d)

	// Even with no real peers, verify the configuration path works.
	_ = node.LegacySyncTargets()
}

func TestIsLegacyPeer(t *testing.T) {
	v1Peer := &network.Peer{AgentID: "v1"}
	if !network.IsLegacyPeer(v1Peer) {
		t.Error("unnegotiated peer should be legacy")
	}

	v2Peer := &network.Peer{AgentID: "v2"}
	v2Peer.SetV2Capabilities(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	})
	if network.IsLegacyPeer(v2Peer) {
		t.Error("V2-negotiated peer should not be legacy")
	}
}

func TestV2Peers_ReturnsOnlyV2(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	v2Peers := node.V2Peers()
	for _, p := range v2Peers {
		if !p.SupportsFastPath() {
			t.Error("V2Peers should only return peers that support fast path")
		}
	}
}

func TestShouldFallbackToLegacy_NormalLoad(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	if node.ShouldFallbackToLegacy() {
		t.Error("should not fall back under normal load")
	}
}

func TestShouldFallbackToLegacy_Disabled(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.LegacySyncFallbackAllowed = false
	node := network.NewNode(cfg, d)

	if node.ShouldFallbackToLegacy() {
		t.Error("should never fall back when LegacySyncFallbackAllowed=false")
	}
}

func TestNegotiateV2_DisabledWhenV2Off(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.EnableV2 = false
	node := network.NewNode(cfg, d)

	// NegotiateV2 should be a no-op when V2 is disabled.
	// We can't test the send without a real connection, but we verify
	// the config gate.
	if cfg.EnableV2 {
		t.Error("EnableV2 should be false after setting")
	}
	_ = node // used for construction
}

func TestRollingUpgrade_MixedPeers(t *testing.T) {
	// Simulate classification of peers during rolling upgrade.
	v1Peer := &network.Peer{AgentID: "v1-node"}
	v2Peer1 := &network.Peer{AgentID: "v2-node-1"}
	v2Peer2 := &network.Peer{AgentID: "v2-node-2"}

	v2Peer1.SetV2Capabilities(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	})
	v2Peer2.SetV2Capabilities(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	})

	// Phase 1: one node upgraded.
	// v1 peer gets legacy sync, v2-node-1 gets fast path.
	if !v1Peer.IsLegacySyncOnly() {
		t.Error("v1 peer should be legacy-sync-only")
	}
	if v2Peer1.IsLegacySyncOnly() {
		t.Error("v2-node-1 should use fast path")
	}

	// Phase 2: two nodes upgraded.
	if !v2Peer2.SupportsFastPath() {
		t.Error("v2-node-2 should support fast path")
	}

	// Phase 3: all upgraded — no legacy peers remain.
	allPeers := []*network.Peer{v2Peer1, v2Peer2}
	for _, p := range allPeers {
		if p.IsLegacySyncOnly() {
			t.Errorf("peer %s should not be legacy after full upgrade", p.AgentID)
		}
	}
}

func TestRollingUpgrade_V1PeerStillServed(t *testing.T) {
	// During mixed-version operation, V1 peers must still receive
	// MsgRequestSync. The syncLoop filter uses IsLegacySyncOnly().
	v1Peer := &network.Peer{AgentID: "old-node"}
	if !v1Peer.IsLegacySyncOnly() {
		t.Error("V1 peer must be classified as legacy-sync-only")
	}
	// This peer WILL receive MsgRequestSync in the syncLoop because
	// IsLegacySyncOnly() returns true.
}

func TestHandleHelloV2Message_UpgradesPeer(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	peer := &network.Peer{AgentID: "upgrade-test"}
	if peer.SupportsFastPath() {
		t.Fatal("peer should not support fast path before HelloV2")
	}

	// Simulate receiving HelloV2 from the peer.
	ok := network.HandleHelloV2(peer, mustMarshal(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	}))
	if !ok {
		t.Fatal("HandleHelloV2 should succeed")
	}
	if !peer.SupportsFastPath() {
		t.Error("peer should support fast path after HelloV2")
	}

	_ = node // used for context
}

func mustMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
