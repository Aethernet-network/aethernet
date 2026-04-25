package derivation

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// fakeAnchorReaderForDepth is a minimal AnchorReader that lets tests
// construct exact DAG topologies without going through dag.Add (which
// requires signatures + crypto). Each event is a stub with given ID +
// CausalRefs + Type.
type fakeAnchorReaderForDepth struct {
	events map[event.EventID]*event.Event
	// inScope: set of EventIDs that ARE canonical ancestors of the
	// test's anchor. Members satisfy IsAncestor(member, anchor) == true.
	inScope map[event.EventID]bool
	// anchorID is the canonical anchor against which IsAncestor is
	// evaluated (the anchor argument passed to ReadAtAnchor).
	anchorID event.EventID
}

func (f *fakeAnchorReaderForDepth) Get(id event.EventID) (*event.Event, error) {
	if ev, ok := f.events[id]; ok {
		return ev, nil
	}
	return nil, dag.ErrEventNotFound
}

func (f *fakeAnchorReaderForDepth) IsAncestor(ancestor, descendant event.EventID) (bool, error) {
	// Tests use IsAncestor only for the anchor-scoping predicate inside
	// ReadAtAnchor. We answer YES iff `ancestor` is in the inScope set
	// (and we expect descendant == anchorID for that path).
	if _, ok := f.events[ancestor]; !ok {
		return false, dag.ErrEventNotFound
	}
	if _, ok := f.events[descendant]; !ok {
		return false, dag.ErrEventNotFound
	}
	if descendant != f.anchorID {
		// Defensive — the production caller only invokes IsAncestor
		// with descendant == anchor. If a future test exercises a
		// different shape, the fake should be extended.
		return false, nil
	}
	return f.inScope[ancestor], nil
}

func (f *fakeAnchorReaderForDepth) CountAncestorsByType(_ event.EventID, _ event.EventType) (uint64, error) {
	return 0, nil
}

// TestReadAtAnchor_DepthIsAnchorScopedNotShortestPath is the regression
// test for multi-AI Fix #1 (2026-04-25). Verifies that the depth
// returned by ReadAtAnchor is the FIRST-ENCOUNTER DEPTH FROM THE
// ANCHOR-SCOPED BFS — NOT the global shortest path from root over all
// CausalRefs.
//
// Topology under test:
//
//	      root  (the seed; in-scope)
//	      / \
//	     A   C   (A is in-scope; C is out-of-scope)
//	     |   |
//	     B   |   (B is in-scope; C goes directly to X)
//	     |   |
//	      \ /
//	       X    (X is in-scope; reachable via two paths)
//
// Anchor-scoped paths to X: root → A → B → X (depth 3, in-scope only).
// Out-of-scope path to X: root → C → X (depth 2, but C fails the
// IsAncestor(C, anchor) predicate so this path is filtered).
//
// Pre-Fix #1: a separate computeDepths helper ran unrestricted BFS and
// reported X's depth as 2 (via root → C → X), even though
// ReadAtAnchor's anchor-scoped membership only included X via the
// length-3 in-scope path. Gen-ledger weight = quality / 2² = quality/4.
//
// Post-Fix #1: ReadAtAnchor's BFS itself produces the depth from the
// anchor-scoped traversal. X's depth is 3 (via root → A → B → X).
// Gen-ledger weight = quality / 3² = quality/9. SAME canonical state
// → SAME depth → SAME weight on every node. D-1 holds.
func TestReadAtAnchor_DepthIsAnchorScopedNotShortestPath(t *testing.T) {
	t.Parallel()

	const (
		root   = event.EventID("root")
		A      = event.EventID("A")
		B      = event.EventID("B")
		C      = event.EventID("C")
		X      = event.EventID("X")
		anchor = event.EventID("anchor")
	)

	reader := &fakeAnchorReaderForDepth{
		anchorID: anchor,
		events: map[event.EventID]*event.Event{
			root:   {ID: root, CausalRefs: []event.EventID{A, C}},
			A:      {ID: A, CausalRefs: []event.EventID{B}},
			B:      {ID: B, CausalRefs: []event.EventID{X}},
			C:      {ID: C, CausalRefs: []event.EventID{X}},
			X:      {ID: X, CausalRefs: nil},
			anchor: {ID: anchor, CausalRefs: nil},
		},
		inScope: map[event.EventID]bool{
			A: true,
			B: true,
			X: true,
			// C is OUT of scope: IsAncestor(C, anchor) == false, so the
			// ReadAtAnchor BFS never follows root → C and never
			// discovers X via the length-2 out-of-scope path.
		},
	}

	ancestors, err := ReadAtAnchor(reader, anchor, root, 5)
	if err != nil {
		t.Fatalf("ReadAtAnchor: %v", err)
	}

	// Result should include: root (depth 0), A (depth 1), B (depth 2), X (depth 3).
	// C is excluded by the anchor predicate.
	want := map[event.EventID]int{
		root: 0,
		A:    1,
		B:    2,
		X:    3,
	}

	if len(ancestors) != len(want) {
		t.Fatalf("ancestor count = %d, want %d (members: %+v)", len(ancestors), len(want), ancestors)
	}

	for _, anc := range ancestors {
		expected, ok := want[anc.EventID]
		if !ok {
			t.Errorf("unexpected ancestor in result: %s (out-of-scope path leaked into anchor-scoped BFS)", anc.EventID)
			continue
		}
		if anc.Depth != expected {
			t.Errorf("depth mismatch for %s: got %d, want %d (anchor-scoped path; out-of-scope shorter path must NOT be used)",
				anc.EventID, anc.Depth, expected)
		}
	}

	// Specifically assert X's depth — the load-bearing canonicality
	// property: X gets the in-scope-path depth 3, NOT the out-of-scope
	// shorter-path depth 2.
	var xDepth int
	for _, anc := range ancestors {
		if anc.EventID == X {
			xDepth = anc.Depth
			break
		}
	}
	if xDepth != 3 {
		t.Fatalf("X depth = %d, want 3 (anchor-scoped via root→A→B→X). Pre-Fix #1 bug would report %d (out-of-scope via root→C→X).", xDepth, 2)
	}
}

// TestReadAtAnchor_NilAnchorBypassesScopePredicate verifies the Fix A
// pre-activation case: when anchor is empty, the anchor-scoped predicate
// is skipped and depth-bounded BFS proceeds unrestricted. Depth is
// the BFS-tracked depth (still canonical because depth + membership
// come from the same traversal).
func TestReadAtAnchor_NilAnchorBypassesScopePredicate(t *testing.T) {
	t.Parallel()

	const (
		root = event.EventID("root")
		A    = event.EventID("A")
		B    = event.EventID("B")
	)

	reader := &fakeAnchorReaderForDepth{
		anchorID: "", // nil anchor case
		events: map[event.EventID]*event.Event{
			root: {ID: root, CausalRefs: []event.EventID{A}},
			A:    {ID: A, CausalRefs: []event.EventID{B}},
			B:    {ID: B, CausalRefs: nil},
		},
		inScope: nil, // not consulted when anchor is empty
	}

	ancestors, err := ReadAtAnchor(reader, "", root, 5)
	if err != nil {
		t.Fatalf("ReadAtAnchor: %v", err)
	}

	want := map[event.EventID]int{
		root: 0,
		A:    1,
		B:    2,
	}
	if len(ancestors) != 3 {
		t.Fatalf("ancestor count = %d, want 3", len(ancestors))
	}
	for _, anc := range ancestors {
		if want[anc.EventID] != anc.Depth {
			t.Errorf("depth mismatch for %s: got %d, want %d", anc.EventID, anc.Depth, want[anc.EventID])
		}
	}
}

// TestReadAtAnchor_DepthCappedAtMaxDepth verifies the depth bound. An
// ancestor beyond maxDepth is excluded from the result; reachable
// ancestors at exactly maxDepth ARE included with depth=maxDepth.
func TestReadAtAnchor_DepthCappedAtMaxDepth(t *testing.T) {
	t.Parallel()

	const (
		root = event.EventID("root")
		A    = event.EventID("A")
		B    = event.EventID("B")
		C    = event.EventID("C")
		D    = event.EventID("D")
	)

	reader := &fakeAnchorReaderForDepth{
		anchorID: "",
		events: map[event.EventID]*event.Event{
			root: {ID: root, CausalRefs: []event.EventID{A}},
			A:    {ID: A, CausalRefs: []event.EventID{B}},
			B:    {ID: B, CausalRefs: []event.EventID{C}},
			C:    {ID: C, CausalRefs: []event.EventID{D}},
			D:    {ID: D, CausalRefs: nil},
		},
	}

	ancestors, err := ReadAtAnchor(reader, "", root, 2)
	if err != nil {
		t.Fatalf("ReadAtAnchor: %v", err)
	}

	// maxDepth=2: include root (0), A (1), B (2). C (3) and D (4) excluded.
	if len(ancestors) != 3 {
		t.Fatalf("count = %d, want 3 (root, A, B); got %+v", len(ancestors), ancestors)
	}
	for _, anc := range ancestors {
		if anc.Depth > 2 {
			t.Errorf("ancestor %s has depth %d > maxDepth=2", anc.EventID, anc.Depth)
		}
	}
}

// TestReadAtAnchor_AllOrDeferOnMissingEvent verifies the all-or-defer
// semantic: a missing intermediate ancestor surfaces as ErrEventNotFound
// (caller defers per Plan v3 §2.3 step 6).
func TestReadAtAnchor_AllOrDeferOnMissingEvent(t *testing.T) {
	t.Parallel()

	const (
		root = event.EventID("root")
		A    = event.EventID("A") // not in DAG
	)

	reader := &fakeAnchorReaderForDepth{
		anchorID: "",
		events: map[event.EventID]*event.Event{
			root: {ID: root, CausalRefs: []event.EventID{A}},
			// A intentionally missing — simulates materialization lag.
		},
	}

	_, err := ReadAtAnchor(reader, "", root, 5)
	if !errors.Is(err, dag.ErrEventNotFound) {
		t.Fatalf("expected ErrEventNotFound for missing intermediate ancestor, got %v", err)
	}
}
