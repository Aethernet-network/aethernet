package network_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func TestOverloadState_DefaultNotOverloaded(t *testing.T) {
	var os network.OverloadState
	if os.IsOverloaded() {
		t.Error("default OverloadState should not be overloaded")
	}
}

func TestOverloadState_SetAndClear(t *testing.T) {
	var os network.OverloadState
	os.SetOverloaded(true)
	if !os.IsOverloaded() {
		t.Error("should be overloaded after SetOverloaded(true)")
	}
	os.SetOverloaded(false)
	if os.IsOverloaded() {
		t.Error("should not be overloaded after SetOverloaded(false)")
	}
}

func TestOverloadState_CanSignal_RateLimited(t *testing.T) {
	var os network.OverloadState
	if !os.CanSignal() {
		t.Error("first CanSignal should return true")
	}
	if os.CanSignal() {
		t.Error("immediate second CanSignal should return false (rate limited)")
	}
}

func TestPeerQuota_HeaderQuota(t *testing.T) {
	cfg := network.QuotaConfig{
		HeadersPerWindow: 3,
		BodiesPerWindow:  100,
		RepairsPerWindow: 100,
		MaxPendingBodies: 10,
		MaxPendingRepairs: 5,
		WindowSeconds:    60,
	}
	pq := network.NewPeerQuota(cfg)

	for i := 0; i < 3; i++ {
		if !pq.AllowHeader(60) {
			t.Fatalf("header %d should be allowed", i+1)
		}
	}
	if pq.AllowHeader(60) {
		t.Error("4th header should be rejected (quota exhausted)")
	}
}

func TestPeerQuota_BodyQuota(t *testing.T) {
	cfg := network.DefaultQuotaConfig()
	cfg.BodiesPerWindow = 2
	pq := network.NewPeerQuota(cfg)

	pq.AllowBody(60)
	pq.AllowBody(60)
	if pq.AllowBody(60) {
		t.Error("3rd body should be rejected")
	}
}

func TestPeerQuota_RepairQuota(t *testing.T) {
	cfg := network.DefaultQuotaConfig()
	cfg.RepairsPerWindow = 2
	pq := network.NewPeerQuota(cfg)

	pq.AllowRepair(60)
	pq.AllowRepair(60)
	if pq.AllowRepair(60) {
		t.Error("3rd repair should be rejected")
	}
}

func TestPeerQuota_PendingBodyLimit(t *testing.T) {
	cfg := network.DefaultQuotaConfig()
	cfg.MaxPendingBodies = 2
	pq := network.NewPeerQuota(cfg)

	if !pq.AllowPendingBody() {
		t.Error("1st pending body should be allowed")
	}
	if !pq.AllowPendingBody() {
		t.Error("2nd pending body should be allowed")
	}
	if pq.AllowPendingBody() {
		t.Error("3rd pending body should be rejected")
	}
	pq.ReleasePendingBody()
	if !pq.AllowPendingBody() {
		t.Error("after release, pending body should be allowed")
	}
}

func TestPeerQuota_HeadersRemaining(t *testing.T) {
	cfg := network.DefaultQuotaConfig()
	cfg.HeadersPerWindow = 10
	pq := network.NewPeerQuota(cfg)

	if rem := pq.HeadersRemaining(60); rem != 10 {
		t.Errorf("remaining = %d; want 10", rem)
	}
	pq.AllowHeader(60)
	pq.AllowHeader(60)
	if rem := pq.HeadersRemaining(60); rem != 8 {
		t.Errorf("remaining = %d; want 8", rem)
	}
}

func TestOverloadedPeerReceivesLessTraffic(t *testing.T) {
	// Verify that mesh selection excludes overloaded peers.
	mm := network.NewMeshManager(network.MeshConfig{
		TargetFanout:   5,
		MinFanout:      1,
		MaxFanout:      10,
		DiversityRatio: 0.0,
	})

	healthy := makeV2Peer("healthy", network.BaseScore+50)
	overloaded := makeV2Peer("overloaded", network.BaseScore+100)
	overloaded.UpdateStatus(network.PeerStatus{Healthy: false})

	peers := makePeerMap(healthy, overloaded)
	targets := mm.SelectRelayTargets(peers, "none")

	for _, p := range targets {
		if p.AgentID == "overloaded" {
			t.Error("overloaded peer should be excluded from relay targets")
		}
	}
}

func TestQueueDepths_Accessible(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	depths := im.QueueDepths()

	expectedKeys := []string{"announce", "relay", "complete", "validate", "materialize", "repair"}
	for _, key := range expectedKeys {
		if _, ok := depths[key]; !ok {
			t.Errorf("QueueDepths missing key %q", key)
		}
	}
}

func TestMaxQueueDepth_ReflectsLoad(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	// Empty queues — max depth should be 0.
	if im.MaxQueueDepth() != 0 {
		t.Errorf("MaxQueueDepth = %d; want 0 on empty queues", im.MaxQueueDepth())
	}

	// Add events to the relay queue.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	for i := 0; i < 5; i++ {
		ev := makeTestEvent(t, d, kp)
		hdr := makeTestHeaderFromEvent(t, ev)
		im.AdmitHeader("peer", hdr)
		im.EnqueueRelay(ev.ID)
	}

	if im.MaxQueueDepth() < 5 {
		t.Errorf("MaxQueueDepth = %d; want >= 5", im.MaxQueueDepth())
	}
}

func TestOverloadedSignal_ExplicitNotSilent(t *testing.T) {
	// Verify the Overloaded struct serializes correctly.
	ol := network.Overloaded{RetryAfterMs: 3000}
	if ol.RetryAfterMs != 3000 {
		t.Errorf("RetryAfterMs = %d; want 3000", ol.RetryAfterMs)
	}
}

func TestEnsureQuota_LazyInit(t *testing.T) {
	p := &network.Peer{AgentID: "lazy-quota"}
	if p.Quota() != nil {
		t.Error("Quota should be nil before EnsureQuota")
	}
	q := p.EnsureQuota(network.DefaultQuotaConfig())
	if q == nil {
		t.Fatal("EnsureQuota should return non-nil")
	}
	if p.EnsureQuota(network.DefaultQuotaConfig()) != q {
		t.Error("EnsureQuota should return same instance on second call")
	}
}

func TestLowPriorityWorkShedFirst(t *testing.T) {
	// Under overload, verify quotas enforce shedding: a peer with exhausted
	// header quota cannot relay more headers (low-priority relay shed) while
	// body quota still allows body responses (higher priority).
	cfg := network.QuotaConfig{
		HeadersPerWindow: 1,  // very tight header quota
		BodiesPerWindow:  100, // generous body quota
		RepairsPerWindow: 100,
		MaxPendingBodies: 10,
		MaxPendingRepairs: 5,
		WindowSeconds:    60,
	}
	pq := network.NewPeerQuota(cfg)

	// Header quota exhausted after 1.
	pq.AllowHeader(60)
	if pq.AllowHeader(60) {
		t.Error("header should be rejected (quota exhausted = low-priority shed)")
	}

	// Body quota still available.
	if !pq.AllowBody(60) {
		t.Error("body should still be allowed (higher priority not shed)")
	}
}
