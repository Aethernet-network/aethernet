# F5 Phase 5A — Input-Domain Canonicalization — Plan v3

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5A (of 5A/5B/5C/5D)
**Version**: v3 — founder-approved 2026-04-23 after multi-AI review (Grok + ChatGPT) and Claude Code plan-mode review.
**Status**: Phase 5A.1 complete and Gate 5A.1 closed (2026-04-23, post-multi-AI review). Phase 5A.2 in progress.

This document is the on-disk record of the plan that was iterated through architect-session conversation. The approval/review chain is preserved in `docs/historical/journal.txt`. The full v3 text from the architect-session was written to disk at Gate 5A.1 closure to capture the 5A.2 deliverable expansion described in §3.2 below.

---

## 0. Decisions locked before implementation

1. **F5 is escrow-settlement-scoped** plus all output artifacts emitted by settlement (including the transfer-record projection layer). Transfer events in general and stake events stay out. Canonical emission is F6.
2. **F5 is phased into 5A/5B/5C/5D** with internal architect-session gates. This document covers 5A only.
3. **Derivation direction is locked.** F5 closes the concurrent-Apply race by making settlement a pure function of canonical inputs, not by serializing imperative mutation.
4. **F4 does not merge to main until F5 closes.** Combined F4+F5 merges together. Historical divergence on the frozen f4-combined-0e93f48 testnet is forward-only and not reconciled. **At F5 merge to main, testnet state wipes; replay semantics apply from genesis forward under post-gate derivation rules only. Historical divergent state is not migrated.**
5. **Reputation Step 4 remains paused** until F5 completes.
6. **Float-path excision becomes a consequence of F5.** `shadowMode` is forbidden in derivation. Integer-only arithmetic throughout settlement. Float-path removal is a cleanup PR landing alongside or immediately after F5 (was previously F11; now absorbed).
7. **Silent-zero family (F7), stake-manager defense-in-depth (F8), Divergence (A) characterization (F9) remain queued**, non-blocking, post-F5.
8. **Canonical emission (F6) is a separate workstream** that follows F5. F5 makes consumers robust to multi-emit; F6 eliminates multi-emit at the source.
9. **Q-C is the locked default design for Q-score canonicalization** (canonical Q projection read at canonical cutoff). Q-B is acceptable alternative if 5A.2 surfaces blocker. Q-A rejected. Q-D (advisory fallback) is emergency valve.

---

## 1. Load-bearing framing

Bug F5 closes: concurrent-Apply race in settlement. Two TVConsensus events for the same RoundID racing through `settler.Settle → escrow.ReleaseSettlement` on a single node, partially draining the escrow bucket via the per-recipient paid-flag pattern, erroring mid-payout on insufficient balance, silently converging the admission record to StateApplied with permanently-diverged ledger state across nodes.

Architectural statement F5 establishes:

> *Settlement effects for a given settlement key are a pure function of canonical settlement context. Every correct node computes the same ordered payout multiset and the same terminal settlement summary. No local mutable execution state influences the result. Recomputation after crash, replay, re-admit, or duplicate observation yields byte-identical outputs.*

Phase 5A is the input-domain work. Phase 5A enumerates every variable read during settlement, canonicalizes or forbids each, produces scaffolding (invariants, manifest, schema, cutover matrix) for Phase 5B's derivation implementation.

---

## 3. Phase 5A scope

### 3.1 Sub-phase 5A.1 — Settlement input audit

Deliverables produced and Gate 5A.1 closed at 2026-04-23 post-multi-AI review:
- `docs/audits/2026-04-23-settlement-input-audit.md`
- `docs/architecture/settlement-input-manifest.yaml`

Five 5A.1 closure changes from multi-AI review absorbed into manifest + audit:
1. `semantic_role` column added to manifest (derivation / control-flow-idempotency / observability). Every in-scope row classified.
2. `task_status` row rewritten: classification stays canonical-live, semantic_role = control-flow-idempotency, target_retrieval_mode stays mutable-store-read (formally exempt from Input-Domain-4). proof_obligation field added with explicit 5B guarantee text.
3. `validator_q_score` upgraded to HIGH mixed-version-risk in cutover matrix. Projection-version-binding rule must be enforced at gate time (5A.2 deliverable item 7).
4. Finding 6 (escrow paid-flag persistence) disposition rewritten with explicit 5B architectural obligation (a/b/c clauses); to be captured in `docs/architecture/locked-invariants-review-f5a.md` when created.
5. semantic_role classifications applied: control-flow-idempotency rows = task_status, applicator_applied_set, escrow_entry_paid_flags, escrow_has_settlement_started; observability rows = metrics callbacks, time.Now() sites, time_now_recorded_at, payload_finalization_time_unix.

### 3.2 Sub-phase 5A.2 — Q-score canonicalization design (12-item expanded deliverable list)

**Deliverable**: `docs/architecture/q-score-canonicalization-design.md`

Locked default design: **Q-C (canonical Q projection read at canonical cutoff)**.

Plan v3 §3.2 v3-as-published called for 5 deliverable items. Multi-AI review (ChatGPT 5 additions + Grok 2 additions) at Gate 5A.1 closure expanded the 5A.2 design doc to a **12-item required output list**:

1. **Historical-read semantics at cutoff anchor, formally specified** (Plan v3 Finding 1). Projection keyed by `(validator, category, anchor)` with historical anchors as first-class retrieval keys. Advancement of the projection does NOT mutate values at prior anchors.
2. **Storage/replay cost bounds**, including worst-case rebuild and query complexity. Quantified: storage growth per anchor, query cost at lookup time, full-replay cost from genesis.
3. **Projection cutoff semantics aligned with Input-Domain-2/3.** The cutoff for round R is the DAG anchor at the canonical `IsComplete(R) = true` moment, which is itself a pure function of the canonical vote set.
4. **Coupling analysis versus existing advisory reputation projection.** Q-C introduces a new projection that coexists with the existing advisory reputation projection. Document the interaction surface, write paths, read consumers of each.
5. **Integration interface for Reputation Step 4.** Specify the API and semantic guarantees Reputation Step 4 will build on.
6. **(ChatGPT new) Evidence domain definition**: exactly which canonical events update canonical Q, in what order, with what exclusion rules. Specify each event type that contributes evidence (TVConsensus events, slashing events, validator-set changes); ordering rules; exclusion rules for malformed/equivocating evidence.
7. **(ChatGPT new) Version-binding rule**: projection schema/version is part of gate semantics, NOT an implicit binary detail. The schema/version of the projection is canonically named in the gate event (5C); two nodes with different projection versions reading the "same" cutoff anchor return well-defined behavior (either both succeed at the same version-bound value, or one returns an explicit version-mismatch error). Mixed-version cluster operation is impossible by design.
8. **(ChatGPT new) Bootstrap/recovery behavior**: reconstruction after restart, snapshot restore, replay from genesis. Specify how a node bootstraps the projection from canonical evidence; how the projection is restored from a snapshot; how genesis-replay rebuilds it deterministically.
9. **(ChatGPT new) Absence-of-data policy**: neutral/default Q for new validators with no history; missing category/family history; tie/zero-total-weight behavior. Each absence case has a defined, deterministic Q value or error.
10. **(ChatGPT new) Interaction with generation-ledger quality**: whether `qualityFn` (currently a neutral stub) uses the same projection, a sibling projection, or a derived view. Affects 5A.3 ancestry design.
11. **(Grok new) Cutoff anchor precision**: the anchor is the DAG anchor at IsComplete-true moment, NOT just "any anchor at the same round". Formally define the relationship between round-seal and anchor selection; prove that the anchor is uniquely determined by canonical state.
12. **(Grok new) Scaling target**: p99 lookup < 100µs at 10k QPS sustained with hot-validator cache. Specify cache design (LRU? size? eviction policy); benchmark methodology; halt condition if scaling cannot be achieved within design constraints.

**Gate 5A.2**: architect-session + multi-AI review of the 12-item deliverable. Q-C confirmed (or specific reason for Q-B documented). Canonical Q projection specification complete, with all 12 items addressed. Only after this gate does 5A.3 begin.

### 3.3 Sub-phase 5A.3 — Generation-ledger ancestry canonicalization

**Deliverable**: `docs/architecture/generation-ledger-canonical-derivation.md`

Six specifications required:
1. Ancestor selection semantics per hop (deterministic from canonical properties).
2. Dedup semantics.
3. Traversal order (BFS confirmed; child-visit order deterministic via sort key).
4. Cycle and reciprocal-reference exclusion (lock or document defer).
5. Quality function canonicalization (depends on Q-C / 5A.2 item 10).
6. Frozen-DAG-at-anchor primitive: design `DAGAncestorReader.ReadAtAnchor(anchor_id) → ancestor_set` (option a per Gate 5A.1 §9.3 architect decision).

Halt condition: if cycle exclusion requires broader reputation architecture, narrow scope to advisory royalty/even-split. Architect session decides.

### 3.4 Sub-phase 5A.4 — Synthetic-ID determinism + ID ownership architectural split

Deliverables:
1. Repo-wide synthetic-ID consumer audit (16 callsites enumerated in 5A.1 audit §4.7; ALL 16 included in test surface per Gate 5A.1 §9.6 architect decision).
2. Synthetic-ID refactor with ID ownership moved from ledger helper to derivation layer.
3. Payout artifact schema (`docs/architecture/payout-artifact-schema.yaml`).
4. CI lint — settlement input manifest scanner (`internal/settlement/lint/input_manifest.go`).
5. M2 polish comment at `verification_consensus_settler.go:516` (per Gate 5A.1 §9.7 architect decision).

Uniqueness invariant: `(settlement_key, recipient, purpose.ordinal)` triple in hash preimage prevents collision.

### 3.5 Float-path excision

Since shadowMode is non-canonical-artifact (forbidden in derivation), F5 forces integer-only arithmetic throughout settlement. Float path removal lands alongside Phase 5B as cleanup. Supersedes F4 plan v2 §12's F11 scheduling.

---

## 4. Invariants Phase 5A establishes

- **Input-Domain-1**: Every input classified per §3.1 taxonomy. Canonical-frozen, canonical-live, canonical-derived permitted. Non-canonical-artifact, local-live, advisory forbidden in derivation meaning.
- **Input-Domain-2**: Canonical-live inputs read at canonical cutoff (DAG anchor at round-seal).
- **Input-Domain-3**: Canonical cutoff is derived from round-seal state, not wall-clock.
- **Input-Domain-4**: Every canonical-live and canonical-derived input has deterministic, replayable lookup at cutoff. **Exemption**: control-flow-idempotency inputs (semantic_role classification per Gate 5A.1 closure) are formally exempt from Input-Domain-4 because they do not flow into derivation meaning; their proof obligations are documented per-row in the manifest.

These compose with F4's C-17 (Serialization-2) by extending "derive from canonical underlying state only" into settlement input selection and lookup realizability.

---

## 5. Deliverables summary

Phase 5A produces:
1. ✅ `docs/audits/2026-04-23-settlement-input-audit.md`
2. ✅ `docs/architecture/settlement-input-manifest.yaml`
3. (5A.2) `docs/architecture/q-score-canonicalization-design.md` — 12-item structure
4. (5A.3) `docs/architecture/generation-ledger-canonical-derivation.md`
5. (5A.4) `docs/architecture/payout-artifact-schema.yaml`
6. (5A.4) Synthetic-ID consumer audit (16 callsites in test surface)
7. (5A.4) Synthetic-ID refactor + cross-node byte-equality test
8. (5A end) `docs/architecture/locked-invariants-review-f5a.md` (includes Finding 6's 5B architectural obligation per Gate 5A.1 closure)

Plus completion gate report.

---

## 7. Cutover preparation matrix for 5C

The matrix is in `docs/architecture/settlement-input-manifest.yaml` per-row. Summary in `docs/audits/2026-04-23-settlement-input-audit.md` §7.

HIGH mixed-version-risk inputs:
- `bucket_counter` / `synthetic_transfer_id`
- `validator_q_score` (upgraded HIGH at Gate 5A.1 closure; projection-version-binding required)

---

## 9. Halt-and-surface triggers

Per v3 §9 (unchanged). Plus Gate 5A.1 closure addition: any 5A.2 design surface that cannot satisfy the 12-item deliverable list halts and architect-session reviews scope.

---

## Forward notes (not 5A.1 scope; carried to 5C/5D)

**Forward note for 5C — production-cutover strategy for synthetic_transfer_id**:

Document as a 5C deliverable. Two candidate approaches identified at Gate 5A.1 closure:
- **(a)** One-time migration job rewriting historical IDs to content-hash form at cutover.
- **(b)** Dual-read ledger queries with legacy-format fallback during a defined transition window.

Not resolved at Gate 5A.1. Resolved in 5C with a third concrete option if multi-AI review at that gate surfaces one. Plan v3 §0.4 testnet-wipe-at-merge means production-cutover-from-existing-state is not in scope at first F5 main merge; this strategy is for any future production cluster that adopts F5 with prior settlement history.

**Forward note for 5D — explicit Q-C performance gate in verification matrix**:

5D verification matrix gets explicit Q-C performance gate: **p99 lookup < 100µs at 10k QPS sustained with hot-validator cache**. Aligned with 5A.2 deliverable item 12 (Grok scaling target). 5D's verification harness includes a benchmark suite that asserts this gate; failure halts at Gate 5D.

---

## 14. Sign-off conditions

- ✅ Architect-session review (v1, v2, v3 produced).
- ✅ Multi-AI review (Grok + ChatGPT on v1+v2; absorbed into v3).
- ✅ Claude Code plan-mode review (10 findings on v2; absorbed into v3).
- ✅ Founder approval of v3.
- ✅ Implementation 5A.1 complete; Gate 5A.1 closed at 2026-04-23 post-multi-AI review on audit/manifest.
- ⏳ Implementation 5A.2-5A.4 in progress.
- ⏳ Phase 5A completion gate report.
- ⏳ Architect session reviews; Phase 5B plan v1 drafted.

---

**End of F5 Phase 5A Plan v3 (on-disk version, 2026-04-23 post Gate 5A.1 closure).**
