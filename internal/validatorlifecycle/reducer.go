package validatorlifecycle

import (
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrSeatNotFound is returned when a lifecycle event targets a seat
	// that does not exist in the current state.
	ErrSeatNotFound = errors.New("validatorlifecycle: seat not found")

	// ErrSeatAlreadyExists is returned when a Join event targets a seat ID
	// that is already present.
	ErrSeatAlreadyExists = errors.New("validatorlifecycle: seat already exists")

	// ErrInvalidTransition is returned when a lifecycle event would cause
	// a state transition that the state machine does not permit.
	ErrInvalidTransition = errors.New("validatorlifecycle: invalid status transition")

	// ErrUnsupportedEvent is returned when the Reducer encounters a
	// LifecycleEventKind that is not yet wired to a DAG event type.
	ErrUnsupportedEvent = errors.New("validatorlifecycle: unsupported event kind")

	// ErrMissingOperatorKey is returned when a Join or RotateKey event is
	// missing the required OperatorKey field.
	ErrMissingOperatorKey = errors.New("validatorlifecycle: operator key required")

	// ErrTerminalSeat is returned when a lifecycle event targets a seat in
	// a terminal state (Exited or Excluded) and the event is not a re-entry
	// Join.
	ErrTerminalSeat = errors.New("validatorlifecycle: seat is in terminal state")

	// ErrCooldownNotElapsed is returned when an Exit event is applied before
	// the required cooldown period has elapsed.
	ErrCooldownNotElapsed = errors.New("validatorlifecycle: cooldown period not elapsed")

	// ErrKeyEpochRegression is returned when a RotateKey event would not
	// advance the key epoch (e.g., same key as current).
	ErrKeyEpochRegression = errors.New("validatorlifecycle: key rotation must advance epoch")

	// ErrInsufficientStake is returned when a Join event's bonded stake is
	// below the MinBondedStake threshold.
	ErrInsufficientStake = errors.New("validatorlifecycle: bonded stake below minimum threshold")

	// ErrOldKeyMismatch is returned when a RotateKey event's OldOperatorKey
	// does not match the seat's current operator key.
	ErrOldKeyMismatch = errors.New("validatorlifecycle: old key does not match seat's current operator key")

	// ErrSlashExceedsStake is returned when a slash amount exceeds the
	// seat's current bonded stake.
	ErrSlashExceedsStake = errors.New("validatorlifecycle: slash amount exceeds bonded stake")
)

// ---------------------------------------------------------------------------
// Reducer
// ---------------------------------------------------------------------------

// Reducer is the deterministic state machine for the validator lifecycle. It
// maintains the canonical validator set and processes lifecycle events in
// causal order. All state is derived from the sequence of LifecycleEvents;
// the Reducer never reads wall-clock time or external state.
//
// Thread safety: the Reducer is NOT safe for concurrent use. The caller must
// serialize event application (which is natural in DAG replay).
type Reducer struct {
	seats   map[ValidatorID]*ValidatorSeat
	version ValidatorSetVersion
}

// NewReducer creates an empty Reducer with no validator seats.
func NewReducer() *Reducer {
	return &Reducer{
		seats:   make(map[ValidatorID]*ValidatorSeat),
		version: 0,
	}
}

// Apply processes a single lifecycle event and advances the validator set
// state. Returns an error if the event is invalid, targets a nonexistent
// seat (except Join), or would cause a disallowed state transition.
//
// Every successful Apply that changes the active validator set increments
// the internal version counter.
func (r *Reducer) Apply(ev LifecycleEvent) error {
	switch ev.Kind {
	case EventJoin:
		return r.applyJoin(ev)
	case EventActivate:
		return r.applyActivate(ev)
	case EventSuspend:
		return r.applySuspend(ev)
	case EventReinstate:
		return r.applyReinstate(ev)
	case EventBeginCooldown:
		return r.applyBeginCooldown(ev)
	case EventExit:
		return r.applyExit(ev)
	case EventExclude:
		return r.applyExclude(ev)
	case EventRotateKey:
		return r.applyRotateKey(ev)
	case EventStakeUpdate:
		return r.applyStakeUpdate(ev)
	case EventSlash:
		return r.applySlash(ev)
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedEvent, ev.Kind)
	}
}

// Version returns the current validator set version.
func (r *Reducer) Version() ValidatorSetVersion { return r.version }

// Seat returns a deep copy of the seat with the given ID, or ErrSeatNotFound.
func (r *Reducer) Seat(id ValidatorID) (*ValidatorSeat, error) {
	s, ok := r.seats[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSeatNotFound, id)
	}
	return cloneSeat(s), nil
}

// SeatByOperatorKey returns the seat currently operated by the given key, or
// ErrSeatNotFound if no seat uses that key.
func (r *Reducer) SeatByOperatorKey(key crypto.AgentID) (*ValidatorSeat, error) {
	for _, s := range r.seats {
		if s.OperatorKey == key {
			return cloneSeat(s), nil
		}
	}
	return nil, fmt.Errorf("%w: operator_key=%s", ErrSeatNotFound, key)
}

// AllSeats returns a deep copy of every seat, sorted by ValidatorID.
func (r *Reducer) AllSeats() []*ValidatorSeat {
	ids := make([]ValidatorID, 0, len(r.seats))
	for id := range r.seats {
		ids = append(ids, id)
	}
	sortValidatorIDs(ids)
	out := make([]*ValidatorSeat, len(ids))
	for i, id := range ids {
		out[i] = cloneSeat(r.seats[id])
	}
	return out
}

// SeatCount returns the total number of seats (including terminal).
func (r *Reducer) SeatCount() int { return len(r.seats) }

// ---------------------------------------------------------------------------
// Apply implementations
// ---------------------------------------------------------------------------

func (r *Reducer) applyJoin(ev LifecycleEvent) error {
	if ev.OperatorKey == "" {
		return ErrMissingOperatorKey
	}

	// Enforce minimum bonded stake. Genesis validators are exempt (their
	// stake is set by protocol parameters, not market dynamics).
	if !ev.IsGenesis && ev.StakeAmount < MinBondedStake {
		return fmt.Errorf("%w: have %d, need %d", ErrInsufficientStake, ev.StakeAmount, MinBondedStake)
	}

	seatID := ev.SeatID
	if seatID == "" {
		seatID = DeriveValidatorID(ev.EventID)
	}

	// The new version after this Apply. Joins take effect at this version,
	// meaning they are NOT eligible in snapshots taken at prior versions.
	nextVersion := r.version + 1

	// Re-entry: if the seat exists and is Exited, allow re-join.
	if existing, ok := r.seats[seatID]; ok {
		if existing.Status != SeatExited {
			return fmt.Errorf("%w: %s (status=%s)", ErrSeatAlreadyExists, seatID, existing.Status)
		}
		if !existing.Status.CanTransitionTo(SeatPendingJoin) {
			return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, existing.Status, SeatPendingJoin)
		}
		// Reset for re-entry.
		existing.Status = SeatPendingJoin
		existing.OperatorKey = ev.OperatorKey
		existing.StakeAmount = ev.StakeAmount
		existing.EffectiveFromVersion = nextVersion
		existing.KeyHistory = append(existing.KeyHistory, KeyEpoch{
			Epoch:                 existing.CurrentKeyEpoch().Epoch + 1,
			PublicKey:             ev.OperatorKey,
			EffectiveFromEventID:  ev.EventID,
			EffectiveFromCausalTS: ev.CausalTS,
		})
		existing.CooldownStartCausalTS = 0
		existing.CooldownDurationEvents = 0
		existing.SuspensionReason = ""
		existing.ProbationTaskCount = 0
		existing.ProbationAccuracy = 0
		r.version = nextVersion
		return nil
	}

	seat := &ValidatorSeat{
		ID:                   seatID,
		Status:               SeatPendingJoin,
		OperatorKey:          ev.OperatorKey,
		StakeAmount:          ev.StakeAmount,
		JoinEventID:          ev.EventID,
		JoinCausalTS:         ev.CausalTS,
		IsGenesis:            ev.IsGenesis,
		EffectiveFromVersion: nextVersion,
		KeyHistory: []KeyEpoch{
			{
				Epoch:                 1,
				PublicKey:             ev.OperatorKey,
				EffectiveFromEventID:  ev.EventID,
				EffectiveFromCausalTS: ev.CausalTS,
			},
		},
	}
	r.seats[seatID] = seat
	r.version = nextVersion
	return nil
}

func (r *Reducer) applyActivate(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if !seat.Status.CanTransitionTo(SeatActive) {
		return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatActive, ev.SeatID)
	}
	nextVersion := r.version + 1
	seat.Status = SeatActive
	seat.ActivatedCausalTS = ev.CausalTS
	seat.ProbationTaskCount = 0
	seat.ProbationAccuracy = 0
	seat.Weight = seat.StakeAmount
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

func (r *Reducer) applySuspend(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if !seat.Status.CanTransitionTo(SeatSuspended) {
		return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatSuspended, ev.SeatID)
	}
	nextVersion := r.version + 1
	seat.Status = SeatSuspended
	seat.SuspensionReason = ev.Reason
	seat.Weight = 0
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

func (r *Reducer) applyReinstate(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	// Resume returns to Probationary, not Active. The seat must re-earn
	// Active status through the normal probation process.
	if !seat.Status.CanTransitionTo(SeatProbationary) {
		return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatProbationary, ev.SeatID)
	}
	// Enforce cooldown if one was set (e.g., by a slash event). The seat
	// cannot be reinstated until the cooldown period has elapsed.
	if seat.CooldownDurationEvents > 0 && seat.CooldownStartCausalTS > 0 {
		elapsed := ev.CausalTS - seat.CooldownStartCausalTS
		if elapsed < seat.CooldownDurationEvents {
			return fmt.Errorf("%w: need %d, elapsed %d (seat=%s)",
				ErrCooldownNotElapsed, seat.CooldownDurationEvents, elapsed, ev.SeatID)
		}
	}
	nextVersion := r.version + 1
	seat.Status = SeatProbationary
	seat.SuspensionReason = ""
	seat.CooldownStartCausalTS = 0
	seat.CooldownDurationEvents = 0
	seat.ProbationTaskCount = 0
	seat.ProbationAccuracy = 0
	seat.Weight = seat.StakeAmount / 2 // probationary: half weight
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

func (r *Reducer) applyBeginCooldown(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if !seat.Status.CanTransitionTo(SeatCoolingDown) {
		return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatCoolingDown, ev.SeatID)
	}
	nextVersion := r.version + 1
	seat.Status = SeatCoolingDown
	seat.CooldownStartCausalTS = ev.CausalTS
	seat.CooldownDurationEvents = ev.CooldownDuration
	seat.Weight = 0
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

func (r *Reducer) applyExit(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if !seat.Status.CanTransitionTo(SeatExited) {
		return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatExited, ev.SeatID)
	}
	if seat.CooldownDurationEvents > 0 {
		elapsed := ev.CausalTS - seat.CooldownStartCausalTS
		if elapsed < seat.CooldownDurationEvents {
			return fmt.Errorf("%w: need %d, elapsed %d (seat=%s)",
				ErrCooldownNotElapsed, seat.CooldownDurationEvents, elapsed, ev.SeatID)
		}
	}
	nextVersion := r.version + 1
	seat.Status = SeatExited
	seat.Weight = 0
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

func (r *Reducer) applyExclude(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if seat.Status == SeatExcluded {
		return nil // idempotent
	}
	if seat.Status == SeatExited {
		return fmt.Errorf("%w: cannot exclude exited seat %s", ErrTerminalSeat, ev.SeatID)
	}
	if !seat.Status.CanTransitionTo(SeatExcluded) {
		return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatExcluded, ev.SeatID)
	}
	nextVersion := r.version + 1
	seat.Status = SeatExcluded
	seat.SlashCount++
	seat.LastSlashEventID = ev.EventID
	seat.SuspensionReason = ev.Reason
	seat.Weight = 0
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

func (r *Reducer) applyRotateKey(ev LifecycleEvent) error {
	if ev.OperatorKey == "" {
		return ErrMissingOperatorKey
	}
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if seat.Status.IsTerminal() {
		return fmt.Errorf("%w: cannot rotate key for %s seat %s", ErrTerminalSeat, seat.Status, ev.SeatID)
	}
	if seat.OperatorKey == ev.OperatorKey {
		return fmt.Errorf("%w: new key identical to current (seat=%s)", ErrKeyEpochRegression, ev.SeatID)
	}
	// Validate the old key matches the seat's current operator key. This
	// ensures the rotation was authorized by the current key holder, not
	// an impersonator. When OldOperatorKey is empty (legacy events from
	// before this check was added), the check is skipped for backward
	// compatibility during DAG replay.
	if ev.OldOperatorKey != "" && ev.OldOperatorKey != seat.OperatorKey {
		return fmt.Errorf("%w: seat=%s expected=%s got=%s",
			ErrOldKeyMismatch, ev.SeatID, seat.OperatorKey, ev.OldOperatorKey)
	}
	nextVersion := r.version + 1
	nextEpoch := seat.CurrentKeyEpoch().Epoch + 1
	seat.OperatorKey = ev.OperatorKey
	seat.KeyHistory = append(seat.KeyHistory, KeyEpoch{
		Epoch:                 nextEpoch,
		PublicKey:             ev.OperatorKey,
		EffectiveFromEventID:  ev.EventID,
		EffectiveFromCausalTS: ev.CausalTS,
	})
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

func (r *Reducer) applyStakeUpdate(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if seat.Status.IsTerminal() {
		return fmt.Errorf("%w: cannot update stake for %s seat %s", ErrTerminalSeat, seat.Status, ev.SeatID)
	}
	nextVersion := r.version + 1
	seat.StakeAmount = ev.StakeAmount
	if seat.Status.IsParticipating() {
		seat.Weight = ev.StakeAmount
	}
	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

// applySlash processes a slash event that reduces the seat's bonded stake and
// transitions the seat to either Suspended (with cooldown) or Excluded
// (permanent). This is the unified handler for all slash severities —
// unlike the separate applySuspend/applyExclude handlers, applySlash
// atomically reduces stake AND changes status in a single version increment.
func (r *Reducer) applySlash(ev LifecycleEvent) error {
	seat, err := r.mustSeat(ev.SeatID)
	if err != nil {
		return err
	}
	if seat.Status.IsTerminal() {
		if seat.Status == SeatExcluded {
			return nil // idempotent: already excluded
		}
		return fmt.Errorf("%w: cannot slash %s seat %s", ErrTerminalSeat, seat.Status, ev.SeatID)
	}
	if ev.SlashAmount > seat.StakeAmount {
		return fmt.Errorf("%w: slash=%d stake=%d (seat=%s)",
			ErrSlashExceedsStake, ev.SlashAmount, seat.StakeAmount, ev.SeatID)
	}

	nextVersion := r.version + 1

	// Reduce bonded stake.
	seat.StakeAmount -= ev.SlashAmount
	seat.SlashCount++
	seat.LastSlashEventID = ev.EventID

	if ev.PermanentExclusion {
		// Permanent exclusion — terminal state.
		if !seat.Status.CanTransitionTo(SeatExcluded) {
			return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatExcluded, ev.SeatID)
		}
		seat.Status = SeatExcluded
		seat.SuspensionReason = ev.Reason
		seat.Weight = 0
	} else {
		// Non-permanent slash: suspend with cooldown.
		if !seat.Status.CanTransitionTo(SeatSuspended) {
			return fmt.Errorf("%w: %s → %s (seat=%s)", ErrInvalidTransition, seat.Status, SeatSuspended, ev.SeatID)
		}
		seat.Status = SeatSuspended
		seat.SuspensionReason = ev.Reason
		seat.Weight = 0
		// Set cooldown so reinstatement requires cooldown elapsed check
		// (enforced by future resume/reinstate handlers).
		if ev.CooldownDuration > 0 {
			seat.CooldownStartCausalTS = ev.CausalTS
			seat.CooldownDurationEvents = ev.CooldownDuration
		}
	}

	seat.EffectiveFromVersion = nextVersion
	r.version = nextVersion
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (r *Reducer) mustSeat(id ValidatorID) (*ValidatorSeat, error) {
	s, ok := r.seats[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSeatNotFound, id)
	}
	return s, nil
}

func cloneSeat(s *ValidatorSeat) *ValidatorSeat {
	c := *s
	if len(s.KeyHistory) > 0 {
		c.KeyHistory = make([]KeyEpoch, len(s.KeyHistory))
		copy(c.KeyHistory, s.KeyHistory)
	}
	return &c
}

func sortValidatorIDs(ids []ValidatorID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && string(ids[j]) < string(ids[j-1]); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}
