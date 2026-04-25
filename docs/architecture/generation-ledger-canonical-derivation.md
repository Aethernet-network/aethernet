# Generation-Ledger Canonical Derivation

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5A.3 — Generation-ledger ancestry canonicalization
**Status**: **v1.2 — Gate 5A.3 closed** (post architect read v1 → v1.1 + post multi-AI review v1.1 → v1.2). All six specifications addressed per Plan v3 §3.3. All §9 open questions resolved (§9.1, §9.2, §9.3 round-1 architect; §9.4 round-2 forward-noted to 5A.4). Five round-2 multi-AI changes integrated: terminology precision (R.SubmissionEventID vs R.canonical_seal_context distinction made explicit; §2.2 + 5A.2 v2.2 companion); "ancestor set" terminology clarified (BFS result is seed-inclusive causal subgraph, not strict-ancestors-only; §2.2 algorithm + §2.2.1 A-2); NeutralQualityStub future-versioning explicit (§6.4); test adapters as migration items (§2.1); Tips() preserved-for-shared-primitive rationale (§2.1). Three F5 completion gate-report notes captured externally (Grok cycle-exclusion wording, Grok first-encounter determinism warning, ChatGPT economic-substrate-vs-semantics framing). Architect sign-off recorded: epoch-coarse same-epoch-ancestor-exclusion accepted as deliberate consequence of cutoff alignment with W. No halt-trigger fired.
**Commit**: `51bce89` (F4-frozen branch).
**Date**: 2026-04-23
**Plan reference**: F5 Phase 5A Plan v3 §3.3 + Gate 5A.1 §9.3 architect decision (option a: design `ReadAtAnchor`) + Gate 5A.2 forward notes §15.4 (Grok predictions for 5A.3) + Gate 5A.2 closure consolidation direction (path α).

---

## 0. Decisions locked

1. **Consolidation path α** (architect direction post-Gate-5A.2): the existing `dispatch.DAGAnchorReader` interface (`Tips() + IsAncestor() + Get()`) is the substrate. F5 5A.3 ships this consolidation:
   - Move `DAGAnchorReader` from `internal/dispatch/anchor.go` to a neutral location in `internal/dag/`.
   - Retire the duplicate minimal `settlement.DAGAncestorReader` (`Get()` only) at `internal/settlement/generation_ledger_calculator.go:14-16`.
   - Generation-ledger BFS imports the consolidated reader from `internal/dag/`.

2. **`ReadAtAnchor` is an algorithm, not a new interface**: the consolidated reader's existing methods (`Tips`, `IsAncestor`, `Get`) compose into anchor-scoped BFS. F5 5A.3 specifies the algorithm; the interface gains no new method beyond what F3-B already shipped.

3. **Materialization-lag policy**: settler defers on `ErrEventNotFound`. V-1-preserving (per F5 5A.2 §7.2). Reuses F3-B causal-prerequisite-gating pattern (D-1 through D-8) as the established precedent.

4. **Quality function canonicalization (spec 5) deferred to future workstream**: F5 5A.3 specifies that `qualityFn` remains a constant-returning stub for F5 ship; future "real quality" lands behind the canonical-position-bound activation pattern (V-1) when the workstream that defines per-event quality semantics ships. Same pattern as F5 5A.2's `NeutralBPStubW`.

5. **Cycle/reciprocal-reference exclusion (spec 4) DEFERRED with risk assessment**: F5 5A.3 does NOT lock cycle exclusion semantics. The locked Reputation-and-Consensus-Integrity workstream's pair-aggregate + challenge-path mechanism (§3.2 + §4.4 of locked plan) is the architectural home for collusion detection; pulling it forward into F5 ancestry is out of scope. Risk: gen-ledger's narrow attack surface (2% of pool per accepted task settlement) bounds the damage; documented in §4.

6. **Cross-package coupling with F5 5A.2**: shared-primitive requirement satisfied. F5 5A.2 §7.2 V-1 ancestor check uses the consolidated `IsAncestor`. F5 5A.3 ancestry BFS uses the same consolidated reader. Single primitive serves both — Principle 6.

---

## 1. Background — what exists today

### 1.1 Generation-ledger calculator current state

`internal/settlement/generation_ledger_calculator.go`:
- Interface at `:14-16`: `DAGAncestorReader interface { Get(id event.EventID) (*event.Event, error) }` — minimal, single method, NO cutoff parameter.
- Constant at `:45`: `GenerationLedgerMaxDepth = 3` — locks BFS depth.
- BFS body at `:307-345`: walks ancestors via `e.CausalRefs` from a seed event (the submission event) to depth ≤ 3.
- `g.qualityFn(cur.id)` at `:319` (integer path) and `:199` (float path): per-ancestor quality lookup. Production wiring at `cmd/node/main.go:1937` always returns `protocolmath.NeutralBP`.
- Weight computation at `:328`: `q / (depth*depth)` — depth-squared decay applied to quality.
- Allocation at `:360`: `protocolmath.Allocate(pm, pool)` distributes the gen-ledger pool deterministically over the BFS-produced ancestor list.
- Float path at `:128-229` uses non-deterministic remainder absorption (5A.1 audit §4.3); excised post-shadowMode-removal alongside F5.

### 1.2 What F3-B already shipped (consolidation substrate)

`internal/dispatch/anchor.go:61-99`:
```go
type DAGAnchorReader interface {
    Tips() []event.EventID
    IsAncestor(ancestor, descendant event.EventID) (bool, error)
    Get(id event.EventID) (*event.Event, error)
}
```
Satisfied by `*dag.DAG` directly. Tested across 7 test cases for IsAncestor (irreflexive strict ancestor, reverse-direction false, sibling-branch false, materialization-lag returns `ErrEventNotFound`).

`VerifyAnchor` exported function reuses this interface for applicator + escrow startup-load DAG-anchor verification. F5 5A.3 reuses the same mechanism, broadened.

### 1.3 What F5 5A.3 contributes

- Consolidated `internal/dag/AnchorReader` interface (rename + relocation; no behavior change).
- `ReadAtAnchor(anchor, root, maxDepth) → []EventID` algorithm specification (built on existing methods). Returns the bounded anchor-scoped causal subgraph including root; not a strict-ancestors-only set.
- Generation-ledger BFS rewritten to use anchor-scoped reads with deterministic per-hop ordering.
- Materialization-lag deferral semantic (V-1-preserving).
- Cycle-exclusion deferral with risk assessment.
- Quality-function-stays-neutral specification with future-evolution path (matches F5 5A.2 stub-W pattern).

---

## 2. Spec 6 — ReadAtAnchor primitive (foundational)

### 2.1 Consolidated AnchorReader interface

Note on Tips() retention: Tips() remains in the consolidated interface for F5 5A.2 V-1 enforcement (currentAnchor lookups in admission paths, future use cases) and for F3-B `VerifyAnchor` reuse (which iterates current tips to confirm an anchor is still in the canonical lineage). 5A.3's `ReadAtAnchor` algorithm does NOT use Tips() directly; the method is preserved for shared-primitive Principle-6 reuse, not because gen-ledger BFS requires it.

Move `DAGAnchorReader` from `internal/dispatch/anchor.go` to `internal/dag/anchor_reader.go`:

```go
// internal/dag/anchor_reader.go
package dag

import "github.com/Aethernet-network/aethernet/internal/event"

// AnchorReader is the canonical interface for DAG anchor queries.
// Replaces the historical dispatch.DAGAnchorReader (per F5 5A.3
// consolidation; the dispatch-package placement was historically
// accidental). Replaces the minimal settlement.DAGAncestorReader
// (single Get() method) which is retired.
//
// Both F5 5A.2 §7.2 V-1 enforcement (canonical_ancestor check) and
// F5 5A.3 generation-ledger BFS consume this interface. Principle 6:
// one canonical primitive for DAG anchor queries.
//
// *dag.DAG satisfies this interface directly. Test stubs and harness
// adapters satisfy it via map[event.EventID]*event.Event helpers
// (see internal/verification/cross_node/cluster.go for the existing
// stub pattern).
type AnchorReader interface {
    // Tips returns the current DAG frontier in lex-sorted EventID
    // order (deterministic across nodes for identical DAG state).
    Tips() []event.EventID

    // IsAncestor reports whether `ancestor` is a strict ancestor of
    // `descendant` in the DAG.
    //
    // Returns false for: ancestor == descendant (irreflexive),
    // ancestor reachable only via reverse direction, sibling-branch
    // events with no ancestor relationship.
    //
    // Returns ErrEventNotFound if either ancestor or descendant is not
    // locally materialized. Callers MUST handle this error explicitly
    // — propagating wrong-direction defaults (e.g., returning false on
    // not-found) is a V-1 violation. See §2.3 for the canonical
    // materialization-lag deferral semantic.
    IsAncestor(ancestor, descendant event.EventID) (bool, error)

    // Get returns the event with the given ID, or ErrEventNotFound if
    // not locally materialized.
    Get(id event.EventID) (*event.Event, error)
}
```

**Rename + relocation. Zero new methods.** Migration:
- `internal/dispatch/anchor.go` — `DAGAnchorReader` deleted; `VerifyAnchor` updated to consume `dag.AnchorReader`.
- `internal/settlement/generation_ledger_calculator.go:14-16` — `DAGAncestorReader interface` deleted; replaced by `dag.AnchorReader` import.
- All call sites (`internal/settlement/applicator.go` startup-load, `internal/escrow/escrow.go` load, `internal/dispatch/dispatcher.go:182`, `internal/dispatch/logical_key_admit.go:41`) update imports; behavior unchanged.
- **Test adapters** (per ChatGPT Finding 4, Gate 5A.3 round 2): `internal/verification/cross_node/cluster.go` stub implementation updates import from `internal/dag/`. **Survey required as part of 5A.3 implementation**: any other test stubs currently satisfying `dispatch.DAGAnchorReader` or `settlement.DAGAncestorReader` (likely candidates: per-package `_test.go` files in dispatch, settlement, escrow, applicator, recognition; harness adapters under `internal/verification/`) update imports to `internal/dag/AnchorReader`. The implementation step that lands the consolidation must include the full test-adapter survey + update; missing one leaves a build error and a clear signal, but better to enumerate up-front.

### 2.2 ReadAtAnchor as an algorithm

**Important — terminology and handles** (per ChatGPT Finding 1, Gate 5A.3 round 2):

The BFS root parameter is **`R.SubmissionEventID`** (R's task-submission event), NOT `R.canonical_seal_context` (which is R's TVConsensus finalization event used for F5 5A.2 §7.2 V-1 activation-ancestor check). These are distinct canonical handles serving distinct purposes:

| Handle | Canonical event | Purpose | Where consumed |
|---|---|---|---|
| `R.SubmissionEventID` | The TaskSubmitted event (or task-submission canonical event for R) | BFS root for gen-ledger ancestry traversal | F5 5A.3 spec 6 (this section) |
| `R.canonical_seal_context` | R's TaskVerificationConsensus event | V-1 activation-ancestor check (stub-vs-real-W selection) | F5 5A.2 §7.2 |

**Implementers working across F5 5A.2 and 5A.3 MUST use the correct handle for each path.** Confusing them would either (a) make V-1 selection depend on a non-canonical-seal point, or (b) make BFS root the wrong anchor for ancestry traversal. Both produce wrong canonical state.

`ReadAtAnchor` is NOT a new method on `AnchorReader`. It is an ALGORITHM that uses the existing methods to produce a bounded anchor-scoped causal subgraph (BFS result), seed-inclusive — NOT a strict-ancestors-only set:

```
// ReadAtAnchor performs a bounded BFS from `root`, traversing only
// events that are canonical ancestors of (or equal to) `anchor`, up
// to maxDepth hops. Result is deterministic across nodes for
// identical canonical DAG state.
//
// SEED-INCLUSIVE: root is included from the first dequeue; result is
// the bounded anchor-scoped causal subgraph including root and
// (when reachable) anchor, NOT a strict-ancestors-only set.
//
// Anchor-in-result semantic per §2.2.1: when `anchor` is reachable
// from `root` within maxDepth hops, anchor IS included in the
// result (option a). Anchor's own CausalRefs are not traversed
// further because IsAncestor(anchor's_child, anchor) = false
// (anchor is not its own ancestor — irreflexive).
//
// Pseudocode:
ReadAtAnchor(reader AnchorReader, anchor, root EventID, maxDepth int) ([]EventID, error):
    visited := {root}
    result  := []
    queue   := [(root, depth=0)]

    while queue is non-empty:
        (cur, depth) := queue.pop_front()
        if depth > maxDepth: break

        ev, err := reader.Get(cur)
        if err == ErrEventNotFound:
            return nil, err  // signal materialization-lag — see §2.3
        if err != nil:
            return nil, err

        result.append(cur)  // SEED-INCLUSIVE: root appended on first dequeue;
                            // anchor appended when reached as a child.

        if depth == maxDepth: continue

        // Spec 3 — deterministic per-hop child enumeration:
        // Lex-sort CausalRefs by EventID before iterating (replaces
        // map-iteration which has non-deterministic order).
        children := lex_sort(ev.CausalRefs)
        for child in children:
            if child in visited: continue  // Spec 2 dedup

            // Anchor-scoped predicate: only follow children that
            // are canonical ancestors of (or equal to) `anchor`.
            // Filters out events causally beyond the anchor.
            // The `child == anchor` short-circuit includes anchor
            // itself as a leaf in the BFS tree (option a per §2.2.1).
            if child != anchor:
                isAnc, err := reader.IsAncestor(child, anchor)
                if err == ErrEventNotFound:
                    return nil, err  // materialization-lag
                if err != nil:
                    return nil, err
                if !isAnc: continue

            visited[child] = true
            queue.push_back((child, depth+1))

    return result, nil
```

The algorithm composes the existing `IsAncestor(child, anchor)` to bound the BFS to anchor-ancestors. No new interface method is required.

### 2.2.1 Anchor-in-result semantic — option (a) ANCHOR IS INCLUDED

When `anchor` is reachable from `root` within `maxDepth` hops, **anchor itself is included in the result slice**. The `child == anchor` special case in §2.2 enqueues anchor without an `IsAncestor` self-check (which would correctly return false per the irreflexive strict-ancestor semantic), then BFS dequeues anchor, appends it to `result`, attempts to enumerate its children — but every child of anchor fails the `IsAncestor(child, anchor)` predicate (descendants of anchor are not ancestors of anchor), so BFS naturally terminates the anchor's subtree.

**Decision per architect direction (Gate 5A.3 round 1)**: option (a) — anchor IS included. Anchor gets quality / depth² weight at allocation time like any other ancestor.

**Why (a) is correct**:
- This is the existing implementation behavior (the live BFS at `internal/settlement/generation_ledger_calculator.go:307-345` includes the seed event in its enumeration; the seed in the existing code is the submission event, not the anchor — but the structural pattern of "seed-is-included" carries over).
- Anchor semantically represents the cutoff snapshot (per F5 5A.2 §11.5), not a separate-from-evidence reference. If anchor is causally on the BFS path from R's submission event, it has economically participated in the trajectory; including it in the gen-ledger pool is consistent with the "ancestors that contributed to the work" framing.
- Excluding anchor would require a special-case allocation rule (skip-anchor) that doesn't exist today and would add complexity for marginal benefit.

**Allocation pseudocode confirmation**: the existing `protocolmath.Allocate(pm, pool)` at `internal/settlement/generation_ledger_calculator.go:360` distributes `pool` proportionally across all entries in `pm`. Each entry in `pm` is a `(CanonicalKey: ancestor.id, Weight: q / depth²)` tuple per `:353-359`. With anchor included in `result`, anchor gets `(CanonicalKey: anchor.id, Weight: q / depth²)` like any other ancestor. The weight is depth-bounded by anchor's BFS depth (from `root`), so anchor reachable at depth 2 weighs 10000/4 = 2500 BP; depth 3 weighs ~1111 BP. Allocation is canonical-derived.

**Property A-1**: if anchor is reachable from root within maxDepth hops, anchor appears exactly once in `result` (Spec 2 dedup ensures uniqueness) at its first-encounter depth (Spec 2 first-encounter-wins).

**Property A-2**: if anchor is NOT reachable from root within maxDepth hops, anchor does NOT appear in `result`. The BFS terminates normally; anchor was never enqueued. **Note (per ChatGPT Finding 2 clarification)**: A-2 only states that anchor itself doesn't appear. The result is NOT necessarily empty — root is always included (seed-inclusive per §2.2 algorithm), and other anchor-ancestors reachable from root within maxDepth may be present. A-2 is specifically about anchor's own membership, not about result emptiness.

**Property A-3** (boundary case): if `root == anchor`, `result == [anchor]` (one element). The BFS dequeues root, appends it, and finds no children that satisfy `IsAncestor(child, anchor=root)` (because no child of root is an ancestor of root). This is the degenerate single-event case; allocation distributes the entire pool to anchor.

### 2.3 Materialization-lag deferral semantic (V-1-preserving)

When `ReadAtAnchor` (or any caller of `IsAncestor`) returns `ErrEventNotFound`, the caller MUST defer the operation rather than return a guessed value. F5 5A.2 §7.2 specifies the same semantic for V-1 ancestor checks; F5 5A.3 inherits and extends to the gen-ledger BFS path.

**Deferral mechanism** (mirrors F3-B causal-prerequisite-gating D-1 through D-8):

```
settle_round(R):
    ancestors, err := ReadAtAnchor(
        reader, cutoff_anchor_for(R), R.SubmissionEventID, MaxDepth=3,
    )
    if err == ErrEventNotFound:
        // One or more events on the BFS path are not yet locally
        // materialized. V-1 forbids returning false (would couple
        // selection to local materialization state) or guessing
        // (would produce wrong canonical state). Only V-1-preserving
        // semantic: defer R until materialization completes.
        defer_round_settlement(R, reason="ancestry materialization lag")
        return  // settler retries R when local DAG advances
    if err != nil:
        propagate_error(err)
        return
    // ... continue settlement using ancestors
```

The deferral mechanism uses the same recognition-fabric retry path the F3-B causal-prerequisite-gating pattern uses (per `docs/plans/2026-04-15-f3b-part-d-causal-prerequisite-gating.md`). Settler emits a "deferred settlement" record; the recognition fabric retries when DAG state advances.

**Why this is V-1-preserving**: the selection of which W implementation to use (F5 5A.2 §7.2) and the selection of which ancestor set to compute (F5 5A.3 BFS) both depend on canonical state. If a node cannot evaluate canonical state because of local materialization lag, deferring is the only correct response — the answer that would be produced once materialization completes is the canonical-state answer, byte-identical across nodes. Returning false or guessing produces a per-node wall-clock-coupled answer, which V-1 forbids.

### 2.4 Cross-reference to F5 5A.2 §7.2

F5 5A.2 §7.2 V-1 enforcement uses `IsAncestor(ReputationActivation, R.canonical_seal_context)` for W implementation selection. F5 5A.3 specifies the consolidated reader that provides this method; the materialization-lag deferral semantic is the same in both directions.

The two cross-cutting uses:

| Caller | Method invocation | Purpose |
|---|---|---|
| F5 5A.2 derivation function | `IsAncestor(ReputationActivation_event_id, R.canonical_seal_context)` | Select stub-W vs real-W per V-1 |
| F5 5A.3 generation-ledger BFS | `IsAncestor(child, cutoff_anchor_for(R))` repeatedly during anchor-scoped traversal | Bound BFS to anchor-ancestors |

Both use the consolidated `dag.AnchorReader.IsAncestor`. Both defer R's settlement on `ErrEventNotFound`. Both produce byte-identical results across nodes given identical canonical state.

---

## 3. Spec 3 — Traversal order (BFS, lex-sort on EventID at each hop)

### 3.1 The non-determinism problem

Today's `internal/settlement/generation_ledger_calculator.go:307-345` BFS iterates `e.CausalRefs` in slice order. `CausalRefs` is set at event creation; for a given event the slice content is canonical (same on every node). However, downstream allocators (e.g., `protocolmath.Allocate`) sort by CanonicalKey before allocation, so slice-order non-determinism in the BFS doesn't necessarily reach the payout output today.

The float path at `:229` is the actual non-determinism: it absorbs rounding remainder at the last-in-traversal-order recipient, which depends on BFS order. The integer path sorts before allocation, masking the BFS-order issue.

Per Grok's forward note #2: BFS traversal order MUST be **explicitly lex-sorted on EventID at each hop**, regardless of the downstream allocator's behavior. This is an explicit safety belt: even if a future allocator-rewrite removes the downstream sort, the BFS itself is deterministic.

### 3.2 Specification

**At each BFS hop**, the children to enqueue are derived from `event.CausalRefs`. Before enqueuing, **sort children by EventID lex order** (the deterministic ordering that aligns with all canonical primitives in the codebase — same sort key used by `protocolmath.AllocateWithCeiling` for recipient ordering; same key used by F4's `dag.Tips()` per E.P2.A1).

Pseudocode (extracted from §2.2):
```
children := lex_sort(ev.CausalRefs)
for child in children:
    if child in visited: continue
    if !IsAncestor(child, anchor): continue
    visited[child] = true
    queue.push_back((child, depth+1))
```

`lex_sort` is byte-string lex order on the EventID (which is a `string`-typed alias for the canonical content-addressed identifier).

### 3.3 Determinism property

**Property T-1**: For any two nodes processing the same `(anchor, root, maxDepth)` against the same canonical DAG state, the BFS visits events in identical order and produces identical `result` slices (same elements in same order).

T-1 is the load-bearing guarantee. Downstream allocators MAY further sort the result, but T-1 ensures the input to the allocator is already deterministic.

### 3.4 Why lex on EventID, not on CausalTimestamp

EventIDs are content-addressed (SHA-256 hex of canonical event content per `internal/event/event.go:221`). Lex-sorting on EventID is determined by the events' content; for events created by independent agents, the order has no protocol semantic meaning, but it is canonical.

CausalTimestamps could also be used (Lamport timestamps incremented monotonically), but Lamport ties (multiple children of the same parent) require an EventID tiebreak anyway. Using EventID directly is simpler and avoids the tiebreak path.

The codebase precedent (per F3-B + F4): EventID-lex is the canonical sort key for cross-node-deterministic ordering. F5 5A.3 follows the precedent.

---

## 4. Specs 1 + 2 — Ancestor selection + dedup semantics

### 4.1 Spec 1 — Ancestor selection per hop

**Selection rule**: at each hop, **all** of `ev.CausalRefs` are candidates. No filtering by weight, age, or other property. The BFS enqueues every child that:
1. Is not already visited (Spec 2 dedup, §4.2).
2. Is a canonical ancestor of `anchor` per the anchor-scoped predicate (§2.2).
3. Has depth ≤ MaxDepth.

Per locked plan §3.3 specification 1: "Ancestor selection semantics per hop: given a node with multiple ancestor candidates, which are selected? All? Highest-weight? Oldest?" Answer: **All**. Locked plan v3 lists "all" as one of the candidates; F5 5A.3 picks it as the simplest deterministic-by-construction policy.

**Why "all" rather than a filtering rule**:
- Filtering by weight would require evaluating quality before BFS completes, creating an ordering dependency between spec 5 (quality) and the traversal itself.
- Filtering by age would couple the traversal to canonical timestamps, a complication for marginal benefit.
- Selecting all + applying depth-squared decay at allocation time (existing `:328`) gives the same economic effect as "weight by closeness" without the spec interaction.

Selecting all is also the existing behavior; spec 1 is a confirmation rather than a change.

### 4.1.1 Size bound on `all` — verification finding

The `all` selection is safe under bounded `CausalRefs` size. F5 5A.3 surveyed the codebase for an event-validation size bound and **found no explicit limit**:

- `internal/event/event.go:243` declares `CausalRefs []EventID` with no length constraint.
- `internal/event/trajectory.go:87-96` validates trajectory payloads but does not bound CausalRefs.
- `internal/dag/dag.go:187+214` enforces "every ref must already be present in the DAG" (causal validation per `dag.go:163`) but does not enforce a maximum slice length.

**Worst-case bound under valid events**: with no length cap, an adversary crafting an event with N CausalRefs (each referencing a valid prior event) produces a BFS that enumerates O(N) events at depth 1, O(N²) at depth 2, O(N³) at depth 3. With `MaxDepth = 3` (per `internal/settlement/generation_ledger_calculator.go:45`), worst case is **O(N³)** before Spec 2 dedup terminates the explosion.

**Practical impact today**: no observed pathologically-large events in production. Typical settlement events have 1–4 CausalRefs (root events have 0; semantic-parent pattern keeps ref counts small). Adversarial event construction is theoretically possible but has no current incentive — gen-ledger 2% pool gain doesn't outweigh the gas/storage cost of inflating event size.

**Disposition**: surfaced as new §9.4 open question for architect attention. **NOT a halt-trigger for 5A.3** because:
- This is an event-validation concern (where to enforce a `MaxCausalRefs` constant), not a settlement-derivation concern.
- 5A.3's traversal is deterministic and correct under any CausalRefs size; only the cost is unbounded.
- The fix (add `MaxCausalRefs = K` to event validation) is a small change in `internal/event/` and orthogonal to F5's scope.

Per architect direction: surface, don't halt.

### 4.2 Spec 2 — Dedup semantics

When the same ancestor is reachable via multiple paths in the BFS:
- **First-encountered (in BFS order) wins.** The ancestor enters `visited` on first encounter; subsequent paths to the same event are skipped.

Per locked plan §3.3 specification 2: "When the same ancestor is reachable via multiple paths, how is it counted? First-path-wins? All-paths-contribute? Canonical merge with specified weighting?" Answer: **First-path-wins** with BFS order determined by Spec 3 (lex-sort on EventID at each hop).

**Determinism**: combined with T-1 (Spec 3 traversal order is deterministic), the first-encounter is also deterministic — the BFS visits children in lex order at each hop, so "first encountered" is byte-identical across nodes.

**Economic property**: an ancestor reachable via multiple paths gets weight only from its first-encountered depth. Closer paths win (BFS visits closer paths first). This matches the "closer ancestors get more weight" intuition implicit in depth-squared decay.

**Rationale vs all-paths-contribute**: all-paths-contribute would require defining a merge function (sum? average? max?) and would amplify weight for highly-connected ancestors, creating a centrality bias. First-path-wins is simpler, deterministic, and the existing implementation's behavior.

---

## 5. Spec 4 — Cycle / reciprocal-reference exclusion (DEFERRED with risk assessment)

### 5.1 The problem

The existing code at `internal/settlement/generation_ledger_calculator.go` has a `// TODO` for cycle/collusion-ring exclusion. The concern: if Agent A approves Agent B's task and Agent B approves Agent A's task, both gain gen-ledger weight from each other's tasks, creating a self-reinforcing collusion ring.

Plan v3 §3.3 specification 4: "Cycle and reciprocal-reference exclusion: the code has a TODO for cycle/collusion-ring exclusion. 5A.3 must decide: lock exclusion semantics for F5, or explicitly defer with documented risk assessment."

### 5.2 Decision: defer with risk assessment

F5 5A.3 **explicitly defers cycle/reciprocal-reference exclusion** to the locked Reputation-and-Consensus-Integrity workstream's challenge path (next workstream after locked per locked plan §17 step 14).

**Rationale**:

- The locked plan §3.2 already specifies the **`ReputationPairAggregate`** primitive — pair-aggregates that detect coordinated voting patterns. This is the canonical primitive for collusion-ring detection. Per locked plan §4.4 (Challenge Alert projection): "Excess co-agreement against base rate", "Identical-deviation pattern", "Abstention coupling", "Cross-axis coupling" — all generated from pair aggregates. The mechanism that ACTS on these alerts is the challenge-path workstream, scoped out of the locked workstream.

- Pulling cycle exclusion into F5 5A.3 would require either: (a) re-implementing pair-aggregate detection ahead of the locked workstream (Principle 6 violation), OR (b) introducing F5-local heuristics for cycle detection that the locked-workstream pair-aggregate would later supersede (waste).

- F5 5A.3's job is to make ancestry traversal canonical and deterministic. Once traversal is canonical, a future cycle-exclusion rule can be applied on top deterministically. F5 5A.3 produces the substrate; the challenge-path workstream produces the policy.

### 5.3 Risk assessment

**Attack surface bound**: gen-ledger pool is **2% of accepted task budget** per `internal/settlement/verification_consensus_settler.go:22` (`generationShareBP = 200`). For a typical task with budget 1M µAET, gen-ledger pool is 20K µAET. Even a fully-successful collusion ring captures only this 2%; the worker (73%) and validator (23%) shares are unaffected.

**Detection latency**: pair-aggregate accumulation per locked plan §3.2 begins as soon as Step 2 retrofit ships (already implemented per Item 1 of pre-design verification). Detection alerts begin firing at the locked workstream's tier-2 observability layer (per §10.1) before the challenge-path workstream ships. Operators can see suspicious patterns even without canonical action.

**Forward-only damage**: F5's testnet wipe at main merge (Plan v3 §0.4) means historical collusion accumulation is not preserved; production cluster starts fresh. Cycle-exclusion lands before substantial production gen-ledger volume accumulates.

**Halt condition assessment**: per Plan v3 §3.3 halt clause, "if cycle exclusion requires broader reputation architecture, narrow F5 ancestry scope to advisory royalty / even-split or skip generation-ledger payouts until a future workstream." F5 5A.3 does NOT need to halt because the gen-ledger pool retains its 2% allocation under canonical-but-cycle-permissive semantics — the locked workstream's pair-aggregate machinery is the right home for cycle detection, and that work is already in scope (steps 1-2 implemented; pair-aggregate primitive is ready for the challenge-path workstream to consume).

**Documented residual**: a coordinated collusion ring can capture up to 2% of accepted-task budgets until the challenge-path workstream ships. This is an acknowledged temporary residual, the same kind of explicit deferral the locked plan §8.4 documents for systematic-divergence slashing.

### 5.4 Future canonicalization path

When the challenge-path workstream ships, cycle exclusion in gen-ledger BFS becomes:

```
// Future addition to ReadAtAnchor (post-challenge-path workstream):
for child in children:
    if child in visited: continue
    if !IsAncestor(child, anchor): continue
    if challenge_resolution_excludes(child, R): continue  // NEW
    visited[child] = true
    queue.push_back((child, depth+1))
```

`challenge_resolution_excludes(child, R)` would consult the `ChallengeResolutionStore` (locked plan §2.3) for any confirmed collusion findings affecting `child` that should exclude it from `R`'s gen-ledger payouts. Pure canonical-state function; deterministic across nodes.

This future addition is COMPATIBLE with F5 5A.3's design (just an additional predicate in the BFS); it is NOT required by F5 to ship.

---

## 6. Spec 5 — Quality function canonicalization

### 6.1 Current state

`g.qualityFn(cur.id)` at `:319` returns `protocolmath.BasisPoints`. Production wiring at `cmd/node/main.go:1937` is a stub returning `NeutralBP` (10000) for every ancestor.

### 6.2 Specification — qualityFn stays neutral; future swap follows V-1 pattern

F5 5A.3 specifies that `qualityFn` is a **stub returning NeutralBP** for F5 ship. This matches the existing production behavior; no behavioral change.

The future evolution path mirrors F5 5A.2's `NeutralBPStubW` → real-W pattern:

1. **F5 ships with `NeutralQualityStub`** (analogous to `NeutralBPStubW`). All ancestor lookups return NeutralBP. Behaviorally equivalent to today; structurally explicit (named stub, not anonymous closure).

2. **Future "quality activation event"**: a separate canonical activation event (analogous to `ReputationActivation`) emitted by a future workstream that defines per-event quality semantics. The interface contract is `CanonicalQualityProjection.Lookup(event_id, cutoff_epoch) → uint64` (or whatever signature the future workstream specifies — F5 5A.3 does NOT lock this interface; it locks only that the swap follows V-1).

3. **V-1-preserving swap**: F5's BFS uses a canonical-position-bound check (analogous to F5 5A.2 §7.2) to select stub vs real quality. Selection is by R's canonical position relative to the quality activation event, NOT by runtime flag.

### 6.3 Why NOT design real quality in F5 5A.3

Per architect direction (Plan v3 §3.3 spec 5): "When real quality is wired, must be canonical-live with specified retrieval mode. This depends on Q-C (canonical Q projection) being in place — quality is Q-weighted."

F5 5A.2 specifies Q-C (the W projection). But:
- Per F5 5A.2 §10 (now collapsed): gen-ledger quality is per-ancestor-event (task-execution-quality), distinct from per-validator W (vote-agreement). Different metric, different evidence domain.
- Designing real per-event quality in F5 5A.3 would require defining a new evidence domain (what canonical events update per-event quality?) and a new aggregation rule (how does an ancestor event's quality derive from canonical state?). This is comparable in scope to the entire locked Reputation-and-Consensus-Integrity workstream — too much for F5 5A.3.

F5 5A.3 specifies the SHAPE (V-1-preserving swap behind a canonical interface) and defers the IMPLEMENTATION to the future workstream that owns per-event quality. Same pattern F5 5A.2 used for W (define interface; ship stub; locked workstream implements real).

### 6.4 NeutralQualityStub specification

```go
// NeutralQualityStub is F5 Phase 5B's stub implementation of the
// generation-ledger quality function. Returns NeutralBP (10000) for
// all ancestor events, matching today's production behavior at
// cmd/node/main.go:1937.
//
// SUPERSEDED by a future canonical quality projection for any round R
// where the corresponding quality-activation event is a canonical
// ancestor of R's settlement context (V-1 invariant per F5 5A.2 §7.1
// applied to gen-ledger quality activation). The selection between
// stub and real-quality is canonical-position-bound; no runtime flag.
type NeutralQualityStub struct{}

func (NeutralQualityStub) Lookup(
    ancestorID event.EventID,
    cutoffEpoch uint64,
) (uint64, error) {
    return 10000, nil  // NeutralBP
}
```

The stub takes a `cutoffEpoch` parameter (matching `CanonicalWProjection`'s signature shape) so the interface is consistent with F5 5A.2's pattern. Real-quality will use `cutoffEpoch` for historical-read at the canonical cutoff.

**Future-evolution note (per ChatGPT Finding 3, Gate 5A.3 round 2)**: future family/category-sensitive quality (or any extension of this signature) is a **new interface version per F5 5A.2 §7.5 version-binding rule**, NOT a source-compatible extension of this V1 signature. The V1 signature `(ancestorID, cutoffEpoch) → (uint64, error)` ships locked; if a future workstream needs richer parameters, it defines `CanonicalQualityProjectionV2` (or equivalent) with a fresh canonical-activation cutover. This matches the F5 5A.2 §7.5 pattern of explicit version-binding at activation events; prevents implicit signature drift across activations.

### 6.5 Effect on weight computation

The existing weight at `:328` is `q / (depth*depth)`. With `q == NeutralBP == 10000`, weight per ancestor at depth d is `10000 / d²` — depth-squared decay applied to a constant. Ancestors at depth 1 weigh 10000; depth 2 weigh 2500; depth 3 weigh ~1111. The locked allocator (`protocolmath.Allocate`) distributes the gen-ledger pool proportionally.

This is the existing economic shape; F5 5A.3 does not change it. The shape is canonical (deterministic from BFS structure + constant quality + locked allocator) under stub-quality.

When real quality lands, the shape changes: ancestors with high quality get more weight at their depth; low quality less. The depth-squared decay still applies. Cluster-uniformity is preserved by the V-1-bound swap.

---

## 7. Forward notes integrated

### 7.1 Forward note #1 — materialization-lag in genesis replay

Per Gate 5A.2 §15.4 Grok prediction: "a node replaying from genesis may not have all ancestors materialized at the moment it processes a generation-ledger settlement; `DAGAnchorReader.ReadAtAnchor` must define behavior."

**Integrated into §2.3**: settler defers on `ErrEventNotFound`; reuses F3-B causal-prerequisite-gating pattern. During genesis replay, deferred rounds are retried as DAG materialization advances; eventual materialization always completes (canonical state is consistent), so all rounds eventually settle byte-identically to steady-state nodes.

**Replay sequence**: replayer processes events in canonical order. When a TVConsensus event for round R is processed, the replayer attempts gen-ledger BFS via `ReadAtAnchor`. If any ancestor on the BFS path is not yet materialized (because the canonical event ordering placed it after R but the replayer is still catching up), the BFS returns `ErrEventNotFound`; settler defers. The replayer continues processing later events; when all of R's BFS path is materialized, settler retries and succeeds.

**Determinism property**: replay produces byte-identical settlement results to steady-state, regardless of the order in which deferred rounds eventually retry. The retry simply waits for materialization; the result is canonical.

### 7.2 Forward note #3 — first-round-of-epoch boundary race interaction with ReadAtAnchor cutoff

Per Gate 5A.2 §15.4 Grok prediction: "a generation-ledger ancestor BFS for a round R settling in epoch E uses `ReadAtAnchor(cutoff_for(E))`. The cutoff anchor for E is the snapshot at end-of-(E-1). If the ancestor traversal needs events that are canonically positioned WITHIN epoch E (after the cutoff), the cutoff binding must be unambiguous."

**Integrated answer**: F5 5A.3's BFS uses **R's submission event ID as `root`** and **cutoff_anchor_for(R) as `anchor`**, where `cutoff_anchor_for` is per F5 5A.2 §11.4 (epoch index of R's snapshot — formally, the canonical event corresponding to the snapshot taken at end of R's immediately-prior epoch).

The BFS walks ancestors of `root` (R.SubmissionEventID) BUT ONLY traverses events that are also canonical ancestors of `anchor`. This bounds the traversal to events canonically positioned at-or-before the cutoff.

**Edge case — task posted late in epoch E uses ancestors from epoch E-1**:
- R is in epoch E. R's submission event is in epoch E.
- The BFS traverses backward from R's submission event.
- Ancestors at depth ≥ 1 are from earlier rounds; if they are canonically positioned in epoch E, they may or may not be ancestors of cutoff_anchor (which is the epoch-E-1 snapshot canonical event).
- Per the anchor-scoped predicate: ancestors NOT in the canonical past of cutoff_anchor are EXCLUDED.

**Implication**: a task posted late in epoch E whose immediate causal-ref is another task posted earlier in epoch E will see the BFS exclude the in-epoch ancestor (because it's not in the cutoff's canonical past). This is correct under epoch-coarse cutoff semantics — within-epoch state cannot influence within-epoch settlements.

**Alternative considered and rejected**: making the cutoff round-precise (R's own canonical position rather than epoch boundary). Rejected for the same reasons F5 5A.2 §11.2 rejected round-precise W cutoff: locked plan's snapshot framework is epoch-coarse; round-precise would force per-round projection storage and break alignment with W.

**Documented behavior**: ancestors canonically positioned in the same epoch as R but not in cutoff_anchor's past are EXCLUDED from gen-ledger payouts. This is a deliberate consequence of epoch-coarse cutoff alignment with W.

---

## 8. Halt-trigger assessment

Five triggers per architect direction (5A.3 message), evaluated against the completed draft:

| Trigger | Fired? | Rationale |
|---|:-:|---|
| Cycle/collusion-ring exclusion (spec 4) cannot be made fully deterministic within F5 scope | NO | Deferred with risk assessment per §5.2; locked workstream's pair-aggregate + challenge-path is the canonical home for cycle detection. F5 5A.3's substrate (canonical traversal + anchor-scoped reader) is compatible with future cycle exclusion; doesn't preclude or delay it. Risk bounded by gen-ledger 2% pool. |
| ReadAtAnchor primitive cannot be designed without broader DAG-reader refactor | NO | Consolidation reuses existing F3-B `dispatch.DAGAnchorReader` (rename + relocation). Algorithm built on existing methods; no new method introduced. |
| Materialization-lag behavior cannot be made deterministic across nodes | NO | Deferral semantic per §2.3: settler defers on `ErrEventNotFound`; reuses F3-B causal-prerequisite-gating pattern. V-1-preserving. Eventual canonical materialization gives byte-identical results across nodes. |
| Quality function's CanonicalWProjection coupling introduces dependency F5 5A.2 didn't scope | NO | F5 5A.3 defers real quality to a future workstream. Stub returns NeutralBP (existing production behavior). Future swap follows V-1 pattern (analogous to F5 5A.2's stub-W). No new dependency on F5 5A.2's CanonicalWProjection. |
| Any spec forces a change to F3-B, F4, or F5 5A.2 V-1 | NO | F3-B `VerifyAnchor` continues unchanged (consumes the consolidated reader; behavior preserved). F4 invariants unchanged. F5 5A.2 V-1 reinforced by 5A.3's compatible deferral semantic and shared primitive. |

**No halt-trigger fired. Draft complete; ready for Gate 5A.3 review.**

---

## 9. Open questions — all resolved at Gate 5A.3 (round 1 architect + round 2 multi-AI)

### §9.1 ReadAtAnchor result ordering — RESOLVED (round 1)

**Architect direction (round 1)**: visit-order is fine. T-1 makes it deterministic. No final sort needed. Design as-drafted.

### §9.2 NeutralQualityStub interface lock — RESOLVED (round 1 + round 2 strengthening)

**Architect direction (round 1)**: ship minimal signature `(ancestorID, cutoffEpoch) → (uint64, error)`. Future workstream extends via new interface version per V-1's version-binding rule (F5 5A.2 §7.5). Do NOT speculatively add parameters now.

**ChatGPT Finding 3 (round 2)**: §6.4 strengthened with explicit "future family/category-sensitive quality is a new interface version per F5 5A.2 §7.5 version-binding rule, NOT a source-compatible extension of this V1 signature."

### §9.3 Cycle exclusion transparent-residual framing — RESOLVED (round 1 + Grok round 2 wording)

**Architect direction (round 1)**: accept transparent-residual framing. Add to F5 ship docs a one-paragraph note in locked-plan §8.4 honesty-clause pattern.

**Grok round 2 wording** (final wording for the F5 ship docs note, captured in F5 completion gate report Gate-report note 1):
> "Until the challenge-path workstream ships, gen-ledger royalty (2% of accepted-task budgets) may be captured by coordinated collusion rings that the pair-aggregate detection layer has not yet acted upon. This is an acknowledged, time-bounded residual. Operators receive pair-aggregate alerts today; no economic loss is invisible."

The note will be added to the F5 completion gate report (5A end) and F5 ship documentation, not to this 5A.3 design doc directly. Captured here for forward-traceability.

### §9.4 Unbounded `CausalRefs` size — RESOLVED (round 1 + round 2)

**Round 1 disposition**: surface but DO NOT halt 5A.3. Event-validation concern, not settlement-derivation concern.

**Architect direction (round 2)**: forward-noted to 5A.4 as a polish item or follow-on event-validation workstream. `MaxCausalRefs` constant (suggested 8 or 16, alongside locked plan §16's `MaxFamilies = 16` / `MaxCategories = 64`) goes into 5A.4 scope planning when that phase begins. Architecturally correct fix is event-validation hardening; settlement-time guard band-aid rejected.

---

**All §9 questions resolved. No remaining open architectural questions for 5A.3.**

---

## 10. Cross-references summary

This design composes cleanly with prior phase work. Key cross-references:

| Reference | Purpose |
|---|---|
| F5 5A.2 §7.1 V-1 invariant | Canonical-position-bound selection — F5 5A.3 quality stub follows same pattern |
| F5 5A.2 §7.2 enforcement mechanism | `IsAncestor(ReputationActivation, R.canonical_seal_context)` — uses consolidated reader |
| F5 5A.2 §11 cutoff_anchor_for | Epoch-coarse cutoff — F5 5A.3 BFS consumes the same cutoff |
| F5 5A.2 v2.1 §7.2 deferral pseudocode | Materialization-lag deferral — F5 5A.3 inherits and extends to BFS |
| F3-B Parts A/B §2.1 VerifyAnchor | Consolidated reader interface origin |
| F3-B Part D causal-prerequisite-gating | Deferral pattern precedent (D-1 through D-8) |
| Plan v3 §3.3 specifications 1-6 | Six specifications addressed in this document |
| Plan v3 §3.3 halt clause | Cycle exclusion deferral within scope (no halt) |
| Locked plan §3.2 ReputationPairAggregate | Future home for cycle detection |
| Locked plan §4.4 Challenge Alert (C projection) | Detection mechanism the challenge-path workstream consumes |
| Locked plan §8.4 honesty clause | Precedent for documented temporary residual |

---

## 11. Next step: Gate 5A.3 architect + multi-AI review

Per Plan v3 §12 step 7-8: "Gate 5A.3 — architect review. 5A.4 can proceed in parallel with 5A.3 since synthetic-ID work doesn't depend on ancestry."

Gate 5A.3 conditions:
- ✅ All six specifications addressed.
- ✅ Two Grok forward-notes integrated (materialization-lag, first-round-of-epoch boundary race).
- ✅ Shared-primitive consolidation specified (DAGAnchorReader → `internal/dag/`; `settlement.DAGAncestorReader` retired).
- ✅ Cross-references to F5 5A.2 §7.2 V-1 invariant landed.
- ✅ Halt-trigger assessment complete; no trigger fired (§8).
- ⏳ Multi-AI review (Grok + ChatGPT) per same discipline as Gate 5A.1 / 5A.2.

After multi-AI review absorbed:
- Gate 5A.3 closes.
- 5A.4 begins (per Plan v3 §12: 5A.4.a schema-lock first, then 5A.4.b synthetic-ID refactor + consumer audit + CI lint, in parallel with 5A.3 implementation if not already executing).

### 11.1 Multi-AI review prompt structure (suggested)

Following the standing-instructions pattern from Gates 5A.1 and 5A.2:

- **Grok**: push on ambition and scope. Is the cycle-exclusion deferral risk-justified, or does the 2% gen-ledger pool argument under-state the attack surface? Is the materialization-lag deferral semantic actually safe under all replay scenarios (network partition, node desync, snapshot restore from peer)? Are the V-1 cross-references between 5A.3 and 5A.2 watertight? Is the unbounded-CausalRefs concern (§9.4) under-stated — should F5 5A.3 add a settlement-time guard rather than deferring to event-validation hardening?

- **ChatGPT**: push on structural correctness. Is the consolidated AnchorReader interface semantically equivalent to what callers expected from the old interfaces? Is the BFS algorithm in §2.2 deterministic in all corner cases (empty CausalRefs, self-referencing event, anchor unreachable, root == anchor)? Is the NeutralQualityStub interface signature consistent with future-evolution expectations? Are properties A-1, A-2, A-3 (anchor-in-result invariants per §2.2.1) covered by the BFS algorithm exactly?

- **Both reviewers — explicit pressure-test on the epoch-coarse cutoff payout consequence (§7.2)**: a task posted later in epoch E whose immediate causal-ref is another task posted earlier in epoch E will see the BFS exclude the in-epoch ancestor, because that ancestor is not in the cutoff anchor's canonical past (cutoff is end-of-epoch-E-1). The architect framed this as a deliberate consequence of epoch-coarse cutoff alignment with W. **Pressure-test whether this payout semantic is desirable or surprising; if surprising, surface as a real design decision to revisit, not as a documentation gap.** Two related sub-questions: (1) does this exclude legitimate gen-ledger contributions from same-epoch ancestors, deflating the 2% pool's distribution? (2) does this create an incentive to post tasks at specific times within an epoch to maximize gen-ledger inclusion?

---

**End of Generation-Ledger Canonical Derivation — draft v1, 2026-04-23. Ready for Gate 5A.3 review.**
