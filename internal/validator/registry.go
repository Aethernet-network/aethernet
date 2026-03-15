package validator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Status constants
// ---------------------------------------------------------------------------

// ValidatorStatus describes the lifecycle state of a registered validator.
type ValidatorStatus string

const (
	// StatusProbationary is the initial state for all new validators.
	// A probationary validator participates in verification at reduced weight
	// while accumulating the task / accuracy record required for promotion.
	StatusProbationary ValidatorStatus = "probationary"
	// StatusActive means the validator has completed probation and is fully
	// trusted for verification routing.
	StatusActive ValidatorStatus = "active"
	// StatusSuspended means the validator has been temporarily removed from
	// the routing pool (stake below minimum, explicit suspension, etc.).
	StatusSuspended ValidatorStatus = "suspended"
	// StatusExcluded means the validator has been permanently excluded after
	// exhausting its maximum allowed probation cycles.
	StatusExcluded ValidatorStatus = "excluded"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrRegistryValidatorNotFound is returned when an operation targets a
	// validator ID that does not exist in the registry.
	ErrRegistryValidatorNotFound = errors.New("validator: registry entry not found")
	// ErrInsufficientStake is returned by Register when the provided stake is
	// below the computed requirement.
	ErrInsufficientStake = errors.New("validator: insufficient_stake")
	// ErrProbationInProgress is returned by EvaluateProbation when the
	// probation duration has not yet elapsed.
	ErrProbationInProgress = errors.New("validator: probation_in_progress")
	// ErrMaxProbationCyclesExceeded is returned by EvaluateProbation when a
	// validator has failed all allowed probation cycles.
	ErrMaxProbationCyclesExceeded = errors.New("validator: max_probation_cycles_exceeded")
)

// ---------------------------------------------------------------------------
// Validator record
// ---------------------------------------------------------------------------

// Validator is the persisted registry record for a single verification node.
// It tracks stake, status, probation progress, and lifecycle timestamps.
type Validator struct {
	ID                 string          `json:"id"`
	AgentID            string          `json:"agent_id"`
	StakeAmount        uint64          `json:"stake_amount"`
	RequiredStake      uint64          `json:"required_stake"`
	Status             ValidatorStatus `json:"status"`
	Categories         []string        `json:"categories"`
	ProbationStartedAt time.Time       `json:"probation_started_at,omitempty"`
	ProbationCycle     int             `json:"probation_cycle"`
	ProbationTaskCount int             `json:"probation_task_count"`
	ProbationAccuracy  float64         `json:"probation_accuracy"`
	ActivatedAt        time.Time       `json:"activated_at,omitempty"`
	SuspendedAt        time.Time       `json:"suspended_at,omitempty"`
	SuspendedUntil     time.Time       `json:"suspended_until,omitempty"`
	SuspensionReason   string          `json:"suspension_reason,omitempty"`
	RegisteredAt       time.Time       `json:"registered_at"`
	LastStakeCheck     time.Time       `json:"last_stake_check"`
	IsGenesis          bool            `json:"is_genesis"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// validatorStore is the persistence back-end required by ValidatorRegistry.
// *store.Store from the store package satisfies this interface.
type validatorStore interface {
	PutValidator(id string, data []byte) error
	GetValidator(id string) ([]byte, error)
	AllValidators() (map[string][]byte, error)
	DeleteValidator(id string) error
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// ValidatorRegistry manages the full lifecycle of registered validators:
// permissionless entry, probation, stake checks, and query operations.
// It is safe for concurrent use by multiple goroutines.
type ValidatorRegistry struct {
	mu         sync.RWMutex
	validators map[string]*Validator // keyed by Validator.ID
	byAgent    map[string]string     // agentID → validatorID (reverse index)
	store      validatorStore
	cfg        *config.ValidatorConfig

	// trailing stats used by CheckStakeRequirements
	trailing30dVolume   uint64
	maxRecentTaskSize   uint64
}

// NewValidatorRegistry creates an empty ValidatorRegistry backed by cfg and
// the optional store. store may be nil (in-memory only; data not persisted).
func NewValidatorRegistry(cfg *config.ValidatorConfig, s validatorStore) *ValidatorRegistry {
	return &ValidatorRegistry{
		validators: make(map[string]*Validator),
		byAgent:    make(map[string]string),
		store:      s,
		cfg:        cfg,
	}
}

// LoadFromStore creates a ValidatorRegistry and restores all previously
// persisted validators from s. Returns an error if any stored blob cannot
// be parsed. The store is retained for subsequent write-through operations.
func LoadFromStore(cfg *config.ValidatorConfig, s validatorStore) (*ValidatorRegistry, error) {
	reg := NewValidatorRegistry(cfg, s)
	if s == nil {
		return reg, nil
	}
	blobs, err := s.AllValidators()
	if err != nil {
		return nil, fmt.Errorf("validator: load from store: %w", err)
	}
	for id, blob := range blobs {
		var v Validator
		if err := json.Unmarshal(blob, &v); err != nil {
			return nil, fmt.Errorf("validator: unmarshal %s: %w", id, err)
		}
		reg.validators[v.ID] = &v
		reg.byAgent[v.AgentID] = v.ID
	}
	return reg, nil
}

// SetTrailingStats updates the rolling-window metrics used by
// CheckStakeRequirements to compute the dynamic stake requirement.
// Call periodically (e.g. every StakeRecheckPeriod days) with fresh data.
func (r *ValidatorRegistry) SetTrailingStats(trailing30dVolume uint64, maxRecentTaskSize uint64) {
	r.mu.Lock()
	r.trailing30dVolume = trailing30dVolume
	r.maxRecentTaskSize = maxRecentTaskSize
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// Register adds a new validator to the registry. It generates a unique ID
// from agentID + timestamp, sets the initial status to StatusProbationary
// (or StatusActive for genesis validators when GenesisSkipProbation is true),
// and persists the record.
//
// Returns ErrInsufficientStake if stakeAmount < ComputeRequiredStake with
// zero trailing volume (i.e. at least StakeBaseMinimum must be provided).
func (r *ValidatorRegistry) Register(agentID string, stakeAmount uint64, categories []string, isGenesis bool) (*Validator, error) {
	// For initial registration there is no trailing volume data yet; use base
	// minimum as the requirement floor.
	required := r.ComputeRequiredStake(0, 0, 0)
	if stakeAmount < required {
		return nil, fmt.Errorf("%w: have %d µAET, need %d µAET", ErrInsufficientStake, stakeAmount, required)
	}

	now := time.Now()
	id := generateValidatorID(agentID, now)

	status := StatusProbationary
	var probationStart time.Time
	if isGenesis && r.cfg.GenesisSkipProbation {
		status = StatusActive
	} else {
		probationStart = now
	}

	cats := make([]string, len(categories))
	copy(cats, categories)

	v := &Validator{
		ID:                 id,
		AgentID:            agentID,
		StakeAmount:        stakeAmount,
		RequiredStake:      required,
		Status:             status,
		Categories:         cats,
		ProbationStartedAt: probationStart,
		ProbationCycle:     1,
		RegisteredAt:       now,
		LastStakeCheck:     now,
		IsGenesis:          isGenesis,
	}

	r.mu.Lock()
	r.validators[id] = v
	r.byAgent[agentID] = id
	r.mu.Unlock()

	if err := r.persist(v); err != nil {
		slog.Error("validator: failed to persist new validator", "id", id, "agent_id", agentID, "err", err)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Dynamic stake
// ---------------------------------------------------------------------------

// ComputeRequiredStake returns the dynamic stake requirement (µAET) given
// trailing assurance volume, the number of currently active validators, and
// the largest recent assured task size.
//
//   volumeComponent    = StakeVolumeMultiple × trailing30dAssuredVolume / max(activeValidatorCount, 1)
//   taskSizeComponent  = StakeTaskSizeMultiple × maxRecentAssuredTask
//   result             = max(StakeBaseMinimum, volumeComponent, taskSizeComponent)
func (r *ValidatorRegistry) ComputeRequiredStake(trailing30dAssuredVolume uint64, activeValidatorCount int, maxRecentAssuredTask uint64) uint64 {
	divisor := activeValidatorCount
	if divisor < 1 {
		divisor = 1
	}
	volumeComponent := uint64(r.cfg.StakeVolumeMultiple * float64(trailing30dAssuredVolume) / float64(divisor))
	taskSizeComponent := uint64(r.cfg.StakeTaskSizeMultiple * float64(maxRecentAssuredTask))

	result := r.cfg.StakeBaseMinimum
	if volumeComponent > result {
		result = volumeComponent
	}
	if taskSizeComponent > result {
		result = taskSizeComponent
	}
	return result
}

// CheckStakeRequirements iterates all active and probationary validators,
// recomputes each one's required stake using the stored trailing stats, and
// suspends any validator whose stake has fallen below the requirement for
// longer than the configured grace period.
func (r *ValidatorRegistry) CheckStakeRequirements() {
	r.mu.RLock()
	volume := r.trailing30dVolume
	maxTask := r.maxRecentTaskSize
	// Count active validators (Active + Probationary).
	activeCnt := 0
	for _, v := range r.validators {
		if v.Status == StatusActive || v.Status == StatusProbationary {
			activeCnt++
		}
	}
	r.mu.RUnlock()

	required := r.ComputeRequiredStake(volume, activeCnt, maxTask)
	now := time.Now()
	graceDur := time.Duration(r.cfg.StakeGracePeriod) * 24 * time.Hour

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, v := range r.validators {
		if v.Status != StatusActive && v.Status != StatusProbationary {
			continue
		}
		v.RequiredStake = required
		if v.StakeAmount >= required {
			v.LastStakeCheck = now
			if err := r.persistLocked(v); err != nil {
				slog.Error("validator: failed to persist stake check", "id", v.ID, "err", err)
			}
			continue
		}
		// Stake is below requirement.
		if now.Sub(v.LastStakeCheck) <= graceDur {
			slog.Warn("validator: stake below minimum — within grace period",
				"id", v.ID, "agent_id", v.AgentID,
				"stake", v.StakeAmount, "required", required)
			continue
		}
		// Grace period expired — suspend.
		v.Status = StatusSuspended
		v.SuspendedAt = now
		v.SuspensionReason = "stake_below_minimum"
		if err := r.persistLocked(v); err != nil {
			slog.Error("validator: failed to persist suspension", "id", v.ID, "err", err)
		}
		slog.Warn("validator: suspended due to insufficient stake past grace period",
			"id", v.ID, "agent_id", v.AgentID,
			"stake", v.StakeAmount, "required", required)
	}
}

// ---------------------------------------------------------------------------
// Probation
// ---------------------------------------------------------------------------

// EvaluateProbation evaluates whether a probationary validator has met the
// requirements to be promoted to StatusActive. If the probation duration has
// not elapsed it returns ErrProbationInProgress. If requirements are met the
// validator is promoted; otherwise the cycle is incremented or the validator
// is excluded if max cycles are reached.
func (r *ValidatorRegistry) EvaluateProbation(validatorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	v, ok := r.validators[validatorID]
	if !ok {
		return ErrRegistryValidatorNotFound
	}
	if v.Status != StatusProbationary {
		return fmt.Errorf("validator: %s is not in probationary status (current: %s)", validatorID, v.Status)
	}

	now := time.Now()
	probationDur := time.Duration(r.cfg.ProbationDuration) * 24 * time.Hour
	if now.Sub(v.ProbationStartedAt) < probationDur {
		return ErrProbationInProgress
	}

	if v.ProbationTaskCount >= r.cfg.ProbationMinTasks && v.ProbationAccuracy >= r.cfg.ProbationMinAccuracy {
		// Requirements met — promote.
		v.Status = StatusActive
		v.ActivatedAt = now
		if err := r.persistLocked(v); err != nil {
			slog.Error("validator: failed to persist promotion", "id", v.ID, "err", err)
		}
		slog.Info("validator: promoted to active",
			"id", v.ID, "agent_id", v.AgentID,
			"tasks", v.ProbationTaskCount, "accuracy", v.ProbationAccuracy)
		return nil
	}

	// Requirements not met — check cycle count.
	v.ProbationCycle++
	if v.ProbationCycle > r.cfg.ProbationMaxCycles {
		v.Status = StatusExcluded
		if err := r.persistLocked(v); err != nil {
			slog.Error("validator: failed to persist exclusion", "id", v.ID, "err", err)
		}
		slog.Warn("validator: excluded after max probation cycles",
			"id", v.ID, "agent_id", v.AgentID, "cycles", v.ProbationCycle-1)
		return ErrMaxProbationCyclesExceeded
	}

	// Reset for next cycle.
	v.ProbationStartedAt = now
	v.ProbationTaskCount = 0
	v.ProbationAccuracy = 0
	if err := r.persistLocked(v); err != nil {
		slog.Error("validator: failed to persist probation reset", "id", v.ID, "err", err)
	}
	slog.Info("validator: probation cycle reset",
		"id", v.ID, "agent_id", v.AgentID, "new_cycle", v.ProbationCycle)
	return nil
}

// RecordProbationTask records one completed task for a probationary validator
// and updates ProbationAccuracy as a running mean.
// passValue is 1.0 if the task passed verification, 0.0 otherwise.
func (r *ValidatorRegistry) RecordProbationTask(validatorID string, passed bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	v, ok := r.validators[validatorID]
	if !ok {
		return ErrRegistryValidatorNotFound
	}
	if v.Status != StatusProbationary {
		return fmt.Errorf("validator: %s is not in probationary status", validatorID)
	}

	var passValue float64
	if passed {
		passValue = 1.0
	}
	count := v.ProbationTaskCount + 1
	// Running average: newAccuracy = (oldAccuracy × (count-1) + passValue) / count
	v.ProbationAccuracy = (v.ProbationAccuracy*float64(v.ProbationTaskCount) + passValue) / float64(count)
	v.ProbationTaskCount = count

	if err := r.persistLocked(v); err != nil {
		slog.Error("validator: failed to persist probation task", "id", v.ID, "err", err)
		return err
	}
	return nil
}

// ResetProbationOnSlash resets the probation state of a probationary validator
// after a slashing event. The cycle counter is incremented and the probation
// period restarts from now.
func (r *ValidatorRegistry) ResetProbationOnSlash(validatorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	v, ok := r.validators[validatorID]
	if !ok {
		return ErrRegistryValidatorNotFound
	}
	if v.Status != StatusProbationary {
		return nil // no-op for non-probationary
	}

	now := time.Now()
	v.ProbationStartedAt = now
	v.ProbationTaskCount = 0
	v.ProbationAccuracy = 0
	v.ProbationCycle++

	if err := r.persistLocked(v); err != nil {
		slog.Error("validator: failed to persist slash reset", "id", v.ID, "err", err)
		return err
	}
	slog.Info("validator: probation reset after slash",
		"id", v.ID, "agent_id", v.AgentID, "new_cycle", v.ProbationCycle)
	return nil
}

// ---------------------------------------------------------------------------
// Query methods
// ---------------------------------------------------------------------------

// ActiveValidators returns all validators with StatusActive or
// StatusProbationary, sorted by ID for determinism.
func (r *ValidatorRegistry) ActiveValidators() []*Validator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Validator
	for _, v := range r.validators {
		if v.Status == StatusActive || v.Status == StatusProbationary {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ActiveValidatorsForCategory returns active and probationary validators that
// list category in their Categories slice.
func (r *ValidatorRegistry) ActiveValidatorsForCategory(category string) []*Validator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Validator
	for _, v := range r.validators {
		if v.Status != StatusActive && v.Status != StatusProbationary {
			continue
		}
		for _, c := range v.Categories {
			if c == category {
				cp := *v
				out = append(out, &cp)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ActiveEligibleCount returns the count of validators with StatusActive or
// StatusProbationary.
func (r *ValidatorRegistry) ActiveEligibleCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, v := range r.validators {
		if v.Status == StatusActive || v.Status == StatusProbationary {
			count++
		}
	}
	return count
}

// Get returns the validator record for the given validator ID. Returns
// ErrRegistryValidatorNotFound if not present.
func (r *ValidatorRegistry) Get(validatorID string) (*Validator, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.validators[validatorID]
	if !ok {
		return nil, ErrRegistryValidatorNotFound
	}
	cp := *v
	return &cp, nil
}

// GetByAgentID returns the validator record associated with agentID.
// Returns ErrRegistryValidatorNotFound when no registration exists.
func (r *ValidatorRegistry) GetByAgentID(agentID string) (*Validator, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byAgent[agentID]
	if !ok {
		return nil, ErrRegistryValidatorNotFound
	}
	v, ok := r.validators[id]
	if !ok {
		return nil, ErrRegistryValidatorNotFound
	}
	cp := *v
	return &cp, nil
}

// IsEligible returns true when the validator:
//   - exists and has StatusActive or StatusProbationary
//   - has not been suspended (SuspendedUntil.IsZero() or past)
//   - lists category in its Categories slice (or category is "")
//   - has StakeAmount >= RequiredStake
func (r *ValidatorRegistry) IsEligible(validatorID string, category string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.validators[validatorID]
	if !ok {
		return false
	}
	if v.Status != StatusActive && v.Status != StatusProbationary {
		return false
	}
	if !v.SuspendedUntil.IsZero() && time.Now().Before(v.SuspendedUntil) {
		return false
	}
	if v.StakeAmount < v.RequiredStake {
		return false
	}
	if category == "" {
		return true
	}
	for _, c := range v.Categories {
		if c == category {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// persist writes v to the backing store. Must NOT be called while r.mu is held
// (acquires no lock itself; caller must ensure v is not being mutated).
func (r *ValidatorRegistry) persist(v *Validator) error {
	if r.store == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("validator: marshal %s: %w", v.ID, err)
	}
	return r.store.PutValidator(v.ID, data)
}

// persistLocked writes v while r.mu is already held (write-lock assumed).
func (r *ValidatorRegistry) persistLocked(v *Validator) error {
	return r.persist(v)
}

// generateValidatorID produces a stable unique ID for a new validator record.
// Uses sha256(agentID + "|" + nanosecond timestamp) to prevent collisions.
func generateValidatorID(agentID string, t time.Time) string {
	raw := agentID + "|" + strconv.FormatInt(t.UnixNano(), 10)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16]) // 32-char hex from first 16 bytes
}
