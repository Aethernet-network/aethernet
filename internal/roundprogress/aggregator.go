package roundprogress

import (
	"fmt"
	"log/slog"
	"sync"
)

// ProgressAggregator reconciles incoming ProgressUpdate messages against
// the snapshot store, enforcing all lease and validation rules.
type ProgressAggregator struct {
	store        SnapshotStore
	rateLimiter  *RateLimiter
	anomalyCount map[string]uint32 // keyed by validatorID
	mu           sync.Mutex
}

// NewProgressAggregator creates an aggregator with the given store and rate limiter.
func NewProgressAggregator(store SnapshotStore, rateLimiter *RateLimiter) *ProgressAggregator {
	return &ProgressAggregator{
		store:        store,
		rateLimiter:  rateLimiter,
		anomalyCount: make(map[string]uint32),
	}
}

// Apply validates an incoming ProgressUpdate against the current snapshot,
// enforces monotonic advancement, lease rules, rate limiting, and ETA clamping.
// Returns nil if the update was applied, error if rejected.
// nowUnix is the caller-provided wall clock (no internal time.Now calls).
func (a *ProgressAggregator) Apply(update *ProgressUpdate, nowUnix int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. Rate-limit check.
	if !a.rateLimiter.Allow(update.RoundID, update.ValidatorID, update.AnalyzerFamily, nowUnix) {
		a.incrementAnomaly(update.ValidatorID)
		return fmt.Errorf("rate limited: validator %s, round %s, family %s",
			update.ValidatorID, update.RoundID, update.AnalyzerFamily)
	}

	// 2. Get current snapshot.
	current, err := a.store.Get(update.RoundID, update.ValidatorID, update.AnalyzerFamily)
	if err != nil {
		return fmt.Errorf("store get: %w", err)
	}

	// 3. First update — create initial snapshot.
	if current == nil {
		clampedETA := ClampETA(update.Phase, update.EstimatedReadyUnix, nowUnix)
		leaseExpiry := ComputeLeaseExpiry(ProgressPhaseAcknowledged, 0, 0, nowUnix)
		if update.Phase != ProgressPhaseAcknowledged {
			// First update skips Acknowledged — compute lease for actual phase.
			leaseExpiry = ComputeLeaseExpiry(update.Phase, 0, clampedETA, nowUnix)
		}

		snap := &RoundProgressSnapshot{
			RoundID:            update.RoundID,
			ValidatorID:        update.ValidatorID,
			AnalyzerFamily:     update.AnalyzerFamily,
			CurrentPhase:       update.Phase,
			PhaseEnteredUnix:   nowUnix,
			LastUpdateUnix:     nowUnix,
			LeaseExpiryUnix:    leaseExpiry,
			ProgressGeneration: update.ProgressGeneration,
			EstimatedReadyUnix: clampedETA,
			ReasonCode:         update.ReasonCode,
			DiagnosticText:     update.DiagnosticText,
		}
		if err := a.store.Put(snap); err != nil {
			return fmt.Errorf("store put (new): %w", err)
		}
		slog.Debug("roundprogress: snapshot created",
			"round", update.RoundID,
			"validator", update.ValidatorID,
			"family", update.AnalyzerFamily,
			"phase", update.Phase)
		return nil
	}

	// 4. Same phase, same generation — update metadata only (no lease extension).
	if update.Phase == current.CurrentPhase && update.ProgressGeneration == current.ProgressGeneration {
		current.LastUpdateUnix = nowUnix
		current.ReasonCode = update.ReasonCode
		current.DiagnosticText = update.DiagnosticText
		if err := a.store.Put(current); err != nil {
			return fmt.Errorf("store put (heartbeat): %w", err)
		}
		return nil
	}

	// 5. Validate lease extension (phase transition or generation bump).
	if err := ValidateLeaseExtension(current, update); err != nil {
		a.incrementAnomaly(update.ValidatorID)
		return fmt.Errorf("lease validation failed: %w", err)
	}

	// 6. Clamp ETA.
	clampedETA := ClampETA(update.Phase, update.EstimatedReadyUnix, nowUnix)

	// 7. Compute new lease expiry.
	leaseExpiry := ComputeLeaseExpiry(update.Phase, current.LeaseExpiryUnix, clampedETA, nowUnix)

	// 8. Determine if phase changed.
	phaseEnteredUnix := current.PhaseEnteredUnix
	if update.Phase != current.CurrentPhase {
		phaseEnteredUnix = nowUnix
	}

	// 9. Update snapshot.
	current.CurrentPhase = update.Phase
	current.PhaseEnteredUnix = phaseEnteredUnix
	current.LastUpdateUnix = nowUnix
	current.LeaseExpiryUnix = leaseExpiry
	current.ProgressGeneration = update.ProgressGeneration
	current.EstimatedReadyUnix = clampedETA
	current.ReasonCode = update.ReasonCode
	current.DiagnosticText = update.DiagnosticText

	if err := a.store.Put(current); err != nil {
		return fmt.Errorf("store put (update): %w", err)
	}

	slog.Debug("roundprogress: snapshot updated",
		"round", update.RoundID,
		"validator", update.ValidatorID,
		"family", update.AnalyzerFamily,
		"phase", update.Phase,
		"generation", update.ProgressGeneration)
	return nil
}

// AnomalyCount returns the number of invalid update attempts for a validator.
func (a *ProgressAggregator) AnomalyCount(validatorID string) uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.anomalyCount[validatorID]
}

func (a *ProgressAggregator) incrementAnomaly(validatorID string) {
	a.anomalyCount[validatorID]++
}
