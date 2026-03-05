package dag_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// makeGenesis creates a genesis event (no causal references) for the given agent.
// Genesis events are unsigned; the DAG allows this for bootstrap.
func makeGenesis(t *testing.T, agentID string) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeTransfer,
		nil,
		event.TransferPayload{FromAgent: agentID, ToAgent: "sink", Amount: 1, Currency: "AET"},
		agentID,
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("makeGenesis(%q): %v", agentID, err)
	}
	return e
}

// makeChild creates a signed event that references all provided parents.
// priorTimestamps is built automatically from the parents' CausalTimestamps.
// A fresh keypair is generated for signing since non-genesis events must be signed.
func makeChild(t *testing.T, agentID string, parents ...*event.Event) *event.Event {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("makeChild: GenerateKeyPair: %v", err)
	}
	aid := string(kp.AgentID())

	refs := make([]event.EventID, len(parents))
	prior := make(map[event.EventID]uint64, len(parents))
	for i, p := range parents {
		refs[i] = p.ID
		prior[p.ID] = p.CausalTimestamp
	}
	e, err := event.New(
		event.EventTypeTransfer,
		refs,
		event.TransferPayload{FromAgent: aid, ToAgent: "sink", Amount: 1, Currency: "AET"},
		aid,
		prior,
		0,
	)
	if err != nil {
		t.Fatalf("makeChild(%q): %v", agentID, err)
	}
	if err := crypto.SignEvent(e, kp); err != nil {
		t.Fatalf("makeChild(%q) sign: %v", agentID, err)
	}
	return e
}

// mustAdd adds an event to d and fails the test on error.
func mustAdd(t *testing.T, d *dag.DAG, e *event.Event) {
	t.Helper()
	if err := d.Add(e); err != nil {
		t.Fatalf("Add(%s): unexpected error: %v", e.ID, err)
	}
}

// assertTips verifies that d.Tips() contains exactly the expected IDs (order-independent).
func assertTips(t *testing.T, d *dag.DAG, want ...event.EventID) {
	t.Helper()
	got := d.Tips()

	if len(got) != len(want) {
		t.Errorf("Tips() len = %d, want %d\n  got:  %v\n  want: %v", len(got), len(want), got, want)
		return
	}
	wantSet := make(map[event.EventID]struct{}, len(want))
	for _, id := range want {
		wantSet[id] = struct{}{}
	}
	for _, id := range got {
		if _, ok := wantSet[id]; !ok {
			t.Errorf("Tips() contains unexpected ID %s", id)
		}
	}
}

// assertAncestors verifies that d.Ancestors(id) contains exactly the expected IDs
// (order-independent, and that id itself is not included).
func assertAncestors(t *testing.T, d *dag.DAG, id event.EventID, want ...event.EventID) {
	t.Helper()
	got, err := d.Ancestors(id)
	if err != nil {
		t.Fatalf("Ancestors(%s): unexpected error: %v", id, err)
	}
	if len(got) != len(want) {
		t.Errorf("Ancestors(%s) len = %d, want %d\n  got:  %v\n  want: %v",
			id, len(got), len(want), got, want)
		return
	}
	wantSet := make(map[event.EventID]struct{}, len(want))
	for _, w := range want {
		wantSet[w] = struct{}{}
	}
	for _, g := range got {
		if g == id {
			t.Errorf("Ancestors(%s): result includes the event itself", id)
		}
		if _, ok := wantSet[g]; !ok {
			t.Errorf("Ancestors(%s): unexpected ID %s in result", id, g)
		}
	}
}

// assertTopologicalOrder verifies that for every event at position i in the slice,
// all of its CausalRefs appear at positions less than i.
func assertTopologicalOrder(t *testing.T, events []*event.Event) {
	t.Helper()
	pos := make(map[event.EventID]int, len(events))
	for i, e := range events {
		pos[e.ID] = i
	}
	for i, e := range events {
		for _, ref := range e.CausalRefs {
			refPos, ok := pos[ref]
			if !ok {
				t.Errorf("event %s at index %d references %s which is not in the result", e.ID, i, ref)
				continue
			}
			if refPos >= i {
				t.Errorf("causal violation: event %s at index %d references %s at index %d (want refPos < %d)",
					e.ID, i, ref, refPos, i)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Empty DAG
// ---------------------------------------------------------------------------

func TestNew_EmptyDAG(t *testing.T) {
	d := dag.New()

	if got := d.Size(); got != 0 {
		t.Errorf("new DAG Size = %d, want 0", got)
	}
	if tips := d.Tips(); len(tips) != 0 {
		t.Errorf("new DAG Tips len = %d, want 0", len(tips))
	}
}

func TestTopologicalSort_EmptyDAG(t *testing.T) {
	d := dag.New()
	got, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort on empty DAG: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("TopologicalSort on empty DAG: len = %d, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// Adding genesis events
// ---------------------------------------------------------------------------

func TestAdd_SingleGenesisEvent(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")

	mustAdd(t, d, g)

	if d.Size() != 1 {
		t.Errorf("Size = %d, want 1", d.Size())
	}
	assertTips(t, d, g.ID)
}

func TestAdd_TwoIndependentGenesisEvents(t *testing.T) {
	d := dag.New()
	g1 := makeGenesis(t, "agent-a")
	g2 := makeGenesis(t, "agent-b")

	mustAdd(t, d, g1)
	mustAdd(t, d, g2)

	if d.Size() != 2 {
		t.Errorf("Size = %d, want 2", d.Size())
	}
	// Both genesis events are tips — neither references the other.
	assertTips(t, d, g1.ID, g2.ID)
}

// ---------------------------------------------------------------------------
// Adding events with causal references
// ---------------------------------------------------------------------------

func TestAdd_EventWithValidRef(t *testing.T) {
	d := dag.New()
	parent := makeGenesis(t, "agent-a")
	child := makeChild(t, "agent-b", parent)

	mustAdd(t, d, parent)
	mustAdd(t, d, child)

	if d.Size() != 2 {
		t.Errorf("Size = %d, want 2", d.Size())
	}
	// parent is referenced — it leaves the frontier. child has no children — it is the only tip.
	assertTips(t, d, child.ID)
}

func TestAdd_ChainOfThree(t *testing.T) {
	d := dag.New()
	a := makeGenesis(t, "agent-a")
	b := makeChild(t, "agent-b", a)
	c := makeChild(t, "agent-c", b)

	mustAdd(t, d, a)
	mustAdd(t, d, b)
	mustAdd(t, d, c)

	if d.Size() != 3 {
		t.Errorf("Size = %d, want 3", d.Size())
	}
	// Only the tail of the chain is a tip.
	assertTips(t, d, c.ID)
}

// ---------------------------------------------------------------------------
// Rejecting invalid events
// ---------------------------------------------------------------------------

func TestAdd_RejectsUnknownCausalRef(t *testing.T) {
	d := dag.New()

	// Craft an event that references an ID not in the DAG.
	fakeID := event.EventID(strings.Repeat("a", 64))
	prior := map[event.EventID]uint64{fakeID: 1}
	e, err := event.New(
		event.EventTypeTransfer,
		[]event.EventID{fakeID},
		event.TransferPayload{FromAgent: "x", ToAgent: "y", Amount: 1, Currency: "AET"},
		"agent-x",
		prior,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	err = d.Add(e)
	if err == nil {
		t.Fatal("Add should have returned an error for an unknown causal ref, got nil")
	}
	if !errors.Is(err, dag.ErrMissingCausalRef) {
		t.Errorf("error type: got %v, want errors.Is(err, ErrMissingCausalRef) == true", err)
	}

	// DAG must be unchanged.
	if d.Size() != 0 {
		t.Errorf("DAG size after rejection = %d, want 0", d.Size())
	}
}

func TestAdd_RejectsDuplicate(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")

	mustAdd(t, d, g)

	err := d.Add(g)
	if err == nil {
		t.Fatal("second Add of same event should return an error, got nil")
	}
	if !errors.Is(err, dag.ErrDuplicateEvent) {
		t.Errorf("error type: got %v, want errors.Is(err, ErrDuplicateEvent) == true", err)
	}
	// Size must not change.
	if d.Size() != 1 {
		t.Errorf("Size after duplicate Add = %d, want 1", d.Size())
	}
}

func TestAdd_RejectsRefToEventNotYetAdded(t *testing.T) {
	// Attempting to reference an event that exists but has not been added to *this*
	// DAG instance must fail. Nodes cannot assume the network has delivered all events.
	d := dag.New()
	parent := makeGenesis(t, "agent-a")
	child := makeChild(t, "agent-b", parent)

	// Add child before parent — this violates causal ordering.
	err := d.Add(child)
	if err == nil {
		t.Fatal("Add should fail when ref is not yet in DAG, got nil")
	}
	if !errors.Is(err, dag.ErrMissingCausalRef) {
		t.Errorf("want ErrMissingCausalRef, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestGet_Found(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")
	mustAdd(t, d, g)

	got, err := d.Get(g.ID)
	if err != nil {
		t.Fatalf("Get(%s): unexpected error: %v", g.ID, err)
	}
	if got.ID != g.ID {
		t.Errorf("Get returned event with ID %s, want %s", got.ID, g.ID)
	}
	if got.AgentID != g.AgentID {
		t.Errorf("Get returned event with AgentID %q, want %q", got.AgentID, g.AgentID)
	}
}

func TestGet_NotFound(t *testing.T) {
	d := dag.New()
	_, err := d.Get(event.EventID(strings.Repeat("b", 64)))
	if err == nil {
		t.Fatal("Get on absent ID should return error, got nil")
	}
	if !errors.Is(err, dag.ErrEventNotFound) {
		t.Errorf("want ErrEventNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tip tracking
// ---------------------------------------------------------------------------

func TestTips_LinearChain(t *testing.T) {
	d := dag.New()
	a := makeGenesis(t, "a")
	b := makeChild(t, "b", a)
	c := makeChild(t, "c", b)

	mustAdd(t, d, a)
	assertTips(t, d, a.ID) // only a so far

	mustAdd(t, d, b)
	assertTips(t, d, b.ID) // a is referenced, b takes over

	mustAdd(t, d, c)
	assertTips(t, d, c.ID) // b is referenced, c takes over
}

func TestTips_Fork(t *testing.T) {
	// genesis → left
	//         → right
	// Both left and right are tips after genesis is referenced by both.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	left := makeChild(t, "left", genesis)
	right := makeChild(t, "right", genesis)

	mustAdd(t, d, genesis)
	mustAdd(t, d, left)
	mustAdd(t, d, right)

	assertTips(t, d, left.ID, right.ID)
}

func TestTips_Merge(t *testing.T) {
	// genesis → left  ──┐
	//         → right ──┴→ merge
	// After merge is added, only merge is a tip.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	left := makeChild(t, "left", genesis)
	right := makeChild(t, "right", genesis)
	merge := makeChild(t, "merge", left, right)

	mustAdd(t, d, genesis)
	mustAdd(t, d, left)
	mustAdd(t, d, right)
	mustAdd(t, d, merge)

	assertTips(t, d, merge.ID)
}

func TestTips_MultipleIndependentChains(t *testing.T) {
	// Two fully independent chains — four tips total at the end.
	// chain1: g1 → a1 → b1
	// chain2: g2 → a2 → b2
	d := dag.New()
	g1, g2 := makeGenesis(t, "g1"), makeGenesis(t, "g2")
	a1 := makeChild(t, "a1", g1)
	a2 := makeChild(t, "a2", g2)
	b1 := makeChild(t, "b1", a1)
	b2 := makeChild(t, "b2", a2)

	for _, e := range []*event.Event{g1, g2, a1, a2, b1, b2} {
		mustAdd(t, d, e)
	}
	assertTips(t, d, b1.ID, b2.ID)
}

// ---------------------------------------------------------------------------
// Size
// ---------------------------------------------------------------------------

func TestSize_GrowsWithEachAdd(t *testing.T) {
	d := dag.New()

	for i := 1; i <= 5; i++ {
		g := makeGenesis(t, fmt.Sprintf("agent-%d", i))
		mustAdd(t, d, g)
		if d.Size() != i {
			t.Errorf("after %d adds: Size = %d, want %d", i, d.Size(), i)
		}
	}
}

// ---------------------------------------------------------------------------
// Ancestor traversal
// ---------------------------------------------------------------------------

func TestAncestors_GenesisHasNone(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")
	mustAdd(t, d, g)

	assertAncestors(t, d, g.ID /* want: none */)
}

func TestAncestors_DirectParent(t *testing.T) {
	d := dag.New()
	parent := makeGenesis(t, "p")
	child := makeChild(t, "c", parent)
	mustAdd(t, d, parent)
	mustAdd(t, d, child)

	assertAncestors(t, d, child.ID, parent.ID)
}

func TestAncestors_TransitiveClosure_LinearChain(t *testing.T) {
	// A → B → C → D
	// Ancestors of D = {A, B, C}
	d := dag.New()
	a := makeGenesis(t, "a")
	b := makeChild(t, "b", a)
	c := makeChild(t, "c", b)
	ev := makeChild(t, "d", c)

	for _, e := range []*event.Event{a, b, c, ev} {
		mustAdd(t, d, e)
	}
	assertAncestors(t, d, ev.ID, a.ID, b.ID, c.ID)
}

func TestAncestors_DiamondGraph(t *testing.T) {
	//       A
	//      / \
	//     B   C
	//      \ /
	//       D
	// Ancestors of D = {A, B, C}. A must appear only once (shared ancestor).
	d := dag.New()
	a := makeGenesis(t, "a")
	b := makeChild(t, "b", a)
	c := makeChild(t, "c", a)
	dv := makeChild(t, "d", b, c)

	for _, e := range []*event.Event{a, b, c, dv} {
		mustAdd(t, d, e)
	}
	assertAncestors(t, d, dv.ID, a.ID, b.ID, c.ID)
}

func TestAncestors_DoesNotIncludeSelf(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")
	mustAdd(t, d, g)

	ancestors, err := d.Ancestors(g.ID)
	if err != nil {
		t.Fatalf("Ancestors: %v", err)
	}
	for _, id := range ancestors {
		if id == g.ID {
			t.Errorf("Ancestors result includes the queried event itself")
		}
	}
}

func TestAncestors_NotFound(t *testing.T) {
	d := dag.New()
	_, err := d.Ancestors(event.EventID(strings.Repeat("c", 64)))
	if err == nil {
		t.Fatal("Ancestors on absent ID should error, got nil")
	}
	if !errors.Is(err, dag.ErrEventNotFound) {
		t.Errorf("want ErrEventNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// IsAncestor
// ---------------------------------------------------------------------------

func TestIsAncestor_True_DirectParent(t *testing.T) {
	d := dag.New()
	parent := makeGenesis(t, "p")
	child := makeChild(t, "c", parent)
	mustAdd(t, d, parent)
	mustAdd(t, d, child)

	ok, err := d.IsAncestor(parent.ID, child.ID)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if !ok {
		t.Error("IsAncestor(parent, child) = false, want true")
	}
}

func TestIsAncestor_True_Transitive(t *testing.T) {
	// A → B → C: A is a transitive ancestor of C.
	d := dag.New()
	a := makeGenesis(t, "a")
	b := makeChild(t, "b", a)
	c := makeChild(t, "c", b)
	for _, e := range []*event.Event{a, b, c} {
		mustAdd(t, d, e)
	}

	ok, err := d.IsAncestor(a.ID, c.ID)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if !ok {
		t.Error("IsAncestor(A, C) = false, want true for A→B→C")
	}
}

func TestIsAncestor_False_UnrelatedEvents(t *testing.T) {
	// Two independent genesis events — neither is an ancestor of the other.
	d := dag.New()
	g1 := makeGenesis(t, "g1")
	g2 := makeGenesis(t, "g2")
	mustAdd(t, d, g1)
	mustAdd(t, d, g2)

	ok, err := d.IsAncestor(g1.ID, g2.ID)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if ok {
		t.Error("IsAncestor(g1, g2) = true for unrelated events, want false")
	}
}

func TestIsAncestor_False_ReverseDirection(t *testing.T) {
	// child is not an ancestor of parent — the direction is reversed.
	d := dag.New()
	parent := makeGenesis(t, "p")
	child := makeChild(t, "c", parent)
	mustAdd(t, d, parent)
	mustAdd(t, d, child)

	ok, err := d.IsAncestor(child.ID, parent.ID)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if ok {
		t.Error("IsAncestor(child, parent) = true, want false (reversed direction)")
	}
}

func TestIsAncestor_False_Self(t *testing.T) {
	// An event is not a strict ancestor of itself.
	d := dag.New()
	g := makeGenesis(t, "agent-a")
	mustAdd(t, d, g)

	ok, err := d.IsAncestor(g.ID, g.ID)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if ok {
		t.Error("IsAncestor(g, g) = true, want false (strict / irreflexive)")
	}
}

func TestIsAncestor_False_SiblingBranches(t *testing.T) {
	//     genesis
	//     /     \
	// branchA  branchB
	// Neither branch is an ancestor of the other.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	a := makeChild(t, "branch-a", genesis)
	b := makeChild(t, "branch-b", genesis)
	mustAdd(t, d, genesis)
	mustAdd(t, d, a)
	mustAdd(t, d, b)

	for _, tc := range []struct{ anc, desc event.EventID }{{a.ID, b.ID}, {b.ID, a.ID}} {
		ok, err := d.IsAncestor(tc.anc, tc.desc)
		if err != nil {
			t.Fatalf("IsAncestor: %v", err)
		}
		if ok {
			t.Errorf("IsAncestor(%s, %s) = true for sibling branches, want false", tc.anc, tc.desc)
		}
	}
}

func TestIsAncestor_NotFound_Ancestor(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")
	mustAdd(t, d, g)

	_, err := d.IsAncestor(event.EventID(strings.Repeat("d", 64)), g.ID)
	if !errors.Is(err, dag.ErrEventNotFound) {
		t.Errorf("want ErrEventNotFound for absent ancestor, got %v", err)
	}
}

func TestIsAncestor_NotFound_Descendant(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")
	mustAdd(t, d, g)

	_, err := d.IsAncestor(g.ID, event.EventID(strings.Repeat("e", 64)))
	if !errors.Is(err, dag.ErrEventNotFound) {
		t.Errorf("want ErrEventNotFound for absent descendant, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Topological sort
// ---------------------------------------------------------------------------

func TestTopologicalSort_SingleEvent(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "agent-a")
	mustAdd(t, d, g)

	got, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(got) != 1 || got[0].ID != g.ID {
		t.Errorf("TopologicalSort for single event: got %v, want [%s]", got, g.ID)
	}
}

func TestTopologicalSort_LinearChain(t *testing.T) {
	// A → B → C must produce [A, B, C] — exactly this order due to CausalTimestamps.
	d := dag.New()
	a := makeGenesis(t, "a")
	b := makeChild(t, "b", a)
	c := makeChild(t, "c", b)
	for _, e := range []*event.Event{a, b, c} {
		mustAdd(t, d, e)
	}

	got, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("TopologicalSort len = %d, want 3", len(got))
	}
	assertTopologicalOrder(t, got)
	// For a linear chain, the order is fully determined by CausalTimestamps.
	if got[0].ID != a.ID {
		t.Errorf("position 0: got %s, want %s (A)", got[0].ID, a.ID)
	}
	if got[1].ID != b.ID {
		t.Errorf("position 1: got %s, want %s (B)", got[1].ID, b.ID)
	}
	if got[2].ID != c.ID {
		t.Errorf("position 2: got %s, want %s (C)", got[2].ID, c.ID)
	}
}

func TestTopologicalSort_ValidOrdering_LinearChain(t *testing.T) {
	d := dag.New()
	prev := makeGenesis(t, "agent-0")
	mustAdd(t, d, prev)
	for i := 1; i < 10; i++ {
		cur := makeChild(t, fmt.Sprintf("agent-%d", i), prev)
		mustAdd(t, d, cur)
		prev = cur
	}

	got, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	assertTopologicalOrder(t, got)
}

func TestTopologicalSort_Fork(t *testing.T) {
	// genesis → left
	//         → right
	// genesis must appear before both left and right.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	left := makeChild(t, "left", genesis)
	right := makeChild(t, "right", genesis)
	for _, e := range []*event.Event{genesis, left, right} {
		mustAdd(t, d, e)
	}

	got, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	assertTopologicalOrder(t, got)
	if got[0].ID != genesis.ID {
		t.Errorf("genesis must be first in sort, got %s", got[0].ID)
	}
}

func TestTopologicalSort_ForkMerge(t *testing.T) {
	//       genesis (ts=1)
	//       /       \
	//   left (ts=2)  right (ts=2)
	//       \       /
	//        merge (ts=3)
	//
	// Valid sort: genesis, left or right (either order), merge — in that sequence.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	left := makeChild(t, "left", genesis)
	right := makeChild(t, "right", genesis)
	merge := makeChild(t, "merge", left, right)

	for _, e := range []*event.Event{genesis, left, right, merge} {
		mustAdd(t, d, e)
	}

	got, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("TopologicalSort len = %d, want 4", len(got))
	}

	assertTopologicalOrder(t, got)

	if got[0].ID != genesis.ID {
		t.Errorf("genesis must be first; got %s", got[0].ID)
	}
	if got[3].ID != merge.ID {
		t.Errorf("merge must be last; got %s", got[3].ID)
	}
}

func TestTopologicalSort_Deterministic(t *testing.T) {
	// Same DAG state must produce identical slices across multiple calls.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	left := makeChild(t, "left", genesis)
	right := makeChild(t, "right", genesis)
	merge := makeChild(t, "merge", left, right)
	for _, e := range []*event.Event{genesis, left, right, merge} {
		mustAdd(t, d, e)
	}

	first, _ := d.TopologicalSort()
	second, _ := d.TopologicalSort()

	if len(first) != len(second) {
		t.Fatalf("TopologicalSort returned different lengths: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("position %d: first call = %s, second call = %s", i, first[i].ID, second[i].ID)
		}
	}
}

func TestTopologicalSort_TwoIndependentChains(t *testing.T) {
	// chain1: g1 → a1 (ts 1,2)
	// chain2: g2 → a2 (ts 1,2)
	// Both genesis events have ts=1 and will be sorted by EventID.
	// Both a1/a2 events have ts=2 and will also be sorted by EventID.
	d := dag.New()
	g1 := makeGenesis(t, "g1-agent")
	g2 := makeGenesis(t, "g2-agent")
	a1 := makeChild(t, "a1-agent", g1)
	a2 := makeChild(t, "a2-agent", g2)
	for _, e := range []*event.Event{g1, g2, a1, a2} {
		mustAdd(t, d, e)
	}

	got, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	assertTopologicalOrder(t, got)
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestConcurrent_AddGenesisEvents(t *testing.T) {
	// Spawn N goroutines that each add a unique genesis event simultaneously.
	// With go test -race, this will catch any missing synchronisation.
	const n = 100
	d := dag.New()

	events := make([]*event.Event, n)
	for i := 0; i < n; i++ {
		events[i] = makeGenesis(t, fmt.Sprintf("concurrent-agent-%d", i))
	}

	// Use a channel to release all goroutines at approximately the same time,
	// maximising the overlap of concurrent Add calls.
	ready := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-ready
			errs[i] = d.Add(events[i])
		}()
	}
	close(ready)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected Add error: %v", i, err)
		}
	}
	if got := d.Size(); got != n {
		t.Errorf("Size = %d, want %d after %d concurrent adds", got, n, n)
	}
	if tips := d.Tips(); len(tips) != n {
		t.Errorf("Tips len = %d, want %d (all genesis events)", len(tips), n)
	}
}

func TestConcurrent_ReadsDuringWrites(t *testing.T) {
	// Writers add genesis events while readers concurrently call Tips() and Size().
	// With go test -race, any data race in the RWMutex usage will be caught.
	const n = 50
	d := dag.New()

	ready := make(chan struct{})
	var wg sync.WaitGroup

	// Writers
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-ready
			e := makeGenesis(t, fmt.Sprintf("writer-agent-%d", i))
			_ = d.Add(e)
		}()
	}

	// Readers
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-ready
			_ = d.Size()
			_ = d.Tips()
		}()
	}

	close(ready)
	wg.Wait()
	// The exact final size may be any value 0..n due to scheduling, but should be n.
	if got := d.Size(); got != n {
		t.Errorf("final Size = %d, want %d", got, n)
	}
}

func TestConcurrent_DuplicateAddIsIdempotent(t *testing.T) {
	// Multiple goroutines trying to add the same event concurrently:
	// exactly one should succeed; all others should get ErrDuplicateEvent.
	const n = 20
	d := dag.New()
	g := makeGenesis(t, "shared-agent")

	ready := make(chan struct{})
	results := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-ready
			results[i] = d.Add(g)
		}()
	}
	close(ready)
	wg.Wait()

	var successCount, dupCount int
	for _, err := range results {
		if err == nil {
			successCount++
		} else if errors.Is(err, dag.ErrDuplicateEvent) {
			dupCount++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successCount != 1 {
		t.Errorf("exactly 1 Add should succeed, got %d successes", successCount)
	}
	if dupCount != n-1 {
		t.Errorf("exactly %d duplicate errors expected, got %d", n-1, dupCount)
	}
	if d.Size() != 1 {
		t.Errorf("Size = %d, want 1", d.Size())
	}
}

// ---------------------------------------------------------------------------
// Fork and merge scenario — comprehensive integration test
// ---------------------------------------------------------------------------

func TestForkMerge_CompleteScenario(t *testing.T) {
	// Full diamond fork-merge:
	//
	//       genesis (A, ts=1)
	//          |
	//     ┌────┴────┐
	//  left (B)  right (C)    ts=2 each
	//     └────┬────┘
	//        merge (D, ts=3)
	//           |
	//       tail (E, ts=4)
	//
	// This tests tips, ancestors, IsAncestor, and topological ordering
	// across a graph with both parallel branches and a convergence point.

	d := dag.New()
	A := makeGenesis(t, "root")
	B := makeChild(t, "left", A)
	C := makeChild(t, "right", A)
	D := makeChild(t, "merge", B, C)
	E := makeChild(t, "tail", D)

	for _, e := range []*event.Event{A, B, C, D, E} {
		mustAdd(t, d, e)
	}

	// --- Size ---
	if d.Size() != 5 {
		t.Errorf("Size = %d, want 5", d.Size())
	}

	// --- Tips ---
	// Only E is a tip; A, B, C, D are all referenced by later events.
	assertTips(t, d, E.ID)

	// --- CausalTimestamps ---
	if A.CausalTimestamp != 1 {
		t.Errorf("A.CausalTimestamp = %d, want 1", A.CausalTimestamp)
	}
	if B.CausalTimestamp != 2 || C.CausalTimestamp != 2 {
		t.Errorf("B/C CausalTimestamp = %d/%d, want 2/2", B.CausalTimestamp, C.CausalTimestamp)
	}
	if D.CausalTimestamp != 3 {
		t.Errorf("D.CausalTimestamp = %d, want 3", D.CausalTimestamp)
	}
	if E.CausalTimestamp != 4 {
		t.Errorf("E.CausalTimestamp = %d, want 4", E.CausalTimestamp)
	}

	// --- Ancestors ---
	assertAncestors(t, d, A.ID /* none */)
	assertAncestors(t, d, B.ID, A.ID)
	assertAncestors(t, d, C.ID, A.ID)
	assertAncestors(t, d, D.ID, A.ID, B.ID, C.ID)
	assertAncestors(t, d, E.ID, A.ID, B.ID, C.ID, D.ID)

	// --- IsAncestor: true cases ---
	for _, tc := range []struct{ anc, desc string }{
		{"A→B", "A is ancestor of B"}, {"A→C", ""}, {"A→D", ""}, {"A→E", ""},
		{"B→D", "B is ancestor of D"}, {"C→D", "C is ancestor of D"},
		{"B→E", "B is ancestor of E via D"}, {"D→E", "D is ancestor of E"},
	} {
		_ = tc // descriptive only; actual checks below
	}
	trueChecks := [][2]*event.Event{
		{A, B}, {A, C}, {A, D}, {A, E},
		{B, D}, {C, D}, {B, E}, {C, E}, {D, E},
	}
	for _, tc := range trueChecks {
		ok, err := d.IsAncestor(tc[0].ID, tc[1].ID)
		if err != nil {
			t.Errorf("IsAncestor(%s, %s): %v", tc[0].AgentID, tc[1].AgentID, err)
			continue
		}
		if !ok {
			t.Errorf("IsAncestor(%s, %s) = false, want true", tc[0].AgentID, tc[1].AgentID)
		}
	}

	// --- IsAncestor: false cases ---
	falseChecks := [][2]*event.Event{
		{B, A}, {C, A}, {D, A}, {E, A}, // reversed
		{D, B}, {D, C}, {E, B},          // reversed
		{B, C}, {C, B},                  // independent branches
		{A, A}, {B, B}, {E, E},          // self
	}
	for _, tc := range falseChecks {
		ok, err := d.IsAncestor(tc[0].ID, tc[1].ID)
		if err != nil {
			t.Errorf("IsAncestor(%s, %s): %v", tc[0].AgentID, tc[1].AgentID, err)
			continue
		}
		if ok {
			t.Errorf("IsAncestor(%s, %s) = true, want false", tc[0].AgentID, tc[1].AgentID)
		}
	}

	// --- TopologicalSort ---
	sorted, err := d.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(sorted) != 5 {
		t.Fatalf("TopologicalSort len = %d, want 5", len(sorted))
	}
	assertTopologicalOrder(t, sorted)

	// A has ts=1 → must be first.
	if sorted[0].ID != A.ID {
		t.Errorf("position 0: got agent=%s, want A", sorted[0].AgentID)
	}
	// B and C have ts=2 → must occupy positions 1 and 2 (order by EventID).
	midIDs := map[event.EventID]bool{B.ID: true, C.ID: true}
	if !midIDs[sorted[1].ID] || !midIDs[sorted[2].ID] {
		t.Errorf("positions 1-2: got [%s,%s], want B and C in some order",
			sorted[1].AgentID, sorted[2].AgentID)
	}
	// D has ts=3 → must be position 3.
	if sorted[3].ID != D.ID {
		t.Errorf("position 3: got agent=%s, want D", sorted[3].AgentID)
	}
	// E has ts=4 → must be last.
	if sorted[4].ID != E.ID {
		t.Errorf("position 4: got agent=%s, want E", sorted[4].AgentID)
	}
}

// ---------------------------------------------------------------------------
// Fix 4A: Signature verification in DAG.Add
// ---------------------------------------------------------------------------

func TestAdd_UnsignedNonGenesis_Rejected(t *testing.T) {
	// A non-genesis event with no signature must be rejected.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	mustAdd(t, d, genesis)

	// Create a child event WITHOUT signing it.
	refs := []event.EventID{genesis.ID}
	prior := map[event.EventID]uint64{genesis.ID: genesis.CausalTimestamp}
	child, err := event.New(
		event.EventTypeTransfer,
		refs,
		event.TransferPayload{FromAgent: "attacker", ToAgent: "sink", Amount: 1, Currency: "AET"},
		"attacker",
		prior,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	err = d.Add(child)
	if err == nil {
		t.Fatal("Add should reject unsigned non-genesis event, got nil")
	}
	if !errors.Is(err, dag.ErrMissingSignature) {
		t.Errorf("want ErrMissingSignature, got: %v", err)
	}
}

func TestAdd_InvalidSignature_Rejected(t *testing.T) {
	// An event with a garbage signature must be rejected.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	mustAdd(t, d, genesis)

	kp, _ := crypto.GenerateKeyPair()
	aid := string(kp.AgentID())
	refs := []event.EventID{genesis.ID}
	prior := map[event.EventID]uint64{genesis.ID: genesis.CausalTimestamp}
	child, err := event.New(
		event.EventTypeTransfer,
		refs,
		event.TransferPayload{FromAgent: aid, ToAgent: "sink", Amount: 1, Currency: "AET"},
		aid,
		prior,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	// Set garbage signature instead of a valid one.
	child.Signature = make([]byte, 64)

	err = d.Add(child)
	if err == nil {
		t.Fatal("Add should reject invalid signature, got nil")
	}
	if !errors.Is(err, dag.ErrInvalidSignature) {
		t.Errorf("want ErrInvalidSignature, got: %v", err)
	}
}

func TestAdd_ValidSignature_Accepted(t *testing.T) {
	// A properly signed non-genesis event must be accepted.
	d := dag.New()
	genesis := makeGenesis(t, "root")
	mustAdd(t, d, genesis)

	child := makeChild(t, "child", genesis)
	err := d.Add(child)
	if err != nil {
		t.Fatalf("Add should accept validly signed event, got: %v", err)
	}
}

func TestAdd_GenesisUnsigned_Accepted(t *testing.T) {
	// Genesis events (no CausalRefs) are allowed unsigned for bootstrap.
	d := dag.New()
	g := makeGenesis(t, "bootstrap")
	err := d.Add(g)
	if err != nil {
		t.Fatalf("Add should accept unsigned genesis event, got: %v", err)
	}
}
