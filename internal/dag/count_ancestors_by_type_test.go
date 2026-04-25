package dag_test

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// makeTypedChild creates a signed event of the given type referencing
// the provided parents. Mirrors makeChild but allows the caller to pick
// the EventType — needed for CountAncestorsByType tests that need a mix
// of types in the ancestor set.
func makeTypedChild(t *testing.T, eventType event.EventType, parents ...*event.Event) *event.Event {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("makeTypedChild: GenerateKeyPair: %v", err)
	}
	aid := string(kp.AgentID())

	refs := make([]event.EventID, len(parents))
	prior := make(map[event.EventID]uint64, len(parents))
	for i, p := range parents {
		refs[i] = p.ID
		prior[p.ID] = p.CausalTimestamp
	}

	// Use a minimal Transfer-shaped payload; the test doesn't read it.
	// Only the Type field matters for CountAncestorsByType.
	payload := event.TransferPayload{
		FromAgent: aid,
		ToAgent:   "sink",
		Amount:    1,
		Currency:  "AET",
	}

	e, err := event.New(eventType, refs, payload, aid, prior, 0)
	if err != nil {
		t.Fatalf("makeTypedChild(%q): %v", eventType, err)
	}
	if err := crypto.SignEvent(e, kp); err != nil {
		t.Fatalf("makeTypedChild(%q) sign: %v", eventType, err)
	}
	return e
}

func TestCountAncestorsByType_DescendantMissing(t *testing.T) {
	d := dag.New()
	count, err := d.CountAncestorsByType("nonexistent", event.EventTypeEpochBoundary)
	if !errors.Is(err, dag.ErrEventNotFound) {
		t.Fatalf("CountAncestorsByType: expected ErrEventNotFound, got err=%v count=%d", err, count)
	}
}

func TestCountAncestorsByType_GenesisHasNoAncestors(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "g")
	mustAdd(t, d, g)

	count, err := d.CountAncestorsByType(g.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("genesis has no ancestors of any type, got count=%d", count)
	}
}

func TestCountAncestorsByType_StrictIrreflexive(t *testing.T) {
	// Descendant itself is NOT counted, even if its type matches.
	d := dag.New()
	g := makeGenesis(t, "g")
	boundary := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	mustAdd(t, d, g)
	mustAdd(t, d, boundary)

	count, err := d.CountAncestorsByType(boundary.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("boundary itself should not be counted as its own ancestor, got count=%d", count)
	}
}

func TestCountAncestorsByType_LinearChainCountsMatchingType(t *testing.T) {
	// Chain: genesis → boundary1 → tvc1 → boundary2 → tvc2 → tip
	// Counting EpochBoundary ancestors of tip = 2.
	d := dag.New()
	g := makeGenesis(t, "g")
	boundary1 := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	tvc1 := makeTypedChild(t, event.EventTypeTaskVerificationConsensus, boundary1)
	boundary2 := makeTypedChild(t, event.EventTypeEpochBoundary, tvc1)
	tvc2 := makeTypedChild(t, event.EventTypeTaskVerificationConsensus, boundary2)
	tip := makeTypedChild(t, event.EventTypeTransfer, tvc2)
	for _, e := range []*event.Event{g, boundary1, tvc1, boundary2, tvc2, tip} {
		mustAdd(t, d, e)
	}

	count, err := d.CountAncestorsByType(tip.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: unexpected error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 EpochBoundary ancestors, got %d", count)
	}

	tvcCount, err := d.CountAncestorsByType(tip.ID, event.EventTypeTaskVerificationConsensus)
	if err != nil {
		t.Fatalf("CountAncestorsByType: unexpected error: %v", err)
	}
	if tvcCount != 2 {
		t.Fatalf("expected 2 TVConsensus ancestors, got %d", tvcCount)
	}
}

func TestCountAncestorsByType_DiamondCountsAncestorOnce(t *testing.T) {
	// Diamond:        boundary
	//                /        \
	//             tvc1        tvc2
	//                \        /
	//                  tip
	// boundary has two paths to tip; count must be 1, not 2.
	d := dag.New()
	g := makeGenesis(t, "g")
	boundary := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	tvc1 := makeTypedChild(t, event.EventTypeTaskVerificationConsensus, boundary)
	tvc2 := makeTypedChild(t, event.EventTypeTaskVerificationConsensus, boundary)
	tip := makeTypedChild(t, event.EventTypeTransfer, tvc1, tvc2)
	for _, e := range []*event.Event{g, boundary, tvc1, tvc2, tip} {
		mustAdd(t, d, e)
	}

	count, err := d.CountAncestorsByType(tip.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("diamond ancestor must be counted once, got count=%d", count)
	}
}

func TestCountAncestorsByType_NoMatchesReturnsZero(t *testing.T) {
	d := dag.New()
	g := makeGenesis(t, "g")
	c1 := makeTypedChild(t, event.EventTypeTransfer, g)
	c2 := makeTypedChild(t, event.EventTypeTransfer, c1)
	for _, e := range []*event.Event{g, c1, c2} {
		mustAdd(t, d, e)
	}

	count, err := d.CountAncestorsByType(c2.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("no EpochBoundary ancestors expected, got count=%d", count)
	}
}

func TestCountAncestorsByType_DeterminismAcrossRuns(t *testing.T) {
	// Build the same structure twice and assert identical counts.
	// Property: pure function of DAG topology + descendant + eventType.
	build := func() (*dag.DAG, event.EventID) {
		d := dag.New()
		g := makeGenesis(t, "g")
		b1 := makeTypedChild(t, event.EventTypeEpochBoundary, g)
		b2 := makeTypedChild(t, event.EventTypeEpochBoundary, b1)
		b3 := makeTypedChild(t, event.EventTypeEpochBoundary, b2)
		tip := makeTypedChild(t, event.EventTypeTaskVerificationConsensus, b3)
		for _, e := range []*event.Event{g, b1, b2, b3, tip} {
			mustAdd(t, d, e)
		}
		return d, tip.ID
	}

	// Two runs are NOT byte-identical (signers differ) but the COUNT
	// for the same shape is canonical.
	d1, tip1 := build()
	d2, tip2 := build()
	c1, err := d1.CountAncestorsByType(tip1, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	c2, err := d2.CountAncestorsByType(tip2, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if c1 != c2 {
		t.Fatalf("count divergence across runs of identical structure: c1=%d c2=%d", c1, c2)
	}
	if c1 != 3 {
		t.Fatalf("expected 3 EpochBoundary ancestors of tip, got %d", c1)
	}
}
