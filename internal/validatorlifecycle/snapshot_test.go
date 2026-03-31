package validatorlifecycle

import (
	"testing"
)

func TestSnapshot_Empty(t *testing.T) {
	r := NewReducer()
	snap := r.Snapshot()
	if snap.Version != 0 {
		t.Fatalf("expected version 0, got %d", snap.Version)
	}
	if len(snap.Seats) != 0 {
		t.Fatalf("expected 0 seats, got %d", len(snap.Seats))
	}
	if snap.TotalActiveWeight != 0 {
		t.Fatalf("expected 0 active weight, got %d", snap.TotalActiveWeight)
	}
	if snap.ActiveSeatCount != 0 {
		t.Fatalf("expected 0 active seats, got %d", snap.ActiveSeatCount)
	}
}

func TestSnapshot_ReflectsState(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))

	s1 := DeriveValidatorID("j1")
	s2 := DeriveValidatorID("j2")

	// Make s1 Active, s2 stays PendingJoin.
	r.seats[s1].Status = SeatProbationary
	r.seats[s1].Weight = 50_000
	_ = r.Apply(seatEvent(EventActivate, s1, "act-1", 5))

	snap := r.Snapshot()
	if snap.ActiveSeatCount != 1 {
		t.Fatalf("expected 1 active seat, got %d", snap.ActiveSeatCount)
	}
	if snap.TotalActiveWeight != 100_000 {
		t.Fatalf("expected total active weight 100000, got %d", snap.TotalActiveWeight)
	}

	// s2 is PendingJoin — not participating.
	seat2 := snap.Seats[s2]
	if seat2.Status != SeatPendingJoin {
		t.Fatalf("expected s2 PendingJoin, got %s", seat2.Status)
	}
}

func TestSnapshot_Decoupled(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	s1 := DeriveValidatorID("j1")

	snap := r.Snapshot()

	// Mutate the reducer after snapshot.
	r.seats[s1].Status = SeatActive
	r.seats[s1].Weight = 100_000

	// Snapshot should NOT reflect the post-snapshot mutation.
	if snap.Seats[s1].Status != SeatPendingJoin {
		t.Fatal("snapshot should be decoupled from reducer mutations")
	}
}

func TestSnapshot_ComputeDigest_Deterministic(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))

	snap1 := r.Snapshot()
	d1 := snap1.ComputeDigest()

	snap2 := r.Snapshot()
	d2 := snap2.ComputeDigest()

	if d1 != d2 {
		t.Fatalf("snapshot digests should be deterministic: %s != %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("expected 64-char hex digest, got %d chars", len(d1))
	}
}

func TestSnapshot_ComputeDigest_DiffersOnChange(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))

	snap1 := r.Snapshot()
	d1 := snap1.ComputeDigest()

	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))
	snap2 := r.Snapshot()
	d2 := snap2.ComputeDigest()

	if d1 == d2 {
		t.Fatal("digests should differ after state change")
	}
}

func TestSnapshot_SortedSeatIDs(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("z-event", "kz", 1000, 1))
	_ = r.Apply(joinEvent("a-event", "ka", 2000, 2))
	_ = r.Apply(joinEvent("m-event", "km", 3000, 3))

	snap := r.Snapshot()
	ids := snap.SortedSeatIDs()
	for i := 1; i < len(ids); i++ {
		if string(ids[i]) < string(ids[i-1]) {
			t.Fatalf("seat IDs not sorted: %s before %s", ids[i-1], ids[i])
		}
	}
}

func TestSnapshot_ParticipatingSeats(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))
	_ = r.Apply(joinEvent("j3", "k3", 300_000, 3))

	s1 := DeriveValidatorID("j1")
	s2 := DeriveValidatorID("j2")

	r.seats[s1].Status = SeatActive
	r.seats[s1].Weight = 100_000
	r.seats[s2].Status = SeatProbationary
	r.seats[s2].Weight = 100_000
	// s3 stays PendingJoin.

	snap := r.Snapshot()
	participating := snap.ParticipatingSeats()
	if len(participating) != 2 {
		t.Fatalf("expected 2 participating seats, got %d", len(participating))
	}
}

func TestSnapshot_SelectCommittee(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))

	s1 := DeriveValidatorID("j1")
	s2 := DeriveValidatorID("j2")

	r.seats[s1].Status = SeatActive
	r.seats[s1].Weight = 100_000
	r.seats[s2].Status = SeatActive
	r.seats[s2].Weight = 200_000

	snap := r.Snapshot()
	cs := snap.SelectCommittee("round-123")

	if cs.SnapshotVersion != snap.Version {
		t.Fatalf("committee snapshot version mismatch")
	}
	if cs.RoundID != "round-123" {
		t.Fatalf("expected RoundID=round-123, got %s", cs.RoundID)
	}
	if len(cs.Members) != 2 {
		t.Fatalf("expected 2 committee members, got %d", len(cs.Members))
	}
	if cs.TotalWeight != 300_000 {
		t.Fatalf("expected total weight 300000, got %d", cs.TotalWeight)
	}
	if cs.Digest == "" {
		t.Fatal("committee digest should be computed")
	}
}

func TestSnapshot_SelectCommittee_Deterministic(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))

	s1 := DeriveValidatorID("j1")
	s2 := DeriveValidatorID("j2")

	r.seats[s1].Status = SeatActive
	r.seats[s1].Weight = 100_000
	r.seats[s2].Status = SeatActive
	r.seats[s2].Weight = 200_000

	snap := r.Snapshot()
	cs1 := snap.SelectCommittee("round-x")
	cs2 := snap.SelectCommittee("round-x")

	if cs1.Digest != cs2.Digest {
		t.Fatal("committee selection should be deterministic")
	}
}

func TestDiff_DetectsChanges(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))
	s1 := DeriveValidatorID("j1")
	r.seats[s1].Status = SeatProbationary

	snapBefore := r.Snapshot()

	// Activate s1.
	_ = r.Apply(seatEvent(EventActivate, s1, "act-1", 5))
	// Add a new seat.
	_ = r.Apply(joinEvent("j3", "k3", 300_000, 3))

	snapAfter := r.Snapshot()

	delta := Diff(snapBefore, snapAfter)
	if delta.FromVersion != snapBefore.Version {
		t.Fatalf("wrong FromVersion %d", delta.FromVersion)
	}
	if delta.ToVersion != snapAfter.Version {
		t.Fatalf("wrong ToVersion")
	}
	if len(delta.Joined) != 1 {
		t.Fatalf("expected 1 joined, got %d", len(delta.Joined))
	}
	if len(delta.Activated) != 1 {
		t.Fatalf("expected 1 activated, got %d", len(delta.Activated))
	}
}

// ---------------------------------------------------------------------------
// Recovery Authority Snapshot Tests
// ---------------------------------------------------------------------------

func TestSnapshot_RecoveryKeyPreserved(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	s1 := DeriveValidatorID("j1")

	// Manually set recovery key to simulate a RecoveryKeySet event.
	r.seats[s1].RecoveryKey = "recovery-key-abc"

	snap := r.Snapshot()
	seat := snap.Seats[s1]
	if seat.RecoveryKey != "recovery-key-abc" {
		t.Fatalf("snapshot should preserve recovery key, got %q", seat.RecoveryKey)
	}
}

func TestSnapshot_PendingRotationDoesNotAffectEligibility(t *testing.T) {
	// A seat with a pending recovery rotation should still be eligible in
	// the current snapshot — the rotation hasn't taken effect yet.
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	s1 := DeriveValidatorID("j1")

	// Make the seat active.
	r.seats[s1].Status = SeatActive
	r.seats[s1].Weight = 100_000
	r.seats[s1].EffectiveFromVersion = r.version

	// Set a pending rotation — should NOT affect eligibility.
	r.seats[s1].PendingRotation = &PendingRecoveryRotation{
		RequestEventID: "rot-event-1",
		NewPublicKey:   "new-key-xyz",
		RequestedAt:    1700000000,
		EffectiveAfter: 1700000300,
	}

	snap := r.Snapshot()
	if snap.ActiveSeatCount != 1 {
		t.Fatalf("pending rotation should not affect active seat count, got %d", snap.ActiveSeatCount)
	}
	if snap.TotalActiveWeight != 100_000 {
		t.Fatalf("pending rotation should not affect weight, got %d", snap.TotalActiveWeight)
	}

	// The pending rotation should be in the snapshot for auditability.
	seat := snap.Seats[s1]
	if !seat.HasPendingRotation() {
		t.Fatal("snapshot should preserve pending rotation")
	}
	if seat.PendingRotation.NewPublicKey != "new-key-xyz" {
		t.Fatalf("pending rotation new key mismatch: %q", seat.PendingRotation.NewPublicKey)
	}

	// Verify the current operator key is unchanged.
	w, eligible := snap.VoteWeightByKey("k1")
	if !eligible || w != 100_000 {
		t.Fatalf("current key should still be eligible: weight=%d eligible=%v", w, eligible)
	}
}

func TestSnapshot_SuspendedSeatExcludedFromActiveWeight(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	_ = r.Apply(joinEvent("j2", "k2", 200_000, 2))
	s1 := DeriveValidatorID("j1")
	s2 := DeriveValidatorID("j2")

	// Make both active.
	r.seats[s1].Status = SeatActive
	r.seats[s1].Weight = 100_000
	r.seats[s1].EffectiveFromVersion = r.version
	r.seats[s2].Status = SeatActive
	r.seats[s2].Weight = 200_000
	r.seats[s2].EffectiveFromVersion = r.version

	// Suspend s1 (simulating emergency recovery suspend).
	_ = r.Apply(LifecycleEvent{
		Kind:     EventSuspend,
		EventID:  "suspend-1",
		CausalTS: 10,
		SeatID:   s1,
		Reason:   "recovery:key_compromise",
	})

	snap := r.Snapshot()
	if snap.ActiveSeatCount != 1 {
		t.Fatalf("suspended seat should be excluded: active=%d", snap.ActiveSeatCount)
	}
	if snap.TotalActiveWeight != 200_000 {
		t.Fatalf("suspended seat weight should be excluded: total=%d", snap.TotalActiveWeight)
	}

	// s1 should not be vote-eligible.
	_, eligible := snap.VoteWeightByKey("k1")
	if eligible {
		t.Fatal("suspended seat should not be vote-eligible")
	}

	// s2 should still be eligible.
	w, eligible := snap.VoteWeightByKey("k2")
	if !eligible || w != 200_000 {
		t.Fatalf("active seat should be eligible: weight=%d eligible=%v", w, eligible)
	}
}

func TestSnapshot_PendingRotationDeepCopy(t *testing.T) {
	r := NewReducer()
	_ = r.Apply(joinEvent("j1", "k1", 100_000, 1))
	s1 := DeriveValidatorID("j1")

	r.seats[s1].PendingRotation = &PendingRecoveryRotation{
		RequestEventID: "rot-1",
		NewPublicKey:   "new-k",
		RequestedAt:    1700000000,
		EffectiveAfter: 1700000300,
	}

	snap := r.Snapshot()

	// Mutate the reducer's pending rotation after snapshot.
	r.seats[s1].PendingRotation.NewPublicKey = "mutated"

	// Snapshot should be decoupled.
	if snap.Seats[s1].PendingRotation.NewPublicKey != "new-k" {
		t.Fatal("snapshot pending rotation should be decoupled from reducer mutations")
	}
}

func TestSeat_HasRecoveryKey(t *testing.T) {
	seat := &ValidatorSeat{}
	if seat.HasRecoveryKey() {
		t.Fatal("empty recovery key should return false")
	}
	seat.RecoveryKey = "some-key"
	if !seat.HasRecoveryKey() {
		t.Fatal("set recovery key should return true")
	}
}

func TestSeat_HasPendingRotation(t *testing.T) {
	seat := &ValidatorSeat{}
	if seat.HasPendingRotation() {
		t.Fatal("nil pending rotation should return false")
	}
	seat.PendingRotation = &PendingRecoveryRotation{RequestEventID: "r1"}
	if !seat.HasPendingRotation() {
		t.Fatal("set pending rotation should return true")
	}
}
