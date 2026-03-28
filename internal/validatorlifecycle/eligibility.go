package validatorlifecycle

import (
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrSeatNotInSnapshot is returned when a queried seat does not exist
	// in the snapshot.
	ErrSeatNotInSnapshot = fmt.Errorf("validatorlifecycle: seat not in snapshot")

	// ErrSeatNotEligible is returned when a seat exists but is not eligible
	// to vote.
	ErrSeatNotEligible = fmt.Errorf("validatorlifecycle: seat not eligible to vote")

	// ErrSeatNotInCommittee is returned when a seat is not a member of the
	// queried committee.
	ErrSeatNotInCommittee = fmt.Errorf("validatorlifecycle: seat not in committee")

	// ErrNoKeyForEpoch is returned when no key epoch covers the requested
	// point.
	ErrNoKeyForEpoch = fmt.Errorf("validatorlifecycle: no key for epoch")
)

// ---------------------------------------------------------------------------
// Vote Eligibility
// ---------------------------------------------------------------------------

// IsSeatVoteEligible checks whether the given seat is eligible to cast a vote
// in a consensus round bound to the provided snapshot. A seat is eligible if:
//   - It exists in the snapshot.
//   - Its status is participating (Probationary or Active).
//   - Its weight is > 0.
//
// Returns nil if eligible, or a descriptive error if not.
func IsSeatVoteEligible(snap *ValidatorSnapshot, seatID ValidatorID) error {
	seat, ok := snap.Seats[seatID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSeatNotInSnapshot, seatID)
	}
	if !seat.Status.IsParticipating() {
		return fmt.Errorf("%w: seat %s has status %s", ErrSeatNotEligible, seatID, seat.Status)
	}
	if seat.Weight == 0 {
		return fmt.Errorf("%w: seat %s has zero weight", ErrSeatNotEligible, seatID)
	}
	return nil
}

// IsSeatVoteEligibleByKey resolves a voter's crypto.AgentID (operator key) to
// a seat in the snapshot, then checks eligibility. Returns the resolved
// ValidatorID and nil error if eligible.
func IsSeatVoteEligibleByKey(snap *ValidatorSnapshot, operatorKey crypto.AgentID) (ValidatorID, error) {
	for _, id := range snap.SortedSeatIDs() {
		seat := snap.Seats[id]
		if seat.OperatorKey == operatorKey {
			if err := IsSeatVoteEligible(snap, id); err != nil {
				return "", err
			}
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: operator_key=%s", ErrSeatNotInSnapshot, operatorKey)
}

// ---------------------------------------------------------------------------
// Committee Membership
// ---------------------------------------------------------------------------

// IsSeatInCommittee checks whether the given seat is a member of the
// committee. Returns nil if present, or ErrSeatNotInCommittee.
func IsSeatInCommittee(cs *CommitteeSnapshot, seatID ValidatorID) error {
	for _, m := range cs.Members {
		if m == seatID {
			return nil
		}
	}
	return fmt.Errorf("%w: %s in round %s", ErrSeatNotInCommittee, seatID, cs.RoundID)
}

// IsSeatInCommitteeByKey resolves an operator key to a seat via the snapshot,
// then checks committee membership.
func IsSeatInCommitteeByKey(snap *ValidatorSnapshot, cs *CommitteeSnapshot, operatorKey crypto.AgentID) (ValidatorID, error) {
	seatID, err := resolveSeatByKey(snap, operatorKey)
	if err != nil {
		return "", err
	}
	if err := IsSeatInCommittee(cs, seatID); err != nil {
		return "", err
	}
	return seatID, nil
}

// CommitteeWeightFor returns the weight of a seat in the committee, or 0 if
// the seat is not a member.
func CommitteeWeightFor(cs *CommitteeSnapshot, seatID ValidatorID) uint64 {
	return cs.MemberWeights[seatID]
}

// ---------------------------------------------------------------------------
// Key Epoch Resolution
// ---------------------------------------------------------------------------

// ActiveKeyForSeatEpoch returns the Ed25519 public key (as crypto.AgentID)
// that was authoritative for the given seat at the specified key epoch number
// within the snapshot.
func ActiveKeyForSeatEpoch(snap *ValidatorSnapshot, seatID ValidatorID, epoch uint64) (crypto.AgentID, error) {
	seat, ok := snap.Seats[seatID]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSeatNotInSnapshot, seatID)
	}
	ke, found := seat.KeyAtEpoch(epoch)
	if !found {
		return "", fmt.Errorf("%w: seat=%s epoch=%d", ErrNoKeyForEpoch, seatID, epoch)
	}
	return ke.PublicKey, nil
}

// ActiveKeyForSeatAtCausalTS returns the Ed25519 public key that was
// authoritative for the seat at the given causal timestamp. This is used
// when verifying historical signatures — the key that was active when the
// signature was created, not the seat's current key.
func ActiveKeyForSeatAtCausalTS(snap *ValidatorSnapshot, seatID ValidatorID, causalTS uint64) (crypto.AgentID, error) {
	seat, ok := snap.Seats[seatID]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSeatNotInSnapshot, seatID)
	}
	ke, found := seat.KeyForCausalTS(causalTS)
	if !found {
		return "", fmt.Errorf("%w: seat=%s causal_ts=%d", ErrNoKeyForEpoch, seatID, causalTS)
	}
	return ke.PublicKey, nil
}

// ---------------------------------------------------------------------------
// Key Epoch Vote Validation
// ---------------------------------------------------------------------------

// ErrKeyEpochMismatch is returned when a voter presents a key that was not
// the authoritative key for their seat at the snapshot version.
var ErrKeyEpochMismatch = fmt.Errorf("validatorlifecycle: voter key does not match seat's active epoch")

// ValidateVoteKeyEpoch checks that the voter's presented public key matches
// the key that was authoritative for their seat at the snapshot's version.
//
// After a key rotation at version V, snapshots taken before V have the old
// key as the seat's OperatorKey. Snapshots taken at V or later have the new
// key. Since snapshots are immutable deep copies, the OperatorKey in each
// snapshot is correct for that version.
//
// This function checks the snapshot's OperatorKey (correct for this version)
// and also checks the seat's key history for cases where the voter is using
// a historical key that was active at the snapshot's causal time.
//
// Returns the resolved ValidatorID and nil on success.
func ValidateVoteKeyEpoch(snap *ValidatorSnapshot, voterKey crypto.AgentID) (ValidatorID, error) {
	// Primary path: check if the key is the current operator key for any seat.
	seatID, err := IsSeatVoteEligibleByKey(snap, voterKey)
	if err == nil {
		return seatID, nil
	}

	// Secondary path: the key might be a historical key for a seat whose
	// OperatorKey was updated in this snapshot's version. Check key histories.
	for id, seat := range snap.Seats {
		if err := IsSeatVoteEligible(snap, id); err != nil {
			continue // seat not eligible regardless of key
		}
		for i := len(seat.KeyHistory) - 1; i >= 0; i-- {
			ke := seat.KeyHistory[i]
			if ke.PublicKey != voterKey {
				continue
			}
			// This seat had this key at some epoch. Check that the key was
			// NOT superseded before this snapshot version by looking at the
			// next epoch (if any).
			if i+1 < len(seat.KeyHistory) {
				// A later epoch exists — this key was superseded.
				return "", fmt.Errorf("%w: seat=%s key=%s was superseded by epoch %d",
					ErrKeyEpochMismatch, id, voterKey, seat.KeyHistory[i+1].Epoch)
			}
			// This is the latest epoch with this key — it's current.
			return id, nil
		}
	}

	return "", fmt.Errorf("%w: %s", ErrSeatNotInSnapshot, voterKey)
}

// ---------------------------------------------------------------------------
// Aggregate Eligibility Queries
// ---------------------------------------------------------------------------

// EligibleSeatCount returns the number of vote-eligible seats in the snapshot.
func EligibleSeatCount(snap *ValidatorSnapshot) int {
	count := 0
	for id := range snap.Seats {
		if IsSeatVoteEligible(snap, id) == nil {
			count++
		}
	}
	return count
}

// EligibleTotalWeight returns the sum of weights of all vote-eligible seats.
func EligibleTotalWeight(snap *ValidatorSnapshot) uint64 {
	var total uint64
	for id, seat := range snap.Seats {
		if IsSeatVoteEligible(snap, id) == nil {
			total += seat.Weight
		}
	}
	return total
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func resolveSeatByKey(snap *ValidatorSnapshot, key crypto.AgentID) (ValidatorID, error) {
	for id, seat := range snap.Seats {
		if seat.OperatorKey == key {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: operator_key=%s", ErrSeatNotInSnapshot, key)
}
