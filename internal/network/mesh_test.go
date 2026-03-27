package network_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func makeV2Peer(id string, score int64) *network.Peer {
	p := &network.Peer{AgentID: crypto.AgentID(id)}
	p.SetV2Capabilities(network.HelloV2{
		ProtocolVersion: 2,
		Features:        []string{network.FeatureFastPath},
	})
	// Adjust score from base.
	ps := p.PeerScore()
	if score > network.BaseScore {
		for i := int64(0); i < (score-network.BaseScore)/network.ScoreValidBody; i++ {
			ps.RecordValidBody()
		}
	} else if score < network.BaseScore {
		for i := int64(0); i < (network.BaseScore-score)/(-network.ScoreInvalidBody); i++ {
			ps.RecordInvalidBody()
		}
	}
	return p
}

func makePeerMap(peers ...*network.Peer) map[crypto.AgentID]*network.Peer {
	m := make(map[crypto.AgentID]*network.Peer, len(peers))
	for _, p := range peers {
		m[p.AgentID] = p
	}
	return m
}

func TestMesh_BoundedFanout(t *testing.T) {
	cfg := network.MeshConfig{
		TargetFanout:   3,
		MinFanout:      1,
		MaxFanout:      5,
		DiversityRatio: 0.0, // disable randomization for determinism
	}
	mm := network.NewMeshManager(cfg)

	// Create 10 V2 peers.
	peers := make(map[crypto.AgentID]*network.Peer)
	for i := 0; i < 10; i++ {
		p := makeV2Peer("peer-"+string(rune('A'+i)), network.BaseScore+int64(i)*5)
		peers[p.AgentID] = p
	}

	targets := mm.SelectRelayTargets(peers, "exclude-none")
	if len(targets) > cfg.TargetFanout {
		t.Errorf("targets = %d; want <= %d (bounded fanout)", len(targets), cfg.TargetFanout)
	}
	if len(targets) < cfg.MinFanout {
		t.Errorf("targets = %d; want >= %d (min fanout)", len(targets), cfg.MinFanout)
	}
}

func TestMesh_OriginPeerSkipped(t *testing.T) {
	cfg := network.DefaultMeshConfig()
	cfg.DiversityRatio = 0.0
	mm := network.NewMeshManager(cfg)

	origin := makeV2Peer("origin", 500)
	other := makeV2Peer("other", 500)
	peers := makePeerMap(origin, other)

	targets := mm.SelectRelayTargets(peers, "origin")
	for _, p := range targets {
		if p.AgentID == "origin" {
			t.Error("origin peer should be excluded from relay targets")
		}
	}
}

func TestMesh_HigherScorePreferred(t *testing.T) {
	cfg := network.MeshConfig{
		TargetFanout:   2,
		MinFanout:      1,
		MaxFanout:      5,
		DiversityRatio: 0.0, // disable randomization
	}
	mm := network.NewMeshManager(cfg)

	low := makeV2Peer("low", network.BaseScore)
	mid := makeV2Peer("mid", network.BaseScore+50)
	high := makeV2Peer("high", network.BaseScore+100)
	peers := makePeerMap(low, mid, high)

	targets := mm.SelectRelayTargets(peers, "exclude-none")
	if len(targets) != 2 {
		t.Fatalf("targets = %d; want 2", len(targets))
	}

	// First target should be the highest-scored peer.
	if targets[0].AgentID != "high" {
		t.Errorf("first target = %q; want high (highest score)", targets[0].AgentID)
	}
}

func TestMesh_DiversityNotCollapsed(t *testing.T) {
	cfg := network.MeshConfig{
		TargetFanout:   2,
		MinFanout:      1,
		MaxFanout:      5,
		DiversityRatio: 1.0, // always inject diversity
	}
	mm := network.NewMeshManager(cfg)

	// Create 5 peers with distinct scores.
	peers := make(map[crypto.AgentID]*network.Peer)
	for i := 0; i < 5; i++ {
		p := makeV2Peer("peer-"+string(rune('A'+i)), network.BaseScore+int64(i)*20)
		peers[p.AgentID] = p
	}

	// Run selection 20 times and track which peers appear in the last slot.
	lastSlotPeers := make(map[crypto.AgentID]int)
	for i := 0; i < 20; i++ {
		targets := mm.SelectRelayTargets(peers, "exclude-none")
		if len(targets) >= 2 {
			lastSlotPeers[targets[len(targets)-1].AgentID]++
		}
	}

	// With diversity always enabled and 3 unselected peers in the pool,
	// we should see more than 1 distinct peer in the last slot over 20 runs.
	if len(lastSlotPeers) <= 1 {
		t.Errorf("diversity injection should produce multiple distinct peers in last slot; got %d", len(lastSlotPeers))
	}
}

func TestMesh_OverloadedPeerDeprioritized(t *testing.T) {
	cfg := network.MeshConfig{
		TargetFanout:   3,
		MinFanout:      1,
		MaxFanout:      5,
		DiversityRatio: 0.0,
	}
	mm := network.NewMeshManager(cfg)

	healthy := makeV2Peer("healthy", network.BaseScore+50)
	overloaded := makeV2Peer("overloaded", network.BaseScore+100) // higher score but overloaded
	overloaded.UpdateStatus(network.PeerStatus{Healthy: false, QueueDepth: 200})

	peers := makePeerMap(healthy, overloaded)

	targets := mm.SelectRelayTargets(peers, "exclude-none")
	for _, p := range targets {
		if p.AgentID == "overloaded" {
			t.Error("overloaded peer should be excluded from relay targets")
		}
	}
	if len(targets) == 0 {
		t.Error("should select at least the healthy peer")
	}
}

func TestMesh_UnusableScoreExcluded(t *testing.T) {
	cfg := network.DefaultMeshConfig()
	cfg.DiversityRatio = 0.0
	mm := network.NewMeshManager(cfg)

	good := makeV2Peer("good", network.BaseScore)
	bad := makeV2Peer("bad", 0) // score at 0 (below MinUsableScore)

	peers := makePeerMap(good, bad)

	targets := mm.SelectRelayTargets(peers, "exclude-none")
	for _, p := range targets {
		if p.AgentID == "bad" {
			t.Error("unusable-score peer should be excluded")
		}
	}
}

func TestMesh_EmptyPeerSet(t *testing.T) {
	mm := network.NewMeshManager(network.DefaultMeshConfig())
	targets := mm.SelectRelayTargets(make(map[crypto.AgentID]*network.Peer), "none")
	if targets != nil && len(targets) > 0 {
		t.Error("empty peer set should return nil or empty targets")
	}
}

func TestMesh_FewerThanMinFanout(t *testing.T) {
	cfg := network.MeshConfig{
		TargetFanout:   5,
		MinFanout:      3,
		MaxFanout:      10,
		DiversityRatio: 0.0,
	}
	mm := network.NewMeshManager(cfg)

	// Only 2 peers available — less than MinFanout.
	p1 := makeV2Peer("a", network.BaseScore)
	p2 := makeV2Peer("b", network.BaseScore)
	peers := makePeerMap(p1, p2)

	targets := mm.SelectRelayTargets(peers, "exclude-none")
	if len(targets) != 2 {
		t.Errorf("targets = %d; want 2 (all available when < min)", len(targets))
	}
}

func TestMesh_MaxFanoutRespected(t *testing.T) {
	cfg := network.MeshConfig{
		TargetFanout:   20,
		MinFanout:      1,
		MaxFanout:      3,
		DiversityRatio: 0.0,
	}
	mm := network.NewMeshManager(cfg)

	peers := make(map[crypto.AgentID]*network.Peer)
	for i := 0; i < 10; i++ {
		p := makeV2Peer("peer-"+string(rune('A'+i)), network.BaseScore)
		peers[p.AgentID] = p
	}

	targets := mm.SelectRelayTargets(peers, "exclude-none")
	if len(targets) > cfg.MaxFanout {
		t.Errorf("targets = %d; want <= %d (max fanout)", len(targets), cfg.MaxFanout)
	}
}

func TestMesh_V1PeersIgnored(t *testing.T) {
	mm := network.NewMeshManager(network.DefaultMeshConfig())

	v2Peer := makeV2Peer("v2", network.BaseScore)
	v1Peer := &network.Peer{AgentID: "v1"} // no V2 capabilities
	peers := makePeerMap(v2Peer, v1Peer)

	targets := mm.SelectRelayTargets(peers, "exclude-none")
	for _, p := range targets {
		if p.AgentID == "v1" {
			t.Error("V1 peer should not be selected for V2 relay")
		}
	}
}
