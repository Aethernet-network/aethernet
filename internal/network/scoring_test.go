package network_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func TestPeerScore_InitialValue(t *testing.T) {
	ps := network.NewPeerScore()
	if ps.Score() != network.BaseScore {
		t.Errorf("initial score = %d; want %d", ps.Score(), network.BaseScore)
	}
	if !ps.IsUsable() {
		t.Error("initial score should be usable")
	}
}

func TestPeerScore_RisesForValidHeaders(t *testing.T) {
	ps := network.NewPeerScore()
	initial := ps.Score()
	ps.RecordValidHeader()
	ps.RecordValidHeader()
	ps.RecordValidHeader()
	if ps.Score() <= initial {
		t.Errorf("score should rise after valid headers: initial=%d now=%d", initial, ps.Score())
	}
}

func TestPeerScore_RisesForValidBody(t *testing.T) {
	ps := network.NewPeerScore()
	initial := ps.Score()
	ps.RecordValidBody()
	if ps.Score() <= initial {
		t.Errorf("score should rise after valid body: initial=%d now=%d", initial, ps.Score())
	}
}

func TestPeerScore_RisesForValidRepair(t *testing.T) {
	ps := network.NewPeerScore()
	initial := ps.Score()
	ps.RecordValidRepair()
	if ps.Score() <= initial {
		t.Errorf("score should rise after valid repair: initial=%d now=%d", initial, ps.Score())
	}
}

func TestPeerScore_DropsForInvalidBody(t *testing.T) {
	ps := network.NewPeerScore()
	initial := ps.Score()
	ps.RecordInvalidBody()
	if ps.Score() >= initial {
		t.Errorf("score should drop after invalid body: initial=%d now=%d", initial, ps.Score())
	}
}

func TestPeerScore_DropsForInvalidSignature(t *testing.T) {
	ps := network.NewPeerScore()
	initial := ps.Score()
	ps.RecordInvalidSignature()
	if ps.Score() >= initial {
		t.Errorf("score should drop after invalid signature: initial=%d now=%d", initial, ps.Score())
	}
}

func TestPeerScore_DropsForDuplicateSpam(t *testing.T) {
	ps := network.NewPeerScore()
	initial := ps.Score()
	for i := 0; i < 10; i++ {
		ps.RecordDuplicateSpam()
	}
	if ps.Score() >= initial {
		t.Errorf("score should drop after duplicate spam: initial=%d now=%d", initial, ps.Score())
	}
}

func TestPeerScore_ClampedAtMax(t *testing.T) {
	ps := network.NewPeerScore()
	for i := 0; i < 1000; i++ {
		ps.RecordValidBody()
	}
	if ps.Score() > network.MaxScore {
		t.Errorf("score = %d; should be capped at %d", ps.Score(), network.MaxScore)
	}
}

func TestPeerScore_ClampedAtZero(t *testing.T) {
	ps := network.NewPeerScore()
	for i := 0; i < 100; i++ {
		ps.RecordInvalidSignature()
	}
	if ps.Score() < 0 {
		t.Errorf("score = %d; should be floored at 0", ps.Score())
	}
}

func TestPeerScore_BecomesUnusable(t *testing.T) {
	ps := network.NewPeerScore()
	for i := 0; i < 10; i++ {
		ps.RecordInvalidSignature()
	}
	if ps.IsUsable() {
		t.Errorf("score = %d; should be unusable (below %d)", ps.Score(), network.MinUsableScore)
	}
}

func TestPeerScore_Metrics(t *testing.T) {
	ps := network.NewPeerScore()
	ps.RecordValidHeader()
	ps.RecordValidBody()
	ps.RecordInvalidBody()

	m := ps.Metrics()
	if m["valid_headers"] != 1 {
		t.Errorf("valid_headers = %d; want 1", m["valid_headers"])
	}
	if m["valid_bodies"] != 1 {
		t.Errorf("valid_bodies = %d; want 1", m["valid_bodies"])
	}
	if m["invalid_bodies"] != 1 {
		t.Errorf("invalid_bodies = %d; want 1", m["invalid_bodies"])
	}
}

func TestPeersByScore_SortsDescending(t *testing.T) {
	p1 := &network.Peer{AgentID: "low"}
	p1.SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})

	p2 := &network.Peer{AgentID: "high"}
	p2.SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})
	// Boost p2's score.
	for i := 0; i < 50; i++ {
		p2.PeerScore().RecordValidBody()
	}

	peers := []*network.Peer{p1, p2}
	sorted := network.PeersByScore(peers)

	if len(sorted) < 2 {
		t.Fatalf("expected 2 usable peers, got %d", len(sorted))
	}
	if sorted[0].AgentID != "high" {
		t.Errorf("first peer = %q; want high (highest score)", sorted[0].AgentID)
	}
}

func TestPeersByScore_ExcludesUnusable(t *testing.T) {
	p1 := &network.Peer{AgentID: "good"}
	p1.SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})

	p2 := &network.Peer{AgentID: "bad"}
	p2.SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})
	// Destroy p2's score.
	for i := 0; i < 10; i++ {
		p2.PeerScore().RecordInvalidSignature()
	}

	sorted := network.PeersByScore([]*network.Peer{p1, p2})
	for _, p := range sorted {
		if p.AgentID == "bad" {
			t.Error("unusable peer should be excluded from PeersByScore")
		}
	}
}

func TestBestPeerFor_PrefersHighScore(t *testing.T) {
	peers := map[crypto.AgentID]*network.Peer{
		"low": {AgentID: "low"},
		"high": {AgentID: "high"},
	}
	peers["low"].SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})
	peers["high"].SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})
	for i := 0; i < 50; i++ {
		peers["high"].PeerScore().RecordValidBody()
	}

	best := network.BestPeerFor(peers, "exclude-none")
	if best == nil || best.AgentID != "high" {
		t.Errorf("BestPeerFor = %v; want high", best)
	}
}

func TestBestPeerFor_ExcludesSpecifiedPeer(t *testing.T) {
	peers := map[crypto.AgentID]*network.Peer{
		"a": {AgentID: "a"},
		"b": {AgentID: "b"},
	}
	peers["a"].SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})
	peers["b"].SetV2Capabilities(network.HelloV2{ProtocolVersion: 2, Features: []string{network.FeatureFastPath}})
	// Make "a" the highest score.
	for i := 0; i < 50; i++ {
		peers["a"].PeerScore().RecordValidBody()
	}

	// Exclude "a" — should return "b".
	best := network.BestPeerFor(peers, "a")
	if best == nil || best.AgentID != "b" {
		t.Errorf("BestPeerFor excluding a = %v; want b", best)
	}
}

func TestEnsureScore_LazyInit(t *testing.T) {
	p := &network.Peer{AgentID: "lazy"}
	if p.PeerScore() != nil {
		t.Error("PeerScore should be nil before EnsureScore")
	}
	ps := p.EnsureScore()
	if ps == nil {
		t.Fatal("EnsureScore should return non-nil")
	}
	if ps.Score() != network.BaseScore {
		t.Errorf("Score = %d; want %d", ps.Score(), network.BaseScore)
	}
	// Second call returns same instance.
	if p.EnsureScore() != ps {
		t.Error("EnsureScore should return same instance on second call")
	}
}
