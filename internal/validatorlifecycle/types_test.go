package validatorlifecycle

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

func TestDeriveValidatorID_Deterministic(t *testing.T) {
	id1 := DeriveValidatorID("event-abc-123")
	id2 := DeriveValidatorID("event-abc-123")
	if id1 != id2 {
		t.Fatalf("DeriveValidatorID not deterministic: %s != %s", id1, id2)
	}
	if len(id1) != 64 {
		t.Fatalf("expected 64-char hex, got %d chars: %s", len(id1), id1)
	}
}

func TestDeriveValidatorID_Distinct(t *testing.T) {
	id1 := DeriveValidatorID("event-1")
	id2 := DeriveValidatorID("event-2")
	if id1 == id2 {
		t.Fatal("different events should yield different validator IDs")
	}
}

func TestSeatStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   SeatStatus
		terminal bool
	}{
		{SeatPendingJoin, false},
		{SeatProbationary, false},
		{SeatActive, false},
		{SeatSuspended, false},
		{SeatCoolingDown, false},
		{SeatExited, true},
		{SeatExcluded, true},
	}
	for _, tt := range tests {
		if got := tt.status.IsTerminal(); got != tt.terminal {
			t.Errorf("SeatStatus(%q).IsTerminal() = %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestSeatStatus_IsParticipating(t *testing.T) {
	tests := []struct {
		status       SeatStatus
		participating bool
	}{
		{SeatPendingJoin, false},
		{SeatProbationary, true},
		{SeatActive, true},
		{SeatSuspended, false},
		{SeatCoolingDown, false},
		{SeatExited, false},
		{SeatExcluded, false},
	}
	for _, tt := range tests {
		if got := tt.status.IsParticipating(); got != tt.participating {
			t.Errorf("SeatStatus(%q).IsParticipating() = %v, want %v", tt.status, got, tt.participating)
		}
	}
}

func TestSeatStatus_CanTransitionTo(t *testing.T) {
	// Verify key transitions from the state machine.
	allowed := []struct {
		from, to SeatStatus
	}{
		{SeatPendingJoin, SeatProbationary},
		{SeatPendingJoin, SeatExcluded},
		{SeatProbationary, SeatActive},
		{SeatProbationary, SeatSuspended},
		{SeatProbationary, SeatExcluded},
		{SeatActive, SeatSuspended},
		{SeatActive, SeatCoolingDown},
		{SeatActive, SeatExcluded},
		{SeatSuspended, SeatProbationary}, // reinstatement returns through probation
		{SeatSuspended, SeatCoolingDown},
		{SeatSuspended, SeatExcluded},
		{SeatCoolingDown, SeatExited},
		{SeatCoolingDown, SeatExcluded},
		{SeatExited, SeatPendingJoin},
	}
	for _, tt := range allowed {
		if !tt.from.CanTransitionTo(tt.to) {
			t.Errorf("%s → %s should be allowed", tt.from, tt.to)
		}
	}

	// Verify disallowed transitions.
	disallowed := []struct {
		from, to SeatStatus
	}{
		{SeatExcluded, SeatActive},
		{SeatExcluded, SeatPendingJoin},
		{SeatPendingJoin, SeatActive},
		{SeatPendingJoin, SeatCoolingDown},
		{SeatActive, SeatPendingJoin},
		{SeatSuspended, SeatActive},   // must go through Probationary
		{SeatCoolingDown, SeatActive},
		{SeatExited, SeatActive},
	}
	for _, tt := range disallowed {
		if tt.from.CanTransitionTo(tt.to) {
			t.Errorf("%s → %s should NOT be allowed", tt.from, tt.to)
		}
	}
}

func TestAllSeatStatuses_Complete(t *testing.T) {
	all := AllSeatStatuses()
	if len(all) != 7 {
		t.Fatalf("expected 7 statuses, got %d", len(all))
	}
	seen := make(map[SeatStatus]bool)
	for _, s := range all {
		if seen[s] {
			t.Fatalf("duplicate status: %s", s)
		}
		seen[s] = true
	}
}

func TestValidatorSeat_KeyLookup(t *testing.T) {
	seat := &ValidatorSeat{
		ID: "test-seat",
		KeyHistory: []KeyEpoch{
			{Epoch: 1, PublicKey: crypto.AgentID("key-1"), EffectiveFromCausalTS: 10},
			{Epoch: 2, PublicKey: crypto.AgentID("key-2"), EffectiveFromCausalTS: 50},
			{Epoch: 3, PublicKey: crypto.AgentID("key-3"), EffectiveFromCausalTS: 100},
		},
	}

	// CurrentKeyEpoch
	current := seat.CurrentKeyEpoch()
	if current.Epoch != 3 || current.PublicKey != "key-3" {
		t.Fatalf("CurrentKeyEpoch: got epoch=%d key=%s", current.Epoch, current.PublicKey)
	}

	// KeyAtEpoch
	ke, ok := seat.KeyAtEpoch(2)
	if !ok || ke.PublicKey != "key-2" {
		t.Fatal("KeyAtEpoch(2) failed")
	}
	_, ok = seat.KeyAtEpoch(99)
	if ok {
		t.Fatal("KeyAtEpoch(99) should return false")
	}

	// KeyForCausalTS
	ke, ok = seat.KeyForCausalTS(10) // exact match on epoch 1
	if !ok || ke.PublicKey != "key-1" {
		t.Fatalf("KeyForCausalTS(10): got key=%s", ke.PublicKey)
	}
	ke, ok = seat.KeyForCausalTS(49) // still epoch 1
	if !ok || ke.PublicKey != "key-1" {
		t.Fatalf("KeyForCausalTS(49): got key=%s", ke.PublicKey)
	}
	ke, ok = seat.KeyForCausalTS(50) // epoch 2
	if !ok || ke.PublicKey != "key-2" {
		t.Fatalf("KeyForCausalTS(50): got key=%s", ke.PublicKey)
	}
	ke, ok = seat.KeyForCausalTS(200) // epoch 3
	if !ok || ke.PublicKey != "key-3" {
		t.Fatalf("KeyForCausalTS(200): got key=%s", ke.PublicKey)
	}
	_, ok = seat.KeyForCausalTS(5) // before any epoch
	if ok {
		t.Fatal("KeyForCausalTS(5) should return false (before first epoch)")
	}
}

func TestValidatorSeat_EmptyKeyHistory(t *testing.T) {
	seat := &ValidatorSeat{ID: "empty"}
	ke := seat.CurrentKeyEpoch()
	if ke.Epoch != 0 {
		t.Fatalf("empty KeyHistory should return zero-value KeyEpoch, got epoch=%d", ke.Epoch)
	}
}

func TestCommitteeSnapshot_ComputeDigest_Deterministic(t *testing.T) {
	cs := &CommitteeSnapshot{
		SnapshotVersion: 5,
		RoundID:         "round-abc",
		Members:         []ValidatorID{"v-1", "v-2"},
		MemberWeights:   map[ValidatorID]uint64{"v-1": 100, "v-2": 200},
		TotalWeight:     300,
	}
	d1 := cs.ComputeDigest()
	d2 := cs.ComputeDigest()
	if d1 != d2 {
		t.Fatal("ComputeDigest not deterministic")
	}
	if len(d1) != 64 {
		t.Fatalf("expected 64-char hex digest, got %d chars", len(d1))
	}
}
