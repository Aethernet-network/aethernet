package network_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

func makeRemoteAnnounced(t *testing.T) (*network.IngestManager, event.EventID, json.RawMessage) {
	t.Helper()
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("remote-peer", hdr)

	return im, ev.ID, ev.Payload
}

func TestNeedBody_TrueForAnnouncedEvent(t *testing.T) {
	im, id, _ := makeRemoteAnnounced(t)

	if !im.NeedBody(id) {
		t.Error("NeedBody should return true for Announced event without body")
	}
}

func TestNeedBody_FalseAfterBodyAvailable(t *testing.T) {
	im, id, payload := makeRemoteAnnounced(t)
	_ = im.CompleteBody(id, payload)

	if im.NeedBody(id) {
		t.Error("NeedBody should return false after body is available")
	}
}

func TestNeedBody_FalseAfterRequested(t *testing.T) {
	im, id, _ := makeRemoteAnnounced(t)
	im.MarkBodyRequested(id)

	if im.NeedBody(id) {
		t.Error("NeedBody should return false after body is requested")
	}
}

func TestNeedBody_FalseForUnknownEvent(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	if im.NeedBody("nonexistent") {
		t.Error("NeedBody should return false for unknown event")
	}
}

func TestMarkBodyRequested_Idempotent(t *testing.T) {
	im, id, _ := makeRemoteAnnounced(t)

	if !im.MarkBodyRequested(id) {
		t.Error("first MarkBodyRequested should return true")
	}
	if im.MarkBodyRequested(id) {
		t.Error("second MarkBodyRequested should return false (already requested)")
	}
}

func TestCompleteBody_ValidBody_AdvancesToCompleted(t *testing.T) {
	im, id, payload := makeRemoteAnnounced(t)

	if err := im.CompleteBody(id, payload); err != nil {
		t.Fatalf("CompleteBody: %v", err)
	}

	tr := im.GetTracking(id)
	if tr == nil {
		t.Fatal("tracking entry should exist")
	}
	if tr.Stage != network.StageCompleted {
		t.Errorf("Stage = %v; want Completed", tr.Stage)
	}
	if !tr.BodyAvailable {
		t.Error("BodyAvailable should be true")
	}
	if tr.Body == nil {
		t.Error("Body should be non-nil")
	}
	if tr.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set")
	}
}

func TestCompleteBody_CommitmentMismatch_Rejected(t *testing.T) {
	im, id, _ := makeRemoteAnnounced(t)

	wrongPayload := json.RawMessage(`{"tampered": true}`)
	err := im.CompleteBody(id, wrongPayload)
	if err == nil {
		t.Fatal("CompleteBody should reject body with wrong commitment")
	}
	if err != network.ErrBodyCommitmentMismatch {
		t.Errorf("error = %v; want ErrBodyCommitmentMismatch", err)
	}

	// Event should still be at Announced.
	tr := im.GetTracking(id)
	if tr.Stage != network.StageAnnounced {
		t.Errorf("Stage = %v; want Announced (rejected body should not advance)", tr.Stage)
	}
	if tr.BodyAvailable {
		t.Error("BodyAvailable should be false after rejection")
	}
}

func TestCompleteBody_AlreadyAvailable_NoDuplicateAdvance(t *testing.T) {
	im, id, payload := makeRemoteAnnounced(t)

	_ = im.CompleteBody(id, payload)
	err := im.CompleteBody(id, payload)
	if err != network.ErrBodyAlreadyAvailable {
		t.Errorf("error = %v; want ErrBodyAlreadyAvailable", err)
	}
}

func TestCompleteBody_UnknownEvent_Error(t *testing.T) {
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	err := im.CompleteBody("unknown", json.RawMessage(`{}`))
	if err != network.ErrEventNotTracked {
		t.Errorf("error = %v; want ErrEventNotTracked", err)
	}
}

func TestSetBodyAvailable_LocalEvent(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader(kp.AgentID(), hdr)
	im.SetBodyAvailable(ev.ID, ev.Payload)

	tr := im.GetTracking(ev.ID)
	if !tr.BodyAvailable {
		t.Error("BodyAvailable should be true after SetBodyAvailable")
	}
	if tr.Body == nil {
		t.Error("Body should be non-nil")
	}
}

func TestBodyCommitmentVerification_Correctness(t *testing.T) {
	// Verify the commitment computation matches what ProjectHeader produces.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	ev := makeTestEvent(t, d, kp)
	hdr := makeTestHeaderFromEvent(t, ev)

	// Manually compute commitment from the payload.
	data := ev.Payload
	if data == nil {
		data = json.RawMessage("null")
	}
	sum := sha256.Sum256(data)
	expectedCommitment := hex.EncodeToString(sum[:])

	if hdr.BodyCommitment != expectedCommitment {
		t.Errorf("header commitment = %q; manual = %q", hdr.BodyCommitment, expectedCommitment)
	}

	// CompleteBody should accept the original payload.
	im := network.NewIngestManager(network.DefaultFastPathConfig())
	im.AdmitHeader("peer", hdr)
	if err := im.CompleteBody(ev.ID, ev.Payload); err != nil {
		t.Errorf("CompleteBody should accept matching payload: %v", err)
	}
}

func TestCompletionEnqueuesComplete(t *testing.T) {
	im, id, payload := makeRemoteAnnounced(t)
	_ = im.CompleteBody(id, payload)

	// completeQ should have the event.
	select {
	case got := <-im.CompleteQ():
		if got != id {
			t.Errorf("CompleteQ = %q; want %q", got, id)
		}
	default:
		t.Error("CompleteQ should have the event after body completion")
	}
}
