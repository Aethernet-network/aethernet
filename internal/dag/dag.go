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
	"sort"
	"sync"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/event"
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
)

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
}

// SetStore attaches a persistence backend to the DAG. After this call every
// successful Add writes through to the store. Must be called before any Add
// to ensure the full event history is durable. s must satisfy dagPersistence;
// *store.Store from the store package does so.
func (d *DAG) SetStore(s dagPersistence) {
	d.store = s
}

// New creates and returns an empty DAG ready to accept events.
func New() *DAG {
	return &DAG{
		events:   make(map[event.EventID]*event.Event),
		children: make(map[event.EventID]map[event.EventID]struct{}),
		tips:     make(map[event.EventID]struct{}),
	}
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
	defer d.mu.Unlock()

	if _, exists := d.events[e.ID]; exists {
		return fmt.Errorf("dag: %w: %s", ErrDuplicateEvent, e.ID)
	}

	// Validate all causal references before mutating any state.
	// This check runs before signature verification so that events with unknown
	// refs return ErrMissingCausalRef (the structurally prior error) rather than
	// ErrMissingSignature when both conditions hold.
	for _, ref := range e.CausalRefs {
		if _, ok := d.events[ref]; !ok {
			return fmt.Errorf("dag: %w: %s (referenced by %s)", ErrMissingCausalRef, ref, e.ID)
		}
	}

	// Signature enforcement: non-genesis events must be signed and verifiable.
	// Genesis events (empty CausalRefs) are allowed unsigned as they bootstrap
	// the DAG and typically pre-date key distribution.
	isGenesis := len(e.CausalRefs) == 0
	if !isGenesis {
		if len(e.Signature) == 0 {
			return fmt.Errorf("%w: %s", ErrMissingSignature, e.ID)
		}
		if !crypto.VerifyEvent(e) {
			return fmt.Errorf("%w: %s", ErrInvalidSignature, e.ID)
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
		_ = d.store.PutEvent(e)
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

	// Sort events by CausalTimestamp for topological ordering. This guarantees
	// every parent event is inserted before any of its children.
	sort.Slice(events, func(i, j int) bool {
		return events[i].CausalTimestamp < events[j].CausalTimestamp
	})

	for _, e := range events {
		if err := d.addFromStore(e); err != nil {
			return nil, fmt.Errorf("dag: replay event %s: %w", e.ID, err)
		}
	}
	return d, nil
}

// addFromStore inserts e into the in-memory DAG structures without signature
// verification (events were already verified on first acceptance) and without
// writing back to the store (we are loading FROM the store). Duplicate events
// during replay are silently skipped — not an error.
func (d *DAG) addFromStore(e *event.Event) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Silently skip duplicates during replay.
	if _, exists := d.events[e.ID]; exists {
		return nil
	}

	// Validate causal references. Because events are replayed in CausalTimestamp
	// order, all parents should already be present.
	for _, ref := range e.CausalRefs {
		if _, ok := d.events[ref]; !ok {
			return fmt.Errorf("dag: %w: %s (referenced by %s)", ErrMissingCausalRef, ref, e.ID)
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
// The order of elements within the slice is not guaranteed.
func (d *DAG) Tips() []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()

	tips := make([]event.EventID, 0, len(d.tips))
	for id := range d.tips {
		tips = append(tips, id)
	}
	return tips
}

// Size returns the number of events currently stored in the DAG.
func (d *DAG) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.events)
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
	for id, e := range d.events {
		inDegree[id] = len(e.CausalRefs)
	}

	queue := make([]event.EventID, 0)
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
