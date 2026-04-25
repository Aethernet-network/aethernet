package dag_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// CountAncestorsByType ground-truth tests per architect ask at sub-spec
// breakpoint A completion + carried into breakpoint B. The §1.4 admission
// cross-check correctness depends entirely on this primitive returning
// canonical truth — if it diverges in an edge case, the validator
// silently accepts invalid events. Tests below construct DAG topologies
// by hand, count matching ancestors manually, and assert the primitive
// returns exactly that.

func TestCountAncestorsByType_GroundTruth_DeepLinearChainSparseMatches(t *testing.T) {
	// Chain of 20 events: every 5th is EpochBoundary, rest are
	// TaskVerificationConsensus. Ground truth for descendant at end:
	//   EpochBoundary count = 4 (positions 5, 10, 15, 20 — but 20 is
	//   the descendant itself, irreflexive, so 3).
	//   TVConsensus count = 16 (positions 1-4, 6-9, 11-14, 16-19).
	d := dag.New()
	parent := makeGenesis(t, "g")
	mustAdd(t, d, parent)

	events := make([]*event.Event, 20)
	for i := 0; i < 20; i++ {
		var t1 event.EventType
		if (i+1)%5 == 0 { // positions 5, 10, 15, 20 (1-indexed)
			t1 = event.EventTypeEpochBoundary
		} else {
			t1 = event.EventTypeTaskVerificationConsensus
		}
		ev := makeTypedChild(t, t1, parent)
		mustAdd(t, d, ev)
		events[i] = ev
		parent = ev
	}
	tip := events[19] // 20th event, EpochBoundary

	boundaryCount, err := d.CountAncestorsByType(tip.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: %v", err)
	}
	if boundaryCount != 3 {
		t.Fatalf("ground truth boundary count = 3 (positions 5,10,15; 20 is tip itself, irreflexive); got %d", boundaryCount)
	}

	tvcCount, err := d.CountAncestorsByType(tip.ID, event.EventTypeTaskVerificationConsensus)
	if err != nil {
		t.Fatalf("CountAncestorsByType: %v", err)
	}
	if tvcCount != 16 {
		t.Fatalf("ground truth TVC count = 16 (positions 1-4, 6-9, 11-14, 16-19); got %d", tvcCount)
	}
}

func TestCountAncestorsByType_GroundTruth_DiamondMatchOnOneBranch(t *testing.T) {
	// Diamond:           genesis (Transfer)
	//                       |
	//                     middle (Transfer)
	//                    /        \
	//             leftBnd          rightTVC
	//            (EpochBnd)        (TVConsensus)
	//                    \        /
	//                       tip (Transfer)
	// Ground truth from tip:
	//   EpochBoundary ancestors = 1 (leftBnd)
	//   TVConsensus ancestors = 1 (rightTVC)
	d := dag.New()
	g := makeGenesis(t, "g")
	middle := makeTypedChild(t, event.EventTypeTransfer, g)
	leftBnd := makeTypedChild(t, event.EventTypeEpochBoundary, middle)
	rightTVC := makeTypedChild(t, event.EventTypeTaskVerificationConsensus, middle)
	tip := makeTypedChild(t, event.EventTypeTransfer, leftBnd, rightTVC)
	for _, e := range []*event.Event{g, middle, leftBnd, rightTVC, tip} {
		mustAdd(t, d, e)
	}

	bndCount, err := d.CountAncestorsByType(tip.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: %v", err)
	}
	if bndCount != 1 {
		t.Fatalf("ground truth = 1 EpochBoundary ancestor (leftBnd); got %d", bndCount)
	}

	tvcCount, err := d.CountAncestorsByType(tip.ID, event.EventTypeTaskVerificationConsensus)
	if err != nil {
		t.Fatalf("CountAncestorsByType: %v", err)
	}
	if tvcCount != 1 {
		t.Fatalf("ground truth = 1 TVConsensus ancestor (rightTVC); got %d", tvcCount)
	}
}

func TestCountAncestorsByType_GroundTruth_AnchorAtRoot(t *testing.T) {
	// genesis is itself an EpochBoundary (synthetic; in production
	// genesis would not be a boundary, but the primitive must handle
	// any root type). Ground truth from descendant: 1.
	// From the root itself: 0 (irreflexive).
	d := dag.New()
	root := makeTypedChild(t, event.EventTypeEpochBoundary)
	mustAdd(t, d, root)
	c1 := makeTypedChild(t, event.EventTypeTransfer, root)
	c2 := makeTypedChild(t, event.EventTypeTaskVerificationConsensus, c1)
	mustAdd(t, d, c1)
	mustAdd(t, d, c2)

	rootSelf, err := d.CountAncestorsByType(root.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType(root): %v", err)
	}
	if rootSelf != 0 {
		t.Fatalf("irreflexive: root counts itself? got %d, want 0", rootSelf)
	}

	fromC1, err := d.CountAncestorsByType(c1.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType(c1): %v", err)
	}
	if fromC1 != 1 {
		t.Fatalf("c1 has root (EpochBoundary) as direct ancestor; got %d, want 1", fromC1)
	}

	fromC2, err := d.CountAncestorsByType(c2.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType(c2): %v", err)
	}
	if fromC2 != 1 {
		t.Fatalf("c2 has root (EpochBoundary) as transitive ancestor; got %d, want 1", fromC2)
	}
}

func TestCountAncestorsByType_GroundTruth_DescendantTypeMatchesIrreflexive(t *testing.T) {
	// chain: g → b1 → b2 → b3 (all EpochBoundary except g)
	// Ground truth for b3: 2 (b1, b2 — NOT b3 itself).
	d := dag.New()
	g := makeGenesis(t, "g") // Transfer
	b1 := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	b2 := makeTypedChild(t, event.EventTypeEpochBoundary, b1)
	b3 := makeTypedChild(t, event.EventTypeEpochBoundary, b2)
	for _, e := range []*event.Event{g, b1, b2, b3} {
		mustAdd(t, d, e)
	}

	count, err := d.CountAncestorsByType(b3.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: %v", err)
	}
	if count != 2 {
		t.Fatalf("descendant b3 itself must NOT be counted; ground truth 2 (b1, b2); got %d", count)
	}
}

func TestCountAncestorsByType_GroundTruth_MergeFromMultipleBranches(t *testing.T) {
	// Three independent chains, each with 1 EpochBoundary, all merging
	// into a tip. Ground truth: 3.
	//      g
	//   /  |  \
	//  a   b   c       (all EpochBoundary)
	//  |   |   |
	//  a2  b2  c2      (Transfer)
	//   \  |  /
	//     tip          (Transfer; references a2, b2, c2)
	d := dag.New()
	g := makeGenesis(t, "g")
	a := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	b := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	c := makeTypedChild(t, event.EventTypeEpochBoundary, g)
	a2 := makeTypedChild(t, event.EventTypeTransfer, a)
	b2 := makeTypedChild(t, event.EventTypeTransfer, b)
	c2 := makeTypedChild(t, event.EventTypeTransfer, c)
	tip := makeTypedChild(t, event.EventTypeTransfer, a2, b2, c2)
	for _, e := range []*event.Event{g, a, b, c, a2, b2, c2, tip} {
		mustAdd(t, d, e)
	}

	count, err := d.CountAncestorsByType(tip.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: %v", err)
	}
	if count != 3 {
		t.Fatalf("ground truth = 3 EpochBoundary ancestors (a, b, c); got %d", count)
	}
}

func TestCountAncestorsByType_GroundTruth_NoMatchWithManyAncestors(t *testing.T) {
	// 50-event linear chain, all Transfer. Ground truth EpochBoundary
	// ancestors: 0. Verifies primitive walks the full chain and returns
	// 0 (not an error) when no matches exist.
	d := dag.New()
	parent := makeGenesis(t, "g")
	mustAdd(t, d, parent)
	for i := 0; i < 50; i++ {
		child := makeTypedChild(t, event.EventTypeTransfer, parent)
		mustAdd(t, d, child)
		parent = child
	}

	count, err := d.CountAncestorsByType(parent.ID, event.EventTypeEpochBoundary)
	if err != nil {
		t.Fatalf("CountAncestorsByType: %v", err)
	}
	if count != 0 {
		t.Fatalf("ground truth = 0 (no matching ancestors); got %d", count)
	}
}
