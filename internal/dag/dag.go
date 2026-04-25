// Package dag implements the append-only causal Directed Acyclic Graph (DAG)
// that forms the structural spine of the AetherNet protocol.
//
// Unlike a blockchain, which imposes a single total order by batching events into
// sequentially-linked blocks, the AetherNet DAG allows events to be produced in
// parallel. Each event references only the specific prior events it depends on,
// making causal relationships explicit at the data level. The DAG grows outward
// from genesis events rather than growing as a single chain.
//
// # Internal representation
//
// Three maps maintain the graph state:
//
//	events:   EventID → *event.Event            (primary store, O(1) lookup)
//	children: EventID → set{EventID}            (forward edges, built incrementally)
//	tips:     set{EventID}                      (frontier: events with no children yet)
//
// Directed edges come in two flavors:
//   - Backward (causal) edges: e.CausalRefs lists the events e directly depends on.
//   - Forward edges: children[id] is the set of events that include id in CausalRefs.
//
// Forward edges are maintained explicitly because many operations (topological sort,
// settlement propagation) need to traverse the DAG from parents toward children,
// not just from children toward parents.
//
// # Design principles
//
//  1. Append-only: events are never removed or modified through the DAG API.
//     This mirrors the immutability of the causal record.
//
//  2. Causal validation: Add rejects any event whose CausalRefs reference an ID
//     not already in the DAG. You cannot build on events you haven't seen.
//
//  3. Tip tracking: the frontier (events not yet referenced by any child) is
//     maintained as a set and updated in O(|CausalRefs|) on every Add. Tips are
//     the natural candidates for new events to extend the DAG.
//
//  4. Concurrent access: a single sync.RWMutex protects all internal state.
//     Multiple goroutines may read concurrently; writes serialise. A single
//     coarse-grained lock is chosen over fine-grained per-map locking to avoid
//     the complexity and deadlock risk of coordinating multiple locks, while
//     still providing concurrent reads.
//
//  5. Deterministic topological sort: events are ordered by (CausalTimestamp, EventID),
//     which is a provably valid topological order (parent timestamps are strictly
//     less than their children's) and is stable across repeated calls.
package dag

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// dagPersistence is the subset of store.Store used by DAG.
// Defining a local interface breaks the potential import cycle: the store package
// may import dag-adjacent packages, and using an interface here avoids any
// circular dependency. *store.Store from the store package satisfies this interface.
type dagPersistence interface {
	PutEvent(e *event.Event) error
	AllEvents() ([]*event.Event, error)
}

// Sentinel errors allow callers to use errors.Is for programmatic branching
// rather than string-matching error messages.
var (
	// ErrEventNotFound is returned when a requested EventID is absent from the DAG.
	ErrEventNotFound = errors.New("event not found")

	// ErrDuplicateEvent is returned when an event whose ID is already stored is added
	// again.
	ErrDuplicateEvent = errors.New("duplicate event")

	// ErrMissingCausalRef is returned when an event's CausalRefs include an EventID
	// not yet present in the DAG.
	ErrMissingCausalRef = errors.New("causal reference not in DAG")

	// ErrInvalidSignature is returned when an event's cryptographic signature does
	// not verify against its canonical content and public key.
	ErrInvalidSignature = errors.New("dag: invalid event signature")

	// ErrMissingSignature is returned when a non-genesis event has no signature.
	ErrMissingSignature = errors.New("dag: event has no signature")

	// ErrCrossCheckRejected is returned when an admission-cross-check
	// validator rejects an event during Add. The wrapped error from the
	// validator is preserved for diagnostic purposes.
	ErrCrossCheckRejected = errors.New("dag: admission cross-check rejected")

	// ErrCrossCheckAlreadyRegistered is returned by RegisterAdmissionCrossCheck
	// when a validator is already registered for the given event type.
	// Validators are one-shot per type; reconfiguration is not supported.
	ErrCrossCheckAlreadyRegistered = errors.New("dag: admission cross-check already registered for event type")
)

// AdmissionCrossCheck is a per-event-type, admission-time, canonical-state-
// dependent payload validator. See RegisterAdmissionCrossCheck.
type AdmissionCrossCheck func(ev *event.Event, reader WhileLockedReader) error

// WhileLockedReader is the lock-free read interface available to admission-
// cross-check validators while dag.Add holds its write lock. Methods on
// WhileLockedReader access DAG state directly without re-acquiring the lock
// — the caller (dag.Add) already holds it.
//
// RESTRICTED API. Validators MUST use only methods on this interface; they
// MUST NOT call *DAG methods like d.IsAncestor or d.CountAncestorsByType,
// which acquire RLock and would deadlock against the held write lock.
//
// New canonical-state read methods are added here as future
// admission-cross-check users require them. Each addition is one entry per
// canonical query the validators need; non-canonical or runtime-state reads
// are explicitly out of scope for this interface.
type WhileLockedReader interface {
	// GetWhileLocked returns the event with the given ID, or
	// ErrEventNotFound if absent. Lock-free equivalent of (*DAG).Get.
	GetWhileLocked(id event.EventID) (*event.Event, error)

	// CountAncestorsByTypeWhileLocked counts the canonical strict ancestors
	// of descendant whose event type equals eventType. Lock-free equivalent
	// of (*DAG).CountAncestorsByType. Same all-or-defer semantic per
	// canonical-epoch sub-spec §3.1.
	CountAncestorsByTypeWhileLocked(descendant event.EventID, eventType event.EventType) (uint64, error)
}

// whileLockedReader is the *DAG-backed implementation of WhileLockedReader.
// Methods read d.events directly without acquiring d.mu; constructor must
// only hand this out under a held write lock.
type whileLockedReader struct {
	d *DAG
}

func (r *whileLockedReader) GetWhileLocked(id event.EventID) (*event.Event, error) {
	e, ok := r.d.events[id]
	if !ok {
		return nil, fmt.Errorf("dag: %w: %s", ErrEventNotFound, id)
	}
	return e, nil
}

func (r *whileLockedReader) CountAncestorsByTypeWhileLocked(descendant event.EventID, eventType event.EventType) (uint64, error) {
	if _, ok := r.d.events[descendant]; !ok {
		return 0, fmt.Errorf("dag: %w: %s", ErrEventNotFound, descendant)
	}

	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}
	var count uint64

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		e, ok := r.d.events[cur]
		if !ok {
			return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, cur, descendant)
		}
		for _, ref := range e.CausalRefs {
			if _, seen := visited[ref]; seen {
				continue
			}
			visited[ref] = struct{}{}
			queue = append(queue, ref)

			refEvent, ok := r.d.events[ref]
			if !ok {
				return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, ref, descendant)
			}
			if refEvent.Type == eventType {
				count++
			}
		}
	}

	return count, nil
}

// DAG is a concurrent, append-only causal directed acyclic graph of AetherNet events.
// The zero value is not usable; construct via New.
type DAG struct {
	mu sync.RWMutex

	// events is the authoritative store mapping every known EventID to its event.
	events map[event.EventID]*event.Event

	// children maps each EventID to the set of EventIDs that list it in CausalRefs.
	// These are the forward (parent→child) edges of the DAG. They are dual to the
	// backward (child→parent) edges stored in event.CausalRefs.
	// Every EventID present in events has a corresponding entry in children
	// (possibly an empty set if the event has no children yet).
	children map[event.EventID]map[event.EventID]struct{}

	// tips is the frontier: the set of EventIDs that have no children yet.
	// An event enters tips when it is added, and is removed from tips when
	// any later event lists it in CausalRefs.
	tips map[event.EventID]struct{}

	// store is the optional persistence backend. When non-nil, every successful
	// Add writes the event through to BadgerDB for durability. Defaults to nil
	// (in-memory only) so existing tests require no changes.
	store dagPersistence

	// onCommit is an optional hook called after every successful durable
	// insert (Add or addFromStore). Called OUTSIDE the DAG write lock.
	// The replay flag is true when the event is being loaded from persistent
	// storage (addFromStore), false for live inserts (Add).
	//
	// This is the universal convergence point for the Causal Commit Bus:
	// every event path (local, remote, repair, replay) fires this hook.
	// The hook must not block — use a non-blocking channel or bounded queue.
	//
	// For source tagging (local vs remote vs repair), each call site emits
	// to the recognition bus directly with the correct CommitSource. The
	// onCommit hook provides replay-path coverage that no call site handles.
	onCommit func(ev *event.Event, replay bool)

	// crossChecks holds the admission-time canonical-state cross-check
	// validators, keyed by event type. Populated via
	// RegisterAdmissionCrossCheck at startup; read in Add under the write
	// lock. One validator per event type; registration is one-shot.
	//
	// See RegisterAdmissionCrossCheck for the restricted-API discipline.
	crossChecks map[event.EventType]AdmissionCrossCheck
}

// SetStore attaches a persistence backend to the DAG. After this call every
// successful Add writes through to the store. Must be called before any Add
// to ensure the full event history is durable. s must satisfy dagPersistence;
// *store.Store from the store package does so.
func (d *DAG) SetStore(s dagPersistence) {
	d.store = s
}

// SetOnCommit registers a callback that fires after every successful durable
// insert (Add or addFromStore). The callback runs OUTSIDE the DAG write lock
// and must not block. Set before any Add or LoadFromStore call.
//
// The replay parameter is true for events loaded from persistence (addFromStore),
// false for live inserts (Add). This enables source-aware dispatch in the
// Causal Commit Bus without coupling the DAG to the recognition package.
func (d *DAG) SetOnCommit(fn func(ev *event.Event, replay bool)) {
	d.onCommit = fn
}

// New creates and returns an empty DAG ready to accept events.
func New() *DAG {
	return &DAG{
		events:      make(map[event.EventID]*event.Event),
		children:    make(map[event.EventID]map[event.EventID]struct{}),
		tips:        make(map[event.EventID]struct{}),
		crossChecks: make(map[event.EventType]AdmissionCrossCheck),
	}
}

// RegisterAdmissionCrossCheck registers a validation function that runs
// synchronously during dag.Add, after signature+causal-refs verification
// and before the event is stored. Per F5 5B canonical-epoch sub-spec
// v2.2 §1.4.1.
//
// RESTRICTED API. The validator MUST:
//   - Be a pure function of the candidate event and canonical DAG state.
//   - Use the provided WhileLockedReader; never call *DAG methods that
//     acquire locks (reentrancy deadlock — d.mu is already held).
//   - Return an error to reject; nil to admit.
//   - Be fast (runs under the DAG write lock, blocking all other Add and
//     read traffic).
//   - Have no I/O, no goroutines, no side effects beyond the return value.
//
// One validator per event type; registration at startup, not reconfigurable.
// Returns ErrCrossCheckAlreadyRegistered if a validator is already set
// for eventType.
//
// Use case: canonical cross-checks where payload validity depends on
// canonical DAG state at admission (e.g., EpochBoundary's Payload.Epoch
// equals CountAncestorsByType + 1). NOT for policy, rate-limiting, or
// non-canonical concerns. Mis-use is a halt-worthy regression per
// canonical-epoch sub-spec §9.
//
// The substrate enforces single-registration but cannot enforce purity
// — the discipline is on the validator author, with code review as the
// primary defense and the §9 halt-trigger as the implementation-time
// catch.
func (d *DAG) RegisterAdmissionCrossCheck(eventType event.EventType, validator AdmissionCrossCheck) error {
	if eventType == "" {
		return errors.New("dag: RegisterAdmissionCrossCheck requires non-empty event type")
	}
	if validator == nil {
		return errors.New("dag: RegisterAdmissionCrossCheck requires non-nil validator")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.crossChecks == nil {
		d.crossChecks = make(map[event.EventType]AdmissionCrossCheck)
	}
	if _, exists := d.crossChecks[eventType]; exists {
		return fmt.Errorf("%w: %s", ErrCrossCheckAlreadyRegistered, eventType)
	}
	d.crossChecks[eventType] = validator
	return nil
}

// Add inserts event e into the DAG and updates the causal graph.
//
// Preconditions checked before any state mutation (atomic):
//   - e.ID must not already be present in the DAG.
//   - Every EventID in e.CausalRefs must already be present in the DAG.
//
// On success:
//   - e is stored and retrievable via Get(e.ID).
//   - e.ID is added to the tips set (e has no children yet).
//   - Every ref in e.CausalRefs is removed from tips (they now have at least one child).
//   - children[ref] is updated to include e.ID for every ref in e.CausalRefs.
//
// The event pointer is stored directly. The caller must not mutate e after
// Add returns; treat stored events as immutable. SettlementState transitions
// (via event.Transition) are the one permitted post-Add mutation, but they
// are not protected by the DAG's mutex — coordinate externally if needed.
func (d *DAG) Add(e *event.Event) error {
	d.mu.Lock()

	if _, exists := d.events[e.ID]; exists {
		d.mu.Unlock()
		return fmt.Errorf("dag: %w: %s", ErrDuplicateEvent, e.ID)
	}

	// Validate all causal references before mutating any state.
	// This check runs before signature verification so that events with unknown
	// refs return ErrMissingCausalRef (the structurally prior error) rather than
	// ErrMissingSignature when both conditions hold.
	for _, ref := range e.CausalRefs {
		if _, ok := d.events[ref]; !ok {
			d.mu.Unlock()
			return fmt.Errorf("dag: %w: %s (referenced by %s)", ErrMissingCausalRef, ref, e.ID)
		}
	}

	// Signature enforcement: ALL events must be signed and verifiable,
	// including genesis events (empty CausalRefs). Genesis events are signed
	// by a key in the validator manifest. This prevents injection of unsigned
	// events by unauthorized nodes.
	if len(e.Signature) == 0 {
		d.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrMissingSignature, e.ID)
	}
	if !crypto.VerifyEvent(e) {
		d.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrInvalidSignature, e.ID)
	}

	// Admission cross-check: per-type canonical-state-dependent payload
	// validation, registered via RegisterAdmissionCrossCheck. Runs after
	// signature + causal-refs verification, before any state mutation.
	// The validator is a pure function of (event, canonical DAG state)
	// and uses WhileLockedReader for lock-free reads under our held write
	// lock. Restricted-API discipline per F5 5B canonical-epoch sub-spec
	// v2.2 §1.4.1.
	if validator, ok := d.crossChecks[e.Type]; ok {
		reader := &whileLockedReader{d: d}
		if err := validator(e, reader); err != nil {
			d.mu.Unlock()
			return fmt.Errorf("%w: %s: %w", ErrCrossCheckRejected, e.ID, err)
		}
	}

	// Commit phase — all preconditions are satisfied; mutate state.

	// Store the event and initialise its (empty) child set.
	d.events[e.ID] = e
	d.children[e.ID] = make(map[event.EventID]struct{})

	// Update forward edges and tip set for each causal reference.
	for _, ref := range e.CausalRefs {
		d.children[ref][e.ID] = struct{}{}
		// ref now has at least one child — it leaves the frontier.
		delete(d.tips, ref)
	}

	// e itself has no children yet, so it enters the frontier.
	d.tips[e.ID] = struct{}{}

	// Write-through to the persistence store when one is attached.
	if d.store != nil {
		if err := d.store.PutEvent(e); err != nil {
			slog.Error("dag: failed to persist event", "event_id", e.ID, "err", err)
		}
	}

	// Release lock BEFORE firing the commit hook. The hook must never run
	// under the DAG write lock — it may enqueue work to consumers that
	// read from the DAG, causing deadlock or blocking the hot path.
	d.mu.Unlock()

	// Fire the post-commit hook (Causal Commit Bus convergence point).
	if d.onCommit != nil {
		d.onCommit(e, false)
	}

	return nil
}

// LoadFromStore reconstructs an in-memory DAG from a previously persisted store.
// Events are replayed in CausalTimestamp order so that every parent is inserted
// before its children. The returned DAG has s attached as its store so subsequent
// Add calls continue to write through. s must satisfy dagPersistence;
// *store.Store from the store package does so.
func LoadFromStore(s dagPersistence) (*DAG, error) {
	events, err := s.AllEvents()
	if err != nil {
		return nil, fmt.Errorf("dag: load from store: %w", err)
	}

	d := New()
	d.store = s

	// Topological sort (Kahn's algorithm): replay parents before children.
	// CausalTimestamp ordering is insufficient because multiple events can
	// share a timestamp and clock skew can invert parent/child ordering.
	sorted, skipped := topoSort(events)
	if len(skipped) > 0 {
		slog.Warn("dag: skipped events with unresolvable parents during replay",
			"skipped", len(skipped), "total", len(events))
	}

	for _, e := range sorted {
		if err := d.addFromStore(e); err != nil {
			return nil, fmt.Errorf("dag: replay event %s: %w", e.ID, err)
		}
	}
	return d, nil
}

// topoSort performs a topological sort of events using Kahn's algorithm.
// Events with no CausalRefs (roots) are emitted first, followed by events
// whose parents have all been emitted. Returns the sorted list and any
// events that could not be placed (missing parents — indicates data corruption).
func topoSort(events []*event.Event) (sorted []*event.Event, skipped []*event.Event) {
	byID := make(map[event.EventID]*event.Event, len(events))
	for _, e := range events {
		byID[e.ID] = e
	}

	// inDegree counts the number of unsatisfied parent references per event.
	inDegree := make(map[event.EventID]int, len(events))
	// children maps parent → list of child event IDs.
	children := make(map[event.EventID][]event.EventID)

	for _, e := range events {
		deps := 0
		for _, ref := range e.CausalRefs {
			if _, exists := byID[ref]; exists {
				deps++
				children[ref] = append(children[ref], e.ID)
			}
			// If parent is not in the set, it's either already in the DAG
			// (genesis) or truly missing — don't count it as a dependency.
		}
		inDegree[e.ID] = deps
	}

	// Seed the queue with all zero-dependency events (roots).
	queue := make([]event.EventID, 0, len(events)/2)
	for _, e := range events {
		if inDegree[e.ID] == 0 {
			queue = append(queue, e.ID)
		}
	}

	sorted = make([]*event.Event, 0, len(events))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byID[id])

		for _, childID := range children[id] {
			inDegree[childID]--
			if inDegree[childID] == 0 {
				queue = append(queue, childID)
			}
		}
	}

	// Any events still with inDegree > 0 have unresolvable parents.
	if len(sorted) < len(events) {
		for _, e := range events {
			if inDegree[e.ID] > 0 {
				skipped = append(skipped, e)
			}
		}
	}
	return
}

// addFromStore inserts e into the in-memory DAG structures, verifying the
// EventID and signature for integrity. It does not write back to the store
// (we are loading FROM the store). Duplicate events during replay are
// silently skipped.
func (d *DAG) addFromStore(e *event.Event) error {
	d.mu.Lock()

	// Silently skip duplicates during replay.
	if _, exists := d.events[e.ID]; exists {
		d.mu.Unlock()
		return nil
	}

	// Verify EventID integrity: recompute from content and compare.
	recomputed, err := event.ComputeID(e)
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("dag: replay: failed to recompute EventID for %s: %w", e.ID, err)
	}
	if recomputed != e.ID {
		d.mu.Unlock()
		return fmt.Errorf("dag: replay: EventID mismatch for %s: stored=%s computed=%s — possible store corruption",
			e.ID, e.ID, recomputed)
	}

	// Verify signature: signed events must have valid signatures.
	// Genesis events (empty CausalRefs) may be unsigned in legacy DAGs.
	if len(e.Signature) > 0 {
		if !crypto.VerifyEvent(e) {
			d.mu.Unlock()
			return fmt.Errorf("%w: %s (detected during store replay)", ErrInvalidSignature, e.ID)
		}
	}

	// Validate causal references. Topological sort guarantees parents are
	// replayed first; a missing ref here indicates data corruption.
	for _, ref := range e.CausalRefs {
		if _, ok := d.events[ref]; !ok {
			d.mu.Unlock()
			slog.Warn("dag: missing causal ref during replay, skipping event",
				"event", e.ID, "missing_ref", ref)
			return nil
		}
	}

	// Insert into in-memory structures (mirrors the commit phase of Add).
	d.events[e.ID] = e
	d.children[e.ID] = make(map[event.EventID]struct{})

	for _, ref := range e.CausalRefs {
		d.children[ref][e.ID] = struct{}{}
		delete(d.tips, ref)
	}
	d.tips[e.ID] = struct{}{}

	d.mu.Unlock()

	// Fire the post-commit hook for replay events.
	if d.onCommit != nil {
		d.onCommit(e, true)
	}

	return nil
}

// Get returns the event stored under id, or ErrEventNotFound if no such event exists.
//
// The returned pointer aliases the DAG's internal storage. Treat the pointed-at
// event as read-only; the DAG's RWMutex does not protect individual field writes
// on an event once the pointer is in the caller's hands.
func (d *DAG) Get(id event.EventID) (*event.Event, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	e, ok := d.events[id]
	if !ok {
		return nil, fmt.Errorf("dag: %w: %s", ErrEventNotFound, id)
	}
	return e, nil
}

// Tips returns a snapshot of the current DAG frontier — all events that have not
// yet been referenced by any child event.
//
// New events should reference tips when created: doing so extends the DAG, improves
// connectivity, and contributes to the causal record that validators score.
//
// The returned slice is a copy and is safe to hold after further events are added.
// Elements are returned in lex-sorted EventID order — E.P2.A1 (F4 plan §6).
// Without this, callers like dispatcher.currentAnchor() that select tips[0] as
// the per-node admission anchor would land different anchors on different nodes
// for the same DAG state. Per locked invariant C-15 the anchor is non-canonical
// node-local, so the divergence is harmless for consensus but pollutes
// diagnostic comparisons and replay-conformance reasoning.
func (d *DAG) Tips() []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()

	tips := make([]event.EventID, 0, len(d.tips))
	// safe: collecting keys for the sort that immediately follows
	for id := range d.tips {
		tips = append(tips, id)
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i] < tips[j] })
	return tips
}

// PrimaryTips returns the current DAG tips excluding events of type
// TrajectoryCommit. This is the default parent selection set for
// non-trajectory events — it prevents trajectory commits from polluting
// the causal refs of transfers, tasks, and other primary events.
//
// If filtering removes ALL tips (e.g., the DAG frontier consists entirely
// of trajectory commits), PrimaryTips falls back to the full Tips() set
// to avoid empty causal refs which would create invalid genesis-like events.
//
// The mechanical tip tracking (d.tips) is NOT modified. PrimaryTips is a
// filtered view, not a second tracked set.
func (d *DAG) PrimaryTips() []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()

	primary := make([]event.EventID, 0, len(d.tips))
	// safe: collecting keys for the sort that follows the filter
	for id := range d.tips {
		ev, ok := d.events[id]
		if !ok {
			continue
		}
		if ev.Type == event.EventTypeTrajectoryCommit {
			continue
		}
		primary = append(primary, id)
	}

	// Fallback: if all tips are trajectory commits, return all tips.
	if len(primary) == 0 && len(d.tips) > 0 {
		// safe: fallback collection of keys; sort applied at the bottom of the function
		for id := range d.tips {
			primary = append(primary, id)
		}
	}

	// Sort for cross-node determinism (E.P2.A1 — same rationale as Tips()).
	sort.Slice(primary, func(i, j int) bool { return primary[i] < primary[j] })
	return primary
}

// LocalTips returns the current DAG tips authored by the given agent,
// excluding TrajectoryCommit events. This is the recommended parent
// selection set for locally-created events — it ensures new events only
// reference parents that this node has already broadcast, preventing
// materialization stalls on remote nodes that may not yet have third-party
// tip events synced from other sources.
//
// If the agent has no current tips (e.g., first event after startup),
// LocalTips returns an empty slice. Callers should treat an empty result
// the same as a genesis event (CausalTimestamp will be 1).
func (d *DAG) LocalTips(agentID string) []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()

	local := make([]event.EventID, 0, 4)
	// safe: collecting keys for the sort that follows the filter
	for id := range d.tips {
		ev, ok := d.events[id]
		if !ok {
			continue
		}
		if ev.AgentID != agentID {
			continue
		}
		if ev.Type == event.EventTypeTrajectoryCommit {
			continue
		}
		local = append(local, id)
	}
	// Sort for cross-node determinism (E.P2.A1 — same rationale as Tips()).
	sort.Slice(local, func(i, j int) bool { return local[i] < local[j] })
	return local
}

// Size returns the number of events currently stored in the DAG.
func (d *DAG) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.events)
}

// All returns a snapshot of every event currently stored in the DAG.
// The slice is unordered — for a causally-ordered listing use TopologicalSort.
// Returned pointers alias the DAG's internal storage; treat events as read-only.
func (d *DAG) All() []*event.Event {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]*event.Event, 0, len(d.events))
	// safe: documented as unordered; callers requiring a canonical order use TopologicalSort
	for _, e := range d.events {
		result = append(result, e)
	}
	return result
}

// RecentEvents returns events with CausalTimestamp >= minTimestamp, bounded
// by maxCount. Events are sorted by CausalTimestamp ascending. This is a
// read-only operation — returned pointers alias the DAG's internal storage.
func (d *DAG) RecentEvents(minTimestamp uint64, maxCount int) []*event.Event {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var result []*event.Event
	// safe: filter pass; final result sorted by CausalTimestamp below
	for _, e := range d.events {
		if e.CausalTimestamp >= minTimestamp {
			result = append(result, e)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CausalTimestamp < result[j].CausalTimestamp
	})
	if len(result) > maxCount {
		result = result[len(result)-maxCount:]
	}
	return result
}

// MaxTimestamp returns the highest CausalTimestamp in the DAG, or 0 if empty.
func (d *DAG) MaxTimestamp() uint64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var max uint64
	// safe: commutative max-reduction; final scalar is order-independent
	for _, e := range d.events {
		if e.CausalTimestamp > max {
			max = e.CausalTimestamp
		}
	}
	return max
}

// EventIDs returns all event IDs currently in the DAG.
//
// The returned slice is intentionally NOT sorted; the sole production
// caller (Node.BuildCheckpoint) sorts the slice itself before hashing.
// Calling sort here would duplicate the work without affecting any
// observable property.
func (d *DAG) EventIDs() []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ids := make([]event.EventID, 0, len(d.events))
	// safe: caller sorts (Node.BuildCheckpoint) before any deterministic use
	for id := range d.events {
		ids = append(ids, id)
	}
	return ids
}

// Ancestors returns the set of all events that causally precede id —
// the transitive closure of id's CausalRefs, traversed breadth-first.
//
// id itself is not included (strict / irreflexive ancestor relation).
// The ordering of elements in the returned slice is BFS discovery order.
// For a causally ordered listing of the full DAG, use TopologicalSort.
//
// Returns ErrEventNotFound if id is not in the DAG.
func (d *DAG) Ancestors(id event.EventID) ([]event.EventID, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.ancestorsLocked(id)
}

// ancestorsLocked is the internal, mutex-free implementation of Ancestors.
// It is separated from Ancestors so that other methods (or future internal
// callers that already hold d.mu) can compute ancestors without a second lock
// acquisition, which would panic on a non-reentrant sync.RWMutex.
func (d *DAG) ancestorsLocked(id event.EventID) ([]event.EventID, error) {
	e, ok := d.events[id]
	if !ok {
		return nil, fmt.Errorf("dag: %w: %s", ErrEventNotFound, id)
	}

	// BFS over the backward (causal) edges, collecting all reachable ancestors.
	// We mark nodes visited when they are enqueued, not when they are dequeued,
	// to prevent adding the same ancestor to the result multiple times when it
	// is reachable via multiple paths (common in fork-then-merge subgraphs).
	visited := make(map[event.EventID]struct{})
	queue := make([]event.EventID, 0, len(e.CausalRefs))

	for _, ref := range e.CausalRefs {
		if _, seen := visited[ref]; !seen {
			visited[ref] = struct{}{}
			queue = append(queue, ref)
		}
	}

	result := make([]event.EventID, 0, len(visited))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		result = append(result, cur)

		parent, ok := d.events[cur]
		if !ok {
			// Defensive: should not occur in a DAG built exclusively through Add,
			// which enforces referential integrity before insertion.
			continue
		}
		for _, ref := range parent.CausalRefs {
			if _, seen := visited[ref]; !seen {
				visited[ref] = struct{}{}
				queue = append(queue, ref)
			}
		}
	}

	return result, nil
}

// IsAncestor reports whether ancestor is a strict causal ancestor of descendant —
// that is, whether there exists a directed path from descendant back to ancestor
// through CausalRefs edges.
//
// An event is not considered an ancestor of itself (strict / irreflexive).
//
// Returns ErrEventNotFound if either ID is absent from the DAG.
// Complexity: O(A) where A is the number of ancestors of descendant.
func (d *DAG) IsAncestor(ancestor, descendant event.EventID) (bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if _, ok := d.events[ancestor]; !ok {
		return false, fmt.Errorf("dag: %w: %s", ErrEventNotFound, ancestor)
	}
	if _, ok := d.events[descendant]; !ok {
		return false, fmt.Errorf("dag: %w: %s", ErrEventNotFound, descendant)
	}

	// Strict ancestor: an event is not its own ancestor.
	// Handle this before BFS so we don't short-circuit on the first dequeue.
	if ancestor == descendant {
		return false, nil
	}

	// BFS from descendant, following CausalRefs backward, searching for ancestor.
	// We stop as soon as ancestor is found rather than computing the full ancestor
	// set — this is an early-exit optimisation for hot-path settlement checks.
	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur == ancestor {
			return true, nil
		}

		e, ok := d.events[cur]
		if !ok {
			continue // defensive
		}
		for _, ref := range e.CausalRefs {
			if _, seen := visited[ref]; !seen {
				visited[ref] = struct{}{}
				queue = append(queue, ref)
			}
		}
	}

	return false, nil
}

// CountAncestorsByType counts the canonical strict ancestors of descendant
// whose event type equals eventType. The count does NOT include descendant
// itself (strict / irreflexive, matching IsAncestor semantics).
//
// Canonical: two nodes with identical DAG content compute identical counts
// for identical (descendant, eventType) arguments. The count is a pure
// function of DAG topology; no local-admission-order dependence.
//
// All-or-defer (per F5 5B canonical-epoch sub-spec §3.1): if descendant is
// not locally materialized, OR if any traversed ancestor needed to
// determine the count is not locally materialized, returns ErrEventNotFound.
// Callers MUST defer rather than use a partial count — partial counts
// would diverge across nodes with different materialization progress and
// break the D-1 cross-node byte-equality guarantee. Strict CausalRefs
// admission in Add (lines 187-192) makes the second clause defensive
// against future semantic changes; the first clause covers the genuine
// materialization-lag case.
//
// Returns (0, nil) if descendant exists but has no ancestors of the
// requested type. Not an error.
//
// Complexity: O(A) where A is the number of ancestors of descendant.
// Sub-spec §9 names a 1ms p99 latency gate at 10^6 ancestors;
// implementation may add an LRU cache or projection-assisted counting if
// benchmarking shows the gate is exceeded.
func (d *DAG) CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if _, ok := d.events[descendant]; !ok {
		return 0, fmt.Errorf("dag: %w: %s", ErrEventNotFound, descendant)
	}

	// BFS from descendant over CausalRefs. Strict / irreflexive: descendant
	// itself is not counted. Visited set guards against revisiting events
	// reachable via multiple paths.
	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}
	var count uint64

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		e, ok := d.events[cur]
		if !ok {
			// Defensive: should not happen given strict CausalRefs admission
			// in Add. If it does, the local DAG is missing an ancestor needed
			// to determine the count — defer per §3.1 all-or-defer rather
			// than return a partial count.
			return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, cur, descendant)
		}
		for _, ref := range e.CausalRefs {
			if _, seen := visited[ref]; seen {
				continue
			}
			visited[ref] = struct{}{}
			queue = append(queue, ref)

			refEvent, ok := d.events[ref]
			if !ok {
				return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, ref, descendant)
			}
			if refEvent.Type == eventType {
				count++
			}
		}
	}

	return count, nil
}

// TopologicalSort returns all events in a deterministic causal order.
//
// The ordering satisfies: for every event e at index i, all events in
// e.CausalRefs appear at indices less than i (parents before children).
//
// Algorithm: Kahn's algorithm (for cycle detection) followed by a stable sort
// on (CausalTimestamp, EventID). The sort step yields determinism because:
//
//	For any edge A → B (A is a parent of B):
//	    A.CausalTimestamp < B.CausalTimestamp
//
// This inequality holds by the Lamport clock derivation rule
// (child timestamp = max(parent timestamps) + 1), so sorting by CausalTimestamp
// preserves all causal relationships. EventID tiebreaking provides a total order
// among causally unrelated events (concurrent events at the same logical time).
//
// A cycle cannot occur in a DAG built through Add because content-addressed
// EventIDs make mutual reference cryptographically impossible, but the check
// is retained as a defensive invariant assertion.
//
// Returns an error only if a cycle is detected (which indicates internal corruption).
func (d *DAG) TopologicalSort() ([]*event.Event, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Kahn's algorithm: track in-degree (parent count) for each event.
	// Start by processing events whose in-degree is 0 (genesis events),
	// then iteratively "remove" them and decrement their children's in-degrees.
	inDegree := make(map[event.EventID]int, len(d.events))
	// safe: building per-key inDegree counts; assignment commutes across keys
	for id, e := range d.events {
		inDegree[id] = len(e.CausalRefs)
	}

	queue := make([]event.EventID, 0)
	// safe: queue ordering does not affect output; the final sort.Slice below
	// imposes a deterministic (CausalTimestamp, EventID) total order on result
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	result := make([]*event.Event, 0, len(d.events))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		result = append(result, d.events[cur])

		// safe: in-degree decrement is commutative; final sort.Slice below
		// imposes the canonical (CausalTimestamp, EventID) total order
		for childID := range d.children[cur] {
			inDegree[childID]--
			if inDegree[childID] == 0 {
				queue = append(queue, childID)
			}
		}
	}

	if len(result) != len(d.events) {
		return nil, errors.New("dag: cycle detected during topological sort — DAG invariant violated")
	}

	// Sort by (CausalTimestamp, EventID) for a deterministic total order.
	// See function-level comment for the proof that this preserves causal ordering.
	sort.Slice(result, func(i, j int) bool {
		ti, tj := result[i].CausalTimestamp, result[j].CausalTimestamp
		if ti != tj {
			return ti < tj
		}
		return result[i].ID < result[j].ID
	})

	return result, nil
}
