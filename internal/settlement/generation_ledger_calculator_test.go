package settlement

import (
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// mockDAG is a simple in-memory DAG for testing ancestor traversal.
type mockDAG struct {
	events map[event.EventID]*event.Event
}

func (d *mockDAG) Get(id event.EventID) (*event.Event, error) {
	e, ok := d.events[id]
	if !ok {
		return nil, fmt.Errorf("event not found: %s", id)
	}
	return e, nil
}

func newMockDAG(events ...*event.Event) *mockDAG {
	m := &mockDAG{events: make(map[event.EventID]*event.Event)}
	for _, e := range events {
		m.events[e.ID] = e
	}
	return m
}

func TestCalculate_SingleAncestorDepth1(t *testing.T) {
	parent := &event.Event{ID: "parent-1", AgentID: "agent-parent"}
	child := &event.Event{ID: "child-1", CausalRefs: []event.EventID{"parent-1"}}
	dag := newMockDAG(parent, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) float64 { return 1.0 })

	dist := calc.Calculate("child-1", 1000)
	if len(dist.Recipients) != 1 {
		t.Fatalf("Recipients = %d; want 1", len(dist.Recipients))
	}
	if dist.Recipients[0].Amount != 1000 {
		t.Errorf("Amount = %d; want 1000 (full pool to single ancestor)", dist.Recipients[0].Amount)
	}
	if dist.Recipients[0].Depth != 1 {
		t.Errorf("Depth = %d; want 1", dist.Recipients[0].Depth)
	}
	if dist.Treasury != 0 {
		t.Errorf("Treasury = %d; want 0", dist.Treasury)
	}
}

func TestCalculate_ThreeGenerations(t *testing.T) {
	greatGP := &event.Event{ID: "ggp", AgentID: "agent-ggp"}
	gp := &event.Event{ID: "gp", AgentID: "agent-gp", CausalRefs: []event.EventID{"ggp"}}
	parent := &event.Event{ID: "p", AgentID: "agent-p", CausalRefs: []event.EventID{"gp"}}
	child := &event.Event{ID: "c", CausalRefs: []event.EventID{"p"}}
	dag := newMockDAG(greatGP, gp, parent, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) float64 { return 1.0 })

	pool := uint64(10000)
	dist := calc.Calculate("c", pool)

	// Weights: d=1 → 1.0, d=2 → 0.25, d=3 → 0.111
	// Total ≈ 1.361
	if len(dist.Recipients) != 3 {
		t.Fatalf("Recipients = %d; want 3", len(dist.Recipients))
	}

	// Verify normalization: total must equal pool.
	var total uint64
	for _, r := range dist.Recipients {
		total += r.Amount
	}
	total += dist.Treasury
	if total != pool {
		t.Errorf("total distributed = %d; want %d (normalization error)", total, pool)
	}

	// Depth 1 should get the largest share.
	if dist.Recipients[0].Depth != 1 || dist.Recipients[0].Amount < 7000 {
		t.Errorf("depth-1 ancestor should get >70%% of pool; got %d", dist.Recipients[0].Amount)
	}
}

func TestCalculate_DepthCap(t *testing.T) {
	// Chain of 5 events — only first 3 ancestors should be included.
	e5 := &event.Event{ID: "e5", AgentID: "a5"}
	e4 := &event.Event{ID: "e4", AgentID: "a4", CausalRefs: []event.EventID{"e5"}}
	e3 := &event.Event{ID: "e3", AgentID: "a3", CausalRefs: []event.EventID{"e4"}}
	e2 := &event.Event{ID: "e2", AgentID: "a2", CausalRefs: []event.EventID{"e3"}}
	e1 := &event.Event{ID: "e1", AgentID: "a1", CausalRefs: []event.EventID{"e2"}}
	dag := newMockDAG(e5, e4, e3, e2, e1)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) float64 { return 1.0 })

	dist := calc.Calculate("e1", 1000)
	if len(dist.Recipients) != 3 {
		t.Errorf("Recipients = %d; want 3 (depth cap excludes e4, e5)", len(dist.Recipients))
	}
	for _, r := range dist.Recipients {
		if r.Depth > 3 {
			t.Errorf("depth %d exceeds cap 3", r.Depth)
		}
	}
}

func TestCalculate_EmptyAncestors(t *testing.T) {
	child := &event.Event{ID: "orphan", CausalRefs: nil}
	dag := newMockDAG(child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) float64 { return 1.0 })

	dist := calc.Calculate("orphan", 500)
	if len(dist.Recipients) != 0 {
		t.Errorf("Recipients = %d; want 0", len(dist.Recipients))
	}
	if dist.Treasury != 500 {
		t.Errorf("Treasury = %d; want 500 (full pool)", dist.Treasury)
	}
}

func TestCalculate_NormalizationSum(t *testing.T) {
	parent := &event.Event{ID: "p1", AgentID: "a1"}
	parent2 := &event.Event{ID: "p2", AgentID: "a2"}
	child := &event.Event{ID: "c1", CausalRefs: []event.EventID{"p1", "p2"}}
	dag := newMockDAG(parent, parent2, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) float64 { return 1.0 })

	pool := uint64(999) // odd number to stress rounding
	dist := calc.Calculate("c1", pool)
	var total uint64
	for _, r := range dist.Recipients {
		total += r.Amount
	}
	total += dist.Treasury
	if total != pool {
		t.Errorf("total = %d; want %d (rounding error)", total, pool)
	}
}

func TestCalculate_MultipleAncestorsSameDepth(t *testing.T) {
	p1 := &event.Event{ID: "p1", AgentID: "a1"}
	p2 := &event.Event{ID: "p2", AgentID: "a2"}
	child := &event.Event{ID: "c", CausalRefs: []event.EventID{"p1", "p2"}}
	dag := newMockDAG(p1, p2, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) float64 { return 1.0 })

	dist := calc.Calculate("c", 1000)
	if len(dist.Recipients) != 2 {
		t.Fatalf("Recipients = %d; want 2", len(dist.Recipients))
	}
	// Both at depth 1 with equal Q → equal shares.
	// 1000 / 2 = 500 each.
	for _, r := range dist.Recipients {
		if r.Amount < 499 || r.Amount > 501 {
			t.Errorf("equal-depth ancestor got %d; want ~500", r.Amount)
		}
	}
}
