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

	payload := event.TransferPayload{Version: 1, FromAgent: "a", ToAgent: "b", Amount: 2, Currency: "AET"}
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

// ── Trajectory Commit Validation ─────────────────────────────────────────────

func makeTrajectoryEvent(t *testing.T, kp *crypto.KeyPair, d *dag.DAG, taskID string, claimEventID event.EventID, parentCommitID string) *event.Event {
	t.Helper()
	payload := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         taskID,
		ParentCommitID: parentCommitID,
		Outcome:        event.OutcomeExploring,
		CheckpointHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CheckpointSize: 100,
		QualityScoreBP: 7000,
	}
	var refs []event.EventID
	priorTS := make(map[event.EventID]uint64)
	if parentCommitID == "" {
		refs = []event.EventID{claimEventID}
	} else {
		refs = []event.EventID{claimEventID, event.EventID(parentCommitID)}
		if ev, err := d.Get(event.EventID(parentCommitID)); err == nil {
			priorTS[event.EventID(parentCommitID)] = ev.CausalTimestamp
		}
	}
	if ev, err := d.Get(claimEventID); err == nil {
		priorTS[claimEventID] = ev.CausalTimestamp
	}

	ev, err := event.New(event.EventTypeTrajectoryCommit, refs, payload, string(kp.AgentID()), priorTS, 0)
	if err != nil {
		t.Fatalf("makeTrajectoryEvent: %v", err)
	}
	_ = crypto.SignEvent(ev, kp)
	return ev
}

func TestTrajectoryValidation_ValidRootCommit(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	// Create a claim event.
	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: "task-1", ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)

	// Create root trajectory commit.
	ev := makeTrajectoryEvent(t, kp, d, "task-1", claimEv.ID, "")
	_ = d.Add(ev)

	if err := network.ValidateEvent(ev); err != nil {
		t.Fatalf("valid root trajectory commit should pass: %v", err)
	}
}

func TestTrajectoryValidation_ValidChildCommit(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: "task-1", ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)

	root := makeTrajectoryEvent(t, kp, d, "task-1", claimEv.ID, "")
	_ = d.Add(root)

	child := makeTrajectoryEvent(t, kp, d, "task-1", claimEv.ID, string(root.ID))
	_ = d.Add(child)

	if err := network.ValidateEvent(child); err != nil {
		t.Fatalf("valid child trajectory commit should pass: %v", err)
	}
}

func TestTrajectoryValidation_InvalidOutcomeRejected(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: "task-1", ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)

	// Create event with invalid outcome by raw JSON manipulation.
	rawPayload := json.RawMessage(`{"v":1,"task_id":"task-1","outcome":"invalid_outcome","checkpoint_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","checkpoint_size":100,"quality_score_bp":5000}`)
	ev, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{claimEv.ID}, rawPayload, string(kp.AgentID()), map[event.EventID]uint64{claimEv.ID: claimEv.CausalTimestamp}, 0)
	_ = crypto.SignEvent(ev, kp)

	err := network.ValidateEvent(ev)
	if err == nil {
		t.Fatal("invalid outcome should be rejected")
	}
}

func TestTrajectoryValidation_InvalidQualityScoreRejected(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: "task-1", ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)

	rawPayload := json.RawMessage(`{"v":1,"task_id":"task-1","outcome":"exploring","checkpoint_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","checkpoint_size":100,"quality_score_bp":15000}`)
	ev, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{claimEv.ID}, rawPayload, string(kp.AgentID()), map[event.EventID]uint64{claimEv.ID: claimEv.CausalTimestamp}, 0)
	_ = crypto.SignEvent(ev, kp)

	err := network.ValidateEvent(ev)
	if err == nil {
		t.Fatal("quality_score_bp > 10000 should be rejected")
	}
}

func TestTrajectoryValidation_RootCausalRefsShapeEnforced(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: "task-1", ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)

	// Root commit (no parent_commit_id) but with 2 causal refs — invalid.
	dummy, _ := event.New(event.EventTypeTransfer, nil, event.TransferPayload{Version: 1, FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(dummy, kp)
	_ = d.Add(dummy)

	payload := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-1",
		Outcome:        event.OutcomeExploring,
		CheckpointHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CheckpointSize: 100,
		QualityScoreBP: 5000,
	}
	// Root commit should have 1 ref, but we give it 2.
	ev, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{claimEv.ID, dummy.ID}, payload, string(kp.AgentID()), map[event.EventID]uint64{claimEv.ID: 1, dummy.ID: 1}, 0)
	_ = crypto.SignEvent(ev, kp)

	err := network.ValidateEvent(ev)
	if err == nil {
		t.Fatal("root commit with 2 causal refs should be rejected")
	}
}

func TestTrajectoryValidation_ChildCausalRefsShapeEnforced(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: "task-1", ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)

	// Child commit (has parent_commit_id) but with 1 causal ref — invalid.
	payload := event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-1",
		ParentCommitID: "parent-123",
		Outcome:        event.OutcomeExploring,
		CheckpointHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CheckpointSize: 100,
		QualityScoreBP: 5000,
	}
	ev, _ := event.New(event.EventTypeTrajectoryCommit, []event.EventID{claimEv.ID}, payload, string(kp.AgentID()), map[event.EventID]uint64{claimEv.ID: 1}, 0)
	_ = crypto.SignEvent(ev, kp)

	err := network.ValidateEvent(ev)
	if err == nil {
		t.Fatal("child commit with 1 causal ref should be rejected")
	}
}

func TestTrajectoryValidation_BlobAbsenceDoesNotBlockValidation(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()

	claimPayload := event.TaskClaimedPayload{Version: 1, TaskID: "task-1", ClaimerID: string(kp.AgentID())}
	claimEv, _ := event.New(event.EventTypeTaskClaimed, nil, claimPayload, string(kp.AgentID()), nil, 0)
	_ = crypto.SignEvent(claimEv, kp)
	_ = d.Add(claimEv)

	// Create a valid trajectory commit referencing a blob that doesn't exist.
	// Validation should NOT require blob presence.
	ev := makeTrajectoryEvent(t, kp, d, "task-1", claimEv.ID, "")
	_ = d.Add(ev)

	if err := network.ValidateEvent(ev); err != nil {
		t.Fatalf("blob absence should not block validation: %v", err)
	}
}

func TestTrajectoryValidation_NormalEventUnaffected(t *testing.T) {
	// Non-trajectory events should not trigger trajectory-specific validation.
	kp, _ := crypto.GenerateKeyPair()
	d := dag.New()
	parent := makeTestEvent(t, d, kp)

	payload := event.TransferPayload{Version: 1, FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}
	child, _ := event.New(event.EventTypeTransfer, []event.EventID{parent.ID}, payload, string(kp.AgentID()), map[event.EventID]uint64{parent.ID: parent.CausalTimestamp}, 0)
	_ = crypto.SignEvent(child, kp)
	_ = d.Add(child)

	if err := network.ValidateEvent(child); err != nil {
		t.Fatalf("normal event validation should pass: %v", err)
	}
}
