package testnetwork

import (
	"context"
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// stubApplier records every Apply call. Used to verify the transport's
// own delivery semantics without standing up a real node.
type stubApplier struct {
	delivered []event.EventID
	failOn    map[event.EventID]error
}

func (s *stubApplier) Apply(_ context.Context, ev *event.Event) error {
	s.delivered = append(s.delivered, ev.ID)
	if err, ok := s.failOn[ev.ID]; ok {
		return err
	}
	return nil
}

func mkEvent(id string) *event.Event {
	return &event.Event{ID: event.EventID(id)}
}

func TestDeliverTo_Basic(t *testing.T) {
	tr := New()
	a := &stubApplier{}
	if err := tr.DeliverTo(context.Background(), a, mkEvent("ev-1")); err != nil {
		t.Fatalf("DeliverTo: %v", err)
	}
	if len(a.delivered) != 1 || a.delivered[0] != "ev-1" {
		t.Errorf("delivered = %v; want [ev-1]", a.delivered)
	}
}

func TestDeliverTo_NilTarget(t *testing.T) {
	tr := New()
	if err := tr.DeliverTo(context.Background(), nil, mkEvent("ev-1")); err == nil {
		t.Error("DeliverTo(nil target): want error")
	}
}

func TestDeliverTo_NilEvent(t *testing.T) {
	tr := New()
	a := &stubApplier{}
	if err := tr.DeliverTo(context.Background(), a, nil); err == nil {
		t.Error("DeliverTo(nil event): want error")
	}
}

func TestDeliverTo_PropagatesError(t *testing.T) {
	tr := New()
	want := errors.New("apply failed")
	a := &stubApplier{failOn: map[event.EventID]error{"ev-1": want}}
	if err := tr.DeliverTo(context.Background(), a, mkEvent("ev-1")); !errors.Is(err, want) {
		t.Errorf("DeliverTo error = %v; want %v", err, want)
	}
}

func TestDeliverInOrder_Sequence(t *testing.T) {
	tr := New()
	a := &stubApplier{}
	evs := []*event.Event{mkEvent("a"), mkEvent("b"), mkEvent("c")}
	tr.DeliverInOrder(context.Background(), a, evs)

	want := []event.EventID{"a", "b", "c"}
	if len(a.delivered) != len(want) {
		t.Fatalf("delivered = %v; want %v", a.delivered, want)
	}
	for i, id := range want {
		if a.delivered[i] != id {
			t.Errorf("delivered[%d] = %s; want %s", i, a.delivered[i], id)
		}
	}
}

func TestBroadcastDeterministic_PerNodeOrdering(t *testing.T) {
	tr := New()
	a0 := &stubApplier{}
	a1 := &stubApplier{}
	a2 := &stubApplier{}

	evs := []*event.Event{mkEvent("x"), mkEvent("y"), mkEvent("z")}
	orders := [][]int{
		{0, 1, 2}, // node 0: x, y, z
		{2, 0, 1}, // node 1: z, x, y
		{1, 2, 0}, // node 2: y, z, x
	}

	err := tr.BroadcastDeterministic(
		context.Background(),
		[]Applier{a0, a1, a2},
		evs, orders,
	)
	if err != nil {
		t.Fatalf("BroadcastDeterministic: %v", err)
	}

	checks := []struct {
		name string
		got  []event.EventID
		want []event.EventID
	}{
		{"node 0", a0.delivered, []event.EventID{"x", "y", "z"}},
		{"node 1", a1.delivered, []event.EventID{"z", "x", "y"}},
		{"node 2", a2.delivered, []event.EventID{"y", "z", "x"}},
	}
	for _, ch := range checks {
		if len(ch.got) != len(ch.want) {
			t.Errorf("%s: delivered = %v; want %v", ch.name, ch.got, ch.want)
			continue
		}
		for i := range ch.want {
			if ch.got[i] != ch.want[i] {
				t.Errorf("%s: delivered[%d] = %s; want %s", ch.name, i, ch.got[i], ch.want[i])
			}
		}
	}
}

func TestBroadcastDeterministic_LengthMismatch(t *testing.T) {
	tr := New()
	a := &stubApplier{}
	err := tr.BroadcastDeterministic(
		context.Background(),
		[]Applier{a},
		[]*event.Event{mkEvent("x")},
		[][]int{{0}, {0}}, // 2 orders for 1 node
	)
	if err == nil {
		t.Error("BroadcastDeterministic(mismatched lengths): want error")
	}
}

func TestBroadcastDeterministic_OrderOutOfRange(t *testing.T) {
	tr := New()
	a := &stubApplier{}
	err := tr.BroadcastDeterministic(
		context.Background(),
		[]Applier{a},
		[]*event.Event{mkEvent("x")},
		[][]int{{1}}, // index 1 out of range for 1-element evs
	)
	if err == nil {
		t.Error("BroadcastDeterministic(order out of range): want error")
	}
}
