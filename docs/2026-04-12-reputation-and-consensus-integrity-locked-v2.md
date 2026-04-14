# Reputation and Consensus Integrity — Locked Design

**Workstream**: Reputation subsystem redesign, §6.4 reopen, projection registry, observability, principle 16 amendment.
**Status**: Locked. Implementation spec for Claude Code. Draft 1 + Grok round 2 + ChatGPT round 2 synthesized.
**Challenge path**: scoped out to its own workstream (immediate next priority after this one ships).
**Binding documents touched**: `docs/blobsync-design.md` §6.4, `docs/design-principles.md` (new principle 16), `CLAUDE.md` invariants, new `docs/projection-registry.md`, new `docs/designs/reputation-and-consensus-integrity.md` (this document).

---

## 0. Decisions already locked

These are not open for debate in this document. They are stated so every reader of the spec sees the same starting point.

1. Shape B (full redesign, integer-only, rebuildable from DAG, projection registry as protocol primitive). No narrow fix, no bridge-and-migrate.
2. The existing `internal/taskverification/reputation.go` is deleted in the same commit that introduces the new evidence store. No migration — the store is empty on every node.
3. Four projections from one evidence base: W (independence weight), E (eligibility), S (scrutiny), C (challenge alert). The evidence base is primary; the projections are disjoint derived views.
4. §6.4 blob-unavailability exclusion is removed entirely. `ReasonCodeBlobUnavailable` survives only as advisory telemetry with zero canonical effect.
5. Hard slashing for systematic divergence passes through an investigation alert path, not an automatic trigger. Automatic hard slashing survives only for equivocation (protocol-observable at ingestion).
6. Principle 16 (incentive compatibility as correctness) is added to `docs/design-principles.md`.
7. Projection registry is a protocol primitive. CI-enforced. Runtime-enforced. Every existing consensus-adjacent store is retrofitted in this workstream.
8. The challenge path (adjudication mechanism, stake bonds, adjudicator selection, finality) is scoped out of this workstream and opens as its own workstream immediately after this one ships.
9. Security by default: every mechanism in this document must be secure against the threats Grok and ChatGPT identified without relying on operators, validators, or developers doing anything beyond default installation.
10. Experience criterion (Jobs standard): reputation is invisible when it works, obvious when it matters. No operator-facing configuration for routine operation. No developer-facing concepts required to integrate.

---

## 1. The load-bearing framing

The reputation subsystem is one evidence base with four disjoint derived projections.

The **evidence base is primary**. Every reputation fact the protocol acts on is a function of canonical evidence records written at well-defined moments. The projections are separately-specified deterministic functions over that evidence, each with its own attack surface and its own correctness criteria.

The four projections are:
- **W (Independence Weight)**: discrete-tier integer weight feeding Q-weighted settlement fee distribution.
- **E (Eligibility)**: binary gate determining whether a validator may participate in a given round.
- **S (Scrutiny Level)**: enum determining the stake-at-risk multiplier and replication requirement for a validator's participation in a given round.
- **C (Challenge Alert)**: anomaly signal derived from pairwise and higher-order evidence. In this workstream, C produces alerts that are logged. The mechanism that acts on alerts is the challenge path, which is the next workstream.

**This framing rejects three alternatives:**
- It rejects the single-scalar Q that was the original shape, because one scalar doing four jobs is a gameable optimization target.
- It rejects the eligibility-only reframe proposed in ChatGPT round 1 question 10, because removing independence from money removes the economic gradient that makes structural independence self-reinforcing (principle 4).
- It rejects deferring canonical consequences to a future challenge path, because both Grok round 2 and ChatGPT round 2 converged on this as the load-bearing gap: you cannot defer the primitive your design depends on.

**Experience test for §1**: A protocol participant touches the evidence base only indirectly, through the four projections. A validator operator sees their W tier and their cold-start state. A developer integrating the protocol sees verified outcomes and trusts that they came from structurally independent verifiers. Nothing about the four-projection architecture is user-facing. It is invisible scaffolding.

---

## 2. The canonical evidence record

### 2.1 The record

```go
type ReputationEvidence struct {
    RoundID           RoundID       // the round this evidence is from
    EpochIndex        uint64        // protocol epoch at round finalization
    RoundHeight       uint64        // monotone counter, per-epoch
    ValidatorID       AgentID       // the validator this evidence concerns
    Family            FamilyID      // analyzer family (protocol-defined enum)
    Category          CategoryID    // task category (protocol-defined enum)
    ContributorID     AgentID       // task poster
    VoteVerdict       Verdict       // pass / fail / abstain (enum)
    ConsensusVerdict  Verdict       // the round's final verdict (enum)
    Agreed            bool          // VoteVerdict == ConsensusVerdict
    StakeBP           uint64        // basis points of total-network stake this validator held at round open
    EscrowBudget      uint64        // µAET escrow of the task
    DerivationVersion uint32        // protocol version that produced this record
}
```

Every field is integer, enum, ID, or bool. No `float64`. No `time.Time`. No open-text strings. `FamilyID` and `CategoryID` are protocol-defined enums registered in the protocol constants, not runtime-introduced strings — this bounds cardinality at the schema level, not at the policy level.

**Key schema**: `"rep:" + EpochIndex(big-endian uint64) + ":" + ValidatorID + ":" + RoundID`. Epoch-first ordering supports natural range scans for replay and compaction.

**Secondary index**: `"rep-by-validator:" + ValidatorID + ":" + EpochIndex + ":" + RoundID` for per-validator reads during W/E/S computation.

### 2.2 The canonical write boundary (resolves ChatGPT round 2 required correction 1)

**Invariant (CR-1)**: A `ReputationEvidence` record is written by the `TaskVerificationConsensusConsumer` in the recognition fabric, in the same consumer invocation that fires settlement, after the round state transition has been persisted. The write is driven by the canonical `TaskVerificationConsensus` DAG event, not by local vote receipt, tentative aggregation, mathematically-secured early-finalization probe, or any other pre-consensus state.

**Invariant (CR-2)**: If the `TaskVerificationConsensus` event is reversed (which is a consensus fault, not normal operation), the evidence write is reversed with it, through the recognition fabric's standard rollback mechanism. The evidence record and the settlement event are siblings on the same canonical moment — they succeed together or fail together.

**Invariant (CR-3)**: A `ReputationEvidence` record is immutable once written. Challenge outcomes, version upgrades, and retroactive policy changes do not modify existing records. See §2.3.

**Invariant (CR-4)**: Every field of `ReputationEvidence` is derivable from the `TaskVerificationConsensus` event plus the round's vote set plus the validator-set snapshot at round open. No field depends on operator input, wall clock, or non-DAG side channels.

### 2.3 Challenge outcomes are not part of the evidence record (resolves ChatGPT round 2 required correction 2)

The original draft placed `ChallengeOutcome` inside `ReputationEvidence`. ChatGPT round 2 correctly identified this as a contradiction: the record is immutable (CR-3), but challenges resolve later, so either the record is mutable (violating CR-3) or the outcome is predicted at write time (impossible).

**Resolution**: `ChallengeResolutionRecord` is a separate canonical record type, written in response to a separate canonical DAG event (`ChallengeResolution`), in its own recognition consumer. It is keyed by `(ChallengeID, ValidatorID)` and linked to the original `ReputationEvidence` by `(RoundID, ValidatorID)`. The aggregate cache in §3 reads both streams.

Because the challenge path is scoped out of this workstream, `ChallengeResolutionRecord` is specified as a type but no producer exists yet. The consumer exists and is wired (so the projection registry's completeness check passes), but it remains idle until the challenge-path workstream ships. This is intentional: we specify the type now so the reputation subsystem does not need to be rewritten later to accommodate it.

**Invariant (CR-8 — semantic inertness)**: Until the challenge-path workstream ships a producer, the empty `ChallengeResolutionRecord` stream is semantically inert. No absence of a record may be interpreted as exoneration, confirmation, neutrality, a bonus, a downgrade reversal, or any other substantive protocol signal. Empty is empty. Any code path that reads the stream must treat absence as "no information" and must not fall through to a default that produces a different behavior than would occur if the stream did not exist at all. This prevents the placeholder stream from becoming a shadow semantic during the regression window.

**Invariant (CR-9 — named exception)**: `ChallengeResolutionStore` is the **only** Canonical projection permitted to run idle with zero aggregate state after its eligibility window. The permission is granted because its producer is a named, scoped-out, explicitly-next-workstream dependency. No other Canonical projection may claim this exception. The registry entry for `ChallengeResolutionStore` carries an explicit justification comment referencing this invariant; any future attempt to add a second idle-canonical projection requires a new named exception with its own justification and explicit founder approval, not a generic relaxation of PR-5.

### 2.4 Cardinality bounds

**Invariant (CR-5)**: A single round produces at most `|committee|` evidence records, where `|committee|` is the active committee at round open.

**Invariant (CR-6)**: `FamilyID` and `CategoryID` are protocol-defined enums with fixed cardinality. A round cannot introduce a new family or category; attempting to do so fails at ingestion. Current bound: 16 families, 64 categories. Tunable by protocol upgrade, not by runtime policy.

**Invariant (CR-7)**: The number of distinct `(family, category)` tuples observed in an epoch is bounded by `families × categories = 1024` at current values. No per-epoch cap beyond the schema cap is needed because the schema already bounds it.

This resolves Grok round 2 attack 4 (snapshot DoS via unbounded tuple space): the tuple space cannot be inflated because the protocol enum system refuses to accept new values at runtime. Category and family proliferation is a protocol upgrade, not an attack vector.

---

## 3. The aggregate cache

### 3.1 Per-validator aggregate

```go
type ReputationAggregate struct {
    ValidatorID          AgentID
    Family               FamilyID
    Category             CategoryID
    TotalEvidence        uint64  // total records
    AgreementEvents      uint64  // records where Agreed == true
    DisagreementEvents   uint64  // records where Agreed == false and Verdict != Abstain
    AbstainEvents        uint64  // records where Verdict == Abstain
    ChallengedConfirmed  uint64  // from ChallengeResolutionRecord stream
    ChallengedExonerated uint64  // from ChallengeResolutionRecord stream
    LastEvidenceEpoch    uint64
    DerivationVersion    uint32
}
```

### 3.2 Pair aggregate — bounded

```go
type ReputationPairAggregate struct {
    ValidatorA       AgentID    // lexicographically less than B
    ValidatorB       AgentID
    Family           FamilyID
    Category         CategoryID
    CoParticipation  uint64     // rounds where both voted
    CoAgreement      uint64     // rounds where both voted same verdict
    CoDeviation      uint64     // rounds where both voted against consensus identically
    LastUpdatedEpoch uint64
    DerivationVersion uint32
}
```

**Invariant (CA-1)**: A pair aggregate is maintained only for pairs with `CoParticipation ≥ CoParticipationThreshold` (locked at 5) within the current retained evidence window. Pairs below the threshold are not persisted. Pair aggregates whose retained-window co-participation falls below the threshold during compaction are deleted.

**Invariant (CA-2 — operational bound, test-backed)**: Worst-case retained pair-aggregate state under the locked protocol constants (max_committee = 256, rounds_per_epoch = 1000, retained_epochs = 8, CoParticipationThreshold = 5) must fit within the storage budget on supported node hardware (m7i.large minimum). The implementation must include a test that constructs a worst-case adversarial participation pattern and verifies that resulting pair-state storage remains below 2 GB of BadgerDB usage. This test is mandatory per §15 success criterion 10 and is the quantitative proof of boundedness. An abstract big-O argument is insufficient; the test carries the claim.

**Invariant (CA-3)**: The aggregate cache is a strict projection of the evidence stream. It can be rebuilt from evidence deterministically at any time, and rebuild produces a byte-identical result. If cache and evidence disagree, evidence wins and the cache is discarded.

**Invariant (CA-4)**: The cache is not authoritative. No protocol decision reads a cache value that cannot be recomputed from evidence. The cache exists for performance only.

### 3.3 Why this closes the growth attack

Grok round 2 attack 4 and ChatGPT round 2 required correction 5 converged on pair state growth as a DoS vector. The closure has three layers:

1. **Schema-level cardinality bound** (CR-6, CR-7): the `(family, category)` tuple space is fixed by protocol enum, not by runtime data. An attacker cannot inflate the tuple space.
2. **Participation threshold** (CA-1): pairs with trivial co-participation are not persisted, preventing O(|V|²) pair proliferation from single-round interactions.
3. **Retention window**: evidence older than the retention window is compacted (§5), and pair aggregates for pairs with no recent co-participation decay out with their underlying evidence.

All three bounds are enforced at write time, not at query time. An attacker cannot produce a state the system cannot store.

**Experience test for §2 and §3**: A node operator sees the aggregate cache as an implementation detail of their local BadgerDB. They never touch it directly. Its size is bounded and predictable. It is invisible scaffolding.

---

## 4. The four projections

### 4.1 W — Independence Weight

**Purpose**: feeds Q-weighted settlement fee distribution. Replaces the float-based `ValidatorQScoreFn` in `internal/settlement/verification_consensus_settler.go`.

**Interface**: `ValidatorIndependenceWeightFn(validator, family, category, contributor, escrowBudget, epoch) → uint64` returning basis points where 10000 = even-split baseline.

The existing `ValidatorQScoreFn` signature is removed. The `float64` return type is deleted from the codebase in the same commit that introduces the new signature. There is no transitional shim.

**Tier system**: five discrete tiers.

| Tier | Value (BP) | Meaning |
|---|---|---|
| T0 | 7000 | Cold-start baseline, or earned-negative |
| T1 | 8500 | Below-average earned |
| T2 | 10000 | Earned neutral (not cold-start) |
| T3 | 11500 | Above-average earned |
| T4 | 13000 | Top earned |

**Computation**:

```
agreement_bp = (AgreementEvents * 10000) / max(1, TotalEvidence - AbstainEvents)
```

Pure integer division. AbstainEvents are excluded from the denominator because the agreement rate is over voted rounds, not over all rounds. Abstentions affect TotalEvidence (so they feed cold-start progression and the S projection) but do not mechanically punish agreement rate in W.

Tier placement:
- `agreement_bp >= 9000` → T4
- `agreement_bp >= 7500` → T3
- `agreement_bp >= 6000` → T2
- `agreement_bp >= 4000` → T1
- `agreement_bp <  4000` → T0

**Cold-start override**: if the validator is in Bootstrap state (§6), W = T0 (7000), regardless of agreement rate. This closes Grok round 2 attack 1 (low-escrow Sybil dilution at neutral W): a new Sybil seat earns at the minimum tier, not the neutral tier, so adding seats does not dilute honest mature earnings at par.

**Context-bounded trust transfer**: if the round's `EscrowBudget` exceeds the validator's trailing-20-round median escrow in this `(family, category)` by more than 3×, W is capped at T2 (neutral) regardless of the tier the raw agreement rate would place it at. This closes Grok attack 10 (reputation laundering via category specialization): accumulated high agreement rate in low-stakes rounds cannot cash out at top tier in a high-stakes round.

**Challenge outcomes do not feed W directly** (resolves ChatGPT round 2 required correction 3): challenge-exonerated and challenge-confirmed events from the `ChallengeResolutionRecord` stream affect E (eligibility holds), S (scrutiny ratcheting), and economic compensation via forfeited bond transfers — but they do not shift W tiers. The ±1 tier bonus from the original draft is removed. Reputation is not adjudication path-dependent for routine payout math.

**Invariant (W-1)**: The W function is pure integer. Same inputs produce identical outputs on any architecture.

**Invariant (W-2)**: The W function is monotone in contextual agreement rate and bounded in `[T0, T4]`.

**Invariant (W-3)**: Single-round weight movement is bounded. Accumulating one more evidence record cannot move a validator by more than one tier.

**Invariant (W-4)**: Total distributed in settlement equals pool exactly, to the µAET, verified by the "last recipient absorbs remainder" pattern with deterministic ordering by `ValidatorID`.

**Integer distribution math in the settler**:

```go
// Deterministic ordering
sort.Slice(recipients, func(i, j int) bool {
    return bytes.Compare(recipients[i][:], recipients[j][:]) < 0
})

var sumWeights uint64
weights := make([]uint64, len(recipients))
for i, r := range recipients {
    weights[i] = weightFn(r, family, category, contributor, escrowBudget, epoch)
    sumWeights += weights[i]
}

var distributed uint64
for i := 0; i < len(recipients)-1; i++ {
    amount := (pool * weights[i]) / sumWeights  // pure integer division
    transfer(from, recipients[i], amount)
    distributed += amount
}
// Last recipient absorbs rounding remainder
transfer(from, recipients[len(recipients)-1], pool-distributed)
```

This replaces lines 330–394 of `internal/settlement/verification_consensus_settler.go` in full. The existing even-split fallback path is retained as the zero-sum-weights case.

**Experience test for W**: A validator operator sees their tier assignment, nothing more. They do not see agreement rate percentages, basis points, or context-bounded trust transfer math. They see "T3 in (statistical_structural, code_review)" and they understand what that means without reading documentation. The tier system is designed to be user-intelligible at a glance.

### 4.2 E — Eligibility

**Purpose**: binary gate at vote ingestion. A validator whose vote is ingested for a round must be eligible at that moment.

**Interface**: `ValidatorEligibleFn(validator, family, category, round) → (eligible bool, reason string)`.

**Rules** (applied in order; first failure returns false):
1. Validator is in the active validator set at round open.
2. Validator holds the claimed analyzer family as registered and calibrated in the `CalibrationStore` (which is itself a canonical projection per §7 and must be non-empty — see §7.6 retrofit).
3. Validator is not in an active `EligibilityHold` from a previous challenge-confirmed event.
4. Validator's cold-start state permits participation at the round's escrow scale (§6.3).

**Invariant (E-1)**: Eligibility is a pure function of DAG-observable state at round open. No wall clock. No operator input. No self-report.

**Invariant (E-2)**: Ineligible votes are rejected at ingestion time in the vote consumer, not at tally time in the finalizer. The error is surfaced immediately to the submitting validator.

**Experience test for E**: A validator operator who is eligible never sees E. If they become ineligible (cold-start escrow cap exceeded, calibration lost, challenge hold), the node's health API surfaces the reason in plain language. The validator does not have to reason about protocol internals to understand why their vote was rejected.

### 4.3 S — Scrutiny Level

**Purpose**: enum determining the stake-at-risk multiplier and the replication requirement for a validator's participation in a round.

**Interface**: `ValidatorScrutinyLevelFn(validator, family, category, round) → ScrutinyLevel`.

**Levels**:

| Level | Stake Multiplier | Replication Floor | Meaning |
|---|---|---|---|
| Normal | 1.0× | DiversityFloor + 0 | Default |
| Elevated | 1.5× | DiversityFloor + 1 | Cold-start, out-of-context escrow, or recent challenge-confirmed event |
| High | 2.0× | DiversityFloor + 2 | Multiple recent challenge-confirmed events |

**Rules**:
- Default: `Normal`.
- Bootstrap cold-start state: `Elevated`.
- Round `EscrowBudget` > 3× trailing-20-round median in this (family, category): `Elevated`.
- One challenge-confirmed event in trailing 100 rounds: `Elevated`.
- Two or more challenge-confirmed events in trailing 100 rounds: `High`.

**Invariant (S-1)**: Scrutiny level is a pure function of evidence and round context. Integer enum.

**Invariant (S-2)**: Scrutiny never reduces below Normal. Elevation from a *confirmed* adverse event is ratcheted. Elevation from cold-start or out-of-context escrow decays as those conditions resolve; a Bootstrap validator transitioning to Probation is automatically back to Normal (assuming no other elevation cause).

**Invariant (S-3)** (resolves ChatGPT round 2 refinement 6.1): Mere existence of a `ChallengeAlert` does not elevate scrutiny. Only a `ChallengeResolutionRecord` with outcome `Confirmed` elevates. This prevents alert-flooding from becoming a grief vector in this workstream (and the challenge-path workstream will harden it further).

**Experience test for S**: An operator at Normal scrutiny never sees S. An operator at Elevated scrutiny sees "your stake-at-risk is 1.5× for the next N rounds in (family, category) because [reason]" with a clear path to return to Normal. The reason is always a specific, actionable condition.

### 4.4 C — Challenge Alert

**Purpose**: produce anomaly signals from pair aggregate patterns. In this workstream, C alerts are logged to an append-only advisory stream and surfaced on the node's health endpoint. They do not drive any canonical action.

**The challenge path is the next workstream.** The mechanism that acts on alerts — stake-bonded initiation, adjudicator selection, adjudication finality, hard slashing — is specified there. This workstream produces the alerts and consumes them only via logging.

**Alert conditions** (evaluated at epoch boundaries against the pair aggregate cache):

1. **Excess co-agreement against base rate**: a pair's `CoAgreement / CoParticipation` exceeds the base rate for their `(family, category)` by more than a protocol-defined statistical threshold, conditioned on minimum sample size (CoParticipation ≥ 20). Base rate is computed over all pairs in the family-category to avoid false positives on narrow task distributions.
2. **Identical-deviation pattern**: `CoDeviation / CoParticipation` above threshold. Coordinated wrong votes are a stronger signal than coordinated right votes.
3. **Abstention coupling**: one validator's abstentions correlate with another's votes-against-consensus.
4. **Cross-axis coupling**: normal pairwise correlation but anomalous alignment on a specific contributor or escrow band.

**Invariant (C-1)**: Alerts are logged, not acted on, in this workstream. They produce no settlement effect, no W movement, no E revocation, no S elevation.

**Invariant (C-2)**: Alerts are derived from the pair aggregate cache, which is a projection of canonical evidence. Alert generation is deterministic across nodes given the same aggregate.

**Invariant (C-3)**: The alert logic is conservative: it does not fire without minimum sample size, does not fire on narrow task distributions where base rate is undefined, and does not fire on pairs below `CoParticipationThreshold`. False positives degrade the subsystem's signal quality and must be minimized.

**Experience test for C**: A validator operator sees alerts on their node's health endpoint only if they are a subject of one. The alert is informational: "your node has been flagged for a pattern of coordinated voting with validator X in family Y. This alert is advisory. No protocol action has been taken." When the challenge path workstream ships, this will become the trigger surface for formal adjudication.

---

## 5. Bounded rebuild and snapshots

### 5.1 The requirement

Principle 5 requires reputation state to be rebuildable from the DAG. Grok round 2 attack 4 showed that naive replay from genesis is a DoS vector. The solution is deterministic epoch snapshots.

### 5.2 Snapshot framing

A snapshot is a deterministic serialization of the aggregate cache at an epoch boundary, derived from already-finalized DAG state. It is **not** an operator-produced artifact that other nodes trust by signature. It is a derived commitment that any honest node replaying the same DAG under the same rules must produce bit-for-bit identically.

There is no vote on snapshot roots. There is no consensus-on-consensus. The snapshot root is verified by recomputation. Disagreement on the root is disagreement about DAG state or about implementation, either of which is a consensus fault, not a snapshot fault.

### 5.3 Snapshot contents

Per epoch:
- All `ReputationAggregate` entries with any evidence in the epoch or carried forward.
- All `ReputationPairAggregate` entries meeting `CoParticipation ≥ 5` in the retained window.
- The snapshot root: a stable hash over the canonical lexicographic serialization of both caches.
- The `DerivationVersion` that produced it.
- The epoch index and the DAG event range covered.

**Invariant (SN-1)**: The snapshot root is a deterministic function of `(previous_snapshot_root, epoch_consensus_events_in_canonical_order, derivation_version)`.

**Invariant (SN-2)**: `snapshot(epoch E) = snapshot(epoch E-1) + forward_apply(epoch E events)`. Snapshot-plus-forward-replay equals full-replay.

**Invariant (SN-3)**: Snapshot generation has no dependence on map iteration order, memory layout, wall clock, or local compaction choices. All serialization is ordered by lexicographic key.

**Invariant (SN-4)**: Snapshot generation cost is linear in epoch events, bounded by `O(max_rounds_per_epoch × max_committee × CoParticipationThreshold)`. Empirically verified on the testnet before this workstream ships: snapshot generation must complete in under 30 seconds on the smallest supported node type (m7i.large) under worst-case epoch content.

### 5.4 Epoch length

**Locked value**: 1000 rounds per epoch.

This is a locked decision, not an open question. It is chosen such that startup replay from the latest snapshot completes in under 60 seconds on commodity hardware at the 5-node testnet scale, and scales linearly to at least 256-validator committees. If testnet measurement shows the 60-second bound is exceeded at realistic load, the value is revisited in a follow-up — but until then, 1000 is the shipping value.

### 5.5 Retention and compaction

**Locked value**: 8 epochs of retained raw evidence. Raw `ReputationEvidence` records older than 8 epochs are compacted; their contributions to the aggregate cache persist, but the per-round records are deleted.

**Invariant (SN-5)** (resolves ChatGPT round 2 improvement 8.2): After compaction, post-window audit is **aggregate-only**, not round-granular. This is stated explicitly in the design: Principle 15 does not require indefinite fine-grained inspectability. It requires auditable observability from evidence, and the aggregate is evidence.

**Invariant (SN-6)**: Compaction preserves all information required for future canonical computation at the current derivation version. If a future derivation version requires information the current schema does not retain, that is a protocol upgrade, not a compaction bug.

**Experience test for §5**: An operator sees snapshots as a startup optimization. Their node comes up in seconds, not minutes. They do not configure snapshot cadence, retention, or compaction. Defaults are the shipping values.

---

## 6. Cold-start

### 6.1 The four states

Derived from evidence, not stored explicitly:

- **Unseated**: not in active validator set.
- **Bootstrap**: `TotalEvidence < 20`.
- **Probation**: `20 ≤ TotalEvidence < 80`.
- **Mature**: `TotalEvidence ≥ 80`.

State is always recomputed from retained aggregates under current derivation rules. There is no stored state flag. ChatGPT round 2 improvement 9.1 corrected the earlier language: maturity is a derived label, and if retained evidence no longer supports it, the validator's current state reflects that.

### 6.2 How cold-start affects each projection

| State | W | E | S |
|---|---|---|---|
| Bootstrap | T0 (7000) | Escrow cap at `BootstrapEscrowCap` | Elevated |
| Probation | Earned tier capped at T2 | No escrow cap | Normal unless other elevation |
| Mature | Earned tier in `[T0, T4]` | No restriction | Normal unless other elevation |

**Invariant (CS-1)** (resolves ChatGPT round 2 improvement 9.1 and Grok round 2 attack 2): State is derived from accumulated evidence and is a monotone non-decreasing function of `TotalEvidence`. A validator cannot "return to Bootstrap" through adversarial behavior because `TotalEvidence` is monotonically increasing on every round it participates in (including abstentions). The "slow carousel oscillation" attack Grok described requires oscillation between states, which is impossible under monotone evidence accumulation — once past Bootstrap, a validator stays past Bootstrap.

**Grok round 2 attack 2's carousel variant** (one engineered disagreement per cycle to oscillate) is closed by CS-1 combined with the Bootstrap W=T0 rule: a validator with 20 evidence records is in Probation, and remains in Probation regardless of its agreement rate. Dropping agreement rate only moves it to a lower W tier within Probation, not back to Bootstrap. The carousel has no state to oscillate between. It is a pure tier-drop, which is economically dominated by honest operation at higher tiers.

### 6.3 BootstrapEscrowCap

**Locked value**: the trailing-100-round median task escrow observed in the protocol, computed at epoch boundary. Deterministic, protocol-defined, cannot be locally selected (resolves ChatGPT round 2 improvement 9.3).

At testnet, this will initially be a protocol constant (100000 µAET) until enough history accumulates for the median to be meaningful. Switchover from constant to derived median happens at epoch 10 of mainnet or when the protocol has observed 1000 rounds, whichever is later.

### 6.4 Sybil cost dependency

**Scope note** (resolves Grok round 2 attack 1 and ChatGPT round 2 attack closure tone correction): cold-start's attack resistance depends on the validator seat creation cost being high enough that Sybil rotation is not economically rational. Seat creation cost is a validator onboarding economics parameter, not a reputation subsystem parameter. This workstream does not set it.

**Explicit conditional**: the reputation design's closure of Grok attack 1 (validator-seat spam) is materially mitigated, not fully closed, within the scope of this workstream. Full closure depends on future work on validator onboarding economics. This is stated honestly in §13 (attack closure table) and §14 (scope boundary).

### 6.5 Neutral = 10000 clarification (resolves ChatGPT round 2 improvement 4.2)

T2 (10000 BP) is not "trusted neutral." It is non-bonus baseline under constrained eligibility and elevated scrutiny when reached from Bootstrap (Bootstrap caps at T0, not T2). A Mature validator at T2 has earned it through demonstrated evidence. A Probation validator at T2 has earned it and is capped at T2 by cold-start policy. A Bootstrap validator is never at T2 — they are at T0 regardless of raw agreement rate.

**Experience test for §6**: An honest new validator sees a bounded, predictable ramp: "you are in Bootstrap, you need 20 more rounds of participation (including abstentions) to reach Probation, then 60 more for Mature. Your expected earnings during Bootstrap are at tier T0 (70% of baseline). This is not punishment; it is the on-ramp." The message is shipped on the node's default startup screen.

---

## 7. §6.4 reopen — blob-unavailability exclusion removal

### 7.1 The replacement

The existing text in `docs/blobsync-design.md` §6.4 requiring validators that abstain with `ReasonCodeBlobUnavailable` to be excluded from the agreement rate penalty is **removed in full**.

**The new rule**: an abstention with `ReasonCodeBlobUnavailable` is recorded as an abstention like any other. It produces a `ReputationEvidence` record with `VoteVerdict = Abstain`, contributes to `AbstainEvents`, and affects E and S the same as any other abstention. It does not affect W's agreement rate denominator because §4.1's formula already excludes `AbstainEvents` from the W denominator — which applies symmetrically to all abstentions, not conditionally on reason code.

### 7.2 Advisory telemetry (resolves ChatGPT round 2 improvement 10)

`ReasonCodeBlobUnavailable` survives in the wire protocol as advisory telemetry only. Operators can still emit it to signal why they abstained. The protocol logs it for operator debugging. It has **zero canonical effect**. It does not exempt from penalty, does not gate any projection, does not trigger any alert.

This is stated explicitly in the updated `docs/blobsync-design.md` §6.4 text so a future reader cannot misinterpret the reason code's presence as a live exception path.

### 7.3 Why this works without punishing honest failures

The W formula in §4.1 excludes abstentions from the agreement rate denominator, so an abstention does not mechanically lower a validator's agreement rate. Abstentions do contribute to `TotalEvidence` (driving cold-start progression) and to S (an abstention in an out-of-context-escrow round elevates scrutiny temporarily), but they do not compound into the kind of catastrophic penalty that would require an exclusion.

Honest operators experiencing occasional BlobSync failures accumulate abstention events at a low rate that is absorbed in the noise of normal operation. Coalition attackers faking unavailability gain no advantage, because faking produces the same record as honest failure — symmetric cost removes the strategic opt-out. This closes Grok round 2 attack 5.

### 7.4 Operational reliability clause

Validators are responsible for BlobSync reliability. The protocol does not distinguish honest infrastructure failure from deliberate abstention because it cannot and will not attempt to. Operational reliability is a first-class obligation of running a validator seat. This is stated in the validator onboarding documentation that ships with the reputation workstream.

### 7.5 Monitoring obligation

BlobSync's honest failure rate on the testnet is measured against realistic load before the data ingestion workstream ships. Target: under 1% abstention-due-to-unavailability on honest-operator rounds. If measured rate exceeds this, the design is revisited in a follow-up — but the revisit does not reintroduce self-reported exclusion. It either raises `W_MIN` (currently T0 = 7000) to reduce per-abstention impact, or adjusts the `AbstainEvents` counting rule in a way that preserves protocol-observability.

**Experience test for §7**: An honest operator whose BlobSync fails once a month sees no visible impact on their earnings or standing. An operator whose BlobSync fails constantly sees their W tier drop gradually, their cold-start progression slow (they accumulate abstentions instead of agreements), and their health endpoint surface a clear diagnostic: "your BlobSync success rate is X%, below the operational threshold of Y%. Investigate your network configuration." The protocol tells them what is wrong in plain language.

---

## 8. Slashing revision

### 8.1 What is removed

The existing `SlashingPolicy.SystematicDivergenceThreshold = 0.30` with `SystematicDivergenceMinVotes = 50` in `internal/taskverification/slashing.go` is removed:
- The `float64` agreement rate comparison is removed (principle 11 violation).
- The `TotalVotes >= 50` absolute gate is removed (creates cold-start free-pass).
- The automatic hard-slash trigger is removed (creates weaponizable cliff).

`SlashingAction.ReputationPenalty int` is also removed. The evidence record itself is the reputation effect; there is no separate penalty counter.

### 8.2 What is added

**Equivocation hard-slash remains automatic.** Equivocation is protocol-observable at vote ingestion (two conflicting votes from the same validator for the same round). The existing `detectEquivocators` logic is preserved with one defensive fix: its output is sorted by `ValidatorID` before return to ensure deterministic action ordering across nodes. `slashing.go:167`'s native map iteration is replaced with a sorted key iteration.

**Systematic-divergence detection becomes an investigation alert**, not a hard-slash trigger.

```
agreement_bp = (AgreementEvents * 10000) / max(1, AgreementEvents + DisagreementEvents)
```

Abstentions excluded from the denominator (same rule as W). Pure integer math. If `agreement_bp < 3000` AND `TotalEvidence ≥ 10` AND validator is Mature or Probation (not Bootstrap), an `InvestigationAlert` event is emitted on the DAG.

**In this workstream**, `InvestigationAlert` events are:
- Logged to the node's health endpoint, **node-local tier 2 only** (§10.1). Visible to the validator operator inspecting their own node.
- **Not** surfaced on any tier 3 public statistics endpoint. Not broadcast to any cross-node dashboard. Not aggregated into any public feed by validator identity.
- Not acted on by any canonical consequence (no W movement, no E revocation, no S elevation, no fee hold).

**Invariant (HS-4 — regression-window grief containment)**: During the regression window between this workstream shipping and the challenge-path workstream shipping, `InvestigationAlert` events are visible only to the subject validator's own operator on their own node. They are not publicly queryable, not publicly aggregated, and not broadcast to the network. This closes the operational grief vector identified in Grok round 3 attack 7 and blind-spot 10: an attacker cannot demoralize honest operators with public alert noise because the alerts are not public. The alerts still accumulate on the subject node as evidence for the future challenge path, but they produce zero operator-visible or network-visible surface beyond the subject's own inspection.

When the challenge-path workstream ships, HS-4 is superseded by that workstream's visibility rules, which will define how alerts become part of the adjudication process. Until then, HS-4 is absolute.

**In the challenge-path workstream** (next priority), `InvestigationAlert` becomes the trigger for a stake-bonded adjudication process that can apply hard slashing through that workstream's mechanism. This workstream specifies the trigger; the next workstream specifies the action.

### 8.3 The narrow invariant (resolves ChatGPT round 2 improvement 11.1)

**Invariant (HS-1)**: Systematic-divergence hard slashing is never automatic. It requires adjudication through the challenge path. Until the challenge path ships, systematic divergence produces an investigation alert with no canonical action.

**Invariant (HS-2)**: Equivocation hard slashing remains automatic because equivocation is cryptographically provable at ingestion without adjudication.

**Invariant (HS-3)**: All slashing math is pure integer BP. No float paths remain in the slashing evaluator.

### 8.4 The temporary regression (honesty clause)

Between this workstream shipping and the challenge-path workstream shipping, systematic divergence is detected but not acted on. This is a **known temporary regression** from the current code's behavior (which has the automatic rule but whose rule never fires because the reputation store has no writers). It is called out here honestly because we are intentionally accepting a time-bounded gap rather than shipping a rushed challenge-path design. The gap closes when the next workstream ships.

This is not a shortcut. It is a scope boundary drawn correctly. Principle 16 demands that we not ship a challenge-path design hastily just to avoid the gap; the gap is the correct answer.

**Experience test for §8**: A validator operator sees equivocation as automatic and clearly explained — "you equivocated on round X, automatic slashing applied, here is the evidence." An operator whose node is drifting toward systematic divergence sees an investigation alert well in advance — "your agreement rate is below 30% after 15 rounds in (family, category). This is not yet slashable, but it will be flagged for review when the challenge path ships." They have time to correct before the regression closes.

---

## 9. Projection registry

### 9.1 Purpose

Close the "writer exists, caller doesn't" pattern protocol-wide. The reputation audit of 2026-04-12 found two instances (reputation, calibration) of this pattern; Grok round 2 attack 8 reframed it from bug to stealth denial-of-defense vector. The fix is a structural primitive that makes it architecturally impossible to ship a consensus-adjacent store with exported writers and no production callers.

### 9.2 What exists

A new package `internal/projections/` containing:

```go
type Classification int
const (
    Canonical Classification = iota
    Advisory
)

type CanonicalProjection struct {
    Name                 string
    Package              string
    StoreType            string
    Classification       Classification
    SourceEvents         []EventType
    LiveConsumerRef      string      // fully qualified symbol path
    ReplayConsumerRef    string      // fully qualified symbol path
    ObservabilitySurface Surface
    IntegrationTestRef   string      // fully qualified test symbol path — must exist and pass
    Owner                string
    CreatedAt            string      // ISO date
}

type ProjectionRegistry struct {
    entries map[string]CanonicalProjection
}
```

### 9.3 Structural definition of "consensus-adjacent store"

ChatGPT round 2 improvement 12 correctly pushed back that the original structural definition was implementation-contingent and evadable. The revised definition, stated in prose:

**Principle-level**: any durable state projection whose outputs can affect canonical protocol behavior — directly or transitively, through any call chain no matter how indirect — must be registered as Canonical.

**CI heuristic** (approximation, not substitute): the CI check scans for types with at least one exported writer method that mutates persistent state and are constructed with a BadgerDB handle or equivalent durable persistence dependency in `cmd/node/main.go`. These must be registered. The heuristic is a sufficient condition (if matched, must be registered) not a necessary one (unmatched types may still need registration per the principle-level rule).

**Code review requirement**: every PR that adds a new durable store type requires explicit review confirmation that the projection registry has been updated. This is a process rule backing the CI heuristic.

### 9.4 Registry entry requirements

**Invariant (PR-1)**: A `CanonicalProjection` entry with `LiveConsumerRef == ""` fails node startup with a fatal error.

**Invariant (PR-2)**: A `CanonicalProjection` entry with `ReplayConsumerRef == ""` fails node startup.

**Invariant (PR-3)** (resolves ChatGPT round 2 feedback on §7.7): A `CanonicalProjection` entry with `IntegrationTestRef == ""` or with a test reference that does not exist in the codebase fails CI. The integration test is part of the registry entry; no registry entry is complete without a passing integration test.

**Invariant (PR-4)**: A `CanonicalProjection` entry with `ObservabilitySurface == None` and no explicit justification comment fails startup. Advisory projections can opt out with justification; Canonical projections cannot opt out at all.

### 9.5 Runtime health check

The registry exposes `HealthCheck()` called at node startup and periodically during operation (every epoch boundary). The check verifies that every Canonical projection has non-empty aggregate state consistent with the DAG events the projection should project.

**Invariant (PR-5)**: A Canonical projection that has been wired and running for more than `EligibilityWindow` (locked at 3 epochs) and still has an empty aggregate is a fatal health error. The node emits a critical alert on its health endpoint and refuses to claim healthy status until the condition resolves. This is the runtime signal that catches the exact reputation situation from the 2026-04-11 accept verdict.

### 9.6 Initial registry entries

Entered in this workstream:

| Name | Package | Classification | Notes |
|---|---|---|---|
| EvidenceStore | `internal/reputation` (new) | Canonical | Replaces `ValidatorReputationStore`. Source: `TaskVerificationConsensus`. |
| ChallengeResolutionStore | `internal/reputation` (new) | Canonical | Specified but producer deferred to challenge-path workstream. LiveConsumer exists and is idle; idle state is permitted pre-producer. |
| CalibrationStore | `internal/taskverification` | Canonical | Retrofitted. Has the same writer-without-caller bug as reputation; the retrofit wires the writer. |
| BlobServingReputation | `internal/blobsync` | Advisory | Currently serves BlobSync peer selection, not consensus. Upgraded to Canonical when it feeds consensus weighting in a later workstream. |
| AgentReputation | `internal/reputation` (existing worker repo) | **Audit required** | Classified as Canonical if it feeds settlement, Advisory otherwise. Audit is part of the retrofit commit. |
| EscrowStore | `internal/escrow` | Canonical | Retrofitted. |
| LedgerStore | `internal/ledger` | Canonical | Retrofitted. |
| GenerationLedgerCalculator | `internal/settlement` | Canonical | Retrofitted. |

**Retrofit scope**: every existing store in the codebase is enumerated, classified, and either registered or deliberately excluded with justification. This is part of this workstream's commit, not deferred.

### 9.7 The defense is a stack, not a single mechanism

Grok round 2 confirmed the registry is a strong structural fix but correctly identified that the only residual is a registered-but-silently-no-op consumer. The defense:

1. **CI static check** — type graph scanned for writer-without-caller pattern, must be in registry.
2. **Registry entry** — all fields required, including integration test reference.
3. **Integration test** — must drive real events through the live consumer and assert mutation. Part of PR-3.
4. **Startup health check** — PR-5, runtime verification that Canonical projections accumulate state.
5. **Code review checklist** — process rule requiring reviewer sign-off on projection changes.

All five are mandatory for every Canonical projection. None is optional.

**Experience test for §9**: A contributor adding a new store type is guided by CI and code review to register it, provide a live consumer, provide a replay consumer, provide an integration test, and specify an observability surface, all before the PR can merge. They experience the registry as a checklist that prevents a class of bug, not as bureaucracy.

---

## 10. Observability — locked policy

### 10.1 The three tiers (resolves ChatGPT round 2 required correction 6)

**Tier 1: protocol decision logic** — full access to all aggregate and pair aggregate data. Internal to the running node. No external surface.

**Tier 2: node-local self and operator access** — unauthenticated read access to all reputation state stored on the node itself. This is local administrative access; it uses no cross-node authentication because the access is to local state. A validator operator inspects their own node's data via a node-local HTTP endpoint bound to `127.0.0.1` by default, and a CLI tool (`aet reputation inspect`). No protocol-wide auth infrastructure.

**Tier 3: public aggregate statistics** — published at epoch boundaries with one epoch of delay. Cohort sizes below 10 are not reported individually; affected validators are grouped into an "other" bucket. Public stats are: W tier distribution histogram, total evidence volume per epoch, equivocation-slash counts per epoch. No per-validator scores, no per-validator tier, no per-validator pair correlations, **no per-validator `InvestigationAlert` counts, no per-validator C alert counts, no alert counts by validator identity at any granularity**. Aggregate alert volume across the whole network per epoch is publishable (for ecosystem health monitoring) but alert attribution is never published. This closes the grief amplification vector in Grok round 3 attack 7 and blind-spot 10 at the observability layer, reinforcing HS-4.

### 10.2 Why this replaces the "authorized auditor" language

ChatGPT round 2 correctly pushed back that "authorized auditor" was a governance placeholder with no defined mechanism. The replacement is simpler: **any external observer who wants to audit the reputation subsystem runs a full node.** This is the existing answer for blockchain observability. Full nodes derive their own reputation state from the DAG under the same canonical rules, and the derived state is verifiable by recomputation. No new authorization infrastructure.

The downside is that external parties who are not operators cannot audit individual validator reputation without running a node. This is acceptable under the protocol's trust model: external parties who want to audit are operators of the audit. Every third-party security firm conducting a review runs a node; that is how blockchain audits work.

### 10.3 Statistical targeting residual (resolves ChatGPT round 2 improvement 13.2)

Grok round 2 attack 9 correctly identified that tier 3 aggregates plus self-queries still leak enough metadata for patient Bayesian inference. The mitigations:

- **Delay**: tier 3 aggregates are delayed by at least one full epoch. By the time an attacker infers anything from the histogram, underlying positions have moved.
- **Cohort gating**: cohorts below 10 are not individually reported.
- **Coarsening**: W tier distribution is reported as the five-tier histogram, not as exact validator counts at each specific basis-point value. Tier granularity is the minimum observable unit.

**Honest residual**: these mitigations reduce statistical targeting from "free oracle" to "expensive inference with stale data." The attack is not fully closed. This is stated honestly in the attack closure table in §13. Under principle 16, an acknowledged partial closure is architecturally correct.

### 10.4 Query logging

Tier 2 queries against the local node are logged to an advisory log file for operator debugging. The log is explicitly non-canonical and is not part of any consensus decision.

**Experience test for §10**: An operator runs `aet reputation inspect --validator self` and sees their full state in a readable format. A developer querying the public stats endpoint gets a JSON object with network-level aggregates and no per-validator data. A third-party auditor runs a full node, waits for it to sync, and derives any state they want from the DAG. None of these experiences requires configuration, authentication flows, or documentation beyond the command's help text.

---

## 11. Principle 16 — proposed amendment to `docs/design-principles.md`

**Added as principle 16** (resolves ChatGPT round 2 sharpening):

> **Principle 16 — Incentive compatibility is part of correctness.**
>
> A subsystem is invalid if it is technically replayable, byzantine-resistant in the narrow sense, and internally consistent but creates strategies for misbehavior that are profitable in expectation. A design that is mechanically correct and strategically gameable is wrong.
>
> Every consensus-adjacent design must identify the plausible adversarial actor classes relevant to the subsystem and state why profitable deviation is or is not available. This is not a completeness exercise — the goal is serious adversarial accounting, not an impossible enumeration of every hypothetical. For each actor class, the design must describe the mechanism that prevents profit, or explicitly accept a residual risk with justification.
>
> This principle is why every consensus-adjacent design in this protocol must include an adversarial review pass (typically red-teamed by an independent reviewer) and why the output of that review must feed back into the design before it is locked. "No one would do that" is not an argument; "the math says it does not pay, and here is the math" is.
>
> This principle was codified after the reputation subsystem redesign (April 2026), where the original design was mechanically correct but created Sybil carousels, cold-start exploits, reputation laundering, and a weaponizable slashing cliff — none of which violated any existing principle in isolation. The new principle formalizes that such gaming is itself a correctness violation.

---

## 12. Rewritten `docs/blobsync-design.md` §6.4

The locked text replacing the existing §6.4 paragraph on blob-unavailability exclusion:

> **§6.4 Blob unavailability and reputation.** Validators that abstain from a round due to blob unavailability are treated identically to validators that abstain for any other reason. Reputation evidence is recorded as a normal abstention. There is no canonical exception path, no reason-code-based exclusion, and no self-reported hardship accommodation. `ReasonCodeBlobUnavailable` survives in the wire protocol as advisory telemetry — validators may emit it to aid operator debugging — but the reason code has zero canonical effect and does not alter any projection, any penalty, or any alert.
>
> **Rationale**: a self-reported exclusion path is a principle 15 violation (observable evidence beats self-reported claims). Proving negative network availability robustly is architecturally hard and no cheap-to-verify primitive exists. The cleaner answer is symmetric treatment: honest BlobSync failures and deliberate abstention produce the same reputation cost, and the cost is absorbed by the smoothness of the W function which excludes abstentions from the agreement rate denominator. Operators are responsible for BlobSync reliability as part of running a validator seat.
>
> **Monitoring**: BlobSync's honest failure rate is measured on the testnet before the data ingestion workstream ships, with a target of under 1% on honest-operator rounds. If measured rate exceeds this, the W-baseline or the abstention-counting rule may be adjusted; the self-reported exclusion will not be reintroduced.

---

## 13. Attack closure table — honest language

Per ChatGPT round 2 required correction 7 and Grok round 2 residual analysis, the table uses:
- **Closed**: closure is structural and complete within the scope of this workstream.
- **Mitigated**: closure depends on parameters, future work, or a residual the design acknowledges.
- **Partially closed**: known residual explicitly stated in the relevant section.

| Attack | Status | Mechanism | Residual |
|---|---|---|---|
| 1. Validator-seat spam at degenerate Q | **Mitigated** | Bootstrap W=T0 (§4.1), schema cardinality bounds (§2.4) | Full closure depends on validator seat creation cost being set elsewhere (§6.4 scope note). |
| 2a. Perpetual-new-validator carousel | **Mitigated** | Bootstrap W=T0 + CS-1 monotone state (§6.2) | Same as attack 1: Sybil cost per seat is not in this workstream. |
| 2a-variant. Slow carousel oscillation (Grok round 2) | **Closed** | CS-1 monotone state makes oscillation impossible (§6.2) | None. |
| 2b. 0.01 floor rider | **Closed** | No floor exists; T0 is a tier, not a decay floor (§4.1) | None. |
| 2c. Strategic abstention cold-start | **Closed** | AbstainEvents count toward TotalEvidence and state progression (§6.1) | None. |
| 3. Float64 consensus divergence | **Closed** | Integer-only W (§4.1), integer slashing (§8), signature removal (§4.1) | None. |
| 4. DAG-replay DoS | **Closed** | Schema cardinality bound (§2.4), CoParticipationThreshold (§3.2), snapshot bound SN-4 (§5.3) | Snapshot generation cost verified in testnet before ship. |
| 5. Fake blob unavailability | **Closed** | §6.4 removal (§7); symmetric treatment eliminates strategic opt-out | None. |
| 6. Pairwise correlation metagame | **Mitigated** | Correlation moved out of W entirely (§4.1, §4.4); alerts logged only (§4.4); tier-3 does not expose per-validator alert attribution (§10.1) | Full closure depends on challenge-path workstream specifying alert-action mechanism. This workstream produces alerts; it does not act on them. |
| 7. Weaponized slashing cliff | **Mitigated** | Automatic hard-slash removed (§8); systematic divergence produces investigation alert only (§8.2); HS-4 node-local-only visibility closes the public-grief vector during the regression window | Full closure depends on challenge-path workstream specifying adjudication. Temporary regression: systematic divergence is not slashable until next workstream ships (§8.4 honesty clause), but the grief amplification vector is closed at the observability layer via HS-4. |
| 8. Writer-without-caller stealth | **Closed** | Projection registry (§9) + CI + runtime health check PR-5 + mandatory integration test PR-3 | Residual: registered-but-silently-no-op consumer, closed by mandatory integration test. |
| 9. Observability as targeting metadata | **Partially closed** | Three-tier model (§10), delay, cohort gating, coarsening | Residual: expensive statistical inference still possible with stale data. Honestly acknowledged. Not further closable without weakening principle 15. |
| 10. Reputation laundering via category specialization | **Mitigated** | Context-bounded trust transfer caps at T2 (§4.1); S elevation on scale mismatch (§4.3) | Patient adversary building at matching scale over long horizon still succeeds; accepted as boundary of economic protection per principle 16. |
| 11 (Grok round 2). Challenge-adjudication capture | **Out of scope** | Challenge path is next workstream; attack is tracked in that workstream's opening scope | None in this workstream because the capture vector does not exist until the challenge path exists. |
| 12 (Grok round 3). Alert pre-positioning + regression compounding | **Mitigated** | HS-4 node-local-only `InvestigationAlert` visibility (§8.2); tier-3 never exposes per-validator alert attribution (§10.1); the challenge-path workstream is committed as immediate next priority with explicit non-deprioritization (§17 implementation sequence) | Residual: the attacker can still accumulate alerts as evidence for the future challenge path. This is not preventable in this workstream and is the correct input for the next workstream to design around. The grief amplification during the regression window is closed at the visibility layer. |

---

## 14. Scope boundaries — what this workstream does not do

- **Challenge path adjudication**. Scoped out. Next workstream. Adjudicator selection, stake bonds, adjudication finality, challenge evidence standards, hard slashing via adjudication: all specified in that workstream, not this one.
- **Validator onboarding economics**. Seat creation cost, minimum stake, bond structure: not in this workstream. The reputation design's closure of Sybil attacks is conditional on future work here.
- **Generation ledger math**. Untouched. The settler still distributes 2% to the generation ledger on accept; this workstream does not modify `GenerationLedgerCalculator`.
- **Agent/worker reputation** (`internal/reputation/ReputationManager`). Classified in the projection registry retrofit (audit required in this workstream's commit). Not redesigned.
- **Data ingestion workstream's independence-weighting query interface**. This workstream produces the primitives (pair aggregate, context-bounded trust transfer, multi-axis evidence). The query interface itself is the next workstream after challenge path.
- **Per-operator identity binding**. Not a concept in the protocol today. Every validator identity is independent. This workstream does not assume per-operator binding exists. Attack resistance is stated conditional on the current identity model.

---

## 15. Success criteria — shipping conditions

All of the following must be true before the workstream is marked complete:

1. `internal/taskverification/reputation.go` is deleted. The replacement lives at `internal/reputation/evidence_store.go` (or equivalent new path).
2. `internal/projections/` package exists with registry, CI check, startup check, health check.
3. `EvidenceStore`, `ChallengeResolutionStore`, `CalibrationStore`, `EscrowStore`, `LedgerStore`, and `GenerationLedgerCalculator` are registered as Canonical projections with non-nil live consumer, replay consumer, integration test, and observability surface references.
4. `VerificationConsensusSettler.distributeByQuality` uses integer-only math with `ValidatorIndependenceWeightFn` returning `uint64`. The old `float64` signature is removed from the codebase.
5. `go test -race ./...` passes across all packages with zero failures.
6. Unit tests verify W invariants W-1 through W-4 across a property-based input space.
7. Unit test verifies `ReputationEvidence` canonical write boundary CR-1: evidence is written exactly when settlement fires, not before.
8. Unit test verifies snapshot-from-genesis equals snapshot-by-forward-apply (SN-2) on a synthetic DAG.
9. Unit test verifies cross-architecture determinism: W computation produces bit-identical output under differing Go compile flags on the same input (resolves ChatGPT round 2 success criteria addition).
10. Unit test verifies pair aggregate bound CA-2 under worst-case cardinality input (resolves ChatGPT round 2 success criteria addition).
11. Unit test verifies compaction preserves aggregate consistency across the retention boundary (resolves ChatGPT round 2 success criteria addition).
12. Unit test verifies the observability tier-3 endpoint does not expose per-validator data (resolves ChatGPT round 2 success criteria addition).
13. Integration test drives a real `TaskVerificationConsensus` event through the full pipeline and asserts `EvidenceStore` mutates correctly, including the canonical write boundary.
14. Integration test verifies projection registry startup health check correctly fails a node that has a Canonical projection with empty state after 3 epochs of eligibility.
15. Live testnet: deploy to all 5 nodes via the 6-step protocol in CLAUDE.md.
16. Live testnet: produce a first accept verdict and verify `EvidenceStore` contains non-empty state on every node.
17. Live testnet: produce a second accept verdict and verify two validators with different agreement histories receive different W tier assignments.
18. Live testnet: measure snapshot generation time under realistic load; confirm SN-4's 30-second bound.
19. Live testnet: measure BlobSync honest failure rate; confirm §7.5's 1% target.
20. Live testnet: verify `InvestigationAlert` fires correctly on a synthetic systematic-divergence scenario (alert only, no action).
21. `docs/blobsync-design.md` §6.4 is updated to the §12 locked text.
22. `docs/design-principles.md` has principle 16 added.
23. `docs/multi-validator-consensus-final-design.md` is updated to reference the new projection and new slashing path.
24. `docs/projection-registry.md` exists with the registry specification, the structural definition, and the retrofit list.

---

## 16. Locked constants

These values are locked for the initial ship. They are marked clearly so future readers understand which values are calibration decisions and which are architectural invariants.

| Constant | Value | Rationale |
|---|---|---|
| `BootstrapThreshold` | 20 evidence records | Bounded honest ramp; closes Bootstrap state on ~hours of testnet participation |
| `MatureThreshold` | 80 evidence records | 4× Bootstrap; gives Probation a meaningful window |
| `CoParticipationThreshold` | 5 co-rounds | Statistically meaningful minimum for pair aggregate storage |
| `EvidenceRetentionEpochs` | 8 | Compaction boundary; aligns with reasonable operational history |
| `EpochLength` | 1000 rounds | Targets 60-second startup replay on commodity hardware |
| `MaxFamilies` | 16 | Schema cardinality bound |
| `MaxCategories` | 64 | Schema cardinality bound |
| `MinEvidenceForInvestigationAlert` | 10 evidence records | Statistical minimum before systematic divergence is meaningful |
| `InvestigationAlertThreshold` | 3000 BP (30%) | Agreement rate below which divergence alert fires |
| `EscrowContextMultiplier` | 3× | Trailing-median ratio above which context-bound caps W and elevates S |
| `BootstrapEscrowCap` | trailing-100-round median, or 100000 µAET constant pre-bootstrap | Locked formula, not a tunable |
| `CohortGatingMinimum` | 10 | Below this, cohorts are bucketed in tier-3 public stats |
| `Tier values (T0–T4)` | 7000, 8500, 10000, 11500, 13000 | Five discrete tiers, anti-optimization-target shape |
| `Tier placement thresholds` | 9000/7500/6000/4000 BP | Non-uniform spacing; top tiers are harder to reach |
| `EligibilityWindow (PR-5)` | 3 epochs | Health check window for registered Canonical projections |
| `SnapshotGenerationBound` | 30 seconds | Hard operational bound verified in testnet |

These values are calibrated against the 5-node testnet and the current validator count. If testnet measurement on production hardware shows any value is wrong, it is revisited in a follow-up — but the revision does not invalidate the workstream's ship.

---

## 17. Implementation sequence

This is the order Claude Code follows. Each step is a commit, verified on testnet before the next step begins.

1. **Projection registry package** (`internal/projections/`). New package, no dependencies on reputation. CI check, startup check, health check, tests. Does not yet register anything.
2. **Retrofit existing stores** into the registry. `EscrowStore`, `LedgerStore`, `CalibrationStore`, `GenerationLedgerCalculator`, `BlobServingReputation`, `AgentReputation`. Each registration includes the required integration test reference. Calibration store's writer is wired in this step (closing the second audit instance of the pattern).
3. **Delete `internal/taskverification/reputation.go`**. Remove the old `ValidatorReputationStore`, the old `ValidatorQScoreFn` signature, and the old slashing references to `AgreementRate()`. Compilation fails at the settler and slashing call sites until step 4 and 5.
4. **Create `internal/reputation/` package** with `EvidenceStore`, `ReputationEvidence`, `ReputationAggregate`, `ReputationPairAggregate`, `ChallengeResolutionStore`, and `ChallengeResolutionRecord`. Integer-only. Registered as Canonical. Includes snapshot and replay logic.
5. **Rewrite `VerificationConsensusSettler.distributeByQuality`** to integer math with the new `ValidatorIndependenceWeightFn` signature. Deterministic ordering. Last-recipient-absorbs-remainder.
6. **Wire the `EvidenceStore` writer** into `TaskVerificationConsensusConsumer` at the canonical write boundary (CR-1). This is the step that makes reputation actually accumulate.
7. **Rewrite `SlashingEvaluator`** to remove automatic systematic-divergence hard-slash, add `InvestigationAlert` emission, preserve automatic equivocation slash, fix map iteration determinism in `detectEquivocators`.
8. **Add tier-2 observability endpoints** (`aet reputation inspect`, node-local HTTP) and tier-3 public stats with delay and cohort gating.
9. **Update `docs/blobsync-design.md` §6.4** to the §12 locked text.
10. **Add principle 16** to `docs/design-principles.md`.
11. **Create `docs/projection-registry.md`**.
12. **Update `docs/multi-validator-consensus-final-design.md`** to reference new projections and new slashing path.
13. **Live testnet verification** of all 20 items in §15 success criteria.
14. **Workstream closes.** Open the challenge-path workstream as the next priority.

**Non-deprioritization commitment**: the challenge-path workstream is the immediate next priority after this one ships. It is not deprioritized behind data ingestion, decentralized persistence, or any other named workstream, because the regression window in §8.4 is an accepted attack surface only for its duration. The commitment here is explicit: this workstream's HS-4 grief containment is operationally sufficient for a time-bounded gap, not for an indefinite one. The data ingestion workstream and all subsequent workstreams queue behind the challenge path until the regression window closes.

Each step is implemented by a fresh subagent under the subagent-driven-development skill from Superpowers. Each step is plan-mode-first, per CLAUDE.md §1. No step merges without its integration test passing and its live testnet verification where applicable.

---

## 18. What this document does not say

If a detail is not in this document, Claude Code should **not** invent it. Genuine gaps should stop implementation and surface a specific question to the founder for resolution. The following are deliberate deferrals:

- The exact statistical formula for alert triggering thresholds in §4.4 (C). This is deferred because alerts are logged-only in this workstream; the statistical formula is specified precisely when the challenge path workstream defines how alerts are acted on.
- The exact schema of `ChallengeResolutionRecord` beyond the key fields. Deferred to the challenge path workstream.
- Specific observability endpoint URL paths and JSON schemas. These are implementation details left to Claude Code to design consistent with the existing API style in `internal/api/server.go`.
- The exact wire format for `InvestigationAlert` events. Implementation detail, consistent with existing DAG event patterns.

---

## 19. Sign-off

This document is the locked spec. All decisions named in §0 are final. Every ChatGPT round-2 required correction is resolved. Every Grok round-2 attack is addressed (closed, mitigated, or explicitly scoped out with a tracked next-workstream handoff). Every open question from draft 1 has been locked to a specific value or resolved by reference.

Claude Code implements from this document. The founder approves section-by-section or en bloc. Implementation begins after approval.
