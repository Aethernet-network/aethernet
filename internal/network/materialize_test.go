package network_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func TestMaterialize_ValidatedEvent_ReachesDAG(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Create a signed event and add it to one DAG, then test that the
	// fast-path pipeline can materialize it into a second DAG.
	sourceDAG := dag.New()
	ev := makeTestEvent(t, sourceDAG, kp)

	// Simulate the full pipeline: admit → complete → validate → materialize.
	hdr := makeTestHeaderFromEvent(t, ev)
	im := node.Ingest()
	im.AdmitHeader("remote", hdr)
	_ = im.CompleteBody(ev.ID, ev.Payload)

	tr := im.GetTracking(ev.ID)
	reconstructed, err := network.ReconstructEvent(tr)
	if err != nil {
		t.Fatalf("ReconstructEvent: %v", err)
	}
	if err := network.ValidateEvent(reconstructed); err != nil {
		t.Fatalf("ValidateEvent: %v", err)
	}
	im.SetReconstructedEvent(ev.ID, reconstructed)
	im.MarkValidated(ev.ID)

	// Now materialize — this calls dag.Add on the node's DAG.
	if err := d.Add(reconstructed); err != nil {
		t.Fatalf("dag.Add: %v", err)
	}
	im.MarkMaterialized(ev.ID)

	// Verify the event is in the DAG.
	got, err := d.Get(ev.ID)
	if err != nil {
		t.Fatalf("dag.Get: %v", err)
	}
	if got.ID != ev.ID {
		t.Errorf("DAG event ID = %q; want %q", got.ID, ev.ID)
	}

	// Verify tracking reached Materialized.
	tr = im.GetTracking(ev.ID)
	if tr.Stage != network.StageMaterialized {
		t.Errorf("Stage = %v; want Materialized", tr.Stage)
	}
}

func TestMaterialize_DAGEnforcesDuplicateRejection(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Add event directly to DAG first.
	ev := makeTestEvent(t, d, kp)

	// Try to add it again (simulating fast-path materialization).
	err := d.Add(ev)
	if err == nil {
		t.Fatal("dag.Add should reject duplicate event")
	}
}

func TestMaterialize_DAGEnforcesCausalRefCheck(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Create an event referencing a parent that doesn't exist in this DAG.
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}
	ev, _ := event.New(event.EventTypeTransfer, []event.EventID{"nonexistent-parent"}, payload, string(kp.AgentID()), map[event.EventID]uint64{"nonexistent-parent": 1}, 0)
	_ = crypto.SignEvent(ev, kp)

	err := d.Add(ev)
	if err == nil {
		t.Fatal("dag.Add should reject event with missing causal ref")
	}
}

func TestRelayBeforeMaterialization_EndToEnd(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	// Create a remote event.
	sourceDAG := dag.New()
	ev := makeTestEvent(t, sourceDAG, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := node.Ingest()

	// Step 1: Admit header → Announced.
	im.AdmitHeader("remote", hdr)
	im.EnqueueRelay(ev.ID)

	// Step 2: Relay queue has the event while stage is Announced.
	select {
	case id := <-im.RelayQ():
		if id != ev.ID {
			t.Errorf("RelayQ = %q; want %q", id, ev.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RelayQ should have the event")
	}

	tr := im.GetTracking(ev.ID)
	if tr.Stage != network.StageAnnounced {
		t.Errorf("Stage at relay time = %v; want Announced", tr.Stage)
	}

	// Step 3: Complete body → Completed.
	_ = im.CompleteBody(ev.ID, ev.Payload)
	tr = im.GetTracking(ev.ID)
	if tr.Stage != network.StageCompleted {
		t.Errorf("Stage after body = %v; want Completed", tr.Stage)
	}

	// Step 4: Validate → Validated.
	reconstructed, _ := network.ReconstructEvent(tr)
	_ = network.ValidateEvent(reconstructed)
	im.SetReconstructedEvent(ev.ID, reconstructed)
	im.MarkValidated(ev.ID)
	tr = im.GetTracking(ev.ID)
	if tr.Stage != network.StageValidated {
		t.Errorf("Stage after validation = %v; want Validated", tr.Stage)
	}

	// Step 5: Materialize → dag.Add.
	if err := d.Add(reconstructed); err != nil {
		t.Fatalf("dag.Add: %v", err)
	}
	im.MarkMaterialized(ev.ID)
	tr = im.GetTracking(ev.ID)
	if tr.Stage != network.StageMaterialized {
		t.Errorf("Stage after materialization = %v; want Materialized", tr.Stage)
	}

	// Confirm: relay happened at Announced (step 2), materialization at step 5.
	// The relay did not wait for validation or materialization.
}

func TestFullPipeline_EndToEnd(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	cfg := network.DefaultNodeConfig(kp.AgentID())
	node := network.NewNode(cfg, d)

	sourceDAG := dag.New()
	ev := makeTestEvent(t, sourceDAG, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := node.Ingest()

	// Announced.
	im.AdmitHeader("remote", hdr)
	assertStage(t, im, ev.ID, network.StageAnnounced)

	// Completed.
	_ = im.CompleteBody(ev.ID, ev.Payload)
	assertStage(t, im, ev.ID, network.StageCompleted)

	// Validated.
	tr := im.GetTracking(ev.ID)
	reconstructed, _ := network.ReconstructEvent(tr)
	_ = network.ValidateEvent(reconstructed)
	im.SetReconstructedEvent(ev.ID, reconstructed)
	im.MarkValidated(ev.ID)
	assertStage(t, im, ev.ID, network.StageValidated)

	// Materialized.
	_ = d.Add(reconstructed)
	im.MarkMaterialized(ev.ID)
	assertStage(t, im, ev.ID, network.StageMaterialized)

	// Verify in DAG.
	if _, err := d.Get(ev.ID); err != nil {
		t.Fatalf("event should be in DAG: %v", err)
	}
}

func assertStage(t *testing.T, im *network.IngestManager, id event.EventID, expected network.EventStage) {
	t.Helper()
	tr := im.GetTracking(id)
	if tr == nil {
		t.Fatalf("event %s not tracked", id)
	}
	if tr.Stage != expected {
		t.Errorf("event %s stage = %v; want %v", id, tr.Stage, expected)
	}
}
