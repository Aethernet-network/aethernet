package validatorlifecycle

import (
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// makeSnapshot creates a snapshot with n Active seats, each with the given stake.
func makeSnapshot(n int, stake uint64) *ValidatorSnapshot {
	entries := make([]GenesisManifestEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = GenesisManifestEntry{
			ValidatorID:        ValidatorID(fmt.Sprintf("seat-%03d", i)),
			OperatorAgentID:    crypto.AgentID(fmt.Sprintf("op-%03d", i)),
			ConsensusPublicKey: crypto.AgentID(fmt.Sprintf("cpk-%03d", i)),
			KeyEpoch:           1,
			BondedStake:        stake,
			InitialStatus:      SeatActive,
		}
	}
	m := &GenesisManifest{Entries: entries}
	r, err := SeedReducerFromManifest(m)
	if err != nil {
		panic(err)
	}
	return r.Snapshot()
}

// ---------------------------------------------------------------------------
// Deterministic selection
// ---------------------------------------------------------------------------

func TestSelectBoundedCommittee_Deterministic(t *testing.T) {
	snap := makeSnapshot(10, 100_000)
	policy := DefaultCommitteePolicy()

	cs1 := SelectBoundedCommittee(snap, "round-abc", policy)
	cs2 := SelectBoundedCommittee(snap, "round-abc", policy)

	if cs1.Digest != cs2.Digest {
		t.Fatalf("same inputs must produce same committee: %s != %s", cs1.Digest, cs2.Digest)
	}
	if len(cs1.Members) != len(cs2.Members) {
		t.Fatalf("member count mismatch: %d != %d", len(cs1.Members), len(cs2.Members))
	}
	for i := range cs1.Members {
		if cs1.Members[i] != cs2.Members[i] {
			t.Fatalf("member %d differs: %s != %s", i, cs1.Members[i], cs2.Members[i])
		}
	}
}

func TestSelectBoundedCommittee_DifferentRounds_DifferentCommittees(t *testing.T) {
	// With 30 seats and max committee 21, different round IDs should
	// produce different committees (unless astronomically unlikely collision).
	snap := makeSnapshot(30, 100_000)
	policy := DefaultCommitteePolicy()

	cs1 := SelectBoundedCommittee(snap, "round-1", policy)
	cs2 := SelectBoundedCommittee(snap, "round-2", policy)

	if cs1.Digest == cs2.Digest {
		t.Fatal("different round IDs should (almost always) produce different committees")
	}
	// At least one member should differ.
	set1 := CommitteeMemberSet(cs1)
	set2 := CommitteeMemberSet(cs2)
	anyDiff := false
	for id := range set1 {
		if !set2[id] {
			anyDiff = true
			break
		}
	}
	if !anyDiff {
		for id := range set2 {
			if !set1[id] {
				anyDiff = true
				break
			}
		}
	}
	if !anyDiff {
		t.Fatal("expected at least one member difference between different rounds")
	}
}

// ---------------------------------------------------------------------------
// Size bounds
// ---------------------------------------------------------------------------

func TestSelectBoundedCommittee_SmallNetwork_AllSeats(t *testing.T) {
	// 3 seats, MaxSize=21: all seats should be included.
	snap := makeSnapshot(3, 100_000)
	policy := DefaultCommitteePolicy()

	cs := SelectBoundedCommittee(snap, "round-small", policy)
	if len(cs.Members) != 3 {
		t.Fatalf("small network: expected 3 members, got %d", len(cs.Members))
	}
	if cs.TotalWeight != 300_000 {
		t.Fatalf("expected total weight 300000, got %d", cs.TotalWeight)
	}
}

func TestSelectBoundedCommittee_ExactMaxSize(t *testing.T) {
	// 21 seats, MaxSize=21: all seats should be included.
	snap := makeSnapshot(21, 100_000)
	policy := DefaultCommitteePolicy()

	cs := SelectBoundedCommittee(snap, "round-exact", policy)
	if len(cs.Members) != 21 {
		t.Fatalf("expected 21 members, got %d", len(cs.Members))
	}
}

func TestSelectBoundedCommittee_BoundedAtMax(t *testing.T) {
	// 50 seats, MaxSize=21: committee bounded at 21.
	snap := makeSnapshot(50, 100_000)
	policy := DefaultCommitteePolicy()

	cs := SelectBoundedCommittee(snap, "round-bounded", policy)
	if len(cs.Members) != 21 {
		t.Fatalf("expected committee bounded at 21, got %d", len(cs.Members))
	}
}

func TestSelectBoundedCommittee_SingleSeat(t *testing.T) {
	snap := makeSnapshot(1, 100_000)
	policy := DefaultCommitteePolicy()

	cs := SelectBoundedCommittee(snap, "round-one", policy)
	if len(cs.Members) != 1 {
		t.Fatalf("single seat: expected 1 member, got %d", len(cs.Members))
	}
}

func TestSelectBoundedCommittee_CustomPolicy(t *testing.T) {
	snap := makeSnapshot(100, 100_000)
	policy := CommitteePolicy{MinSize: 5, MaxSize: 7}

	cs := SelectBoundedCommittee(snap, "round-custom", policy)
	if len(cs.Members) != 7 {
		t.Fatalf("custom policy: expected 7 members, got %d", len(cs.Members))
	}
}

// ---------------------------------------------------------------------------
// Spread and fairness
// ---------------------------------------------------------------------------

func TestSelectBoundedCommittee_SpreadAcrossRounds(t *testing.T) {
	// With 50 seats and committee size 21, over 100 rounds, most seats
	// should appear at least once — verifying spread.
	snap := makeSnapshot(50, 100_000)
	policy := DefaultCommitteePolicy()

	seatCount := make(map[ValidatorID]int)
	for i := 0; i < 100; i++ {
		cs := SelectBoundedCommittee(snap, fmt.Sprintf("round-%d", i), policy)
		for _, id := range cs.Members {
			seatCount[id]++
		}
	}

	// With 100 rounds × 21 slots = 2100 seat-slots across 50 seats,
	// expected per-seat = 42. No seat should have 0 appearances unless
	// extremely unlucky (statistically negligible with SHA-256 uniformity).
	zeroCount := 0
	for _, count := range seatCount {
		if count == 0 {
			zeroCount++
		}
	}
	if zeroCount > 2 { // allow up to 2 zeros for extreme bad luck
		t.Fatalf("expected good spread: %d seats never selected in 100 rounds", zeroCount)
	}
}

// ---------------------------------------------------------------------------
// Snapshot version affects selection
// ---------------------------------------------------------------------------

func TestSelectBoundedCommittee_VersionChangesCommittee(t *testing.T) {
	// Same seats but different snapshot versions should produce different
	// committees (different version bytes in the hash input).
	snap1 := makeSnapshot(30, 100_000)
	snap2 := makeSnapshot(30, 100_000)
	// Manually change the version.
	snap2.Version = ValidatorSetVersion(999)

	policy := DefaultCommitteePolicy()
	cs1 := SelectBoundedCommittee(snap1, "round-ver", policy)
	cs2 := SelectBoundedCommittee(snap2, "round-ver", policy)

	if cs1.Digest == cs2.Digest {
		t.Fatal("different snapshot versions should produce different committees")
	}
}

// ---------------------------------------------------------------------------
// CommitteeMemberSet helper
// ---------------------------------------------------------------------------

func TestCommitteeMemberSet(t *testing.T) {
	snap := makeSnapshot(5, 100_000)
	cs := SelectBoundedCommittee(snap, "round-set", DefaultCommitteePolicy())

	memberSet := CommitteeMemberSet(cs)
	if len(memberSet) != len(cs.Members) {
		t.Fatalf("member set size mismatch: %d != %d", len(memberSet), len(cs.Members))
	}
	for _, id := range cs.Members {
		if !memberSet[id] {
			t.Fatalf("member %s not in set", id)
		}
	}
}

// ---------------------------------------------------------------------------
// Members are sorted deterministically
// ---------------------------------------------------------------------------

func TestSelectBoundedCommittee_MembersSorted(t *testing.T) {
	snap := makeSnapshot(50, 100_000)
	policy := DefaultCommitteePolicy()
	cs := SelectBoundedCommittee(snap, "round-sorted", policy)

	for i := 1; i < len(cs.Members); i++ {
		if string(cs.Members[i]) < string(cs.Members[i-1]) {
			t.Fatalf("members not sorted: %s before %s", cs.Members[i-1], cs.Members[i])
		}
	}
}
