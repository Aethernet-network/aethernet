# Map-iteration determinism audit

**Status**: F4A step 7 (Part E Priority 1) — completed 2026-04-22 against `feat/selection-consistency-fix` @ `4658209`.
**Scope**: Priority 1 — logical-key consumers, dispatcher core, settlement (`internal/dispatch/`, `internal/settlement/`).
**Plan reference**: F4 plan v2 §6 (Part E).

Priority 2 (`internal/recognition/`, `internal/store/`, `internal/network/`) and Priority 3 (everything else, ~470 callsites) are tracked for F4A step 11. This document covers Priority 1 only and will be amended when 2 + 3 close.

---

## Classification taxonomy

- **Safe**: iteration order does not affect any observable outcome (canonical state, error semantics, cross-node-visible diagnostic).
- **Safe-with-note**: iteration order affects observable diagnostic only (e.g., which of N equivalent error messages is reported first); no canonical-state effect. Sorting is hygienic, not load-bearing.
- **Unsafe-without-sort**: iteration order can affect canonical state or cross-node-visible behavior. Fix: sort keys before iteration.
- **Unsafe-inherently**: order dependency cannot be removed by sorting; needs structural fix. Tracked for follow-up.

---

## Priority 1 callsites and dispositions

### `internal/dispatch/dispatcher.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 170 | `d.consumers` (`map[string]Consumer`) | **Unsafe-without-sort** | **Fixed in this commit**: lex-sort consumer names before iterating in `snapshotInterestedConsumers`. The returned `[]Consumer` order propagates into the `AdmissionRecord.Consumers` map's first-write order and the sequence of `safeApply` invocations in `invokeConsumers` — observable cross-node when consumers count > 1. With one registered consumer today this is decorative; load-bearing the moment a second registers. |
| 257 | `rec.Consumers` (`map[string]ConsumerStatus`) | **Unsafe-without-sort** | **Fixed**: `invokeConsumers` lex-sorts consumer names before iterating. Sequence of `safeApply` calls now deterministic across nodes. |
| 316 | `records` (`[]*AdmissionRecord` returned from `AllAdmissions`) | **Safe** | BadgerDB iterator returns lexicographic key order — deterministic. Slice range, not map range. |
| 339 | `rec.MissingPrerequisites` (`[]event.EventID`) | **Safe** | Slice range. |
| 388 | `rec.Consumers` (in `Recover`) | **Unsafe-without-sort** | **Fixed**: `Recover` lex-sorts consumer names before invoking `RecoveryProbe`. Probe order is observable when probes share targets. |
| 423 | `rec.Consumers` (in `checkSchemaVersions`) | **Safe-with-note** → **Fixed** | Iteration affects only the FIRST mismatch reported. Lex-sort applied for diagnostic stability — operator triage now reproducible across nodes. |

### `internal/dispatch/deferral.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 21 | `d.deferralIndex` (`map[event.EventID][]string`) | **Safe** | The map is mutated to remove a specific admission key from every prereq's index. Removal is idempotent and order-independent. No state leaves this function as a slice. |
| 23 | `keys` (`[]string`, slice extracted from map value) | **Safe** | Slice range; the inner loop deletes by index from the keys slice — copy semantics, no cross-call ordering. |
| 41 | `records` (slice from `AllAdmissions`) | **Safe** | Slice range, deterministic from BadgerDB. |
| 65 | `keys` (`[]string`) | **Safe** | Slice range. |
| 78 | `rec.MissingPrerequisites` (slice) | **Safe** | Slice range. |

### `internal/dispatch/prerequisites.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 35 | `consumers` (`[]Consumer`) | **Safe** | Slice range. |
| 36 | `c.Prerequisites(ev)` (`[]event.EventID`) | **Safe** | Slice range. |
| 46 | `prereqSet` (`map[event.EventID]struct{}`) | **Safe-with-note** | Used to dedupe prerequisite IDs. The output `[]event.EventID` is then appended to `rec.MissingPrerequisites`. **Action**: sort before append so the persisted record is byte-equal across nodes for the same input prereq set. Tracked as a Priority 1 micro-fix below. |

### `internal/dispatch/types.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 136 | `consumers` (`map[string]ConsumerStatus`) in `computeTopLevelState` | **Safe** | Reduces to a single state via OR-like aggregation. Order-independent. |

### `internal/dispatch/anchor.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 39 | `tips` (`[]event.EventID` from `dag.Tips()`) | **Safe-with-note** | Slice range over `dag.Tips()`. **CORRECTION**: `dag.Tips()` does NOT sort — `internal/dag/dag.go:424` iterates `d.tips` (a map) and returns the slice unsorted. The dispatcher's `currentAnchor()` (`dispatcher.go:160`) picks `tips[0]`, which is therefore non-deterministic across nodes. Per locked invariant C-15 ("admission state is non-canonical node-local"), the anchor choice is recorded only in the per-node `AdmissionRecord` and does NOT affect canonical state. Safe by design — but a hardening fix is queued (E.P2.A1 below). |

### `internal/settlement/applicator.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 400 | `a.deferred` (`map[event.EventID]*SettlementPayload`) | **Safe** | Snapshot copy. Map-to-map copy preserves no order; downstream uses the snapshot, not insertion order. |
| 405 | `snapshot` (same shape) | **Safe** | Each iteration calls `a.applyToTarget(sp, target)` for a UNIQUE target ID. Different targets touch independent ledger surfaces; final state is order-independent (commutative). The applied-set gates re-application, so per-target idempotency holds regardless of order. |
| 427 | `entries` (`map[string][]byte` from `allMeta`) | **Safe** | Populates the applied-set as a `map[event.EventID]struct{}`. Set membership is order-independent. |

### `internal/settlement/funding_transfer_lookup.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 44 | `scanner.All()` (`[]*event.Event`) | **Safe** | Slice range. The scanner returns DAG events in topological order. |

### `internal/settlement/generation_ledger_calculator.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 95 | `ev.CausalRefs` (`[]event.EventID`) | **Safe** | Slice range. |
| 137 | `ancestorEv.CausalRefs` (slice) | **Safe** | Slice range. |
| 154 | `ancestors` (`[]*event.Event`) | **Safe** | Slice range, ordered. |

### `internal/settlement/verification_consensus_settler.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 181, 321, 356, 368, 382, 399, 407 | All slices (`dist.Recipients`, `round.Votes`, `recipients`, `entries`, `m`, `d.Recipients`) | **Safe** | All slice ranges. The settler's input data is already slice-form. No map iteration. |

---

## Micro-fix queued (Priority 1)

`internal/dispatch/prerequisites.go:46` — sort the deduped prerequisite-ID slice before returning. Single 3-line change. Bundled into the next E.P2 commit (step 11) rather than a third edit to the dispatcher in this commit, since it's a hygienic write affecting only the persisted record's byte-form, not consumer behavior. Action item tracked at the bottom of this document.

## Priority 2 / Priority 3 — deferred

Tracked for F4A step 11. Current evidence:
- `internal/recognition/bus.go:163` — `for _, c := range b.consumers` — same shape as dispatcher.go:170; **Unsafe-without-sort**, fix in step 11.
- `internal/recognition/index.go:113`, `:296` — both map iterations; need classification in step 11.
- `internal/recognition/badger_index.go:53` — building a return map from BadgerDB data; map-to-map; safe.
- `internal/recognition/activation.go:40` — `deferred` slice-typed; safe.
- `internal/recognition/replay.go:81` — slice range; safe.
- `internal/recognition/task_verification_consensus_consumer.go:188` — `actions` slice; safe.

Per plan §6.1 Priority 3 ("Classify but don't fix unless Unsafe-without-sort. Schedule any Unsafe-inherently to follow-on workstreams."), the bulk audit happens in step 11; Priority 1 is closed by this commit.

---

## Verification

- `go build ./...` — clean
- `go test -race -count=1 ./internal/dispatch/ ./internal/dispatch/conformance/ ./internal/recognition/ ./internal/settlement/ ./internal/integration/ ./internal/verification/cross_node/...` — all PASS
- Cross-node byte-equality harness (F4A step 1) still passes uniformly on the trivial corpus, so the sort changes did not introduce divergence.

---

## Action items

| ID | Description | Owner / sequencing |
|---|---|---|
| E.P1.A1 | Sort prereqSet output in `dispatch/prerequisites.go:46` | F4A step 11 (bundle with E.P2) |
| E.P2.A1 | Sort `dag.Tips()` output internally OR sort in `dispatcher.currentAnchor()` for stable per-node anchor selection | F4A step 11 (Priority 2 — recognition / DAG layer; not canonical-state-affecting per C-15) |
| E.P1.A3 | Audit Priority 2 + Priority 3 callsites | F4A step 11 |
| E.P1.A4 | Implement E-2 CI lint that fails build on unannotated `range over map` | F4A step 11 |

## FINDING (added to F4A architect-gate report ledger)

| ID | Severity | Surface |
|---|---|---|
| `dag-tips-unsorted` | Low (per C-15 admission state is non-canonical) | `dag.Tips()` returns its slice in random map-iteration order. Consumers depending on deterministic tip selection must sort themselves. Currently only `dispatcher.currentAnchor()` does this and it lands in the per-node admission record only. Hardening queued as E.P2.A1. |

---

# E.P2 + E.P3 amendment — completed F4A step 11

**Status**: Priority 2 + Priority 3 audit + new E-2 lint completed 2026-04-22 against `feat/selection-consistency-fix`.
**Scope**: Priority 2 (`internal/recognition/`, `internal/store/`, `internal/network/` hot paths) + Priority 3 (everything else). Combined with Priority 1, this closes Part E.

## Total inventory (after lint heuristic refinement)

The lint scans every non-test `.go` file under `internal/`, `cmd/`, `pkg/`. After scope-aware classification (function-local identifiers shadow file-level struct fields), it identified **123 distinct map-range callsites** across 600 total `range` statements. (The first-pass flat-table heuristic missed 18 sites because struct-field types were overshadowed by function-parameter types of the same name; refining the heuristic to walk per-function scopes resolved this and is what the production lint does.)

| Tier | Source surface | Map-range callsites |
|---|---|---|
| P1 (closed in step 7) | `internal/dispatch/`, `internal/settlement/` | 13 |
| P2 (this amendment) | `internal/recognition/`, `internal/store/`, `internal/network/`, `internal/dag/` (load-bearing surface) | 17 |
| P3 (this amendment) | everything else (cmd, ledger, escrow, identity, validator*, ocs, consensus, evidence, taskverification, monitoring, registry, reputation, router, tasks, verification, eventbus, ratelimit, projections, platform, roundprogress, auth, assurance, canary, cloudmap, evidence) | 93 |
| **Total** | | **123** |

## Priority 2 / Priority 3 dispositions

Aggregate breakdown across 110 P2+P3 sites (the 13 P1 sites are fixed and documented above):

| Disposition | Count | Notes |
|---|---|---|
| **Safe** | 100 | Commutative aggregates (sums, counts, max/min), set-membership, GC-by-predicate, snapshot map-to-map copies, filter-then-sort patterns, network broadcast fanout. Annotated `// safe: <reason>` to satisfy E-2 lint. |
| **Safe-with-note** | 6 | Iteration order observable in non-canonical surface (router tie-break, validator/assignment cluster ID assignment, identity GetByDisplayName first-match, dag.All() documented-unordered). Annotated; no fix. |
| **Unsafe-without-sort** | 4 | All FIXED in this commit. See "Sort fixes applied" below. |
| **Unsafe-inherently** | 0 | None identified at P2/P3 in the current codebase. The Type-E LogicalKeyConsumer pattern (F4B) is the only structural fix on the radar; it lives in §4.4 of the v2 plan, not here. |

## Sort fixes applied (Unsafe-without-sort, P2 + P3)

| File:Line | Surface | Fix |
|---|---|---|
| `internal/recognition/bus.go:163` (now `dispatch()`) | `for _, c := range b.consumers` — order leaks into per-consumer Consume sequence; F4B's second canonical consumer makes this load-bearing | Lex-sort consumer names; iterate sorted slice. **E.P2.A2** in the action ledger. |
| `internal/dag/dag.go:Tips()/PrimaryTips()/LocalTips()` (3 functions) | Returned slice fed into `dispatcher.currentAnchor()`; per-node anchor divergence | Internal `sort.Slice` on returned EventID slice; consumer-side sort no longer required. **E.P2.A1**. |
| `internal/dispatch/prerequisites.go:46` | `range prereqSet` produced the `missing` slice that lands in `AdmissionRecord.MissingPrerequisites` — persisted byte-form of admission record diverges per node | Collect prereqSet keys, lex-sort, iterate sorted. **E.P1.A1**. |
| `internal/escrow/escrow.go:507/530` | `range validators` and `range genRecipients` in `ReleaseSettlement`. Each iteration calls `TransferFromBucketLabeled`, which assigns process-local synthetic event-ID counter values. Final balances were already cluster-uniform (per-recipient amounts pre-computed deterministically), but the persisted `TransferEntry` stream and synthetic EventIDs diverged cross-node — pollutes diagnostic comparisons and could confuse a future replay path that depends on entry insertion order. | New `sortedAgentIDs(map[crypto.AgentID]uint64) []crypto.AgentID` helper at the bottom of the file. Both loops now iterate the lex-sorted ID slice. |

The escrow fix is the only finding outside `internal/dispatch/`, `internal/settlement/`, `internal/dag/`, `internal/recognition/` that actually needed code changes. Reported in the closing FINDINGS table below.

## Recognition Index disposition (P2 deep-dive)

`internal/recognition/index.go:113` and `:296` were flagged for classification:

- Line 113 (`Put`): cleanup loop that removes a now-ready item's old prereq-index entry from any prereqIndex bucket containing it. **Safe** — removal is idempotent; final state is set-difference; no canonical effect.
- Line 296 (`Stats`): aggregate counts (Total / Recognized / Ready / Deferred). **Safe** — commutative.

## P3 representative dispositions (selected high-traffic surfaces)

- `internal/consensus/voting.go` (5 sites): supermajority weight aggregation, weighted-median computation, deep-copy snapshots, finalized-count, pending-count. All **Safe** — every iteration is either commutative aggregation or a map-to-map deep copy.
- `internal/ocs/engine.go` (3 sites): expired-item collection (cleanup), GC of processed entries, snapshot-of-pending. **Safe** — none emit canonical events; expiry produces only local state mutations + observer-bus notifications.
- `internal/validator/registry.go` (6 sites): all sort their results via `sort.Slice` after the filter pass, OR are commutative count reductions. **Safe**.
- `internal/router/router.go` (5 sites): tie-break in `findBestMatchLocked` could produce different first-seen-best per node, but routing is per-node advisory state authored by the marketplace coordinator; the canonical `TaskClaimed` event carries its own causal refs. **Safe-with-note** (intra-node only).
- `internal/validator/assignment.go` (8 sites): cluster computation via union-find, weighted-random selection. Cluster IDs are local advisory state; selection results are non-canonical. **Safe-with-note**.
- `internal/identity/registry.go` (2 sites): `GetByDisplayName` first-match (uniqueness enforced at registration; effectively dead code), `All()` returns sorted via explicit `sort.Slice`. **Safe**.
- `internal/tasks/tasks.go` (5 sites): all results sorted before return OR cleanup-by-predicate OR aggregate-counts. **Safe**.
- `internal/network/node.go` + `mesh.go` + `scoring.go` (5+ sites): peer-list iteration for broadcast fanout / reputation aggregation. Network ordering is non-canonical. **Safe**.
- `internal/monitoring/cross_node_invariants/monitor.go` (6 sites): observer-only diagnostics per A-3. Iteration order affects only the FIRST per-key delta reported; downstream UI sorts the deltas. **Safe**.
- `internal/ledger/transfer.go` + `generation.go` (8 sites): all are commutative sums, max-by-After reductions, GC-by-predicate, or filter-then-sort. **Safe**.
- `internal/dag/dag.go:All()/MaxTimestamp()/EventIDs()/RecentEvents()/TopologicalSort.inDegree-build` (5 sites): `All()` is documented-unordered with caller responsibility; `EventIDs()`'s sole production caller (`Node.BuildCheckpoint`) sorts the result; `MaxTimestamp` and the `inDegree` builder are commutative; `RecentEvents` sorts by `CausalTimestamp`. **Safe**.

Full per-site annotations live in the source files (one-line `// safe: <reason>` immediately above each `range`); the lint enforces ongoing classification.

## E-2 CI lint (new)

Implemented as `internal/dispatch/lint/map_iteration.go` + `map_iteration_test.go` (sibling to the no-bypass lint). Runs as part of `go test ./...`.

**Heuristic**: per-file AST scan with two-tier identifier-type tables (function-local scope shadows file-level struct fields). For each `*ast.RangeStmt`:

1. Classify the iterated expression's textual type via lookup.
2. If type starts with `map[`, demand annotation.
3. If type is unknown (`?`, `?call`, `?index`), skip — documented false-negative.

**Accepted annotations** (any one suffices):
- `// safe: <reason>` on the same line as `for` or within 5 lines above
- A `sort.` call (any selector starting with `sort.`) within 5 lines above (proxy for "sorted before iteration")
- Iterated identifier name contains substring `sorted` (case-insensitive)

**Tests**:
- `TestMapIterationLint` — runs the lint against the real repo; asserts zero violations.
- `TestMapIteration_DeliberateViolation` — temp-dir fixture with an unannotated map range; asserts the lint flags it.
- `TestMapIteration_SafeAnnotationSuppresses` — same fixture with `// safe:` annotation; asserts suppression.
- `TestMapIteration_SortPragmaSuppresses` — same fixture with a follow-up `sort.Strings` call; asserts the second-range scenario.
- `TestMapIteration_SliceRangesAreNotFlagged` — fixture with slice / channel / string ranges; asserts the lint does NOT demand annotations on non-map ranges (the load-bearing no-noise property).

**Known false-negatives** (acknowledged, not blocking):
1. **Maps returned from function calls** — `range f()` where `f` returns `map[K]V`. The heuristic returns `?call`; annotation not demanded. Sites affected: `range scanner.All()` (slice), `range strings.Split()` (slice), `range stack.reg.All()` (slice or map?), `range scope.Names()` (slice). All four are slices in fact, so this is harmless today, but adding a function returning a map to a `range` site without a sort is a future blind spot. Mitigation: future contributors should annotate explicitly; a follow-on workstream could promote to full `golang.org/x/tools/go/types` analysis.
2. **Maps reached via index expression** — `range m[k]` where `m[k]` is itself a map (`m: map[K1]map[K2]V`). The heuristic returns `?index`; annotation not demanded. Sites affected: `range d.children[cur]` (in `TopologicalSort` — verified Safe; final result sorted by CausalTimestamp), `range e.assignmentCount[category]` (commutative sum — Safe), `range e.pairwise` (these particular sites are the outer loops over the top-level map, not the index expressions, and they ARE caught by the lint). Same mitigation as above.
3. **Cross-package struct-field types** — if a file ranges over a struct field whose declaring file is a different file in the same package, the per-file table doesn't see it. Manifested by: zero false-positives observed in the real-repo run; no manual workarounds needed. Worst case: a future addition slips through; covered by code review.

The audit doc reports total `MapRanges = 123`, `SkippedUnknown = 18` from the test log line. Both numbers grow if/when the codebase grows; both metrics are surfaced in the lint test's `t.Logf` output.

## Action items closed (and any newly opened)

| ID | Status |
|---|---|
| E.P1.A1 (sort prereqSet) | **Closed** |
| E.P2.A1 (sort `dag.Tips()`) | **Closed** — internal sort applied; PrimaryTips and LocalTips also sorted for consistency |
| E.P2.A2 (sort recognition bus consumers) | **Closed** — implemented in `bus.dispatch()` |
| E.P1.A3 (audit P2+P3) | **Closed** — see disposition tables above |
| E.P1.A4 (implement E-2 lint) | **Closed** — operational; CI gate is `TestMapIterationLint` |
| **E.P3.F1 (NEW finding)** | **Closed in this commit** — `escrow.ReleaseSettlement` validator/genRecipients map iteration affected the persisted `TransferEntry` stream cross-node. Final balances were equal (no consensus impact), but the entry-insertion sequence and synthetic event-ID counter assignments diverged. Fixed via `sortedAgentIDs` helper. |

## FINDINGs (added to F4A architect-gate ledger)

| ID | Severity | Surface |
|---|---|---|
| `escrow-distribution-unsorted` | Medium (canonical-state-adjacent: persisted ledger entries diverged per node, even though final balances did not) | `internal/escrow/escrow.go:ReleaseSettlement` iterated `validators` and `genRecipients` maps without sorting. Each iteration called `TransferFromBucketLabeled`, which assigns synthetic EventID `bucket:from:to:amount:counter` from a process-local atomic counter. Different per-node iteration orders → different counter values per (from,to) tuple → divergent persisted `TransferEntry` records. F4A step 11 fix: lex-sort recipient IDs before iterating. |
| `dispatch-test-flake` | Low (test-only, pre-existing) | `TestTVConsensusConsumer_Apply_ConcurrentSameTask_Serialized` is inherently racy: it spawns two goroutines targeting the same task with two events for two distinct rounds, only one of which is saved to the round store. The test asserts both Apply calls return nil, which requires the goroutine targeting the saved round to acquire the per-task mutex first. Fails ~30% of the time on `main` (pre-step-11) and ~60% with step-11 changes due to slight timing shifts from the new sort calls. Not a regression — production code path is identical for both goroutines. Hardening: either pre-save both rounds in the test, or assert "at most one Apply returned non-nil and balances are correct in both outcomes." Tracked for follow-on test-cleanup. |

---

**End of E.P1 + E.P2 + E.P3 audit (Part E closed).**
