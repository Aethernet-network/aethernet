package validatorlifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// Snapshot takes an immutable point-in-time copy of the Reducer's validator
// set. The returned ValidatorSnapshot is safe for concurrent reads and is
// decoupled from subsequent Reducer mutations.
//
// Consensus rounds should bind to a snapshot version so that votes are
// evaluated against the validator set that was active when the round opened.
func (r *Reducer) Snapshot() *ValidatorSnapshot {
	snap := &ValidatorSnapshot{
		Version: r.version,
		Seats:   make(map[ValidatorID]*ValidatorSeat, len(r.seats)),
	}
	for id, seat := range r.seats {
		c := cloneSeat(seat)
		snap.Seats[id] = c
		// A seat participates in this snapshot only if its current status
		// is participating AND the status change is effective at or before
		// this snapshot version. This ensures joins/activations do not
		// retroactively affect already-open rounds.
		if c.Status.IsParticipating() && c.EffectiveFromVersion <= r.version {
			snap.TotalActiveWeight += c.Weight
			snap.ActiveSeatCount++
		}
	}
	return snap
}

// ComputeDigest computes and stores the deterministic SHA-256 digest of the
// snapshot. The canonical serialization is:
//
//	AETHERNET-VALIDATORSET-V1
//	version:<version>
//	seat:<id>:<status>:<operator_key>:<stake>:<weight>:<key_epoch>
//	... (sorted by seat ID)
//
// Idempotent: calling again recomputes and overwrites. Two Reducers that
// processed the same sequence of LifecycleEvents will produce identical
// digests.
func (vs *ValidatorSnapshot) ComputeDigest() string {
	var b strings.Builder
	b.WriteString("AETHERNET-VALIDATORSET-V1\n")
	b.WriteString(fmt.Sprintf("version:%d\n", vs.Version))
	for _, id := range vs.SortedSeatIDs() {
		seat := vs.Seats[id]
		epoch := seat.CurrentKeyEpoch().Epoch
		b.WriteString(fmt.Sprintf("seat:%s:%s:%s:%d:%d:%d\n",
			id, seat.Status, seat.OperatorKey, seat.StakeAmount, seat.Weight, epoch))
	}
	h := sha256.Sum256([]byte(b.String()))
	vs.Digest = hex.EncodeToString(h[:])
	return vs.Digest
}

// SelectCommittee creates a CommitteeSnapshot from the participating seats
// in this snapshot using the default committee policy. roundID is the
// consensus round identifier (typically the event ID being voted on).
//
// For small networks (≤ MaxSize participating seats), all seats are included.
// For larger networks, deterministic SHA-256-based sortition bounds the
// committee to MaxSize members.
func (vs *ValidatorSnapshot) SelectCommittee(roundID string) *CommitteeSnapshot {
	return SelectBoundedCommittee(vs, roundID, DefaultCommitteePolicy())
}

// SnapshotDelta describes the difference between two consecutive snapshot
// versions. Used for audit logging and protocol diagnostics.
type SnapshotDelta struct {
	FromVersion ValidatorSetVersion `json:"from_version"`
	ToVersion   ValidatorSetVersion `json:"to_version"`
	Joined      []ValidatorID       `json:"joined,omitempty"`
	Activated   []ValidatorID       `json:"activated,omitempty"`
	Suspended   []ValidatorID       `json:"suspended,omitempty"`
	Exited      []ValidatorID       `json:"exited,omitempty"`
	Excluded    []ValidatorID       `json:"excluded,omitempty"`
	KeyRotated  []ValidatorID       `json:"key_rotated,omitempty"`
	StakeChanged []ValidatorID      `json:"stake_changed,omitempty"`
}

// Diff computes the delta between two snapshots. Both snapshots must exist;
// the caller is responsible for providing chronologically ordered snapshots
// (from.Version < to.Version).
func Diff(from, to *ValidatorSnapshot) *SnapshotDelta {
	d := &SnapshotDelta{
		FromVersion: from.Version,
		ToVersion:   to.Version,
	}

	// Detect new seats in `to` that don't exist in `from`.
	for _, id := range to.SortedSeatIDs() {
		toSeat := to.Seats[id]
		fromSeat, existed := from.Seats[id]
		if !existed {
			d.Joined = append(d.Joined, id)
			continue
		}
		if fromSeat.Status != toSeat.Status {
			switch toSeat.Status {
			case SeatActive:
				d.Activated = append(d.Activated, id)
			case SeatSuspended:
				d.Suspended = append(d.Suspended, id)
			case SeatExited:
				d.Exited = append(d.Exited, id)
			case SeatExcluded:
				d.Excluded = append(d.Excluded, id)
			}
		}
		if fromSeat.OperatorKey != toSeat.OperatorKey {
			d.KeyRotated = append(d.KeyRotated, id)
		}
		if fromSeat.StakeAmount != toSeat.StakeAmount {
			d.StakeChanged = append(d.StakeChanged, id)
		}
	}

	return d
}

// ---------------------------------------------------------------------------
// consensus.ValidatorSetSource interface satisfaction
// ---------------------------------------------------------------------------

// VoteWeightByKey returns the consensus weight for the given operator key.
// Returns 0, false if the key does not hold an eligible seat (not found,
// non-participating status, or zero weight). This method allows
// ValidatorSnapshot to satisfy the consensus.ValidatorSetSource interface
// without the validatorlifecycle package importing the consensus package.
func (vs *ValidatorSnapshot) VoteWeightByKey(operatorKey crypto.AgentID) (uint64, bool) {
	for _, seat := range vs.Seats {
		if seat.OperatorKey == operatorKey {
			if !seat.Status.IsParticipating() || seat.Weight == 0 {
				return 0, false
			}
			// Seat's status change must be effective at or before this
			// snapshot version. A seat that joined or activated after this
			// snapshot was taken is not yet eligible.
			if seat.EffectiveFromVersion > vs.Version {
				return 0, false
			}
			return seat.Weight, true
		}
	}
	return 0, false
}

// SetVersion returns the monotonic validator set version. Satisfies
// consensus.ValidatorSetSource.
func (vs *ValidatorSnapshot) SetVersion() uint64 {
	return uint64(vs.Version)
}

// ActiveWeight returns the sum of weights of all participating seats in this
// snapshot. Used by consensus to compute the BFT supermajority threshold over
// total active weight, not just received-vote weight.
// Satisfies consensus.ValidatorSetSource.
func (vs *ValidatorSnapshot) ActiveWeight() uint64 {
	return vs.TotalActiveWeight
}
