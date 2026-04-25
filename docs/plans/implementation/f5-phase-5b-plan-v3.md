# F5 Phase 5B Plan v3

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5B — Pure Derivation Function + Applicator Refactor
**Version**: v3 — post Claude Code plan-mode review of Plan v2. 10 findings applied (8 critical + 2 medium) plus 2 low-priority polish items.
**Status**: Draft. Pre-architect-final-read. Pre-founder-approval. Lifecycle: v1 drafted ✅ → architect review v1 ✅ → multi-AI review (Grok + ChatGPT) ✅ → v2 revisions ✅ → CC plan-mode review of v2 ✅ → v3 revisions (this document) ⏳ → architect final read → founder approval → 5B implementation.
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

9. **Pre-5A.4.b test surface is preserved byte-identically under 5B for canonical-input cases**. Per Gate 5A.1 Finding 10 halt discipline: do NOT "just update the test"; investigate whether a failing test encodes a legitimate invariant.

   **Reframing rule (tightened per Gate 5B Plan v1 multi-AI review, ChatGPT Finding 8)**: a test may be rewritten only when (i) the old assertion is shown to encode an implementation MECHANISM rather than a protocol-level BEHAVIOR, AND (ii) the replacement assertion must be at least as strong on protocol behavior. Every rewritten test documents in commit body: old asserted invariant, why mechanism-specific, new asserted invariant, why behaviorally stronger or equivalent. The default is "preserve verbatim"; reframing requires explicit justification for each test.

10. **Float-path removal (companion-PR scope decision)**: float-path removal — `computeValidatorPayoutsFloat`, `shadowMode` branches, float remainder absorption — lands as a **companion PR**, not in 5B proper. 5B's scope stays focused on pure derivation + applicator refactor. Architect-confirmed at Plan v2 review per Grok recommendation from Gate 5B Plan v1 multi-AI review round 2. The companion PR can land alongside 5B (same merge unit) or post-5B; architect decision at v3 review. F5 plan v3 §3.5's "float-path-excision-as-consequence-of-F5" is satisfied either way; 5B does not BLOCK on float-path removal but also doesn't itself perform the excision.

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

### 2.1 DerivationInputs contract (LOAD-BEARING — primary structural finding from Gate 5B Plan v1 multi-AI review)

The `DerivationInputs` struct (defined in §2.2) bundles the canonical primitives the derivation function reads. Every field in `DerivationInputs` MUST satisfy at least one of:

- **(a) Canonical-frozen value** — the field's value is fixed at `DerivationInputs` construction time and does NOT change during the derivation function's execution. Example: locked enum, locked-version interface handle to a stub. The bundle itself is immutable post-construction.

- **(b) Deterministic replayable lookup at cutoff** — the field exposes a query interface whose return values are pure functions of canonical state at the cutoff anchor. Example: `CanonicalWProjection.Lookup(v, family, category, contributor, escrowBudget, epoch)` returns a deterministic value for the same arguments at the same cutoff; `dag.AnchorReader.IsAncestor(a, b)` returns a deterministic value at the same DAG state.

**No field may expose mutable state through an alternative path** — including: unconstrained closures over goroutine-local variables, channels backed by runtime mutable state, contextual values that change per-call, or any "convenience" interface that bypasses the cutoff-bound lookup pattern.

**Rationale**: prevents non-canonical state from sneaking into derivation through the input bundle. The 5B analogue of "runtime flag reintroduction via convenience" from F5 5A.2 §7.2 (V-1 invariant). A future implementer might add a field to `DerivationInputs` that LOOKS like a canonical-projection interface but secretly reads runtime state (e.g., a mutable cache, a cluster-state pointer, a wrapper that falls back to local state on cache miss). The bundle's contract MUST forbid this by design.

**Enforcement**:
- Every `DerivationInputs` field's documentation explicitly names which contract clause (a or b) it satisfies.
- Code review checks new fields against the contract.
- Future 5A.4.c lint expansion (post-5D) can validate the contract structurally (e.g., flag fields whose types don't conform to known canonical-interface patterns).
- 5D verification harness includes a property-based test that constructs `DerivationInputs` with deliberately-non-canonical wrappers and asserts the derivation function detects the violation (or fails the property-test guarantee).

This is the 5B analogue of V-1's "no runtime flag" rule. V-1 forbade a `reputationActivated bool` field anywhere in the derivation package. The DerivationInputs contract generalizes: forbid any state-leaking path through the input bundle's field set.

### 2.2 Signature

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
// Package: internal/settlement/derivation (new subpackage, co-located
// with the existing internal/settlement/ package). Separation prevents
// accidental coupling with imperative settlement code and enables clean
// scoping for CI lint expansion and future-workstream imports.
// LOCKED at Plan v2 architect review.
func DeriveSettlement(
    ctx context.Context,
    round *taskverification.TaskVerificationRound,
    inputs DerivationInputs,
) (DerivationResult, error)

// DerivationInputs bundles the canonical primitives 5B's derivation
// function reads. Wired once at settler construction.
//
// Every field MUST satisfy the §2.1 DerivationInputs contract:
// (a) canonical-frozen value, OR
// (b) deterministic replayable lookup at cutoff.
// Adding a field that violates the contract is a halt-and-surface
// trigger per §5.
type DerivationInputs struct {
    // (b) — canonical projection lookup; stub or real selected per V-1
    // canonical-ancestor check at derivation invocation time.
    W CanonicalWProjection

    // (b) — canonical quality lookup; stub today (always returns
    // NeutralBP); future-real selected per V-1 when quality-activation
    // workstream ships.
    Quality CanonicalQualityProjection

    // (b) — consolidated DAG anchor reader (Tips/IsAncestor/Get) per
    // F5 5A.3 §2.1 path α consolidation. Reads canonical DAG state.
    DAGReader dag.AnchorReader

    // (b) — escrow entry + amount lookup; reads canonical-frozen
    // entry fields populated at RegisterEscrow time.
    EscrowMgr EscrowLookup

    // (b) — task metadata lookup; reads canonical-frozen task fields
    // (PosterID, Budget) plus the canonical-live task.Status (which
    // is read for early-exit ONLY per Gate 5A.1 §9.2 + reopen
    // condition: if 5B discovers task.Status influences payout math,
    // halt per §5).
    TaskMgr TaskLookup

    // (b) — V-1 canonical-ancestor check function; pure function of
    // canonical DAG state. Returns ErrEventNotFound on materialization
    // lag, triggering deferral per §2.5.
    ActivationCheck func(activationEventID event.EventID, sealContext event.EventID) (bool, error)
}

// DerivationStatus discriminates Derived (records produced) vs
// Deferred (materialization lag, retry later). Closed enum; no other
// values. Replaces the v1 Deferred-bool + DeferReason-string pattern.
type DerivationStatus int

const (
    StatusDerived DerivationStatus = iota
    StatusDeferred
)

// DeferredCause is a typed enum of deferral causes. Allows the
// caller's retry-policy logic to discriminate without parsing
// strings. Add new variants only when a new deferral path is added.
type DeferredCause int

const (
    DeferredCauseV1AncestorCheck DeferredCause = iota   // ActivationCheck returned ErrEventNotFound
    DeferredCauseDAGAncestorBFS                         // ReadAtAnchor returned ErrEventNotFound
    DeferredCauseWLookup                                // CanonicalWProjection.Lookup returned ErrEventNotFound
    DeferredCauseQualityLookup                          // CanonicalQualityProjection.Lookup returned ErrEventNotFound
)

// DerivationResult is the output of DeriveSettlement.
//
// When Status == StatusDerived: Records is populated, TerminalStatus
// is set, Cause is unused, Summary is populated. Caller passes Records
// to the applicator in slice order.
//
// When Status == StatusDeferred: Records is empty, TerminalStatus is
// unused, Cause is populated, Summary may be partially populated for
// debugging. Caller defers the round per §2.5.
type DerivationResult struct {
    Status         DerivationStatus
    Records        []PayoutRecord
    TerminalStatus TerminalStatus

    // ResolvedCutoffAnchor is the canonical_cutoff_anchor that was
    // used during this derivation, returned for caller's audit and
    // for 5D verification. EventID | nil per Fix A semantic
    // (nil iff ReputationActivation is NOT a canonical ancestor).
    // Always set when Status == StatusDerived.
    ResolvedCutoffAnchor event.EventID

    // ResolvedCutoffAnchorIsNil distinguishes the nil case from the
    // empty-string-with-different-semantic case. true iff the cutoff
    // was determined to be the Fix A nil. Always meaningful when
    // Status == StatusDerived.
    ResolvedCutoffAnchorIsNil bool

    // Cause populated only when Status == StatusDeferred.
    Cause DeferredCause

    // Summary is observability metadata for debugging and 5D
    // verification. NOT in the canonical_id hash preimage of any
    // record. Counts and flags only; no per-record data that would
    // create coupling with the canonical schema.
    Summary DerivationSummary
}

// DerivationSummary is observability metadata about a derivation
// invocation. NOT in canonical state; never feeds back into derivation.
// Used for diagnostics and for 5D verification's cross-node sanity
// checks (e.g., "every node selected real-W for round R").
type DerivationSummary struct {
    RecordCountByRole       map[string]uint32  // role name → count
    SelectedWMode           string             // "stub" | "real"
    SelectedQualityMode     string             // "stub" | "real"
    GenLedgerTraversalRan   bool               // true iff verdict == Accept and gen-ledger pool > 0
    GenLedgerAncestorCount  uint32             // 0 if traversal didn't run
    AgreeingValidatorCount  uint32             // 0 on dispute path
}
```

### 2.3 Semantics

Preconditions (caller responsibility):
- `round` is a finalized TaskVerificationRound (round.State ∈ {FinalizedAccept, FinalizedReject, FinalizedDispute}).
- `round.Votes` is the canonical vote set (cluster-uniform per F4).
- `round.FundingReference` identifies the canonical Transfer event that funded escrow (via `EscrowMgr.FundingRef(round.TaskID)`).

Derivation steps (in strict order, all pure):

1. **Compute the canonical cutoff for round R in both forms**:
   - `cutoff_anchor: EventID | nil` — per Fix A (nil iff `ReputationActivation` is NOT a canonical ancestor of `R.canonical_seal_context`; non-nil = locked-workstream snapshot encoding per F5 5A.2 §11.5).
   - `cutoff_epoch: uint64` — epoch index of R's immediately-prior epoch per F5 5A.2 §11 (`epoch_of(R) - 1`).

   Both are pure functions of R's canonical position. The anchor form feeds `provenance.canonical_cutoff_anchor` on PayoutRecord; the epoch form feeds `CanonicalWProjection.Lookup`.

2. **Compute R.canonical_seal_context** = round's TVConsensus event ID (canonical-position of round's finalization).

3. **V-1 check for each activation-gated primitive**:
   - `useRealW := ActivationCheck(ReputationActivation_event_id, R.canonical_seal_context)` — determines stub-W vs real-W per round's canonical position relative to activation event. If returns `ErrEventNotFound`: set `Status=StatusDeferred, Cause=DeferredCauseV1AncestorCheck`, return.
   - (Future) `useRealQuality := ActivationCheck(QualityActivation_event_id, R.canonical_seal_context)` — analogous pattern; not active until quality-activation workstream ships.

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
   - Compute per-validator W: for each `v` in agreeing, `w_v := wImpl.Lookup(v, family, round.Category, round.PosterID, round.EscrowBudget, cutoff_epoch)`. If any `Lookup` returns `ErrEventNotFound`: set `Status=StatusDeferred, Cause=DeferredCauseWLookup`, return.
   - Compute per-validator payout via `protocolmath.AllocateWithCeiling(recipients, validatorPool)` per F5 5A.1 integer-path invariants.
   - Compute generation-ledger ancestors: `ancestors := ReadAtAnchor(dagReader, cutoff_anchor, round.SubmissionEventID, MaxDepth=3)`. If returns `ErrEventNotFound`: set `Status=StatusDeferred, Cause=DeferredCauseDAGAncestorBFS`, return.

   **NOTE (per F5 5A.3 §2.2.1 anchor-in-result semantic)**: `ReadAtAnchor` returns a **seed-inclusive slice** — `round.SubmissionEventID` is included in the returned slice as the BFS root, and the cutoff anchor is included when reachable. Derivation MUST NOT treat the result as strict-ancestors-only. Per F5 5A.3 properties A-1 / A-2 / A-3 the slice has well-defined contents in every reachability case; gen-ledger weight assignment applies depth-squared decay starting from depth=0 (root) per existing convention.
   - Compute per-ancestor weight `q_a / depth²` per F5 5A.3 §3.1 using `qualityImpl.Lookup(ancestor_id, cutoff_epoch)`. If `Lookup` returns `ErrEventNotFound`: set `Status=StatusDeferred, Cause=DeferredCauseQualityLookup`, return.
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
   - `provenance.canonical_cutoff_anchor = cutoff_anchor` (EventID when non-nil; JCS-null when nil per Fix A — see JCS encoding discipline below)
   - `derivation_version = DerivationVersion` (const 1 at F5 ship)
   - `canonical_id = SHA256(JCS(record excluding canonical_id))`

   **JCS encoding discipline**: all optional fields in the canonical_id hash preimage are present with **explicit null when nil**, never omitted. Per schema §`canonical_id` notes + Fix A nil semantic: `canonical_cutoff_anchor: EventID | nil` is encoded as JCS `null` when nil, as the EventID string when non-nil — never absent from the JSON object. Same rule for any future optional field. Field omission would create canonical-id ambiguity (two different absences could hash to the same preimage); explicit-null prevents this.

   **Schema-implements-to discipline**: 5B implements TO the schema; the schema is the canonical contract. Any deviation from the schema's locked shape (ordinal rule, nil semantics, hash preimage, field types, locked enum vocabulary) is a halt-and-surface trigger per §5 schema-reopen — surface to architect for Gate 5A.4.a reopen consideration; do NOT silently extend the schema in 5B.

8. **Construct DerivationSummary** with role counts, selected-W mode, selected-quality mode, gen-ledger-traversal-happened flag, gen-ledger-ancestor-count, agreeing-validator-count. Used for observability + 5D verification cross-node sanity checks. NOT in canonical-id hash preimage.

9. **Return `DerivationResult{Status: StatusDerived, Records: records, TerminalStatus: terminal, ResolvedCutoffAnchor: cutoff_anchor, ResolvedCutoffAnchorIsNil: isNil, Summary: summary}`** (or the verdict-appropriate terminal status).

### 2.4 Purity enforcement

The function body must satisfy:
- **No `time.Now()`**: CI lint + code review catch.
- **No `math/rand` or `crypto/rand`**: CI lint + code review catch.
- **No mutation of inputs**: all map/slice arguments treated as read-only; outputs are freshly-constructed records.
- **No package-level mutable state read**: forbidden by the 5A.4.c lint (warnings today; fails at 5D).
- **Map iteration must sort or be commutative**: per 5A.1 §5.11 + existing codebase convention with `// safe:` annotations.

### 2.5 Deferral behavior

When `Status == StatusDeferred` is returned:
- Caller (settler) does NOT apply any records.
- Caller re-enqueues the round for retry (F3-B causal-prerequisite-gating pattern per 5A.2 §7.2 + 5A.3 §2.3).
- Retry is driven by the recognition fabric; when canonical state advances (materialization catches up), the next `DeriveSettlement(round)` call produces a `StatusDerived` result.

Deferral is NOT an error in the economic sense — it's a "not yet" signal. The typed `Cause` field discriminates which canonical-state lookup hit `ErrEventNotFound`; this informs (e.g.) telemetry without requiring string parsing. Error returns (distinct from `Status == StatusDeferred`) indicate implementation bugs or canonical-state corruption.

### 2.6 Output determinism property

**Property D-1**: for any finalized round R, any two nodes computing `DeriveSettlement(R, inputs)` with identical canonical state and both returning `Status == StatusDerived` produce byte-identical `DerivationResult` values for the canonical fields (Records, TerminalStatus, ResolvedCutoffAnchor, ResolvedCutoffAnchorIsNil). The record slice is ordered identically; each record's every field is byte-identical; each `canonical_id` hash is byte-identical. Summary fields are observability-only and may differ in non-canonical ways (e.g., counters reflecting per-node observation), but the Summary's content is derivable from canonical inputs and SHOULD be identical when both nodes process the same canonical state.

D-1 is the load-bearing guarantee 5D verification asserts.

**Property D-2 (sharpened per Gate 5B Plan v1 multi-AI review, ChatGPT Finding 5)**: Materialization lag may cause **temporary divergence in PROGRESS** (node A defers; node B proceeds), but **NEVER divergence in DERIVED MEANING**. Deferral is canonical-state-bound — a node returns `Status == StatusDeferred` only when it cannot evaluate canonical state (one of the four `DeferredCause` values triggered by `ErrEventNotFound`). Once the missing canonical state is locally available, retry of `DeriveSettlement(R)` converges to the byte-identical `DerivationResult` the proceeding node produced.

Concretely: if node A defers round R at wall-clock t=10s with `Cause=DeferredCauseDAGAncestorBFS`, and node B proceeds at t=10s and returns `Status=StatusDerived` with records {r1, r2, r3}, then at t=15s (when materialization catches up on A) node A's retry of `DeriveSettlement(R)` returns {r1, r2, r3} byte-identically. Cross-node ledger end-state converges; cross-node settlement TIMING differs (t=15s on A vs t=10s on B); but cross-node MEANING (the records produced) is identical.

D-2 is the property that makes deferral safe under the "same end-state from any sequence of admissions" replay guarantee.

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

**Crash atomicity decision (Gate 5B Plan v1 multi-AI review, ChatGPT Finding 6)**: 5B uses **ledger-level idempotency** — `TransferFromBucketLabeled` already provides canonical_id idempotency at the ledger layer post-5A.4.b refactor. Verification: `internal/ledger/transfer.go:531` — `if _, exists := l.entries[eid]; exists { return ErrDuplicateEntry }` under `l.mu.Lock()`. The ledger-side write (in-memory map mutation + BadgerDB `PutTransfer` call) is atomic with the duplicate check via the mutex.

Caller (applicator) treats `ErrDuplicateEntry` as **benign no-op** — record was already applied to the ledger on this node. Crash window analysis:

| Crash position | On restart | Outcome |
|---|---|---|
| Before ledger write | paid-flag projection says "not applied"; retry calls TransferFromBucketLabeled; succeeds | Self-healing |
| After ledger write, before paid-flag projection persist | paid-flag projection says "not applied"; retry calls TransferFromBucketLabeled; ledger returns `ErrDuplicateEntry`; applicator treats as no-op AND updates paid-flag | Self-healing |
| After both | paid-flag projection says "applied"; retry skips entirely | Self-healing |

The ledger's existing duplicate-detection IS the canonical idempotency primitive; atomic-persist coordination at the applicator layer is unnecessary. **Paid-flag projection becomes strictly observability + crash-recovery skip-optimization** (allows skipping the ledger call when we already know it's been applied locally), never load-bearing for correctness.

For each record in order (records already sorted per ordinal-assignment rule from 5B derivation):

1. Check paid-flag projection for the record's role/recipient (e.g., `entry.WorkerPaid` for role=Worker, or `entry.ValidatorsPaid[recipient.id]` for role=Validator). If set: skip — already applied; treat as no-op fast path.
2. Acquire per-`canonical_id` intra-node idempotency lock (sync.Map of mutexes keyed by canonical_id).
3. Re-check paid-flag under lock; if set, release lock and skip (handles concurrent-goroutine race within a single node).
4. Invoke `ledger.TransferFromBucketLabeled(record.canonical_id, source-bucket, record.recipient.id, record.amount.value, memo-from-tag, false)`. Three outcomes:
   - **nil error** — transfer succeeded; record is now in the ledger.
   - **`ErrDuplicateEntry`** — ledger detected duplicate canonical_id; treat as no-op (record was applied on a prior call that crashed before paid-flag persist).
   - **other error** — real failure (e.g., insufficient bucket balance — should not happen under correct derivation; halt-and-investigate).
5. On nil OR `ErrDuplicateEntry`: update paid-flag projection (`entry.WorkerPaid = true`, etc.) and persist via `e.persist(entry)`. On other error: return error without paid-flag update.
6. Release the per-`canonical_id` lock.

**The idempotency-via-canonical_id pattern replaces the check-then-transfer-then-set race**. Two concurrent callers with identical canonical_id sequences race ONLY on the ledger's `l.mu.Lock()` (which is atomic). No transfer happens twice; the second caller's transfer attempt returns `ErrDuplicateEntry` cleanly.

**The per-canonical_id lock in ApplySettlementRecords is intra-node defense-in-depth only** (Grok precision sentence). Cross-node idempotency and ordering are guaranteed by the derivation function producing byte-identical records for identical canonical inputs (property D-1). The lock prevents wasted ledger calls within a single node when two settler goroutines race on the same record set; the LEDGER's canonical_id duplicate detection is what makes the cross-node guarantee load-bearing.

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
if result.Status == StatusDeferred { deferRound(round); return nil }
return e.escrowMgr.ApplySettlementRecords(taskID, result.Records)
```

Substantial signature change; 1 callsite in `settler.settleAccept/Reject/Dispute` (3 branches but same settler). No other callers of `ReleaseSettlement` exist (it's settler-only per 5A.1 audit).

## 4. Discovery-tax items — handling if they materialize during 5B implementation

Per F5 Phase 5A completion gate §5.4:

### 4.1 Materialization-lag in genesis replay

**If surfaces**: 5B's `Status=StatusDeferred` path is the handler. Settler re-enqueues via F3-B causal-prerequisite-gating; retry when DAG state advances. Should NOT surface as a halt unless: (a) a round cannot be materialized from genesis deterministically (canonical-state corruption), OR (b) the deferral loop never converges (no materialization progress). Either is a canonical-state bug, not a 5B design failure.

**Position**: 5B implementation includes a "genesis replay from fresh testnet" verification step that exercises the deferral path. If the retry loop fails to converge within a configurable timeout (e.g., 10 retries over 30 seconds), halt with a diagnostic — but this is a canonical-state failure signal, not a 5B-specific concern.

### 4.2 First-round-of-epoch boundary race interaction

**If surfaces**: Per F5 5A.3 §7.2 — same-epoch-ancestor exclusion is the deliberate consequence of epoch-coarse cutoff alignment with W. Architect signed off on this at Gate 5A.3. 5B implementation preserves the behavior; 5B verification observes it; any economic-policy concern is downstream of 5B per Gate 5A.3 gate-report note 4.3.

**Position**: 5B ships the behavior as-designed. If testnet observation reveals the exclusion is economically surprising at scale (not in F5 scope to prove), surface as a post-5B economic-analysis workstream per gate-report note 4.3.

### 4.3 Equivocation inertness preservation

**If surfaces**: Per F5 5A.3 §6.4 + Gate 5A.1 §13.4 verification audit — equivocation is canonically inert today; `RecordEquivocation` has zero production callers; `EventTypeSlashingChallenge` is defined but never instantiated; locked workstream §17 step 7 owns canonical anchoring. **5B MUST NOT wire equivocation into the derivation path.** Equivocation evidence is out of F5 scope.

**Position**: 5B's `DeriveSettlement` does NOT read equivocation state, does NOT write equivocation evidence, does NOT emit slashing-challenge events. If 5B implementation reveals coupling (e.g., settler's current code path touches equivocation in a way that derivation preservation would propagate), surface as scope-boundary issue — likely requires locked workstream coordination.

### 4.4 task.Status reopen condition

**If surfaces**: Per Gate 5A.1 §9.2 — option (b) accepted (idempotency-bounded harmlessness); reopen condition: if 5B discovers task.Status influences PAYOUT MATH (not just short-circuit), task.Status reverts to canonicalization candidate (option a — anchor-scoped task-status projection).

**Position**: 5B implementation surveys every task.Status read in the derivation path. If any task.Status read influences payout amount, recipient set, or ordinal ordering (NOT just the early-exit short-circuit): HALT and surface to architect. Task.Status canonicalization becomes 5B-blocking scope expansion, potentially rolling up to 5A.2 reopen.

### 4.5 Contributor propagation test

**If surfaces**: Per 5A.2 §0.8.3 — `contributor` parameter's semantic role in W is ambiguous (not visible in locked plan §4.1 W computation). 5D stub-to-real swap verification test explicitly verifies contributor propagation per 5A.2 §0.8.5.

**Position**: 5B's `DeriveSettlement` passes `round.PosterID` as the `contributor` argument to every `CanonicalWProjection.Lookup` call. The propagation-correctness test lives in 5D (or in 5B's own test suite as an advance-guard). 5B does NOT need to resolve the contributor-semantic ambiguity — it only needs to forward the value consistently.

### 4.6 Grok-predicted 5B implementation discovery-tax (forward-looking alertness)

Per Gate 5B Plan v1 multi-AI review, Grok identified four implementation-stage discovery-tax items that are NOT design changes but are forward-looking alertness items for the 5B implementer:

- **(a) V-1 edge case at activation boundary**: at the EXACT canonical position of `ReputationActivation`, the `IsAncestor(activation, R.canonical_seal_context)` check has a boundary semantic. If `R.canonical_seal_context == ReputationActivation_event_id` (the round IS the activation event itself, hypothetically), the check is irreflexive (returns false per existing `dag.IsAncestor` semantic — strict ancestor only, not equal). Plan v1 position: derivation function MUST handle this case as "use STUB W" (consistent with strict-ancestor semantic — activation only takes effect for rounds canonically AFTER it, not AT it). Edge case is unlikely in practice (activation event is a special canonical event, unlikely to be a TVConsensus settling a round) but should be unit-tested explicitly.

- **(b) Paid-flag projection wiring bug**: implementation may forget to update paid-flag for one of the role types (e.g., gen-ledger ancestor records get applied to the ledger but paid-flag projection doesn't get updated for their canonical_id). Symptom: subsequent settler retry calls re-attempt the transfer; ledger correctly returns `ErrDuplicateEntry`; applicator correctly treats as no-op; no economic damage but unnecessary work. Detection: 5B test that asserts `entry.GenLedgerPaid` map is fully populated for all gen-ledger records after a successful `ApplySettlementRecords`.

- **(c) Ordinal ordering subtlety**: schema 4-step ordinal-assignment rule processes tag groups in fixed sequence (worker_payout → poster_refund → validator_distribution → gen_ledger_royalty → treasury_remainder); within each tag group, sort by recipient.id lex order; ordinal sequential from 0 across full sequence. 5B implementer might naively reset ordinal at each tag group (off-by-one against the schema's "monotone across full sequence"). Detection: 5B test asserts ordinal is strictly monotone across the full record slice.

- **(d) task.Status reopen trigger**: per §4.4 — if 5B implementation surveys reveal task.Status influences PAYOUT MATH (not just early-exit short-circuit per Gate 5A.1 §9.2 option-b), halt-and-surface for architect; task.Status canonicalization enters 5B scope or 5A.2 reopens. Most likely surfaces during code review of the derivation function's task.Status reads: any read whose value flows into amount calculation, recipient set, or ordinal ordering triggers the reopen. The Gate 5A.1 §9.2 option-b decision is BOUNDED on the assumption that task.Status only influences early-exit; 5B implementation tests that assumption against the actual code.

These predictions are forward-looking alertness for the 5B implementer; they do NOT change the v2 design but document expected discovery surface during implementation. If any of (a)-(d) requires architectural change rather than implementation polish, the §5 halt triggers apply.

## 5. Halt-and-surface triggers for 5B implementation

- **task.Status reopen trigger fires**: 5B discovers task.Status influences payout math (per §4.4). Halt; surface to architect; task-status canonicalization enters 5B scope or 5A.2 reopens.
- **Equivocation coupling surfaced**: 5B implementation reveals existing code paths touching equivocation that derivation-preservation would propagate (per §4.3). Halt; surface as scope-boundary; likely requires locked workstream coordination.
- **V-1 ancestor check cannot be implemented as pure canonical-state function**: 5A.2 §7.2 specified the check uses `IsAncestor(activation, R.canonical_seal_context)`. If 5B implementation reveals this check requires non-canonical-state data (e.g., runtime flag, local projection), halt; surface as 5A.2 reopen candidate.
- **Existing test fails on 5B refactor with legitimate-invariant encoding**: per Gate 5A.1 Finding 10 discipline. If a test fails, investigate whether the test encoded a legitimate behavioral invariant before mutating it. Only reframe the test (not the code) if the failure is asserting imperative mechanics that the pure-derivation refactor legitimately supersedes. Otherwise: halt; test is right, code is wrong.
- **Cross-node byte-equality test fails**: 5B verification includes a 3-node-harness test asserting two nodes produce byte-identical `DerivationResult` for the same canonical input (property D-1). Any failure halts — indicates derivation is NOT pure.
- **Deferral loop fails to converge**: per §4.1. Indicates canonical-state bug or deferral-mechanism bug. Halt; surface.
- **Applicator race re-appears under concurrent-invocation test**: 5B verification includes a test that concurrently invokes `ApplySettlementRecords` from two goroutines with the same record set. Second caller must find all records already applied (no-op). Any divergence halts.
- **Derivation function impurity detected**: code review or CI lint catches mutation of inputs, wall-clock read, randomness, unsorted map iteration. Halt; purity is load-bearing.

- **Schema reopen** (per Gate 5B Plan v1 multi-AI review, ChatGPT Finding 7): 5B implementation discovers `PayoutRecord` schema is missing a field the derivation function needs to express canonical meaning (e.g., a recipient sub-role discriminator, a provenance field for ancestor-derived weight). Halt; surface to architect for Gate 5A.4.a schema-lock reopen consideration. Do NOT silently extend the schema in 5B (per §2.3 step 7 schema-implements-to discipline).

- **Applied-key growth / storage DoS** (per Gate 5B Plan v1 multi-AI review, ChatGPT Finding 7): long-running node's applied[canonical_id] tracking (whether the paid-flag projection on `EscrowEntry` or any new dedicated store) cannot be bounded without losing correctness. The paid-flag projection persists per-task on `EscrowEntry`, and `EscrowEntry` is deleted when the task completes settlement (per `escrow.go:580` `delete(e.entries, taskID)`). Storage growth is naturally bounded by in-flight task count. But if 5B introduces a separate `applied[canonical_id]` map outside of `EscrowEntry` lifecycle, growth bounds must be analyzed. Halt if growth cannot be bounded; surface for storage-strategy redesign.

- **Partial-apply / batch atomicity** (per Gate 5B Plan v1 multi-AI review, ChatGPT Finding 7): `ApplySettlementRecords` processes records sequentially. If `TransferFromBucketLabeled` fails mid-batch with a non-`ErrDuplicateEntry` error (e.g., insufficient bucket balance — should not happen under correct derivation), some records are applied and some are not. The applicator returns the error; on retry, applied records are skipped via paid-flag projection or `ErrDuplicateEntry`, unapplied records are re-attempted. **Safe prefix-replay holds** as long as: (a) the derivation function produces records in deterministic order (D-1 ensures this), (b) each record is independently applicable (no inter-record dependencies in the ledger). If 5B implementation reveals an inter-record dependency that breaks safe prefix-replay (e.g., a bucket's balance affecting a later record's transfer eligibility within the same batch), halt and surface — this would require batch-atomicity primitives at the ledger layer, which are scope expansion.

## 6. Deliverables summary

### 6.1 Code

1. **Derivation function** `DeriveSettlement` — new file(s) in `internal/settlement/derivation/` (new subpackage, locked at v2 architect review).
2. **Applicator refactor** — `internal/escrow/escrow.go` `ReleaseSettlement` → `ApplySettlementRecords` signature + body changes.
3. **Settler caller update** — `internal/settlement/verification_consensus_settler.go` `settleAccept/Reject/Dispute` → call `DeriveSettlement` + `ApplySettlementRecords`.
4. **`CanonicalSyntheticID` helper removal** — dead code at 5B completion; the 14 external callsites + 1 internal wrapper from 5A.4.b no longer invoke it because the derivation function produces records with canonical_id directly.
5. **Manifest expansion** — 5B's new derivation-function reads must be added to `docs/architecture/settlement-input-manifest.yaml` `inputs` list to keep the 5A.4.c lint at "warnings only" (or green on undeclared-reads). 5D handles the comprehensive expansion + failure-gate flip.
6. **Float-path removal cleanup** — per §0 decision 10 and F5 plan v3 §3.5, float-path removal (`computeValidatorPayoutsFloat`, `shadowMode` branches, float remainder absorption) lands as a **companion PR, NOT in 5B proper**. 5B stays focused on pure derivation + applicator refactor. The companion PR may land alongside 5B (same merge unit) or post-5B per architect decision at v3 review.

### 6.2 Tests

1. **Derivation function unit tests** — exhaustive coverage of round.Verdict branches, agreeing-validator edge cases, gen-ledger depth boundaries, cutoff-anchor edge cases.
2. **Cross-node byte-equality test** — property-based: random canonical inputs → derivation on 3 in-process nodes → assert byte-identical records.
3. **Concurrent-applicator test** — two goroutines invoke `ApplySettlementRecords` with same records; assert idempotency and zero double-application.
4. **Deferral-path test** — synthetic canonical state where `IsAncestor` returns `ErrEventNotFound`; assert Status=StatusDeferred, Records empty, no ledger mutation.
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
8. ✅ `CanonicalSyntheticID` helper dead code removed.
9. ✅ Settler's `Settle` method calls `DeriveSettlement` + `ApplySettlementRecords` instead of imperative mechanics.
10. ✅ `ApplySettlementRecords` keyed on `canonical_id` idempotency; paid-flag fields remain as crash-recovery projection only (obligation a/b/c preserved).
11. ✅ Fresh testnet deploy (post-F5 merge, wiped state): settlement end-to-end on 5-node AWS cluster produces byte-identical ledger state on every node for every round. Verified via cross-node-invariant monitoring.
    - **11a (Grok scenario expansion)**: deliberate node crash mid-`ApplySettlementRecords` → restart → verify paid-flag projection + deferral path correctly resume without double-pay or divergence. Crash injected at three specific positions (before ledger write, after ledger write before paid-flag persist, after both); each position must self-heal to byte-identical end-state with non-crashed nodes.
    - **11b (Grok scenario expansion)**: genesis replay on fresh node vs steady-state node → verify materialization-lag deferral converges and produces byte-identical records. A node syncing from genesis must produce ledger end-state byte-identical to a node that has been running continuously since the test rounds were processed.
12. ✅ 5B completion gate report produced + architect-reviewed + founder-confirmed.

## 8. Sign-off sequence

Mirrors Plan v3's lifecycle for 5A (reused for 5B):

1. **CC drafts Plan v1**. ✅ Complete.
2. **Architect reviews v1 in full** before any multi-AI send. ✅ Complete.
3. **Multi-AI review** (Grok + ChatGPT) with architect-supplied prompts. ✅ Complete.
4. **Revisions → Plan v2** integrating multi-AI feedback. ✅ Complete.
5. **Claude Code plan-mode review** of v2. ✅ Complete.
6. **Revisions → Plan v3** integrating plan-mode findings (this document). ⏳ In progress; pending architect final read.
7. **Founder approval** of v3.
8. **5B implementation** begins.
9. **5B completion gate** closes on architect review + founder confirmation.

## 9. Scope boundaries — open questions Plan v3 does NOT decide

- **Deferral retry policy**: retry count, backoff, timeout. Tuning decision at 5B implementation time (conservative defaults can be refined based on testnet observation).
- **Paid-flag storage encoding**: schema-level decisions about how paid-flag projection is serialized to BadgerDB. Implementation detail for 5B; doesn't need a plan-level lock.
- **Companion-PR landing timing**: alongside 5B merge unit vs post-5B. Architect decision at implementation time. (Companion-PR itself is locked per §0 decision 10; only the landing window is open.)
- **Applicator error handling under partial-apply non-crash error**: if node hits a non-`ErrDuplicateEntry` error from `TransferFromBucketLabeled` mid-batch (e.g., insufficient bucket balance — should not happen under correct derivation), policy: retry the record, skip and continue, or abort? Current §3.3 position: abort + return error + surface (halt-trigger per §5 partial-apply). Refine at implementation time if testnet observation reveals a legitimate recoverable case.
- **Order of 5B commits** (if split across multiple): the plan targets one combined commit but breaking it into staged landing (e.g., derivation function + tests first; applicator refactor second; dead-code removal third) is permissible if it simplifies review. Implementation discretion.

## 10. Risks and dependencies

**Primary risk**: 5B implementation reveals a hidden dependency between derivation purity and existing settler/applicator integration that 5A didn't scope. Mitigation: the halt triggers in §5 include this class (task.Status reopen, equivocation coupling, V-1 non-pureness, test-preservation violation). Halt-and-surface discipline per the Plan v3 precedent.

**Secondary risk**: cross-node byte-equality test reveals D-1 property violation (derivation is not pure). Mitigation: strict purity enforcement in code review + CI lint (5A.4.c scope, warning today, failure at 5D).

**Dependency on 5A primitives**: listed in §0 decision 2. All are on disk; 5B consumes without redesign.

**Dependency on F4**: F4 LogicalKeyConsumer pattern is the surface 5B's settler call chain operates inside. F4 is frozen; F4+F5 merge together at main merge.

**Dependency on testnet infrastructure**: 5-node AWS cluster at `f4-combined-0e93f48`. Testnet wipes at F5 merge; 5B verification deploys fresh.

**Tertiary risk (locked-workstream coordination)**: the locked Reputation-and-Consensus-Integrity workstream's steps 3-7 (delete old reputation, create EvidenceStore, rewrite distributeByQuality, wire EvidenceStore writer) are not yet implemented. If those steps land during 5B development, 5B may need re-integration. Mitigation: 5B assumes stub-W only. V-1 canonical-ancestor check handles stub-vs-real-W selection automatically without 5B-side changes. If real-W lands mid-5B-development, 5B's derivation function picks up the real implementation via the canonical-position-bound selection at the next round post-`ReputationActivation`. No 5B code changes required; testnet verification includes the stub-to-real transition per §7 criterion 11.

## 11. Meta-observation — the analogous error hiding in 5B

Per the gate-closure discipline: what is the analogous hidden error Plan v2 could contain?

### 11.1 PRIMARY hidden-error candidate (Gate 5B Plan v1 multi-AI convergent finding)

**DerivationInputs abstraction boundary leaks non-canonical state.**

This is the 5B analogue of F4's mutex-claim error (post-condition mistaken for pre-condition serialization) and 5A.2's V-1 invariant (runtime flag reintroduction via convenience). Both Grok and ChatGPT independently identified the pattern — convergent identification is high-confidence signal that the gap is real.

The shape: a future implementer adds a field to `DerivationInputs` that LOOKS like a canonical-projection interface but secretly reads impurity through the input bundle. Examples:

- A mutable wrapper around `CanonicalWProjection` that caches results in a process-local map and falls back to live-read on cache miss — looks like the canonical interface; actually couples derivation to local state.
- A flag-closing `ActivationCheck` function whose closure captures a `reputationActivated bool` set by a consumer at admission time — looks like a pure function; actually queries runtime state through the closure.
- A `dag.AnchorReader` wrapper that falls back to local-tip on `ErrEventNotFound` — looks like the canonical reader; actually returns wrong-direction defaults instead of propagating the deferral signal.
- A "convenience" interface like `EscrowMgr.GetWithFallback(taskID)` that returns synthesized data when the entry doesn't exist — looks like a canonical lookup; actually invents data.

**The §2.1 DerivationInputs contract is the structural defense.** Every field MUST satisfy contract clause (a) or (b); no field may expose mutable state through alternative paths. The contract is enforced by:
1. Field-level documentation naming which clause it satisfies.
2. Code review checks new fields against the contract.
3. Future 5A.4.c lint expansion (post-5D) that validates the contract structurally.
4. 5D verification harness property-test that constructs `DerivationInputs` with deliberately-non-canonical wrappers and asserts detection.

**Grep-level test (Grok)**: verify no derivation path reads `escrowMgr`, `taskMgr`, applied maps, or paid-flag fields to influence output (as distinct from idempotency-bounded short-circuit reads already permitted per Gate 5A.1 §9.2 task.Status option-b). The grep should return zero hits in the derivation package's source files.

This is the load-bearing meta-observation for 5B. The DerivationInputs contract IS the V-1 analogue at the boundary level.

### 11.2 Secondary hidden-error candidates (carried from Plan v1)

The four candidates from Plan v1 remain as secondary patterns worth naming:

- **Implicit coupling between derivation purity and applicator state**: if derivation reads anything applicator-side (e.g., "has this been applied?" to decide what to emit), purity is broken. §2.4 forbids this explicitly; 5B implementation must hold it.
- **Ordinal-assignment rule assumed locked at 5A.4.a but not enforced in 5B**: if 5B's derivation function produces ordinals differently from schema §`purpose.ordinal.ordinal_assignment_rule`, records fail U-1 uniqueness. CI lint at 5A.4.c + 5D verification harness are defenses. §2.3 step 7 schema-implements-to discipline calls this out.
- **Crash-recovery flag semantics mis-wired**: if paid-flag fields are ever READ to determine canonical behavior (violating obligation c per 5A.1 Finding 6 5B architectural obligation), the obligation breaks. §3.4 explicitly forbids.
- **Runtime-flag reintroduction via convenience**: if 5B implementation introduces a `reputationActivated bool` field (or similar) "for performance" to avoid repeating canonical-ancestor checks, V-1 is violated. §0 decision 3 + §2.4 forbid. The DerivationInputs contract (§2.1, §11.1) generalizes this defense.

These five candidates (one primary + four secondary) are the classes of hidden error Plan v2 v3 + Claude Code plan-mode review should pressure-test.

---

**End of F5 Phase 5B Plan v3 — post CC plan-mode review. Ready for architect final read.**
