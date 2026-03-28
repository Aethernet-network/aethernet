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
		t.Fatalf("wrong FromVersion")
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
