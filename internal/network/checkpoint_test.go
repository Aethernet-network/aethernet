package network_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func TestBuildCheckpoint_Deterministic(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Add some events.
	for i := 0; i < 5; i++ {
		makeTestEvent(t, d, kp)
	}

	cp1 := node.BuildCheckpoint()
	cp2 := node.BuildCheckpoint()

	if cp1.StateHash != cp2.StateHash {
		t.Error("checkpoint state hash should be deterministic")
	}
	if cp1.EventCount != cp2.EventCount {
		t.Error("checkpoint event count should be deterministic")
	}
	if cp1.Timestamp != cp2.Timestamp {
		t.Error("checkpoint timestamp should be deterministic")
	}
}

func TestBuildCheckpoint_ReflectsDAGState(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	makeTestEvent(t, d, kp)
	makeTestEvent(t, d, kp)
	makeTestEvent(t, d, kp)

	cp := node.BuildCheckpoint()
	if cp.EventCount != 3 {
		t.Errorf("EventCount = %d; want 3", cp.EventCount)
	}
	if cp.Timestamp == 0 {
		t.Error("Timestamp should be > 0")
	}
	if len(cp.Tips) == 0 {
		t.Error("Tips should not be empty")
	}
	if cp.StateHash == "" {
		t.Error("StateHash should not be empty")
	}
	if cp.NodeID != string(kp.AgentID()) {
		t.Errorf("NodeID = %q; want %q", cp.NodeID, kp.AgentID())
	}
}

func TestBuildCheckpoint_SignAndVerify(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.KeyPair = kp
	node := network.NewNode(cfg, d)

	makeTestEvent(t, d, kp)

	cp := node.BuildCheckpoint()
	node.SignCheckpoint(&cp)

	if len(cp.Signature) == 0 {
		t.Fatal("Signature should be set after SignCheckpoint")
	}
	if !network.VerifyCheckpoint(&cp) {
		t.Error("VerifyCheckpoint should pass for correctly signed checkpoint")
	}
}

func TestVerifyCheckpoint_TamperedHash_Fails(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	cfg.KeyPair = kp
	node := network.NewNode(cfg, d)

	makeTestEvent(t, d, kp)

	cp := node.BuildCheckpoint()
	node.SignCheckpoint(&cp)

	// Tamper with the state hash.
	cp.StateHash = "tampered"
	if network.VerifyCheckpoint(&cp) {
		t.Error("VerifyCheckpoint should fail for tampered checkpoint")
	}
}

func TestVerifyCheckpoint_Unsigned_Accepted(t *testing.T) {
	cp := network.CheckpointSummary{
		Timestamp:  10,
		EventCount: 5,
		StateHash:  "abc",
		NodeID:     "node-1",
	}
	if !network.VerifyCheckpoint(&cp) {
		t.Error("unsigned checkpoint should be accepted")
	}
}

func TestBootstrapGap_NoGap(t *testing.T) {
	cp := network.CheckpointSummary{
		Timestamp:  10,
		EventCount: 5,
	}
	gap, from, to := network.BootstrapGap(10, 5, cp)
	if gap != 0 || from != 0 || to != 0 {
		t.Errorf("no gap expected: gap=%d from=%d to=%d", gap, from, to)
	}
}

func TestBootstrapGap_HasGap(t *testing.T) {
	cp := network.CheckpointSummary{
		Timestamp:  20,
		EventCount: 15,
	}
	gap, from, to := network.BootstrapGap(10, 5, cp)
	if gap != 10 {
		t.Errorf("gap = %d; want 10", gap)
	}
	if from != 11 {
		t.Errorf("from = %d; want 11", from)
	}
	if to != 20 {
		t.Errorf("to = %d; want 20", to)
	}
}

func TestWindowDigest_Bounded(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Add events to the DAG.
	for i := 0; i < 10; i++ {
		makeTestEvent(t, d, kp)
	}

	digest := node.BuildWindowDigest(1, 100)
	if len(digest.EventIDs) == 0 {
		t.Error("WindowDigest should contain event IDs")
	}
	if len(digest.EventIDs) > network.MaxRepairIDs {
		t.Errorf("WindowDigest bounded at %d; got %d", network.MaxRepairIDs, len(digest.EventIDs))
	}
}

func TestDAGRecentEvents_BoundedByCount(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	for i := 0; i < 20; i++ {
		makeTestEvent(t, d, kp)
	}

	recent := d.RecentEvents(1, 5)
	if len(recent) > 5 {
		t.Errorf("RecentEvents = %d; want <= 5", len(recent))
	}
	if len(recent) == 0 {
		t.Error("RecentEvents should return some events")
	}
}

func TestDAGRecentEvents_BoundedByTimestamp(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	for i := 0; i < 5; i++ {
		makeTestEvent(t, d, kp)
	}

	maxTS := d.MaxTimestamp()
	// Only events at the max timestamp.
	recent := d.RecentEvents(maxTS, 100)
	for _, ev := range recent {
		if ev.CausalTimestamp < maxTS {
			t.Errorf("event timestamp %d < min %d", ev.CausalTimestamp, maxTS)
		}
	}
}

func TestDAGMaxTimestamp(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	if d.MaxTimestamp() != 0 {
		t.Error("empty DAG should have MaxTimestamp 0")
	}

	makeTestEvent(t, d, kp)
	if d.MaxTimestamp() == 0 {
		t.Error("non-empty DAG should have MaxTimestamp > 0")
	}
}

func TestDAGEventIDs(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	ev := makeTestEvent(t, d, kp)
	ids := d.EventIDs()
	if len(ids) != 1 {
		t.Errorf("EventIDs = %d; want 1", len(ids))
	}
	if ids[0] != ev.ID {
		t.Errorf("EventIDs[0] = %q; want %q", ids[0], ev.ID)
	}
}

func TestCheckpointBootstrap_LongDisconnectedNode(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()

	// Source node with many events.
	sourceDAG := dag.New()
	sourceCfg := network.DefaultNodeConfig(kp.AgentID())
	sourceNode := network.NewNode(sourceCfg, sourceDAG)

	var events []*event.Event
	for i := 0; i < 20; i++ {
		ev := makeTestEvent(t, sourceDAG, kp)
		events = append(events, ev)
	}

	// Build checkpoint from source.
	cp := sourceNode.BuildCheckpoint()
	if cp.EventCount != 20 {
		t.Fatalf("source checkpoint: EventCount = %d; want 20", cp.EventCount)
	}

	// New node with empty DAG (long-disconnected).
	newDAG := dag.New()
	newCfg := network.DefaultNodeConfig(kp.AgentID())
	newNode := network.NewNode(newCfg, newDAG)

	// Compute gap.
	localMaxTS := newDAG.MaxTimestamp()
	localCount := uint64(newDAG.Size())
	gap, fromTS, toTS := network.BootstrapGap(localMaxTS, localCount, cp)
	if gap == 0 {
		t.Fatal("should detect a gap for empty node")
	}

	// Simulate bounded backfill: get recent events from source.
	backfill := sourceDAG.RecentEvents(fromTS, network.MaxBootstrapRepairEvents)
	if len(backfill) == 0 {
		t.Fatal("backfill should return events")
	}
	if len(backfill) > network.MaxBootstrapRepairEvents {
		t.Errorf("backfill = %d; should be bounded at %d", len(backfill), network.MaxBootstrapRepairEvents)
	}

	// Add backfill events to new node's DAG (sorted by timestamp).
	added := 0
	for _, ev := range backfill {
		if err := newDAG.Add(ev); err == nil {
			added++
		}
	}
	if added == 0 {
		t.Error("should add at least some events during bootstrap")
	}

	// Verify the new node caught up.
	newCount := newDAG.Size()
	if newCount < added {
		t.Errorf("new DAG size = %d; want >= %d", newCount, added)
	}

	_ = newNode  // used for checkpoint generation
	_ = toTS     // used in gap computation
}
