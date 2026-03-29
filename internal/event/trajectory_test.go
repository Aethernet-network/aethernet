package event_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

func validTrajectoryPayload() event.TrajectoryCommitPayload {
	return event.TrajectoryCommitPayload{
		Version:        1,
		TaskID:         "task-123",
		Outcome:        event.OutcomeExploring,
		CheckpointHash: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		CheckpointSize: 1024,
		ComputeCost:    50000,
		QualityScoreBP: 7500,
		CategoryHint:   "research",
		BranchID:       "main",
	}
}

func TestTrajectoryPayload_Validate_Valid(t *testing.T) {
	p := validTrajectoryPayload()
	if err := p.Validate(); err != nil {
		t.Fatalf("valid payload should pass: %v", err)
	}
}

func TestTrajectoryPayload_Validate_MissingTaskID(t *testing.T) {
	p := validTrajectoryPayload()
	p.TaskID = ""
	if err := p.Validate(); err == nil {
		t.Error("should reject missing task_id")
	}
}

func TestTrajectoryPayload_Validate_MissingOutcome(t *testing.T) {
	p := validTrajectoryPayload()
	p.Outcome = ""
	if err := p.Validate(); err == nil {
		t.Error("should reject missing outcome")
	}
}

func TestTrajectoryPayload_Validate_InvalidOutcome(t *testing.T) {
	p := validTrajectoryPayload()
	p.Outcome = "invalid_value"
	if err := p.Validate(); err == nil {
		t.Error("should reject invalid outcome")
	}
}

func TestTrajectoryPayload_Validate_AllOutcomes(t *testing.T) {
	outcomes := []event.TrajectoryOutcome{
		event.OutcomeExploring,
		event.OutcomeDeadEnd,
		event.OutcomePivot,
		event.OutcomeConverged,
	}
	for _, o := range outcomes {
		p := validTrajectoryPayload()
		p.Outcome = o
		if err := p.Validate(); err != nil {
			t.Errorf("outcome %q should be valid: %v", o, err)
		}
	}
}

func TestTrajectoryPayload_Validate_MissingCheckpointHash(t *testing.T) {
	p := validTrajectoryPayload()
	p.CheckpointHash = ""
	if err := p.Validate(); err == nil {
		t.Error("should reject missing checkpoint_hash")
	}
}

func TestTrajectoryPayload_Validate_InvalidCheckpointSize(t *testing.T) {
	p := validTrajectoryPayload()
	p.CheckpointSize = 0
	if err := p.Validate(); err == nil {
		t.Error("should reject zero checkpoint_size")
	}
	p.CheckpointSize = -1
	if err := p.Validate(); err == nil {
		t.Error("should reject negative checkpoint_size")
	}
}

func TestTrajectoryPayload_Validate_QualityScoreRange(t *testing.T) {
	p := validTrajectoryPayload()

	p.QualityScoreBP = 10001
	if err := p.Validate(); err == nil {
		t.Error("should reject quality_score_bp > 10000")
	}

	p.QualityScoreBP = 0
	if err := p.Validate(); err != nil {
		t.Errorf("quality_score_bp 0 should be valid: %v", err)
	}

	p.QualityScoreBP = 10000
	if err := p.Validate(); err != nil {
		t.Errorf("quality_score_bp 10000 should be valid: %v", err)
	}
}

func TestTrajectoryPayload_IsRootCommit(t *testing.T) {
	p := validTrajectoryPayload()
	if !p.IsRootCommit() {
		t.Error("should be root commit when ParentCommitID is empty")
	}
	p.ParentCommitID = "parent-123"
	if p.IsRootCommit() {
		t.Error("should not be root commit when ParentCommitID is set")
	}
}

func TestTrajectoryCommit_DeterministicEventID(t *testing.T) {
	p := validTrajectoryPayload()
	ev1, err := event.New(event.EventTypeTrajectoryCommit, nil, p, "agent-1", nil, 0)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	ev2, err := event.New(event.EventTypeTrajectoryCommit, nil, p, "agent-1", nil, 0)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	if ev1.ID != ev2.ID {
		t.Error("same payload should produce same EventID")
	}
}

func TestTrajectoryCommit_EventIDChangesWithPayload(t *testing.T) {
	p1 := validTrajectoryPayload()
	p2 := validTrajectoryPayload()
	p2.QualityScoreBP = 5000

	ev1, _ := event.New(event.EventTypeTrajectoryCommit, nil, p1, "agent-1", nil, 0)
	ev2, _ := event.New(event.EventTypeTrajectoryCommit, nil, p2, "agent-1", nil, 0)
	if ev1.ID == ev2.ID {
		t.Error("different payloads should produce different EventIDs")
	}
}

func TestTrajectoryCommit_SignVerify(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := priv.Public().(ed25519.PublicKey)

	kp := &crypto.KeyPair{PrivateKey: priv, PublicKey: pub}
	agentID := kp.AgentID()

	p := validTrajectoryPayload()
	ev, err := event.New(event.EventTypeTrajectoryCommit, nil, p, string(agentID), nil, 0)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	if err := crypto.SignEvent(ev, kp); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}
	if !crypto.VerifyEvent(ev) {
		t.Error("signed trajectory commit should verify")
	}
}

func TestTrajectoryCommit_PayloadRoundtrip(t *testing.T) {
	p := validTrajectoryPayload()
	p.ParentCommitID = "parent-abc"

	ev, _ := event.New(event.EventTypeTrajectoryCommit, nil, p, "agent-1", nil, 0)

	var decoded event.TrajectoryCommitPayload
	if err := json.Unmarshal(ev.Payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.TaskID != p.TaskID {
		t.Errorf("TaskID = %q; want %q", decoded.TaskID, p.TaskID)
	}
	if decoded.Outcome != p.Outcome {
		t.Errorf("Outcome = %q; want %q", decoded.Outcome, p.Outcome)
	}
	if decoded.ParentCommitID != p.ParentCommitID {
		t.Errorf("ParentCommitID = %q; want %q", decoded.ParentCommitID, p.ParentCommitID)
	}
}
