// Package validator provides verification infrastructure for AetherNet nodes.
//
// This file (validator.go) defines the mainnet Validator — a protocol participant
// that earns fees by performing genuine verification work on OCS events. On
// testnet the AutoValidator (auto.go) fills this role automatically; on mainnet
// real verification nodes register here and are assigned work via AssignVerification.
//
// This is SCAFFOLDING for the mainnet validator protocol. The interfaces are
// stable but the full P2P assignment and slashing mechanics are implemented in
// subsequent milestones.
package validator

import (
	"errors"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ocs"
)

// ErrValidatorNotFound is returned when an operation targets an unregistered validator.
var ErrValidatorNotFound = errors.New("validator: not registered")

// ErrAlreadyAssigned is returned when an event already has an assigned validator.
var ErrAlreadyAssigned = errors.New("validator: event already assigned")

// AssignmentConfig holds tunable parameters for the mainnet assignment
// coordinator. Renamed from ValidatorConfig to free that name for the
// persisted validator-registry config in internal/config.
type AssignmentConfig struct {
	// MaxConcurrentAssignments is the maximum number of unresolved verifications
	// a single validator can hold simultaneously. Prevents monopolisation.
	MaxConcurrentAssignments int

	// AssignmentTimeout is how long a validator has to submit its verdict before
	// the assignment is considered failed and reassigned.
	AssignmentTimeout time.Duration
}

// DefaultAssignmentConfig returns conservative defaults for production use.
func DefaultAssignmentConfig() *AssignmentConfig {
	return &AssignmentConfig{
		MaxConcurrentAssignments: 50,
		AssignmentTimeout:        5 * time.Minute,
	}
}

// ValidatorInfo describes a registered mainnet verification node.
type ValidatorInfo struct {
	ID           crypto.AgentID
	RegisteredAt time.Time
	Active       bool
	Assignments  int // current pending assignments
}

// assignment tracks an in-flight verification work item.
type assignment struct {
	eventID    event.EventID
	validatorID crypto.AgentID
	assignedAt time.Time
}

// AssignmentCoordinator manages the pool of registered mainnet verification
// nodes and routes OCS verification work to them. Renamed from Validator to
// free that name for the persisted validator-registry record in registry.go.
//
// It is safe for concurrent use by multiple goroutines.
type AssignmentCoordinator struct {
	config      *AssignmentConfig
	engine      *ocs.Engine
	mu          sync.RWMutex
	validators  map[crypto.AgentID]*ValidatorInfo
	assignments map[event.EventID]*assignment
}

// NewAssignmentCoordinator creates an AssignmentCoordinator backed by the
// provided OCS engine. cfg may be nil, in which case DefaultAssignmentConfig is used.
func NewAssignmentCoordinator(engine *ocs.Engine, cfg *AssignmentConfig) *AssignmentCoordinator {
	if cfg == nil {
		cfg = DefaultAssignmentConfig()
	}
	return &AssignmentCoordinator{
		config:      cfg,
		engine:      engine,
		validators:  make(map[crypto.AgentID]*ValidatorInfo),
		assignments: make(map[event.EventID]*assignment),
	}
}

// RegisterValidator adds a verification node to the active pool.
// Returns ErrValidatorNotFound (adapted) if the validator was already registered
// (idempotent: registering twice re-activates without error).
func (v *AssignmentCoordinator) RegisterValidator(id crypto.AgentID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if existing, ok := v.validators[id]; ok {
		existing.Active = true
		return nil
	}
	v.validators[id] = &ValidatorInfo{
		ID:           id,
		RegisteredAt: time.Now(),
		Active:       true,
	}
	return nil
}

// UnregisterValidator removes a verification node from the active pool.
// Any pending assignments for this validator are not automatically reassigned;
// they will time out via AssignmentTimeout and be picked up by another node.
func (v *AssignmentCoordinator) UnregisterValidator(id crypto.AgentID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	info, ok := v.validators[id]
	if !ok {
		return ErrValidatorNotFound
	}
	info.Active = false
	return nil
}

// AssignVerification selects an available validator and assigns the OCS event
// to it for verification. Returns ErrAlreadyAssigned if the event already has
// an active assignment. Returns an error if no validators are available.
func (v *AssignmentCoordinator) AssignVerification(eventID event.EventID) (crypto.AgentID, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, exists := v.assignments[eventID]; exists {
		return "", ErrAlreadyAssigned
	}

	// Select the active validator with the fewest current assignments (least-loaded).
	var selected *ValidatorInfo
	for _, info := range v.validators {
		if !info.Active {
			continue
		}
		if info.Assignments >= v.config.MaxConcurrentAssignments {
			continue
		}
		if selected == nil || info.Assignments < selected.Assignments {
			selected = info
		}
	}
	if selected == nil {
		return "", errors.New("validator: no available validators")
	}

	selected.Assignments++
	v.assignments[eventID] = &assignment{
		eventID:     eventID,
		validatorID: selected.ID,
		assignedAt:  time.Now(),
	}
	return selected.ID, nil
}

// ProcessVerification submits a validator's verdict to the OCS engine and
// clears the assignment record. Returns ErrValidatorNotFound if the
// submitting validator is not the assigned one (anti-spoofing).
func (v *AssignmentCoordinator) ProcessVerification(result ocs.VerificationResult) error {
	v.mu.Lock()
	asgn, exists := v.assignments[result.EventID]
	if !exists {
		v.mu.Unlock()
		// Not assigned — submit directly (e.g. during testing or auto-settlement).
		return v.engine.ProcessResult(result)
	}
	if asgn.validatorID != result.VerifierID {
		v.mu.Unlock()
		return ErrValidatorNotFound
	}
	// Decrement assignment counter on the validator.
	if info, ok := v.validators[asgn.validatorID]; ok && info.Assignments > 0 {
		info.Assignments--
	}
	delete(v.assignments, result.EventID)
	v.mu.Unlock()

	return v.engine.ProcessResult(result)
}

// SweepExpiredAssignments removes assignments that have exceeded AssignmentTimeout
// without a verdict. Call periodically from a background goroutine.
func (v *AssignmentCoordinator) SweepExpiredAssignments() []event.EventID {
	now := time.Now()
	v.mu.Lock()
	defer v.mu.Unlock()

	var expired []event.EventID
	for eventID, asgn := range v.assignments {
		if now.Sub(asgn.assignedAt) > v.config.AssignmentTimeout {
			if info, ok := v.validators[asgn.validatorID]; ok && info.Assignments > 0 {
				info.Assignments--
			}
			delete(v.assignments, eventID)
			expired = append(expired, eventID)
		}
	}
	return expired
}

// ActiveValidators returns a snapshot of all currently registered validators.
func (v *AssignmentCoordinator) ActiveValidators() []*ValidatorInfo {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]*ValidatorInfo, 0, len(v.validators))
	for _, info := range v.validators {
		cp := *info
		out = append(out, &cp)
	}
	return out
}
