package validatorlifecycle

// This file defines the canonical DAG event payload structs for validator
// lifecycle transitions. Each payload is used with event.New() to create
// standard event.Event objects. The payloads are deserialized via
// event.GetPayload[T]() like any other event payload.
//
// These structs participate in normal event canonicalization — they are
// marshaled to json.RawMessage at event creation time and included in the
// canonical hash and signature. No parallel event model is introduced.

import (
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ---------------------------------------------------------------------------
// Sentinel validation errors
// ---------------------------------------------------------------------------

var (
	ErrPayloadMissingValidatorID      = errors.New("validatorlifecycle: validator_id is required")
	ErrPayloadMissingOperatorAgentID  = errors.New("validatorlifecycle: operator_agent_id is required")
	ErrPayloadMissingConsensusKey     = errors.New("validatorlifecycle: consensus_public_key is required")
	ErrPayloadMissingBondedStake      = errors.New("validatorlifecycle: bonded_stake must be > 0")
	ErrPayloadMissingKeyEpoch         = errors.New("validatorlifecycle: key_epoch must be > 0")
	ErrPayloadMissingReason           = errors.New("validatorlifecycle: reason is required")
	ErrPayloadMissingEvidenceRef      = errors.New("validatorlifecycle: evidence_ref is required for slash")
	ErrPayloadMissingOffense          = errors.New("validatorlifecycle: offense is required for slash")
	ErrPayloadInvalidSlashPercent     = errors.New("validatorlifecycle: slash_percent must be in (0, 100]")
	ErrPayloadKeyEpochNotAdvanced     = errors.New("validatorlifecycle: new_key_epoch must be > old_key_epoch")
	ErrPayloadMissingGenesisSeats     = errors.New("validatorlifecycle: genesis set must contain at least one seat")
	ErrPayloadMissingEffectiveVersion = errors.New("validatorlifecycle: effective_from_version is required")
	ErrPayloadMissingCooldownDuration = errors.New("validatorlifecycle: cooldown_duration must be > 0 for begin_cooldown")
	ErrPayloadInvalidExitPhase        = errors.New("validatorlifecycle: exit_phase must be begin_cooldown or complete_exit")
)

// ---------------------------------------------------------------------------
// Genesis Set Payload
// ---------------------------------------------------------------------------

// GenesisSeatEntry describes a single validator in the genesis set.
type GenesisSeatEntry struct {
	// ValidatorID is the stable seat identifier for this genesis validator.
	ValidatorID ValidatorID `json:"validator_id"`

	// OperatorAgentID is the hex-encoded Ed25519 public key of the operator.
	OperatorAgentID crypto.AgentID `json:"operator_agent_id"`

	// ConsensusPublicKey is the Ed25519 public key used for consensus signing.
	// For genesis validators this is typically the same as OperatorAgentID.
	ConsensusPublicKey crypto.AgentID `json:"consensus_public_key"`

	// BondedStake is the initial staked amount in µAET.
	BondedStake uint64 `json:"bonded_stake"`
}

// ValidatorGenesisSetPayload bootstraps the initial validator set. Emitted
// once during genesis. Each seat enters Active status immediately (genesis
// validators bypass PendingJoin and Probationary).
type ValidatorGenesisSetPayload struct {
	// Seats is the ordered list of genesis validator seats.
	Seats []GenesisSeatEntry `json:"seats"`

	// EffectiveFromVersion is the initial ValidatorSetVersion after this
	// event is applied. Must be 1 for genesis.
	EffectiveFromVersion uint64 `json:"effective_from_version"`
}

// Validate checks required fields and structural invariants.
func (p *ValidatorGenesisSetPayload) Validate() error {
	if len(p.Seats) == 0 {
		return ErrPayloadMissingGenesisSeats
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	seen := make(map[ValidatorID]bool, len(p.Seats))
	for i, s := range p.Seats {
		if s.ValidatorID == "" {
			return fmt.Errorf("%w (seat index %d)", ErrPayloadMissingValidatorID, i)
		}
		if seen[s.ValidatorID] {
			return fmt.Errorf("validatorlifecycle: duplicate validator_id %s in genesis set", s.ValidatorID)
		}
		seen[s.ValidatorID] = true
		if s.OperatorAgentID == "" {
			return fmt.Errorf("%w (seat %s)", ErrPayloadMissingOperatorAgentID, s.ValidatorID)
		}
		if s.ConsensusPublicKey == "" {
			return fmt.Errorf("%w (seat %s)", ErrPayloadMissingConsensusKey, s.ValidatorID)
		}
		if s.BondedStake == 0 {
			return fmt.Errorf("%w (seat %s)", ErrPayloadMissingBondedStake, s.ValidatorID)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Join Payload
// ---------------------------------------------------------------------------

// ValidatorJoinPayload records a new validator requesting to join the set.
// Creates a seat in PendingJoin status.
type ValidatorJoinPayload struct {
	// ValidatorID is derived from the join event ID by the Reducer.
	// Included in the payload for explicitness and auditability.
	ValidatorID ValidatorID `json:"validator_id"`

	// OperatorAgentID is the hex-encoded Ed25519 public key operating this seat.
	OperatorAgentID crypto.AgentID `json:"operator_agent_id"`

	// ConsensusPublicKey is the Ed25519 key used for consensus participation.
	ConsensusPublicKey crypto.AgentID `json:"consensus_public_key"`

	// KeyEpoch is the initial key epoch number (must be 1 for new joins).
	KeyEpoch uint64 `json:"key_epoch"`

	// BondedStake is the initial stake in µAET.
	BondedStake uint64 `json:"bonded_stake"`

	// EffectiveFromVersion is the validator set version that will include
	// this seat after the event is applied.
	EffectiveFromVersion uint64 `json:"effective_from_version"`
}

// Validate checks all required fields.
func (p *ValidatorJoinPayload) Validate() error {
	if p.ValidatorID == "" {
		return ErrPayloadMissingValidatorID
	}
	if p.OperatorAgentID == "" {
		return ErrPayloadMissingOperatorAgentID
	}
	if p.ConsensusPublicKey == "" {
		return ErrPayloadMissingConsensusKey
	}
	if p.KeyEpoch == 0 {
		return ErrPayloadMissingKeyEpoch
	}
	if p.BondedStake == 0 {
		return ErrPayloadMissingBondedStake
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// Activate Payload
// ---------------------------------------------------------------------------

// ValidatorActivatePayload promotes a seat from Probationary to Active.
type ValidatorActivatePayload struct {
	ValidatorID          ValidatorID `json:"validator_id"`
	EffectiveFromVersion uint64      `json:"effective_from_version"`
}

// Validate checks required fields.
func (p *ValidatorActivatePayload) Validate() error {
	if p.ValidatorID == "" {
		return ErrPayloadMissingValidatorID
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// Suspend Payload
// ---------------------------------------------------------------------------

// ValidatorSuspendPayload temporarily removes a seat from consensus.
type ValidatorSuspendPayload struct {
	ValidatorID          ValidatorID `json:"validator_id"`
	Reason               string      `json:"reason"`
	EvidenceRef          string      `json:"evidence_ref,omitempty"`
	EffectiveFromVersion uint64      `json:"effective_from_version"`
}

// Validate checks required fields.
func (p *ValidatorSuspendPayload) Validate() error {
	if p.ValidatorID == "" {
		return ErrPayloadMissingValidatorID
	}
	if p.Reason == "" {
		return ErrPayloadMissingReason
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// Resume Payload
// ---------------------------------------------------------------------------

// ValidatorResumePayload reinstates a suspended seat to Active status.
type ValidatorResumePayload struct {
	ValidatorID          ValidatorID `json:"validator_id"`
	EffectiveFromVersion uint64      `json:"effective_from_version"`
}

// Validate checks required fields.
func (p *ValidatorResumePayload) Validate() error {
	if p.ValidatorID == "" {
		return ErrPayloadMissingValidatorID
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// Exit Payload
// ---------------------------------------------------------------------------

// ExitPhase distinguishes the two stages of voluntary exit.
type ExitPhase string

const (
	// ExitPhaseBeginCooldown initiates the cooldown period.
	ExitPhaseBeginCooldown ExitPhase = "begin_cooldown"
	// ExitPhaseCompleteExit finalizes exit after cooldown.
	ExitPhaseCompleteExit ExitPhase = "complete_exit"
)

// ValidatorExitPayload records a voluntary exit. The Phase field determines
// whether this begins cooldown or completes the exit.
type ValidatorExitPayload struct {
	ValidatorID          ValidatorID `json:"validator_id"`
	Phase                ExitPhase   `json:"phase"`
	CooldownDuration     uint64      `json:"cooldown_duration,omitempty"`
	EffectiveFromVersion uint64      `json:"effective_from_version"`
}

// Validate checks required fields and phase-specific constraints.
func (p *ValidatorExitPayload) Validate() error {
	if p.ValidatorID == "" {
		return ErrPayloadMissingValidatorID
	}
	if p.Phase != ExitPhaseBeginCooldown && p.Phase != ExitPhaseCompleteExit {
		return fmt.Errorf("%w: %q", ErrPayloadInvalidExitPhase, p.Phase)
	}
	if p.Phase == ExitPhaseBeginCooldown && p.CooldownDuration == 0 {
		return ErrPayloadMissingCooldownDuration
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// Key Rotate Payload
// ---------------------------------------------------------------------------

// ValidatorKeyRotatePayload records a key rotation for a seat.
type ValidatorKeyRotatePayload struct {
	ValidatorID          ValidatorID    `json:"validator_id"`
	OldConsensusKey      crypto.AgentID `json:"old_consensus_key"`
	NewConsensusKey      crypto.AgentID `json:"new_consensus_key"`
	OldKeyEpoch          uint64         `json:"old_key_epoch"`
	NewKeyEpoch          uint64         `json:"new_key_epoch"`
	EffectiveFromVersion uint64         `json:"effective_from_version"`
}

// Validate checks required fields and epoch advancement.
func (p *ValidatorKeyRotatePayload) Validate() error {
	if p.ValidatorID == "" {
		return ErrPayloadMissingValidatorID
	}
	if p.OldConsensusKey == "" || p.NewConsensusKey == "" {
		return ErrPayloadMissingConsensusKey
	}
	if p.OldKeyEpoch == 0 || p.NewKeyEpoch == 0 {
		return ErrPayloadMissingKeyEpoch
	}
	if p.NewKeyEpoch <= p.OldKeyEpoch {
		return fmt.Errorf("%w: old=%d new=%d", ErrPayloadKeyEpochNotAdvanced, p.OldKeyEpoch, p.NewKeyEpoch)
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// Slash Applied Payload
// ---------------------------------------------------------------------------

// ValidatorSlashAppliedPayload records a slash applied to a validator seat.
// The resulting status depends on the severity: minor offenses may suspend,
// severe offenses permanently exclude.
type ValidatorSlashAppliedPayload struct {
	ValidatorID          ValidatorID `json:"validator_id"`
	Offense              string      `json:"offense"`
	EvidenceRef          string      `json:"evidence_ref"`
	SlashPercent         float64     `json:"slash_percent"`
	SlashAmount          uint64      `json:"slash_amount"`
	RemainingStake       uint64      `json:"remaining_stake"`
	PermanentExclusion   bool        `json:"permanent_exclusion"`
	CooldownDuration     uint64      `json:"cooldown_duration,omitempty"`
	Reason               string      `json:"reason"`
	EffectiveFromVersion uint64      `json:"effective_from_version"`
}

// Validate checks required fields and numeric constraints.
func (p *ValidatorSlashAppliedPayload) Validate() error {
	if p.ValidatorID == "" {
		return ErrPayloadMissingValidatorID
	}
	if p.Offense == "" {
		return ErrPayloadMissingOffense
	}
	if p.EvidenceRef == "" {
		return ErrPayloadMissingEvidenceRef
	}
	if p.SlashPercent <= 0 || p.SlashPercent > 100 {
		return fmt.Errorf("%w: %.2f", ErrPayloadInvalidSlashPercent, p.SlashPercent)
	}
	if p.Reason == "" {
		return ErrPayloadMissingReason
	}
	if p.EffectiveFromVersion == 0 {
		return ErrPayloadMissingEffectiveVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// DAG Event ↔ LifecycleEvent Conversion
// ---------------------------------------------------------------------------

// ExtractLifecycleEvent converts a standard event.Event into the Reducer's
// LifecycleEvent input. Returns ErrUnsupportedEvent for non-lifecycle events.
// This is the single conversion point between the DAG event model and the
// lifecycle state machine.
func ExtractLifecycleEvent(ev *event.Event) ([]LifecycleEvent, error) {
	switch ev.Type {
	case event.EventTypeValidatorGenesisSet:
		return extractGenesis(ev)
	case event.EventTypeValidatorJoin:
		return extractJoin(ev)
	case event.EventTypeValidatorActivate:
		return extractActivate(ev)
	case event.EventTypeValidatorSuspend:
		return extractSuspend(ev)
	case event.EventTypeValidatorResume:
		return extractResume(ev)
	case event.EventTypeValidatorExit:
		return extractExit(ev)
	case event.EventTypeValidatorKeyRotate:
		return extractKeyRotate(ev)
	case event.EventTypeValidatorSlashApplied:
		return extractSlash(ev)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEvent, ev.Type)
	}
}

func extractGenesis(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorGenesisSetPayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var out []LifecycleEvent
	for _, s := range p.Seats {
		out = append(out, LifecycleEvent{
			Kind:        EventJoin,
			EventID:     string(ev.ID),
			CausalTS:    ev.CausalTimestamp,
			SeatID:      s.ValidatorID,
			OperatorKey: s.ConsensusPublicKey,
			StakeAmount: s.BondedStake,
			IsGenesis:   true,
		})
	}
	return out, nil
}

func extractJoin(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorJoinPayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []LifecycleEvent{{
		Kind:        EventJoin,
		EventID:     string(ev.ID),
		CausalTS:    ev.CausalTimestamp,
		SeatID:      p.ValidatorID,
		OperatorKey: p.ConsensusPublicKey,
		StakeAmount: p.BondedStake,
	}}, nil
}

func extractActivate(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorActivatePayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []LifecycleEvent{{
		Kind:     EventActivate,
		EventID:  string(ev.ID),
		CausalTS: ev.CausalTimestamp,
		SeatID:   p.ValidatorID,
	}}, nil
}

func extractSuspend(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorSuspendPayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []LifecycleEvent{{
		Kind:     EventSuspend,
		EventID:  string(ev.ID),
		CausalTS: ev.CausalTimestamp,
		SeatID:   p.ValidatorID,
		Reason:   p.Reason,
	}}, nil
}

func extractResume(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorResumePayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []LifecycleEvent{{
		Kind:     EventReinstate,
		EventID:  string(ev.ID),
		CausalTS: ev.CausalTimestamp,
		SeatID:   p.ValidatorID,
	}}, nil
}

func extractExit(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorExitPayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var kind LifecycleEventKind
	switch p.Phase {
	case ExitPhaseBeginCooldown:
		kind = EventBeginCooldown
	case ExitPhaseCompleteExit:
		kind = EventExit
	}
	return []LifecycleEvent{{
		Kind:             kind,
		EventID:          string(ev.ID),
		CausalTS:         ev.CausalTimestamp,
		SeatID:           p.ValidatorID,
		CooldownDuration: p.CooldownDuration,
	}}, nil
}

func extractKeyRotate(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorKeyRotatePayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []LifecycleEvent{{
		Kind:           EventRotateKey,
		EventID:        string(ev.ID),
		CausalTS:       ev.CausalTimestamp,
		SeatID:         p.ValidatorID,
		OperatorKey:    p.NewConsensusKey,
		OldOperatorKey: p.OldConsensusKey,
	}}, nil
}

func extractSlash(ev *event.Event) ([]LifecycleEvent, error) {
	p, err := event.GetPayload[ValidatorSlashAppliedPayload](ev)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []LifecycleEvent{{
		Kind:               EventSlash,
		EventID:            string(ev.ID),
		CausalTS:           ev.CausalTimestamp,
		SeatID:             p.ValidatorID,
		Reason:             p.Reason,
		SlashAmount:        p.SlashAmount,
		PermanentExclusion: p.PermanentExclusion,
		CooldownDuration:   p.CooldownDuration,
	}}, nil
}
