# F5 Phase 5A — Completion Gate Report

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5A — Input-Domain Canonicalization (5A.1 audit + 5A.2 Q-score design + 5A.3 ancestry design + 5A.4 schema/refactor/lint)
**Status**: **All four sub-phases CLOSED. Ready for architect + founder confirmation to close Phase 5A and proceed to 5B plan v1 drafting.**
**Branch base**: `51bce89` (F4-frozen branch; tag `f4-complete-selection-race-closed-pending-f5`)
**Date range**: 2026-04-23 (Phase 5A.1 begin) → 2026-04-24 (Gate 5A.4 close)
**Plan reference**: `docs/plans/implementation/f5-phase-5a-plan-v3.md`

---

## 1. Phase 5A summary — sub-phase gate closures (chronological)

| Gate | Sub-phase | Closed (date) | Deliverable | Cross-reference |
|---|---|---|---|---|
| 5A.1 | Settlement input audit | 2026-04-23 (round 1 architect + multi-AI) | Audit doc + manifest YAML | `docs/audits/2026-04-23-settlement-input-audit.md`; `docs/architecture/settlement-input-manifest.yaml` |
| 5A.2 | Q-score canonicalization design | 2026-04-23 (post multi-AI round 2 V-1 finding + post-architect-read v1.2 cleanup) | CanonicalWProjection design with 12-item structure + V-1 invariant | `docs/architecture/q-score-canonicalization-design.md` (v2.2) |
| 5A.3 | Generation-ledger ancestry canonicalization | 2026-04-23 (post multi-AI round 2; v1.2 + companion 5A.2 v2.2 patch) | ReadAtAnchor primitive + 6 specs + cycle-exclusion deferral | `docs/architecture/generation-ledger-canonical-derivation.md` (v1.2) |
| 5A.4.a | Payout artifact schema | 2026-04-23 (architect Q-1/Q-2/Q-3 resolutions) | Schema YAML with V-1 + Input-Domain + uniqueness invariants | `docs/architecture/payout-artifact-schema.yaml` (v1.1) |
| 5A.4.b | Synthetic-ID refactor | 2026-04-24 | TransferFromBucketLabeled signature change + 14 callsites + bucketCounter removed + 3 tests | `internal/ledger/canonical_id.go` (new); `internal/ledger/canonical_id_test.go` (new); modifications to `internal/ledger/transfer.go`, `internal/escrow/escrow.go`, `internal/staking/staking.go`, `internal/fees/collector.go` |
| 5A.4.c | CI lint package | 2026-04-24 | `internal/settlement/lint/` package (5 prod files + 4 test files + testdata; 23 tests; warnings-now / failures-at-5D posture documented in doc.go) | `internal/settlement/lint/*` |
| 5A.4.d | Consumer audit reconciliation | 2026-04-24 | Verification report (no doc artifact); zero-breaking-consumers prediction CONFIRMED | `docs/historical/journal.txt` Gate 5A.4 entries |

Full chronological trace including multi-AI review rounds, architect decisions, and intermediate v→v patches: `docs/historical/journal.txt`.

## 2. Deliverables list — 8 artifacts

| # | Artifact | Path | Size | Cross-reference |
|---|---|---|---|---|
| 1 | Settlement input audit | `docs/audits/2026-04-23-settlement-input-audit.md` | 276 lines | 5A.1 deliverable; cited by 5A.2/5A.3/5A.4 designs |
| 2 | Settlement input manifest | `docs/architecture/settlement-input-manifest.yaml` | 1116 lines | 5A.1 deliverable; consumed by 5A.4.c lint at runtime |
| 3 | Q-score canonicalization design | `docs/architecture/q-score-canonicalization-design.md` | 944 lines | 5A.2 deliverable (v2.2); defines `CanonicalWProjection` interface + V-1 invariant |
| 4 | Generation-ledger canonical derivation | `docs/architecture/generation-ledger-canonical-derivation.md` | 613 lines | 5A.3 deliverable (v1.2); defines `ReadAtAnchor` algorithm + 6 specs |
| 5 | Payout artifact schema | `docs/architecture/payout-artifact-schema.yaml` | 523 lines | 5A.4.a deliverable (v1.1); schema 5B will derive, 5C gates on, 5D verifies |
| 6 | F5 Phase 5A plan v3 | `docs/plans/implementation/f5-phase-5a-plan-v3.md` | 179 lines | The plan that this report closes |
| 7 | Synthetic-ID refactor (code) | `internal/ledger/canonical_id.go` + `_test.go` + 14-callsite updates across 4 packages | ~70 lines new code + ~150 lines test + targeted callsite edits | 5A.4.b deliverable; surface for 5B PayoutRecord-based derivation |
| 8 | CI lint package (code) | `internal/settlement/lint/` (10 files: doc, input_manifest, manifest_loader, extractor, matcher, helpers_test, lint_test, negative_test, repository_test, testdata/) | 1086 prod + 1265 test lines, 23 tests | 5A.4.c deliverable; ships warnings-now / failures-at-5D posture |

Total documentation: ~3651 lines across 6 design/audit/manifest/schema/plan documents. Total code: ~2500 lines across 12 new files + targeted edits to 4 existing files.

## 3. Architect sign-offs consolidated

Every architect decision across Phase 5A, in one place, for traceability and reference during 5B plan v1 drafting and beyond.

### 3.1 Sequencing

- **Option (b) sequencing — F5 ships with stub-W; locked Reputation-and-Consensus-Integrity workstream swaps in real W via canonical `ReputationActivation` event**. Confirmed at Gate 5A.2 §13.5 architect decision. Rationale: locked workstream's W primitive doesn't exist yet (steps 3+ of locked plan §17 unimplemented); F5 cannot block on it; stub matches today's effective production behavior (existing `ValidatorReputationStore.RecordVote` has zero production callers per 5A.1 audit §4.7, so today's `ValidatorQScoreFn` always returns NeutralBP).

### 3.2 Q-score design

- **Q-C confirmed as default** (canonical Q projection read at canonical cutoff). Plan v3 §3.2; 5A.2 §0 Decision.
- **Epoch-coarse cutoff** (NOT round-precise): cutoff anchor for round R is the snapshot at end of R's immediately-prior epoch, matching the locked W projection's `epoch` parameter. 5A.2 §11; verified against locked plan §5.2 SN-1.
- **`CanonicalWProjection` interface verified** against locked plan §4.1: 6 parameters `(validator, family, category, contributor, escrowBudget, epoch) → (uint64, error)`. Stub `NeutralBPStubW` ships F5; locked workstream supplies real W.

### 3.3 Activation semantics (V-1 invariant)

- **V-1 invariant — canonical-position-bound activation**: W implementation for round R is selected by R's canonical ordering relative to `ReputationActivation`, NOT by node's runtime activation state at settler execution time. 5A.2 §7.1 (post-multi-AI-review).
- **Enforcement via canonical-ancestor check**: derivation function uses `IsAncestor(ReputationActivation_event_id, R.canonical_seal_context)` — pure canonical-state function, no runtime flag. 5A.2 §7.2.
- **Materialization-lag deferral**: settler defers R's settlement on `ErrEventNotFound` from `IsAncestor`, reusing F3-B causal-prerequisite-gating pattern (D-1 through D-8). V-1-preserving (returning false would be a V-1 violation; returning wrong answer is forbidden). 5A.2 §7.2 v2.1 patch + 5A.3 §2.3.
- **Emission ordering (Grok)**: `ReputationActivation` cannot be emitted before F5 activation; admission gate at consumer layer enforces causal prerequisite. 5A.2 §7.3.
- **Replay/catch-up**: V-1 corollary — replay-invariance is automatic when selection is canonical-state-only. 5A.2 §7.4.

### 3.4 Schema choices

- **SHA-256 hash algorithm** (NOT BLAKE3): codebase consistency with EventID hashing per `internal/event/event.go:221`. Confirmed at Gate 5A.4.a Q-1 resolution.
- **RFC 8785 JCS canonical serialization**: codebase convention for fresh canonical-JSON work. Existing implementation at `internal/auth/canonical.go` with cross-language Go/Python parity verified by `internal/auth/canonical_test.go` and `sdk/python/tests/test_canonical_crosslang.py`. EventID's historical use of `json.Marshal` is a documented historical artifact preserved for backward compatibility, NOT a precedent for new canonical artifacts. Confirmed at Gate 5A.4.a (canonical-JSON-by-name-specification fix).
- **Ordinal-assignment rule LOCKED at schema level** (4-step rule): Group by `purpose.tag` → sort by `recipient.id` lex order within each tag group → tag groups in fixed sequence (worker_payout → poster_refund → validator_distribution → gen_ledger_royalty → treasury_remainder) → ordinal sequential from 0, monotone across full sequence. Schema specifies the canonical contract; 5B implements TO this; 5A.4.c CI lint verifies; 5D asserts cross-node byte-equality. Confirmed at Gate 5A.4.a Q-3 resolution.
- **`canonical_cutoff_anchor: EventID | nil` (Fix A)**: nil iff `ReputationActivation` is NOT a canonical ancestor of `R.canonical_seal_context`; non-nil (locked workstream's snapshot encoding) iff it is. Hash preimage handles nil as a distinct canonical value per RFC 8785 explicit-null encoding. Placeholder approaches considered earlier (empty-string sentinel, epoch-index-encoded fallback) REJECTED — they reintroduced V-1-violating semantics. EventID | nil with canonical-position-bound semantics is the only V-1-preserving encoding. Confirmed at Gate 5A.4.a Q-2 resolution.
- **Recipient role enum LOCKED**: {Worker, Validator, Treasury, PosterRefund, GenLedgerAncestor}. Extension is new interface version per V-1 §7.5 version-binding rule, NOT source-compatible additive change.
- **`purpose.tag` LOCKED vocabulary**: 5 settlement-purpose strings (settlement.worker_payout, .poster_refund, .validator_distribution, .gen_ledger_royalty, .treasury_remainder).
- **Schema preserves removed inputs** with `target_classification: removed` per Gate 5A.1 §9.4 architect decision (5D regression detection requires baseline).

### 3.5 Generation-ledger ancestry

- **DAG reader consolidation path α**: move `DAGAnchorReader` from `internal/dispatch/anchor.go` to `internal/dag/anchor_reader.go` (new file, neutral package); retire minimal `settlement.DAGAncestorReader`; gen-ledger BFS imports the consolidated reader. Principle 6. Confirmed at 5A.3 pre-design.
- **`ReadAtAnchor` is an algorithm, not a new method**: built on existing `Tips() + IsAncestor() + Get()` interface. 5A.3 §2.2.
- **Anchor-in-result semantic = option (a) anchor IS included** when reachable. 5A.3 §2.2.1; verified properties A-1, A-2, A-3.
- **Cycle-exclusion DEFERRED** to locked workstream's pair-aggregate + challenge-path. Risk-assessed at gen-ledger 2% pool; transparent-residual framing accepted. 5A.3 §5.
- **Quality function stub stays neutral** (NeutralQualityStub returns NeutralBP); future swap follows V-1 pattern analogous to `NeutralBPStubW`. 5A.3 §6.
- **Same-epoch-ancestor-exclusion ACCEPTED** as deliberate consequence of epoch-coarse cutoff alignment with W. Grok validated no material gaming incentive. 5A.3 §7.2 + Gate 5A.3 architect sign-off.

### 3.6 Refactor and CI lint

- **Synthetic-ID refactor signature change** (architect direction): `TransferFromBucketLabeled` accepts caller-supplied `eid event.EventID` rather than generating from `bucketCounter`. ID generation moves to derivation layer (callsites compute via `CanonicalSyntheticID` helper for 5A.4.b interim; 5B will centralize via PayoutRecord derivation). 5A.4.b architect direction.
- **CI lint warnings-now / failures-at-5D posture**: undeclared in-scope reads surfaced as informational warnings during 5A (manifest covers ~97 source_locations vs ~2363 in-scope AST-read-shaped sites; most of the 2363 are mechanical local-variable reads, not semantic derivation inputs). Capability proven via `TestLint_FailsOnUndeclaredRead` synthetic-module test; active enforcement gate flips at 5D when the manifest is expanded for comprehensive regression-detection coverage. Threshold change is a one-line edit. Confirmed at Gate 5A.4.c architect decision.
- **CI lint scope**: `internal/settlement/`, `internal/escrow/`, `internal/ledger/`, `internal/reputation/` only. Out-of-scope-but-adjacent (`internal/staking/`, `internal/fees/`, `internal/identity/`) NOT scanned. Future workstreams (F8 stake-manager defense-in-depth, post-F5 broader transfer workstream, Reputation Step 4) own those domains.

### 3.7 Discipline

- **5A.1 task.Status reopen condition**: option (b) accepted (idempotency-bounded harmlessness without canonicalization), with explicit reopen condition documented in manifest if 5B discovers task.Status influences payout math (not just short-circuit). Gate 5A.1 §9.2.
- **`AdmissionRecord.DAGAnchor` MUST NOT be used for V-1 ancestor check**: that field is C-15 non-canonical node-local state (per-node per-admission marker; differs across nodes for the same round). V-1 derives from R's own canonical event ID (R.canonical_seal_context). Documented in 5A.2 v2.2 §7.2 with explicit warning.
- **`R.canonical_seal_context` ≠ `R.SubmissionEventID`**: distinct canonical handles serving different purposes. canonical_seal_context = R's TVConsensus finalization event (5A.2 V-1 check); SubmissionEventID = R's task-submission event (5A.3 BFS root). Both 5A.2 and 5A.3 documents include the disambiguation table per ChatGPT Finding 1 at Gate 5A.3 round 2.

## 4. Gate-report notes (captured during 5A.3 multi-AI review for F5 ship docs)

### 4.1 Grok cycle-exclusion honesty-clause wording

For the F5 ship documentation, the cycle-exclusion deferral note uses this final wording (replaces all earlier drafts):

> **"Until the challenge-path workstream ships, gen-ledger royalty (2% of accepted-task budgets) may be captured by coordinated collusion rings that the pair-aggregate detection layer has not yet acted upon. This is an acknowledged, time-bounded residual. Operators receive pair-aggregate alerts today; no economic loss is invisible."**

Mirrors the locked plan §8.4 honesty-clause pattern. Lands in F5 ship docs (post 5B/5C/5D), not in any 5A document directly.

### 4.2 Grok first-encounter determinism load-bearing note

For the F5 completion gate report and any future maintenance documentation:

> **"BFS hop ordering is lex-sort on EventID at every hop (per 5A.3 §3.2). This is load-bearing for first-encounter-wins determinism; downstream allocator sort is defense-in-depth only. Future maintainers removing the lex-sort at any hop would reintroduce non-determinism masked by downstream sort — the same class of bug as 5A.1 §4.3 float-path remainder absorption."**

Captured in this report. Future contributors editing `internal/settlement/generation_ledger_calculator.go` (or its 5B replacement) MUST preserve the per-hop lex-sort.

### 4.3 ChatGPT economic-substrate-vs-semantics deferral

For the F5 completion gate report:

> **"5A.3 canonicalizes the generation-ledger traversal substrate. It does NOT prove that the economic semantics layered on top (first-encounter-wins, anchor-in-result, same-epoch-exclusion) match intended royalty behavior in all task topologies. Those economic correctness questions are downstream of the substrate work; future economic-analysis workstreams (or the challenge-path workstream's slashing policy) are the appropriate home. 5A.3's deliverable is the canonical substrate; economic policy lives elsewhere."**

Documented residual. F5 5B implements derivation against the substrate; economic-correctness analysis remains a separate concern owned by future workstreams.

## 5. Forward notes for 5B plan v1 drafting

### 5.1 Plan v3 §0.4 testnet-wipe-at-merge reminder

At F5 merge to main, **all testnet nodes wipe state**; replay semantics apply from genesis forward under post-gate derivation rules only. Historical divergent state on `f4-combined-0e93f48` is NOT migrated. 5B verification at testnet does NOT need to reconcile pre-F5 ledger divergence; the wipe gives a clean slate.

### 5.2 What 5B consumes from 5A

Interfaces, schemas, primitives, and patterns 5B inherits and uses without redesign:

- **`CanonicalWProjection` interface** (`docs/architecture/q-score-canonicalization-design.md` §0.8.1): 6-parameter signature locked. F5 5A.2 ships `NeutralBPStubW`; 5B's derivation function calls `Lookup` against whichever implementation is active per V-1 canonical-ancestor check.
- **`ReadAtAnchor` algorithm** (`docs/architecture/generation-ledger-canonical-derivation.md` §2.2): 5B's gen-ledger derivation invokes this algorithm against the consolidated `dag.AnchorReader`.
- **`PayoutRecord` schema** (`docs/architecture/payout-artifact-schema.yaml` v1.1): 5B's derivation function produces records conforming to this schema. canonical_id computed via SHA-256 + RFC 8785 JCS over the record (excluding canonical_id field).
- **Ordinal-assignment rule** (schema §`purpose.ordinal.ordinal_assignment_rule`): 5B implements TO this rule. CI lint verifies; 5D harness asserts.
- **`CanonicalSyntheticID` helper** (`internal/ledger/canonical_id.go`): the 5A.4.b interim helper that 5B will subsume. 5B's derivation function emits full `PayoutRecord` and uses `record.canonical_id` directly; the per-callsite helper invocation becomes dead code at 5B time.
- **`TransferFromBucketLabeled` signature** (post-5A.4.b refactor): accepts `eid event.EventID` as caller-supplied first parameter. 5B's derivation passes the `PayoutRecord.canonical_id` directly.
- **V-1 invariant** (5A.2 §7.1) and **Input-Domain-1/2/3/4 invariants** (5A.1 manifest §3.1): 5B's derivation function MUST satisfy these. The 5A.4.c CI lint enforces declared-reads discipline (warnings during 5A, will flip to failures at 5D).
- **Settlement input manifest** (`docs/architecture/settlement-input-manifest.yaml`): 5B reads from `target_*` fields per the consumer contract documented in §3.1. Manifest will be expanded during 5B implementation to cover the full canonical input domain (drives the 5D enforcement-gate flip).

### 5.3 What 5B produces

5B's primary deliverable: **a pure derivation function** that replaces the imperative `escrow.ReleaseSettlement` flow with:

```
derive_settlement(R: TaskVerificationRound) → []PayoutRecord, err
```

Pure function of canonical inputs at cutoff_anchor_for(R). Returns deterministic, ordered payout records satisfying U-1 uniqueness and the canonical-position-bound activation semantics.

Concretely:
- Reads canonical inputs per the manifest's `target_*` fields (the enforcement boundary).
- Computes per-validator W via `CanonicalWProjection.Lookup` (stub during pre-ReputationActivation period; real W post-activation; selection canonical-position-bound).
- Computes gen-ledger ancestry via `ReadAtAnchor` (with deferral on ErrEventNotFound).
- Produces `PayoutRecord` values with content-hashed canonical_ids per the schema.
- Output is consumed by a refactored applicator (replacing `escrow.ReleaseSettlement` per F5 plan v3 §3.5 + F4C halt characterization §7).

5B also lands the **applicator refactor**: replaces the 5-paid-flag-pattern check-then-transfer-then-set logic in `escrow.ReleaseSettlement` (5A.1 audit §4.6 — the load-bearing concurrent-Apply race site) with a record-driven application that takes derived payout records and writes them with idempotency keyed on canonical_id.

5B's halt-trigger surface includes: any 5B implementation finding that requires a 5A architectural change (would require gate reopens), any test surface finding that the architect's "no breaking consumers" prediction was incomplete, any cross-node verification at testnet that surfaces divergence not covered by 5A invariants.

### 5.4 Discovery-tax predictions carried forward from 5A.3 multi-AI review

For 5B implementation alertness:

- **Materialization-lag in genesis replay**: `DAGAnchorReader.ReadAtAnchor` and V-1 ancestor checks defer settler on `ErrEventNotFound`. 5B implementation must wire the deferral path through to the settler's retry mechanism (F3-B causal-prerequisite-gating reuse). Likely surfaces during 5B testnet verification (snapshot-restored or fresh-genesis nodes hit the deferral path before ones that admitted real-time).
- **First-round-of-epoch boundary race interaction**: the cutoff anchor binding crosses an epoch boundary at the first round of each new epoch; 5A.3 §7.2 documented same-epoch-ancestor exclusion as a deliberate consequence. 5B testnet should observe this behavior and confirm it matches expected payout shape; if not, surface as economic-policy concern (per gate-report note 4.3).
- **Equivocation evidence is INERT today**: locked workstream §17 step 7 owns canonical anchoring of equivocation evidence; F5 5A.3 §6.4 explicitly excluded equivocation from F5 scope. 5B should NOT touch equivocation paths; if 5B implementation reveals coupling, surface as scope-boundary issue (likely needs locked workstream coordination).
- **task.Status reopen condition** (5A.1 §9.2): if 5B discovers task.Status influences PAYOUT MATH (not just short-circuit), task.Status reverts to canonicalization candidate (option a — anchor-scoped task-status projection). Watch for this during 5B implementation of the derivation function.
- **`contributor` parameter ambiguity** (5A.2 §0.8.3): the locked W's semantic use of `contributor` is not visible in §4.1; 5B's stub-to-real swap test (`TestLint_FailsOnUndeclaredRead` analog at 5D) MUST verify contributor propagation; coordinate with locked-workstream author when real W lands.

## 6. Meta-lessons for future workstreams

### 6.1 V-1 pattern generalization

Any workstream that introduces a "swap implementation X for Y at activation event A" mechanism MUST default to **canonical-position-bound selection** (V-1 pattern: selection by R's canonical position relative to A) rather than runtime-flag-bound (the older, error-prone pattern that couples selection to local activation state). Runtime-flag-bound requires explicit justification; default rejection.

The pattern was first surfaced in F4 (mutex-claim error: post-condition mistaken for pre-condition serialization), generalized in F5 5A.2 (W activation), reinforced in F5 5A.3 (quality-function future activation). Future workstreams introducing similar implementation-selection mechanics inherit this default.

### 6.2 Locked-workstream inventory discipline

Before drafting any architecturally substantive design, **inventory `docs/plans/` for locked or in-progress workstreams touching the same primitives**. F5 5A.2's most consequential discovery (the locked Reputation-and-Consensus-Integrity workstream already specifies the W projection F5 needs) was caught at the start of substantive design rather than mid-implementation. Pre-drafting verification at every sub-phase boundary (5A.3 prep, 5A.4 prep) caught additional overlap-and-coordination opportunities.

The 20-minute discipline pays for itself in avoided redesign tax. Required for every new workstream's substantive-design phase.

### 6.3 Multi-AI review ROI at gate boundaries

Multi-AI review (Grok + ChatGPT) at every gate boundary (5A.1, 5A.2, 5A.3) produced load-bearing findings that neither architect nor Claude Code caught alone:

- **Gate 5A.1**: ChatGPT taxonomy split (canonical-derived vs non-canonical-artifact); retrieval-mode field; cutover preparation matrix. Grok shadow-mode classification scope; pre-derivation control-flow reads; out-of-scope adjacent surfaces.
- **Gate 5A.2**: ChatGPT + Grok convergent V-1 invariant (canonical-position-bound vs runtime-flag-bound) — the most consequential structural finding in Phase 5A. Independent identification from two different framings = high-confidence signal.
- **Gate 5A.3**: ChatGPT 4 substantive precision findings (terminology distinction R.SubmissionEventID vs R.canonical_seal_context; ancestor-set seed-inclusive clarification; NeutralQualityStub future-versioning; test adapter migration items). Grok cycle-exclusion wording + first-encounter determinism warning + economic-substrate-vs-semantics framing.

Budget for multi-AI review at every architectural-decision gate. The cost is small; the structural-gap-catch ROI is high.

### 6.4 Mid-draft-discovery cleanup discipline

**When a mid-draft discovery invalidates earlier sections, REWRITE or DELETE them — don't annotate with "still valid."** F5 5A.2 v1 → v2 → v2.1 → v2.2 lineage demonstrated this lesson. v1's foundational sections (drafted under "design from scratch" assumption) became misleading after the locked-workstream-extension discovery; v1.1 added "still valid" annotations that confused reviewers; v1.2 cleanup deleted the stale framing entirely.

Defer-with-annotation creates document drift. Aggressive rewrite-or-delete preserves document trustworthiness. Same lesson applies to design docs evolving across multi-AI review rounds.

## 7. 5B readiness assessment

All 5A readiness criteria met:

✅ **Settlement input audit complete** (5A.1 manifest at "unique input" granularity covers the 5A-relevant derivation surface; 5D will expand to comprehensive coverage when wiring full enforcement gate).

✅ **CanonicalWProjection interface locked** (5A.2 §0.8.1) and verified against locked-workstream-author's W spec.

✅ **NeutralBPStubW implementation specified** (5A.2 §0.8.6); 5B Phase 5B can ship the stub as a small concrete struct.

✅ **V-1 invariant + canonical-ancestor enforcement specified** (5A.2 §7); 5B's derivation function knows exactly how to select stub vs real W per round canonical position.

✅ **Materialization-lag deferral pattern specified** (5A.2 §7.2 + 5A.3 §2.3); 5B inherits the F3-B causal-prerequisite-gating semantic.

✅ **`ReadAtAnchor` algorithm specified** (5A.3 §2.2); 5B's gen-ledger derivation has a deterministic algorithm to invoke.

✅ **DAGAnchorReader consolidation path locked** (path α; 5A.3 §0.1); 5B implementation includes the rename + relocation.

✅ **Cycle-exclusion deferral explicit** (5A.3 §5); 5B doesn't need to design cycle exclusion; locked workstream's challenge path owns it.

✅ **NeutralQualityStub specification** (5A.3 §6.4); 5B can ship the stub.

✅ **PayoutRecord schema locked** (5A.4.a v1.1); 5B derivation produces records to this schema with all locked invariants (U-1 uniqueness, V-1 compatibility, Input-Domain-1/2/3/4).

✅ **Synthetic-ID refactor surface ready** (5A.4.b); `TransferFromBucketLabeled` accepts caller-supplied `eid` so 5B's derivation function can pass `PayoutRecord.canonical_id` directly. Interim `CanonicalSyntheticID` helper in place during the transition; 5B subsumes it.

✅ **CI lint package shipped** (5A.4.c); 5B-introduced reads will be visible to the lint as the manifest is expanded; warnings-now / failures-at-5D posture preserves current build green during 5B drafting.

✅ **Consumer audit reconciliation confirmed** (5A.4.d); zero-breaking-consumers prediction held; refactor surface is clean for 5B to build on.

✅ **All architect decisions consolidated** (this report §3); 5B plan v1 author has single-document reference for what's locked.

✅ **Forward notes captured** (this report §5); 5B plan v1 author has explicit prediction surface for likely discovery items.

✅ **Cluster frozen** at `f4-combined-0e93f48`; testnet wipe at F5 merge per Plan v3 §0.4 means 5B verification doesn't need to reconcile pre-F5 ledger divergence.

**Phase 5A is ready to close. 5B plan v1 drafting can begin next session.**

---

## 8. Recommended next steps

1. Architect review of this completion gate report.
2. Founder confirmation closes Phase 5A.
3. Next session: 5B plan v1 drafting, consuming this report's §5 forward notes as the starting brief.
4. After 5B plan v1 multi-AI review + architect approval: 5B implementation begins.
5. F5 (5A through 5D) closes with combined F4+F5 main merge per Plan v3 §0.4 (testnet wipes at merge; replay semantics post-gate).

---

**End of F5 Phase 5A Completion Gate Report.** 2026-04-24.
