package identity

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// identityPersistence is the subset of store.Store used by Registry.
// *store.Store from the store package satisfies this interface.
type identityPersistence interface {
	PutIdentity(fp *CapabilityFingerprint) error
	AllIdentities() ([]*CapabilityFingerprint, error)
}

// Sentinel errors for programmatic handling by callers.
var (
	// ErrAgentNotFound is returned when an operation targets an AgentID that
	// has not been registered with this Registry instance.
	ErrAgentNotFound = errors.New("agent not found")

	// ErrAgentAlreadyExists is returned when Register is called with an AgentID
	// that is already present. Identity is globally unique; re-registration is
	// rejected to prevent accidental overwriting of an established fingerprint.
	ErrAgentAlreadyExists = errors.New("agent already registered")

	// ErrVersionMismatch is returned by Update when the incoming fingerprint's
	// FingerprintVersion does not equal the stored version plus one. This enforces
	// optimistic concurrency control: callers must Get the current fingerprint,
	// apply their changes to a fresh copy (incrementing the version by exactly 1),
	// and then call Update. If another mutation occurred concurrently, the version
	// will have advanced and the caller must retry with the new base.
	ErrVersionMismatch = errors.New("fingerprint version must increment by exactly 1")
)

// Adjustment constants for OptimisticTrustLimit.
const (
	// trustIncreaseBase is added to OptimisticTrustLimit on every successful task
	// completion, before the value-proportional component (valueGenerated/100).
	trustIncreaseBase uint64 = 500 // micro-AET

	// failurePct is the percentage of OptimisticTrustLimit retained after a failure
	// (100 - failurePct = 15% reduction per failure).
	failurePct uint64 = 85
)

// Registry is the node's local view of the network's agent reputation database.
// It is the primary data source for OCS trust decisions: before extending optimistic
// credit, the settlement engine queries the Registry to check the transacting agent's
// OptimisticTrustLimit and StakedAmount.
//
// Consistency model: the Registry is an eventually-consistent, last-write-wins
// store of capability fingerprints. Version numbers enforce causal ordering of
// updates: a fingerprint update is only accepted if it increments the stored
// version by exactly one, preventing out-of-order or stale writes.
//
// Concurrency: a single sync.RWMutex protects all internal state. Read operations
// (Get, CanTransact, All) acquire a read lock and run concurrently. Write operations
// (Register, Update, RecordTask*) acquire an exclusive write lock.
type Registry struct {
	mu     sync.RWMutex
	agents map[crypto.AgentID]*CapabilityFingerprint
	store  identityPersistence
}

// SetStore attaches a persistence backend to the Registry. After this call
// Register, Update, RecordTaskCompletion, and RecordTaskFailure write through.
// s must satisfy identityPersistence; *store.Store from the store package does so.
func (r *Registry) SetStore(s identityPersistence) {
	r.store = s
}

// NewRegistry creates an empty Registry ready to receive agent registrations.
func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[crypto.AgentID]*CapabilityFingerprint),
	}
}

// Register adds a new agent to the Registry.
//
// The incoming fingerprint is cloned and its FingerprintHash is recomputed before
// storage — the Registry always owns hash integrity and does not trust caller-provided hashes.
//
// Returns ErrAgentAlreadyExists if the AgentID is already registered. Identity is
// globally unique; use Update to modify an existing fingerprint.
func (r *Registry) Register(fp *CapabilityFingerprint) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[fp.AgentID]; exists {
		return fmt.Errorf("identity: %w: %s", ErrAgentAlreadyExists, fp.AgentID)
	}

	stored := fp.clone()
	if err := stored.refreshHash(); err != nil {
		return fmt.Errorf("identity: failed to compute hash during registration: %w", err)
	}
	r.agents[fp.AgentID] = stored
	if r.store != nil {
		_ = r.store.PutIdentity(stored)
	}
	return nil
}

// Update replaces an existing fingerprint with updatedFP, enforcing version ordering.
//
// The incoming FingerprintVersion must equal the currently stored version plus one.
// This implements optimistic concurrency control: callers should Get the current
// fingerprint, apply their changes (incrementing FingerprintVersion by 1), and call
// Update. If another writer advanced the version concurrently, Update returns
// ErrVersionMismatch and the caller must Get again and retry.
//
// The Registry recomputes FingerprintHash from updatedFP's canonical fields,
// ignoring whatever hash the caller may have set.
func (r *Registry) Update(agentID crypto.AgentID, updatedFP *CapabilityFingerprint) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.agents[agentID]
	if !ok {
		return fmt.Errorf("identity: %w: %s", ErrAgentNotFound, agentID)
	}

	want := existing.FingerprintVersion + 1
	if updatedFP.FingerprintVersion != want {
		return fmt.Errorf("identity: %w: got %d, want %d",
			ErrVersionMismatch, updatedFP.FingerprintVersion, want)
	}

	stored := updatedFP.clone()
	if err := stored.refreshHash(); err != nil {
		return fmt.Errorf("identity: failed to compute hash during update: %w", err)
	}
	r.agents[agentID] = stored
	if r.store != nil {
		_ = r.store.PutIdentity(stored)
	}
	return nil
}

// Get returns a clone of the stored fingerprint for agentID.
//
// A clone is returned (not a pointer to the stored value) so the caller may
// safely read and modify the returned fingerprint without affecting registry state.
// Returns ErrAgentNotFound if the agent has not been registered.
func (r *Registry) Get(agentID crypto.AgentID) (*CapabilityFingerprint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	fp, err := r.getRaw(agentID)
	if err != nil {
		return nil, err
	}
	return fp.clone(), nil
}

// GetByDisplayName performs a linear scan and returns the first fingerprint whose
// DisplayName matches name (case-sensitive). Returns (nil, false) when not found.
// Use Get (by AgentID) for performance-critical lookups.
func (r *Registry) GetByDisplayName(name string) (*CapabilityFingerprint, bool) {
	if name == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, fp := range r.agents {
		if fp.DisplayName == name {
			return fp.clone(), true
		}
	}
	return nil, false
}

// RecordTaskCompletion records a successful task for agentID in the given capability
// domain, crediting valueGenerated micro-AET of generated value.
//
// Mutations applied atomically under the write lock:
//   - TasksCompleted incremented by 1
//   - TotalValueGenerated incremented by valueGenerated
//   - LastActive updated to now
//   - ReputationScore recalculated from new task counts
//   - OptimisticTrustLimit increased by (500 + valueGenerated/100),
//     then capped at StakedAmount×10 (if StakedAmount > 0), then floored at minTrustLimit
//   - Capability domain's EvidenceCount incremented; domain added if new
//   - FingerprintVersion incremented by 1
//   - FingerprintHash recomputed
func (r *Registry) RecordTaskCompletion(agentID crypto.AgentID, valueGenerated uint64, domain string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	fp, err := r.getRaw(agentID)
	if err != nil {
		return err
	}

	fp.TasksCompleted++
	fp.TotalValueGenerated += valueGenerated
	fp.LastActive = time.Now().UTC()

	fp.recalcReputationScore()

	// Expand the trust limit proportional to the value generated.
	// The larger the task, the bigger the trust boost — agents are rewarded
	// for taking on high-value work and delivering.
	increase := trustIncreaseBase + valueGenerated/100
	fp.OptimisticTrustLimit += increase

	// Cap at StakedAmount × 10. Stake is skin-in-the-game: an agent cannot
	// earn more optimistic credit than 10× what it has put at risk. This
	// prevents trust farming without economic commitment.
	// Only applied when StakedAmount > 0; unstaked agents have no cap from
	// this formula (the floor still applies).
	if fp.StakedAmount > 0 {
		cap := fp.StakedAmount * 10
		if fp.OptimisticTrustLimit > cap {
			fp.OptimisticTrustLimit = cap
		}
	}
	if fp.OptimisticTrustLimit < minTrustLimit {
		fp.OptimisticTrustLimit = minTrustLimit
	}

	// Update the capability evidence for the task's domain.
	found := false
	for i := range fp.Capabilities {
		if fp.Capabilities[i].Domain == domain {
			fp.Capabilities[i].EvidenceCount++
			fp.Capabilities[i].LastVerified = fp.LastActive
			found = true
			break
		}
	}
	if !found {
		// New domain: agent demonstrated a skill they had not previously recorded.
		fp.Capabilities = append(fp.Capabilities, Capability{
			Domain:        domain,
			Confidence:    0, // starts at 0; raised by attestation, not by task count alone
			EvidenceCount: 1,
			LastVerified:  fp.LastActive,
		})
	}

	fp.FingerprintVersion++
	if err := fp.refreshHash(); err != nil {
		return err
	}
	if r.store != nil {
		_ = r.store.PutIdentity(fp)
	}
	return nil
}

// RecordTaskFailure records a failed task for agentID in the given capability domain.
//
// Mutations applied atomically under the write lock:
//   - TasksFailed incremented by 1
//   - LastActive updated to now
//   - ReputationScore recalculated (decreases due to higher failure count)
//   - OptimisticTrustLimit reduced to 85% of current value, floored at minTrustLimit
//   - FingerprintVersion incremented by 1
//   - FingerprintHash recomputed
//
// The capability domain is recorded for audit purposes but failures do not add
// EvidenceCount — only verified completions build positive evidence.
func (r *Registry) RecordTaskFailure(agentID crypto.AgentID, domain string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	fp, err := r.getRaw(agentID)
	if err != nil {
		return err
	}

	fp.TasksFailed++
	fp.LastActive = time.Now().UTC()

	fp.recalcReputationScore()

	// 15% reduction per failure. This is designed to be recoverable: a strong
	// track record absorbs occasional failures without catastrophic trust loss,
	// but repeated failures rapidly erode the limit toward the floor.
	fp.OptimisticTrustLimit = fp.OptimisticTrustLimit * failurePct / 100
	if fp.OptimisticTrustLimit < minTrustLimit {
		fp.OptimisticTrustLimit = minTrustLimit
	}

	fp.FingerprintVersion++
	if err := fp.refreshHash(); err != nil {
		return err
	}
	if r.store != nil {
		_ = r.store.PutIdentity(fp)
	}
	return nil
}

// CanTransact reports whether agentID may optimistically transact the given amount.
//
// Returns true only if:
//   - amount <= fp.OptimisticTrustLimit  (within network-granted credit)
//   - amount <= fp.StakedAmount          (skin-in-the-game constraint)
//
// Both conditions must hold. An agent with high reputation but zero stake cannot
// transact any non-zero amount — collateral is always required.
//
// Returns ErrAgentNotFound if the agent has not been registered.
func (r *Registry) CanTransact(agentID crypto.AgentID, amount uint64) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	fp, err := r.getRaw(agentID)
	if err != nil {
		return false, err
	}
	return amount <= fp.OptimisticTrustLimit && amount <= fp.StakedAmount, nil
}

// All returns a paginated, deterministically-ordered snapshot of all registered
// fingerprints. Fingerprints are sorted by AgentID (lexicographic ascending)
// before the offset and limit are applied — ensuring consistent pagination across
// multiple calls on the same Registry state.
//
// limit ≤ 0 means "return all remaining entries from offset".
// offset ≤ 0 is treated as 0 (start from the beginning).
// If offset exceeds the total count, an empty slice is returned.
//
// Each returned element is a clone; callers may mutate them freely.
func (r *Registry) All(limit, offset int) []*CapabilityFingerprint {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*CapabilityFingerprint, 0, len(r.agents))
	for _, fp := range r.agents {
		result = append(result, fp.clone())
	}

	// Sort by AgentID for deterministic ordering. Without this, Go map iteration
	// order is random, making pagination non-deterministic across calls.
	sort.Slice(result, func(i, j int) bool {
		return result[i].AgentID < result[j].AgentID
	})

	if offset < 0 {
		offset = 0
	}
	if offset >= len(result) {
		return []*CapabilityFingerprint{}
	}
	result = result[offset:]

	if limit > 0 && limit < len(result) {
		result = result[:limit]
	}
	return result
}

// getRaw returns the raw stored pointer for agentID without cloning.
// Must be called with at least a read lock held. Internal callers that need
// to mutate the stored fingerprint use this directly rather than Get (which
// would create a useless clone that would be discarded after mutation).
func (r *Registry) getRaw(agentID crypto.AgentID) (*CapabilityFingerprint, error) {
	fp, ok := r.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("identity: %w: %s", ErrAgentNotFound, agentID)
	}
	return fp, nil
}

// LoadRegistryFromStore reconstructs a Registry from a persisted store, restoring
// all previously-registered agent fingerprints. The returned registry has s
// attached so subsequent mutations write through. s must satisfy identityPersistence;
// *store.Store from the store package does so.
func LoadRegistryFromStore(s identityPersistence) (*Registry, error) {
	fps, err := s.AllIdentities()
	if err != nil {
		return nil, fmt.Errorf("identity: load from store: %w", err)
	}
	r := NewRegistry()
	r.store = s
	for _, fp := range fps {
		r.agents[fp.AgentID] = fp
	}
	return r, nil
}
