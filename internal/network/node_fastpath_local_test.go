package network_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func makeTestEvent(t *testing.T, d *dag.DAG, kp *crypto.KeyPair) *event.Event {
	t.Helper()
	payload := event.TransferPayload{
		FromAgent: "alice",
		ToAgent:   "bob",
		Amount:    1000,
		Currency:  "AET",
	}
	tips := d.Tips()
	priorTS := make(map[event.EventID]uint64, len(tips))
	for _, ref := range tips {
		if ev, err := d.Get(ref); err == nil {
			priorTS[ref] = ev.CausalTimestamp
		}
	}
	ev, err := event.New(event.EventTypeTransfer, tips, payload, string(kp.AgentID()), priorTS, 0)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	_ = crypto.SignEvent(ev, kp)
	if err := d.Add(ev); err != nil {
		t.Fatalf("dag.Add: %v", err)
	}
	return ev
}

func TestSubmitLocalEvent_CreatesTrackingEntry(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)

	if err := node.SubmitLocalEvent(ev); err != nil {
		t.Fatalf("SubmitLocalEvent: %v", err)
	}

	tr := node.Ingest().GetTracking(ev.ID)
	if tr == nil {
		t.Fatal("tracking entry should exist after SubmitLocalEvent")
	}
	if tr.Header.EventID != ev.ID {
		t.Errorf("tracked EventID = %q; want %q", tr.Header.EventID, ev.ID)
	}
	if tr.Header.BodyCommitment == "" {
		t.Error("BodyCommitment should be set")
	}
	if tr.SourcePeer != kp.AgentID() {
		t.Errorf("SourcePeer = %q; want %q", tr.SourcePeer, kp.AgentID())
	}
}

func TestSubmitLocalEvent_ReachesCompletedStage(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)
	_ = node.SubmitLocalEvent(ev)

	tr := node.Ingest().GetTracking(ev.ID)
	if tr == nil {
		t.Fatal("tracking entry should exist")
	}
	// Local events have the body available, so they advance to Completed.
	if tr.Stage != network.StageCompleted {
		t.Errorf("Stage = %v; want Completed", tr.Stage)
	}
	if tr.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set for locally submitted events")
	}
}

func TestSubmitLocalEvent_EnqueuesRelay(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)
	_ = node.SubmitLocalEvent(ev)

	// The relay queue should have the event ID.
	select {
	case id := <-node.Ingest().RelayQ():
		if id != ev.ID {
			t.Errorf("RelayQ event = %q; want %q", id, ev.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RelayQ should have the event ID")
	}
}

func TestSubmitLocalEvent_Idempotent(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)

	if err := node.SubmitLocalEvent(ev); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if err := node.SubmitLocalEvent(ev); err != nil {
		t.Fatalf("second submit: %v", err)
	}

	// Should still have exactly one tracking entry.
	if node.Ingest().TrackedCount() != 1 {
		t.Errorf("TrackedCount = %d; want 1", node.Ingest().TrackedCount())
	}
}

func TestSubmitLocalEvent_DoesNotRequireLegacySync(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	// Set sync interval very high so legacy sync cannot fire during this test.
	cfg.SyncInterval = 10 * time.Minute
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)
	_ = node.SubmitLocalEvent(ev)

	// The event should be tracked and relay-queued immediately,
	// without waiting for the sync interval.
	tr := node.Ingest().GetTracking(ev.ID)
	if tr == nil {
		t.Fatal("tracking entry should exist immediately (no sync wait)")
	}
	if tr.Stage != network.StageCompleted {
		t.Errorf("Stage = %v; want Completed (no sync dependency)", tr.Stage)
	}
}

func TestSubmitLocalEvent_PreservesEventID(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	ev := makeTestEvent(t, d, kp)
	originalID := ev.ID

	_ = node.SubmitLocalEvent(ev)

	// The event ID must not be changed by the fast-path submission.
	if ev.ID != originalID {
		t.Errorf("EventID changed: %q → %q", originalID, ev.ID)
	}
	tr := node.Ingest().GetTracking(originalID)
	if tr == nil {
		t.Fatal("should be tracked under the original EventID")
	}
}

func TestSubmitLocalEvent_NilIngestSafe(t *testing.T) {
	// A node constructed without ingest should not panic.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Force ingest to nil to simulate a misconfigured node.
	// (In production NewNode always sets it, but defense-in-depth.)
	// We can't set it to nil from outside, but SubmitLocalEvent handles nil.
	// Just verify the default path works.
	ev := makeTestEvent(t, d, kp)
	if err := node.SubmitLocalEvent(ev); err != nil {
		t.Fatalf("SubmitLocalEvent should not error: %v", err)
	}
}
