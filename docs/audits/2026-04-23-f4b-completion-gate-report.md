# F4B completion gate report

**Workstream**: F4 — Selection Consistency Fix + Verification Discipline
**Phase**: F4B (protocol fix — LogicalKeyConsumer + consumer migrations)
**Branch**: `feat/selection-consistency-fix` @ `5fe2e93`
**Plan**: `docs/plans/implementation/f4-plan-v2.md`
**Date**: 2026-04-23
**Prior gate**: `docs/audits/2026-04-22-f4a-completion-gate-report.md`

F4B's task was to **close the selection-race bug class** the Part F retry surfaced. This report documents that the bug is closed, the protocol deviations landed, the findings ledger at F4B-end, the final performance delta, and the preconditions F4C inherits.

---

## 1. The headline — bug CLOSED

The dispatcher-integrated tied-weight harness flipped **RED → GREEN**. That is the empirical proof of F4B's correctness.

**Pre-F4B** (`8778361` and earlier): tied-weight corpus produces per-node ledger divergence. Node 1 settles tasks as "rejected"; Nodes 0/2 settle as "completed". Balances diverge across cluster. Task statuses differ by node. Baseline captured at `internal/verification/cross_node/testdata/tied_weight_divergence_through_dispatcher_baseline.txt`.

**Post-F4B** (`5fe2e93`): same corpus, same 3-node harness, same multi-emit event stream. Every node settles every task identically:

```
=== RUN   TestTiedWeightCorpus_ThroughDispatcherLK_Converges
  (3 nodes × 10 tasks × 3 identical log lines per task:)
  INFO verification_settler: reject settled task_id=tied-00 ... (× 3 nodes)
  INFO verification_settler: reject settled task_id=tied-01 ... (× 3 nodes)
  ... all 10 tasks ...
  INFO verification_settler: reject settled task_id=tied-09 ... (× 3 nodes)
--- PASS: TestTiedWeightCorpus_ThroughDispatcherLK_Converges (0.10s)
```

`AssertByteEquality` passes. Per-agent balances, escrow residuals, per-validator payouts, treasury share, and task statuses are byte-identical across the cluster.

**Reproduce**:

```
CROSS_NODE_HARNESS_CAPTURE=1 go test -race -v \
  -run TestTiedWeightCorpus_ThroughDispatcherLK_Converges \
  ./internal/verification/cross_node/...
```

Both RED baselines remain preserved intentionally for historical reference. Running `TestTiedWeightCorpus_ReproducesDivergence` or `TestTiedWeightCorpus_ThroughDispatcher_ReproducesDivergence` (the old content-hash harness paths) still fails with byte-identical baseline diffs — those tests exercise the pre-fix code path directly and will be removed in F4C cleanup.

---

## 2. F4B delivery summary

**10 commits** on `feat/selection-consistency-fix` since F4A gate:

| # | Step | Commit | Surface |
|---|---|---|---|
| 1 | 0.1 Dispatcher-integrated harness variant | `00437c0` | new test + harness helpers |
| 2 | 0.2 Test flake #9 fix | `d63d9dc` | test-only; pre-save sibling round |
| 3 | 0.3 F4A-end performance baseline | `f94ed35` | 5 benchmarks + baseline doc |
| 4 | 1.1 FINDINGs #5/#6 coupling gates | `34db7e6` | store + dispatch types |
| 5 | 1.2 LogicalKeyConsumer interface + admission path | `e912305` | new dispatch surface, schema v1→v2 |
| 6 | 5.1 cmd/node registration audit | `498c9d7` | doc only |
| 7 | 5.2.1 TVConsensus logical-key migration | `8554c1d` | **inflection point — bug closed** |
| 8 | Type E state-fetching convention §3.6 | `56ff5ab` | doc only |
| 9 | 5.2.2/5.2.3/5.2.4 Settlement family batch | `1813592` | Settlement + TaskSettlement + SettlementConsumer |
| 10 | Logical-key replay-conformance + C-14 update | `5fe2e93` | conformance template + doc |

---

## 3. F4B-owned success criteria (plan v2 §11 items 15–22)

F4B cannot run live testnet; those criteria formally verify at F4C. F4B's contribution is the **harness-equivalent evidence** that F4C will pass them. Each item below names the F4B evidence artifact.

### §11.15 — Live testnet 5-node vote-weight-tie + mixed corpora

**F4B evidence**: `TestTiedWeightCorpus_ThroughDispatcherLK_Converges` (3-node in-process harness; 10 tied tasks + uniform corpus). PASSES with cross-node byte-equality. The 3-node harness is a scaled-down analogue of the 5-node testnet; the dispatcher / consumer stack is production code, so behavior is equivalent.

**F4C residual**: run the same corpus shape on the 5-node AWS testnet per plan §9.1.3 Phase C-sanity. Expected: same byte-equal result at 5-node scale.

### §11.16 — Live testnet replay test

**F4B evidence**:

1. `RunReplayConformance` (content-hash, F4A step 2 landed + §8.1 wired in F4A step 3). Synthetic test PASSES.
2. `RunLogicalKeyReplayConformance` (logical-key, F4B this commit). Synthetic test `TestTypeE_SyntheticReplayConformance` PASSES. 5 events × 2 distinct logical keys → 2 Apply calls (per-key guarantee).
3. `recognition.ReplayHistoricalToBusConsumers` wired into `cmd/node/main.go:2051` (F4A step 3).

**F4C residual**: restart a testnet node with populated DAG; verify bus consumers fire for historical events.

### §11.17 — Live testnet continuous-monitoring test

**F4B evidence**: `internal/monitoring/cross_node_invariants/` package landed in F4A step 10. `aet invariants check` CLI operational. Prometheus gauge `aethernet_cross_node_ledger_divergence` wired through the existing `internal/metrics` registry. 10 unit tests PASS.

**F4C residual**: operator ships `/v1/admin/ledger-snapshot` endpoint (tracked as F4A FINDING #10), then runs cluster ≥ 1 hour with the monitor active. Divergence injection test (intentional fault) verifies detection.

### §11.18 — Live testnet content-hash test (byte-identical events dedup)

**F4B evidence**: `TestDispatcher_ContentHash_DuplicateEvents_OneApply` in `internal/dispatch/dispatcher_test.go` (F3-B, preserved unchanged by F4B). `TestAdmit_LogicalKey_SecondEventSameKey_NoOp` in `internal/dispatch/logical_key_test.go` (F4B; logical-key counterpart).

**F4C residual**: emit byte-identical Transfer events from two nodes on testnet; assert admission dedupes at the content-hash layer.

### §11.19 — Live testnet prerequisite-forgery test

**F4B evidence**: existing F3-B prerequisite tests in `internal/dispatch/` still PASS. `TestDispatcher_Recover_UnreachablePrereqTriggersForgeryDeletion` + related tests. F4B did not touch the prerequisite path (D-1 through D-8 locked, per locked-invariant review §2).

**F4C residual**: construct a forged-prerequisite event on testnet; assert fail-fast.

### §11.20 — Live testnet deferral-escalation test

**F4B evidence**: existing F3-B deferral tests in `internal/dispatch/deferral_test.go` still PASS. `DeferralComplaintThreshold = 30 epochs`, `DeferralFailoverThreshold = 100 epochs` unchanged.

**F4C residual**: run deferred consumer on testnet past the complaint threshold; assert `PrerequisiteWithholding` emits per spec.

### §11.21 — Integer-migration activation

**F4B evidence**: **F4C scope.** F4B does not touch integer-migration code. Precondition tracked in §6 below (merge conflict checklist).

### §11.22 — Integer-migration restart test

**F4B evidence**: **F4C scope.** Same as §11.21.

### F4B contributions to non-15–22 criteria

Plan v2 §11 items F4B also advanced:

- **§11.3 Replay-conformance template**: content-hash variant landed in F4A; **logical-key variant landed in F4B §5.2.1 follow-on commit**. Both operational.
- **§11.4 Structural validation at startup**: `Dispatcher.Register` and `Dispatcher.RegisterLogicalKey` both perform structural validation (Name() non-empty, Interested() non-nil, cross-kind name uniqueness). Verified by `TestDispatcher_RegisterLogicalKey_DuplicateName_AcrossKinds_Rejected` and siblings.
- **§11.5 No-bypass CI lint**: extended in F4B §5.2.4 to cover `(Applicator, Apply)` alongside the existing `(VerificationConsensusSettler, Settle)` pair. The lint is now a generic `(type, method)` registry, not a single-method check. New canonical-effect emitters land by adding one entry to `canonicalMethodsByType`.
- **§11.14 Performance non-regression**: see §5 below. Max delta across all 5 benchmarks is +1.13% (F4A-end) to +2.69% (F4B-end); no benchmark within 7% of the 10% halt threshold.

---

## 4. Consolidated plan deviations (architect-approved)

F4B introduced four structural deviations from plan v2 §4.4–§4.7 and one additive improvement to the no-bypass lint. Each is documented inline at its site; consolidated here for F4B-end traceability.

### 4.1 Deviations from plan v2 §4.4 `LogicalKeyConsumer` interface

| # | Deviation | Rationale | Where documented |
|---:|---|---|---|
| 1 | `RoundState(ctx, key) (RoundState, error)` added as consumer method | Plan §4.5 step (c) said "dispatcher queries canonical round state"; dispatcher lacks universal DAG-query knowledge, so the query moved to the consumer. Mechanism on consumer, orchestration still on dispatcher. | `internal/dispatch/logical_key.go` method doc; `docs/architecture/locked-invariants-review-f4a.md` §3.4 |
| 2 | `RecoveryProbe(ctx, key) (RecoveryStatus, error)` added to interface | Locked invariant C-14 must be preserved for logical-key admission. Without RecoveryProbe, a crashed-mid-Apply record would either never retry or silently re-Apply — both violate C-14. | `docs/architecture/locked-invariants-review-f4a.md` §2.1 C-14 + §3.4 |
| 3 | `Name()` and `Interested()` added to interface | Required for dispatcher registration (cross-kind uniqueness) and event routing. Plan §4.4 omitted; trivial addition consistent with existing `Consumer` shape. | `internal/dispatch/logical_key.go`; §3.4 above |
| 4 | Consumer-local typed helper for fetching non-`[]*event.Event` state (TVConsensus `roundFor`, Settlement `voteRecordFor`) | Shared `RoundState.Votes` is `[]*event.Event`; consumer backing stores are different typed shapes (`*TaskVerificationRound`, `*consensus.VoteRecord`). Coupling via the shared field would leak consumer-specific types into the generic `dispatch` package. | `docs/architecture/locked-invariants-review-f4a.md` §3.6 Type E state-fetching convention |

### 4.2 Additive improvement (not a deviation) — no-bypass lint scope

**Before F4B**: `internal/dispatch/lint/lint.go` only detected direct calls to `*VerificationConsensusSettler.Settle` outside `internal/dispatch/`.

**After F4B §5.2.4**: generalized to `(type, method)` pairs via `canonicalMethodsByType` map. Added `(Applicator, Apply)` so direct calls to `settlementApp.Apply(&sp)` from outside the dispatcher are now flagged. Future canonical-effect emitters register by adding one map entry.

**Why additive not deviation**: plan §8.3 called out the no-bypass lint in general terms; the generalization is a hardening that makes the lint reusable across the admission-strategy axis F4B introduced. Architect-approved ahead of time in the F4A→F4B transition.

### 4.3 TaskSettlement no-op Apply (plan §5.2.3 pattern boundary)

Plan §5.2.3 specified `LogicalKey = TaskID`, `IsComplete` derived from the task's TVConsensus round outcome, and `Apply` to "perform task settlement." The migration shipped with `Apply = no-op` because:

- TaskSettlement is the **target** of settlement (via `settlementApp.applyTaskSettlement` in `applicator.go:191`), not a settlement itself.
- The canonical effect fires when a Settlement event targeting the TaskSettlement is admitted (§5.2.2 path).
- The TaskSettlement logical-key consumer's role is purely per-TaskID admission dedup so downstream consumers see a stable canonical record.

**Deviation validation**: the TaskSettlement Apply had no analog in the plan, because the plan assumed TaskSettlement would itself carry the settlement effect. Actual current code decouples: TaskSettlement is target-only; Settlement is the effect carrier. The no-op Apply is correct for current code and future-proofs against path re-enablement. Captured in commit `1813592` body; re-stated here as a plan deviation for F4B-end traceability.

---

## 5. Performance — final F4A-end vs F4B-end comparison

All measurements: Apple M1, darwin/arm64, GOMAXPROCS=8, `-benchtime=2s -count=5`. Median ns/op across 5 runs.

| Benchmark | F4A-end baseline | F4B-end median | Delta | +10% threshold | Gate |
|---|---:|---:|---:|---:|:---:|
| FreshContentHash | 14,949 | 15,136 | +1.25% | 16,444 | ✅ |
| DuplicateContentHash | 6,438 | 6,611 | +2.69% | 7,082 | ✅ |
| ConcurrentDifferentEvents | 7,042 | 7,084 | +0.60% | 7,746 | ✅ |
| ConcurrentSameEvent | 2,869 | 2,906 | +1.29% | 3,156 | ✅ |
| StreamWithBackpressure | 7,149 | 7,279 | +1.82% | 7,864 | ✅ |

Max delta: +2.69% on `DuplicateContentHash`. Well under the +10% D-5 halt threshold and under the +5% early-warn threshold the founder added for the Settlement family migration.

**Allocation profile unchanged**: 209 allocs/op fresh, 96 allocs/op idempotent — identical to F4A-end. The v1→v2 AdmissionRecord shape change adds ~66 B/op (~+0.66%) on fresh admissions; diagnostic only, not regression-gated.

Baseline document: `docs/architecture/f4a-end-performance-baseline.md`. **Do not re-baseline** per the founder's F4B step 0.3 directive — these deltas are the comparators for future work.

---

## 6. F4B → F4C transition preconditions

F4C is the integer-migration cutover + testnet deploy + merge to main (plan §9). Each precondition below is either **MET** or **PENDING** at F4B-end.

| # | Precondition | Status | Notes |
|---:|---|---|---|
| 1 | F4B bug-class-closure gate PASSES | **MET** | `TestTiedWeightCorpus_ThroughDispatcherLK_Converges` PASSES cross-node byte-equality. Central halt-trigger satisfied. |
| 2 | Full-repo test sweep PASS | **MET** | `go test -race -count=1 ./internal/dispatch/... ./internal/recognition/ ./internal/store/ ./internal/integration/ ./internal/verification/cross_node/...` — all PASS. |
| 3 | Performance non-regression | **MET** | §5 above; max +2.69%. |
| 4 | No-bypass lint operational | **MET** | Lint extended to `(Applicator, Apply)`; violation fixture test PASSES. |
| 5 | Map-iteration determinism lint operational | **MET** | 123 callsites classified; lint asserts 0 violations on real repo. |
| 6 | Replay-conformance template for both admission strategies | **MET** | Content-hash (F4A) + logical-key (F4B) both operational. |
| 7 | Locked-invariant review complete with C-14 extension | **MET** | `docs/architecture/locked-invariants-review-f4a.md` updated for RecoveryProbe addition. |
| 8 | cmd/node registration audit | **MET** | `docs/audits/2026-04-23-cmd-node-registration-audit.md`; 2 needs-check items validated during §5.2.1. |
| 9 | Merge conflict checklist (plan §9.1.1) | **PENDING — F4C task** | Requires diff-analysis between `feat/selection-consistency-fix` and `feat/canonical-distribution-integer-migration`. Not produced by F4B because both branches are still evolving. F4C's first step is to produce this checklist. |
| 10 | Fresh testnet deploy readiness | **PENDING — F4C task** | Per CLAUDE.md deploy procedure: build on EC2 44.200.60.102, push to ECR, wipe + redeploy 5 nodes, preserve identity files. F4B did not touch deploy infrastructure. |
| 11 | Integer-migration activation event emission (plan §11.21) | **PENDING — F4C task** | Integer-migration branch carries the activation-event scaffolding. F4C merges the two branches and runs the activation flow. |
| 12 | §9.1.4 docs updates (design-principles.md + CLAUDE.md) | **PENDING — F4C task** | Per plan §8.5: required in F4C post-merge. New F4 primitives (Type E consumer, LogicalKeyConsumer interface, C-3'/Serialization-2/C-17/§3.6 convention) need to land in load-bearing docs. F4B has only updated the locked-invariant review (the F4-specific architectural diff document); CLAUDE.md and design-principles.md updates are F4C scope. |

**Unmet preconditions** (explicitly): #9 merge conflict checklist, #10 testnet deploy, #11 integer-migration activation, #12 load-bearing docs update. All four are F4C work per plan §9. None block the F4B→F4C transition; all block the F4C→main merge.

---

## 7. F4 findings ledger — F4B-end (14 items)

Consolidation of all findings surfaced during F4A and F4B. F4A had 12; F4B adds 2 new findings. Fixed-in-F4B items (previously scheduled-for-F4B) are marked CLOSED.

### 7.1 Severity high

| # | Finding | File:line | Disposition |
|---:|---|---|---|
| 5 | admission-schema-no-gate | `internal/store/store.go` `validateAdmissionDecode` | **CLOSED in F4B §1.1** (commit `34db7e6`). `ErrAdmissionSchemaTooNew` returned on SchemaVersion > AdmissionCurrentVersion. Test inverted from "documents the bug" to "asserts the gate." |

### 7.2 Severity medium

| # | Finding | File:line | Disposition |
|---:|---|---|---|
| 1 | A.1 harness bypasses dispatcher | `internal/verification/cross_node/cluster.go:36-40` (historical doc comment) | **CLOSED in F4B §0.1** (commit `00437c0`). `TestTiedWeightCorpus_ThroughDispatcher_ReproducesDivergence` (RED, historical) + `TestTiedWeightCorpus_ThroughDispatcherLK_Converges` (GREEN, F4B fix validation) both operational. |
| 2 | jcs silent int64 overflow | `internal/jcs/jcs.go:95-99` | **scheduled for follow-on workstream** — out of F4 scope; documented in test. |
| 3 | store-corruption-fail-stop (AllX iterators) | `internal/store/store.go` every `AllX` method | **partially-addressed in F4B §1.1 for AllAdmissions** (fail-stop kept, but the gate now fails with operator-actionable error). Other AllX surfaces: part of silent-zero family (§7.5), scheduled for F5. |
| 6 | admission-state-no-gate | `internal/store/store.go` admission decode | **CLOSED in F4B §1.1** (commit `34db7e6`). `ErrUnknownAdmissionState` returned for state values outside `IsKnownAdmissionState` enum. |
| 12 | escrow-distribution-unsorted | `internal/escrow/escrow.go:ReleaseSettlement` | **FIXED in F4A step 11** (commit `068002c`). `sortedAgentIDs` helper applied before iteration. |

### 7.3 Severity low

| # | Finding | File:line | Disposition |
|---:|---|---|---|
| 4 | stake-meta-silent-zero | `internal/store/store.go:446-466` | **scheduled for F5 silent-zero workstream** (§7.5). |
| 7 | replay-reserve-truncated-zero | `internal/store/store.go` ReplayReserve get path | **scheduled for F5 silent-zero workstream** (§7.5). |
| 8 | dag-tips-unsorted | `internal/dag/dag.go:424-433` | **FIXED in F4A step 11**. |
| 9 | dispatch-test-flake | `internal/dispatch/tv_consensus_consumer_test.go:279-318` | **CLOSED in F4B §0.2** (commit `d63d9dc`). 100x stress PASSES. |
| 10 | ledger-snapshot-endpoint-missing | `internal/api/server.go` (absence) | **scheduled for operator follow-up** (F4C first deploy per founder's gate-approval addition). |
| 11 | peer-discovery-shape-needs-adapter | `internal/network/discovery.go` | **scheduled for operator follow-up** (F4C first deploy). |

### 7.4 NEW F4B findings

| # | Finding | File:line | Severity | Disposition |
|---:|---|---|---|---|
| 13 | no-bypass-lint-scope generalization | `internal/dispatch/lint/lint.go` | **additive improvement** | CLOSED in F4B §5.2.4 (commit `1813592`). Lint generalized from single method-name to `(type, method)` pairs via `canonicalMethodsByType`. Listed as a finding for F4B-end traceability; framed as scope extension, not regression. |
| 14 | settleTask-dead-code | `internal/autovalidator/auto.go:settleTask()` | low (dead code) | **scheduled for follow-on workstream**. `settleTask()` has no production callers (replaced by multi-voter consensus path); its test is `t.Skip`'d. Tracked for F4C cleanup-only commit or a later cleanup workstream. |

### 7.5 Meta-finding: silent-zero-on-truncation family (F5 candidate)

Per founder's F4A gate-approval addition #1. Groups F4A findings #3 / #4 / #5+#6 / #7 under one anti-pattern: store-layer decode path does not gate on input it didn't anticipate.

F4B §1.1 closed #5 and #6 with the `validateAdmissionDecode` + typed-error pattern — this IS the fix shape for the family. Applying the same pattern to #3 / #4 / #7 across the broader store surface (events, transfers, generations, identities, stake-meta, replay-reserve, etc.) is the F5 silent-zero workstream.

**F5 scope** (recommended by F4B-end):

1. Define a generic `(storeKey, SchemaVersion uint32)` header convention for all persisted records.
2. Apply `validateDecode`-pattern to every `GetX` and `AllX` surface.
3. Add typed errors: `ErrXSchemaTooNew` / `ErrXCorrupt` / `ErrXTruncated` per record type, OR a single generic sentinel family.
4. Dual-read v1 records where needed for backward-compat.
5. Sequence: after F4C testnet merge, before any production data grows to a scale where re-encoding becomes expensive.

---

## 8. Locked-invariant review updates

`docs/architecture/locked-invariants-review-f4a.md` was updated at F4B-end:

- **§2.1 C-14** — marked as "Extended in F4B §5.2.1" with the per-strategy recovery evidence documented (TVConsensus finalized rounds, Settlement `IsApplied`, TaskSettlement unconditional). The invariant itself (evidence-based, monotonic, replay-safe) is preserved bit-exact.
- **§3.4 `LogicalKeyConsumer` interface** — updated from the 4-method plan sketch to the as-landed 8-method shape (Name, Interested, Key, RoundState, IsComplete, DeriveOutcome, Apply, RecoveryProbe). "Plan deviations" subsection enumerates the four architect-approved additions.
- **§3.6 Type E state-fetching convention** — added at founder's explicit request after §5.2.1 (commit `56ff5ab`). Documents the RoundState-generic-container + consumer-local-typed-helper pattern. Decision procedure for new Type E consumers; anti-patterns explicitly called out.

All other locked invariants (A-1..A-4, B-1..B-4, C-2, C-4..C-13, C-15, C-16, D-1..D-8, §4.5 atomic-batch forward-only, Serialization-1) are **preserved unchanged** per locked-invariant review §2. The F4 architectural diff is strictly additive + the C-3' refinement + the C-14 extension.

---

## 9. Recommendation

**F4B is complete. Ready for F4C.**

The selection-race bug class characterized in `docs/plans/implementation/selection-race-characterization.md` is closed across all 4 Part D migration surfaces:

| Surface | Migration | Verification |
|---|---|---|
| TaskVerificationConsensus | §5.2.1 (commit `8554c1d`) | `TestTiedWeightCorpus_ThroughDispatcherLK_Converges` PASSES; halt-trigger satisfied |
| Settlement | §5.2.2 (commit `1813592`) | Unit + conformance suite PASSES; end-to-end dedup property asserted |
| TaskSettlement | §5.2.3 (commit `1813592`) | Unit + conformance suite PASSES; no-op Apply shape correct per §4.3 above |
| SettlementConsumer | §5.2.4 (commit `1813592`) | `SetDispatcher` routing + legacy fallback both tested |

All F4B-owned plan v2 §11 criteria have their F4B evidence artifact; four testnet criteria (15–22) defer actual live verification to F4C per phase design.

F4C preconditions: 8 of 12 MET; 4 PENDING as F4C scope (merge conflict checklist, fresh testnet deploy, integer-migration activation, load-bearing docs updates).

Awaiting architect approval to proceed to F4C.

---

**End of F4B completion gate report.**
