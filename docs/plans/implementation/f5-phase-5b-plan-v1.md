# F5 Phase 5B Plan v1

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5B — Pure Derivation Function + Applicator Refactor
**Version**: v1 — initial draft consuming §5 of the F5 Phase 5A completion gate report as starting brief.
**Status**: Draft. Pre-architect-review. Pre-multi-AI-review. Pre-CC-plan-mode-review. Pre-founder-approval.
**Branch base**: `51bce89` (F4-frozen) + uncommitted 5A.4 working tree.
**Date**: 2026-04-24

**Starting brief**: `docs/plans/implementation/f5-phase-5a-completion-gate-report.md` §5 (Forward notes for 5B plan v1 drafting).

**Prior art**:
- F5 5A Phase 5A plan v3: `docs/plans/implementation/f5-phase-5a-plan-v3.md`
- F5 5A completion gate report: `docs/plans/implementation/f5-phase-5a-completion-gate-report.md`
- F5 5A deliverables (8 artifacts listed in completion gate §2)
- Cross-node settlement emission characterization: `docs/plans/implementation/cross-node-settlement-emission-characterization.md` (F4C halt root cause)
- Locked Reputation-and-Consensus-Integrity workstream: `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` (supplies real W post-`ReputationActivation`)

---

## 0. Decisions locked before implementation

Informed by pre-design verification + 5A consolidated architect sign-offs.

1. **5B's primary deliverable is a pure derivation function** that replaces the imperative `escrow.ReleaseSettlement` flow. Canonical-state in → `[]PayoutRecord` out. Ledger application becomes a separate, record-driven step keyed on `canonical_id` (the uniqueness-invariant-bearing field per schema U-1).

2. **All 5A primitives are load-bearing inputs to 5B, not re-design candidates**. The plan consumes; does NOT revisit. Specifically:
   - `CanonicalWProjection` interface (5A.2 §0.8.1)
   - `NeutralBPStubW` implementation (5A.2 §0.8.6)
   - `ReadAtAnchor` algorithm (5A.3 §2.2)
   - `NeutralQualityStub` implementation (5A.3 §6.4)
   - PayoutRecord schema (5A.4.a — `docs/architecture/payout-artifact-schema.yaml`)
   - V-1 canonical-ancestor check (5A.2 §7.2)
   - Materialization-lag deferral semantic (5A.2 §7.2 + 5A.3 §2.3)
   - 4-step ordinal-assignment rule (schema §`purpose.ordinal`)
   - SHA-256 + RFC 8785 JCS canonical-id hashing
   - Consolidated `dag.AnchorReader` interface (5A.3 §2.1 consolidation path α)

3. **V-1 preservation is non-negotiable**. The derivation function's selection of stub-W vs real-W, stub-quality vs real-quality, and any future activation-gated implementation swap MUST be canonical-position-bound. Runtime-flag-bound selection is forbidden (the 5A.2 structural defense carries forward into 5B implementation).

4. **5A.4.b's `CanonicalSyntheticID` helper is subsumed by 5B**. At each of the 14 external callsites + 1 internal wrapper, the 5A.4.b interim helper invocation is replaced by: (a) 5B's derivation function produces `PayoutRecord` values with full `canonical_id`; (b) callsites pass `record.canonical_id` directly to `TransferFromBucketLabeled`. The interim helper becomes dead code at 5B completion — removable in the same commit that lands 5B's derivation function.

5. **Testnet wipe at F5 merge** per Plan v3 §0.4. 5B verification at testnet does NOT reconcile pre-F5 ledger divergence on the frozen `f4-combined-0e93f48` cluster. Clean slate from genesis post-merge.

6. **5B ships as one combined work product across derivation + applicator**, NOT split into separate sub-phases. Rationale: derivation function produces `[]PayoutRecord`; applicator consumes `[]PayoutRecord`; they are co-designed and co-shipped. Splitting would force an intermediate stub-applicator that provides no verification value.

7. **`ReleaseNet` dispute-path flow is OUT OF SCOPE for 5B** (new decision informed by pre-design verification item 2). `ReleaseNet` at `internal/escrow/escrow.go:390+` is the 3-paid-flag variant used on the pre-F5 dispute-resolution code path. F5's scope per Plan v3 §0.1 is "escrow-settlement-scoped plus all output artifacts emitted by settlement (including the transfer-record projection layer)." ReleaseNet is a separate flow (task cancellation / escrow refund) — adjacent but not settlement. 5B scope locks to `ReleaseSettlement` (the 5-paid-flag race site per 5A.1 Finding 6). A future cleanup workstream may refactor `ReleaseNet` by the same pattern; out of 5B scope to avoid scope creep.

8. **5A.4.c CI lint enforcement flip is 5D responsibility, not 5B**. 5B introduces new reads (the derivation function accesses canonical inputs) which will surface as warnings under the current warnings-now posture. Expanding the manifest to cover the full in-scope surface + flipping the gate to failures is 5D's deliverable per Gate 5A.4.c architect direction. 5B extends the manifest for its own new reads and keeps the lint green-or-warning; 5D does the comprehensive expansion and gate flip.

9. **Pre-5A.4.b test surface is preserved byte-identically under 5B for canonical-input cases**. Per Gate 5A.1 Finding 10 halt discipline: do NOT "just update the test"; investigate whether a failing test encodes a legitimate invariant. Tests asserting imperative mechanics (e.g., "paid-flag set in this order") may be reframed during 5B if and only if the BEHAVIORAL invariant (ledger end-state after settlement) is preserved byte-identically. Any test change is documented as deliberate contract transition in the 5B commit body.

## 1. Load-bearing framing

The bug F5 closes: concurrent-Apply race in `escrow.ReleaseSettlement`. Two TVConsensus events for the same RoundID racing through `settler.Settle` → `escrow.ReleaseSettlement`, partially draining the escrow bucket via the 5-paid-flag check-then-transfer-then-set pattern, erroring mid-payout, silently converging the admission record to StateApplied with permanently-diverged ledger state across nodes.

F5 5A established the architectural invariant and input-domain scaffolding. **F5 5B is where the invariant becomes code.**

The derivation function formalizes:

> *Settlement effects for a given settlement key are a pure function of canonical settlement context. Every correct node computes the same ordered payout multiset and the same terminal settlement summary. No local mutable execution state influences the result. Recomputation after crash, replay, re-admit, or duplicate observation yields byte-identical outputs.*

(F5 5A.2 §0 decision 3; restated here as the 5B implementation target.)

The applicator refactor closes the race at the source. Under record-driven application keyed on `canonical_id`:
- Two concurrent callers computing the same derivation produce records with the same `canonical_id` values (determinism).
- The applicator uses `canonical_id` as an idempotency key: second-and-subsequent writes for the same record are no-ops.
- The 5-paid-flag check-then-transfer-then-set pattern is eliminated. No race window exists.

## 2. Core derivation function specification

### 2.1 Signature

```go
// DeriveSettlement is the pure derivation function: given a finalized
// verification round, produces the ordered PayoutRecord sequence that
// fully settles the round. Pure function of canonical state at the
// cutoff anchor.
//
// Determinism: every correct node computes byte-identical output for
// the same round at the same canonical state. No local state, no
// wall-clock, no map-iteration-order dependence.
//
// Caller passes a finalized TaskVerificationRound. Function does NOT
// mutate any state; returns records + optional deferral signal.
//
// Package: internal/settlement/derivation (new, co-located with existing
// settlement package). Alternative: internal/settlement (existing
// package) — architect decision at v2 review.
func DeriveSettlement(
    ctx context.Context,
    round *taskverification.TaskVerificationRound,
    inputs DerivationInputs,
) (DerivationResult, error)

// DerivationInputs bundles the canonical primitives 5B's derivation
// function reads. Wired once at settler construction.
type DerivationInputs struct {
    W          CanonicalWProjection   // 5A.2 interface; stub or real per V-1
    Quality    CanonicalQualityStub   // 5A.3 stub (NeutralQualityStub today)
    DAGReader  dag.AnchorReader       // 5A.3 consolidated reader (Tips/IsAncestor/Get)
    EscrowMgr  EscrowLookup           // escrow entry + amount lookup
    TaskMgr    TaskLookup             // task metadata lookup (PosterID, Budget, Status)
    ActivationCheck func(event.EventID) (bool, error) // V-1 canonical-ancestor check against current R
    // ... other primitives TBD; keep minimal
}

// DerivationResult is the output of a successful (non-deferred)
// derivation. Records are ordered per the schema ordinal-assignment
// rule; caller passes them to the applicator in slice order.
type DerivationResult struct {
    Records          []PayoutRecord
    TerminalStatus   TerminalStatus  // the task status transition the round resolves to
    Deferred         bool            // true iff materialization-lag deferral signaled
    DeferReason      string          // diagnostic only, populated iff Deferred
}
```

### 2.2 Semantics

Preconditions (caller responsibility):
- `round` is a finalized TaskVerificationRound (round.State ∈ {FinalizedAccept, FinalizedReject, FinalizedDispute}).
- `round.Votes` is the canonical vote set (cluster-uniform per F4).
- `round.FundingReference` identifies the canonical Transfer event that funded escrow (via `EscrowMgr.FundingRef(round.TaskID)`).

Derivation steps (in strict order, all pure):

1. **Compute canonical cutoff anchor** `cutoff = cutoff_anchor_for(round)` per F5 5A.2 §11.5 (epoch-coarse: end-of-immediately-prior-epoch snapshot).

2. **Compute R.canonical_seal_context** = round's TVConsensus event ID (canonical-position of round's finalization).

3. **V-1 check for each activation-gated primitive**:
   - `useRealW := ActivationCheck(ReputationActivation_event_id)` — determines stub-W vs real-W per round's canonical position relative to activation event. If returns `ErrEventNotFound`: set `Deferred=true`, `DeferReason="V-1 ancestor check: materialization-lag"`, return.
   - (Future) `useRealQuality := ActivationCheck(QualityActivation_event_id)` — analogous pattern; not active until quality-activation workstream ships.

4. **Select implementations**:
   - `wImpl := useRealW ? W.Real : W.Stub` (both satisfy CanonicalWProjection interface)
   - `qualityImpl := NeutralQualityStub` (today; future: V-1-selected similarly)

5. **Route on round.Verdict**:
   - `FinalizedAccept` → `deriveAccept(round, wImpl, qualityImpl, inputs)`
   - `FinalizedReject` → `deriveReject(round, wImpl, inputs)` (no gen-ledger on reject)
   - `FinalizedDispute` → `deriveDispute(round, inputs)` (no validator payouts, no gen-ledger)

6. **deriveAccept** (the main path; deriveReject and deriveDispute are structural variants):
   - Compute pool shares from canonical constants: `workerAmount = budget * workerShareBP / 10000`, `validatorPool = ...`, etc.
   - Collect agreeing validators: `agreeing := collectAgreeingValidators(round, VerdictPass)` — deterministic sort by ValidatorID per F4.
   - Compute per-validator W: for each `v` in agreeing, `w_v := wImpl.Lookup(v, family, round.Category, round.PosterID, round.EscrowBudget, cutoff_epoch_for(round))`. If any `Lookup` returns `ErrEventNotFound`: set `Deferred=true`, return.
   - Compute per-validator payout via `protocolmath.AllocateWithCeiling(recipients, validatorPool)` per F5 5A.1 integer-path invariants.
   - Compute generation-ledger ancestors: `ancestors := ReadAtAnchor(dagReader, cutoff_anchor, round.SubmissionEventID, MaxDepth=3)`. If returns `ErrEventNotFound`: `Deferred=true`, return.
   - Compute per-ancestor weight `q_a / depth²` per F5 5A.3 §3.1 using `qualityImpl.Lookup(ancestor_id, cutoff_epoch)`.
   - Compute per-ancestor payout via `protocolmath.Allocate(ancestors, genPool)`.
   - Treasury absorbs all rounding remainders per 5A.1 integer-path conservation.

7. **Construct PayoutRecord values** per schema §`PayoutRecord`:
   - `settlement_key.round_id = round.RoundID`
   - `settlement_key.task_id = round.TaskID`
   - `settlement_key.funding_reference = round.FundingReference`
   - `recipient.id, recipient.role` per each payout's destination
   - `amount.value` from step 6 arithmetic, `amount.currency = "AET"`
   - `purpose.tag` per locked vocabulary (worker_payout, poster_refund, validator_distribution, gen_ledger_royalty, treasury_remainder)
   - `purpose.ordinal` per schema 4-step ordinal-assignment rule (tag-group order + lex-sort within group + monotone global counter)
   - `provenance.round_verdict = round.Verdict`
   - `provenance.canonical_cutoff_anchor = cutoff` (or nil if ReputationActivation is NOT a canonical ancestor per Fix A)
   - `derivation_version = DerivationVersion` (const 1 at F5 ship)
   - `canonical_id = SHA256(JCS(record excluding canonical_id))`

8. **Return `DerivationResult{Records: records, TerminalStatus: TerminalAccept}`** (or the verdict-appropriate terminal status).

### 2.3 Purity enforcement

The function body must satisfy:
- **No `time.Now()`**: CI lint + code review catch.
- **No `math/rand` or `crypto/rand`**: CI lint + code review catch.
- **No mutation of inputs**: all map/slice arguments treated as read-only; outputs are freshly-constructed records.
- **No package-level mutable state read**: forbidden by the 5A.4.c lint (warnings today; fails at 5D).
- **Map iteration must sort or be commutative**: per 5A.1 §5.11 + existing codebase convention with `// safe:` annotations.

Plan v1 does NOT yet choose where to place the derivation function (new package `internal/settlement/derivation` vs existing `internal/settlement`). Architect decision at Plan v1 review.

### 2.4 Deferral behavior

When `Deferred=true` is returned:
- Caller (settler) does NOT apply any records.
- Caller re-enqueues the round for retry (F3-B causal-prerequisite-gating pattern per 5A.2 §7.2 + 5A.3 §2.3).
- Retry is driven by the recognition fabric; when canonical state advances (materialization catches up), the next `DeriveSettlement(round)` call produces a non-deferred result.

Deferral is NOT an error in the economic sense — it's a "not yet" signal. Error returns (distinct from `Deferred=true`) indicate implementation bugs or canonical-state corruption.

### 2.5 Output determinism property

**Property D-1**: for any finalized round R, any two nodes computing `DeriveSettlement(R, inputs)` with identical canonical state produce byte-identical `DerivationResult` values. The record slice is ordered identically; each record's every field is byte-identical; each `canonical_id` hash is byte-identical.

D-1 is the load-bearing guarantee 5D verification asserts.

**Property D-2**: the deferral signal is canonical-state-bound, not runtime-state-bound. Two nodes at different local materialization states may differ in whether they defer on a given round, but if they both proceed (not defer), they produce byte-identical records. Deferral is a local-state observation; the records, when produced, are canonical.

## 3. Applicator refactor specification

### 3.1 Current state (5A.4.b HEAD)

`internal/escrow/escrow.go:ReleaseSettlement` at `:476+` implements the 5-paid-flag check-then-transfer-then-set pattern. Post-5A.4.b refactor (5A.1 §4.6 updated addresses):

| Site | Check line | Set line |
|---|---:|---:|
| Worker | :508 | :518 |
| Poster refund | :524 | :534 |
| Per-Validator (loop) | :559 | :573 |
| Per-GenRecipient (loop) | :590 | :604 |
| Treasury | :610 | :620 |

This is the concurrent-Apply race site.

### 3.2 Target state (post-5B)

Replace `ReleaseSettlement` with a record-driven function:

```go
// ApplySettlementRecords applies a derivation-function-produced slice
// of PayoutRecords atomically-with-respect-to-canonical_id. Records
// already applied on this node (per canonical_id) are skipped; new
// records are transferred via TransferFromBucketLabeled with the
// record's canonical_id as the EventID.
//
// Idempotency: keyed on canonical_id. Multiple concurrent callers
// producing the same derivation result apply the same records; the
// second call is a no-op for records already applied.
//
// Replaces the 5-paid-flag check-then-transfer-then-set pattern in
// the pre-5B ReleaseSettlement. The paid-flag fields (WorkerPaid,
// ValidatorsPaid map, etc.) remain as CRASH-RECOVERY SCAFFOLDING per
// 5A.1 §4.6 Finding 6 5B obligation (a/b/c):
//   (a) derivation produces the canonical payout record FIRST,
//   (b) flags are written as pure projection of the derived records,
//   (c) flags are NEVER read to determine canonical semantic behavior.
func (e *Escrow) ApplySettlementRecords(
    taskID string,
    records []PayoutRecord,
) error
```

### 3.3 Per-record application step

For each record in order (records already sorted per ordinal-assignment rule from 5B derivation):

1. Check `e.applied[record.canonical_id]` (or equivalent BadgerDB key). If set: skip — already applied on this node.
2. Acquire per-`canonical_id` idempotency lock (sync.Map or equivalent).
3. Re-check under lock; skip if set.
4. Invoke `ledger.TransferFromBucketLabeled(record.canonical_id, record.recipient.role-to-bucket, record.recipient.id, record.amount.value, record.purpose.tag-to-memo, false)`.
5. Set `applied[record.canonical_id] = true` on success, persist to BadgerDB.
6. Release the per-`canonical_id` lock.

**The idempotency-via-canonical_id pattern replaces the check-then-transfer-then-set race**. Two concurrent callers with identical canonical_id sequences race ONLY on the check-set-under-lock primitive, which is atomic. No transfer happens twice; no paid-flag is set before the transfer completes.

### 3.4 Paid-flag preservation as crash-recovery scaffolding

Per 5A.1 §4.6 Finding 6 5B obligation: paid-flag fields (WorkerPaid, ValidatorsPaid map, TreasuryPaid, GenLedgerPaid map, PosterRefundPaid) remain in `EscrowEntry`. 5B wires them as PURE PROJECTION of applied records:
- When a record with `role=Worker` is applied, `WorkerPaid = true` (plus persist).
- When a record with `role=Validator, recipient.id=v` is applied, `ValidatorsPaid[string(v)] = true` (plus persist).
- Etc.

This preserves crash-recovery semantic: if a node crashes mid-settlement, on restart the persisted flags indicate which records have been applied; retry skips them. The flags are WRITTEN BY the derivation-driven applicator, not READ BY it to determine canonical behavior — satisfies obligation (b) and (c).

HasSettlementStarted continues to work (reads the flags). Its role shifts from "is settlement in progress?" to "have any records been applied for this task?" — semantically the same, crash-recovery purpose preserved.

### 3.5 ReleaseSettlement signature change

The public `ReleaseSettlement` signature (currently accepts budget + agent IDs + amount maps) changes to accept `(taskID, records)`:

```go
// Before (pre-5B):
func (e *Escrow) ReleaseSettlement(
    taskID string,
    worker crypto.AgentID, workerAmount uint64,
    posterRefund crypto.AgentID, posterRefundAmount uint64,
    validators map[crypto.AgentID]uint64,
    genRecipients map[crypto.AgentID]uint64,
    treasury crypto.AgentID, treasuryAmount uint64,
) error

// After (post-5B):
func (e *Escrow) ApplySettlementRecords(
    taskID string,
    records []PayoutRecord,
) error
```

Caller (settler) changes from:
```go
// Before:
e.escrowMgr.ReleaseSettlement(taskID, worker, workerAmount, poster, posterRefundAmount, validators, genRecipients, treasury, treasuryAmount)

// After:
result, err := deriveSettlement(ctx, round, inputs)
if err != nil { return err }
if result.Deferred { deferRound(round); return nil }
return e.escrowMgr.ApplySettlementRecords(taskID, result.Records)
```

Substantial signature change; 1 callsite in `settler.settleAccept/Reject/Dispute` (3 branches but same settler). No other callers of `ReleaseSettlement` exist (it's settler-only per 5A.1 audit).

## 4. Discovery-tax items — handling if they materialize during 5B implementation

Per F5 Phase 5A completion gate §5.4:

### 4.1 Materialization-lag in genesis replay

**If surfaces**: 5B's `Deferred=true` path is the handler. Settler re-enqueues via F3-B causal-prerequisite-gating; retry when DAG state advances. Should NOT surface as a halt unless: (a) a round cannot be materialized from genesis deterministically (canonical-state corruption), OR (b) the deferral loop never converges (no materialization progress). Either is a canonical-state bug, not a 5B design failure.

**Plan v1 position**: 5B implementation includes a "genesis replay from fresh testnet" verification step that exercises the deferral path. If the retry loop fails to converge within a configurable timeout (e.g., 10 retries over 30 seconds), halt with a diagnostic — but this is a canonical-state failure signal, not a 5B-specific concern.

### 4.2 First-round-of-epoch boundary race interaction

**If surfaces**: Per F5 5A.3 §7.2 — same-epoch-ancestor exclusion is the deliberate consequence of epoch-coarse cutoff alignment with W. Architect signed off on this at Gate 5A.3. 5B implementation preserves the behavior; 5B verification observes it; any economic-policy concern is downstream of 5B per Gate 5A.3 gate-report note 4.3.

**Plan v1 position**: 5B ships the behavior as-designed. If testnet observation reveals the exclusion is economically surprising at scale (not in F5 scope to prove), surface as a post-5B economic-analysis workstream per gate-report note 4.3.

### 4.3 Equivocation inertness preservation

**If surfaces**: Per F5 5A.3 §6.4 + Gate 5A.1 §13.4 verification audit — equivocation is canonically inert today; `RecordEquivocation` has zero production callers; `EventTypeSlashingChallenge` is defined but never instantiated; locked workstream §17 step 7 owns canonical anchoring. **5B MUST NOT wire equivocation into the derivation path.** Equivocation evidence is out of F5 scope.

**Plan v1 position**: 5B's `DeriveSettlement` does NOT read equivocation state, does NOT write equivocation evidence, does NOT emit slashing-challenge events. If 5B implementation reveals coupling (e.g., settler's current code path touches equivocation in a way that derivation preservation would propagate), surface as scope-boundary issue — likely requires locked workstream coordination.

### 4.4 task.Status reopen condition

**If surfaces**: Per Gate 5A.1 §9.2 — option (b) accepted (idempotency-bounded harmlessness); reopen condition: if 5B discovers task.Status influences PAYOUT MATH (not just short-circuit), task.Status reverts to canonicalization candidate (option a — anchor-scoped task-status projection).

**Plan v1 position**: 5B implementation surveys every task.Status read in the derivation path. If any task.Status read influences payout amount, recipient set, or ordinal ordering (NOT just the early-exit short-circuit): HALT and surface to architect. Task.Status canonicalization becomes 5B-blocking scope expansion, potentially rolling up to 5A.2 reopen.

### 4.5 Contributor propagation test

**If surfaces**: Per 5A.2 §0.8.3 — `contributor` parameter's semantic role in W is ambiguous (not visible in locked plan §4.1 W computation). 5D stub-to-real swap verification test explicitly verifies contributor propagation per 5A.2 §0.8.5.

**Plan v1 position**: 5B's `DeriveSettlement` passes `round.PosterID` as the `contributor` argument to every `CanonicalWProjection.Lookup` call. The propagation-correctness test lives in 5D (or in 5B's own test suite as an advance-guard). 5B does NOT need to resolve the contributor-semantic ambiguity — it only needs to forward the value consistently.

## 5. Halt-and-surface triggers for 5B implementation

- **task.Status reopen trigger fires**: 5B discovers task.Status influences payout math (per §4.4). Halt; surface to architect; task-status canonicalization enters 5B scope or 5A.2 reopens.
- **Equivocation coupling surfaced**: 5B implementation reveals existing code paths touching equivocation that derivation-preservation would propagate (per §4.3). Halt; surface as scope-boundary; likely requires locked workstream coordination.
- **V-1 ancestor check cannot be implemented as pure canonical-state function**: 5A.2 §7.2 specified the check uses `IsAncestor(activation, R.canonical_seal_context)`. If 5B implementation reveals this check requires non-canonical-state data (e.g., runtime flag, local projection), halt; surface as 5A.2 reopen candidate.
- **Existing test fails on 5B refactor with legitimate-invariant encoding**: per Gate 5A.1 Finding 10 discipline. If a test fails, investigate whether the test encoded a legitimate behavioral invariant before mutating it. Only reframe the test (not the code) if the failure is asserting imperative mechanics that the pure-derivation refactor legitimately supersedes. Otherwise: halt; test is right, code is wrong.
- **Cross-node byte-equality test fails**: 5B verification includes a 3-node-harness test asserting two nodes produce byte-identical `DerivationResult` for the same canonical input (property D-1). Any failure halts — indicates derivation is NOT pure.
- **Deferral loop fails to converge**: per §4.1. Indicates canonical-state bug or deferral-mechanism bug. Halt; surface.
- **Applicator race re-appears under concurrent-invocation test**: 5B verification includes a test that concurrently invokes `ApplySettlementRecords` from two goroutines with the same record set. Second caller must find all records already applied (no-op). Any divergence halts.
- **Derivation function impurity detected**: code review or CI lint catches mutation of inputs, wall-clock read, randomness, unsorted map iteration. Halt; purity is load-bearing.

## 6. Deliverables summary

### 6.1 Code

1. **Derivation function** `DeriveSettlement` — new file(s) in `internal/settlement/derivation/` (or `internal/settlement/` — architect decision at v2 review).
2. **Applicator refactor** — `internal/escrow/escrow.go` `ReleaseSettlement` → `ApplySettlementRecords` signature + body changes.
3. **Settler caller update** — `internal/settlement/verification_consensus_settler.go` `settleAccept/Reject/Dispute` → call `DeriveSettlement` + `ApplySettlementRecords`.
4. **`CanonicalSyntheticID` helper removal** — dead code at 5B completion; the 14 external callsites + 1 internal wrapper from 5A.4.b no longer invoke it because the derivation function produces records with canonical_id directly.
5. **Manifest expansion** — 5B's new derivation-function reads must be added to `docs/architecture/settlement-input-manifest.yaml` `inputs` list to keep the 5A.4.c lint at "warnings only" (or green on undeclared-reads). 5D handles the comprehensive expansion + failure-gate flip.
6. **Float-path removal cleanup** — per F5 plan v3 §3.5, the float path in `computeValidatorPayouts` + generation-ledger calculator becomes dead code when derivation is pure. 5B or a companion cleanup PR removes the float path entirely (shadowMode forbidden, integer-only arithmetic).

### 6.2 Tests

1. **Derivation function unit tests** — exhaustive coverage of round.Verdict branches, agreeing-validator edge cases, gen-ledger depth boundaries, cutoff-anchor edge cases.
2. **Cross-node byte-equality test** — property-based: random canonical inputs → derivation on 3 in-process nodes → assert byte-identical records.
3. **Concurrent-applicator test** — two goroutines invoke `ApplySettlementRecords` with same records; assert idempotency and zero double-application.
4. **Deferral-path test** — synthetic canonical state where `IsAncestor` returns `ErrEventNotFound`; assert Deferred=true, no records returned, no ledger mutation.
5. **V-1 selection test** — stub-W vs fake-real-W swap via canonical-ancestor check; assert selection per round's canonical position (reuses 5A.2 §0.8.5 test framework).
6. **Test surface preservation** — all 6 existing test files' behavioral assertions preserved (per pre-design verification item 3). Any reframed test is documented with rationale.

### 6.3 Documentation

1. **5B completion gate report** (analogous to 5A's) covering design decisions, multi-AI findings, halt-triggers evaluated, 5C readiness.
2. **Journal entry** marking 5B closure.

## 7. Success criteria

5B is complete when:

1. ✅ `go test ./...` passes across all packages with zero failures.
2. ✅ `go test -race ./...` passes (concurrent-applicator determinism under race detector).
3. ✅ Cross-node byte-equality test passes (3-node harness; property-based).
4. ✅ Deferral-path test passes (materialization-lag deferral works end-to-end).
5. ✅ V-1 selection test passes (canonical-position-bound stub-vs-real-W selection).
6. ✅ Pre-existing test surface preserved byte-identically for canonical-input cases; any reframed test documented.
7. ✅ 5A.4.c CI lint green (warnings allowed; no failures).
8. ✅ Float-path dead code removed (or flagged for companion cleanup PR).
9. ✅ `CanonicalSyntheticID` helper dead code removed.
10. ✅ Settler's `Settle` method calls `DeriveSettlement` + `ApplySettlementRecords` instead of imperative mechanics.
11. ✅ `ApplySettlementRecords` keyed on `canonical_id` idempotency; paid-flag fields remain as crash-recovery projection only (obligation a/b/c preserved).
12. ✅ Fresh testnet deploy (post-F5 merge, wiped state): settlement end-to-end on 5-node AWS cluster produces byte-identical ledger state on every node for every round. Verified via cross-node-invariant monitoring.
13. ✅ 5B completion gate report produced + architect-reviewed + founder-confirmed.

## 8. Sign-off sequence

Mirrors Plan v3's lifecycle:

1. **CC drafts Plan v1** (this document). ✅ Complete.
2. **Architect reviews v1 in full** before any multi-AI send.
3. **Multi-AI review** (Grok + ChatGPT) with architect-supplied prompts.
4. **Revisions → Plan v2** integrating multi-AI feedback.
5. **Claude Code plan-mode review** of v2.
6. **Revisions → Plan v3** integrating plan-mode findings.
7. **Founder approval** of v3.
8. **5B implementation** begins.
9. **5B completion gate** closes on architect review + founder confirmation.

## 9. Scope boundaries — what Plan v1 does NOT decide

- **Derivation function package placement**: `internal/settlement/derivation` (new) vs `internal/settlement` (existing). Architect decision at v1/v2 review.
- **Deferral retry policy**: retry count, backoff, timeout. Tuning decision at v2 review or 5B implementation time (conservative defaults can be refined based on testnet observation).
- **Paid-flag storage encoding**: schema-level decisions about how paid-flag projection is serialized to BadgerDB. Implementation detail for 5B; doesn't need v1 lock.
- **Float-path removal timing**: in the same 5B commit vs companion cleanup PR. Architect preference at v2 review.
- **Applicator error handling under partial-apply crash**: if node crashes mid-`ApplySettlementRecords` after some records applied, paid-flag projection serves the crash-recovery role (retry skips applied records). Confirmed at 5A.1 §4.6 obligation. But policy for handling if a RECORD fails (not a crash, but an error in TransferFromBucketLabeled) — retry the record, skip and continue, or abort? Architect decision at v2 review.
- **Order of 5B commits** (if split across multiple): the plan targets one combined commit but breaking it into staged landing (e.g., derivation function + tests first; applicator refactor second; dead-code removal third) is permissible if it simplifies review. Implementation discretion.

## 10. Risks and dependencies

**Primary risk**: 5B implementation reveals a hidden dependency between derivation purity and existing settler/applicator integration that 5A didn't scope. Mitigation: the halt triggers in §5 include this class (task.Status reopen, equivocation coupling, V-1 non-pureness, test-preservation violation). Halt-and-surface discipline per the Plan v3 precedent.

**Secondary risk**: cross-node byte-equality test reveals D-1 property violation (derivation is not pure). Mitigation: strict purity enforcement in code review + CI lint (5A.4.c scope, warning today, failure at 5D).

**Dependency on 5A primitives**: listed in §0 decision 2. All are on disk; 5B consumes without redesign.

**Dependency on F4**: F4 LogicalKeyConsumer pattern is the surface 5B's settler call chain operates inside. F4 is frozen; F4+F5 merge together at main merge.

**Dependency on testnet infrastructure**: 5-node AWS cluster at `f4-combined-0e93f48`. Testnet wipes at F5 merge; 5B verification deploys fresh.

## 11. Meta-observation — the analogous error hiding in 5B

Per the gate-closure discipline: what is the analogous hidden error Plan v1 could contain?

Candidates:

- **Implicit coupling between derivation purity and applicator state**: if derivation reads anything applicator-side (e.g., "has this been applied?" to decide what to emit), purity is broken. Plan v1 §2.3 forbids this explicitly; 5B implementation must hold it.
- **Ordinal-assignment rule assumed locked at 5A.4.a but not enforced in 5B**: if 5B's derivation function produces ordinals differently from schema §`purpose.ordinal.ordinal_assignment_rule`, records fail U-1 uniqueness. CI lint at 5A.4.c + 5D verification harness are defenses. Plan v1 calls this out at §2.2 step 7.
- **Crash-recovery flag semantics mis-wired**: if paid-flag fields are ever READ to determine canonical behavior (violating obligation c), the 5A.1 Finding 6 5B architectural obligation breaks. Plan v1 §3.4 explicitly forbids.
- **Runtime-flag reintroduction via convenience**: if 5B implementation introduces a `reputationActivated bool` field (or similar) "for performance" to avoid repeating canonical-ancestor checks, V-1 is violated. Plan v1 §2.3 + §0 decision 3 forbids.

These four candidates are the classes of hidden error Plan v1 v2/v3 multi-AI review should pressure-test.

---

**End of F5 Phase 5B Plan v1 — draft complete. Ready for architect review.**
