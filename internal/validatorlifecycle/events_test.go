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
		EffectiveFromVersion: 1,
		Seats: []GenesisSeatEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1", BondedStake: 100_000},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid genesis set should pass: %v", err)
	}

	// Empty seats.
	empty := &ValidatorGenesisSetPayload{EffectiveFromVersion: 1}
	if err := empty.Validate(); !errors.Is(err, ErrPayloadMissingGenesisSeats) {
		t.Fatalf("expected ErrPayloadMissingGenesisSeats, got %v", err)
	}

	// Missing version.
	noVer := &ValidatorGenesisSetPayload{Seats: valid.Seats}
	if err := noVer.Validate(); !errors.Is(err, ErrPayloadMissingEffectiveVersion) {
		t.Fatalf("expected ErrPayloadMissingEffectiveVersion, got %v", err)
	}

	// Missing operator key on a seat.
	badSeat := &ValidatorGenesisSetPayload{
		EffectiveFromVersion: 1,
		Seats:                []GenesisSeatEntry{{ValidatorID: "v1", ConsensusPublicKey: "cpk1", BondedStake: 100}},
	}
	if err := badSeat.Validate(); !errors.Is(err, ErrPayloadMissingOperatorAgentID) {
		t.Fatalf("expected ErrPayloadMissingOperatorAgentID, got %v", err)
	}

	// Duplicate validator IDs.
	dup := &ValidatorGenesisSetPayload{
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
		ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
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
	valid := &ValidatorSuspendPayload{ValidatorID: "v1", Reason: "stake deficit", EffectiveFromVersion: 3}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid suspend should pass: %v", err)
	}
	noReason := &ValidatorSuspendPayload{ValidatorID: "v1", EffectiveFromVersion: 3}
	if err := noReason.Validate(); !errors.Is(err, ErrPayloadMissingReason) {
		t.Fatalf("expected ErrPayloadMissingReason, got %v", err)
	}
}

func TestValidatorExitPayload_Validate(t *testing.T) {
	beginCD := &ValidatorExitPayload{
		ValidatorID: "v1", Phase: ExitPhaseBeginCooldown,
		CooldownDuration: 100, EffectiveFromVersion: 4,
	}
	if err := beginCD.Validate(); err != nil {
		t.Fatalf("valid begin_cooldown should pass: %v", err)
	}

	completeExit := &ValidatorExitPayload{
		ValidatorID: "v1", Phase: ExitPhaseCompleteExit, EffectiveFromVersion: 5,
	}
	if err := completeExit.Validate(); err != nil {
		t.Fatalf("valid complete_exit should pass: %v", err)
	}

	// begin_cooldown without duration.
	noDur := &ValidatorExitPayload{
		ValidatorID: "v1", Phase: ExitPhaseBeginCooldown, EffectiveFromVersion: 4,
	}
	if err := noDur.Validate(); !errors.Is(err, ErrPayloadMissingCooldownDuration) {
		t.Fatalf("expected ErrPayloadMissingCooldownDuration, got %v", err)
	}

	// Invalid phase.
	badPhase := &ValidatorExitPayload{
		ValidatorID: "v1", Phase: "garbage", EffectiveFromVersion: 4,
	}
	if err := badPhase.Validate(); !errors.Is(err, ErrPayloadInvalidExitPhase) {
		t.Fatalf("expected ErrPayloadInvalidExitPhase, got %v", err)
	}
}

func TestValidatorKeyRotatePayload_Validate(t *testing.T) {
	valid := &ValidatorKeyRotatePayload{
		ValidatorID: "v1", OldConsensusKey: "old", NewConsensusKey: "new",
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
		ValidatorID: "v1", Offense: "fraudulent_approval", EvidenceRef: "ev-hash-1",
		SlashPercent: 30.0, SlashAmount: 30_000, RemainingStake: 70_000,
		PermanentExclusion: false, Reason: "caught cheating",
		EffectiveFromVersion: 5,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid slash should pass: %v", err)
	}

	// Invalid slash percent.
	badPct := *valid
	badPct.SlashPercent = 0
	if err := badPct.Validate(); !errors.Is(err, ErrPayloadInvalidSlashPercent) {
		t.Fatalf("expected ErrPayloadInvalidSlashPercent, got %v", err)
	}
	badPct.SlashPercent = 101
	if err := badPct.Validate(); !errors.Is(err, ErrPayloadInvalidSlashPercent) {
		t.Fatalf("expected ErrPayloadInvalidSlashPercent for >100, got %v", err)
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
	valid := &ValidatorActivatePayload{ValidatorID: "v1", EffectiveFromVersion: 2}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid activate should pass: %v", err)
	}
	noID := &ValidatorActivatePayload{EffectiveFromVersion: 2}
	if err := noID.Validate(); !errors.Is(err, ErrPayloadMissingValidatorID) {
		t.Fatalf("expected ErrPayloadMissingValidatorID, got %v", err)
	}
}

func TestValidatorResumePayload_Validate(t *testing.T) {
	valid := &ValidatorResumePayload{ValidatorID: "v1", EffectiveFromVersion: 3}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid resume should pass: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Canonical Deterministic Serialization
// ---------------------------------------------------------------------------

func TestPayload_CanonicalSerialization_Deterministic(t *testing.T) {
	p := &ValidatorJoinPayload{
		ValidatorID: "v-abc", OperatorAgentID: "op-abc", ConsensusPublicKey: "cpk-abc",
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
		ValidatorID: "v-rt", Offense: "collusion", EvidenceRef: "ev-123",
		SlashPercent: 50.0, SlashAmount: 50_000, RemainingStake: 50_000,
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
		decoded.SlashPercent != original.SlashPercent ||
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
		ValidatorID: "v-det", OperatorAgentID: crypto.AgentID(agentID),
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
		ValidatorID: "v-1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
		KeyEpoch: 1, BondedStake: 100_000, EffectiveFromVersion: 1,
	}
	p2 := ValidatorJoinPayload{
		ValidatorID: "v-2", OperatorAgentID: "op2", ConsensusPublicKey: "cpk2",
		KeyEpoch: 1, BondedStake: 200_000, EffectiveFromVersion: 1,
	}

	ev1, _ := event.New(event.EventTypeValidatorJoin, nil, p1, agentID, nil, 0)
	ev2, _ := event.New(event.EventTypeValidatorJoin, nil, p2, agentID, nil, 0)

	if ev1.ID == ev2.ID {
		t.Fatal("different payloads should produce different EventIDs")
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
			EffectiveFromVersion: 1,
			Seats: []GenesisSeatEntry{
				{ValidatorID: "v1", OperatorAgentID: crypto.AgentID(agentID), ConsensusPublicKey: crypto.AgentID(agentID), BondedStake: 100_000},
			},
		}},
		{"join", event.EventTypeValidatorJoin, ValidatorJoinPayload{
			ValidatorID: "v2", OperatorAgentID: crypto.AgentID(agentID), ConsensusPublicKey: crypto.AgentID(agentID),
			KeyEpoch: 1, BondedStake: 50_000, EffectiveFromVersion: 2,
		}},
		{"activate", event.EventTypeValidatorActivate, ValidatorActivatePayload{
			ValidatorID: "v2", EffectiveFromVersion: 3,
		}},
		{"suspend", event.EventTypeValidatorSuspend, ValidatorSuspendPayload{
			ValidatorID: "v2", Reason: "stake deficit", EffectiveFromVersion: 4,
		}},
		{"resume", event.EventTypeValidatorResume, ValidatorResumePayload{
			ValidatorID: "v2", EffectiveFromVersion: 5,
		}},
		{"exit_begin", event.EventTypeValidatorExit, ValidatorExitPayload{
			ValidatorID: "v2", Phase: ExitPhaseBeginCooldown, CooldownDuration: 100, EffectiveFromVersion: 6,
		}},
		{"exit_complete", event.EventTypeValidatorExit, ValidatorExitPayload{
			ValidatorID: "v2", Phase: ExitPhaseCompleteExit, EffectiveFromVersion: 7,
		}},
		{"key_rotate", event.EventTypeValidatorKeyRotate, ValidatorKeyRotatePayload{
			ValidatorID: "v2", OldConsensusKey: "old-key", NewConsensusKey: "new-key",
			OldKeyEpoch: 1, NewKeyEpoch: 2, EffectiveFromVersion: 8,
		}},
		{"slash", event.EventTypeValidatorSlashApplied, ValidatorSlashAppliedPayload{
			ValidatorID: "v3", Offense: "fraud", EvidenceRef: "ev-hash",
			SlashPercent: 30, SlashAmount: 30_000, RemainingStake: 70_000,
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
		ValidatorID: "v-ext", OperatorAgentID: crypto.AgentID(agentID),
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
		ValidatorID: "v-exit", Phase: ExitPhaseBeginCooldown,
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
		ValidatorID: "v-exit", Phase: ExitPhaseCompleteExit, EffectiveFromVersion: 6,
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
		ValidatorID: "v-sl", Offense: "fraud", EvidenceRef: "ev-1",
		SlashPercent: 100, SlashAmount: 100_000, RemainingStake: 0,
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
		ValidatorID: "v-sl2", Offense: "dishonest_replay", EvidenceRef: "ev-2",
		SlashPercent: 30, SlashAmount: 30_000, RemainingStake: 70_000,
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
		ValidatorID: ValidatorID("seat-rt"), OperatorAgentID: crypto.AgentID(agentID),
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
