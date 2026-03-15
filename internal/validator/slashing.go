// Package validator — slashing.go
//
// SlashEngine applies economic penalties to validators whose behaviour violates
// protocol rules. Three offense tiers are supported:
//
//   - FraudulentApproval  30 % stake / 30-day cooldown
//   - DishonestReplay     40 % stake / 60-day cooldown
//   - Collusion           75 % stake / 180-day cooldown
//     (+ permanent exclusion on a repeat collusion when CollusionRepeatExclusion=true)
//
// Slashed stake is split equally between a challenger bounty pool and the
// protocol dispute reserve. The SlashEngine does not transfer funds itself;
// it returns a SlashResult that the caller uses to drive ledger operations.
//
// Resume lifecycle: after the cooldown expires, SlashEngine.Resume() lifts
// the suspension and returns the validator to StatusActive. A permanently
// excluded validator cannot resume.
package validator

import (
	"errors"
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Offense types
// ---------------------------------------------------------------------------

// SlashOffense identifies the type of protocol violation that triggered a slash.
type SlashOffense string

const (
	// OffenseFraudulentApproval is recorded when a validator approved a task
	// result that was later proven fraudulent.
	OffenseFraudulentApproval SlashOffense = "fraudulent_approval"
	// OffenseDishonestReplay is recorded when a validator submitted a replay
	// report that was inconsistent with independently verified results.
	OffenseDishonestReplay SlashOffense = "dishonest_replay"
	// OffenseCollusion is recorded when coordinated misbehaviour is detected
	// between affiliated validators.
	OffenseCollusion SlashOffense = "collusion"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrSlashValidatorNotFound is returned by Slash when the target
	// validator ID does not exist in the registry.
	ErrSlashValidatorNotFound = errors.New("slash: validator not found")
	// ErrSlashInCooldown is returned by Resume and ResumeFromSlash when the
	// validator's suspension period has not yet elapsed.
	ErrSlashInCooldown = errors.New("slash: validator is in cooldown period")
	// ErrSlashPermanentlyExcluded is returned when an operation targets a
	// validator that has been permanently excluded from the protocol.
	ErrSlashPermanentlyExcluded = errors.New("slash: validator is permanently excluded")
	// ErrSlashUnknownOffense is returned when an unrecognised SlashOffense
	// string is passed to Slash.
	ErrSlashUnknownOffense = errors.New("slash: unknown offense type")
)

// ---------------------------------------------------------------------------
// SlashResult
// ---------------------------------------------------------------------------

// SlashResult is the outcome of a successful Slash call. The caller must use
// these values to drive the corresponding ledger transfers; the SlashEngine
// itself does not touch the ledger.
type SlashResult struct {
	// ValidatorID is the validator that was slashed.
	ValidatorID string
	// Offense is the protocol violation that triggered the slash.
	Offense SlashOffense
	// SlashPercentage is the fraction of stake that was burned (e.g. 0.30).
	SlashPercentage float64
	// SlashAmount is the total µAET deducted from the validator's stake.
	SlashAmount uint64
	// RemainingStake is the validator's stake after deduction.
	RemainingStake uint64
	// CooldownDays is the length of the suspension period in days.
	// Zero when PermanentExclusion is true.
	CooldownDays int
	// CooldownUntil is the wall-clock time when the validator may resume.
	// Zero when PermanentExclusion is true.
	CooldownUntil time.Time
	// ChallengerShare is the µAET portion awarded to the successful challenger.
	// This is 50 % of SlashAmount (integer split, remainder to ReserveShare).
	ChallengerShare uint64
	// ReserveShare is the µAET portion held in the protocol dispute reserve.
	ReserveShare uint64
	// PermanentExclusion is true when this slash triggered an irreversible
	// exclusion (repeat collusion with CollusionRepeatExclusion enabled).
	PermanentExclusion bool
}

// ---------------------------------------------------------------------------
// SlashEngine
// ---------------------------------------------------------------------------

// SlashEngine applies slash penalties to validators using the parameters in
// ValidatorConfig. It delegates all state mutations to ValidatorRegistry so
// that registry invariants (locking, persistence) are maintained.
//
// It is safe for concurrent use by multiple goroutines.
type SlashEngine struct {
	registry *ValidatorRegistry
	cfg      *config.ValidatorConfig
}

// NewSlashEngine creates a SlashEngine backed by registry and cfg.
func NewSlashEngine(registry *ValidatorRegistry, cfg *config.ValidatorConfig) *SlashEngine {
	return &SlashEngine{registry: registry, cfg: cfg}
}

// ---------------------------------------------------------------------------
// Slash
// ---------------------------------------------------------------------------

// offenseParams returns the slash fraction and cooldown duration for the
// given offense. Returns ErrSlashUnknownOffense for unrecognised offenses.
func (e *SlashEngine) offenseParams(offense SlashOffense) (pct float64, days int, err error) {
	switch offense {
	case OffenseFraudulentApproval:
		return e.cfg.SlashFraudulentApproval, e.cfg.CooldownTier1Days, nil
	case OffenseDishonestReplay:
		return e.cfg.SlashDishonestReplay, e.cfg.CooldownTier2Days, nil
	case OffenseCollusion:
		return e.cfg.SlashCollusion, e.cfg.CooldownTier3Days, nil
	default:
		return 0, 0, fmt.Errorf("%w: %q", ErrSlashUnknownOffense, offense)
	}
}

// Slash applies a slash penalty to the given validator for the specified
// offense. The returned SlashResult describes the economic outcome. The caller
// is responsible for executing the corresponding ledger transfers.
//
// Errors:
//   - ErrSlashUnknownOffense  — offense is not a recognised SlashOffense
//   - ErrSlashValidatorNotFound — validatorID not in registry
//   - ErrSlashPermanentlyExcluded — validator is already permanently excluded
func (e *SlashEngine) Slash(validatorID string, offense SlashOffense) (*SlashResult, error) {
	pct, days, err := e.offenseParams(offense)
	if err != nil {
		return nil, err
	}

	// Read current state to check for repeat-collusion condition before
	// mutating. The subsequent ApplySlash call is atomic under the registry
	// write lock, so this snapshot may be momentarily stale only in
	// concurrent slash scenarios — an acceptable race for an operator-driven
	// action.
	v, err := e.registry.Get(validatorID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSlashValidatorNotFound, validatorID)
	}
	if v.Status == StatusExcluded {
		return nil, fmt.Errorf("%w: %s", ErrSlashPermanentlyExcluded, validatorID)
	}

	// Determine whether this slash triggers permanent exclusion.
	// Condition: offense is collusion AND repeat-exclusion is enabled AND the
	// validator has at least one prior slash (SlashCount ≥ 1 before this one).
	permanent := offense == OffenseCollusion &&
		e.cfg.CollusionRepeatExclusion &&
		v.SlashCount >= 1

	// Compute slash and distribution amounts.
	slashAmount := uint64(pct * float64(v.StakeAmount))
	remaining := uint64(0)
	if v.StakeAmount > slashAmount {
		remaining = v.StakeAmount - slashAmount
	}
	challengerShare := slashAmount / 2
	reserveShare := slashAmount - challengerShare

	cooldownUntil := time.Time{}
	cooldownDays := 0
	if !permanent {
		cooldownDays = days
		cooldownUntil = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	}

	// Delegate state mutation to the registry.
	if _, err := e.registry.ApplySlash(validatorID, slashAmount, string(offense), cooldownUntil, permanent); err != nil {
		return nil, fmt.Errorf("slash: apply failed for %s: %w", validatorID, err)
	}

	return &SlashResult{
		ValidatorID:        validatorID,
		Offense:            offense,
		SlashPercentage:    pct,
		SlashAmount:        slashAmount,
		RemainingStake:     remaining,
		CooldownDays:       cooldownDays,
		CooldownUntil:      cooldownUntil,
		ChallengerShare:    challengerShare,
		ReserveShare:       reserveShare,
		PermanentExclusion: permanent,
	}, nil
}

// ---------------------------------------------------------------------------
// CanResume / Resume
// ---------------------------------------------------------------------------

// CanResume reports whether the validator's slash cooldown has expired and the
// validator may be reinstated. Returns (false, time.Time{}) when the validator
// is permanently excluded, not found, or not currently in a slash suspension.
func (e *SlashEngine) CanResume(validatorID string) (bool, time.Time) {
	v, err := e.registry.Get(validatorID)
	if err != nil {
		return false, time.Time{}
	}
	if v.Status == StatusExcluded {
		return false, time.Time{}
	}
	if v.Status != StatusSuspended {
		return false, time.Time{}
	}
	if v.SuspendedUntil.IsZero() || !time.Now().Before(v.SuspendedUntil) {
		return true, time.Time{}
	}
	return false, v.SuspendedUntil
}

// Resume lifts the slash suspension and restores the validator to StatusActive.
//
// Errors:
//   - ErrSlashInCooldown          — cooldown has not yet expired
//   - ErrSlashPermanentlyExcluded — validator is permanently excluded
//   - ErrRegistryValidatorNotFound — validator ID not found
func (e *SlashEngine) Resume(validatorID string) error {
	return e.registry.ResumeFromSlash(validatorID)
}
