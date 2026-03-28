// Package validatorlifecycle implements the deterministic validator seat
// lifecycle subsystem for AetherNet.
//
// Validator seats are the canonical unit of consensus participation. Each seat
// has a stable identifier that persists across key rotations, a status that
// moves through a well-defined state machine, and a key epoch that tracks
// which Ed25519 public key is authoritative for the seat at any point.
//
// All seat state is deterministically derived from DAG events via the Reducer.
// No wall-clock time or randomness is used — timestamps come from event
// payloads (DAG-anchored causal time).
//
// Layer: Core Protocol — may import only crypto and standard library.
// Must not import Coordination or Application packages.
package validatorlifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ---------------------------------------------------------------------------
// Validator Seat Identity
// ---------------------------------------------------------------------------

// ValidatorID is a stable, immutable seat identifier. It is derived from the
// DAG event that created the seat (SHA-256 of the join event ID) and persists
// across key rotations. It is NOT the same as crypto.AgentID — an agent
// operates a seat, but the seat outlives any single key.
type ValidatorID string

// DeriveValidatorID computes the canonical ValidatorID from the DAG event ID
// that created the seat. Deterministic: same event ID always yields the same
// validator ID.
func DeriveValidatorID(joinEventID string) ValidatorID {
	h := sha256.Sum256([]byte("aethernet-validator-seat:" + joinEventID))
	return ValidatorID(hex.EncodeToString(h[:]))
}

// String returns the hex representation.
func (v ValidatorID) String() string { return string(v) }

// ---------------------------------------------------------------------------
// Seat Status State Machine
// ---------------------------------------------------------------------------

// SeatStatus is the lifecycle state of a validator seat. The state machine is:
//
//	PendingJoin → Probationary → Active ←→ Suspended
//	                                  ↓         ↓
//	                             CoolingDown → Exited
//	                                  ↓
//	                              (re-entry → PendingJoin)
//
//	Any state except Exited/Excluded → Excluded (via slashing)
//
// All transitions are driven by DAG events processed through the Reducer.
type SeatStatus string

const (
	// SeatPendingJoin is the initial state when a join event is observed but
	// the seat has not yet met the minimum criteria for probation entry
	// (e.g., stake deposit confirmation, genesis inclusion).
	SeatPendingJoin SeatStatus = "pending_join"

	// SeatProbationary means the seat is participating in verification at
	// reduced weight while accumulating the task/accuracy record required
	// for full activation.
	SeatProbationary SeatStatus = "probationary"

	// SeatActive means the seat has completed probation and participates in
	// consensus at full weight.
	SeatActive SeatStatus = "active"

	// SeatSuspended means the seat has been temporarily removed from
	// consensus participation (stake deficit, voluntary pause, minor
	// infraction). The seat retains its ID and can return to Active.
	SeatSuspended SeatStatus = "suspended"

	// SeatCoolingDown means the seat has initiated voluntary exit and is
	// serving a cooldown period before stake can be withdrawn. The seat
	// does not participate in new consensus rounds but must honor
	// commitments from rounds that began before the cooldown.
	SeatCoolingDown SeatStatus = "cooling_down"

	// SeatExited means the seat has completed cooldown and the operator has
	// withdrawn stake. The seat is no longer part of the validator set.
	// Re-entry creates a new PendingJoin transition (potentially reusing
	// the same ValidatorID if the protocol allows).
	SeatExited SeatStatus = "exited"

	// SeatExcluded means the seat has been permanently removed due to
	// slashing for severe offenses. The seat cannot re-enter.
	SeatExcluded SeatStatus = "excluded"
)

// AllSeatStatuses returns all valid status values in canonical order.
func AllSeatStatuses() []SeatStatus {
	return []SeatStatus{
		SeatPendingJoin,
		SeatProbationary,
		SeatActive,
		SeatSuspended,
		SeatCoolingDown,
		SeatExited,
		SeatExcluded,
	}
}

// IsTerminal returns true if the seat cannot transition to any other state.
func (s SeatStatus) IsTerminal() bool {
	return s == SeatExited || s == SeatExcluded
}

// IsParticipating returns true if the seat is eligible to participate in new
// consensus rounds (may still have obligations from prior rounds).
func (s SeatStatus) IsParticipating() bool {
	return s == SeatProbationary || s == SeatActive
}

// validTransitions maps each status to the set of statuses it can transition to.
var validTransitions = map[SeatStatus]map[SeatStatus]bool{
	SeatPendingJoin: {
		SeatProbationary: true,
		SeatExcluded:     true, // slashed before activation
	},
	SeatProbationary: {
		SeatActive:    true,
		SeatSuspended: true,
		SeatExcluded:  true,
	},
	SeatActive: {
		SeatSuspended:  true,
		SeatCoolingDown: true,
		SeatExcluded:   true,
	},
	SeatSuspended: {
		SeatProbationary: true, // reinstatement returns through probation
		SeatCoolingDown:  true,
		SeatExcluded:     true,
	},
	SeatCoolingDown: {
		SeatExited:   true,
		SeatExcluded: true,
	},
	SeatExited: {
		SeatPendingJoin: true, // re-entry
	},
	SeatExcluded: {}, // terminal — no transitions
}

// CanTransitionTo reports whether the state machine permits a transition
// from the current status to the target status.
func (s SeatStatus) CanTransitionTo(target SeatStatus) bool {
	allowed, ok := validTransitions[s]
	if !ok {
		return false
	}
	return allowed[target]
}

// ---------------------------------------------------------------------------
// Key Epoch
// ---------------------------------------------------------------------------

// KeyEpoch tracks the Ed25519 public key that is authoritative for a
// validator seat at a given point in the DAG history. Key rotation creates a
// new epoch; prior epochs remain valid for verifying historical signatures.
type KeyEpoch struct {
	// Epoch is a monotonically increasing counter per seat. The first key
	// is epoch 1.
	Epoch uint64 `json:"epoch"`

	// PublicKey is the hex-encoded Ed25519 public key for this epoch.
	PublicKey crypto.AgentID `json:"public_key"`

	// EffectiveFromEventID is the DAG event that activated this key epoch.
	EffectiveFromEventID string `json:"effective_from_event_id"`

	// EffectiveFromCausalTS is the causal timestamp of the activating event.
	EffectiveFromCausalTS uint64 `json:"effective_from_causal_ts"`
}

// ---------------------------------------------------------------------------
// Validator Seat
// ---------------------------------------------------------------------------

// ValidatorSeat is the canonical state of a single validator seat. All fields
// are deterministically derived from DAG events via the Reducer.
type ValidatorSeat struct {
	// ID is the stable seat identifier, derived from the join event.
	ID ValidatorID `json:"id"`

	// Status is the current lifecycle state.
	Status SeatStatus `json:"status"`

	// OperatorKey is the current Ed25519 public key operating this seat.
	// Updated on key rotation. Used for signature verification of new votes.
	OperatorKey crypto.AgentID `json:"operator_key"`

	// KeyHistory tracks all key epochs for this seat in chronological order.
	// The last entry is the current epoch.
	KeyHistory []KeyEpoch `json:"key_history"`

	// StakeAmount is the current staked amount in µAET.
	StakeAmount uint64 `json:"stake_amount"`

	// JoinEventID is the DAG event that created this seat.
	JoinEventID string `json:"join_event_id"`

	// JoinCausalTS is the causal timestamp of the join event.
	JoinCausalTS uint64 `json:"join_causal_ts"`

	// ActivatedCausalTS is the causal timestamp when the seat entered Active
	// status. Zero if never activated.
	ActivatedCausalTS uint64 `json:"activated_causal_ts,omitempty"`

	// CooldownStartCausalTS is the causal timestamp when cooldown began.
	// Zero if not cooling down.
	CooldownStartCausalTS uint64 `json:"cooldown_start_causal_ts,omitempty"`

	// CooldownDurationEvents is the required cooldown length in causal
	// timestamp units (events). Set by protocol parameters at cooldown start.
	CooldownDurationEvents uint64 `json:"cooldown_duration_events,omitempty"`

	// SuspensionReason records why the seat was suspended. Empty if not
	// suspended.
	SuspensionReason string `json:"suspension_reason,omitempty"`

	// SlashCount is the total number of slash events applied to this seat.
	SlashCount int `json:"slash_count"`

	// LastSlashEventID is the DAG event ID of the most recent slash. Empty
	// if never slashed.
	LastSlashEventID string `json:"last_slash_event_id,omitempty"`

	// ProbationTaskCount tracks tasks completed during current probation.
	ProbationTaskCount int `json:"probation_task_count,omitempty"`

	// ProbationAccuracy is the running accuracy during probation (0.0–1.0).
	ProbationAccuracy float64 `json:"probation_accuracy,omitempty"`

	// IsGenesis is true if this seat was included in the genesis validator set.
	IsGenesis bool `json:"is_genesis,omitempty"`

	// Weight is the effective consensus weight. Computed from stake, status,
	// and protocol parameters. Zero for non-participating seats.
	Weight uint64 `json:"weight"`

	// EffectiveFromVersion is the validator set version at which this seat's
	// current status became effective. A seat with EffectiveFromVersion > V
	// is not yet participating in snapshots taken at version V. This
	// ensures joins and activations do not affect already-open rounds.
	EffectiveFromVersion ValidatorSetVersion `json:"effective_from_version"`
}

// MinBondedStake is the minimum stake (µAET) required to join the validator
// set. Below this threshold, join events are rejected.
const MinBondedStake uint64 = 10_000_000_000 // 10,000 AET

// CurrentKeyEpoch returns the most recent key epoch, or a zero-value
// KeyEpoch if no keys have been registered.
func (s *ValidatorSeat) CurrentKeyEpoch() KeyEpoch {
	if len(s.KeyHistory) == 0 {
		return KeyEpoch{}
	}
	return s.KeyHistory[len(s.KeyHistory)-1]
}

// KeyAtEpoch returns the key epoch with the given epoch number, or false if
// no such epoch exists.
func (s *ValidatorSeat) KeyAtEpoch(epoch uint64) (KeyEpoch, bool) {
	for _, ke := range s.KeyHistory {
		if ke.Epoch == epoch {
			return ke, true
		}
	}
	return KeyEpoch{}, false
}

// KeyForCausalTS returns the key epoch that was active at the given causal
// timestamp. Returns the most recent epoch whose EffectiveFromCausalTS is <=
// the query timestamp. Returns false if no epoch covers the timestamp.
func (s *ValidatorSeat) KeyForCausalTS(causalTS uint64) (KeyEpoch, bool) {
	var best KeyEpoch
	found := false
	for _, ke := range s.KeyHistory {
		if ke.EffectiveFromCausalTS <= causalTS {
			if !found || ke.Epoch > best.Epoch {
				best = ke
				found = true
			}
		}
	}
	return best, found
}

// ---------------------------------------------------------------------------
// Validator Set Version
// ---------------------------------------------------------------------------

// ValidatorSetVersion is a monotonically increasing version number for
// validator set snapshots. Each lifecycle event that changes the active
// validator set increments the version.
type ValidatorSetVersion uint64

// ---------------------------------------------------------------------------
// Validator Snapshot
// ---------------------------------------------------------------------------

// ValidatorSnapshot is an immutable point-in-time view of the entire
// validator set. Consensus rounds bind to a specific snapshot version so
// that votes are evaluated against the validator set that was active when
// the round opened, not the latest local view.
type ValidatorSnapshot struct {
	// Version is the monotonic snapshot version.
	Version ValidatorSetVersion `json:"version"`

	// Seats contains all validator seats (including non-participating ones)
	// at this snapshot version, keyed by ValidatorID.
	Seats map[ValidatorID]*ValidatorSeat `json:"seats"`

	// TotalActiveWeight is the sum of weights of all participating seats.
	TotalActiveWeight uint64 `json:"total_active_weight"`

	// ActiveSeatCount is the number of seats with IsParticipating() == true.
	ActiveSeatCount int `json:"active_seat_count"`

	// Digest is the SHA-256 hash of the canonical snapshot serialization.
	// Computed lazily by ComputeDigest.
	Digest string `json:"digest,omitempty"`
}

// SortedSeatIDs returns all seat IDs in deterministic (lexicographic) order.
func (vs *ValidatorSnapshot) SortedSeatIDs() []ValidatorID {
	ids := make([]ValidatorID, 0, len(vs.Seats))
	for id := range vs.Seats {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return string(ids[i]) < string(ids[j])
	})
	return ids
}

// ParticipatingSeats returns seats that are eligible to participate in new
// consensus rounds, sorted by ValidatorID for determinism. A seat is
// participating only if its status is participating AND its
// EffectiveFromVersion is ≤ the snapshot version.
func (vs *ValidatorSnapshot) ParticipatingSeats() []*ValidatorSeat {
	var seats []*ValidatorSeat
	for _, id := range vs.SortedSeatIDs() {
		seat := vs.Seats[id]
		if seat.Status.IsParticipating() && seat.EffectiveFromVersion <= vs.Version {
			seats = append(seats, seat)
		}
	}
	return seats
}

// ---------------------------------------------------------------------------
// Committee Snapshot
// ---------------------------------------------------------------------------

// CommitteeSnapshot represents the specific subset of validators selected for
// a consensus round. Bound to a ValidatorSnapshot version.
type CommitteeSnapshot struct {
	// SnapshotVersion identifies the validator set this committee was derived
	// from.
	SnapshotVersion ValidatorSetVersion `json:"snapshot_version"`

	// RoundID is an opaque identifier for the consensus round this committee
	// serves. Typically the event ID being voted on.
	RoundID string `json:"round_id"`

	// Members lists the seat IDs in this committee, in deterministic order.
	Members []ValidatorID `json:"members"`

	// MemberWeights maps each committee member to their weight at snapshot
	// time.
	MemberWeights map[ValidatorID]uint64 `json:"member_weights"`

	// TotalWeight is the sum of all member weights.
	TotalWeight uint64 `json:"total_weight"`

	// Digest is the SHA-256 hash of the canonical committee serialization.
	Digest string `json:"digest,omitempty"`
}

// ComputeDigest computes and stores the deterministic digest for this
// committee snapshot. Idempotent.
func (cs *CommitteeSnapshot) ComputeDigest() string {
	var b strings.Builder
	b.WriteString("AETHERNET-COMMITTEE-V1\n")
	b.WriteString(fmt.Sprintf("snapshot_version:%d\n", cs.SnapshotVersion))
	b.WriteString("round_id:" + cs.RoundID + "\n")
	for _, id := range cs.Members {
		w := cs.MemberWeights[id]
		b.WriteString(fmt.Sprintf("member:%s:%d\n", id, w))
	}
	h := sha256.Sum256([]byte(b.String()))
	cs.Digest = hex.EncodeToString(h[:])
	return cs.Digest
}

// ---------------------------------------------------------------------------
// Lifecycle Event Classification
// ---------------------------------------------------------------------------

// LifecycleEventKind classifies the DAG events that the Reducer processes.
// These map to future EventType constants; the Reducer uses this enum
// internally so the classification is explicit and auditable.
type LifecycleEventKind string

const (
	EventJoin          LifecycleEventKind = "validator_join"
	EventActivate      LifecycleEventKind = "validator_activate"
	EventSuspend       LifecycleEventKind = "validator_suspend"
	EventReinstate     LifecycleEventKind = "validator_reinstate"
	EventBeginCooldown LifecycleEventKind = "validator_begin_cooldown"
	EventExit          LifecycleEventKind = "validator_exit"
	EventExclude       LifecycleEventKind = "validator_exclude"
	EventRotateKey     LifecycleEventKind = "validator_rotate_key"
	EventStakeUpdate   LifecycleEventKind = "validator_stake_update"
	EventSlash         LifecycleEventKind = "validator_slash"
)

// LifecycleEvent is the input to the Reducer. It wraps the minimum data
// extracted from a DAG event needed to advance the validator state machine.
// The caller is responsible for extracting this from event.Event payloads.
type LifecycleEvent struct {
	// Kind identifies the lifecycle transition.
	Kind LifecycleEventKind `json:"kind"`

	// EventID is the originating DAG event ID (for auditability).
	EventID string `json:"event_id"`

	// CausalTS is the causal timestamp from the DAG event.
	CausalTS uint64 `json:"causal_ts"`

	// SeatID is the target validator seat. For Join events, this is
	// derived from EventID via DeriveValidatorID.
	SeatID ValidatorID `json:"seat_id"`

	// OperatorKey is the Ed25519 public key (hex). Required for Join and
	// RotateKey events.
	OperatorKey crypto.AgentID `json:"operator_key,omitempty"`

	// StakeAmount is the stake in µAET. Required for Join and StakeUpdate.
	StakeAmount uint64 `json:"stake_amount,omitempty"`

	// Reason is a human-readable reason. Used for Suspend and Exclude.
	Reason string `json:"reason,omitempty"`

	// CooldownDuration is the required cooldown in causal timestamp units.
	// Required for BeginCooldown and Slash (non-permanent).
	CooldownDuration uint64 `json:"cooldown_duration,omitempty"`

	// IsGenesis is true for genesis validator join events.
	IsGenesis bool `json:"is_genesis,omitempty"`

	// OldOperatorKey is the current operator key being replaced. Required
	// for RotateKey events to validate the caller possesses the old key.
	OldOperatorKey crypto.AgentID `json:"old_operator_key,omitempty"`

	// SlashAmount is the µAET amount deducted from the seat's bonded stake
	// by a slash event. Required for EventSlash.
	SlashAmount uint64 `json:"slash_amount,omitempty"`

	// PermanentExclusion indicates whether a slash permanently excludes the
	// seat (terminal) versus temporarily suspending it with cooldown.
	PermanentExclusion bool `json:"permanent_exclusion,omitempty"`
}
