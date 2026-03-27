package network_test

import (
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func makeCompletedTracking(t *testing.T) (*network.IngestManager, event.EventID, *event.Event) {
	t.Helper()
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader(kp.AgentID(), hdr)
	_ = im.CompleteBody(ev.ID, ev.Payload)

	return im, ev.ID, ev
}

func TestReconstructEvent_MatchesOriginal(t *testing.T) {
	im, id, original := makeCompletedTracking(t)

	tr := im.GetTracking(id)
	if tr == nil {
		t.Fatal("tracking should exist")
	}

	reconstructed, err := network.ReconstructEvent(tr)
	if err != nil {
		t.Fatalf("ReconstructEvent: %v", err)
	}

	if reconstructed.ID != original.ID {
		t.Errorf("ID = %q; want %q", reconstructed.ID, original.ID)
	}
	if reconstructed.Type != original.Type {
		t.Errorf("Type = %q; want %q", reconstructed.Type, original.Type)
	}
	if reconstructed.AgentID != original.AgentID {
		t.Errorf("AgentID = %q; want %q", reconstructed.AgentID, original.AgentID)
	}
	if reconstructed.CausalTimestamp != original.CausalTimestamp {
		t.Errorf("CausalTimestamp = %d; want %d", reconstructed.CausalTimestamp, original.CausalTimestamp)
	}
	if string(reconstructed.Payload) != string(original.Payload) {
		t.Errorf("Payload mismatch")
	}

	// Verify the reconstructed ID matches ComputeID.
	computedID, err := event.ComputeID(reconstructed)
	if err != nil {
		t.Fatalf("ComputeID: %v", err)
	}
	if computedID != original.ID {
		t.Errorf("ComputeID = %q; want %q", computedID, original.ID)
	}
}

func TestValidateEvent_ValidSignature(t *testing.T) {
	_, _, original := makeCompletedTracking(t)

	if err := network.ValidateEvent(original); err != nil {
		t.Fatalf("ValidateEvent should pass for correctly signed event: %v", err)
	}
}

func makeNonGenesisCompletedTracking(t *testing.T) (*network.IngestManager, event.EventID, *event.Event) {
	t.Helper()
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Create a genesis event first, then a child that references it.
	parent := makeTestEvent(t, d, kp)

	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 2, Currency: "AET"}
	child, err := event.New(event.EventTypeTransfer, []event.EventID{parent.ID},
		payload, string(kp.AgentID()),
		map[event.EventID]uint64{parent.ID: parent.CausalTimestamp}, 0)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	_ = crypto.SignEvent(child, kp)
	if err := d.Add(child); err != nil {
		t.Fatalf("dag.Add: %v", err)
	}

	hdr := makeTestHeaderFromEvent(t, child)
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader(kp.AgentID(), hdr)
	_ = im.CompleteBody(child.ID, child.Payload)

	return im, child.ID, child
}

func TestValidateEvent_BadSignature_Rejected(t *testing.T) {
	im, id, _ := makeNonGenesisCompletedTracking(t)

	tr := im.GetTracking(id)
	reconstructed, _ := network.ReconstructEvent(tr)

	// Tamper with the signature.
	reconstructed.Signature = []byte("bad-signature-bytes-that-wont-verify")

	err := network.ValidateEvent(reconstructed)
	if err == nil {
		t.Fatal("ValidateEvent should reject event with bad signature")
	}
}

func TestValidateEvent_MissingSignature_Rejected(t *testing.T) {
	im, id, _ := makeNonGenesisCompletedTracking(t)

	tr := im.GetTracking(id)
	reconstructed, _ := network.ReconstructEvent(tr)

	// Remove the signature (non-genesis event must have one).
	reconstructed.Signature = nil

	err := network.ValidateEvent(reconstructed)
	if err == nil {
		t.Fatal("ValidateEvent should reject non-genesis event without signature")
	}
}

func TestReconstructEvent_IDMismatch_Rejected(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	// Tamper with the header's EventID.
	hdr.EventID = "tampered-id-that-wont-match"

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("peer", hdr)
	_ = im.CompleteBody("tampered-id-that-wont-match", ev.Payload)

	tr := im.GetTracking("tampered-id-that-wont-match")
	_, err := network.ReconstructEvent(tr)
	if err == nil {
		t.Fatal("ReconstructEvent should reject event with mismatched ID")
	}
}

func TestReconstructEvent_NoBody_Error(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("peer", hdr)

	// Do not complete body.
	tr := im.GetTracking(ev.ID)
	_, err := network.ReconstructEvent(tr)
	if err == nil {
		t.Fatal("ReconstructEvent should fail when body is not available")
	}
}

func TestValidationSuccess_AdvancesToValidated(t *testing.T) {
	im, id, _ := makeCompletedTracking(t)

	tr := im.GetTracking(id)
	reconstructed, _ := network.ReconstructEvent(tr)
	_ = network.ValidateEvent(reconstructed)

	im.SetReconstructedEvent(id, reconstructed)
	if !im.MarkValidated(id) {
		t.Fatal("MarkValidated should succeed after validation")
	}

	tr = im.GetTracking(id)
	if tr.Stage != network.StageValidated {
		t.Errorf("Stage = %v; want Validated", tr.Stage)
	}
	if tr.Reconstructed == nil {
		t.Error("Reconstructed event should be stored")
	}
}

func TestBodyHeaderMismatch_ReconstructionFails(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("peer", hdr)

	// Try to complete with wrong body — should fail at CompleteBody level.
	wrongBody := json.RawMessage(`{"wrong": true}`)
	err := im.CompleteBody(ev.ID, wrongBody)
	if err == nil {
		t.Fatal("CompleteBody should reject mismatched body")
	}
}
