# Settlement Input Audit — Phase 5A.1

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5A.1 — Settlement input audit (deliverable 1)
**Status**: **Gate 5A.1 architect decisions accepted; awaiting multi-AI review.** All five boundary walks integrated (settler + payout math + applicator + generation ledger + hidden-input sweep + escrow ReleaseSettlement + ledger TransferFromBucketLabeled/bucketCounter). Architect decisions on §9 open questions reflected in §9 below and in the manifest.
**Commit**: `51bce89` (F4-frozen branch; tag `f4-complete-selection-race-closed-pending-f5`).
**Date**: 2026-04-23
**Plan reference**: F5 Phase 5A Plan v3 §3.1.

---

## 1. Scope

Walk every code path from `Dispatcher.admitOneLogicalKey` entry for a TaskVerificationConsensus event through final ledger state commit. Enumerate every variable read. Classify per Plan v3 §3.1 taxonomy with retrieval mode. Surface any halt-worthy finding.

Walk boundary: settlement package + transitively called surfaces in escrow, ledger (synthetic-ID surface), generation-ledger calculator, reputation store (qScoreFn resolution), and dispatch pre-derivation control-flow.

## 2. Methodology

Five parallel subagent walks, all complete:

- **A — Settler core**: `VerificationConsensusSettler.Settle` → `settleAccept`/`settleReject`/`settleDispute` → `computeValidatorPayouts` (integer + float) → `applicator.Apply`/`applyToTarget`/`applyTransfer`. **81 reads enumerated.**
- **B — Generation ledger + Q-score**: `GenerationLedgerCalculator.Calculate` + qScoreFn → `tvReputationStore.ValidatorQScore()` → `ValidatorReputationStore.Get()` → BadgerDB. Production wiring traced (`cmd/node/main.go:1949-1954`, `:1935-1939`). DAGAncestorReader analyzed. **11 detailed findings.**
- **C — Hidden-input sweep**: 11 targets across `internal/{settlement,escrow,ledger,dispatch}`. **PASS on all 11.**
- **D — Escrow ReleaseSettlement**: full walk of ReleaseSettlement + Get + HasSettlementStarted + RegisterEscrow + IsLocked + sortedAgentIDs. **85+ reads enumerated; 5 paid-flag race patterns documented with file:line.**
- **E — Ledger transfer.go + synthetic-ID surface**: TransferFromBucketLabeled + bucketCounter + RecordFromSync + Settle + validLedgerTransitions table + repo-wide bucket: consumer survey. **16 callsites enumerated; zero breaking consumers found.**

All classifications per Plan v3 §3.1 taxonomy. Retrieval modes per Plan v3 §3.1.

## 3. Aggregate counts

| Classification | Count | Disposition |
|---|---:|---|
| canonical-frozen | 60 | Permitted in derivation |
| canonical-live | 4 | Permitted IFF retrieval mode defined + Input-Domain-4 satisfiable |
| canonical-derived | 22 | Permitted in derivation |
| non-canonical-artifact | 38 | Forbidden in derivation meaning |
| local-live | 2 | Forbidden — must canonicalize or exclude |
| advisory | 5 | Diagnostics only |
| unclear | 1 | See §9 open questions |

Total: ~132 reads enumerated across the five walks. Counts exclude internal loop variables, lock acquire/release, and standard control flow.

## 4. Load-bearing findings

Seven findings that materially shape 5A.2–5A.4 design work and 5B implementation. Each is a confirmation-or-refutation of a Plan v3 assumption.

### 4.1 Q-score is live-mutable-store-read. Cross-node divergence confirmed possible.

**File:line**: production wiring at `cmd/node/main.go:1949-1954`; concrete at `internal/taskverification/reputation.go:74-89, 208-216`; settler call sites at `internal/settlement/verification_consensus_settler.go:448` (float path) and `:500` (integer path).

**Mechanism**: `qScoreFn` is bound at settler construction to `tvReputationStore.ValidatorQScore(ctx, validatorID, family, category)`. Concrete reads BadgerDB via `s.db.View(...)`. Each node has its own BadgerDB. Reputation written async via `RecordVote()` (`reputation.go:93-140`) on vote observation.

**Divergence arithmetic**: `AgreementRate() = (AgreeingVotes * 10000) / TotalVotes` at `reputation.go:48-52`. Node A receiving a vote earlier than Node B can produce different rate values for the same validator at the same round cutoff. Validator payouts diverge.

**Classification today**: `local-live`. **Target (5A.2)**: `canonical-live` via `CanonicalQProjection.Lookup(validator, category, cutoff_anchor)` per Plan v3 §3.2 Q-C design. Confirmed direction. Not halt-worthy.

### 4.2 DAGAncestorReader reads live DAG. No anchor-scoped primitive.

**File:line**: interface at `internal/settlement/generation_ledger_calculator.go:14-16`; concrete at `internal/dag/dag.go:405-413`; call sites at `generation_ledger_calculator.go:286, 314`.

**Mechanism**: `Get(id) (*event.Event, error)` takes no cutoff. Concrete reads `d.events` map under RLock. `TaskVerificationRound` has no anchor field.

**Cross-node determinism**: events are immutable once in DAG; BFS traversal at depth ≤ 3 produces same ancestor set IFF both nodes have materialized all ancestors. A node with materialization lag produces a shorter traversal. **Live reader is not anchor-equivalent by construction.**

**Classification today**: underlying content `canonical-frozen`; retrieval mode `live-DAG`. **Target (5A.3 spec #6)**: option (a) — design `DAGAncestorReader.ReadAtAnchor`. Recommendation: do NOT pursue option (b) live-equivalence proof; materialization-lag breaks it.

### 4.3 Float-path remainder-absorption is non-deterministic across nodes.

**File:line**: documented at `internal/settlement/verification_consensus_settler.go:420-425`; arithmetic at `:460-466`; generation-ledger float remainder at `internal/settlement/generation_ledger_calculator.go:229`.

**Mechanism**: float path absorbs rounding remainder at caller-slice-last recipient. `collectAgreeingValidators` iterates `round.Votes` whose receive order differs per node. Integer path (`protocolmath.AllocateWithCeiling`) sorts by CanonicalKey before allocation; float path does not.

**Classification**: `non-canonical-artifact` flow (cross-node-divergent output). **Target**: forbidden in derivation; float path becomes dead code post-shadowMode-excision (5B cleanup per Plan §3.5). Validates Plan §3.5 assumption.

### 4.4 task.Status is canonical-live via taskMgr.Get.

**File:line**: `internal/settlement/verification_consensus_settler.go:140-150` (read + idempotency branch).

**Mechanism**: `taskMgr.Get(payload.TaskID)` then switch on `task.Status`. Terminal-state short-circuit at `:146-150` returns `AlreadyApplied=true`. Status sourced from TaskManager projection (canonical event-derived) but read live, no cutoff.

**Implication**: divergent short-circuit possible. End state is idempotent (both paths reach same ledger effect) but control-flow diverges.

**Classification today**: `canonical-live` with `mutable-store-read`. **Target (architect open question §9.2)**: option (a) anchor-scoped task-status projection, OR (b) accept idempotency as sufficient. **Not halt-worthy** — needs architect decision on retrieval-mode target.

### 4.5 shadowMode is deterministic per canonical activation event.

**File:line**: field + RWMutex methods at `internal/settlement/verification_consensus_settler.go:50-80`; read at `:401`; gen-ledger counterpart at `generation_ledger_calculator.go:64, 128`. Writes only via `IntegerMigrationActivationConsumer` on canonical activation event projection.

**Within a single Settle call**: stable. **Cross-cluster**: deterministic per canonical activation. Validates Plan §3.5: classify as `non-canonical-artifact`, forbidden in derivation, removed alongside float-path excision.

### 4.6 Escrow paid-flag pattern: 5 race sites with identical structure (Agent D).

**File:line**: all in `internal/escrow/escrow.go`.

| Pattern | Recipient | Read Line | Held? | Transfer Line | Held? | Set Line | Held? |
|---|---|---:|:-:|---:|:-:|---:|:-:|
| 1 | Worker | 486 (`!entry.WorkerPaid`) | NO | 487 | NO | 491 | YES |
| 2 | PosterRefund | 497 (`!entry.PosterRefundPaid`) | NO | 498 | NO | 502 | YES |
| 3 | Per-Validator | 526 (`alreadyPaid := entry.ValidatorsPaid[vid]`) | YES | 531 | NO | 535 | YES |
| 4 | Per-GenRecipient | 553 (`alreadyPaid := entry.GenLedgerPaid[rid]`) | YES | 558 | NO | 562 | YES |
| 5 | Treasury | 568 (`!entry.TreasuryPaid`) | NO | 569 | NO | 573 | YES |

**All five patterns share**: read paid flag (some unlocked, some lock-then-unlock-before-transfer), call `TransferFromBucketLabeled` without holding lock, re-acquire lock to set flag. NOT atomic. Concurrent goroutines can both observe the same unpaid flag and both transfer.

**Classification**: `entry.WorkerPaid` / `ValidatorPaid` / `TreasuryPaid` / `PosterRefundPaid` / `ValidatorsPaid[vid]` / `GenLedgerPaid[rid]` are all `non-canonical-artifact` (mutable per-node state, idempotency guards only). Persisted to BadgerDB for crash recovery, NOT for cross-node sync.

**NEW finding from Agent D — cross-node persistence divergence**: `ValidatorsPaid` and `GenLedgerPaid` maps persist to per-node BadgerDB after each successful transfer (lines 537, 564). On retry across nodes with different concurrent-Apply outcomes, divergent paid-flag maps in persistent storage create permanent ledger divergence. This is the *persistence half* of the concurrent-Apply race; the race plus per-node persistence is what makes the divergence permanent (non-self-healing per Plan v3 characterization §10.3).

**Disposition (rewritten at Gate 5A.1 closure post multi-AI review)**: paid-flag persistence may remain non-canonical, **provided 5B**:

- (a) derives the canonical payout record FIRST (pure function of canonical inputs at cutoff anchor),
- (b) applies flags as a pure projection of that record only (flags are emitted/written as a function of the derived record, never read back to influence the record's content), AND
- (c) NEVER uses previously-persisted flag prefixes to determine canonical semantic behavior (paid flags serve strictly as node-local crash-recovery cache; they do not gate WHICH payouts to make, only WHETHER to skip a payout that's already been applied on this node).

This 5B architectural obligation is the precondition for the "defense-in-depth only" framing. If 5B violates any of (a)/(b)/(c), the persistence layer becomes a hidden derivation input and 5A.1 reopens.

This obligation will be formally captured in `docs/architecture/locked-invariants-review-f5a.md` when that document is created at end of Phase 5A.

### 4.7 Ledger synthetic-ID surface: zero breaking consumers (Agent E).

**File:line**: `internal/ledger/transfer.go:49` (`bucketCounter atomic.Uint64`); `:500-501` (Add + format `bucket:fromID:toID:amount:counter`); 16 callsites of `TransferFromBucketLabeled` enumerated.

**Cross-node determinism**: NO. `bucketCounter` is process-local, resets on restart, increments at different rates per node. Same `(fromID, toID, amount)` tuple produces different IDs.

**Repo-wide consumer survey (5A.4 consumer audit)**:
- **Sites that PARSE `bucket:` prefix**: ZERO. No code in the repo decomposes synthetic IDs.
- **Sites that ASSERT exact synthetic IDs in tests**: ZERO. Tests verify balance semantics by AgentID (e.g., `tl.Balance(crypto.AgentID("escrow:" + taskID))`), not by synthetic-transfer IDs.
- **Restart-collision risk** (Agent E §8.B): bucketCounter resets on process restart; new IDs after restart can collide with pre-restart IDs for different `(from, to, amount)` tuples. Content-hash refactor (5A.4) fixes this as a side effect.

**Disposition**: 5A.4's content-hash refactor has **NO breaking consumers**. Refactor is significantly de-risked. The 16 callsites are call-through; they invoke `TransferFromBucketLabeled` and persist the returned ID — no decomposition. Refactor changes the ID-generation function's body; callsites unchanged.

**16 callsites** (escrow + fees + staking — fees/staking are out-of-scope for F5 but use the same generator):

| File:line | Caller | F5 scope? |
|---|---|:-:|
| internal/ledger/transfer.go:482 | TransferFromBucket (wrapper) | indirect |
| internal/fees/collector.go:234 | validator share fee | out-of-scope (general) |
| internal/fees/collector.go:237 | treasury share fee | out-of-scope |
| internal/staking/staking.go:243 | staking lock | out-of-scope (F8) |
| internal/staking/staking.go:271 | staking unlock | out-of-scope (F8) |
| internal/escrow/escrow.go:366 | full release | F5 |
| internal/escrow/escrow.go:411 | worker release (net) | F5 |
| internal/escrow/escrow.go:422 | validator release (net) | F5 |
| internal/escrow/escrow.go:433 | treasury release (net) | F5 |
| internal/escrow/escrow.go:487 | worker payout (settlement) | F5 |
| internal/escrow/escrow.go:498 | poster refund | F5 |
| internal/escrow/escrow.go:531 | validator distribution | F5 |
| internal/escrow/escrow.go:558 | gen-ledger royalty | F5 |
| internal/escrow/escrow.go:569 | treasury fee (settlement) | F5 |
| internal/escrow/escrow.go:600 | poster refund (cancel) | F5 |

Note: out-of-scope-but-adjacent callsites (fees, staking) inherit the new ID generator semantics automatically since they use the same function. Whether the consumer audit broadens to F5 scope or stays out-of-scope is an architect call (§9.6).

**`validLedgerTransitions` confirmation** (Agent E §5-§6): table at `transfer.go:280-289`. `[Settled][Settled] = false` is the atomic barrier. `Settle` holds write lock for read → validate → mutate → persist (`transfer.go:297-321`). Confirms Plan characterization §10.1 atomic-barrier claim used to determine that Divergence (A) is a different mechanism than the TVConsensus race.

## 5. Hidden-input sweep results — PASS on all 11 targets

| Target | Result | Notes |
|---|:-:|---|
| 5.1 Time-based reads | PASS | 3 `time.Now()` sites + 1 ticker, all observability/control-flow only |
| 5.2 Epoch reads | PASS | dispatcher-layer only; settler does not read epoch |
| 5.3 Validator seat snapshot | PASS | activeWeightFn pre-Apply only; settler does not re-read |
| 5.4 Identity-manager lookups | PASS | post-settlement side effects, errors discarded |
| 5.5 Metrics/observability | PASS | all post-payout, no feedback loops |
| 5.6 Genesis/treasury constants | PASS | configured params + protocol constants |
| 5.7 Dispute-path inputs | PASS | strict subset of accept/reject; no unique inputs |
| 5.8 shadowMode | M-priority | manifest classification clarification (per §4.5) |
| 5.9 Randomness | PASS | zero references in entire surface |
| 5.10 Config/env reads | PASS | zero references |
| 5.11 Map iteration | PASS | all sorted (escrow.go:516, :543) or commutative-safe (`// safe:` comments) |

**Three medium-priority manifest notes**:
- **M1 (shadowMode)**: classify as `non-canonical-artifact`, forbidden in derivation per Plan §0.6. Done in manifest.
- **M2 (`verification_consensus_settler.go:516`)**: add `// safe: result map iteration order irrelevant; recipients re-sorted at escrow ReleaseSettlement before transfer execution` comment. Recommended in 5A.4 polish.
- **M3 (treasuryID)**: classified `canonical-frozen` absent setter (architect confirm §9.1).

## 6. Halt-trigger assessment

Plan v3 §9 triggers evaluated against integrated findings:

| Trigger | Fired? | Rationale |
|---|:-:|---|
| Live-read that cannot be canonicalized | NO | All identified live-reads have realizable canonicalization targets (Q via 5A.2, DAG reader via 5A.3, task.Status via §9.2 architect decision, bucketCounter via 5A.4) |
| Input-Domain-4 unsatisfiable | NO | All canonical-live targets have defined retrieval modes (canonical-projection or anchor-scoped reader) |
| shadowMode cannot be forbidden | NO | Confirmed migration flag; deterministic per canonical activation; float-path excision is clean |
| Bug class orthogonal to F5 | NO | Two new findings (Agent D escrow-persistence-divergence, Agent E bucketCounter-restart-collision) are both subordinate to F5: closed by 5B pure-derivation + 5A.4 content-hash refactor respectively |
| Synthetic-ID refactor test failures | N/A | Refactor not yet executed; consumer audit shows ZERO breaking consumers — refactor is de-risked |

**No halt triggered. Audit complete; ready for Gate 5A.1 review.**

## 7. Cutover preparation matrix (Plan v3 §7)

For 5C version-gate planning. One row per input with mixed-version risk classification.

| Input | Legacy source | Derived source | Pre-gate? | Post-gate? | Historical replay | Mixed-version risk |
|---|---|---|:-:|:-:|---|:-:|
| round_votes | DAG events | same | Y | Y | replays from DAG | none |
| round_category | round struct | same | Y | Y | trivial | none |
| payload.TaskID/WorkerID/PosterID/SubmissionEventID | event payload | same | Y | Y | trivial | none |
| payload.FinalVerdict | advisory payload | Outcome.Verdict (canonical) | Y | Y | DeriveOutcome deterministic | none (post-redirect) |
| payload.FinalScoreBP | advisory payload | Outcome.ScoreBP | Y | Y | DeriveOutcome deterministic | none (post-redirect) |
| task.Status | taskMgr.Get (live projection) | anchor-scoped projection (TBD §9.2) | Y | Y (TBD) | replays from canonical events | low (idempotency limits damage) |
| task.PosterID/Budget | taskMgr.Get | same-via-anchor-scoped-projection | Y | Y | replays from canonical events | none (frozen post-task-creation) |
| escrow_entry.Amount | escrowMgr.Get | same-via-anchor-scoped-projection | Y | Y | replays from canonical Transfer | low |
| validator_q_score | tvReputationStore.ValidatorQScore (BadgerDB) | CanonicalQProjection.Lookup(validator, category, anchor) | Y (legacy) | Y (canonical) | canonical-projection replays from canonical evidence | **medium** — mixed cluster must agree on projection version |
| qualityFn (gen-ledger) | neutral stub | CanonicalQProjection (when wired) | Y (stub) | Y (when wired) | deterministic-when-wired | none today; medium when wired |
| dag_event_lookup | dag.DAG.Get (live) | DAGAncestorReader.ReadAtAnchor (5A.3 design) | Y | Y | anchor-scoped reader replays from canonical DAG | low (events immutable) |
| ancestor_event.CausalRefs | dag.Get return | anchor-scoped read | Y | Y | deterministic from event | none |
| protocol constants (workerShareBP etc.) | const | const | Y | Y | constant | none |
| treasuryID | construction param | same | Y | Y | config-deterministic | none |
| shadowMode | RWMutex-guarded bool | REMOVED | Y (migration) | N (excised) | N/A post-excision | forces integer-only cluster |
| float_path_result | float arithmetic | REMOVED | Y | N (excised) | non-deterministic — pre-F5 only | none post-excision |
| applicator.applied set | local + persisted | same (idempotency guard) | Y | Y | per-node persistence replays locally | none (node-local) |
| escrow paid-flag fields | local + per-node persisted | same (idempotency guard, not derivation input) | Y | Y | per-node persistence replays locally | none (node-local; pure derivation produces same flags) |
| **synthetic_transfer_id (bucketCounter)** | `bucket:from:to:amount:counter` (process-local) | `BLAKE3(canonical-payout-record)` (5A.4) | Y (legacy) | Y (derived) | IDs differ pre/post gate — REQUIRES gate coordination | **HIGH** — atomic cluster-wide cutover required |

**Gate coordination rule** (Plan v3 §7): inputs with HIGH risk require atomic cluster-wide transition. Only `synthetic_transfer_id` is HIGH-risk in this audit. Plan v3 §0.4 testnet-wipe-at-merge resolves: post-F5 testnet wipes; replay applies post-gate rules from genesis. No mixed-version cluster-replay scenario at production cutover.

`validator_q_score` is MEDIUM-risk because the canonical Q projection version is itself a versioned derived store — projection-version mismatches between nodes are detectable pre-cutover via the canonical evidence stream. 5C must define the projection-version-binding rules.

## 8. Outstanding walks — none. All five walks complete.

- ✅ Agent A — Settler core
- ✅ Agent B — Generation ledger + Q-score resolution
- ✅ Agent C — Hidden-input sweep
- ✅ Agent D — Escrow ReleaseSettlement
- ✅ Agent E — Ledger TransferFromBucketLabeled + bucketCounter + repo-wide consumer survey

## 9. Architect decisions on open questions (Gate 5A.1)

Gate 5A.1 architect-session review accepted findings; decisions on the seven open questions:

1. **§9.1 treasuryID classification**: ✅ `canonical-frozen` confirmed. No setter exists; classification will be revisited only if a setter is added.
2. **§9.2 task.Status target retrieval mode**: ✅ Option (b) accepted — current idempotency pattern is sufficient. Rationale: the read is for early-exit optimization; under pure derivation in 5B the end-state is deterministic from canonical inputs regardless of control-flow divergence. **Reopen condition** (documented in manifest): decision reopens if 5B discovers that task.Status influences PAYOUT MATH, not just the short-circuit branch.
3. **§9.3 DAG reader option**: ✅ Option (a) confirmed — 5A.3 designs `DAGAncestorReader.ReadAtAnchor`. Materialization-lag argument for rejecting option (b) is correct.
4. **§9.4 removed inputs in manifest**: ✅ Keep with `target_classification: removed`. 5D regression detection needs them.
5. **§9.5 escrow persistence divergence**: ✅ Confirmed — stays non-canonical. Defense-in-depth provided by derivation purity; persistence layer is crash-recovery scaffolding only.
6. **§9.6 5A.4 consumer audit scope**: ✅ Include ALL 16 callsites of `TransferFromBucketLabeled` in the test surface. Out-of-scope-but-adjacent failures (fees, staking) get fixed as inherited cleanup within F5 scope; not a scope expansion.
7. **§9.7 M2 polish comment**: ✅ Accepted as in-scope 5A.4 polish (`// safe: result map iteration order irrelevant; recipients re-sorted at escrow ReleaseSettlement before transfer execution` at `verification_consensus_settler.go:516`).

All decisions written into the manifest where they affect specific input rows (task_status, synthetic_transfer_id, statistics block).

## 10. Gate 5A.1 closed; Phase 5A.2 in progress

Multi-AI review (Grok + ChatGPT) complete. Both validated audit substance. Five closure changes absorbed (semantic_role column, task_status proof_obligation, validator_q_score HIGH-risk upgrade with version-binding requirement, Finding 6 disposition rewrite with 5B architectural obligation, semantic_role classifications across all in-scope rows).

Phase 5A.2 begins with the **expanded 12-item deliverable list** for `docs/architecture/q-score-canonicalization-design.md`:

1. Historical-read semantics at cutoff anchor (Finding 1).
2. Storage/replay cost bounds (worst-case rebuild + query complexity).
3. Projection cutoff semantics aligned with Input-Domain-2/3.
4. Coupling analysis versus existing advisory reputation projection.
5. Integration interface for Reputation Step 4.
6. Evidence domain definition (which canonical events update Q; ordering; exclusion rules).
7. Version-binding rule (projection schema/version part of gate semantics).
8. Bootstrap/recovery behavior (restart, snapshot restore, genesis-replay).
9. Absence-of-data policy (neutral/default Q; new validators; missing categories; ties).
10. Interaction with generation-ledger quality (same projection, sibling, or derived view).
11. Cutoff anchor precision (DAG anchor at IsComplete-true moment, not arbitrary same-round anchor).
12. Scaling target (p99 lookup < 100µs at 10k QPS with hot-validator cache).

Two forward notes carried out of 5A.1 scope (in `docs/plans/implementation/f5-phase-5a-plan-v3.md`):

- **Forward note for 5C**: production-cutover strategy for `synthetic_transfer_id` — one-time migration vs dual-read with legacy fallback. Resolved in 5C.
- **Forward note for 5D**: explicit Q-C performance gate in verification matrix — `p99 lookup < 100µs at 10k QPS sustained`. Aligned with 5A.2 deliverable item 12.

The two 5A.1 deliverables remain authoritative:
- `docs/audits/2026-04-23-settlement-input-audit.md` (this file)
- `docs/architecture/settlement-input-manifest.yaml`

Plan v3 on disk: `docs/plans/implementation/f5-phase-5a-plan-v3.md`.

---

**End of Settlement Input Audit. Gate 5A.1 closed 2026-04-23. Phase 5A.2 in progress.**
