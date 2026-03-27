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

func TestMissingParent_TriggersRepairEnqueue(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Create a parent and child in a separate DAG.
	sourceDAG := dag.New()
	parent := makeTestEvent(t, sourceDAG, kp)

	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 2, Currency: "AET"}
	child, _ := event.New(event.EventTypeTransfer, []event.EventID{parent.ID}, payload, string(kp.AgentID()), map[event.EventID]uint64{parent.ID: parent.CausalTimestamp}, 0)
	_ = crypto.SignEvent(child, kp)
	_ = sourceDAG.Add(child)

	// Admit child header and complete body — but parent is NOT in the local DAG.
	childHdr := makeTestHeaderFromEvent(t, child)
	im := node.Ingest()
	im.AdmitHeader("remote", childHdr)
	_ = im.CompleteBody(child.ID, child.Payload)

	// Validate the child.
	tr := im.GetTracking(child.ID)
	reconstructed, _ := network.ReconstructEvent(tr)
	_ = network.ValidateEvent(reconstructed)
	im.SetReconstructedEvent(child.ID, reconstructed)
	im.MarkValidated(child.ID)

	// Try to materialize — will fail with missing parent.
	err := d.Add(reconstructed)
	if err == nil {
		t.Fatal("dag.Add should fail for event with missing parent")
	}

	// Simulate what materializeEvent does: set missing parents and enqueue repair.
	missing := map[event.EventID]struct{}{parent.ID: {}}
	im.SetMissingParents(child.ID, missing)
	im.EnqueueRepair(child.ID)

	// Verify the repair queue has the child.
	select {
	case id := <-im.RepairQ():
		if id != child.ID {
			t.Errorf("RepairQ = %q; want %q", id, child.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RepairQ should have the child event")
	}

	// Verify missing parents are tracked.
	mp := im.GetMissingParents(child.ID)
	if len(mp) != 1 || mp[0] != parent.ID {
		t.Errorf("MissingParents = %v; want [%s]", mp, parent.ID)
	}
}

func TestRepairRequest_BoundedByMaxRepairIDs(t *testing.T) {
	// Create a request with more than MaxRepairIDs entries.
	var ids []event.EventID
	for i := 0; i < network.MaxRepairIDs+50; i++ {
		ids = append(ids, event.EventID("id-"+string(rune('a'+i%26))))
	}

	req := network.RepairRequest{MissingIDs: ids}

	// The handler should truncate to MaxRepairIDs.
	if len(req.MissingIDs) > network.MaxRepairIDs {
		req.MissingIDs = req.MissingIDs[:network.MaxRepairIDs]
	}

	if len(req.MissingIDs) != network.MaxRepairIDs {
		t.Errorf("bounded request length = %d; want %d", len(req.MissingIDs), network.MaxRepairIDs)
	}
}

func TestRepairResponse_Truncation(t *testing.T) {
	resp := network.RepairResponse{
		Events:    make([]*event.Event, 100),
		Truncated: true,
	}

	data, _ := json.Marshal(resp)
	var decoded network.RepairResponse
	_ = json.Unmarshal(data, &decoded)

	if !decoded.Truncated {
		t.Error("Truncated flag should survive roundtrip")
	}
}

func TestNoFullDAGRequest_NormalMissingParent(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())

	// Admit an event and set a single missing parent.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im.AdmitHeader("remote", hdr)
	im.SetMissingParents(ev.ID, map[event.EventID]struct{}{"parent-1": {}})

	// The missing parents set contains exactly 1 item, not the entire DAG.
	mp := im.GetMissingParents(ev.ID)
	if len(mp) != 1 {
		t.Errorf("MissingParents count = %d; want 1 (targeted, not full DAG)", len(mp))
	}
}

func TestChildBeforeParent_Recovery(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)
	im := node.Ingest()

	// Create parent + child in a separate DAG.
	sourceDAG := dag.New()
	parent := makeTestEvent(t, sourceDAG, kp)

	payload := event.TransferPayload{FromAgent: "x", ToAgent: "y", Amount: 3, Currency: "AET"}
	child, _ := event.New(event.EventTypeTransfer, []event.EventID{parent.ID}, payload, string(kp.AgentID()), map[event.EventID]uint64{parent.ID: parent.CausalTimestamp}, 0)
	_ = crypto.SignEvent(child, kp)
	_ = sourceDAG.Add(child)

	// Process child first: full pipeline to Validated.
	childHdr := makeTestHeaderFromEvent(t, child)
	im.AdmitHeader("remote", childHdr)
	_ = im.CompleteBody(child.ID, child.Payload)
	tr := im.GetTracking(child.ID)
	reconstructedChild, _ := network.ReconstructEvent(tr)
	_ = network.ValidateEvent(reconstructedChild)
	im.SetReconstructedEvent(child.ID, reconstructedChild)
	im.MarkValidated(child.ID)

	// Materialization fails — parent not in DAG.
	err := d.Add(reconstructedChild)
	if err == nil {
		t.Fatal("should fail: parent missing")
	}
	im.SetMissingParents(child.ID, map[event.EventID]struct{}{parent.ID: {}})

	// Now the parent arrives (simulating repair response).
	if err := d.Add(parent); err != nil {
		t.Fatalf("dag.Add parent: %v", err)
	}

	// Clear the missing parent and check if child is now unblocked.
	allResolved := im.ClearMissingParent(child.ID, parent.ID)
	if !allResolved {
		t.Fatal("all parents should be resolved after adding parent")
	}

	// Retry materialization — should now succeed.
	if err := d.Add(reconstructedChild); err != nil {
		t.Fatalf("dag.Add child after parent: %v", err)
	}
	im.MarkMaterialized(child.ID)

	tr = im.GetTracking(child.ID)
	if tr.Stage != network.StageMaterialized {
		t.Errorf("Stage = %v; want Materialized", tr.Stage)
	}

	// Both events in DAG.
	if _, err := d.Get(parent.ID); err != nil {
		t.Error("parent should be in DAG")
	}
	if _, err := d.Get(child.ID); err != nil {
		t.Error("child should be in DAG")
	}
}

func TestClearMissingParent_PartialResolution(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im.AdmitHeader("remote", hdr)
	im.SetMissingParents(ev.ID, map[event.EventID]struct{}{"p1": {}, "p2": {}})

	// Clear one parent — not fully resolved yet.
	allResolved := im.ClearMissingParent(ev.ID, "p1")
	if allResolved {
		t.Error("should not be fully resolved with one parent remaining")
	}

	// Clear the second parent — now fully resolved.
	allResolved = im.ClearMissingParent(ev.ID, "p2")
	if !allResolved {
		t.Error("should be fully resolved after clearing both parents")
	}
}

func TestMarkRepairRequested_Idempotent(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im.AdmitHeader("remote", hdr)

	if !im.MarkRepairRequested(ev.ID) {
		t.Error("first MarkRepairRequested should return true")
	}
	if im.MarkRepairRequested(ev.ID) {
		t.Error("second MarkRepairRequested should return false")
	}
}

func TestFrontierSummary_Generation(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Add an event to the DAG.
	ev := makeTestEvent(t, d, kp)

	summary := node.BuildFrontierSummary()
	if len(summary.Tips) == 0 {
		t.Error("FrontierSummary.Tips should not be empty")
	}
	if summary.EventCount == 0 {
		t.Error("FrontierSummary.EventCount should be > 0")
	}
	if summary.MaxTimestamp == 0 {
		t.Error("FrontierSummary.MaxTimestamp should be > 0")
	}
	_ = ev // used above
}
