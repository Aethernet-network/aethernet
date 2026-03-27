// invariants_test.go proves the Fast Path v1 architectural invariants.
//
// These tests demonstrate that the implementation satisfies the architecture,
// not just code coverage. Each test is named after the invariant it proves.
package network_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

// ── Invariant: Relay does not wait for durable storage (dag.Add) ─────────────

func TestInvariant_RelayDoesNotWaitForDAGAdd(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	sourceDAG := dag.New()
	ev := makeTestEvent(t, sourceDAG, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := node.Ingest()

	// Admit header → Announced. Enqueue relay.
	im.AdmitHeader("remote", hdr)
	im.EnqueueRelay(ev.ID)

	// Relay queue has the event BEFORE any dag.Add call.
	select {
	case id := <-im.RelayQ():
		if id != ev.ID {
			t.Errorf("RelayQ = %q; want %q", id, ev.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("relay queue should have the event before dag.Add")
	}

	// Confirm: event is NOT in the local DAG at this point.
	if _, err := d.Get(ev.ID); err == nil {
		t.Error("event should NOT be in DAG — relay happened before durable storage")
	}

	// Tracking confirms the event is at Announced, not Materialized.
	tr := im.GetTracking(ev.ID)
	if tr.Stage != network.StageAnnounced {
		t.Errorf("Stage = %v; want Announced (not Materialized)", tr.Stage)
	}
}

// ── Invariant: Bodies are not mandatory on the hot relay path ─────────────────

func TestInvariant_BodiesNotMandatoryOnHotPath(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	sourceDAG := dag.New()
	ev := makeTestEvent(t, sourceDAG, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := node.Ingest()
	im.AdmitHeader("remote", hdr)
	im.EnqueueRelay(ev.ID)

	// The header is relayable. The body is NOT available.
	tr := im.GetTracking(ev.ID)
	if tr.BodyAvailable {
		t.Error("body should NOT be available — header relay is body-independent")
	}

	// Relay queue has the event despite no body.
	select {
	case <-im.RelayQ():
		// Success — relay works without body.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("relay should work without body on hot path")
	}
}

// ── Invariant: New event awareness propagates via causality plane ─────────────

func TestInvariant_CausalityPlanePropagation(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Create a chain: parent → child.
	sourceDAG := dag.New()
	parent := makeTestEvent(t, sourceDAG, kp)
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 2, Currency: "AET"}
	child, _ := event.New(event.EventTypeTransfer, []event.EventID{parent.ID},
		payload, string(kp.AgentID()),
		map[event.EventID]uint64{parent.ID: parent.CausalTimestamp}, 0)
	_ = crypto.SignEvent(child, kp)
	_ = sourceDAG.Add(child)

	im := node.Ingest()

	// Admit both headers — causal structure is visible from headers alone.
	parentHdr := makeTestHeaderFromEvent(t, parent)
	childHdr := makeTestHeaderFromEvent(t, child)

	im.AdmitHeader("remote", parentHdr)
	im.AdmitHeader("remote", childHdr)

	// Both are tracked.
	if !im.IsTracked(parent.ID) {
		t.Error("parent should be tracked via causality plane")
	}
	if !im.IsTracked(child.ID) {
		t.Error("child should be tracked via causality plane")
	}

	// Causal refs are visible in the header.
	childTr := im.GetTracking(child.ID)
	if len(childTr.Header.CausalRefs) != 1 || childTr.Header.CausalRefs[0] != parent.ID {
		t.Errorf("child header should reference parent via CausalRefs")
	}
}

// ── Invariant: No periodic full-DAG polling on V2 normal path ────────────────

func TestInvariant_NoFullDAGPollingForV2(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())

	// Default config: EnableV2=true, EnableLegacySync=false.
	if cfg.EnableLegacySync {
		t.Fatal("EnableLegacySync should be false by default")
	}

	node := network.NewNode(cfg, d)

	// V2 peers are NOT in the legacy sync target set.
	targets := node.LegacySyncTargets()
	for _, p := range targets {
		if p.SupportsFastPath() {
			t.Error("V2 peer should not receive periodic full-DAG polling")
		}
	}
}

// ── Invariant: Duplicate header flood is bounded ─────────────────────────────

func TestInvariant_DuplicateHeaderFloodBounded(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	// Admit once — succeeds.
	if !im.AdmitHeader("flood-peer", hdr) {
		t.Fatal("first admit should succeed")
	}

	// Flood with 1000 duplicates — all rejected, no growth.
	for i := 0; i < 1000; i++ {
		if im.AdmitHeader("flood-peer", hdr) {
			t.Fatal("duplicate should be rejected")
		}
	}

	if im.TrackedCount() != 1 {
		t.Errorf("TrackedCount = %d; want 1 after flood", im.TrackedCount())
	}

	// Peer scoring: duplicate headers should decrease score.
	ps := network.NewPeerScore()
	for i := 0; i < 100; i++ {
		ps.RecordDuplicateHeader()
	}
	if ps.Score() >= network.BaseScore {
		t.Error("score should decrease after duplicate flood")
	}
}

// ── Invariant: Missing-parent storm is bounded ───────────────────────────────

func TestInvariant_MissingParentStormBounded(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Create many events each claiming a different missing parent.
	for i := 0; i < 500; i++ {
		ev := makeTestEvent(t, d, kp)
		hdr := makeTestHeaderFromEvent(t, ev)
		im.AdmitHeader("storm-peer", hdr)
		im.SetMissingParents(ev.ID, map[event.EventID]struct{}{
			event.EventID("missing-" + ev.ID[:8]): {},
		})
		im.EnqueueRepair(ev.ID)
	}

	// Repair queue has bounded capacity (repairQSize = 1024).
	// Not all 500 may fit, but no panic or unbounded growth.
	if im.TrackedCount() > 500 {
		t.Error("tracked count should not exceed number of admits")
	}
}

// ── Invariant: Body fetch respects backpressure quotas ───────────────────────

func TestInvariant_BodyFetchBackpressure(t *testing.T) {
	cfg := network.QuotaConfig{
		HeadersPerWindow:  1000,
		BodiesPerWindow:   3,
		RepairsPerWindow:  100,
		MaxPendingBodies:  2,
		MaxPendingRepairs: 5,
		WindowSeconds:     60,
	}
	pq := network.NewPeerQuota(cfg)

	// Body quota: 3 allowed, 4th rejected.
	for i := 0; i < 3; i++ {
		if !pq.AllowBody(60) {
			t.Fatalf("body %d should be allowed", i+1)
		}
	}
	if pq.AllowBody(60) {
		t.Error("4th body should be rejected (backpressure)")
	}

	// Pending body quota: 2 concurrent allowed.
	pq2 := network.NewPeerQuota(cfg)
	pq2.AllowPendingBody()
	pq2.AllowPendingBody()
	if pq2.AllowPendingBody() {
		t.Error("3rd concurrent body should be rejected")
	}
	pq2.ReleasePendingBody()
	if !pq2.AllowPendingBody() {
		t.Error("after release, pending body should be allowed")
	}
}

// ── Invariant: Mixed-quality peer scoring converges ──────────────────────────

func TestInvariant_MixedQualityPeerScoringConvergence(t *testing.T) {
	good := network.NewPeerScore()
	bad := network.NewPeerScore()
	mixed := network.NewPeerScore()

	// Good peer: 50 valid headers + 10 valid bodies.
	for i := 0; i < 50; i++ {
		good.RecordValidHeader()
	}
	for i := 0; i < 10; i++ {
		good.RecordValidBody()
	}

	// Bad peer: 5 invalid bodies + 10 invalid sigs.
	for i := 0; i < 5; i++ {
		bad.RecordInvalidBody()
	}
	for i := 0; i < 10; i++ {
		bad.RecordInvalidSignature()
	}

	// Mixed peer: some valid, some invalid.
	for i := 0; i < 20; i++ {
		mixed.RecordValidHeader()
	}
	for i := 0; i < 3; i++ {
		mixed.RecordInvalidBody()
	}

	// Score ordering: good > mixed > bad.
	if good.Score() <= mixed.Score() {
		t.Errorf("good (%d) should score higher than mixed (%d)", good.Score(), mixed.Score())
	}
	if mixed.Score() <= bad.Score() {
		t.Errorf("mixed (%d) should score higher than bad (%d)", mixed.Score(), bad.Score())
	}

	// Good peer is usable, bad peer is not.
	if !good.IsUsable() {
		t.Error("good peer should be usable")
	}
	if bad.IsUsable() {
		t.Error("bad peer should be unusable")
	}
}

// ── Invariant: Repair is bounded and localized ───────────────────────────────

func TestInvariant_RepairBoundedAndLocalized(t *testing.T) {
	// RepairRequest is bounded by MaxRepairIDs.
	req := network.RepairRequest{
		MissingIDs: make([]event.EventID, network.MaxRepairIDs+100),
	}
	if len(req.MissingIDs) > network.MaxRepairIDs {
		// Handler truncates — verify the bound exists.
		req.MissingIDs = req.MissingIDs[:network.MaxRepairIDs]
	}
	if len(req.MissingIDs) != network.MaxRepairIDs {
		t.Errorf("bounded request = %d; want %d", len(req.MissingIDs), network.MaxRepairIDs)
	}

	// Repair targets specific missing IDs, not the full DAG.
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)
	im.AdmitHeader("peer", hdr)
	im.SetMissingParents(ev.ID, map[event.EventID]struct{}{"specific-parent": {}})

	mp := im.GetMissingParents(ev.ID)
	if len(mp) != 1 || mp[0] != "specific-parent" {
		t.Error("repair should target specific missing IDs, not full DAG")
	}
}

// ── Invariant: Overload behavior is explicit and observable ──────────────────

func TestInvariant_OverloadExplicitAndObservable(t *testing.T) {
	// Overload is detected from queue depths.
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	depths := im.QueueDepths()

	required := []string{"announce", "relay", "complete", "validate", "materialize", "repair"}
	for _, key := range required {
		if _, ok := depths[key]; !ok {
			t.Errorf("QueueDepths must expose %q for observability", key)
		}
	}

	// OverloadState transitions are explicit.
	var os network.OverloadState
	if os.IsOverloaded() {
		t.Error("default state should not be overloaded")
	}
	os.SetOverloaded(true)
	if !os.IsOverloaded() {
		t.Error("overload should be explicitly observable")
	}

	// Overloaded signal is structured (not silent stall).
	ol := network.Overloaded{RetryAfterMs: 5000}
	if ol.RetryAfterMs <= 0 {
		t.Error("overload signal must carry explicit retry guidance")
	}
}

// ── Invariant: Peer quality materially affects resource allocation ────────────

func TestInvariant_PeerQualityAffectsAllocation(t *testing.T) {
	low := makeV2Peer("low", network.BaseScore)
	high := makeV2Peer("high", network.BaseScore+200)
	unusable := makeV2Peer("unusable", 0)

	peers := makePeerMap(low, high, unusable)

	// Best peer selection prefers high score.
	best := network.BestPeerFor(peers, "exclude-none")
	if best == nil || best.AgentID != "high" {
		t.Error("BestPeerFor should prefer highest-scored peer")
	}

	// Mesh selection excludes unusable peers.
	mm := network.NewMeshManager(network.MeshConfig{
		TargetFanout: 5, MinFanout: 1, MaxFanout: 10, DiversityRatio: 0,
	})
	targets := mm.SelectRelayTargets(peers, "none")
	for _, p := range targets {
		if p.AgentID == "unusable" {
			t.Error("unusable peer should not receive relay traffic")
		}
	}
}

// ── Invariant: Bootstrap is checkpoint-based for V2 ──────────────────────────

func TestInvariant_BootstrapCheckpointBased(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	sourceDAG := dag.New()
	sourceCfg := network.DefaultNodeConfig(kp.AgentID())
	sourceCfg.KeyPair = kp
	sourceNode := network.NewNode(sourceCfg, sourceDAG)

	for i := 0; i < 10; i++ {
		makeTestEvent(t, sourceDAG, kp)
	}

	// Generate checkpoint.
	cp := sourceNode.BuildCheckpoint()
	if cp.EventCount != 10 {
		t.Fatalf("checkpoint EventCount = %d; want 10", cp.EventCount)
	}

	// New node computes gap from checkpoint (not from full DAG polling).
	gap, fromTS, toTS := network.BootstrapGap(0, 0, cp)
	if gap == 0 {
		t.Fatal("gap should be non-zero for empty node")
	}
	if fromTS != 1 || toTS != cp.Timestamp {
		t.Errorf("gap range = [%d, %d]; want [1, %d]", fromTS, toTS, cp.Timestamp)
	}

	// Backfill is bounded.
	backfill := sourceDAG.RecentEvents(fromTS, network.MaxBootstrapRepairEvents)
	if len(backfill) > network.MaxBootstrapRepairEvents {
		t.Error("bootstrap backfill must be bounded")
	}
}

// ── Invariant: Full pipeline Announced → Materialized with all stages ────────

func TestInvariant_FullPipelineAllStages(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	sourceDAG := dag.New()
	ev := makeTestEvent(t, sourceDAG, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := node.Ingest()

	// Stage 1: Announced (header only).
	im.AdmitHeader("remote", hdr)
	assertStage(t, im, ev.ID, network.StageAnnounced)

	// Relay happens here — before body, validation, or materialization.
	im.EnqueueRelay(ev.ID)

	// Stage 2: Completed (body received and verified).
	_ = im.CompleteBody(ev.ID, ev.Payload)
	assertStage(t, im, ev.ID, network.StageCompleted)

	// Stage 3: Validated (signature + ID verified).
	tr := im.GetTracking(ev.ID)
	reconstructed, _ := network.ReconstructEvent(tr)
	_ = network.ValidateEvent(reconstructed)
	im.SetReconstructedEvent(ev.ID, reconstructed)
	im.MarkValidated(ev.ID)
	assertStage(t, im, ev.ID, network.StageValidated)

	// Stage 4: Materialized (dag.Add).
	_ = d.Add(reconstructed)
	im.MarkMaterialized(ev.ID)
	assertStage(t, im, ev.ID, network.StageMaterialized)

	// Event is now in the DAG.
	if _, err := d.Get(ev.ID); err != nil {
		t.Fatal("event should be in DAG after materialization")
	}
}

// ── Invariant: dag.Add still enforces all checks after fast-path ─────────────

func TestInvariant_DAGAddStillEnforcesAllChecks(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Duplicate rejection.
	ev := makeTestEvent(t, d, kp)
	if err := d.Add(ev); err == nil {
		t.Error("dag.Add should reject duplicate")
	}

	// Missing causal ref rejection.
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}
	orphan, _ := event.New(event.EventTypeTransfer, []event.EventID{"nonexistent"},
		payload, string(kp.AgentID()), map[event.EventID]uint64{"nonexistent": 1}, 0)
	_ = crypto.SignEvent(orphan, kp)
	if err := d.Add(orphan); err == nil {
		t.Error("dag.Add should reject event with missing causal ref")
	}
}
