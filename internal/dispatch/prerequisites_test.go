package dispatch

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

func TestPrerequisiteValidation_AncestorIsValid(t *testing.T) {
	parent := makeTestEvent(t, "alice", "parent")
	child := makeTestEvent(t, "alice", "child")

	dag := &stubDAG{
		tips: []event.EventID{"tip-1"},
		ancestors: map[[2]event.EventID]bool{
			{parent.ID, child.ID}: true,
		},
		events: map[event.EventID]*event.Event{
			parent.ID: parent,
			child.ID:  child,
		},
	}
	d, _ := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test")
	c.interested = func(_ *event.Event) bool { return true }
	prereqConsumer := &prereqConsumer{
		syntheticConsumer: c,
		prereqs:          []event.EventID{parent.ID},
	}

	result, err := d.checkPrerequisites(child, []Consumer{prereqConsumer})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.allProjected {
		t.Error("expected allProjected=true; parent is in DAG")
	}
}

func TestPrerequisiteValidation_NonAncestorIsRejected(t *testing.T) {
	unrelated := makeTestEvent(t, "bob", "unrelated")
	child := makeTestEvent(t, "alice", "child")

	dag := &stubDAG{
		tips:      []event.EventID{"tip-1"},
		ancestors: map[[2]event.EventID]bool{},
		events: map[event.EventID]*event.Event{
			unrelated.ID: unrelated,
			child.ID:     child,
		},
	}
	d, _ := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test")
	prereqConsumer := &prereqConsumer{
		syntheticConsumer: c,
		prereqs:          []event.EventID{unrelated.ID},
	}

	_, err := d.checkPrerequisites(child, []Consumer{prereqConsumer})
	if !errors.Is(err, ErrPrerequisiteForgery) {
		t.Fatalf("want ErrPrerequisiteForgery, got %v", err)
	}
}

func TestPrerequisiteValidation_EmptyPrerequisites(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:   []event.EventID{"tip-1"},
		events: map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, _ := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test")
	result, err := d.checkPrerequisites(ev, []Consumer{c})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.allProjected {
		t.Error("empty prerequisites should mean allProjected=true")
	}
}

func TestPrerequisiteValidation_MissingButValid(t *testing.T) {
	parent := makeTestEvent(t, "alice", "parent")
	child := makeTestEvent(t, "alice", "child")

	// Simulate an edge case where the ancestor relationship is known
	// (IsAncestor returns true) but the event is not yet retrievable
	// from the local DAG (Get returns error). This can happen during
	// repair-path synchronization where ancestor metadata arrives
	// before the full event body.
	dag := &stubDAGWithGetOverride{
		stubDAG: stubDAG{
			tips: []event.EventID{"tip-1"},
			ancestors: map[[2]event.EventID]bool{
				{parent.ID, child.ID}: true,
			},
			events: map[event.EventID]*event.Event{
				child.ID: child,
			},
		},
		getMissing: map[event.EventID]bool{parent.ID: true},
	}
	d, _ := newTestDispatcherWithDAG(t, &dag.stubDAG)
	d.dag = dag // override with the Get-override stub

	c := newSyntheticConsumer("test")
	prereqConsumer := &prereqConsumer{
		syntheticConsumer: c,
		prereqs:          []event.EventID{parent.ID},
	}

	result, err := d.checkPrerequisites(child, []Consumer{prereqConsumer})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.allProjected {
		t.Error("expected allProjected=false; parent not retrievable via Get")
	}
	if len(result.missing) != 1 || result.missing[0] != parent.ID {
		t.Errorf("missing: got %v want [%s]", result.missing, parent.ID)
	}
}

// stubDAGWithGetOverride wraps stubDAG but returns "not found" for
// specific EventIDs in Get while still reporting them as ancestors
// via IsAncestor.
type stubDAGWithGetOverride struct {
	stubDAG
	getMissing map[event.EventID]bool
}

func (s *stubDAGWithGetOverride) Get(id event.EventID) (*event.Event, error) {
	if s.getMissing[id] {
		return nil, errors.New("not found")
	}
	return s.stubDAG.Get(id)
}

func (s *stubDAGWithGetOverride) IsAncestor(ancestor, descendant event.EventID) (bool, error) {
	return s.stubDAG.IsAncestor(ancestor, descendant)
}

func (s *stubDAGWithGetOverride) Tips() []event.EventID {
	return s.stubDAG.Tips()
}

// prereqConsumer wraps syntheticConsumer with non-empty Prerequisites.
type prereqConsumer struct {
	*syntheticConsumer
	prereqs []event.EventID
}

func (c *prereqConsumer) Prerequisites(_ *event.Event) []event.EventID {
	return c.prereqs
}
