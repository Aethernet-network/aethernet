// Package assurance — bootstrap.go
//
// BootstrapOverride enforces elevated replay rates and linearly-decaying
// reward supplements during the network bootstrap phase.
//
// The bootstrap phase is ACTIVE as long as at least one of the following
// conditions is true:
//
//   - fewer than BootstrapMinValidators are active
//   - fewer than BootstrapDurationDays have elapsed since launch
//
// Once BOTH conditions are satisfied simultaneously, the bootstrap phase ends
// and EffectiveReplayRates() returns the caller-supplied normal rates.
//
// Bootstrap rewards decay linearly from BootstrapBaseReward at zero monthly
// volume to zero at BootstrapTargetMonthlyVolume, and are forced to zero once
// BootstrapSunsetMonths months have elapsed since launch regardless of volume.
package assurance

import (
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Rates struct
// ---------------------------------------------------------------------------

// BootstrapRates groups the three replay rate parameters returned by
// EffectiveReplayRates.
type BootstrapRates struct {
	// BaselineReplay is the base fraction of tasks replayed.
	BaselineReplay float64
	// GenerationReplay is the replay rate applied to tasks that carry a
	// generation-ledger credit (higher scrutiny).
	GenerationReplay float64
	// NewAgentReplay is the replay rate applied to tasks claimed by agents
	// with limited track records.
	NewAgentReplay float64
}

// ---------------------------------------------------------------------------
// validatorCounter interface
// ---------------------------------------------------------------------------

// validatorCounter is the minimal interface required by BootstrapOverride to
// query the current active-validator count. *validator.ValidatorRegistry
// satisfies this interface; a stub can be used in tests.
type validatorCounter interface {
	ActiveEligibleCount() int
}

// ---------------------------------------------------------------------------
// BootstrapOverride
// ---------------------------------------------------------------------------

// BootstrapOverride reports whether the network is still in the bootstrap
// phase and provides the effective replay rates and reward amounts for the
// current conditions. It is safe for concurrent use by multiple goroutines
// (the underlying fields are read-only after construction).
type BootstrapOverride struct {
	cfg         *config.AssuranceConfig
	counter     validatorCounter
	launchTime  time.Time
	normalRates BootstrapRates
}

// NewBootstrapOverride creates a BootstrapOverride.
//
// counter is called on every IsBootstrapActive check to obtain the live
// validator count; it must not be nil.
//
// launchTime is the moment the network first started; it is used to measure
// elapsed days and months.
//
// normalRates is the set of replay rates to return from EffectiveReplayRates
// once the bootstrap phase has ended.
func NewBootstrapOverride(
	cfg *config.AssuranceConfig,
	counter validatorCounter,
	launchTime time.Time,
	normalRates BootstrapRates,
) *BootstrapOverride {
	return &BootstrapOverride{
		cfg:         cfg,
		counter:     counter,
		launchTime:  launchTime,
		normalRates: normalRates,
	}
}

// ---------------------------------------------------------------------------
// IsBootstrapActive
// ---------------------------------------------------------------------------

// IsBootstrapActive returns true when the bootstrap phase has not yet ended.
// The phase ends only when BOTH of the following conditions are met:
//
//   - time.Since(launchTime) >= BootstrapDurationDays × 24h
//   - counter.ActiveEligibleCount() >= BootstrapMinValidators
func (b *BootstrapOverride) IsBootstrapActive() bool {
	durationMet := time.Since(b.launchTime) >= time.Duration(b.cfg.BootstrapDurationDays)*24*time.Hour
	validatorsMet := b.counter.ActiveEligibleCount() >= b.cfg.BootstrapMinValidators
	// Both conditions must be satisfied for the phase to end.
	return !(durationMet && validatorsMet)
}

// ---------------------------------------------------------------------------
// EffectiveReplayRates
// ---------------------------------------------------------------------------

// EffectiveReplayRates returns the bootstrap rates when IsBootstrapActive()
// is true, otherwise returns the normal rates supplied at construction.
func (b *BootstrapOverride) EffectiveReplayRates() BootstrapRates {
	if b.IsBootstrapActive() {
		return BootstrapRates{
			BaselineReplay:   b.cfg.BootstrapBaselineReplay,
			GenerationReplay: b.cfg.BootstrapGenerationReplay,
			NewAgentReplay:   b.cfg.BootstrapNewAgentReplay,
		}
	}
	return b.normalRates
}

// ---------------------------------------------------------------------------
// ComputeBootstrapReward
// ---------------------------------------------------------------------------

// ComputeBootstrapReward returns the per-task bootstrap reward (µAET) for the
// given conditions.
//
// The reward decays linearly from BootstrapBaseReward (at zero monthly volume)
// to zero (at BootstrapTargetMonthlyVolume). It is unconditionally zero once
// monthsSinceLaunch >= BootstrapSunsetMonths.
//
// monthsSinceLaunch is the number of complete calendar months since launch
// (the caller is responsible for computing this from launchTime).
// monthlyVolume is the total assured-task volume (µAET) in the current month.
func (b *BootstrapOverride) ComputeBootstrapReward(monthsSinceLaunch int, monthlyVolume uint64) uint64 {
	if monthsSinceLaunch >= b.cfg.BootstrapSunsetMonths {
		return 0
	}
	target := b.cfg.BootstrapTargetMonthlyVolume
	if target == 0 || monthlyVolume >= target {
		return 0
	}
	// Linear decay: reward = BaseReward × (1 − volume/target)
	fraction := 1.0 - float64(monthlyVolume)/float64(target)
	reward := uint64(float64(b.cfg.BootstrapBaseReward) * fraction)
	return reward
}
