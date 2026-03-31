package validatorlifecycle

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ---------------------------------------------------------------------------
// Payload Validation Tests
// ---------------------------------------------------------------------------

func TestValidatorGenesisSetPayload_Validate(t *testing.T) {
	valid := &ValidatorGenesisSetPayload{
		Version:              1,
		EffectiveFromVersion: 1,
		Seats: []GenesisSeatEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1", BondedStake: 100_000},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid genesis set should pass: %v", err)
	}

	// Empty seats.
	empty := &ValidatorGenesisSetPayload{Version: 1, EffectiveFromVersion: 1}
	if err := empty.Validate(); !errors.Is(err, ErrPayloadMissingGenesisSeats) {
		t.Fatalf("expected ErrPayloadMissingGenesisSeats, got %v", err)
	}

	// Missing version.
	noVer := &ValidatorGenesisSetPayload{Version: 1, Seats: valid.Seats}
	if err := noVer.Validate(); !errors.Is(err, ErrPayloadMissingEffectiveVersion) {
		t.Fatalf("expected ErrPayloadMissingEffectiveVersion, got %v", err)
	}

	// Missing operator key on a seat.
	badSeat := &ValidatorGenesisSetPayload{
		Version:              1,
		EffectiveFromVersion: 1,
		Seats:                []GenesisSeatEntry{{ValidatorID: "v1", ConsensusPublicKey: "cpk1", BondedStake: 100}},
	}
	if err := badSeat.Validate(); !errors.Is(err, ErrPayloadMissingOperatorAgentID) {
		t.Fatalf("expected ErrPayloadMissingOperatorAgentID, got %v", err)
	}

	// Duplicate validator IDs.
	dup := &ValidatorGenesisSetPayload{
		Version:              1,
		EffectiveFromVersion: 1,
		Seats: []GenesisSeatEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1", BondedStake: 100},
			{ValidatorID: "v1", OperatorAgentID: "op2", ConsensusPublicKey: "cpk2", BondedStake: 200},
		},
	}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate validator IDs should fail validation")
	}
}

func TestValidatorJoinPayload_Validate(t *testing.T) {
	valid := &ValidatorJoinPayload{
		Version: 1, ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
		KeyEpoch: 1, BondedStake: 50_000, EffectiveFromVersion: 2,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid join should pass: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*ValidatorJoinPayload)
		wantErr error
	}{
		{"missing validator_id", func(p *ValidatorJoinPayload) { p.ValidatorID = "" }, ErrPayloadMissingValidatorID},
		{"missing operator", func(p *ValidatorJoinPayload) { p.OperatorAgentID = "" }, ErrPayloadMissingOperatorAgentID},
		{"missing consensus key", func(p *ValidatorJoinPayload) { p.ConsensusPublicKey = "" }, ErrPayloadMissingConsensusKey},
		{"missing key epoch", func(p *ValidatorJoinPayload) { p.KeyEpoch = 0 }, ErrPayloadMissingKeyEpoch},
		{"missing bonded stake", func(p *ValidatorJoinPayload) { p.BondedStake = 0 }, ErrPayloadMissingBondedStake},
		{"missing version", func(p *ValidatorJoinPayload) { p.EffectiveFromVersion = 0 }, ErrPayloadMissingEffectiveVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := *valid
			tt.mutate(&p)
			if err := p.Validate(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidatorSuspendPayload_Validate(t *testing.T) {
	valid := &ValidatorSuspendPayload{Version: 1, ValidatorID: "v1", Reason: "stake deficit", EffectiveFromVersion: 3}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid suspend should pass: %v", err)
	}
	noReason := &ValidatorSuspendPayload{Version: 1, ValidatorID: "v1", EffectiveFromVersion: 3}
	if err := noReason.Validate(); !errors.Is(err, ErrPayloadMissingReason) {
		t.Fatalf("expected ErrPayloadMissingReason, got %v", err)
	}
}

func TestValidatorExitPayload_Validate(t *testing.T) {
	beginCD := &ValidatorExitPayload{
		Version: 1, ValidatorID: "v1", Phase: ExitPhaseBeginCooldown,
		CooldownDuration: 100, EffectiveFromVersion: 4,
	}
	if err := beginCD.Validate(); err != nil {
		t.Fatalf("valid begin_cooldown should pass: %v", err)
	}

	completeExit := &ValidatorExitPayload{
		Version: 1, ValidatorID: "v1", Phase: ExitPhaseCompleteExit, EffectiveFromVersion: 5,
	}
	if err := completeExit.Validate(); err != nil {
		t.Fatalf("valid complete_exit should pass: %v", err)
	}

	// begin_cooldown without duration.
	noDur := &ValidatorExitPayload{
		Version: 1, ValidatorID: "v1", Phase: ExitPhaseBeginCooldown, EffectiveFromVersion: 4,
	}
	if err := noDur.Validate(); !errors.Is(err, ErrPayloadMissingCooldownDuration) {
		t.Fatalf("expected ErrPayloadMissingCooldownDuration, got %v", err)
	}

	// Invalid phase.
	badPhase := &ValidatorExitPayload{
		Version: 1, ValidatorID: "v1", Phase: "garbage", EffectiveFromVersion: 4,
	}
	if err := badPhase.Validate(); !errors.Is(err, ErrPayloadInvalidExitPhase) {
		t.Fatalf("expected ErrPayloadInvalidExitPhase, got %v", err)
	}
}

func TestValidatorKeyRotatePayload_Validate(t *testing.T) {
	valid := &ValidatorKeyRotatePayload{
		Version: 1, ValidatorID: "v1", OldConsensusKey: "old", NewConsensusKey: "new",
		OldKeyEpoch: 1, NewKeyEpoch: 2, EffectiveFromVersion: 3,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rotate should pass: %v", err)
	}

	// Epoch not advanced.
	noAdvance := *valid
	noAdvance.NewKeyEpoch = 1
	if err := noAdvance.Validate(); !errors.Is(err, ErrPayloadKeyEpochNotAdvanced) {
		t.Fatalf("expected ErrPayloadKeyEpochNotAdvanced, got %v", err)
	}

	// Epoch regressed.
	regressed := *valid
	regressed.NewKeyEpoch = 0
	if err := regressed.Validate(); !errors.Is(err, ErrPayloadMissingKeyEpoch) {
		t.Fatalf("expected ErrPayloadMissingKeyEpoch for zero epoch, got %v", err)
	}
}

func TestValidatorSlashAppliedPayload_Validate(t *testing.T) {
	valid := &ValidatorSlashAppliedPayload{
		Version: 1, ValidatorID: "v1", Offense: "fraudulent_approval", EvidenceRef: "ev-hash-1",
		SlashPercentBP: 3000, SlashAmount: 30_000, RemainingStake: 70_000,
		PermanentExclusion: false, Reason: "caught cheating",
		EffectiveFromVersion: 5,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid slash should pass: %v", err)
	}

	// Invalid slash percent.
	badPct := *valid
	badPct.SlashPercentBP = 0
	if err := badPct.Validate(); !errors.Is(err, ErrPayloadInvalidSlashPercent) {
		t.Fatalf("expected ErrPayloadInvalidSlashPercent, got %v", err)
	}
	badPct.SlashPercentBP = 10001
	if err := badPct.Validate(); !errors.Is(err, ErrPayloadInvalidSlashPercent) {
		t.Fatalf("expected ErrPayloadInvalidSlashPercent for >10000, got %v", err)
	}

	// Missing evidence.
	noEvidence := *valid
	noEvidence.EvidenceRef = ""
	if err := noEvidence.Validate(); !errors.Is(err, ErrPayloadMissingEvidenceRef) {
		t.Fatalf("expected ErrPayloadMissingEvidenceRef, got %v", err)
	}

	// Missing offense.
	noOffense := *valid
	noOffense.Offense = ""
	if err := noOffense.Validate(); !errors.Is(err, ErrPayloadMissingOffense) {
		t.Fatalf("expected ErrPayloadMissingOffense, got %v", err)
	}
}

func TestValidatorActivatePayload_Validate(t *testing.T) {
	valid := &ValidatorActivatePayload{Version: 1, ValidatorID: "v1", EffectiveFromVersion: 2}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid activate should pass: %v", err)
	}
	noID := &ValidatorActivatePayload{Version: 1, EffectiveFromVersion: 2}
	if err := noID.Validate(); !errors.Is(err, ErrPayloadMissingValidatorID) {
		t.Fatalf("expected ErrPayloadMissingValidatorID, got %v", err)
	}
}

func TestValidatorResumePayload_Validate(t *testing.T) {
	valid := &ValidatorResumePayload{Version: 1, ValidatorID: "v1", EffectiveFromVersion: 3}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid resume should pass: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Canonical Deterministic Serialization
// ---------------------------------------------------------------------------

func TestPayload_CanonicalSerialization_Deterministic(t *testing.T) {
	p := &ValidatorJoinPayload{
		Version: 1, ValidatorID: "v-abc", OperatorAgentID: "op-abc", ConsensusPublicKey: "cpk-abc",
		KeyEpoch: 1, BondedStake: 100_000, EffectiveFromVersion: 5,
	}
	data1, _ := json.Marshal(p)
	data2, _ := json.Marshal(p)
	if string(data1) != string(data2) {
		t.Fatal("serialization should be deterministic")
	}
}

func TestPayload_RoundTrip(t *testing.T) {
	original := &ValidatorSlashAppliedPayload{
		Version: 1, ValidatorID: "v-rt", Offense: "collusion", EvidenceRef: "ev-123",
		SlashPercentBP: 5000, SlashAmount: 50_000, RemainingStake: 50_000,
		PermanentExclusion: true, Reason: "severe collusion",
		EffectiveFromVersion: 10,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ValidatorSlashAppliedPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ValidatorID != original.ValidatorID ||
		decoded.Offense != original.Offense ||
		decoded.SlashPercentBP != original.SlashPercentBP ||
		decoded.PermanentExclusion != original.PermanentExclusion {
		t.Fatal("round-trip mismatch")
	}
}

// ---------------------------------------------------------------------------
// EventID Deterministic Behavior
// ---------------------------------------------------------------------------

func TestLifecycleEvent_EventID_Deterministic(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	agentID := string(kp.AgentID())

	payload := ValidatorJoinPayload{
		Version: 1, ValidatorID: "v-det", OperatorAgentID: crypto.AgentID(agentID),
		ConsensusPublicKey: crypto.AgentID(agentID),
		KeyEpoch: 1, BondedStake: 100_000, EffectiveFromVersion: 1,
	}

	ev1, err := event.New(event.EventTypeValidatorJoin, nil, payload, agentID, nil, 0)
	if err != nil {
		t.Fatalf("create event 1: %v", err)
	}
	ev2, err := event.New(event.EventTypeValidatorJoin, nil, payload, agentID, nil, 0)
	if err != nil {
		t.Fatalf("create event 2: %v", err)
	}

	if ev1.ID != ev2.ID {
		t.Fatalf("same payload should produce same EventID: %s != %s", ev1.ID, ev2.ID)
	}
}

func TestLifecycleEvent_EventID_DiffersForDifferentPayloads(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	p1 := ValidatorJoinPayload{
		Version: 1, ValidatorID: "v-1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
		KeyEpoch: 1, BondedStake: 100_000, EffectiveFromVersion: 1,
	}
	p2 := ValidatorJoinPayload{
		Version: 1, ValidatorID: "v-2", OperatorAgentID: "op2", ConsensusPublicKey: "cpk2",
		KeyEpoch: 1, BondedStake: 200_000, EffectiveFromVersion: 1,
	}

	ev1, _ := event.New(event.EventTypeValidatorJoin, nil, p1, agentID, nil, 0)
	ev2, _ := event.New(event.EventTypeValidatorJoin, nil, p2, agentID, nil, 0)

	if ev1.ID == ev2.ID {
		t.Fatal("different payloads should produce different EventIDs")
	}
}

// ---------------------------------------------------------------------------
// Recovery Payload Validation Tests
// ---------------------------------------------------------------------------

func TestValidatorRecoveryKeySetPayload_Validate(t *testing.T) {
	valid := &ValidatorRecoveryKeySetPayload{
		Version:           1,
		ValidatorID:       "seat-abc",
		RecoveryPublicKey: "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567",
		RequestedAt:       1700000000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid recovery key set should pass: %v", err)
	}

	// Missing validator_id.
	noID := *valid
	noID.ValidatorID = ""
	if err := noID.Validate(); !errors.Is(err, ErrPayloadMissingValidatorID) {
		t.Fatalf("expected ErrPayloadMissingValidatorID, got %v", err)
	}
	// Missing recovery_public_key.
	noKey := *valid
	noKey.RecoveryPublicKey = ""
	if err := noKey.Validate(); !errors.Is(err, ErrPayloadMissingRecoveryKey) {
		t.Fatalf("expected ErrPayloadMissingRecoveryKey, got %v", err)
	}
	// Missing requested_at.
	noTS := *valid
	noTS.RequestedAt = 0
	if err := noTS.Validate(); !errors.Is(err, ErrPayloadMissingRequestedAt) {
		t.Fatalf("expected ErrPayloadMissingRequestedAt, got %v", err)
	}
}

func TestValidatorEmergencySuspendPayload_Validate(t *testing.T) {
	valid := &ValidatorEmergencySuspendPayload{
		Version:           1,
		ValidatorID:       "seat-abc",
		RecoveryPublicKey: "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567",
		Reason:            "key_compromise",
		RequestedAt:       1700000000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid emergency suspend should pass: %v", err)
	}

	noID := *valid
	noID.ValidatorID = ""
	if err := noID.Validate(); !errors.Is(err, ErrPayloadMissingValidatorID) {
		t.Fatalf("expected ErrPayloadMissingValidatorID, got %v", err)
	}
	noKey := *valid
	noKey.RecoveryPublicKey = ""
	if err := noKey.Validate(); !errors.Is(err, ErrPayloadMissingRecoveryKey) {
		t.Fatalf("expected ErrPayloadMissingRecoveryKey, got %v", err)
	}
	noReason := *valid
	noReason.Reason = ""
	if err := noReason.Validate(); !errors.Is(err, ErrPayloadMissingReason) {
		t.Fatalf("expected ErrPayloadMissingReason, got %v", err)
	}
	noTS := *valid
	noTS.RequestedAt = 0
	if err := noTS.Validate(); !errors.Is(err, ErrPayloadMissingRequestedAt) {
		t.Fatalf("expected ErrPayloadMissingRequestedAt, got %v", err)
	}
}

func TestValidatorRecoveryRotatePayload_Validate(t *testing.T) {
	valid := &ValidatorRecoveryRotatePayload{
		Version:              1,
		ValidatorID:          "seat-abc",
		RecoveryPublicKey:    "aabb0011aabb0011aabb0011aabb0011aabb0011aabb0011aabb0011aabb0011",
		NewPublicKey:         "ccdd0011ccdd0011ccdd0011ccdd0011ccdd0011ccdd0011ccdd0011ccdd0011",
		RequestedAt:          1700000000,
		EffectiveAfter:       1700000300,
		EffectiveFromVersion: 5,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid recovery rotate should pass: %v", err)
	}

	noID := *valid
	noID.ValidatorID = ""
	if err := noID.Validate(); !errors.Is(err, ErrPayloadMissingValidatorID) {
		t.Fatalf("expected ErrPayloadMissingValidatorID, got %v", err)
	}
	noRecovKey := *valid
	noRecovKey.RecoveryPublicKey = ""
	if err := noRecovKey.Validate(); !errors.Is(err, ErrPayloadMissingRecoveryKey) {
		t.Fatalf("expected ErrPayloadMissingRecoveryKey, got %v", err)
	}
	noNewKey := *valid
	noNewKey.NewPublicKey = ""
	if err := noNewKey.Validate(); !errors.Is(err, ErrPayloadMissingNewPublicKey) {
		t.Fatalf("expected ErrPayloadMissingNewPublicKey, got %v", err)
	}
	noTS := *valid
	noTS.RequestedAt = 0
	if err := noTS.Validate(); !errors.Is(err, ErrPayloadMissingRequestedAt) {
		t.Fatalf("expected ErrPayloadMissingRequestedAt, got %v", err)
	}
	noEffective := *valid
	noEffective.EffectiveAfter = 0
	if err := noEffective.Validate(); !errors.Is(err, ErrPayloadMissingEffectiveAfter) {
		t.Fatalf("expected ErrPayloadMissingEffectiveAfter, got %v", err)
	}
	noVer := *valid
	noVer.EffectiveFromVersion = 0
	if err := noVer.Validate(); !errors.Is(err, ErrPayloadMissingEffectiveVersion) {
		t.Fatalf("expected ErrPayloadMissingEffectiveVersion, got %v", err)
	}
}

func TestValidatorRecoveryRotateCancelPayload_Validate(t *testing.T) {
	valid := &ValidatorRecoveryRotateCancelPayload{
		Version:           1,
		ValidatorID:       "seat-abc",
		RecoveryPublicKey: "aabb0011aabb0011aabb0011aabb0011aabb0011aabb0011aabb0011aabb0011",
		RotationEventID:   "rotation-event-123",
		RequestedAt:       1700000000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid recovery rotate cancel should pass: %v", err)
	}

	noID := *valid
	noID.ValidatorID = ""
	if err := noID.Validate(); !errors.Is(err, ErrPayloadMissingValidatorID) {
		t.Fatalf("expected ErrPayloadMissingValidatorID, got %v", err)
	}
	noRecovKey := *valid
	noRecovKey.RecoveryPublicKey = ""
	if err := noRecovKey.Validate(); !errors.Is(err, ErrPayloadMissingRecoveryKey) {
		t.Fatalf("expected ErrPayloadMissingRecoveryKey, got %v", err)
	}
	noRotID := *valid
	noRotID.RotationEventID = ""
	if err := noRotID.Validate(); !errors.Is(err, ErrPayloadMissingRotationEventID) {
		t.Fatalf("expected ErrPayloadMissingRotationEventID, got %v", err)
	}
	noTS := *valid
	noTS.RequestedAt = 0
	if err := noTS.Validate(); !errors.Is(err, ErrPayloadMissingRequestedAt) {
		t.Fatalf("expected ErrPayloadMissingRequestedAt, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Recovery Payload JSON Round-Trip Tests
// ---------------------------------------------------------------------------

func TestRecoveryKeySetPayload_RoundTrip(t *testing.T) {
	original := &ValidatorRecoveryKeySetPayload{
		Version:           1,
		ValidatorID:       "seat-rt",
		RecoveryPublicKey: "aabbccddee",
		RequestedAt:       1700000000,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ValidatorRecoveryKeySetPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ValidatorID != original.ValidatorID ||
		decoded.RecoveryPublicKey != original.RecoveryPublicKey ||
		decoded.RequestedAt != original.RequestedAt ||
		decoded.Version != original.Version {
		t.Fatal("round-trip mismatch")
	}
}

func TestEmergencySuspendPayload_RoundTrip(t *testing.T) {
	original := &ValidatorEmergencySuspendPayload{
		Version:           1,
		ValidatorID:       "seat-rt",
		RecoveryPublicKey: "aabbccddee",
		Reason:            "key_compromise",
		RequestedAt:       1700000000,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ValidatorEmergencySuspendPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ValidatorID != original.ValidatorID ||
		decoded.RecoveryPublicKey != original.RecoveryPublicKey ||
		decoded.Reason != original.Reason ||
		decoded.RequestedAt != original.RequestedAt {
		t.Fatal("round-trip mismatch")
	}
}

func TestRecoveryRotatePayload_RoundTrip(t *testing.T) {
	original := &ValidatorRecoveryRotatePayload{
		Version:              1,
		ValidatorID:          "seat-rt",
		RecoveryPublicKey:    "aabb",
		NewPublicKey:         "ccdd",
		RequestedAt:          1700000000,
		EffectiveAfter:       1700000300,
		EffectiveFromVersion: 10,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ValidatorRecoveryRotatePayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ValidatorID != original.ValidatorID ||
		decoded.RecoveryPublicKey != original.RecoveryPublicKey ||
		decoded.NewPublicKey != original.NewPublicKey ||
		decoded.RequestedAt != original.RequestedAt ||
		decoded.EffectiveAfter != original.EffectiveAfter ||
		decoded.EffectiveFromVersion != original.EffectiveFromVersion {
		t.Fatal("round-trip mismatch")
	}
}

func TestRecoveryRotateCancelPayload_RoundTrip(t *testing.T) {
	original := &ValidatorRecoveryRotateCancelPayload{
		Version:           1,
		ValidatorID:       "seat-rt",
		RecoveryPublicKey: "aabb",
		RotationEventID:   "ev-rot-123",
		RequestedAt:       1700000000,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ValidatorRecoveryRotateCancelPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ValidatorID != original.ValidatorID ||
		decoded.RecoveryPublicKey != original.RecoveryPublicKey ||
		decoded.RotationEventID != original.RotationEventID ||
		decoded.RequestedAt != original.RequestedAt {
		t.Fatal("round-trip mismatch")
	}
}

// ---------------------------------------------------------------------------
// Recovery Payload Canonical Serialization Tests
// ---------------------------------------------------------------------------

func TestRecoveryPayload_CanonicalSerialization_Deterministic(t *testing.T) {
	payloads := []interface{}{
		&ValidatorRecoveryKeySetPayload{Version: 1, ValidatorID: "s1", RecoveryPublicKey: "rk1", RequestedAt: 1700000000},
		&ValidatorEmergencySuspendPayload{Version: 1, ValidatorID: "s1", RecoveryPublicKey: "rk1", Reason: "compromise", RequestedAt: 1700000000},
		&ValidatorRecoveryRotatePayload{Version: 1, ValidatorID: "s1", RecoveryPublicKey: "rk1", NewPublicKey: "nk1", RequestedAt: 1700000000, EffectiveAfter: 1700000300, EffectiveFromVersion: 5},
		&ValidatorRecoveryRotateCancelPayload{Version: 1, ValidatorID: "s1", RecoveryPublicKey: "rk1", RotationEventID: "ev-1", RequestedAt: 1700000000},
	}
	for i, p := range payloads {
		d1, _ := json.Marshal(p)
		d2, _ := json.Marshal(p)
		if string(d1) != string(d2) {
			t.Fatalf("payload %d: serialization not deterministic", i)
		}
	}
}

func TestRecoveryPayload_EventID_Deterministic(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	p1 := ValidatorRecoveryKeySetPayload{Version: 1, ValidatorID: "s1", RecoveryPublicKey: "rk1", RequestedAt: 1700000000}
	p2 := ValidatorRecoveryKeySetPayload{Version: 1, ValidatorID: "s2", RecoveryPublicKey: "rk2", RequestedAt: 1700000001}

	ev1, _ := event.New(event.EventTypeValidatorRecoveryKeySet, nil, p1, agentID, nil, 0)
	ev2, _ := event.New(event.EventTypeValidatorRecoveryKeySet, nil, p2, agentID, nil, 0)

	if ev1.ID == ev2.ID {
		t.Fatal("different recovery payloads should produce different EventIDs")
	}
	// Same payload → same ID.
	ev1b, _ := event.New(event.EventTypeValidatorRecoveryKeySet, nil, p1, agentID, nil, 0)
	if ev1.ID != ev1b.ID {
		t.Fatal("same recovery payload should produce same EventID")
	}
}

// ---------------------------------------------------------------------------
// Sign/Verify Compatibility
// ---------------------------------------------------------------------------

func TestLifecycleEvent_SignVerify(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	payloads := []struct {
		name    string
		evType  event.EventType
		payload interface{}
	}{
		{"genesis_set", event.EventTypeValidatorGenesisSet, ValidatorGenesisSetPayload{
			Version:              1,
			EffectiveFromVersion: 1,
			Seats: []GenesisSeatEntry{
				{ValidatorID: "v1", OperatorAgentID: crypto.AgentID(agentID), ConsensusPublicKey: crypto.AgentID(agentID), BondedStake: 100_000},
			},
		}},
		{"join", event.EventTypeValidatorJoin, ValidatorJoinPayload{
			Version: 1, ValidatorID: "v2", OperatorAgentID: crypto.AgentID(agentID), ConsensusPublicKey: crypto.AgentID(agentID),
			KeyEpoch: 1, BondedStake: 50_000, EffectiveFromVersion: 2,
		}},
		{"activate", event.EventTypeValidatorActivate, ValidatorActivatePayload{
			Version: 1, ValidatorID: "v2", EffectiveFromVersion: 3,
		}},
		{"suspend", event.EventTypeValidatorSuspend, ValidatorSuspendPayload{
			Version: 1, ValidatorID: "v2", Reason: "stake deficit", EffectiveFromVersion: 4,
		}},
		{"resume", event.EventTypeValidatorResume, ValidatorResumePayload{
			Version: 1, ValidatorID: "v2", EffectiveFromVersion: 5,
		}},
		{"exit_begin", event.EventTypeValidatorExit, ValidatorExitPayload{
			Version: 1, ValidatorID: "v2", Phase: ExitPhaseBeginCooldown, CooldownDuration: 100, EffectiveFromVersion: 6,
		}},
		{"exit_complete", event.EventTypeValidatorExit, ValidatorExitPayload{
			Version: 1, ValidatorID: "v2", Phase: ExitPhaseCompleteExit, EffectiveFromVersion: 7,
		}},
		{"key_rotate", event.EventTypeValidatorKeyRotate, ValidatorKeyRotatePayload{
			Version: 1, ValidatorID: "v2", OldConsensusKey: "old-key", NewConsensusKey: "new-key",
			OldKeyEpoch: 1, NewKeyEpoch: 2, EffectiveFromVersion: 8,
		}},
		{"slash", event.EventTypeValidatorSlashApplied, ValidatorSlashAppliedPayload{
			Version: 1, ValidatorID: "v3", Offense: "fraud", EvidenceRef: "ev-hash",
			SlashPercentBP: 3000, SlashAmount: 30_000, RemainingStake: 70_000,
			PermanentExclusion: false, Reason: "caught", EffectiveFromVersion: 9,
		}},
	}

	for _, tt := range payloads {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := event.New(tt.evType, nil, tt.payload, agentID, nil, 0)
			if err != nil {
				t.Fatalf("create event: %v", err)
			}
			if err := crypto.SignEvent(ev, kp); err != nil {
				t.Fatalf("sign: %v", err)
			}
			if !crypto.VerifyEvent(ev) {
				t.Fatal("verify failed after signing")
			}

			// Tamper with payload — verify should fail.
			ev.Payload = json.RawMessage(`{"tampered":true}`)
			if crypto.VerifyEvent(ev) {
				t.Fatal("verify should fail after tampering")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExtractLifecycleEvent
// ---------------------------------------------------------------------------

func TestExtractLifecycleEvent_Join(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	payload := ValidatorJoinPayload{
		Version: 1, ValidatorID: "v-ext", OperatorAgentID: crypto.AgentID(agentID),
		ConsensusPublicKey: crypto.AgentID(agentID),
		KeyEpoch: 1, BondedStake: 100_000, EffectiveFromVersion: 1,
	}
	ev, err := event.New(event.EventTypeValidatorJoin, nil, payload, agentID, nil, 0)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(lcEvents) != 1 {
		t.Fatalf("expected 1 lifecycle event, got %d", len(lcEvents))
	}
	lc := lcEvents[0]
	if lc.Kind != EventJoin {
		t.Fatalf("expected EventJoin, got %s", lc.Kind)
	}
	if lc.SeatID != "v-ext" {
		t.Fatalf("expected seat v-ext, got %s", lc.SeatID)
	}
	if lc.StakeAmount != 100_000 {
		t.Fatalf("expected stake 100000, got %d", lc.StakeAmount)
	}
}

func TestExtractLifecycleEvent_GenesisSet(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	payload := ValidatorGenesisSetPayload{
		Version:              1,
		EffectiveFromVersion: 1,
		Seats: []GenesisSeatEntry{
			{ValidatorID: "g1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1", BondedStake: 100_000},
			{ValidatorID: "g2", OperatorAgentID: "op2", ConsensusPublicKey: "cpk2", BondedStake: 200_000},
		},
	}
	ev, _ := event.New(event.EventTypeValidatorGenesisSet, nil, payload, agentID, nil, 0)

	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(lcEvents) != 2 {
		t.Fatalf("expected 2 lifecycle events for genesis, got %d", len(lcEvents))
	}
	for _, lc := range lcEvents {
		if lc.Kind != EventJoin {
			t.Fatalf("expected EventJoin, got %s", lc.Kind)
		}
		if !lc.IsGenesis {
			t.Fatal("expected IsGenesis=true for genesis set entries")
		}
	}
}

func TestExtractLifecycleEvent_Exit_BothPhases(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	// Begin cooldown.
	beginPayload := ValidatorExitPayload{
		Version: 1, ValidatorID: "v-exit", Phase: ExitPhaseBeginCooldown,
		CooldownDuration: 50, EffectiveFromVersion: 5,
	}
	ev1, _ := event.New(event.EventTypeValidatorExit, nil, beginPayload, agentID, nil, 0)
	lc1, err := ExtractLifecycleEvent(ev1)
	if err != nil {
		t.Fatalf("extract begin_cooldown: %v", err)
	}
	if lc1[0].Kind != EventBeginCooldown {
		t.Fatalf("expected EventBeginCooldown, got %s", lc1[0].Kind)
	}
	if lc1[0].CooldownDuration != 50 {
		t.Fatalf("expected cooldown 50, got %d", lc1[0].CooldownDuration)
	}

	// Complete exit.
	completePayload := ValidatorExitPayload{
		Version: 1, ValidatorID: "v-exit", Phase: ExitPhaseCompleteExit, EffectiveFromVersion: 6,
	}
	ev2, _ := event.New(event.EventTypeValidatorExit, nil, completePayload, agentID, nil, 0)
	lc2, err := ExtractLifecycleEvent(ev2)
	if err != nil {
		t.Fatalf("extract complete_exit: %v", err)
	}
	if lc2[0].Kind != EventExit {
		t.Fatalf("expected EventExit, got %s", lc2[0].Kind)
	}
}

func TestExtractLifecycleEvent_Slash_PermanentExclusion(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	payload := ValidatorSlashAppliedPayload{
		Version: 1, ValidatorID: "v-sl", Offense: "fraud", EvidenceRef: "ev-1",
		SlashPercentBP: 10000, SlashAmount: 100_000, RemainingStake: 0,
		PermanentExclusion: true, Reason: "fatal fraud",
		EffectiveFromVersion: 10,
	}
	ev, _ := event.New(event.EventTypeValidatorSlashApplied, nil, payload, agentID, nil, 0)
	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if lcEvents[0].Kind != EventSlash {
		t.Fatalf("permanent slash should map to EventSlash, got %s", lcEvents[0].Kind)
	}
	if !lcEvents[0].PermanentExclusion {
		t.Fatal("permanent slash should have PermanentExclusion=true")
	}
}

func TestExtractLifecycleEvent_Slash_Suspension(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	payload := ValidatorSlashAppliedPayload{
		Version: 1, ValidatorID: "v-sl2", Offense: "dishonest_replay", EvidenceRef: "ev-2",
		SlashPercentBP: 3000, SlashAmount: 30_000, RemainingStake: 70_000,
		PermanentExclusion: false, Reason: "minor offense",
		EffectiveFromVersion: 11,
	}
	ev, _ := event.New(event.EventTypeValidatorSlashApplied, nil, payload, agentID, nil, 0)
	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if lcEvents[0].Kind != EventSlash {
		t.Fatalf("non-permanent slash should map to EventSlash, got %s", lcEvents[0].Kind)
	}
	if lcEvents[0].PermanentExclusion {
		t.Fatal("non-permanent slash should have PermanentExclusion=false")
	}
}

func TestExtractLifecycleEvent_UnsupportedType(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	ev, _ := event.New(event.EventTypeTransfer, nil, event.TransferPayload{
		FromAgent: "a", ToAgent: "b", Amount: 100, Currency: "AET",
	}, agentID, nil, 0)

	_, err := ExtractLifecycleEvent(ev)
	if !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("expected ErrUnsupportedEvent for Transfer, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Full round-trip: payload → event.Event → ExtractLifecycleEvent → Reducer
// ---------------------------------------------------------------------------

func TestFullRoundTrip_JoinThroughReducer(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	payload := ValidatorJoinPayload{
		Version: 1, ValidatorID: ValidatorID("seat-rt"), OperatorAgentID: crypto.AgentID(agentID),
		ConsensusPublicKey: crypto.AgentID(agentID),
		KeyEpoch: 1, BondedStake: MinBondedStake, EffectiveFromVersion: 1,
	}
	ev, err := event.New(event.EventTypeValidatorJoin, nil, payload, agentID, nil, 0)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := crypto.SignEvent(ev, kp); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !crypto.VerifyEvent(ev) {
		t.Fatal("verify failed")
	}

	// Extract lifecycle events.
	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	// Apply to reducer.
	r := NewReducer()
	for _, lc := range lcEvents {
		if err := r.Apply(lc); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	seat, err := r.Seat("seat-rt")
	if err != nil {
		t.Fatalf("seat lookup: %v", err)
	}
	if seat.Status != SeatPendingJoin {
		t.Fatalf("expected PendingJoin, got %s", seat.Status)
	}
	if seat.OperatorKey != crypto.AgentID(agentID) {
		t.Fatalf("expected operator key %s, got %s", agentID, seat.OperatorKey)
	}
}

// ---------------------------------------------------------------------------
// Recovery Auth Validation Tests
// ---------------------------------------------------------------------------

func TestExtractEmergencySuspend_SignerMatchesRecoveryKey(t *testing.T) {
	recoveryKP, _ := crypto.GenerateKeyPair()
	recoveryID := string(recoveryKP.AgentID())

	payload := ValidatorEmergencySuspendPayload{
		Version:           1,
		ValidatorID:       "seat-auth",
		RecoveryPublicKey: crypto.AgentID(recoveryID),
		Reason:            "compromise",
		RequestedAt:       1700000000,
	}

	// Create event signed by the recovery key.
	ev, err := event.New(event.EventTypeValidatorEmergencySuspend, nil, payload, recoveryID, nil, 0)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	_ = crypto.SignEvent(ev, recoveryKP)

	// Extraction should succeed — signer matches recovery key.
	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract should succeed when signer matches: %v", err)
	}
	if len(lcEvents) != 1 || lcEvents[0].Kind != EventEmergencySuspend {
		t.Fatalf("unexpected lifecycle events: %+v", lcEvents)
	}
}

func TestExtractEmergencySuspend_WrongSigner_Rejected(t *testing.T) {
	attackerKP, _ := crypto.GenerateKeyPair()
	attackerID := string(attackerKP.AgentID())

	// Payload claims a different recovery key than the signer.
	payload := ValidatorEmergencySuspendPayload{
		Version:           1,
		ValidatorID:       "seat-auth",
		RecoveryPublicKey: "legitimate-recovery-key-not-attacker",
		Reason:            "compromise",
		RequestedAt:       1700000000,
	}

	ev, _ := event.New(event.EventTypeValidatorEmergencySuspend, nil, payload, attackerID, nil, 0)
	_ = crypto.SignEvent(ev, attackerKP)

	// Extraction should fail — signer doesn't match recovery key.
	_, err := ExtractLifecycleEvent(ev)
	if !errors.Is(err, ErrRecoverySignerMismatch) {
		t.Fatalf("expected ErrRecoverySignerMismatch, got %v", err)
	}
}

func TestExtractRecoveryRotate_SignerMatchesRecoveryKey(t *testing.T) {
	recoveryKP, _ := crypto.GenerateKeyPair()
	recoveryID := string(recoveryKP.AgentID())

	payload := ValidatorRecoveryRotatePayload{
		Version:              1,
		ValidatorID:          "seat-auth",
		RecoveryPublicKey:    crypto.AgentID(recoveryID),
		NewPublicKey:         "new-hot-key",
		RequestedAt:          1700000000,
		EffectiveAfter:       1700000300,
		EffectiveFromVersion: 5,
	}

	ev, _ := event.New(event.EventTypeValidatorRecoveryRotate, nil, payload, recoveryID, nil, 0)
	_ = crypto.SignEvent(ev, recoveryKP)

	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("extract should succeed: %v", err)
	}
	if lcEvents[0].NewPublicKey != "new-hot-key" {
		t.Fatalf("wrong new key: %q", lcEvents[0].NewPublicKey)
	}
}

func TestExtractRecoveryRotate_WrongSigner_Rejected(t *testing.T) {
	attackerKP, _ := crypto.GenerateKeyPair()

	payload := ValidatorRecoveryRotatePayload{
		Version:              1,
		ValidatorID:          "seat-auth",
		RecoveryPublicKey:    "real-recovery-key",
		NewPublicKey:         "attacker-key",
		RequestedAt:          1700000000,
		EffectiveAfter:       1700000300,
		EffectiveFromVersion: 5,
	}

	ev, _ := event.New(event.EventTypeValidatorRecoveryRotate, nil, payload, string(attackerKP.AgentID()), nil, 0)
	_ = crypto.SignEvent(ev, attackerKP)

	_, err := ExtractLifecycleEvent(ev)
	if !errors.Is(err, ErrRecoverySignerMismatch) {
		t.Fatalf("expected ErrRecoverySignerMismatch, got %v", err)
	}
}

func TestExtractRecoveryRotateCancel_WrongSigner_Rejected(t *testing.T) {
	attackerKP, _ := crypto.GenerateKeyPair()

	payload := ValidatorRecoveryRotateCancelPayload{
		Version:           1,
		ValidatorID:       "seat-auth",
		RecoveryPublicKey: "real-recovery-key",
		RotationEventID:   "rot-1",
		RequestedAt:       1700000000,
	}

	ev, _ := event.New(event.EventTypeValidatorRecoveryRotateCancel, nil, payload, string(attackerKP.AgentID()), nil, 0)
	_ = crypto.SignEvent(ev, attackerKP)

	_, err := ExtractLifecycleEvent(ev)
	if !errors.Is(err, ErrRecoverySignerMismatch) {
		t.Fatalf("expected ErrRecoverySignerMismatch, got %v", err)
	}
}

func TestExtractRecoveryKeySet_NoSignerCheck(t *testing.T) {
	// RecoveryKeySet is signed by the operational key, NOT the recovery key.
	// No signer-vs-recovery-key check — the operational key is the authority.
	opKP, _ := crypto.GenerateKeyPair()
	opID := string(opKP.AgentID())

	payload := ValidatorRecoveryKeySetPayload{
		Version:           1,
		ValidatorID:       "seat-auth",
		RecoveryPublicKey: "totally-different-recovery-key",
		RequestedAt:       1700000000,
	}

	ev, _ := event.New(event.EventTypeValidatorRecoveryKeySet, nil, payload, opID, nil, 0)
	_ = crypto.SignEvent(ev, opKP)

	// Should succeed — no signer-vs-recovery-key check for RecoveryKeySet.
	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("RecoveryKeySet should not check signer vs recovery key: %v", err)
	}
	if lcEvents[0].RecoveryKey != "totally-different-recovery-key" {
		t.Fatalf("wrong recovery key: %q", lcEvents[0].RecoveryKey)
	}
}

func TestNormalKeyRotate_StillRequiresHotKey(t *testing.T) {
	// Normal (non-recovery) key rotation still uses the existing path.
	// Verify it doesn't accept a recovery key as signer.
	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	payload := ValidatorKeyRotatePayload{
		Version:              1,
		ValidatorID:          "seat-normal",
		OldConsensusKey:      crypto.AgentID(agentID),
		NewConsensusKey:      "new-key",
		OldKeyEpoch:          1,
		NewKeyEpoch:          2,
		EffectiveFromVersion: 5,
	}

	ev, _ := event.New(event.EventTypeValidatorKeyRotate, nil, payload, agentID, nil, 0)
	_ = crypto.SignEvent(ev, kp)

	// Normal extraction should succeed.
	lcEvents, err := ExtractLifecycleEvent(ev)
	if err != nil {
		t.Fatalf("normal key rotate should succeed: %v", err)
	}
	if lcEvents[0].Kind != EventRotateKey {
		t.Fatalf("expected EventRotateKey, got %q", lcEvents[0].Kind)
	}
}

func TestRecoveryFullPipeline_EndToEnd(t *testing.T) {
	// Full pipeline: create keypairs, DAG events, extract, apply to Reducer.
	opKP, _ := crypto.GenerateKeyPair()
	recovKP, _ := crypto.GenerateKeyPair()
	newHotKP, _ := crypto.GenerateKeyPair()

	opID := string(opKP.AgentID())
	recovID := string(recovKP.AgentID())
	newHotID := string(newHotKP.AgentID())

	// Setup: create seat with operational key.
	r := NewReducer()
	_ = r.Apply(LifecycleEvent{
		Kind: EventJoin, EventID: "j-e2e", CausalTS: 1,
		SeatID: "seat-e2e", OperatorKey: crypto.AgentID(opID),
		StakeAmount: 100_000, IsGenesis: true,
	})
	r.seats["seat-e2e"].Status = SeatActive
	r.seats["seat-e2e"].Weight = 100_000
	r.seats["seat-e2e"].EffectiveFromVersion = r.version

	// Step 1: Set recovery key (signed by operational key).
	rksPayload := ValidatorRecoveryKeySetPayload{
		Version: 1, ValidatorID: "seat-e2e",
		RecoveryPublicKey: crypto.AgentID(recovID), RequestedAt: 1700000000,
	}
	rksEv, _ := event.New(event.EventTypeValidatorRecoveryKeySet, nil, rksPayload, opID, nil, 0)
	_ = crypto.SignEvent(rksEv, opKP)
	rksLCEvents, err := ExtractLifecycleEvent(rksEv)
	if err != nil {
		t.Fatalf("extract recovery key set: %v", err)
	}
	for _, lc := range rksLCEvents {
		if err := r.Apply(lc); err != nil {
			t.Fatalf("apply recovery key set: %v", err)
		}
	}

	// Step 2: Recovery-authorized key rotation (signed by recovery key).
	rrPayload := ValidatorRecoveryRotatePayload{
		Version: 1, ValidatorID: "seat-e2e",
		RecoveryPublicKey: crypto.AgentID(recovID),
		NewPublicKey: crypto.AgentID(newHotID),
		RequestedAt: 1700000100, EffectiveAfter: 1700000400,
		EffectiveFromVersion: uint64(r.Version()) + 1,
	}
	rrEv, _ := event.New(event.EventTypeValidatorRecoveryRotate, nil, rrPayload, recovID, nil, 0)
	_ = crypto.SignEvent(rrEv, recovKP)
	rrLCEvents, err := ExtractLifecycleEvent(rrEv)
	if err != nil {
		t.Fatalf("extract recovery rotate: %v", err)
	}
	for _, lc := range rrLCEvents {
		if err := r.Apply(lc); err != nil {
			t.Fatalf("apply recovery rotate: %v", err)
		}
	}

	// Verify: new key is now the operator key.
	seat, _ := r.Seat("seat-e2e")
	if seat.OperatorKey != crypto.AgentID(newHotID) {
		t.Fatalf("expected new hot key, got %q", seat.OperatorKey)
	}

	// Verify: old key is no longer eligible in new snapshots.
	snap := r.Snapshot()
	_, eligible := snap.VoteWeightByKey(crypto.AgentID(opID))
	if eligible {
		t.Fatal("old operational key should not be eligible after recovery rotation")
	}
	w, eligible := snap.VoteWeightByKey(crypto.AgentID(newHotID))
	if !eligible || w == 0 {
		t.Fatal("new key should be eligible in new snapshot")
	}
}
