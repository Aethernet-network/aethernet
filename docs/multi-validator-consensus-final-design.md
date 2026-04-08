# AetherNet Multi-Validator Task Verification Consensus — Implementation Reference

**Status**: Implemented and verified on live 5-node testnet (2026-04-08T15:55:13Z)
**Commits**: 887c0e0 through 3526599 (prompts 01-09 + 3 bugfixes)
**Supersedes**: Single-validator scoring (audit at `docs/multi-validator-scoring-audit.md`)

---

## 1. The Problem

The audit at `docs/multi-validator-scoring-audit.md` (commit 9f1bb8c) revealed that AetherNet's "compound verification" thesis was not implemented:

- **Single-validator scoring.** One autovalidator scored each task. No aggregation across validators.
- **Theatrical BFT.** `verifyTaskSettlement()` unconditionally returned `true` — every validator rubber-stamped the TaskSettlement event. BFT consensus confirmed propagation, not quality agreement.
- **No diversity enforcement.** All 5 validators ran identical deterministic heuristic scoring. Agreement was tautological.
- **Local-only rejection.** `RejectSubmission()` was a local state mutation with no DAG record and no cross-node consensus.

## 2. Core Architecture

### Event Flow

```
Worker submits evidence → POST /v1/tasks/{id}/submit
  ↓
TaskSubmitted DAG event propagates via Fast Path
  ↓
TaskVerificationRoundConsumer opens a TaskVerificationRound
  (deterministic RoundID = SHA-256("tvr:" + submissionEventID))
  ↓
Each validator's MultiVoter runs its configured analyzers
  (one TaskVerificationVote DAG event per analyzer family per validator)
  ↓
TaskVerificationVoteConsumer aggregates votes into the round
  (stake-weighted pass/fail/abstain counters + family tracking)
  ↓
When BFT supermajority + diversity floor is met, round finalizes
  ↓
TaskVerificationConsensus DAG event emitted
  ↓
TaskVerificationConsensusConsumer applies v4.1 economic settlement
  (escrow release/refund/split + slashing evaluation)
```

### TaskVerificationRound

A first-class protocol object with explicit state machine:

**States**: `Open → FinalizedAccept | FinalizedReject | Disputed | Expired`

All transitions originate from Open. All other states are terminal.

**Fields**: RoundID, TaskID, SubmissionEventID, WorkerID, PosterID, Category, ValidatorSetVersion, Committee (nil = all active), AnalyzerPolicyID, DiversityFloor, AcceptanceThresholdBP, timestamps, aggregation counters (PassWeight/FailWeight/AbstainWeight), ParticipatingFamilies map, Votes slice, FinalVerdict, FinalScoreBP, FinalizationTime.

**Persistence**: BadgerDB with 4 key prefixes (`tv:round:`, `tv:by_sub:`, `tv:by_task:`, `tv:by_state:`), atomic index updates.

### TaskVerificationVote

New DAG event type (`EventTypeTaskVerificationVote`). Carries: RoundID, TaskID, SubmissionEventID, ValidatorID, Verdict ("pass"/"fail"/"abstain"), ScoreBP (0-10000), ScoreBreakdown, AnalyzerFamily, AnalyzerVersion, PolicyVersion, AnalysisArtifactHash, TimestampUnix.

**Semantic parent**: The TaskSubmitted event being voted on.

### TaskVerificationConsensus

New DAG event type (`EventTypeTaskVerificationConsensus`). Emitted when a round finalizes. Carries: RoundID, TaskID, FinalVerdict, FinalScoreBP, PassWeight, FailWeight, AbstainWeight, ParticipatingFamilies, DiversityFloorMet, VoteCount, FinalizationTimeUnix.

**Semantic parent**: The TaskSubmitted event.

## 3. Analyzer Families

Four bootstrap families with structurally independent failure modes:

| Family | ID | Method | Failure Modes |
|--------|-----|--------|---------------|
| Deterministic Heuristic | `deterministic_heuristic` | Word count, structure detection, keyword matching, formatting (wraps existing ContentVerifier/DataVerifier/CodeVerifier) | Keyword stuffing, structurally correct but factually wrong |
| Statistical Structural | `statistical_structural` | Token entropy, sentence variety, type-token ratio, section detection, citation density | Statistically diverse but factually wrong content |
| Embedding Similarity | `embedding_similarity` | TF-IDF cosine similarity between task description and submission | Topic-relevant but shallow content |
| LLM Semantic | `llm_semantic` | LLM API call with structured evaluation prompt | Hallucinated assessments, prompt injection |

**Analyzer interface**: `ID()`, `Family()`, `Version()`, `Analyze(ctx, AnalysisInput) (*AnalysisOutput, error)`, `Calibration(category) bool`.

**AnalysisInput**: TaskID, Category, TaskTitle, TaskDescription, SubmissionContent (full text via `Evidence.ResolveContent()`), EvidenceHash, SubmittedAt.

**AnalysisOutput**: ScoreBP (0-10000), ScoreBreakdown (per-dimension), Verdict, ArtifactHash (SHA-256 of analysis artifact), DurationMS.

**Registry**: `AnalyzerRegistry` holds all registered families and analyzers. `ValidatorAnalyzers(config)` resolves a validator's configured analyzers. Per-node JSON config loaded from `AETHERNET_ANALYZER_CONFIG` env var.

## 4. Diversity Floor Enforcement

**Default**: 2 distinct analyzer families must contribute pass-weight for acceptance.

**Enforced at finalization** by the Finalizer's `Evaluate()`:
- Accept requires: PassWeight ≥ BFT threshold AND `DistinctPassFamilies() ≥ DiversityFloor` AND `MedianScore(pass votes) ≥ AcceptanceScoreThreshold`
- Reject does NOT require diversity (configurable via `EnforceFailDiversity`, default false)

**Family tracking**: `ParticipatingFamilies` map accumulates pass-weight per family. `DistinctPassFamilies()` counts families with weight > 0.

## 5. v4.1 Economic Model

### Accept (Pass Verdict)

| Recipient | Share | BP | Source |
|-----------|-------|-----|--------|
| Worker | 73% | 7300 | Escrow → worker |
| Validators | 23% | 2300 | Q-weighted among pass-voting validators |
| Generation Ledger | 2% | 200 | Royalties to causal ancestors |
| Treasury | 2% | 200 | Protocol fee |

### Reject (Fail Verdict)

| Recipient | Share | BP |
|-----------|-------|-----|
| Poster | 73% | 7300 |
| Validators | 23% | 2300 (Q-weighted among fail-voting validators) |
| Treasury | 4% | 400 (2% protocol + 2% redirected gen ledger) |

### Dispute (Abstain Verdict — deadline expired without supermajority)

| Recipient | Share |
|-----------|-------|
| Worker | 36.5% (half of 73%) |
| Poster | 36.5% (half of 73%, gets extra µAET on odd amounts) |
| Treasury | 27% (23% validator + 2% gen ledger + 2% protocol) |

**Invariant**: Total distributed == escrowed budget exactly, verified across all verdict types and budget sizes.

**Validator distribution**: Q-weighted via `distributeByQuality()`. Falls back to even-split when all Q=0.

## 6. Generation Ledger Royalty Calculator

- **Depth cap**: 3 hops (non-configurable constant)
- **Decay**: Inverse-square: weight at depth d = 1/d²
- **Quality**: Q(ancestor) multiplier (currently 1.0 neutral; future: ReplicationRate + ChallengeSurvival)
- **Normalization**: Weights summed, each share = pool × (weight / totalWeight)
- **Empty ancestor set**: Full 2% pool goes to treasury
- **Rounding**: Last recipient gets remainder
- **Mandatory**: Cannot be configured or disabled per-task
- **Cycle detection**: Deferred to future prompt (TODO marker in code)

## 7. Quality Score Integration

**Validator Q** (for fee distribution):
```
Q(validator, family, category) = AgreementRate(validator, family, category)
```
- Implemented via `ValidatorReputationStore.ValidatorQScore()`
- New validators (no history): Q = 1.0 (neutral)
- Zero-agreement validators: Q = 0.01 (minimum floor, allows recovery)
- Currently only α₄ (Consistency) from the paper v4.1 formula
- TODO: α₁ (CVD_norm), α₂ (ChallengeSurvival), α₃ (ReplicationRate)

**Ancestor Q** (for Generation Ledger):
- Currently 1.0 (neutral) for all ancestors
- TODO: depends on ReplicationRate and ChallengeSurvival infrastructure

## 8. Calibration Mode

- **Scope**: Per (category × family) combination
- **Counter**: Incremented on each round finalization for that combination
- **Default threshold**: 100 rounds (configurable per-category and per-family)
- **During calibration**: Votes count normally toward consensus. Slashing disabled.
- **After calibration**: Slashing rules activate for that combination.
- **Persistence**: BadgerDB under `cal:` prefix

## 9. Conservative Slashing

### Soft Slashing (Reputation)

- **Trigger**: Validator's vote deviated from final consensus verdict
- **Effect**: Lower AgreementRate → lower Q → lower fee share in future settlements
- **No stake impact**

### Hard Slashing (Stake)

- **Equivocation**: Same validator + same family + conflicting votes. Penalty: 30% stake (3000 BP).
- **Systematic divergence**: Agreement rate < 30% over 50+ votes after calibration. Penalty: 10% stake (1000 BP).
- **Challenge window**: 60 seconds (testnet)

Both soft and hard slashing are skipped during calibration phase.

## 10. Locked Design Decisions

1. **Verification rounds are first-class protocol objects** with explicit state machine, persistence, and deterministic IDs.
2. **Both acceptance AND rejection require BFT consensus.** No local-only mutation paths.
3. **Three layers of consensus**: Verdict (supermajority), Score (median of pass votes), Analyzer-policy (diversity floor).
4. **Analyzer-family diversity floor enforced at finalization.** Acceptance requires ≥2 distinct families.
5. **Day-one analyzer registry with developer extensibility.** Four bootstrap families + registration interface.
6. **Family identity is methodology-based.** Two Claude-based analyzers = one family. Diversity counts methods, not implementations.
7. **Committee abstraction with bootstrap mode.** Committee = nil means all active validators. Config flag for future committee selection.
8. **No human-in-the-loop.** Disputes auto-resolve via 50/50 escrow split.
9. **Conservative slashing.** Soft for disagreement (reputation only), hard only for provable bad faith (equivocation, systematic divergence).
10. **Calibration per analyzer family per category.** First 100 rounds without slashing.
11. **Recognition fabric is the substrate.** Round opening, vote aggregation, finalization, and settlement all flow through CommitConsumer implementations.
12. **Reuses existing infrastructure.** No new BFT engine, event bus, or signing scheme. Uses existing VotingRound, OCS, identity, ledger, settlement, and Fast Path primitives.

## 11. Testnet Analyzer Configuration

| Node | IP | Families |
|------|-----|----------|
| 1 | 44.200.60.102 | deterministic_heuristic + statistical_structural |
| 2 | 3.87.68.158 | deterministic_heuristic + embedding_similarity |
| 3 | 100.27.227.231 | embedding_similarity + statistical_structural |
| 4 | 3.232.95.111 | deterministic_heuristic + embedding_similarity |
| 5 | 32.195.67.127 | statistical_structural + embedding_similarity |

Every family covered by ≥2 nodes. Every node runs ≥2 families. Diversity floor of 2 is satisfiable but not trivial.

## 12. Implementation Commit History

| Prompt | Commit | Description |
|--------|--------|-------------|
| 01 | `887c0e0` | TaskVerificationRound model and BadgerDB persistence |
| 02 | `0259209` | Open round on TaskSubmitted recognition |
| 03 | `5443a2c` | TaskVerificationVote event, aggregator, vote consumer |
| 04 | `017fb68` | Bootstrap analyzer registry with four families |
| 05 | `bac0ea5` | MultiVoter: emit votes per family, deprecate unilateral path |
| 06 | `f12b1e3` | Finalizer with BFT supermajority + diversity floor |
| 07 | `7964e84` | v4.1 economic settlement (73/23/2/2) |
| 08 | `34b2c85` | Calibration, reputation, Q-weighted distribution |
| 09 | `39372d4` | Conservative slashing |
| 09 | `60477d3` | Deprecated code cleanup |
| fix | `0e6d8cc` | Deferred activation signals not retroactive |
| fix | `1cfb8ed` | commitBus.Stop() in startStack killed workers |
| fix | `3526599` | Consensus consumer skipped settlement when round already finalized |

## 13. Bugs Found During Live Testnet Verification

Three critical bugs were invisible in unit tests and only revealed under live cross-node conditions:

1. **Deferred activation signals are not retroactive** (`0e6d8cc`). The round consumer's `Ready()` deferred on a `task_metadata` signal that had already fired when TaskPosted was applied. Fix: `Ready()` always returns true for events with guaranteed causal ancestors.

2. **`defer commitBus.Stop()` in `startStack` killed workers** (`1cfb8ed`). The recognition fabric's commit bus had been dead since it was first wired. `defer` in a function that returns immediately fires before traffic arrives. Fix: bus lifecycle managed by the caller's signal wait loop.

3. **Consensus consumer idempotency check skipped settlement** (`3526599`). The vote consumer finalized the round (emitting the consensus event) but did NOT invoke settlement. The consensus consumer checked `round.IsTerminal()` and returned nil — skipping the settler. Fix: always invoke the settler; it has its own idempotency via task terminal state.

All three are documented in `docs/lessons.md`.
