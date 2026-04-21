package settlement

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
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
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)

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
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)

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
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)

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
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)

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
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)

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
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)

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

// ─── commit-4 shadow-gate tests ─────────────────────────────────────────

func captureSlogLinesGL(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	return &buf, func() { slog.SetDefault(prev) }
}

// TestCalculate_ShadowMode_ReturnsFloat asserts the legacy float path is
// canonical while shadowMode is true. The float path produces the same
// Amount values as the pre-migration code because this test uses neutral
// Q on a single-ancestor DAG (no rounding divergence).
func TestCalculate_ShadowMode_ReturnsFloat(t *testing.T) {
	parent := &event.Event{ID: "p-sm", AgentID: "agent-p"}
	child := &event.Event{ID: "c-sm", CausalRefs: []event.EventID{"p-sm"}}
	dag := newMockDAG(parent, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)

	dist := calc.Calculate("c-sm", 1000)
	if len(dist.Recipients) != 1 {
		t.Fatalf("Recipients = %d; want 1", len(dist.Recipients))
	}
	if dist.Recipients[0].Amount != 1000 {
		t.Errorf("Amount = %d; want 1000 (full pool)", dist.Recipients[0].Amount)
	}
}

// TestCalculate_ShadowMode_LogsDelta asserts shadow_delta is emitted.
func TestCalculate_ShadowMode_LogsDelta(t *testing.T) {
	buf, restore := captureSlogLinesGL(t)
	defer restore()

	parent := &event.Event{ID: "p-log", AgentID: "agent-p"}
	child := &event.Event{ID: "c-log", CausalRefs: []event.EventID{"p-log"}}
	dag := newMockDAG(parent, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, true)
	_ = calc.Calculate("c-log", 1000)

	out := buf.String()
	if !strings.Contains(out, "shadow_delta") {
		t.Fatalf("expected shadow_delta line; got %q", out)
	}
	if !strings.Contains(out, `context=generation_ledger`) {
		t.Errorf("expected context=generation_ledger; got %q", out)
	}
	if !strings.Contains(out, `task_id=c-log`) {
		t.Errorf("expected task_id=c-log; got %q", out)
	}
	if !strings.Contains(out, `pool=1000`) {
		t.Errorf("expected pool=1000; got %q", out)
	}
}

// TestCalculateInteger_Correctness exercises the integer path directly
// on a three-generation ancestry and checks conservation + depth ordering.
func TestCalculateInteger_Correctness(t *testing.T) {
	ggp := &event.Event{ID: "ggp-i", AgentID: "a-ggp"}
	gp := &event.Event{ID: "gp-i", AgentID: "a-gp", CausalRefs: []event.EventID{"ggp-i"}}
	p := &event.Event{ID: "p-i", AgentID: "a-p", CausalRefs: []event.EventID{"gp-i"}}
	c := &event.Event{ID: "c-i", CausalRefs: []event.EventID{"p-i"}}
	dag := newMockDAG(ggp, gp, p, c)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, false)

	const pool uint64 = 10000
	dist := calc.Calculate("c-i", pool)
	if len(dist.Recipients) != 3 {
		t.Fatalf("Recipients = %d; want 3", len(dist.Recipients))
	}
	var sum uint64
	for _, r := range dist.Recipients {
		sum += r.Amount
	}
	sum += dist.Treasury
	if sum != pool {
		t.Errorf("conservation: sum = %d; want %d", sum, pool)
	}
	// With weights 10000, 2500, 1111 (total 13611), depth-1 takes ~73%.
	depth1 := uint64(0)
	for _, r := range dist.Recipients {
		if r.Depth == 1 {
			depth1 = r.Amount
		}
	}
	if depth1 < uint64(pool)*70/100 {
		t.Errorf("depth-1 amount = %d; want >= 7000 (70%% of pool)", depth1)
	}
}

// TestCalculate_NonShadowMode_ReturnsInt asserts shadowMode=false path
// is taken and protocolmath's conservation guarantee holds (Treasury=0
// on the happy path).
func TestCalculate_NonShadowMode_ReturnsInt(t *testing.T) {
	parent := &event.Event{ID: "p-nsm", AgentID: "agent-p"}
	child := &event.Event{ID: "c-nsm", CausalRefs: []event.EventID{"p-nsm"}}
	dag := newMockDAG(parent, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }, false)

	dist := calc.Calculate("c-nsm", 999) // odd pool to stress remainder
	if len(dist.Recipients) != 1 {
		t.Fatalf("Recipients = %d; want 1", len(dist.Recipients))
	}
	if dist.Recipients[0].Amount != 999 {
		t.Errorf("single-ancestor Amount = %d; want 999", dist.Recipients[0].Amount)
	}
	if dist.Treasury != 0 {
		t.Errorf("integer-path Treasury = %d; want 0 (protocolmath conserves)", dist.Treasury)
	}
}

// TestCalculateInteger_NegativeQ_ClampedToZero: a pathological qualityFn
// returning negative values is clamped to zero with a warning log; no
// ErrInvariantViolation bubbles out.
func TestCalculateInteger_NegativeQ_ClampedToZero(t *testing.T) {
	parent := &event.Event{ID: "p-neg", AgentID: "agent-p"}
	child := &event.Event{ID: "c-neg", CausalRefs: []event.EventID{"p-neg"}}
	dag := newMockDAG(parent, child)
	calc := NewGenerationLedgerCalculator(dag, func(_ event.EventID) protocolmath.BasisPoints { return -500 }, false)

	dist := calc.Calculate("c-neg", 1000)
	// With Q clamped to 0, the only ancestor has weight 0. protocolmath
	// zero-total-weight fallback splits evenly, which for 1 recipient is
	// the full pool. Conservation preserved.
	if len(dist.Recipients) != 1 {
		t.Fatalf("Recipients = %d; want 1", len(dist.Recipients))
	}
	if dist.Recipients[0].Amount != 1000 {
		t.Errorf("Amount = %d; want 1000 (clamped-Q fallback)", dist.Recipients[0].Amount)
	}
}

// TestCalculateInteger_QAboveCeiling_ClampedToMax verifies that weights
// above MaxBasisPoints are silently clamped at the gen-ledger boundary.
func TestCalculateInteger_QAboveCeiling_ClampedToMax(t *testing.T) {
	p1 := &event.Event{ID: "p-hi", AgentID: "agent-hi"}
	p2 := &event.Event{ID: "p-norm", AgentID: "agent-norm"}
	child := &event.Event{ID: "c-hi", CausalRefs: []event.EventID{"p-hi", "p-norm"}}
	dag := newMockDAG(p1, p2, child)
	// Return very large Q for p-hi to force the clamp.
	calc := NewGenerationLedgerCalculator(dag, func(id event.EventID) protocolmath.BasisPoints {
		if id == "p-hi" {
			return protocolmath.MaxBasisPoints * 100
		}
		return protocolmath.MaxBasisPoints
	}, false)

	dist := calc.Calculate("c-hi", 1000)
	// Both clamped to MaxBasisPoints → equal weights → 500 each
	// (sorted-last absorbs remainder, but pool is even so no remainder).
	if len(dist.Recipients) != 2 {
		t.Fatalf("Recipients = %d; want 2", len(dist.Recipients))
	}
	var sum uint64
	for _, r := range dist.Recipients {
		sum += r.Amount
	}
	if sum != 1000 {
		t.Errorf("conservation: sum = %d; want 1000", sum)
	}
	// Equal weights after clamp → each gets ~500.
	for _, r := range dist.Recipients {
		if r.Amount < 499 || r.Amount > 501 {
			t.Errorf("recipient %s got %d; want ~500 (clamped-equal weights)", r.EventID, r.Amount)
		}
	}
}

// TestCalculate_NeutralQualityFn_IntegerMatchesFloat_WithinTolerance
// pins the production-today case (qualityFn always NeutralBP) across
// three generations: integer and float paths agree on conservation; per-
// recipient delta is small and bounded by the rounding remainder. Part F
// will see these values in shadow_delta lines from testnet logs.
func TestCalculate_NeutralQualityFn_IntegerMatchesFloat_WithinTolerance(t *testing.T) {
	ggp := &event.Event{ID: "ggp-t", AgentID: "a-ggp"}
	gp := &event.Event{ID: "gp-t", AgentID: "a-gp", CausalRefs: []event.EventID{"ggp-t"}}
	p := &event.Event{ID: "p-t", AgentID: "a-p", CausalRefs: []event.EventID{"gp-t"}}
	c := &event.Event{ID: "c-t", CausalRefs: []event.EventID{"p-t"}}
	dag := newMockDAG(ggp, gp, p, c)
	qFn := func(_ event.EventID) protocolmath.BasisPoints { return protocolmath.NeutralBP }

	calcFloat := NewGenerationLedgerCalculator(dag, qFn, true) // shadow returns float
	calcInt := NewGenerationLedgerCalculator(dag, qFn, false)   // returns int

	const pool uint64 = 10000
	fDist := calcFloat.Calculate("c-t", pool)
	iDist := calcInt.Calculate("c-t", pool)

	// Both paths must conserve.
	var fSum, iSum uint64
	for _, r := range fDist.Recipients {
		fSum += r.Amount
	}
	fSum += fDist.Treasury
	for _, r := range iDist.Recipients {
		iSum += r.Amount
	}
	iSum += iDist.Treasury
	if fSum != pool || iSum != pool {
		t.Errorf("conservation: float_sum=%d int_sum=%d want=%d", fSum, iSum, pool)
	}

	// Per-recipient delta: bounded by the rounding remainder, i.e. at most
	// ~1-2 µAET per recipient given pool=10000 and three recipients.
	fByID := make(map[event.EventID]uint64)
	for _, r := range fDist.Recipients {
		fByID[r.EventID] = r.Amount
	}
	for _, r := range iDist.Recipients {
		f := fByID[r.EventID]
		var d int64
		if f > r.Amount {
			d = int64(f - r.Amount)
		} else {
			d = int64(r.Amount - f)
		}
		// Pool is 10000, 3 recipients, weights 10000/2500/1111. The integer
		// path uses protocolmath.Allocate with sorted-last (ggp-t > gp-t >
		// c-t... actually c-t is not an ancestor, just the seed; ancestor
		// IDs are ggp-t, gp-t, p-t sorted as ggp-t < gp-t < p-t). Delta ≤ 5
		// covers the full integer/float rounding divergence.
		if d > 5 {
			t.Errorf("recipient %s: per-recipient delta %d exceeds tolerance", r.EventID, d)
		}
	}
}

