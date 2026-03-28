package validatorlifecycle

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupEligibilitySnapshot() (*ValidatorSnapshot, ValidatorID, ValidatorID, ValidatorID) {
	sActive := ValidatorID("seat-active")
	sSuspended := ValidatorID("seat-suspended")
	sPending := ValidatorID("seat-pending")

	// Use SeedReducerFromManifest for the active seat.
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: sActive, OperatorAgentID: "key-active", ConsensusPublicKey: "key-active",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Add a suspended seat via join + manual status set.
	_ = r.Apply(LifecycleEvent{
		Kind: EventJoin, EventID: "j-suspended", CausalTS: 10,
		SeatID: sSuspended, OperatorKey: "key-suspended",
		StakeAmount: 50_000, IsGenesis: true, // genesis exempt from min stake
	})
	r.seats[sSuspended].Status = SeatSuspended
	r.seats[sSuspended].Weight = 0
	r.seats[sSuspended].EffectiveFromVersion = r.version

	// Add a pending-join seat.
	_ = r.Apply(LifecycleEvent{
		Kind: EventJoin, EventID: "j-pending", CausalTS: 20,
		SeatID: sPending, OperatorKey: "key-pending",
		StakeAmount: 10_000, IsGenesis: true,
	})

	return r.Snapshot(), sActive, sSuspended, sPending
}

// ---------------------------------------------------------------------------
// IsSeatVoteEligible
// ---------------------------------------------------------------------------

func TestIsSeatVoteEligible_Active(t *testing.T) {
	snap, sActive, _, _ := setupEligibilitySnapshot()
	if err := IsSeatVoteEligible(snap, sActive); err != nil {
		t.Fatalf("Active seat should be eligible: %v", err)
	}
}

func TestIsSeatVoteEligible_Suspended(t *testing.T) {
	snap, _, sSuspended, _ := setupEligibilitySnapshot()
	if err := IsSeatVoteEligible(snap, sSuspended); err == nil {
		t.Fatal("Suspended seat should NOT be eligible")
	}
}

func TestIsSeatVoteEligible_PendingJoin(t *testing.T) {
	snap, _, _, sPending := setupEligibilitySnapshot()
	if err := IsSeatVoteEligible(snap, sPending); err == nil {
		t.Fatal("PendingJoin seat should NOT be eligible")
	}
}

func TestIsSeatVoteEligible_NotInSnapshot(t *testing.T) {
	snap, _, _, _ := setupEligibilitySnapshot()
	err := IsSeatVoteEligible(snap, "nonexistent-seat")
	if !errors.Is(err, ErrSeatNotInSnapshot) {
		t.Fatalf("expected ErrSeatNotInSnapshot, got %v", err)
	}
}

func TestIsSeatVoteEligible_ZeroWeight(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j-zw", "k-zw", 0, 1))
	seatID := DeriveValidatorID("j-zw")
	r.seats[seatID].Status = SeatActive
	r.seats[seatID].Weight = 0

	snap := r.Snapshot()
	if err := IsSeatVoteEligible(snap, seatID); err == nil {
		t.Fatal("zero-weight Active seat should NOT be eligible")
	}
}

// ---------------------------------------------------------------------------
// IsSeatVoteEligibleByKey
// ---------------------------------------------------------------------------

func TestIsSeatVoteEligibleByKey_Found(t *testing.T) {
	snap, sActive, _, _ := setupEligibilitySnapshot()
	id, err := IsSeatVoteEligibleByKey(snap, crypto.AgentID("key-active"))
	if err != nil {
		t.Fatalf("expected eligible: %v", err)
	}
	if id != sActive {
		t.Fatalf("expected seat %s, got %s", sActive, id)
	}
}

func TestIsSeatVoteEligibleByKey_NotFound(t *testing.T) {
	snap, _, _, _ := setupEligibilitySnapshot()
	_, err := IsSeatVoteEligibleByKey(snap, crypto.AgentID("key-unknown"))
	if !errors.Is(err, ErrSeatNotInSnapshot) {
		t.Fatalf("expected ErrSeatNotInSnapshot, got %v", err)
	}
}

func TestIsSeatVoteEligibleByKey_SuspendedKey(t *testing.T) {
	snap, _, _, _ := setupEligibilitySnapshot()
	_, err := IsSeatVoteEligibleByKey(snap, crypto.AgentID("key-suspended"))
	if err == nil {
		t.Fatal("suspended seat's key should not be eligible")
	}
}

// ---------------------------------------------------------------------------
// IsSeatInCommittee
// ---------------------------------------------------------------------------

func TestIsSeatInCommittee(t *testing.T) {
	snap, sActive, _, _ := setupEligibilitySnapshot()
	cs := snap.SelectCommittee("round-1")

	if err := IsSeatInCommittee(cs, sActive); err != nil {
		t.Fatalf("Active seat should be in committee: %v", err)
	}
}

func TestIsSeatInCommittee_NotMember(t *testing.T) {
	snap, _, _, _ := setupEligibilitySnapshot()
	cs := snap.SelectCommittee("round-1")

	err := IsSeatInCommittee(cs, "nonexistent-seat")
	if !errors.Is(err, ErrSeatNotInCommittee) {
		t.Fatalf("expected ErrSeatNotInCommittee, got %v", err)
	}
}

func TestIsSeatInCommitteeByKey(t *testing.T) {
	snap, sActive, _, _ := setupEligibilitySnapshot()
	cs := snap.SelectCommittee("round-1")

	id, err := IsSeatInCommitteeByKey(snap, cs, crypto.AgentID("key-active"))
	if err != nil {
		t.Fatalf("expected in committee: %v", err)
	}
	if id != sActive {
		t.Fatalf("expected %s, got %s", sActive, id)
	}
}

func TestCommitteeWeightFor(t *testing.T) {
	snap, sActive, _, _ := setupEligibilitySnapshot()
	cs := snap.SelectCommittee("round-1")

	w := CommitteeWeightFor(cs, sActive)
	if w != 100_000 {
		t.Fatalf("expected weight 100000, got %d", w)
	}
	w = CommitteeWeightFor(cs, "nonexistent")
	if w != 0 {
		t.Fatalf("expected 0 for non-member, got %d", w)
	}
}

// ---------------------------------------------------------------------------
// Key epoch resolution
// ---------------------------------------------------------------------------

func TestActiveKeyForSeatEpoch(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j-ke", "key-v1", 100_000, 1))
	seatID := DeriveValidatorID("j-ke")
	r.seats[seatID].Status = SeatActive

	// Rotate key.
	_ = r.Apply(LifecycleEvent{
		Kind:        EventRotateKey,
		EventID:     "rot-1",
		CausalTS:    50,
		SeatID:      seatID,
		OperatorKey: "key-v2",
	})

	snap := r.Snapshot()

	// Epoch 1 → key-v1
	key, err := ActiveKeyForSeatEpoch(snap, seatID, 1)
	if err != nil {
		t.Fatalf("epoch 1: %v", err)
	}
	if key != "key-v1" {
		t.Fatalf("expected key-v1, got %s", key)
	}

	// Epoch 2 → key-v2
	key, err = ActiveKeyForSeatEpoch(snap, seatID, 2)
	if err != nil {
		t.Fatalf("epoch 2: %v", err)
	}
	if key != "key-v2" {
		t.Fatalf("expected key-v2, got %s", key)
	}

	// Epoch 99 → error
	_, err = ActiveKeyForSeatEpoch(snap, seatID, 99)
	if !errors.Is(err, ErrNoKeyForEpoch) {
		t.Fatalf("expected ErrNoKeyForEpoch, got %v", err)
	}
}

func TestActiveKeyForSeatAtCausalTS(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j-ts", "key-epoch1", 100_000, 10))
	seatID := DeriveValidatorID("j-ts")
	r.seats[seatID].Status = SeatActive

	_ = r.Apply(LifecycleEvent{
		Kind:        EventRotateKey,
		EventID:     "rot-ts",
		CausalTS:    50,
		SeatID:      seatID,
		OperatorKey: "key-epoch2",
	})

	snap := r.Snapshot()

	// Before first epoch → error
	_, err := ActiveKeyForSeatAtCausalTS(snap, seatID, 5)
	if !errors.Is(err, ErrNoKeyForEpoch) {
		t.Fatalf("expected ErrNoKeyForEpoch for causalTS=5, got %v", err)
	}

	// At epoch 1 boundary
	key, err := ActiveKeyForSeatAtCausalTS(snap, seatID, 10)
	if err != nil || key != "key-epoch1" {
		t.Fatalf("causalTS=10: expected key-epoch1, got %s (err=%v)", key, err)
	}

	// Between epochs
	key, err = ActiveKeyForSeatAtCausalTS(snap, seatID, 40)
	if err != nil || key != "key-epoch1" {
		t.Fatalf("causalTS=40: expected key-epoch1, got %s (err=%v)", key, err)
	}

	// At epoch 2 boundary
	key, err = ActiveKeyForSeatAtCausalTS(snap, seatID, 50)
	if err != nil || key != "key-epoch2" {
		t.Fatalf("causalTS=50: expected key-epoch2, got %s (err=%v)", key, err)
	}

	// After epoch 2
	key, err = ActiveKeyForSeatAtCausalTS(snap, seatID, 999)
	if err != nil || key != "key-epoch2" {
		t.Fatalf("causalTS=999: expected key-epoch2, got %s (err=%v)", key, err)
	}
}

func TestActiveKeyForSeatEpoch_NotInSnapshot(t *testing.T) {
	snap := &ValidatorSnapshot{Seats: map[ValidatorID]*ValidatorSeat{}}
	_, err := ActiveKeyForSeatEpoch(snap, "ghost", 1)
	if !errors.Is(err, ErrSeatNotInSnapshot) {
		t.Fatalf("expected ErrSeatNotInSnapshot, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Aggregate queries
// ---------------------------------------------------------------------------

func TestEligibleSeatCount(t *testing.T) {
	snap, _, _, _ := setupEligibilitySnapshot()
	count := EligibleSeatCount(snap)
	if count != 1 { // only the Active seat is eligible
		t.Fatalf("expected 1 eligible, got %d", count)
	}
}

func TestEligibleTotalWeight(t *testing.T) {
	snap, _, _, _ := setupEligibilitySnapshot()
	w := EligibleTotalWeight(snap)
	if w != 100_000 { // only the Active seat
		t.Fatalf("expected 100000, got %d", w)
	}
}
