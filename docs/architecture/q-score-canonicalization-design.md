# Q-Score Canonicalization Design

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5A.2 — Q-score canonicalization design
**Status**: **v2.2 (post Gate 5A.3 round-2 cross-doc terminology precision patch).** v2.0 was the post-Gate-5A.2-multi-AI-review draft; v2.1 added the §7.2 ErrEventNotFound deferral + AdmissionRecord.DAGAnchor warning concurrent with 5A.3 drafting; **v2.2 (this revision) adds the explicit canonical_seal_context vs SubmissionEventID cross-doc disambiguation per ChatGPT Finding 1 at Gate 5A.3 round 2**. All 12 deliverable items drafted; §7 V-1 invariant; §0.8.7 invalid-input contract; §9 absence-of-data; §12.3 LRU 2-4× sizing; §15.4 forward notes; §15.5 meta-observation. Five open questions §13.1-§13.5 all resolved. No halt-trigger fired. Gate 5A.2 closed; v2.1 + v2.2 are precision patches that did not reopen the gate.
**Commit**: `51bce89` (F4-frozen branch).
**Date**: 2026-04-23 (draft initiated; Gate 5A.2 close TBD)
**Plan reference**: F5 Phase 5A Plan v3 §3.2 + Gate 5A.1 closure 12-item expansion.

---

## 0. Decision locked

**Q-C is the chosen design**: canonical Q projection read at canonical cutoff anchor. Plan v3 §3.2 locked Q-C as default; this document is the specification.

Alternative designs (Q-A snapshot-at-seal, Q-B on-the-fly evidence-replay, Q-D advisory fallback) are not designed here. If a halt-trigger fires during 5A.2 design, the document will explicitly document which alternative is selected and why.

## 0.5 Discovery — integration with locked Reputation-and-Consensus-Integrity workstream

**Status**: Architectural discovery surfaced during 5A.2 substantive design (2026-04-23, after research agents returned). Surfaced as new open question §13.1 for architect attention.

The locked workstream document `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` ("Reputation and Consensus Integrity — Locked Design"; Decisions §0 — locked, not open for debate) already specifies:

- **ReputationEvidence** as the canonical evidence record, written by `TaskVerificationConsensusConsumer` at consensus finalization (locked plan §2.1, invariants CR-1/CR-2/CR-3/CR-4).
- **The existing `internal/taskverification/reputation.go` is deleted in the same commit** that introduces the new evidence store (locked plan Decision 2). No migration; empty on every node post-cutover.
- **W (Independence Weight)** as the canonical projection that **replaces `ValidatorQScoreFn` entirely** (locked plan §4.1). W has 5 discrete tiers (T0-T4: 7000-13000 BP), computed as `agreement_bp = (AgreementEvents * 10000) / max(1, TotalEvidence - AbstainEvents)` then mapped to tier with Bootstrap-override and context-bounded-trust-transfer rules.
- **Snapshot framework** (locked plan §5): deterministic per-epoch snapshots of the aggregate cache, with `snapshot(epoch E) = snapshot(epoch E-1) + forward_apply(epoch E events)` (SN-2). Epoch length 1000 rounds (locked); retention 8 epochs (locked).
- **Cold-start states** (locked plan §6): Bootstrap/Probation/Mature derived from `TotalEvidence`, with W behavior per state.

**Implication for F5 5A.2**: this design document is NOT designing a canonical Q projection from scratch. The locked workstream already designs the canonical replacement for the advisory `ValidatorQScoreFn`. **F5 5A.2's contribution is anchor-precise historical-read extension on top of the locked W projection.**

What F5 must add that the locked plan does not specify:
- **Anchor-precise historical-read** at any past canonical cutoff (the locked plan's W takes `epoch` as input — coarse-grained at 1000-round boundaries; F5's Input-Domain-2 requires lookup at round-seal cutoff).
- **Cutoff anchor precision tying lookup to round-seal** (item 11): is the cutoff the round-seal anchor (round-precise) or the snapshot boundary at the start of round's epoch (epoch-coarse, matches W's existing API)?
- **Version-binding rule** (item 7): aligns with `DerivationVersion` field already in `ReputationEvidence` (locked plan §2.1).
- **Bootstrap/recovery** (item 8): the locked plan's snapshot framework already provides this; F5 confirms compatibility.
- **Scaling target verification** (item 12): the locked plan's snapshot SN-4 invariant bounds snapshot generation; F5 adds the lookup-side gate.

What this means for the rest of this design:
- Items 1-3 (foundational, drafted) are GENERAL specifications of the historical-read property, storage model, and cutoff semantics. They remain valid as the F5 contribution; they're now framed as "extending the locked W projection's API" rather than "designing a new projection."
- Items 4, 6, 11 (drafted in this revision) integrate directly with the locked plan's structures.
- Item 5 (Reputation Step 4 interface) collapses: Reputation Step 4 IS the locked Reputation-and-Consensus-Integrity workstream. F5 consumes W; no separate interface is designed here.

**This discovery is surfaced as open question §13.1** for architect direction. If the architect confirms F5 5A.2 = anchor-precise extension to the locked W projection, the design simplifies substantially. If the architect directs F5 5A.2 to design an INDEPENDENT canonical Q projection separate from W, the design must explicitly justify the duplication.

**§13.1 ARCHITECT DECISION (2026-04-23)**: F5 5A.2 = extension to locked W, NOT separate projection. Two competing canonical reputation primitives would violate Principle 6. 5A.2 scope shifts to: (1) reference W as the canonical Q source, (2) specify how settlement reads W at a settlement cutoff, (3) identify gaps between settlement needs and locked W, (4) flag integration concerns. Sections 4, 6, 7, 8, 9, 10 become integration specifications, not from-scratch designs.

## 0.7 Background — Locked Reputation Workstream Summary

This section summarizes `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` (the "Locked Design") to ground the rest of this document. Read it carefully — the locked plan is the source of truth; this summary is a navigational aid for 5A.2 design context only. Where summary and locked plan differ, the locked plan wins.

### 0.7.1 Locked decisions (§0)

10 decisions named not-open-for-debate. Most relevant to F5:
- **D2**: existing `internal/taskverification/reputation.go` is **deleted** in the same commit that introduces the new evidence store. No migration; empty on every node.
- **D3**: One evidence base + four projections (W, E, S, C). Evidence base is primary; projections are disjoint derived views.
- **D7**: Projection registry is a protocol primitive. CI-enforced. Runtime-enforced. Every existing consensus-adjacent store is retrofitted in the same workstream.

### 0.7.2 Architecture (§1-§3)

**Evidence base**: `ReputationEvidence` records (§2.1) — one per validator per round per (family, category) tuple. Written by `TaskVerificationConsensusConsumer` at consensus finalization (CR-1). Keyed by `"rep:" + EpochIndex + ":" + ValidatorID + ":" + RoundID` — primary index makes writes idempotent under replay (the key correctness improvement over the existing advisory store, which keys by (validator, family, category) alone and double-counts on replay).

Every field of `ReputationEvidence` is integer/enum/ID/bool. No `float64`. No `time.Time`. Derivable purely from `(TaskVerificationConsensus event, round.Votes, validator-seat snapshot at round open)` (CR-4).

**Aggregate cache**: `ReputationAggregate` (per-validator) and `ReputationPairAggregate` (pair, bounded). Strict projection of evidence (CA-3); rebuildable byte-identical at any time. The cache is not authoritative; evidence wins on disagreement. Pair aggregates are bounded by `CoParticipationThreshold ≥ 5` and storage-budget-tested (CA-2 worst-case under 2 GB BadgerDB).

### 0.7.3 The four projections (§4)

- **W (Independence Weight)** §4.1 — feeds Q-weighted settlement fee distribution. **Replaces `ValidatorQScoreFn` entirely.** Five discrete tiers T0-T4 (7000-13000 BP). Computation: `agreement_bp = (AgreementEvents * 10000) / max(1, TotalEvidence - AbstainEvents)` then mapped to tier. Bootstrap-override (cold-start = T0). Context-bounded trust transfer (cap at T2 if `EscrowBudget` > 3× trailing-20-round median). Pure integer (W-1). Bounded movement: one new evidence ≤ one tier change (W-3). The W function signature is `ValidatorIndependenceWeightFn(validator, family, category, contributor, escrowBudget, epoch) → uint64`. **Note the `epoch` parameter** — query is parameterized by epoch index, not anchor.
- **E (Eligibility)** §4.2 — binary gate at vote ingestion. Deterministic from DAG-observable state at round open.
- **S (Scrutiny Level)** §4.3 — three levels (Normal/Elevated/High). Stake-at-risk multiplier and replication floor.
- **C (Challenge Alert)** §4.4 — anomaly signals from pair aggregates; logged only in this workstream. Action mechanism is the next workstream (challenge path).

### 0.7.4 Snapshot framework (§5)

Per-epoch deterministic snapshots of the aggregate cache. Locked: 1000 rounds per epoch (§5.4); 8 epochs of retained raw evidence (§5.5).

**SN-1**: snapshot root is a deterministic function of `(previous_snapshot_root, epoch_consensus_events_in_canonical_order, derivation_version)`.
**SN-2**: `snapshot(epoch E) = snapshot(epoch E-1) + forward_apply(epoch E events)`. Snapshot-plus-forward-replay equals full-replay.
**SN-3**: snapshot generation has no dependence on map iteration order, memory layout, wall clock, or local compaction.
**SN-4**: snapshot generation cost ≤ 30 seconds on m7i.large under worst-case epoch (verified on testnet pre-ship).

§5.2: "A snapshot is a deterministic serialization of the aggregate cache at an epoch boundary, derived from already-finalized DAG state. ... There is no vote on snapshot roots. ... Disagreement on the root is disagreement about DAG state or about implementation, either of which is a consensus fault."

### 0.7.5 Cold-start (§6)

States derived from `TotalEvidence` (not stored as a flag): Unseated / Bootstrap (<20) / Probation (20-79) / Mature (≥80). Bootstrap → W=T0 always; Probation → W capped at T2; Mature → full T0-T4 range. CS-1 (monotone): once past Bootstrap, always past Bootstrap.

### 0.7.6 Slashing (§8)

- Equivocation hard-slash: **automatic** (HS-2; cryptographically provable at ingestion).
- Systematic-divergence hard-slash: **removed** (HS-1); replaced by `InvestigationAlert` event with no canonical action until challenge-path workstream ships.
- Float64 agreement-rate comparison: removed (HS-3); pure integer BP throughout.
- §8.4 honesty clause: temporary regression — systematic divergence detected but not acted on between this workstream and challenge-path workstream.

### 0.7.7 Projection registry (§9)

New `internal/projections/` package. CI checks for writer-without-caller pattern (PR-1/PR-2: empty consumer refs fail startup). Mandatory integration test reference (PR-3). Mandatory observability surface (PR-4). Runtime health check at every epoch boundary; canonical projection with empty aggregate after `EligibilityWindow` (3 epochs) is a fatal health error (PR-5).

### 0.7.8 Sequencing (§17)

14-step implementation sequence. Steps 1-4 are foundation (registry, retrofits, deletion of advisory store, new package). **Step 5: rewrite `VerificationConsensusSettler.distributeByQuality` to integer math with new `ValidatorIndependenceWeightFn` signature.** **Step 6: wire EvidenceStore writer into TVConsensus consumer at canonical write boundary CR-1 — "this is the step that makes reputation actually accumulate."** Steps 7-13: slashing rewrite, observability, doc updates, testnet verification. Step 14: workstream closes; challenge-path opens.

### 0.7.9 What this means for F5 5A.2

F5's "canonical Q projection" is the W projection from this locked plan. F5 contributes:
- **The settlement-cutoff binding rule** (item 11): which `epoch` does settlement pass to W when settling round R.
- **Gap identification**: any settlement need not satisfied by the locked W as specified.
- **Integration concerns**: sequencing, version-binding, bootstrap behavior at the F5/locked-workstream interface.

F5 5A.2 does NOT design the W computation, the evidence record, the snapshot framework, or the projection registry. Those are locked.

## 0.8 CanonicalWProjection interface specification (verified against locked plan §4.1)

Per architect direction §13.5 (option b — F5 ships with stub-W; locked workstream swaps in real W via canonical activation event), F5 5A.2 defines the **CanonicalWProjection interface contract** that both the stub (F5 ships) and the real W (locked workstream ships) implement. The interface MUST exactly match the locked plan §4.1 specification so the swap is invisible to F5's derivation function.

### 0.8.1 Verified interface signature

Locked plan §4.1 specifies:
```
ValidatorIndependenceWeightFn(validator, family, category, contributor, escrowBudget, epoch) → uint64
```

Verified F5 interface (Go):

```go
// CanonicalWProjection is the canonical W (Independence Weight) lookup interface.
// Replaces the existing ValidatorQScoreFn entirely. Both the F5-shipped
// NeutralBPStubW and the locked-reputation-workstream-shipped real W
// implement this interface; the swap happens at canonical
// ReputationActivation event admission.
type CanonicalWProjection interface {
    // Lookup returns the Independence Weight tier (in basis points) for a
    // validator participating in a round with the given context, indexed by
    // the snapshot at the given epoch.
    //
    // Returns one of the locked tier values: 7000 (T0), 8500 (T1),
    // 10000 (T2), 11500 (T3), 13000 (T4). Per W-2 (locked plan §4.1):
    // bounded in [T0, T4]. Per W-1: pure integer; same inputs produce
    // identical outputs on any architecture.
    //
    // The epoch argument is the cutoff_epoch per F5 §11; settlement for
    // round R passes epoch_of(R) - 1 (snapshot at end of immediately-prior
    // epoch). All rounds within an epoch see the same W for given inputs.
    Lookup(
        validator    crypto.AgentID,
        family       FamilyID,        // protocol-defined enum, locked plan §2.1
        category     CategoryID,      // protocol-defined enum, locked plan §2.1
        contributor  crypto.AgentID,  // task poster — see §0.8.3 ambiguity note
        escrowBudget uint64,          // current round's task escrow in µAET
        epoch        uint64,          // cutoff epoch index per F5 §11.5
    ) (weightBP uint64, err error)
}
```

### 0.8.2 Verification matrix

| Architect verification question | Locked plan §4.1 answer | F5 interface |
|---|---|---|
| Input arguments | `validator, family, category, contributor, escrowBudget, epoch` (6 params) | ✅ MATCH |
| Return type | `uint64` raw basis points (NOT `protocolmath.BasisPoints`) | ✅ MATCH (returns uint64) |
| Cold-start behavior | Returns discrete tier value (T0=7000 in Bootstrap state); single uint64 return | ✅ MATCH (single uint64 return; tier discreteness enforced by locked W's implementation, not by interface) |
| Snapshot query semantics | Explicit `epoch` parameter (NOT global-current snapshot) | ✅ MATCH |

**Verification result**: locked plan §4.1 specifies the interface unambiguously. F5 5A.2 can lock the interface contract.

### 0.8.3 Ambiguity surfaced — `contributor` parameter semantic role

The `contributor` parameter (task poster's AgentID) appears in the W function signature in locked plan §4.1, but the **explicit W computation rules in §4.1 do not visibly use it**. The visible rules use `(family, category, agreement_rate, escrowBudget, validator's cold-start state)` — `contributor` doesn't appear in the agreement-rate formula, the tier placement thresholds, the Bootstrap override, or the context-bounded trust transfer rule.

Plausible interpretations:
- **(I1) Reserved for future use**: parameter included for forward compatibility (e.g., future per-contributor reputation laundering detection); current locked W ignores it.
- **(I2) Implicit in context-bounded trust transfer's "trailing-20-round median in this (family, category)" computation**: maybe the median is computed per-contributor too, not just per-(family, category). §4.1 doesn't say this, but it's a possible reading.
- **(I3) Used in a rule not shown in §4.1**: perhaps documented in another section of the locked plan I haven't surfaced.

**For F5 5A.2's interface contract**: include `contributor` as a parameter (matches locked spec); F5's stub returns NeutralBP regardless of contributor; F5's derivation function passes the round's `posterID` as the contributor. If interpretation (I1) holds, the stub is correct and the real W will start using contributor when it ships. If (I2) or (I3) hold, the stub may need refinement, but the SIGNATURE is still correct — the value passed is just what locked W expects.

**Architect awareness flag**: this ambiguity should be coordinated with the locked reputation workstream's author when real W lands. If `contributor` turns out to require specific semantics F5 didn't anticipate, the stub-to-real swap test (§0.8.5) catches it.

### 0.8.4 Edge case noted — trailing-N-round median outside retention window

Locked plan §4.1's context-bounded trust transfer rule requires the validator's "trailing-20-round median escrow in this (family, category)". This requires per-round detail (escrow per round), not just aggregate counters. Per locked plan §5.5, raw evidence is retained for 8 epochs; older data is compacted to aggregates without per-round detail.

**Implication**: W queries at epochs older than 8 epochs ago may not have the data needed to compute trailing-20-round-median accurately. For F5's typical use case (settler queries snapshot of immediately-prior epoch), this is well within retention; not an issue. For audit/replay queries at deeply historical epochs, the trailing-median rule may need a degraded-precision answer or an explicit error.

**Disposition**: noted for awareness; not a blocker for F5's typical use case. Locked workstream may want to address in its own design (whether to preserve trailing-N-round detail in compacted aggregates, or to define degraded-precision behavior for old queries).

### 0.8.5 Stub-to-real selection verification (5D test scenario)

Per architect direction: F5 verification matrix (Phase 5D) includes a stub-vs-real-W selection verification test. **Per §7.1 V-1 invariant**: selection is bound to round R's canonical position relative to `ReputationActivation`, not to runtime activation state.

1. F5 ships with `NeutralBPStubW` implementation of `CanonicalWProjection`. For rounds canonically before `ReputationActivation`, derivation selects `NeutralBPStubW` via the canonical-ancestor check (§7.2); `Lookup` returns 10000 (NeutralBP).
2. A `ReputationActivation` canonical event is emitted on the DAG.
3. Every node admits the activation event canonically (admission gate per §7.3 confirms F5 activation is already admitted).
4. For rounds canonically AT-OR-AFTER `ReputationActivation`, derivation's canonical-ancestor check resolves true, and `Lookup` calls go to real W (or test-stub-2 with distinctive tiered values).
5. Test asserts: for ANY round R, every node selects the same W implementation as a function of R's canonical position relative to `ReputationActivation`, regardless of when each node admitted the activation event in wall-clock time. Replay the same DAG on a freshly-bootstrapped node; selections for every R match the steady-state node's selections byte-identically.

**Contributor parameter propagation verification**: the test scenario MUST explicitly verify that the `contributor` parameter is propagated correctly through F5's derivation function → `CanonicalWProjection.Lookup` → the post-activation implementation. The test setup constructs a round R with a specific known `posterID = K`; the derivation function passes `K` as the `contributor` argument; the test-stub-2 (or real W when shipped) is instrumented to verify it received `K` for the validator queries on R. If the locked W's real implementation uses `contributor` semantically (per §0.8.3 ambiguity I2/I3), the test asserts the produced W values reflect that contributor's relationship to the validator (e.g., distinct W when same validator is queried for different contributors). If the real W ignores `contributor` (per ambiguity I1), the test asserts the parameter is at least passed through correctly even if the value is unused. This catches any wiring error where F5's derivation function fails to forward the round's posterID as the contributor argument.

**Test feasibility before real W exists**: per architect direction, the test can use a "fake real W" (second stub returning distinctive tiered values like {T0: validator A, T4: validator B}, optionally varying with contributor for the propagation check). When real W ships, the same test runs with real W as the post-`ReputationActivation` implementation. The verification mechanism is the canonical-position-bound selection pattern (§7.2) itself; the specific implementation behind the interface is interchangeable.

**Activation pattern reference**: mirrors `IntegerMigrationActivation` event from the integer-migration workstream (referenced in `internal/settlement/verification_consensus_settler.go:50-80` for shadowMode swap) at the EMISSION + ADMISSION level (canonical event + admission consumer). However, per V-1 (§7.1), F5's selection mechanism is canonical-position-bound rather than runtime-flag-bound — the integer-migration workstream's `shadowMode` boolean read at execution time is the older pattern that V-1 explicitly supersedes. F5's stub-to-real swap is selected by `canonical_ancestor(ReputationActivation, R)`, not by a `reputationActivated` boolean read at settler invocation.

### 0.8.6 NeutralBPStubW implementation specification

F5 Phase 5B ships `NeutralBPStubW` in the F5 derivation package:

```go
// NeutralBPStubW is F5 Phase 5B's stub implementation of CanonicalWProjection.
// Returns NeutralBP (10000) for all queries, matching today's effective
// production behavior (the existing ValidatorReputationStore.RecordVote
// has zero production callers, so today's ValidatorQScoreFn always returns
// NeutralBP when TotalVotes is 0).
//
// SUPERSEDED by the locked Reputation-and-Consensus-Integrity workstream's
// real W implementation for any round R where ReputationActivation is a
// canonical ancestor of R's settlement context (per §7.1 V-1 invariant).
// Note: the selection is canonical-position-bound — F5's derivation function
// chooses stub vs real W via the canonical-ancestor check, not via a
// runtime flag. The stub is named explicitly so future readers understand
// it is interim, not the long-term canonical W.
type NeutralBPStubW struct{}

func (NeutralBPStubW) Lookup(
    validator    crypto.AgentID,
    family       FamilyID,
    category     CategoryID,
    contributor  crypto.AgentID,
    escrowBudget uint64,
    epoch        uint64,
) (uint64, error) {
    return 10000, nil  // NeutralBP per protocolmath.NeutralBP equivalent
}
```

The stub is a constant function. F5's derivation function calls it identically to how it will call real W; the selection between stub and real W is determined by the canonical-position-bound invariant per §7.1, not by a runtime swap.

### 0.8.7 Interface invalid-input contract

Per Gate 5A.2 multi-AI review (ChatGPT findings): the `CanonicalWProjection.Lookup` interface contract specifies deterministic error behavior for every degenerate input case. Implementations MUST conform; F5's stub MUST conform; locked workstream's real W MUST conform.

| Input case | Required behavior | Rationale |
|---|---|---|
| `contributor` is an opaque AgentID context parameter — there is no validator-membership constraint on this argument. The contributor is the task poster (round.PosterID), not necessarily a validator. | No error on unknown contributor; W computation may use or ignore the value per implementation; the parameter is forwarded as-is. | Per §0.8.3 ambiguity: contributor's semantic role in W is not visible in locked plan §4.1; the parameter passes through. |
| `family` outside the protocol-defined enum range (per locked plan CR-6: 16 families). | Deterministic error return. NOT implementation choice. | Cluster-uniform error semantics required to prevent one node erroring while another silently uses an undefined-behavior fallback. |
| `category` outside the protocol-defined enum range (per locked plan CR-6: 64 categories). | Deterministic error return. NOT implementation choice. | Same rationale as family. |
| `epoch` earlier than the first retained snapshot (i.e., before the snapshot framework's retention window — locked plan §5.5: 8 epochs). | Deterministic behavior MUST be specified — either (a) error return with a documented error type, OR (b) fall through to a defined base case (e.g., return NeutralBP for "no historical data available"). The choice is the locked workstream's; F5 5A.2 requires the choice be EXPLICIT and DOCUMENTED, not implicit. | Without explicit policy, two implementations may diverge at the same input — V-1 violation. |
| `epoch == 0` or first protocol epoch (no prior epoch's snapshot exists). | Base-case override defined: return NeutralBP (matches "no evidence yet" cold-start state per locked plan §6). | Bootstrap-state edge case; prevents undefined behavior at protocol genesis. |

**F5's `NeutralBPStubW` conforms trivially**: the stub returns NeutralBP for all inputs including the degenerate cases above (it doesn't read the inputs). Conformance is a no-op for the stub; conformance is load-bearing for the real W.

**Test surface**: F5 5D verification harness includes property-based tests asserting each row's behavior across both implementations. Cross-node test: two implementations querying the same degenerate input return the same result (or the same error type). Mismatches are halt-worthy at Gate 5D.

---

## 1. Historical-read requirements (F5's three operational properties)

F5 5A.2 specifies the operational properties the canonical W projection MUST satisfy for settlement derivation. The locked Reputation-and-Consensus-Integrity workstream's snapshot framework (locked plan §5) implements these properties; F5 enumerates the requirements so the integration contract is unambiguous.

For any past canonical cutoff (per §11, the epoch index of the snapshot at end of the immediately-prior epoch) and any (validator, family, category, contributor, escrowBudget) tuple, `CanonicalWProjection.Lookup` (per §0.8.1) MUST satisfy:

1. **Deterministic across nodes**: any two nodes evaluating `Lookup(v, f, c, k, b, e)` for the same arguments return byte-identical results. Per locked plan SN-3 (snapshot generation has no dependence on map iteration order, memory layout, wall clock, or local compaction).

2. **Stable across time**: the value returned for `Lookup(v, f, c, k, b, e)` does NOT change when later canonical evidence is processed. The locked plan's snapshot at epoch `e` is immutable post-creation (per SN-1: snapshot root is a deterministic function of `(previous_snapshot_root, epoch_consensus_events_in_canonical_order, derivation_version)` — a function of historical state, not future state).

3. **Replay-invariant**: a node syncing from genesis and reaching epoch `e` produces the same `Lookup(v, f, c, k, b, e)` value as a node that has run continuously since `e` was reached. Per locked plan SN-2: `snapshot(epoch E) = snapshot(epoch E-1) + forward_apply(epoch E events)` — snapshot-plus-forward-replay equals full-replay.

These three properties are the operational form of Input-Domain-1, -2, -3, -4 (manifest §3.1) applied to the W input. The locked plan §5's snapshot framework satisfies all three by construction. F5 5A.2's contribution is identifying these as the integration requirements, not designing the mechanism that satisfies them.

The actual interface contract is `CanonicalWProjection` per §0.8.1. The actual implementation is the locked workstream's W (or F5's `NeutralBPStubW` per §0.8.6 pre-Reputation-Activation).

## 2. Storage / replay cost (deferred to locked workstream)

Storage and replay cost are the locked workstream's responsibility:

- **Retention**: `EvidenceRetentionEpochs = 8` (locked plan §5.5; locked constant per §16). 8 epochs of retained raw evidence; older compacted to aggregates.
- **Snapshot bound**: SN-4 — snapshot generation cost ≤ 30 seconds on m7i.large under worst-case epoch (verified on testnet pre-ship per locked plan §15 success criterion 18).
- **Pair aggregate bound**: CA-2 — worst-case retained pair-aggregate state under locked constants must fit within 2 GB BadgerDB usage (locked plan §3.2; mandatory test per success criterion 10).

F5 5A.2's contribution is the **scaling target for the lookup hot path** specified in §12 (p99 < 100µs at 10k QPS sustained with hot-validator cache). This applies to the real W; F5's `NeutralBPStubW` trivially satisfies it as O(1) constant-return.

## 3. Projection cutoff semantics aligned with Input-Domain-2/3

For a TaskVerificationRound `R`, the canonical W lookup occurs at exactly one cutoff: the snapshot at end of `R`'s immediately-prior epoch. Per Plan v3 §0 + manifest §3.1:

- **Input-Domain-2**: canonical-live inputs are read at the canonical cutoff (DAG-derived from round-seal state).
- **Input-Domain-3**: cutoff is derived from round-seal state, not from local wall-clock.
- **Input-Domain-4**: every canonical-live / canonical-derived input has a deterministic, replayable lookup at the cutoff.

The cutoff binding is **epoch-coarse** (per §11.2, verified against W update semantics in §13.3): all rounds within an epoch query the SAME snapshot, ensuring cluster-uniform W values for any given round regardless of when each node settles it.

The contract with 5B's derivation function:

```
derive_payouts(round: TaskVerificationRound) → PayoutRecord
```

Internally:
```
e := cutoff_epoch_for(round)         // pure function of round per §11
for each agreeing_validator v:
    w_v := CanonicalWProjection.Lookup(v, family, round.Category,
                                        round.PosterID, round.EscrowBudget, e)
... compute payouts using w_v values ...
```

Both `cutoff_epoch_for(round)` (§11) and `Lookup(v, f, c, k, b, e)` (§0.8.1) are pure functions of canonical state. The derivation is therefore pure.

The full formal cutoff specification — including the relationship between round-seal and epoch index, the snapshot-end-of-(E-1) convention verified against locked plan §5.2, and the why-epoch-coarse-vs-round-precise discussion — is in §11.

---

## 4. Coupling analysis vs existing advisory reputation projection

### 4.1 The advisory store is being deleted (locked plan Decision 2)

There is no coupling to manage. The locked Reputation-and-Consensus-Integrity workstream Decision 2 deletes `internal/taskverification/reputation.go` in the same commit that introduces the new evidence store. The advisory `ValidatorReputationStore.RecordVote` and `ValidatorQScore` functions cease to exist post-cutover. The store is empty on every node.

This collapses the F5 plan v3 §3.2 deliverable item 4 ("coupling analysis vs existing advisory reputation projection") into: **there is nothing to couple to.** The existing advisory projection is removed, not preserved alongside the canonical one.

### 4.2 Confirming the audit observation about non-idempotency

The 5A.1 audit observed that `ValidatorReputationStore.RecordVote` is not keyed by canonical event ID, making it non-idempotent under replay. Research agent walk also surfaced that **`RecordVote` has zero production callers** today — the store is wired into `ValidatorQScoreFn` but never written to in production. So the existing Q-via-`AgreementRate` always returns `NeutralBP` because `TotalVotes` is always 0.

Net effect: today's settlement uses NeutralBP for every validator (effectively even-split). The cross-node Q-divergence path described in 5A.1 audit §4.1 exists in code but is dormant in production because the write path is unwired. F5 closes the dormant divergence path AND brings W online via the locked plan's evidence base.

### 4.3 Coexistence with `internal/reputation` (worker reputation, distinct surface)

`internal/reputation/reputation.go` is **worker** reputation (task completion quality), distinct from validator Q. It is registered as Advisory and is NOT replaced by the locked plan. F5 5A.2 has no coupling concern with worker reputation — it lives on a different surface, contributes to a different decision (worker discovery / marketplace router), and is not consulted by the settler.

### 4.4 Worker / validator distinction in the manifest

The 5A.1 manifest classifies `validator_q_score` as the input being canonicalized. This is correct; worker reputation is not in the settlement derivation surface today. If a future workstream ties worker reputation to settlement derivation, that's a separate canonicalization (parallel to F5 but on a different input).

## 5. Integration interface for Reputation Step 4

### 5.1 The locked Reputation-and-Consensus-Integrity workstream IS Reputation Step 4

Per architect direction §13.1, F5 5A.2 consumes the locked plan as the source of truth for reputation primitives. The "Reputation Step 4" deferral mentioned in F5 Plan v3 §0 decision 5 is the locked Reputation-and-Consensus-Integrity workstream itself.

**Integration interface**: the `CanonicalWProjection` interface in §0.8 IS the integration interface. Reputation Step 4 implements the real W behind this interface; F5 consumes it identically pre- and post-real-W landing.

### 5.2 Forward compatibility for evolving W computation

The locked plan §4.1 implements W's computation as:
```
agreement_bp = (AgreementEvents * 10000) / max(1, TotalEvidence - AbstainEvents)
```
mapped to one of five tiers (T0-T4) per §4.1's tier placement table, with Bootstrap-override (T0) and context-bounded-trust-transfer (cap at T2 if EscrowBudget > 3× trailing-20-round median).

The locked plan does NOT specify a multi-factor "Q formula" for W. The single-formula computation above is what the locked plan ships.

**Note (potential scope addition flagged for verification)**: the existing `internal/taskverification/reputation.go:186-191` TODO comment in the to-be-deleted advisory store describes a 4-factor formula `Q = (α₁·CVD_norm + α₂·ChallengeSurvival + α₃·ReplicationRate + α₄·Consistency) / Σα` from "paper v4.1". This 4-factor framing is **NOT in the locked plan §4.1**. CC encountered the framing in the existing code's TODO; if the locked workstream's author intends to extend W to a multi-factor formula (consistent with the TODO comment's intent), F5 5A.2's interface contract still holds (a single uint64 return is implementation-agnostic about the internal computation). If the locked workstream's author does NOT intend to extend W beyond the single agreement-rate formula, the locked plan §4.1 is the final shape and the TODO comment is an artifact of the to-be-deleted code.

**Gate condition for real-W ship**: real W MUST NOT ship until the locked-workstream owner confirms whether the four-factor formula is superseded, deferred, or intended as later evolution behind the same interface. F5 ship (stub only) does not depend on this resolution — the stub is implementation-agnostic about the formula; only real W's authoring needs the answer.

**F5 5A.2 design implication regardless of resolution**: the `CanonicalWProjection.Lookup` interface returns a single `uint64` weightBP value. Whether that value is computed from one component (locked plan ships) or extended later is hidden behind the interface. F5's derivation function does not change as the W computation evolves.

### 5.3 No additional 5A.2 interface required

Per architect direction, items 4, 5, 10 collapse into "F5 consumes the locked workstream's primitives." There is no additional Reputation Step 4 interface F5 needs to design beyond `CanonicalWProjection`. Future work (challenge path, full Q formula, additional projections E/S/C if F5 ever needs them) is the locked workstream's owners' decision; F5 5A.2 reserves the right to consume them through additional interfaces of similar shape if the need arises.

### 5.4 Coupling boundary

F5 5A.2 owns:
- The `CanonicalWProjection` interface contract (§0.8).
- The `NeutralBPStubW` stub implementation (F5 Phase 5B ships it).
- The settlement-cutoff binding rule (item 11).
- The stub-to-real swap verification test (§0.8.5; F5 Phase 5D).

F5 5A.2 explicitly does NOT own:
- The W computation logic (locked workstream §4.1).
- The evidence record schema (locked plan §2.1).
- The aggregate cache structure (locked plan §3).
- The snapshot framework (locked plan §5).
- The cold-start state machine (locked plan §6).
- The projection registry (locked plan §9).
- The real W implementation (locked workstream §17 step 5).

This is a clean separation. Future workstreams that touch reputation should observe this boundary.

## 6. Evidence domain definition (core)

### 6.1 Evidence record (defined in the locked plan)

The canonical evidence record is `ReputationEvidence` per the locked Reputation-and-Consensus-Integrity workstream §2.1. F5 5A.2 references the locked specification; this section restates the load-bearing properties for F5's purposes.

```go
type ReputationEvidence struct {
    RoundID           RoundID       // the round this evidence is from
    EpochIndex        uint64        // protocol epoch at round finalization
    RoundHeight       uint64        // monotone counter, per-epoch
    ValidatorID       AgentID       
    Family            FamilyID      
    Category          CategoryID    
    ContributorID     AgentID       
    VoteVerdict       Verdict       
    ConsensusVerdict  Verdict       
    Agreed            bool          
    StakeBP           uint64        
    EscrowBudget      uint64        
    DerivationVersion uint32        
    TrajectoryRoot    string        
}
```

Every field is integer, enum, ID, or bool. No `float64`. No `time.Time`.

### 6.2 Canonical write boundary

Per locked plan invariant CR-1: a `ReputationEvidence` record is written by `TaskVerificationConsensusConsumer` in the same consumer invocation that fires settlement, **after the round state transition has been persisted, driven by the canonical `TaskVerificationConsensus` DAG event**. Not by local vote receipt, tentative aggregation, or pre-consensus state.

Per CR-4: every field is derivable from `(TaskVerificationConsensus event, round.Votes, validator-seat snapshot at round open)`. No operator input, wall clock, or non-DAG side channel.

### 6.3 Ordering rule

Evidence records are deterministically ordered for projection update by the canonical position of their triggering `TaskVerificationConsensus` event. Canonical position is `(CausalTimestamp, EventID lex order)` per `internal/event/event.go:270-278` (Lamport timestamp + EventID tiebreak).

This ordering is independent of node-local arrival order. Two nodes processing the same set of TVConsensus events apply the same evidence updates in the same order, producing byte-identical `ReputationAggregate` state at every canonical position.

### 6.4 Exclusion rules

Per locked plan CR-1 + CR-4: only canonical TVConsensus events drive evidence writes. Implicit exclusions:

- **Malformed TVConsensus events**: rejected at ingestion (canonical event validation); never reach the consumer.
- **TVConsensus events for rounds where the validator was not in seat at round-open**: the locked plan's evidence record includes `StakeBP` derived from validator-seat snapshot at round-open; a validator not in seat has `StakeBP = 0`. Whether this case produces an evidence record at all (vs. being skipped) is locked-plan-implementation detail; F5 references the locked plan's behavior.
- **Equivocation events**: separate canonical event type per locked plan §2.1 (`EquivocationEvents` field on the aggregate). **Equivocation canonical anchoring is OUT OF SCOPE for F5 5A.2** — the locked Reputation-and-Consensus-Integrity workstream §17 step 7 owns it. Today's state per §13.4 verification audit: equivocation detection IS wired (`aggregator.go:47-65`, `slashing.go:173-200`) and logged locally, but `RecordEquivocation` has zero production callers and `EventTypeSlashingChallenge` is defined but never instantiated — equivocation is canonically inert. F5 5A.2 references the locked plan's equivocation evidence definition; the locked workstream wires it.
- **Reversed TVConsensus events**: per CR-2, evidence write is reversed with the consensus event via standard rollback mechanism. Evidence and settlement are siblings on the same canonical moment.

### 6.5 Idempotency under replay (the key correctness improvement)

The 5A.1 audit identified the existing `ValidatorReputationStore.RecordVote` as **non-idempotent under replay** because it keys writes by `(validator, family, category)` and increments counters. Replaying the same vote double-counts.

The canonical evidence record fixes this by including `RoundID` in the primary key (`"rep:" + EpochIndex + ":" + ValidatorID + ":" + RoundID` per locked plan §2.1). A second write for the same round is a no-op (idempotent BadgerDB upsert).

**This is the key correctness improvement F5 ratifies**: evidence is keyed canonically; replay produces byte-identical state.

### 6.6 What the F5 canonical W lookup consumes

F5's `CanonicalWProjection.Lookup(validator, family, category, contributor, escrowBudget, epoch)` (per §0.8) reads from the locked plan's `ReputationAggregate` cache. The aggregate is a strict projection of the evidence base (locked plan CA-3); given the same epoch (snapshot anchor per §11), every node returns byte-identical aggregate values, and W is computed from those aggregates per locked plan §4.1.

§11 specifies how the settlement cutoff (epoch index) maps to a specific aggregate snapshot.

## 7. Version-binding rule (schema/gate)

### 7.1 Canonical-position-bound invariant (LOAD-BEARING)

**Invariant V-1**: For any round R, the W implementation used in settlement derivation is selected by R's canonical ordering relative to the relevant activation event, NOT by the node's current runtime activation state at settler execution time.

- Rounds canonically before `ReputationActivation` use `NeutralBPStubW`.
- Rounds canonically at or after `ReputationActivation` use real W.
- The selection is a pure function of canonical state.

This invariant is the load-bearing precision discipline both Grok and ChatGPT independently identified at Gate 5A.2 multi-AI review. The naive alternative — "swap the implementation behind the interface when the activation event is admitted, then the next `Lookup` returns the new value" — looks atomic per-node but is execution-timing-dependent: if a settler is mid-execution when activation is admitted, or if a round is being settled on a node that admitted activation but at a different wall-clock moment than another node, the W value selected for the same round can differ across nodes. V-1 forbids this by binding the selection to the round's canonical position, not the local runtime state.

Two activation events relevant to F5:

1. **F5 activation**: F5's `CanonicalWProjection` interface lands. Pre-activation settlement reads via the existing `ValidatorQScoreFn`; post-activation reads via `CanonicalWProjection.Lookup` (stub). Today's effective behavior is NeutralBP regardless (existing `RecordVote` has zero production callers); F5 activation is a structural cutover (interface boundary, derivation purity), not a behavioral one.

2. **Reputation activation**: the locked workstream's `ReputationActivation` canonical event. Pre-activation: stub. Post-activation: real W. Behavioral cutover at this point — validator payouts begin reflecting agreement-rate-derived tiers.

V-1 applies to both cutovers; the analysis below focuses on `ReputationActivation` as the behaviorally substantive case.

### 7.2 Enforcement mechanism — canonical ancestor check

F5's derivation function determines W implementation by checking whether `ReputationActivation` is a canonical ancestor (or equivalent causal predecessor) of R's canonical settlement context. The check is a pure canonical-state function, not a runtime flag read.

Pseudocode:

```
derive_payouts(round R) → PayoutRecord:
    use_real_w, err := canonical_ancestor(
        ReputationActivation_event_id,
        R.canonical_seal_context,
    )
    if err == ErrEventNotFound:
        // Materialization-lag: either ReputationActivation or R's canonical
        // seal context is not yet locally materialized. V-1 forbids returning
        // false (would couple selection to local materialization state, not
        // canonical position). V-1 forbids returning a guessed value (would
        // produce wrong-canonical-state result). The only V-1-preserving
        // semantic is to DEFER R's settlement until materialization completes.
        // See 5A.3 (generation-ledger-canonical-derivation.md) §spec-6 for
        // detailed deferral semantics. Reuses the F3-B causal-prerequisite-
        // gating pattern (D-1 through D-8) as precedent.
        defer_round_settlement(R)
        return
    if err != nil:
        return propagate_error(err)
    w_impl := real_w if use_real_w else NeutralBPStubW
    for each agreeing_validator v:
        w_v := w_impl.Lookup(v, family, R.Category, R.PosterID, R.EscrowBudget, cutoff_epoch_for(R))
    ... compute payouts using w_v values ...
```

`canonical_ancestor(A, R)` is implemented via `IsAncestor(A, R.canonical_seal_context)` on the consolidated DAG anchor reader (locked to `internal/dag/AnchorReader` per 5A.3 consolidation per architect direction; supersedes the prior `dispatch.DAGAnchorReader` and `settlement.DAGAncestorReader` — see 5A.3 spec 6). Both nodes computing `canonical_ancestor(ReputationActivation, R)` for the same R produce the same boolean — the answer depends only on canonical DAG state at R's seal context, not on the node's current admission state.

**What "canonical settlement context" means** — explicit definition per architect direction:

> **R.canonical_seal_context = R's canonical position in the DAG (R's canonical event ID).** Specifically, the canonical event identifier of the TVConsensus event that finalizes R, derived from R's own canonical state. NOT a per-admission marker.
>
> **NOTE — implementers MUST NOT use `AdmissionRecord.DAGAnchor` for this check.** That field is per-node per-admission (set as `d.currentAnchor()` = `tips[0]` lex-sorted at admission reservation time per `internal/dispatch/dispatcher.go:326` and `logical_key_admit.go:196`). It is C-15 non-canonical node-local state — different on different nodes for the same round. Using it for the V-1 check would reintroduce the execution-timing-dependent selection V-1 explicitly forbids. The correct primitive is R's own canonical event ID; the activation-ancestor check operates over canonical DAG positions, not over per-admission anchors.
>
> **NOTE (5A.2 v2.2 — Gate 5A.3 round-2 cross-doc terminology precision per ChatGPT Finding 1)**: `R.canonical_seal_context` is **R's TVConsensus finalization event**, NOT `R.SubmissionEventID`. The latter is used by 5A.3 gen-ledger BFS as traversal root (per `docs/architecture/generation-ledger-canonical-derivation.md` §2.2). These are different canonical events serving different purposes: `R.canonical_seal_context` (V-1 activation-ancestor check, this document) vs `R.SubmissionEventID` (gen-ledger BFS traversal root, 5A.3). Implementers working across F5 5A.2 and 5A.3 MUST use the correct handle for each path.

This warning is load-bearing: future implementers may reach for `AdmissionRecord.DAGAnchor` because it is the existing "anchor" field on admission records, but the V-1 check requires canonical-position semantics that the per-admission anchor explicitly does not provide. Similarly, confusing `canonical_seal_context` with `SubmissionEventID` would either make V-1 selection depend on a non-canonical-seal point, or make BFS root the wrong anchor for ancestry traversal.

**No runtime flag**: the F5 derivation package does NOT maintain a boolean like `reputationActivated bool` set by a consumer at admission time. Such a flag would be queried at settler execution time, opening the door to execution-timing-dependent selection. V-1 forbids this; the implementation choice is enforced by NOT having a flag at all.

### 7.3 Emission ordering constraint (Grok's finding)

`ReputationActivation` cannot be emitted until F5 activation has been emitted. Two enforcement mechanisms (defense-in-depth):

1. **Workstream sequencing**: the locked Reputation-and-Consensus-Integrity workstream's `ReputationActivation` emission is gated on F5 activation having been previously emitted. This is a procedural guard (operator + workstream-author discipline).

2. **Admission gate**: `ReputationActivation` consumer checks that F5 activation is already admitted on this node (causal-prerequisite gating, same pattern as F3-B D-1 through D-8). If not, `ReputationActivation` is held pending F5 activation admission. This is a structural guard at the consumer layer; it cannot be bypassed by an out-of-order canonical emission.

The admission gate is the load-bearing protection. Workstream sequencing is a soft prerequisite that catches the misordering at design time; the admission gate catches it at runtime if the procedural discipline fails. Both should be in place.

### 7.4 Replay / catch-up semantic

A node replaying canonical events from genesis (or from a snapshot) encounters F5 activation, then `ReputationActivation`, then subsequent rounds. For each round R encountered, the stub-vs-real selection is determined by the round's canonical position relative to the already-processed activation event — exactly the V-1 invariant.

Concretely: replay processes events in canonical order. When the replayer reaches a TVConsensus event for round R, it asks `canonical_ancestor(ReputationActivation, R.canonical_seal_context)`. Since the replayer has already processed `ReputationActivation` (canonical order guarantees it), the ancestor check resolves to true for any R that came after `ReputationActivation` in canonical order.

**Replay produces byte-identical selections to steady-state execution** because both compute the same canonical ancestor relationship. This is the V-1 corollary: replay-invariance is automatic when selection is canonical-state-only.

If a node replays in a different order (e.g., out-of-canonical-order event delivery during catch-up), the ancestor check still resolves correctly because `IsAncestor` is a pure function of the DAG subgraph, not of admission order. The replayer may need to wait until `ReputationActivation` is materialized before computing the ancestor check for any round canonically-after-it; this is bootstrap sequencing per §8.

### 7.5 Schema/version naming

The interface includes a `DerivationVersion` concept inherited from locked plan §2.1 (field on `ReputationEvidence`). F5's `CanonicalWProjection` does not need to expose `DerivationVersion` in its `Lookup` signature — the version is implicit in which W implementation the canonical-ancestor check selected. Two nodes selecting different post-activation implementations for the same round indicate a consensus fault (one node has DAG state the other doesn't), not a runtime ambiguity to handle.

### 7.6 Mixed-version cluster rejection

If a node's binary pre-dates the F5 activation logic (no `CanonicalWProjection` interface support) or pre-dates the locked workstream's real-W implementation, that node cannot participate in post-activation rounds — its derivation function cannot perform the ancestor check or cannot construct the post-activation W implementation. This is the standard canonical-event-admission failure mode; V-1 doesn't change it.

The integer-migration workstream's "shadow mode then activation" pattern is a GOOD reference but **not strictly required** for the F5 stub→real swap because the stub's behavior (always NeutralBP) is equivalent to today's effective production behavior (per Grok's verification: `TotalVotes` always 0 → `qScoreFn` always returns `NeutralBP` → no observable difference). Shadow-mode-equivalent comparison would compare stub-NeutralBP against stub-NeutralBP — no signal. The activation IS the cutover; no shadow window needed.

### 7.7 Gate event payload contents

The F5 activation event payload should include:
- F5 derivation version (uint32, monotone).
- Reference to the F5 `CanonicalWProjection` interface version (a major-version on the interface itself, locked at 1 for F5 ship).

The `ReputationActivation` event payload should include:
- Locked plan's W implementation version (uint32, monotone).
- Reference to the same F5 interface version (must match the F5 activation's interface version, otherwise nodes report version-mismatch error at activation admission).

The payload is consulted at admission time to verify version compatibility. After admission, the canonical-ancestor check at `derive_payouts` time uses the EVENT'S CANONICAL POSITION, not the payload contents — the payload is metadata for compatibility checking; the load-bearing data is the event's existence in canonical history.

## 8. Bootstrap / recovery behavior (operational)

### 8.1 Three bootstrap scenarios per F5 ship

**8.1.1 Fresh-genesis node startup (post-F5, pre-Reputation-activation)**
- Node loads on-disk state from genesis.
- F5 activation has occurred at some past canonical position; the node admits all canonical events including F5 activation.
- After admission, `CanonicalWProjection` is wired to `NeutralBPStubW`.
- All `Lookup` calls return NeutralBP. No projection state to bootstrap.

**8.1.2 Restart with on-disk state intact (post-F5, pre-Reputation-activation)**
- Same as 8.1.1 but with persisted state already on disk.
- The stub has no persisted state to restore (it's a constant function).
- Restart is O(1) for the W stub.

**8.1.3 Post-Reputation-activation bootstrap**
- Locked workstream's bootstrap behavior kicks in (locked plan §5 snapshot framework).
- Node loads most recent on-disk snapshot of the aggregate cache; replays canonical evidence events from snapshot to current canonical anchor.
- Per locked plan §5.2 SN-2: `snapshot(epoch E) + forward_apply(epoch E events) = full_replay(epoch 0 to E)`.
- F5's role: pass cutoff_epoch correctly (per item 11) to the now-active real W.

### 8.2 Bootstrap inheritance from locked workstream

F5 5A.2 does not specify bootstrap behavior for the W projection itself — that's locked plan §5. F5 inherits:
- Snapshot-restore-from-disk on restart.
- Forward-apply from snapshot to current canonical position.
- Genesis replay as last resort (reserved for fresh-genesis or disaster recovery).

### 8.3 Stub-to-real transition is not a bootstrap event

When `ReputationActivation` is admitted, the stub is replaced by real W. This is NOT a bootstrap operation — it's an in-flight implementation swap. The locked workstream's bootstrap framework activates at the same moment to populate the aggregate cache from canonical evidence retrospectively, but this is an internal locked-workstream concern.

F5's only concern at activation: the next `Lookup` call returns from real W, not stub. Verification per §0.8.5.

### 8.4 No bootstrap dependency on un-canonicalized subsystems

Halt-trigger #4 from architect direction ("Bootstrap/recovery reveals a dependency on a subsystem not yet canonicalized") evaluated:
- Stub-W has no dependencies. Trivially canonical (constant function).
- Real-W depends on the locked plan's evidence base, aggregate cache, snapshot framework — all of which the locked workstream canonicalizes within its scope.

**Halt-trigger #4 not fired.** Under option (b) sequencing, F5 ships with stub (no dependencies); locked workstream ships real W with its own self-contained canonical primitives.

## 9. Absence-of-data policy (operational)

### 9.1 Stub-W absence-of-data behavior

NeutralBPStubW returns NeutralBP (10000) for all queries unconditionally. Absence-of-data cases are vacuous because the stub doesn't read any data:
- New validator with no evidence → NeutralBP.
- Validator with evidence in some categories but not the queried one → NeutralBP.
- Tie or zero-total-weight at round level → handled by `protocolmath.AllocateWithCeiling` downstream (already 5A.1 covered).
- Corrupted/missing evidence → vacuous (stub doesn't read evidence).
- Query at an epoch before validator was seated → NeutralBP.

This is consistent with today's effective production behavior (always NeutralBP).

### 9.2 Real-W absence-of-data behavior (locked workstream-defined)

When real W is active per the locked plan's cold-start states (§6):

| Validator state at queried epoch | W return |
|---|---|
| Unseated (not in active validator set) | error or zero (locked plan implementation choice) |
| Bootstrap (TotalEvidence < 20) | T0 (7000) per locked plan §4.1 cold-start override |
| Probation (20 ≤ TotalEvidence < 80) | Earned tier capped at T2 (10000) per locked plan §6.2 |
| Mature (TotalEvidence ≥ 80) | Earned tier in [T0, T4] per agreement_bp |

| Other absence cases | W return |
|---|---|
| New validator with no evidence in queried (family, category) | T0 (Bootstrap state by §6.1; new validators have TotalEvidence = 0) |
| Evidence in some categories but not queried | T0 if TotalEvidence < 20 in queried (family, category) |
| Tie or zero-weight at round level | Handled downstream by allocator (out of scope for W) |
| Corrupted/missing evidence | Locked workstream's error path; F5 inherits |
| Query at epoch before validator seated | locked workstream defines (likely error) |
| `contributor` not in active validator set | No error — `contributor` is opaque context parameter (round.PosterID, not necessarily a validator). Per §0.8.3 + §0.8.7. |
| `family` or `category` outside protocol-defined enum range (per locked plan CR-6: 16 families × 64 categories) | Deterministic error return per §0.8.7 invalid-input contract. NOT implementation choice. |
| `epoch` earlier than first retained snapshot (per locked plan §5.5: 8-epoch retention) | Behavior MUST be specified per §0.8.7: either deterministic error return OR fall through to NeutralBP base case. Choice is locked workstream's; the choice itself must be explicit and documented. |
| `epoch == 0` / first protocol epoch (no prior epoch's snapshot exists) | Base-case override per §0.8.7: return NeutralBP (matches Bootstrap-state convention). |

F5 5A.2's `CanonicalWProjection.Lookup` returns `(uint64, error)`. Errors from the implementation propagate to F5's derivation function; derivation function decides handling (likely: validator excluded from payouts; pool routed to treasury, mirroring 5A.1's existing "no agreeing validators" handling at `verification_consensus_settler.go:217-220`).

### 9.3 Determinism under absence

All absence cases produce deterministic returns from locked W. Two nodes querying the same (validator, family, category, contributor, escrowBudget, epoch) under the same canonical state return the same value or the same error. Cluster-uniformity holds.

## 10. Interaction with generation-ledger quality (deferred to 5A.3)

Generation-ledger ancestor quality (`qualityFn` in `internal/settlement/generation_ledger_calculator.go:319`, currently a neutral stub at `cmd/node/main.go:1937`) is OUT OF SCOPE for F5 5A.2. The W projection is per-validator (vote-agreement); generation-ledger quality is per-ancestor-event (task-execution-quality). Different metric, different evidence domain.

Generation-ledger quality canonicalization is owned by Plan v3 §3.3 (5A.3) per its specification 5 ("Quality function canonicalization: `qualityFn` is currently neutral (deferred). When real quality is wired, must be canonical-live with specified retrieval mode. This depends on Q-C (canonical Q projection) being in place — quality is Q-weighted."). 5A.3 will determine whether gen-ledger quality reuses `CanonicalWProjection`, defines a sibling projection, or computes a derived view.

F5 5A.2's interface inventory has exactly one interface: `CanonicalWProjection` (§0.8). Any gen-ledger quality interface is 5A.3's deliverable.

## 11. Cutoff anchor precision (operational)

### 11.1 Existing anchor primitives in the codebase (Agent 2 research)

Three candidate anchor primitives evaluated against the design requirements (deterministic across nodes; available at Q-lookup time; stable from IsComplete-true through Apply; uniquely determined by canonical state):

| Candidate | Source | Deterministic? | Available pre-Apply? | Stable IsComplete→Apply? | Verdict |
|---|---|:-:|:-:|:-:|---|
| (a) Dispatcher's `currentAnchor()` at IsComplete moment | `internal/dispatch/dispatcher.go:237` (`tips[0]`) | CONDITIONAL — lex-sorted but wall-clock-coupled | YES | NO — varies per node's wall-clock arrival | REJECTED — wall-clock coupling violates Input-Domain-3 |
| (b) The TaskVerificationConsensus event ID itself | Canonical event in DAG | YES — same for all nodes | NO — emitted post-seal, after Apply runs | YES once emitted | RISKY — late availability for Q lookup |
| (c) The lex-greatest VerificationVote.EventID that triggered seal | `round.Votes` deterministic-sort + scan for seal-trigger | YES — pure function of canonical vote set | YES — votes loaded before IsComplete | YES — votes immutable in DAG | **VIABLE** — no schema change needed |
| (d) Epoch boundary identifier | Locked plan §5.4 (1000 rounds/epoch); deterministic from canonical round counter | YES — function of canonical round count | YES — boundaries are deterministic | YES — boundaries are immutable canonical positions | **CHOSEN** — see §11.2 |

### 11.2 Choice: epoch-coarse anchor matching the locked W projection's API

The locked Reputation-and-Consensus-Integrity workstream §4.1 W function takes `epoch` as input, not a sub-epoch anchor. W is computed at epoch boundaries via the snapshot framework (§5.2). All rounds within an epoch see the same W value.

For F5 5A.2, the cutoff anchor is therefore **epoch-coarse, not round-precise**:

```
cutoff_anchor_for(round R) = epoch index of R's parent (i.e., the most-recently-completed epoch before R's epoch)
```

Or equivalently: when round R settles within epoch E, the W lookup uses the snapshot at the boundary between epoch E-1 and epoch E. All rounds in epoch E use the same W. Cross-node-uniform by construction (epoch boundaries are deterministic from canonical round count).

### 11.3 Why epoch-coarse vs round-precise

Round-precise (candidate c) was the F5 plan v3 working assumption: each round R has its own anchor, derived from R's vote set. This would give W per-round freshness — evidence written in round R-1 is reflected in W at round R.

But the locked plan's W function operates at epoch boundary because the snapshot framework optimizes for that granularity. Round-precise W would require either:
- Re-computing the aggregate at every round (expensive — O(retained-window evidence) per round), OR
- Maintaining a separate per-round projection alongside the epoch-coarse one (storage and consistency burden)

Neither is justified by F5's needs. The settler reads W to weight payouts; epoch-coarse freshness is sufficient for fee-distribution fairness. Round-precise freshness would advantage validators who voted just before the round seals (their W reflects their just-counted vote) over validators who voted earlier (their W doesn't), which is not a desirable semantic anyway.

**Decision**: cutoff anchor is epoch-coarse, matching the locked W projection's `epoch` parameter directly. F5's `Lookup(validator, category, anchor)` API takes the epoch index as the `anchor` argument.

This is a design simplification — the locked plan already does the work; F5 just specifies the cutoff binding rule.

### 11.4 Cutoff is uniquely determined by canonical state

For round R, the cutoff anchor (epoch index) is computed as:
```
cutoff_anchor(R) = epoch_of(R) - 1  // OR epoch_of(R), depending on snapshot timing convention
```
where `epoch_of(R) = ⌊R.RoundCounter / RoundsPerEpoch⌋` per locked plan §5.4 (RoundsPerEpoch = 1000 locked).

`R.RoundCounter` is deterministic from canonical state (it's the monotone canonical round number, derivable from the canonical TVConsensus event stream). Two nodes evaluating cutoff_anchor(R) for the same R produce the same value.

### 11.5 Snapshot timing convention (verified against locked plan §5.2)

The locked plan §5.2 states: "A snapshot is a deterministic serialization of the aggregate cache at an epoch boundary, derived from already-finalized DAG state." This pins the convention: the snapshot for epoch E is taken at the **end of epoch E** (after E's TVConsensus events have been finalized), and is queried during epoch E+1 onwards.

For F5 5A.2's settlement cutoff:
```
cutoff_epoch_for(round R) = epoch_of(R) - 1
```
where `epoch_of(R) = ⌊R.RoundCounter / RoundsPerEpoch⌋`. Round R settling in epoch E uses snapshot(E-1).

This satisfies the architect's §13.2 fallback ("if locked plan is silent: start-of-E"; equivalently end-of-(E-1)). The locked plan IS specified, and §5.2's "epoch boundary, derived from already-finalized DAG state" maps to "end of epoch E". The two conventions converge.

**§13.2 RESOLVED** (verified, not deferred): cutoff_epoch = epoch_of(R) - 1, equivalently the snapshot taken at end of the immediately-prior epoch. F5 5A.2 adopts the locked plan's convention.

### 11.6 First-round-of-epoch boundary case

When round R is the FIRST round of a new epoch, two convention edge cases arise:
- Snapshot at end of previous epoch may not yet be persisted on every node (race between snapshot generation and round seal).
- Validators who joined just before R may have no evidence in the previous epoch's snapshot.

Both cases are handled by the locked plan's existing semantics (snapshot generation is deterministic from canonical state per SN-1; new validators get NeutralBP per cold-start §6). F5 inherits these.

### 11.7 Available at Q-lookup time

Cutoff_anchor(R) is computable from R alone (specifically R.RoundCounter or equivalent). Settler computes it once per round before invoking Lookup for each validator. Cost: O(1).

The snapshot at the cutoff anchor must be persisted locally on the querying node. Per locked plan §5.4, snapshots persist at epoch boundaries; under steady-state operation they're available before any round in the next epoch seals. Bootstrap behavior (item 8) handles the case where the snapshot is not yet locally available.

### 11.8 Stable from IsComplete-true through Apply

cutoff_anchor(R) depends only on R's epoch index, which is fixed at round creation. The value cannot change between IsComplete-true and Apply.

### 11.9 Why this is a simplification, not a compromise

Plan v3 §3.2 deliverable item 11 asks to "formally define the relationship between round-seal and anchor selection; prove that the anchor is uniquely determined by canonical state." This section delivers:
- Anchor = epoch index of R, deterministic from canonical state.
- Uniquely determined by R's epoch (which is uniquely determined by R's canonical position).
- No schema change to `TaskVerificationRound` required (Agent 2's "missing SealAnchor field" finding is moot under epoch-coarse semantics).
- No new derivation logic at lookup time (settler computes epoch index trivially).

The Plan v3 working assumption was round-precise; the locked plan's W is epoch-coarse; F5 5A.2 chooses epoch-coarse for clean integration. Surfaced as open question §13.3 for architect confirmation that this simplification is acceptable.

## 12. Scaling target (operational)

### 12.1 Stub-W scaling

`NeutralBPStubW.Lookup` is a constant return. Cost: O(1). Trivially exceeds the target. No benchmarking required.

### 12.2 Real-W scaling target (locked workstream-owned)

Per architect direction (Gate 5A.1 closure, Grok addition): **p99 lookup < 100µs at 10k QPS sustained with hot-validator cache**.

This target applies to real W. The implementation (locked workstream §17 step 4 — `internal/reputation/` package) owns achieving it. F5 5A.2 specifies the target as a verification gate; F5 5D includes the benchmark.

### 12.3 Verification methodology (5D)

5D verification harness includes a Q-C performance gate per Gate 5A.1 closure forward-note:

- **Workload**: 10k `Lookup` calls per second sustained for 60 seconds; randomly distributed over a working set of recent validators (hot-cache scenario).
- **Measurement**: p99 latency over the 60-second window.
- **Pass condition**: p99 < 100µs.
- **Failure handling**: halt at Gate 5D; locked workstream investigates (cache sizing, lookup path optimization, snapshot encoding).

**Cache requirement (Grok finding, Gate 5A.2 multi-AI review)**: implementations MUST use a bounded LRU cache keyed on `(validator, family, category, epoch)` sized to 2-4× active validator count. The bound prevents unbounded cache growth under adversarial query patterns; the lower bound (2× active validators) ensures sufficient working-set coverage for the hot path; the upper bound (4× active validators) caps memory consumption. Eviction policy is LRU; warm-up on cold start populates from the most recent snapshot's contents.

Stub-W trivially passes (O(1) constant return; no cache needed). Real-W must pass when shipped.

### 12.4 Cold-path latency

Hot path (cache hit) is the load-bearing case for settlement (active-validator queries against recent snapshots). Cold-path queries (cache miss for old historical epochs) are not on the settlement hot path; their latency is non-blocking. Per §0.8.4 retention-window note, deep historical queries (epochs older than 8-epoch retention) may require degraded-precision handling for trailing-N-round-median computations; that's a locked workstream concern.

---

## 13. Open questions surfaced during 5A.2 design (new — not in original 12 items)

Per architect direction: questions that don't fit the 12 items cleanly are surfaced here rather than forced into existing items.

### §13.1 Integration with locked Reputation-and-Consensus-Integrity workstream

**Discovery**: F5 5A.2's "canonical Q projection" overlaps substantially with the locked `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` W projection (§4.1). The locked plan deletes the existing advisory store, defines `ReputationEvidence` as the canonical record, and replaces `ValidatorQScoreFn` with the W projection.

**Question**: Is F5 5A.2 designing:
- **(A)** an INDEPENDENT canonical Q projection separate from W (duplication; would need explicit justification), OR
- **(B)** the anchor-precise historical-read EXTENSION on top of the locked W projection (this design's working position; collapses several deliverable items into "reference the locked plan").

**Working position**: (B). Document drafted under this assumption. If architect confirms (B), items 4, 5, 6, 8, 11 collapse to thin "F5 references the locked plan" sections; F5's primary contribution is the historical-read API, anchor-precision specification, and version-binding rule. If architect directs (A), substantial redesign required; the duplication needs justification.

**Architect decision requested.**

### §13.2 Snapshot timing convention — RESOLVED (verified against locked plan)

Locked plan §5.2 specifies the convention: "snapshot is a deterministic serialization of the aggregate cache at an epoch boundary, derived from already-finalized DAG state." Snapshot for epoch E is taken at end of E; queried during E+1 onwards.

F5 5A.2 cutoff: `cutoff_epoch(R) = epoch_of(R) - 1`. Documented in §11.5.

### §13.3 Epoch-coarse vs round-precise cutoff — RESOLVED (W update semantics verified)

**Architect's verification question** (§13.3): "Does W update continuously as evidence arrives, or only at epoch-snapshot moments? If only at snapshots: epoch-coarse cutoff is correct (round-precise reads same value anyway)."

**Verification** (against locked plan §3, §4.1, §5):
- The underlying `ReputationAggregate` cache **updates continuously** as canonical TVConsensus events arrive (CR-1: write happens at consensus finalization, in the consumer invocation that fires settlement).
- The aggregate cache is "a strict projection of the evidence stream" (CA-3) and "rebuildable from evidence deterministically at any time."
- The W function (§4.1) takes `epoch` as input. The W computation reads from the snapshot indexed by that epoch, NOT from the live current aggregate. Snapshots are taken at epoch boundaries (§5.2) and are immutable post-creation (SN-1 makes the snapshot root deterministic).
- All rounds within the same epoch query the SAME snapshot (the most recent prior snapshot at end of the previous epoch) → all rounds within an epoch see the SAME W value for a given (validator, family, category).

**Conclusion**: case (3) confirmed. Round-precise reads would return the same value as epoch-coarse reads anyway because the snapshot is the same. **Epoch-coarse cutoff is the correct answer**, not a compromise.

The aggregate updating continuously vs queryable W changing at epoch boundaries is the right pattern for cluster-uniformity: continuous evidence accumulation gives the freshest possible aggregate at each snapshot moment, while epoch-bounded query stability ensures all nodes process the same epoch's settlements with the same W values.

**§13.3 ANSWER**: epoch-coarse, matching locked W's `epoch` parameter. Documented in §11.

### §13.4 Equivocation evidence write boundary — RESOLVED (verification audit complete)

**Verification audit findings** (2026-04-23, background agent):

| Layer | Status | Evidence |
|---|:-:|---|
| Detection (vote-aggregation level) | ✓ wired | `internal/taskverification/aggregator.go:47-65` (`ApplyVoteToRound`) |
| Detection (slashing-evaluator level on finalized round) | ✓ wired | `internal/taskverification/slashing.go:173-200` (`detectEquivocators`) |
| Local logging on detection | ✓ wired | `internal/recognition/task_verification_vote_consumer.go:163` (WARN); `internal/recognition/task_verification_consensus_consumer.go:174-181` (INFO) |
| `RecordEquivocation` production callers | ✗ NONE | `grep -r RecordEquivocation` — zero hits in `internal/`/`cmd/` outside tests |
| `EventTypeSlashingChallenge` producer | ✗ NONE | Type defined at `internal/event/event.go:109-112`; never instantiated |
| `EventTypeSlashingChallenge` consumer | ✗ NONE | No consumer exists |
| Canonical cross-node anchoring | ✗ NONE | No DAG event emitted; equivocation is purely node-local-ephemeral |
| Integration tests for cross-node propagation | ✗ NONE | Only unit tests at the detection layer |

**Assessment**: equivocation handling is **INERT**, not merely dormant. Detection fires and logs, but the chain from detection to canonical write is severed at multiple points. This is a stronger version of the `RecordVote`-is-unwired finding from 5A.1: not only is the write unreachable, the canonical event type that would carry it cross-node has no producer either.

**Conclusion for F5 5A.2**: equivocation evidence is **OUT OF SCOPE for F5 5A.2**. The locked workstream owns equivocation canonical anchoring per its §17 step 7 ("Rewrite SlashingEvaluator to ... preserve automatic equivocation slash, fix map iteration determinism in detectEquivocators"). F5 5A.2 references the locked plan's `EquivocationEvents` aggregate field per §6.4 of this document, but does NOT specify the producer/consumer/canonical path for equivocation evidence. That's the locked workstream's responsibility.

**§13.4 ANSWER**: equivocation is inert today; locked workstream owns canonical anchoring; F5 5A.2 references but does not specify. Documented in updated §6.4.

### §13.5 F5 vs locked workstream sequencing — NEW

**Discovery**: the locked Reputation-and-Consensus-Integrity workstream §17 step 3 deletes `internal/taskverification/reputation.go`, and step 5 rewrites `VerificationConsensusSettler.distributeByQuality` to use the new `ValidatorIndependenceWeightFn`. F5's settlement-derivation work consumes the W projection that emerges from these steps. **F5 cannot ship before the locked workstream because the W projection it consumes does not yet exist.**

Three sequencing options:

- **(a) Locked workstream ships first; F5 consumes W as a done thing**. Cleanest. F5 5B can integrate against a real W implementation. Disadvantage: F5 blocks until the locked workstream ships, including its 25 success criteria (§15). Some are testnet-dependent (steps 16-20).

- **(b) F5 ships first with a temporary W stub; locked workstream swaps the stub for the real W when it lands**. Allows F5 progress in parallel. Disadvantage: F5's stub is a temporary canonical input — violates F5's own derivation-purity invariant unless the stub is itself canonical (e.g., always returns NeutralBP, which is the today-effective behavior since the existing reputation store has zero production writers anyway). May be feasible: F5 ships with W = NeutralBP-always, the locked workstream replaces the stub later, F5's derivation function is unchanged.

- **(c) F5 and the locked workstream merge as one combined workstream**. Single shipping unit. Disadvantage: large scope (locked workstream alone has 25 success criteria + 14 implementation steps); combined would be substantially larger. Advantage: avoids the F5↔locked-workstream interface coordination entirely.

**§13.5 RESOLVED**: option (b) — F5 ships with `NeutralBPStubW`; locked reputation workstream swaps in real W via canonical `ReputationActivation` event. (Architect decision recorded 2026-04-23.)

Rationale documented:
- F5 ships in parallel with the locked workstream rather than blocking on its 14-step + 25-success-criteria completion.
- Stub matches today's effective production behavior (existing `ValidatorReputationStore.RecordVote` has zero production callers per 5A.1 audit; today's `ValidatorQScoreFn` always returns NeutralBP because TotalVotes is 0). The stub makes this explicit.
- F5's derivation function calls `CanonicalWProjection.Lookup` identically pre- and post-real-W landing. The swap is transparent to derivation.
- Halt-trigger #4 ("Bootstrap/recovery reveals a dependency on a subsystem not yet canonicalized") does NOT fire because the stub has no dependencies; under option (b), F5 ships with a self-contained constant-function implementation.
- Stub-to-real swap verification is specified in §0.8.5 (5D test scenario).

Implementation specifics:
- F5 5A.2 design includes `CanonicalWProjection` as the interface contract (§0.8). Locked plan §4.1 is the specification F5 consumes.
- F5 Phase 5B ships `NeutralBPStubW` (§0.8.6).
- Cutover matrix: `validator_q_score` is HIGH risk at F5 activation (stub ships) AND HIGH risk at locked workstream's `ReputationActivation` (real W replaces stub). Both cutovers are canonical-activation-gated per the integer-migration pattern (§7.2).
- The locked reputation workstream ships real W behind the same interface. Per §7.1 V-1 invariant: W implementation selection for round R is bound to R's canonical position relative to `ReputationActivation`, NOT to runtime activation state. The "swap" is a canonical-state property of each round, not a per-node atomic flag flip.
- F5's canonical derivation function reads W via the interface; selection between stub and real W happens by canonical-ancestor check on the activation event (§7.2). The function does not change when real W lands; the check itself does not change either — only its return value does, deterministically per round's canonical position.

## 14. Halt-trigger assessment

Five triggers per Gate 5A.1 closure architect direction, evaluated against the completed draft:

| Trigger | Fired? | Rationale |
|---|:-:|---|
| Q-C structural blocker (advisory projection inseparable, cutoff undefinable) | NO | Locked workstream Decision 2 deletes the advisory store entirely. Cutoff is epoch-coarse per §11 (verified §13.3); locked plan §5 supplies the snapshot framework. |
| Storage/replay cannot meet scaling target | NO | Stub-W trivially passes (O(1) constant). Real-W's scaling target (§12) is a locked workstream verification gate, not an F5 design blocker. |
| Version-binding requires F6 pulled forward | NO | Per §7, version-binding uses canonical-activation-event pattern (mirrors IntegerMigrationActivation). Self-contained within F5 + locked workstream coordination. F6 (canonical emission) not required. |
| Bootstrap/recovery dependency on uncanonicalized subsystem | NO | Per §8.4: stub has no dependencies; real-W depends only on the locked plan's own primitives (evidence base, aggregate cache, snapshot framework — all canonicalized within locked workstream). |
| Architectural decision compromising F4/F3-B locked invariants | NO | F4 invariants (C-3', C-17, C-14 extension, §4.5 atomic-batch forward-only) are not touched. F3-B invariants are not touched. F5's contribution composes additively with all of them. |

**No halt-trigger fired. Draft complete; ready for Gate 5A.2 review.**

### 14.1 New open question status

| Question | Status |
|---|---|
| §13.1 F5 5A.2 = extension to locked W vs separate projection | RESOLVED — extension (architect direction) |
| §13.2 Snapshot timing convention | RESOLVED — verified against locked plan §5.2 (end-of-E convention) |
| §13.3 Epoch-coarse vs round-precise cutoff | RESOLVED — epoch-coarse (W update semantics verified) |
| §13.4 Equivocation evidence write boundary | RESOLVED — verification audit confirms equivocation is INERT; out of F5 5A.2 scope |
| §13.5 F5 vs locked workstream sequencing | RESOLVED — option (b) F5 ships with stub-W; locked workstream swaps via canonical activation (architect decision) |

All five open questions resolved. One ambiguity surfaced in §0.8.3 (`contributor` parameter semantic role); not a blocker for F5 ship; coordination with locked workstream author when real W lands.

## 15. Next step: Gate 5A.2 architect + multi-AI review

Per Plan v3 §12 step 4: "Gate 5A.2 — Architect + multi-AI review. Most important gate in 5A."

**This document is the Gate 5A.2 deliverable.** Per Plan v3 §3.2 Gate 5A.2 conditions:
- ✅ Q-C confirmed as design (alternative selection not needed; architect direction §13.1).
- ✅ Canonical Q projection specification complete: 12-item structure addressed.
- ✅ All 5 open questions resolved (§13.1-§13.5).
- ✅ Halt-trigger assessment complete; no trigger fired (§14).
- ⏳ Multi-AI review (Grok + ChatGPT) per same discipline as Gate 5A.1.

After multi-AI feedback absorbed:
- Gate 5A.2 closes.
- Phase 5A.3 begins (generation-ledger ancestry canonicalization with `DAGAncestorReader.ReadAtAnchor` design per Gate 5A.1 §9.3 architect decision).

### 15.1 Multi-AI review prompt structure (suggested)

Following the standing-instructions pattern from Gate 5A.1:

- **Grok**: push on ambition and scope. Is the F5 5A.2 design missing anything the locked workstream doesn't cover? Is option (b) sequencing actually safe under all scenarios (e.g., what if locked workstream's W lands materially later than F5's planned ship)? Is the `NeutralBPStubW` matching today's effective-NeutralBP behavior actually correct, or is there a hidden production W path I missed? Is the `contributor` parameter ambiguity (§0.8.3) a real blocker or just a coordination artifact?
- **ChatGPT**: push on structural correctness. Is the interface contract (§0.8.1) syntactically and semantically EXACTLY what locked plan §4.1 specifies? Are the 12 deliverable items addressed in dependency order? Is the version-binding rule (§7) coherent with the canonical-activation pattern? Are the absence-of-data cases (§9) exhaustively covered? Any mechanical gap in the F5/locked-workstream interface specification?

### 15.2 Coordination with locked workstream author

The `contributor` parameter ambiguity (§0.8.3) and the trailing-N-round-median outside-retention edge case (§0.8.4) should be raised with the locked Reputation-and-Consensus-Integrity workstream author when real W is being implemented. F5 5A.2's stub does not block on these resolutions; real W must address them.

### 15.3 Forward sequence

After Gate 5A.2 closes:
- 5A.3 — Generation-ledger ancestry canonicalization (`DAGAncestorReader.ReadAtAnchor` design).
- 5A.4 — Synthetic-ID refactor + payout artifact schema + CI lint + repo-wide consumer audit (16 callsites in test surface).
- 5A completion gate report.
- 5B plan v1 drafting (per Gate 5A.1 §9.3 architect decision: not a 5A success criterion; drafted after gate).

### 15.4 Forward notes carried out of 5A.2 (Grok multi-AI review predictions)

**For 5A.3 (Generation-ledger ancestry canonicalization)**:
1. **Materialization-lag edge case in full genesis replay**: a node replaying from genesis may not have all ancestors materialized at the moment it processes a generation-ledger settlement; `DAGAncestorReader.ReadAtAnchor` must define behavior for "anchor exists but some ancestors not yet materialized" (block until materialized? error? degraded result?). Likely answer: block with timeout; deterministic across nodes given canonical state will eventually catch up. Surface as 5A.3 design question.
2. **BFS traversal order MUST be explicitly lex-sorted on EventID**: child-visit order within each BFS hop must be deterministic. The locked plan §3 ordering (Lamport timestamp + EventID lex tiebreak) is the canonical sort key; 5A.3's traversal must apply it explicitly at each hop, not rely on map iteration order.
3. **First-round-of-epoch boundary race interaction with `ReadAtAnchor` cutoff**: a generation-ledger ancestor BFS for a round R settling in epoch E uses `ReadAtAnchor(cutoff_for(E))`. The cutoff anchor for E is the snapshot at end-of-(E-1). If the ancestor traversal needs events that are canonically positioned WITHIN epoch E (after the cutoff), the cutoff binding must be unambiguous — either traverse only ancestors at-or-before the cutoff, or define explicit semantics for crossing the boundary. 5A.3 must specify.

**For 5A.4 (Synthetic-ID refactor)**:
1. **Preimage uniqueness invariant** (Grok finding, aligns with Plan v3 Finding 6): the canonical payout record's content-hash preimage MUST include `(settlement_key.round_id, recipient.id, purpose.ordinal)` for global uniqueness. Per Plan v3 Finding 6: "settlement_key.round_id prevents cross-round collision; ordinal prevents within-settlement collision for the same recipient+role." 5A.4 must lock this in the payout artifact schema (`docs/architecture/payout-artifact-schema.yaml`) as a non-negotiable preimage requirement. A future refactor that shrinks the preimage and breaks uniqueness is a halt-worthy regression.

### 15.5 Meta-observation — the analogous hidden error pattern for 5A.2

The activation-semantics gap surfaced by Gate 5A.2 multi-AI review is the analogous hidden error pattern for 5A.2. ChatGPT named it precisely: **"semantic implementation selection by execution timing instead of canonical ordering."**

This is structurally the same shape as two prior errors caught in the F4/F5 lineage:

- **F4 mutex-claim error**: the StateApplied short-circuit was a POST-condition (Apply succeeded, mark applied) being mistaken for a PRE-condition serialization (no concurrent Apply for the same key). Multi-AI + plan-mode review caught it before F4B shipped at scale; F4C testnet then exposed the consequences for escrow.ReleaseSettlement.
- **5A.1 task.Status looseness**: idempotency-bounded harmlessness was accepted under architect-decision §9.2 option (b), with explicit reopen condition documented in the manifest if 5B discovers task.Status influences payout math (not just short-circuit). Reopen-condition discipline is the safety net for accepting the looser invariant.

**Common shape**: a per-node-state observation (mutex held / flag set / status terminal) is treated as if it implies a cluster-uniform canonical fact, when in reality the per-node observation is just per-node and the cluster-uniformity must come from the canonical ordering itself.

**5A.2's V-1 invariant explicitly forbids this pattern for W activation**: the W implementation for round R is selected by R's canonical ordering relative to the activation event, not by the node's runtime activation state at settler execution time. The fix is structural — there is no runtime flag; the selection is a pure function of canonical state.

Multi-AI review caught the gap before F5 ship. **This is the value of the multi-AI discipline**: ChatGPT and Grok each identified the gap from different framings (load-bearing invariant vs operational prerequisite); convergent identification is high-confidence signal that the gap is real.

**Process lesson for future workstreams**: any workstream that introduces a "swap implementation X for Y at activation event A" mechanism MUST specify whether the selection is canonical-position-bound (V-1 pattern) or runtime-flag-bound (the older, error-prone pattern). The default answer should be canonical-position-bound; runtime-flag-bound requires explicit justification.

This meta-observation will be carried into the F5 completion gate report and into future workstream-design templates.

---

**End of Q-Score Canonicalization Design — draft complete 2026-04-23. Updated 2026-04-23 post Gate 5A.2 multi-AI review (Grok + ChatGPT). Ready for architect final read.**
