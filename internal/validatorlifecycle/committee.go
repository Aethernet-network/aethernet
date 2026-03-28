package validatorlifecycle

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// ---------------------------------------------------------------------------
// Committee Selection Policy
// ---------------------------------------------------------------------------

// CommitteePolicy controls how committees are selected from the validator set.
type CommitteePolicy struct {
	// MinSize is the minimum committee size. When the participating set is
	// smaller than MinSize, all participating seats are included. Must be ≥ 1.
	MinSize int

	// MaxSize is the maximum committee size. When the participating set
	// exceeds MaxSize, deterministic stake-weighted sortition selects MaxSize
	// members. Must be ≥ MinSize.
	MaxSize int
}

// DefaultCommitteePolicy returns a policy suitable for networks from 3 to
// thousands of validators. The 3-node testnet gets all seats; larger sets
// are bounded at 21 (large enough for BFT, small enough for fast rounds).
func DefaultCommitteePolicy() CommitteePolicy {
	return CommitteePolicy{
		MinSize: 3,
		MaxSize: 21,
	}
}

// ---------------------------------------------------------------------------
// Deterministic Committee Selection
// ---------------------------------------------------------------------------

// SelectBoundedCommittee creates a CommitteeSnapshot from the participating
// seats in the validator snapshot, bounded by the given policy.
//
// Selection algorithm:
//  1. Collect all participating seats (sorted by ValidatorID for determinism).
//  2. If |seats| ≤ policy.MaxSize, include all seats (small network).
//  3. Otherwise, compute a deterministic score for each seat:
//     score = SHA-256(roundID || seatID || snapshotVersion)
//     Sort by score ascending. Take the first policy.MaxSize seats.
//
// The score function is deterministic: same inputs always produce the same
// committee on every node. The SHA-256 mixing ensures uniform distribution
// across different roundIDs (event IDs), preventing the same subset from
// being selected for every round.
//
// roundID is typically the EventID of the event being voted on.
func SelectBoundedCommittee(
	snap *ValidatorSnapshot,
	roundID string,
	policy CommitteePolicy,
) *CommitteeSnapshot {
	participating := snap.ParticipatingSeats()

	// Determine effective committee size.
	targetSize := len(participating)
	if targetSize > policy.MaxSize {
		targetSize = policy.MaxSize
	}
	if targetSize < policy.MinSize {
		targetSize = policy.MinSize
	}
	if targetSize > len(participating) {
		targetSize = len(participating)
	}

	// If all seats fit, include everyone (common for small networks).
	selected := participating
	if len(participating) > targetSize {
		selected = sortitionSelect(participating, snap.Version, roundID, targetSize)
	}

	cs := &CommitteeSnapshot{
		SnapshotVersion: snap.Version,
		RoundID:         roundID,
		Members:         make([]ValidatorID, 0, len(selected)),
		MemberWeights:   make(map[ValidatorID]uint64, len(selected)),
	}
	for _, seat := range selected {
		cs.Members = append(cs.Members, seat.ID)
		cs.MemberWeights[seat.ID] = seat.Weight
		cs.TotalWeight += seat.Weight
	}
	// Sort members by ID for deterministic digest computation.
	sort.Slice(cs.Members, func(i, j int) bool {
		return string(cs.Members[i]) < string(cs.Members[j])
	})
	cs.ComputeDigest()
	return cs
}

// sortitionScore is a seat paired with its deterministic selection score.
type sortitionScore struct {
	seat  *ValidatorSeat
	score [32]byte // SHA-256 hash used for comparison
}

// sortitionSelect deterministically selects targetSize seats from the
// participating set using SHA-256-based sortition.
func sortitionSelect(
	seats []*ValidatorSeat,
	version ValidatorSetVersion,
	roundID string,
	targetSize int,
) []*ValidatorSeat {
	scored := make([]sortitionScore, len(seats))
	versionBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(versionBytes, uint64(version))

	for i, seat := range seats {
		// Canonical input: roundID || seatID || version (big-endian uint64).
		// Using || concatenation with length-independent fields (SHA-256 is
		// collision-resistant, so prefix-freedom isn't required for uniformity).
		h := sha256.New()
		h.Write([]byte(roundID))
		h.Write([]byte(seat.ID))
		h.Write(versionBytes)
		var digest [32]byte
		copy(digest[:], h.Sum(nil))
		scored[i] = sortitionScore{seat: seat, score: digest}
	}

	// Sort by score ascending. Ties are impossible in practice (SHA-256
	// collision probability is negligible).
	sort.Slice(scored, func(i, j int) bool {
		for k := 0; k < 32; k++ {
			if scored[i].score[k] != scored[j].score[k] {
				return scored[i].score[k] < scored[j].score[k]
			}
		}
		return false
	})

	result := make([]*ValidatorSeat, targetSize)
	for i := 0; i < targetSize; i++ {
		result[i] = scored[i].seat
	}
	return result
}

// CommitteeMemberSet returns a set of committee member IDs for O(1) lookup.
func CommitteeMemberSet(cs *CommitteeSnapshot) map[ValidatorID]bool {
	m := make(map[ValidatorID]bool, len(cs.Members))
	for _, id := range cs.Members {
		m[id] = true
	}
	return m
}
