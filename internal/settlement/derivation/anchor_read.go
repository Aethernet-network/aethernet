package derivation

import (
	"errors"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// MaxGenLedgerDepth is the maximum BFS hop depth for generation-ledger
// ancestor traversal. Locked at 3 per F5 5A.3 §3.1 (matches the pre-5B
// gen-ledger calculator's `GenerationLedgerMaxDepth` constant; the depth
// bound is canonical-frozen — changes require a coordinated upgrade).
const MaxGenLedgerDepth = 3

// AncestorWithDepth pairs a canonical EventID with the depth at which
// the anchor-scoped BFS from `root` first encountered it (depth=0 for
// the seed; depth=1 for direct CausalRefs that satisfy the anchor
// predicate; etc.).
//
// Returned by ReadAtAnchor as a SINGLE SOURCE OF TRUTH for both
// (a) ancestor membership in the anchor-scoped subgraph, and
// (b) the depth used for downstream weighting (e.g., gen-ledger
//     `quality / depth²` per F5 5A.3 §3.1).
//
// Critical canonicality property (multi-AI Fix #1, 2026-04-25):
// the depth here is the FIRST-ENCOUNTER DEPTH FROM THE ANCHOR-SCOPED
// BFS. It is NOT the global shortest path from root over all CausalRefs.
// An ancestor reachable via a shorter out-of-scope path (one that
// fails the IsAncestor(child, anchor) predicate) gets the LONGER
// in-scope depth, because the BFS that produced the membership is the
// one that produced the depth — they cannot drift.
//
// Computing depth in a separate helper (the prior `computeDepths`
// approach) created a cross-helper invariant that the two BFSs must
// produce the same depth — which they did NOT for ancestors with
// out-of-scope shorter paths. That gap broke D-1 across nodes whose
// canonical state happened to include the relevant DAG topology.
// This struct collapses depth and membership into the same canonical
// computation; they cannot drift again.
type AncestorWithDepth struct {
	EventID event.EventID
	Depth   int
}

// ReadAtAnchor performs a bounded, anchor-scoped BFS from `root`,
// returning the deterministic seed-inclusive slice of (EventID, depth)
// pairs reachable within `maxDepth` hops whose path to `root` lies
// within the canonical ancestry of (or equal to) `anchor`.
//
// Per F5 5A.3 §2.2 + §2.2.1:
//
//   - **Seed-inclusive**: `root` is included in the result on first
//     dequeue (at depth=0). The result is the bounded anchor-scoped
//     causal subgraph including root and (when reachable) anchor —
//     NOT a strict-ancestors-only set.
//
//   - **Anchor-in-result semantic (option a)**: when `anchor` is
//     reachable from `root` within `maxDepth` hops, anchor IS included.
//     Anchor's own children are not traversed further (descendants of
//     anchor are not ancestors of anchor — IsAncestor returns false).
//
//   - **Spec 2 dedup**: each event is enqueued at most once via the
//     visited set; concurrent paths to the same ancestor produce one
//     entry in the result with the FIRST-ENCOUNTER ANCHOR-SCOPED DEPTH
//     (BFS naturally visits via the shortest in-scope path; lex-sorted
//     per-hop child enumeration breaks ties deterministically).
//
//   - **Spec 3 deterministic per-hop**: at each parent's CausalRefs
//     enumeration, children are visited in lex-sorted order so the
//     result slice is byte-identical across nodes for identical canonical
//     DAG state.
//
//   - **All-or-defer**: any traversed event missing from the local DAG
//     causes the function to return ErrEventNotFound (caller defers per
//     Plan v3 §2.3 step 6 → Status=StatusDeferred,
//     Cause=DeferredCauseDAGAncestorBFS).
//
// Nil-anchor semantic (Fix A pre-activation case): when `anchor` is the
// empty EventID, the anchor-scoped predicate `IsAncestor(child, anchor)`
// is skipped — depth-bounded BFS proceeds from root without the anchor
// filter. Pre-`ReputationActivation`, no canonical snapshot exists per
// Fix A; the gen-ledger traversal still happens but is bounded only by
// `maxDepth`. This matches pre-5B settler behavior (gen-ledger BFS
// without anchor scoping) for the genesis epoch.
//
// **Multi-AI Fix #1 (2026-04-25)**: return type changed from
// `[]event.EventID` to `[]AncestorWithDepth`. The depth is the canonical
// depth at which this anchor-scoped BFS first encountered the ancestor;
// downstream weighting (gen-ledger `quality / depth²`) MUST use this
// depth, NOT a depth recomputed by an unrestricted BFS over all
// CausalRefs (which would pick shorter out-of-scope paths and produce
// cross-node-divergent weights). Single source of truth — depth and
// membership cannot drift.
func ReadAtAnchor(reader AnchorReader, anchor event.EventID, root event.EventID, maxDepth int) ([]AncestorWithDepth, error) {
	type queueEntry struct {
		id    event.EventID
		depth int
	}

	visited := map[event.EventID]struct{}{root: {}}
	queue := []queueEntry{{id: root, depth: 0}}
	var result []AncestorWithDepth

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		// Defensive: depth > maxDepth is unreachable per the enqueue gate
		// below, but if it ever fires, skip rather than enumerate children.
		if cur.depth > maxDepth {
			continue
		}

		ev, err := reader.Get(cur.id)
		if err != nil {
			return nil, err
		}

		// Seed-inclusive: append on first dequeue with the BFS-tracked
		// depth. Anchor (when reached as a child enqueued via the
		// special-case below) appears in the result on its own dequeue.
		// Per §2.2.1 option (a).
		result = append(result, AncestorWithDepth{EventID: cur.id, Depth: cur.depth})

		// Don't enumerate children at maxDepth.
		if cur.depth == maxDepth {
			continue
		}

		// Spec 3: lex-sort CausalRefs before iterating to make the BFS
		// child-visit order deterministic across nodes.
		children := append([]event.EventID(nil), ev.CausalRefs...)
		sort.Slice(children, func(i, j int) bool { return children[i] < children[j] })

		for _, child := range children {
			if _, seen := visited[child]; seen {
				continue // Spec 2 dedup
			}

			// Anchor-scoped predicate: only follow children that are
			// canonical ancestors of (or equal to) `anchor`. The
			// `child == anchor` short-circuit includes anchor itself
			// as a leaf (option a per §2.2.1) without an irreflexive
			// IsAncestor(anchor, anchor) self-check (which returns false).
			//
			// Nil anchor: skip the predicate entirely (Fix A
			// pre-activation case; depth-bounded BFS without
			// anchor-scoping).
			if anchor != "" && child != anchor {
				isAnc, err := reader.IsAncestor(child, anchor)
				if err != nil {
					if errors.Is(err, errIsAncestorNotInScope()) {
						// Unreachable today; reserved for future tighter
						// error-class signaling that distinguishes "not in
						// scope" from "not materialized."
						continue
					}
					return nil, err
				}
				if !isAnc {
					continue
				}
			}

			visited[child] = struct{}{}
			queue = append(queue, queueEntry{id: child, depth: cur.depth + 1})
		}
	}

	return result, nil
}

// errIsAncestorNotInScope is a placeholder error for a future error
// class that would distinguish "child is not in canonical ancestry of
// anchor" from "child is not materialized." Today, dag.IsAncestor
// returns ErrEventNotFound only when an ID is absent; the
// not-in-scope case returns (false, nil). The placeholder allows the
// errors.Is dispatch above to be tightened in the future without
// changing the call shape.
func errIsAncestorNotInScope() error { return nil }
