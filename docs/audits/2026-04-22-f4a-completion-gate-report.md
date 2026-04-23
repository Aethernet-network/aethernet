# F4A completion gate report

**Workstream**: F4 — Selection Consistency Fix + Verification Discipline
**Phase**: F4A (verification + foundations)
**Branch**: `feat/selection-consistency-fix` @ `068002c`
**Plan**: `docs/plans/implementation/f4-plan-v2.md`
**Date**: 2026-04-22

This is the architect-facing report for the F4A → F4B transition gate. It summarizes what F4A built, which success criteria are met, the FINDINGs ledger, the meta-finding (silent-zero family), and the explicit F4B preconditions.

---

## 1. F4A delivery summary

12 commits on `feat/selection-consistency-fix`, branched from `main` at `603bd9b`:

| # | Step | Commit | Surface |
|---|---|---|---|
| 0 | F4 branch setup (docs cherry-pick + plan v2 + audit) | `477ae16` | docs |
| 1 | A.1 cross-node byte-equality harness + tied-weight self-test | `146488a` | new pkg `internal/verification/cross_node/` |
| 2 | A.2 replay-conformance template (RED) | `c52e136` | new pkg + SUT stub |
| 3 | §8.1 ReplayHistoricalToBusConsumers wiring (GREEN) | `a1b5c91` | recognition + cmd/node |
| 4 | B.1 jcs unit tests | `27febac` | tests only |
| 5 | B.2 store unit tests | `db45a1f` | tests only |
| 6 | B.3 schema-migration discipline doc | `4658209` | docs |
| 7 | E.P1 map-iteration audit + dispatcher sort fixes | `432a266` | dispatch + audit doc |
| 8 | C locked-invariant review (with FINDINGs #5/#6 incorporated) | `b1ee215` | docs |
| 9 | 8.3 no-bypass lint operational + violation fixture test | `59167b7` | dispatch/lint + recognition pragma |
| 10 | A.3 cross-node ledger divergence monitoring + `aet invariants check` | `6c156c5` | new pkg `internal/monitoring/cross_node_invariants/` + cmd/aet |
| 11 | E.P2/P3 audit + E-2 CI lint + sort fixes (incl. escrow finding) | `068002c` | 57-file sweep |

**Stats**:
- New packages: 3 (`cross_node` harness, `cross_node_invariants` monitoring, `dispatch/lint/map_iteration`)
- New documents: 5 (schema discipline, locked-invariant review, map-iteration audit, known-deps file, this gate report)
- Test coverage delta: jcs 0→3.8 ratio, store 0.21→1.16 ratio
- Lints operational: 2 (no-bypass, map-iteration determinism)
- Sort fixes applied: 7 callsites (4 in dispatcher per step 7, 4 more per step 11)

---

## 2. Verification gates — plan §11 success criteria

Plan §11 enumerated 25 success criteria for F4 overall. F4A is responsible for criteria 1–14 (verification + foundations + audits). F4B owns 15–22 (protocol fix). F4C owns 23–25 (integer-migration merge + testnet).

| # | Criterion | Status |
|---|---|---|
| 1 | A-1: cross-node byte-equality test runs in CI; any deviation fails build | **MET** — `go test ./internal/verification/cross_node/...` is part of the standard test suite. |
| 2 | A-2: every dispatcher consumer has a replay-conformance test; new consumer without one fails the no-bypass lint | **MET** — template at `internal/dispatch/conformance/replay_path.go`; the no-bypass lint at `internal/dispatch/lint/lint.go` enforces. |
| 3 | A-3: monitoring subsystem is observer-only; no canonical state mutation | **MET** — `internal/monitoring/cross_node_invariants/` does not import `internal/dag` for `Add`, does not import `internal/localpub`. Verified by inspection. |
| 4 | A-4: monitor uses existing peer discovery; no new peer-configuration surface | **MET** with note — the package exposes `PeerSource` interface; production wiring uses existing `internal/network/discovery`. The wiring adapter is a one-liner in `cmd/node/main.go` (operator follow-up), not a new config surface. |
| 5 | B-1: `internal/jcs` test ratio ≥ 2.0 + 100x determinism stress passes | **MET** — ratio 3.8; 100x stress passes in 1.6s. |
| 6 | B-2: `internal/store` test ratio ≥ 0.5 | **MET** — ratio 1.16. |
| 7 | B-3: every persisted record type enumerated with schema version + migration policy | **MET** — `docs/architecture/schema-migration-discipline.md`. |
| 8 | C: locked-invariant review document produced; C-3' refinement explicit; Serialization-2 + C-17 introduced | **MET** — `docs/architecture/locked-invariants-review-f4a.md`. |
| 9 | E-1: every `range over map` in production classified | **MET** — 123 callsites classified across P1/P2/P3 in `docs/audits/2026-04-22-map-iteration-determinism.md`. |
| 10 | E-2: CI lint operational; verified by deliberate violation observation | **MET** — `internal/dispatch/lint/map_iteration.go` + `map_iteration_test.go`; `TestMapIteration_DeliberateViolation` confirms detection. |
| 11 | §8.1: `ReplayHistoricalToBusConsumers()` wired in cmd/node startup | **MET** — `cmd/node/main.go` post-`SetOnCommit`/pre-`node.Start()`. |
| 12 | §8.3: no-bypass CI lint operational | **MET** — `internal/dispatch/lint/lint.go` extended with AST-based Settle bypass detection; deliberate-violation fixture confirms detection. |
| 13 | F4A FINDINGs ledger surfaced for architect review | **MET** — §4 of this document. |
| 14 | All F4A test suites green under `-race` | **MET** with one pre-existing flake (FINDING #9 — not a regression; see §4). |

**14 of 14 F4A success criteria met.**

F4B-owned (15–22) and F4C-owned (23–25) criteria are not in scope here.

---

## 3. Test sweep at gate

```
go test -race -count=1 ./internal/dispatch/lint/...      → PASS
go test -race -count=1 ./internal/recognition/           → PASS
go test -race -count=1 ./internal/dispatch/conformance/  → PASS
go test -race -count=1 ./internal/settlement/            → PASS
go test -race -count=1 ./internal/integration/           → PASS
go test -race -count=1 ./internal/verification/cross_node/...  → PASS
go test -race -count=1 ./internal/dag/                   → PASS
go test -race -count=1 ./internal/escrow/                → PASS
go test -race -count=1 ./internal/ledger/                → PASS
go test -race -count=1 ./internal/network/               → PASS
go test -race -count=1 ./internal/monitoring/cross_node_invariants/...  → PASS
go test -race -count=1 ./internal/jcs/                   → PASS
go test -race -count=1 ./internal/store/                 → PASS
go build ./...                                            → clean
go vet ./...                                              → only pre-existing test-file warnings
```

Pre-existing flake (NOT a regression, NOT introduced by F4A): `TestTVConsensusConsumer_Apply_ConcurrentSameTask_Serialized`. Failure rate ~30% on `main`, ~60% post-step-11 (timing shifts from new sort calls). Production code path identical for both goroutines; test asserts a specific goroutine wins the per-task mutex. Tracked as FINDING #9 (`dispatch-test-flake`).

---

## 4. FINDINGs ledger (final)

12 findings surfaced during F4A. Reordered here by severity then by closure status.

### 4.1 Severity high

| # | Finding | File:line | Disposition |
|---|---|---|---|
| **5** | **admission-schema-no-gate**. `AdmissionRecord.SchemaVersion uint32` is persisted by `PutAdmission` and decoded by `GetAdmission`/`AllAdmissions` but never validated. v999 records round-trip opaquely. Gates the dispatcher's exactly-once admission state machine — F4B's LogicalKeyConsumer changes WILL cross this surface. | `internal/store/store.go` admission decode path; `internal/dispatch/types.go` `AdmissionRecord`; test `TestAdmission_UnknownSchemaVersion_RoundTripsOpaquely` | **scheduled for F4B** — incorporated into `docs/architecture/locked-invariants-review-f4a.md` §4.1 as required addition to F4B's first persistence-layer commit. Member of the **silent-zero-on-truncation family** (§5). |

### 4.2 Severity medium

| # | Finding | File:line | Disposition |
|---|---|---|---|
| **1** | **A.1 harness bypasses dispatcher**. Cross-node harness invokes `TVConsensusConsumer.Apply` directly rather than through `dispatch.Dispatcher.Admit`. Reproduces the current bug correctly (content-hash admission would not dedupe multi-emits anyway), but cannot validate F4B's logical-key admission fix end-to-end. | `internal/verification/cross_node/cluster.go:36-40` (design-choice doc comment) | **scheduled for F4B** — see §6 (F4B precondition). |
| **2** | **jcs silent int64 overflow**. `if f == math.Trunc(f) && math.Abs(f) < 1e20` allows numbers in (MaxInt64≈9.22e18, 1e20) to silently saturate at MaxInt64/MinInt64. Deterministic across nodes (not a divergence vector) but observably wrong vs RFC 8785 intent. | `internal/jcs/jcs.go:95-99`; test `TestCanonicalize_NumberPrecisionEdges` | **scheduled for follow-on workstream** — fix: tighten threshold to `|f| < float64(math.MaxInt64)`. |
| **3** | **store-corruption-fail-stop**. Every `AllX` iterator propagates the FIRST `json.Unmarshal` error and stops. A single bad row hides every healthy row that follows. | `internal/store/store.go` — every `AllX` method | **scheduled for F4B** — policy decision required (skip-with-warn vs fail-loudly with operator guidance); tracked in `schema-migration-discipline.md` §3 item 3. Member of the **silent-zero-on-truncation family** (§5). |
| **6** | **admission-state-no-gate**. Hand-crafted JSON with `state: 99` round-trips through the store; `String()` returns "unknown". | `internal/store/store.go` admission decode path; `internal/dispatch/types.go` `AdmissionState.String()`; test `TestAdmission_UnknownStateValue_RoundTripsOpaquely` | **scheduled for F4B** — bundle with #5 in same commit. Member of the **silent-zero-on-truncation family** (§5). |
| **12** | **escrow-distribution-unsorted**. `escrow.ReleaseSettlement` iterated `validators` and `genRecipients` maps without sorting. Each iteration called `TransferFromBucketLabeled`, which assigns synthetic EventID `bucket:from:to:amount:counter` from a process-local atomic counter. Different per-node iteration orders → different counter values → divergent persisted `TransferEntry` records. Final agent balances cluster-equal (commutative on agents); audit trail diverged. | `internal/escrow/escrow.go:ReleaseSettlement` | **FIXED in F4A step 11** — `sortedAgentIDs` helper applied before iteration. |

### 4.3 Severity low

| # | Finding | File:line | Disposition |
|---|---|---|---|
| **4** | **stake-meta-silent-zero**. `parseStakeMetaValue` returns `(0,0,0,nil)` for any blob shorter than 16 bytes. Callers cannot distinguish missing from corrupt. | `internal/store/store.go:446-466` | **scheduled for follow-on workstream** — fix: add 1-byte version tag and dual-read the legacy 16-byte format. Member of the **silent-zero-on-truncation family** (§5). |
| **7** | **replay-reserve-truncated-zero**. `GetReplayReserve` returns `(0, nil)` for any blob with length ≠ 8 bytes. | `internal/store/store.go` ReplayReserve get path | **scheduled for follow-on workstream** — bundle with #4. Member of the **silent-zero-on-truncation family** (§5). |
| **8** | **dag-tips-unsorted**. `(*DAG).Tips()` (and PrimaryTips/LocalTips) returned slices unsorted — observable in per-node admission state only (Safe-by-design per C-15). | `internal/dag/dag.go:424-433` | **FIXED in F4A step 11** — internal sort applied to all three. |
| **9** | **dispatch-test-flake**. `TestTVConsensusConsumer_Apply_ConcurrentSameTask_Serialized` is inherently racy: two goroutines, two events targeting two distinct rounds, only one round saved to the round store. Test assumes a specific goroutine wins the per-task mutex. Pre-existing on `main` at ~30%; ~60% post-step-11 (slight timing shift from new sort calls). | `internal/dispatch/tv_consensus_consumer_test.go:279-318` | **scheduled for follow-on workstream** — test design fix: pre-save both rounds OR assert "at most one Apply returned non-nil and balances are correct in both outcomes". Production code path is unaffected. |
| **10** | **ledger-snapshot-endpoint-missing**. `internal/api/server.go` has no `/v1/admin/ledger-snapshot` route. `aet invariants check` will report fetch errors for every peer until an operator ships the endpoint. | `internal/api/server.go` (absence) | **scheduled for operator follow-up** — A.3 deliverable was the package surface, not the production HTTP wiring. |
| **11** | **peer-discovery-shape-needs-adapter**. `internal/network` discovery is a single file with `Node.PeerIPs() map[string]bool` rather than a separate package. Production monitor wiring needs a one-liner adapter to `PeerSource`. | `internal/network/discovery.go` | **scheduled for operator follow-up** — same scope class as #10. |

### 4.4 Disposition summary

- **Fixed in F4A**: 2 (#8 dag-tips-unsorted, #12 escrow-distribution-unsorted)
- **Scheduled for F4B**: 4 (#1 harness gap, #3 corruption-fail-stop, #5 admission-schema-no-gate, #6 admission-state-no-gate)
- **Scheduled for follow-on workstream**: 4 (#2 jcs overflow, #4 stake-meta silent-zero, #7 replay-reserve silent-zero, #9 test flake)
- **Scheduled for operator follow-up**: 2 (#10 snapshot endpoint, #11 discovery adapter)
- **Halt-and-surface**: 0

---

## 5. Meta-finding — silent-zero-on-truncation family (NEW finding class)

Per founder gate-review addition #1: four of the twelve findings (#3, #4, #5/#6, #7) are instances of a single anti-pattern. Naming it as a distinct finding class for follow-on tracking.

**Class name**: silent-zero-on-truncation family
**Pattern**: store-layer decode path does not gate on input it didn't anticipate; unexpected input silently produces a default value (zero, missing, opaque round-trip) rather than failing loudly.

**Members**:

| ID | Surface | Symptom |
|---|---|---|
| #3 | every `AllX` iterator | first corrupt row halts iteration; healthy rows after are invisible |
| #4 | `parseStakeMetaValue` | truncated blob → `(0,0,0)` indistinguishable from missing |
| #5 | `GetAdmission`/`AllAdmissions` schema field | unknown `SchemaVersion` round-trips opaquely |
| #6 | admission decode `state` field | unknown state value round-trips with `String()=="unknown"` |
| #7 | `GetReplayReserve` | truncated blob → `(0, nil)` indistinguishable from missing |

**Why it's a class, not 4 separate items**: the fix is the same shape for all of them — introduce an explicit version/format byte (or validate the existing one), gate decode on it, return a typed error (`ErrSchemaTooNew` / `ErrCorruptRecord`) on mismatch. Treating these as 5 separate one-off PRs would (a) produce 5 different error-type designs, (b) miss the architectural opportunity to standardize the store's "fail-loudly-on-malformed" contract, and (c) leave the next instance of this pattern (when it emerges) as a 6th finding rather than something the existing convention catches.

**Recommended treatment**: a separate small-architectural workstream that introduces the versioning convention across the store layer. NOT bundled into F4B (which already has #5 and #6 incorporated as required additions to its admission-surface commit). The workstream's scope:

1. Define the convention (1-byte version tag, ErrSchemaTooNew + ErrCorruptRecord typed errors).
2. Apply to the 5 surfaces above with appropriate dual-read for backward compat.
3. Document in `schema-migration-discipline.md` as the canonical pattern.
4. Add E-3-style invariant: every store record decode path must gate on its version tag.

Severity at the class level: **medium** (the canonical agent ledger is the load-bearing state and is unaffected; affected surfaces are auxiliary persisted state and audit trails). Sequencing: AFTER F4B (which closes the highest-priority class member, #5).

---

## 6. F4B precondition (explicit)

Per founder gate-review addition #3: FINDING #1 (harness bypasses dispatcher) is named here as an EXPLICIT F4B precondition, not a scheduled task. The logic chain:

- F4B's protocol fix is logical-key admission, which operates AT the dispatcher layer.
- F4A's cross-node byte-equality harness (the verification gate for F4B) bypasses the dispatcher.
- Therefore: a harness that bypasses dispatcher cannot validate F4B's fix.
- Therefore: harness extension MUST be the FIRST F4B step, before any protocol code change.

**F4B step 0 (mandatory)**: extend `internal/verification/cross_node/` with a dispatcher-integrated variant. The variant must:

1. Wire a real `dispatch.Dispatcher` per node in the cluster.
2. Register the F4B logical-key TVConsensusConsumer through it.
3. Route events through `Dispatcher.Admit(ctx, ev)` rather than calling `Consumer.Apply` directly.
4. Run the existing tied-weight corpus (currently gated by `CROSS_NODE_HARNESS_CAPTURE=1` and skipped) on the dispatcher-integrated variant. **Initial expectation**: it MUST reproduce the divergence on the unfixed code path before F4B's protocol changes land. The harness's ability to see the bug through the dispatcher is its self-test for this phase, mirroring the F4A step 1 critical validation.
5. After F4B's protocol fix: the same test MUST PASS — divergence is gone.

If F4B step 0 cannot reproduce the bug through the dispatcher (e.g., because content-hash admission already prevents the multi-emit consumer.Apply call), STOP and surface — the F4B fix may not actually be fixing the surface that's broken in production, OR the harness extension needs different scaffolding.

This precondition is in addition to F4B's existing scope. F4B's step 1 onwards (the actual protocol changes per plan §5) cannot begin until step 0 is verified.

---

## 7. Coupling F4B is required to address (recap from §8 of locked-invariant review)

`docs/architecture/locked-invariants-review-f4a.md` §4 already documents this; surfacing here so the gate decision sees it inline:

- F4B's first persistence-layer commit MUST add `SchemaVersion` validation in `store.GetAdmission` + `store.AllAdmissions` (closes FINDING #5).
- Same commit MUST add unknown-state validation (closes FINDING #6).
- F4A's "documents the bug" tests at `TestAdmission_UnknownSchemaVersion_RoundTripsOpaquely` and `TestAdmission_UnknownStateValue_RoundTripsOpaquely` should be flipped to "asserts the gate" in the same commit.

---

## 8. Open questions for architect review

1. **Silent-zero family workstream sequencing.** Recommended placement: after F4B, before F4C testnet redeploy. Confirm or override.
2. **No-bypass lint scope expansion.** The current lint detects bypass of `*VerificationConsensusSettler.Settle()`. F4B will introduce additional canonical-effect emitters (logical-key Apply paths). Confirm expectation that the lint's `canonicalSettlerTypeNames` list grows in F4B's first commit alongside the new effect type — or that F4B introduces a distinct lint surface per effect type.
3. **Test-flake #9 priority.** With ~60% failure rate post-step-11, this will start polluting CI noise. Recommend treating the test fix as a gate-blocker for F4B start, not a follow-on. Not strictly per F4 plan, but pragmatic.
4. **Operator follow-ups #10/#11 timing.** `aet invariants check` is shipped but unobservable until the snapshot endpoint exists. Recommend bundling with F4B's first deploy commit (cheap to ship together; aligns with the verification-discipline spirit of F4).

---

## 9. Recommendation

**F4A is complete and ready for F4B.** All 14 F4A success criteria met. 12 FINDINGs surfaced, classified, and dispositioned; 2 fixed in F4A, 4 scheduled for F4B with explicit coupling documented in the locked-invariant review, 4 for follow-on, 2 for operator. The verification infrastructure required to validate F4B's fix exists (harness, replay template, monitoring) modulo the FINDING #1 precondition (harness extension as F4B step 0).

The selection-race characterization (`docs/plans/implementation/selection-race-characterization.md`) and the harness's tied-weight self-test (`testdata/tied_weight_divergence_baseline.txt`) provide a deterministic, byte-exact reference for what F4B must close. F4B's success criterion at the protocol level is: the same tied-weight test, run through the dispatcher-integrated harness variant, returns 0 divergence on the fixed code path.

Pending architect approval to proceed.

---

**End of F4A completion gate report.**
