package validatorlifecycle

// Property-style determinism tests for the validator lifecycle subsystem.
// These tests verify structural invariants that must hold regardless of
// the specific lifecycle event sequence.

import (
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ---------------------------------------------------------------------------
// Property: Replay determinism — same events → same snapshot digest
// ---------------------------------------------------------------------------

func TestProperty_ReplayDeterminism_ManySequences(t *testing.T) {
	// Generate 10 different lifecycle histories and verify each produces
	// the same digest on two independent replays.
	for seq := 0; seq < 10; seq++ {
		history := generateHistory(seq)
		var digest1, digest2 string

		for attempt := 0; attempt < 2; attempt++ {
			r := NewReducer()
			for _, ev := range history {
				_ = r.Apply(ev)
			}
			snap := r.Snapshot()
			d := snap.ComputeDigest()
			if attempt == 0 {
				digest1 = d
			} else {
				digest2 = d
			}
		}

		if digest1 != digest2 {
			t.Fatalf("sequence %d: replay produced different digests: %s vs %s", seq, digest1, digest2)
		}
	}
}

// generateHistory creates a deterministic lifecycle history parameterized by
// seed. Different seeds produce different event sequences.
func generateHistory(seed int) []LifecycleEvent {
	seatID := ValidatorID(fmt.Sprintf("seat-%d", seed))
	key := crypto.AgentID(fmt.Sprintf("key-%d", seed))
	newKey := crypto.AgentID(fmt.Sprintf("key-%d-rotated", seed))
	ts := uint64(seed * 100)

	events := []LifecycleEvent{
		{Kind: EventJoin, EventID: fmt.Sprintf("j-%d", seed), CausalTS: ts + 1,
			SeatID: seatID, OperatorKey: key, StakeAmount: 100_000, IsGenesis: true},
		{Kind: EventSuspend, EventID: fmt.Sprintf("s-%d", seed), CausalTS: ts + 10,
			SeatID: seatID, Reason: "test"},
		{Kind: EventReinstate, EventID: fmt.Sprintf("r-%d", seed), CausalTS: ts + 20,
			SeatID: seatID},
		{Kind: EventRotateKey, EventID: fmt.Sprintf("rk-%d", seed), CausalTS: ts + 30,
			SeatID: seatID, OperatorKey: newKey, OldOperatorKey: key},
		{Kind: EventStakeUpdate, EventID: fmt.Sprintf("su-%d", seed), CausalTS: ts + 40,
			SeatID: seatID, StakeAmount: 200_000 + uint64(seed)*10_000},
	}

	// After reinstate, seat is Probationary (from earlier patches).
	// Activate requires Probationary → Active, which is allowed.
	events = append(events, LifecycleEvent{
		Kind: EventActivate, EventID: fmt.Sprintf("a-%d", seed), CausalTS: ts + 50,
		SeatID: seatID,
	})

	return events
}

// ---------------------------------------------------------------------------
// Property: No eligible vote is rejected
// ---------------------------------------------------------------------------

func TestProperty_EligibleVotesAccepted(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "k1", ConsensusPublicKey: "k1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
			{ValidatorID: "v2", OperatorAgentID: "k2", ConsensusPublicKey: "k2",
				KeyEpoch: 1, BondedStake: 200_000, InitialStatus: SeatActive},
			{ValidatorID: "v3", OperatorAgentID: "k3", ConsensusPublicKey: "k3",
				KeyEpoch: 1, BondedStake: 300_000, InitialStatus: SeatProbationary},
		},
	}
	r, err := SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	snap := r.Snapshot()

	// Every participating seat should be vote-eligible.
	for _, seat := range snap.ParticipatingSeats() {
		if err := IsSeatVoteEligible(snap, seat.ID); err != nil {
			t.Errorf("participating seat %s should be eligible: %v", seat.ID, err)
		}
		w, eligible := snap.VoteWeightByKey(seat.OperatorKey)
		if !eligible {
			t.Errorf("participating seat %s key %s should be eligible by key", seat.ID, seat.OperatorKey)
		}
		if w == 0 {
			t.Errorf("participating seat %s should have non-zero weight", seat.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Property: No ineligible vote is accepted merely because identity exists
// ---------------------------------------------------------------------------

func TestProperty_IneligibleVotesRejected(t *testing.T) {
	r := NewReducer()
	// Create seats in various non-participating states.
	seats := []struct {
		id     ValidatorID
		key    crypto.AgentID
		status SeatStatus
	}{
		{"pending", "k-pending", SeatPendingJoin},
		{"suspended", "k-suspended", SeatSuspended},
		{"cooling", "k-cooling", SeatCoolingDown},
		{"exited", "k-exited", SeatExited},
		{"excluded", "k-excluded", SeatExcluded},
	}

	for _, s := range seats {
		_ = r.Apply(LifecycleEvent{
			Kind: EventJoin, EventID: "j-" + string(s.id), CausalTS: 1,
			SeatID: s.id, OperatorKey: s.key, StakeAmount: 100_000, IsGenesis: true,
		})
		// Set the desired status directly (test privilege).
		r.seats[s.id].Status = s.status
		r.seats[s.id].Weight = 0
		r.seats[s.id].EffectiveFromVersion = r.version
	}

	snap := r.Snapshot()

	for _, s := range seats {
		err := IsSeatVoteEligible(snap, s.id)
		if err == nil {
			t.Errorf("seat %s (status=%s) should NOT be vote-eligible", s.id, s.status)
		}
		_, eligible := snap.VoteWeightByKey(s.key)
		if eligible {
			t.Errorf("seat %s key %s should NOT be eligible by key", s.id, s.key)
		}
	}
}

// ---------------------------------------------------------------------------
// Property: Version monotonically increases
// ---------------------------------------------------------------------------

func TestProperty_VersionMonotonic(t *testing.T) {
	// Start with Active seats so lifecycle transitions are valid.
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "s1", OperatorAgentID: "k1", ConsensusPublicKey: "k1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
			{ValidatorID: "s2", OperatorAgentID: "k2", ConsensusPublicKey: "k2",
				KeyEpoch: 1, BondedStake: 200_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)
	prevVersion := r.Version()

	events := []LifecycleEvent{
		{Kind: EventSuspend, EventID: "susp-1", CausalTS: 10,
			SeatID: "s1", Reason: "test"},
		{Kind: EventReinstate, EventID: "rein-1", CausalTS: 20,
			SeatID: "s1"},
		{Kind: EventStakeUpdate, EventID: "su-1", CausalTS: 30,
			SeatID: "s2", StakeAmount: 300_000},
	}

	for _, ev := range events {
		if err := r.Apply(ev); err != nil {
			t.Fatalf("apply %s: %v", ev.EventID, err)
		}
		if r.Version() <= prevVersion {
			t.Fatalf("version did not increase: prev=%d current=%d after %s",
				prevVersion, r.Version(), ev.EventID)
		}
		prevVersion = r.Version()
	}
}

// ---------------------------------------------------------------------------
// Property: Snapshot is decoupled from Reducer mutations
// ---------------------------------------------------------------------------

func TestProperty_SnapshotImmutability(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "k1", ConsensusPublicKey: "k1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	snap1 := r.Snapshot()
	digest1 := snap1.ComputeDigest()

	// Mutate the reducer.
	_ = r.Apply(LifecycleEvent{
		Kind: EventJoin, EventID: "j-mut", CausalTS: 100, SeatID: "v2",
		OperatorKey: "k2", StakeAmount: 200_000, IsGenesis: true,
	})

	// snap1 should be unchanged.
	digest1After := snap1.ComputeDigest()
	if digest1 != digest1After {
		t.Fatal("snapshot digest changed after Reducer mutation — snapshot is not immutable")
	}

	snap2 := r.Snapshot()
	digest2 := snap2.ComputeDigest()
	if digest1 == digest2 {
		t.Fatal("new snapshot should differ from old snapshot after state change")
	}
}

// ---------------------------------------------------------------------------
// Property: Committee membership is a subset of participating seats
// ---------------------------------------------------------------------------

func TestProperty_CommitteeSubsetOfParticipating(t *testing.T) {
	// Create 30 seats, various states.
	m := &GenesisManifest{Entries: make([]GenesisManifestEntry, 30)}
	for i := 0; i < 30; i++ {
		m.Entries[i] = GenesisManifestEntry{
			ValidatorID:        ValidatorID(fmt.Sprintf("s-%02d", i)),
			OperatorAgentID:    crypto.AgentID(fmt.Sprintf("k-%02d", i)),
			ConsensusPublicKey: crypto.AgentID(fmt.Sprintf("k-%02d", i)),
			KeyEpoch:           1,
			BondedStake:        uint64(100_000 + i*10_000),
			InitialStatus:      SeatActive,
		}
	}
	r, _ := SeedReducerFromManifest(m)
	snap := r.Snapshot()

	// Select committee for 100 different rounds.
	participatingSet := make(map[ValidatorID]bool)
	for _, seat := range snap.ParticipatingSeats() {
		participatingSet[seat.ID] = true
	}

	for i := 0; i < 100; i++ {
		cs := snap.SelectCommittee(fmt.Sprintf("round-%d", i))
		for _, memberID := range cs.Members {
			if !participatingSet[memberID] {
				t.Fatalf("committee member %s is not in participating set", memberID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Property: EffectiveFromVersion prevents retroactive eligibility
// ---------------------------------------------------------------------------

func TestProperty_NoRetroactiveEligibility(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "gen-1", OperatorAgentID: "k-gen", ConsensusPublicKey: "k-gen",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
		},
	}
	r, _ := SeedReducerFromManifest(m)

	// Take snapshots at each version as new seats join.
	snapshots := []*ValidatorSnapshot{r.Snapshot()}

	for i := 0; i < 5; i++ {
		_ = r.Apply(LifecycleEvent{
			Kind: EventJoin, EventID: fmt.Sprintf("j-%d", i), CausalTS: uint64(100 + i),
			SeatID: ValidatorID(fmt.Sprintf("new-%d", i)),
			OperatorKey: crypto.AgentID(fmt.Sprintf("k-new-%d", i)),
			StakeAmount: 100_000, IsGenesis: true,
		})
		snapshots = append(snapshots, r.Snapshot())
	}

	// For each snapshot, verify that only seats whose EffectiveFromVersion <=
	// snapshot.Version are participating.
	for si, snap := range snapshots {
		for _, seat := range snap.ParticipatingSeats() {
			if seat.EffectiveFromVersion > snap.Version {
				t.Fatalf("snapshot %d (version %d): seat %s has EffectiveFromVersion %d (future!)",
					si, snap.Version, seat.ID, seat.EffectiveFromVersion)
			}
		}
	}

	// Verify earlier snapshots have fewer participating seats than later ones.
	for i := 1; i < len(snapshots); i++ {
		if len(snapshots[i].ParticipatingSeats()) < len(snapshots[i-1].ParticipatingSeats()) {
			t.Fatalf("snapshot %d has fewer participants than snapshot %d", i, i-1)
		}
	}
}

// ---------------------------------------------------------------------------
// Property: Slash amount is reflected exactly in stake
// ---------------------------------------------------------------------------

func TestProperty_SlashAmountExact(t *testing.T) {
	initialStakes := []uint64{100_000, 250_000, 500_000, 1_000_000}
	slashAmounts := []uint64{10_000, 50_000, 100_000, 250_000}

	for _, initial := range initialStakes {
		for _, slash := range slashAmounts {
			if slash > initial {
				continue
			}
			r := NewReducer()
			_ = r.Apply(LifecycleEvent{
				Kind: EventJoin, EventID: "j", CausalTS: 1, SeatID: "s1",
				OperatorKey: "k1", StakeAmount: initial, IsGenesis: true,
			})
			r.seats["s1"].Status = SeatActive
			r.seats["s1"].Weight = initial
			r.seats["s1"].EffectiveFromVersion = r.version

			_ = r.Apply(LifecycleEvent{
				Kind: EventSlash, EventID: "sl", CausalTS: 10, SeatID: "s1",
				Reason: "test", SlashAmount: slash,
			})

			seat, _ := r.Seat("s1")
			expected := initial - slash
			if seat.StakeAmount != expected {
				t.Fatalf("initial=%d slash=%d: expected stake %d, got %d",
					initial, slash, expected, seat.StakeAmount)
			}
		}
	}
}
