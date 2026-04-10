# BlobSync + RoundProgress + RoundPolicy — Locked Design

**Status**: LOCKED pending second-pass reviews by ChatGPT and Grok. After second-pass sign-off and Mike approval, becomes `docs/blobsync-design.md` and is cited verbatim by all implementation prompts.
**Date**: 2026-04-09
**Authors**: Claude (architect, synthesis), ChatGPT (architectural review), Grok (adversarial review)
**Binding documents**: `CLAUDE.md`, `docs/design-principles.md` (15 principles), `docs/lessons.md`, `docs/multi-validator-consensus-final-design.md`, `docs/multi-validator-scoring-audit.md`
**Supersedes**: `docs/blobsync-design-draft.md` (rejected by both reviews for the reasons documented below)

---

## 1. Synthesis Summary

The first-pass reviews from ChatGPT (architectural rigor) and Grok (adversarial red-team) both returned REVISE on the original BlobSync + ValidatorRoundState draft. They found, independently, that the fundamental instinct was correct — the protocol needs cross-node blob availability, state communication, and adaptive round finalization — but the original factoring contained one critical error and several attack surfaces. The critical error, identified by ChatGPT: the draft treated validator durative state as a DAG event type, which would have polluted canonical history with ephemeral liveness chatter and melted at data ingestion scale. The primary attack surface, identified by Grok: self-reported ETAs on state updates created a trivial zero-cost denial-of-service that would turn adaptive timing into a 5-minute static timeout. Both findings pointed at the same underlying architectural insight from different angles, and resolving them required a deeper rework than either review proposed in isolation.

This locked design adopts ChatGPT's three-subsystem factoring — BlobSync for data availability, RoundProgress as a signed control-plane channel, and RoundPolicy as the finalization decision layer — and incorporates Grok's hardening requirements (rate limiting, observable-evidence requirement, cold-start bootstrap mode, BFT-supermajority partition safety, serving reputation tracking, backpressure via existing MsgOverloaded, monotonic state machine with lease expiry). Six new primitives are introduced: BlobRef as the typed reference object, BlobClass distinguishing consensus-blocking blobs from optional ones, ProgressLease enforcing monotonic advancement for any round-state claim, HolderHint as a signed peer advertisement, RoundProgressSnapshot as the persisted latest state per (round, validator, analyzer_family), and BlobFetchPolicy controlling fanout, retry budget, and priority. Final round outcomes remain DAG-anchored via the existing recognition fabric; progress chatter lives only on the control plane; self-reported state informs but does not authorize consensus decisions.

The design is verified by the reference test: a real BFT-vs-Nakamoto consensus task posted to the 5-node testnet, claimed by a worker, executed via the Claude API producing real content, submitted for verification, scored by all participating validators across multiple analyzer families with blobs replicated cross-node via BlobSync, finalized as accept via progress-aware adaptive finalization, and settled with the worker receiving exactly 73% of escrow to the µAET. This test has never passed. It must pass after implementation. Implementation is staged into seven prompts following the pattern used for the multi-validator consensus suite, with live testnet verification as the final gate for the last prompt.

---

## 2. Findings Reconciliation Table

Every finding from both first-pass reviews, resolved. Where a single finding is addressed in multiple places, all locations are listed.

| Finding ID | Source | Severity | Finding Summary | Final Resolution | Where Applied |
|------------|--------|----------|-----------------|------------------|---------------|
| C1 | Grok | Critical | Byzantine state-update DoS via fake long ETAs | Adopt principle 15 (observable evidence over self-reports). ProgressLease primitive requires monotonic advancement; ETAs are clamped to observable network norms; stale leases expire; self-reports are hints not authority. Rate limit state updates per (validator, round, family). | §5 (ProgressLease, §6.4), §7 (RoundPolicy finalization), Prompt 4, Prompt 6 |
| C2 | Grok | Critical | Index poisoning + discovery broadcast storm at ingestion scale | Origin-first fetch, broadcast is fallback only. Explicit blob locators carried in manifests (data ingestion concern, flagged forward). Promise-with-timeout and blacklist-on-failure for lying peers. Bounded fanout for discovery. HolderHint primitive with TTL. | §5 (HolderHint, BlobFetchPolicy), §6.2 (BlobSync discovery), Prompt 2, Prompt 3 |
| H3 | Grok | High | Cold-start failure for new validators overwhelmed by fetch demands | Bootstrap mode: new validators fetch only blobs for events they are actively voting on. Historical blobs fetched lazily on reference. No eager backfill. | §6.2 (BlobSync cold start), Prompt 2 |
| H4 | Grok | High | Partition healing produces conflicting TaskVerificationConsensus events | Verification rounds require BFT supermajority of the *full active validator set*, not a local majority. During partition, neither side finalizes unless it has a full-set supermajority. On healing, convergent state. This matches existing OCS invariant. | §7 (finalization rule), §9 (partition behavior) |
| M5 | Grok | Medium | No incentive for honest blob serving | Serving reputation tracked via existing ValidatorReputationStore as a new dimension. Micro-payment deferred to future economic workstream but explicitly named as a gap. Non-servers lose reputation → lose Q-weighted fee share. | §6.2 (BlobSync serving reputation), §11 (future work) |
| M6 | Grok | Medium | Recognition fabric overload under high blob demand | Separate BlobSync demand queue with its own worker pool and explicit backpressure via existing MsgOverloaded. BlobDemandConsumer batches where possible. | §6.1 (BlobSync demand queue), Prompt 3 |
| L7 | Grok | Low | State update event volume at scale | Moot under final factoring: progress is not a DAG event type. Progress messages rate-limited per (validator, round, family) at max 1 per 10s, with early terminate when state doesn't materially change. | §5 (RoundProgressSnapshot), §6.3 |
| L8 | Grok | Low | Cycle detection for state-update spam | Monotonic state machine (Acknowledged → FetchingBlob → Analyzing → VoteEmitted, no backwards transitions). Per-round per-validator update counter with cap. Stale leases expire. | §5 (ProgressLease), §6.3 |
| 8.1.1 | ChatGPT | Critical | Draft factoring (BlobSync + ValidatorRoundState) is wrong — should be three subsystems | Adopted: BlobSync + RoundProgress + RoundPolicy. Each has a single concern and is independently testable. | §4, §5, §6 |
| 8.1.2 | ChatGPT | High | BlobReferences() on event interface pollutes event.Event | Adopted: BlobRef extractor registry keyed by (event_type, payload_version). event.Event stays clean. | §5 (BlobRef), §6.1 (registry), Prompt 1 |
| 8.1.3 | ChatGPT | Medium | Recognition fabric not suitable for high-frequency progress chatter | Moot under final factoring: progress lives on control plane, not DAG. Recognition fabric only handles durable facts: BlobDemandConsumer (new) and TaskVerificationConsensusConsumer (existing). | §6.1, §6.3 |
| 8.1.4 | ChatGPT | High | Missing primitives: BlobRef, BlobClass, ProgressLease, HolderHint, RoundProgressSnapshot, BlobFetchPolicy | All six adopted as first-class types with full specifications. | §5 (all six) |
| 8.1.5 | ChatGPT | High | Validator round-state should be stream-for-transport + snapshot-for-state, not in-round ad hoc map | Adopted: signed control-plane messages for transport, persisted latest-state snapshot store for state. Finalizer reads the snapshot, not the stream. | §6.3 (RoundProgress), §7 (RoundPolicy) |
| 8.1.6 | ChatGPT | — | Is the larger scope justified? | Yes, with the corrected factoring. BlobSync-only is insufficient; the draft's larger scope had the right instinct but wrong implementation. | §4 |
| 8.1.7 | ChatGPT | — | Package layout | Adopted verbatim with minor adjustments. internal/blobsync/, internal/roundprogress/, extensions to internal/blobstore/ and internal/taskverification/. No modifications to internal/event/ beyond the BlobRef extractor registry. | §8 (package layout) |
| 8.1.8 | ChatGPT | Medium | Design must generalize to future analyzer families requiring external resources | RoundProgress + ProgressLease primitive naturally serves blob fetching, analyzer warmup, external DB lookup, API-dependent analyzers, ingestion-side expensive verification. No special cases for blob fetching. | §6.3 (RoundProgress generality) |
| Both | Grok + ChatGPT | — | Broadcast discovery cannot be the default at scale | Origin-first with bounded fanout discovery as fallback, explicit holder hints for ingestion workloads. | §6.2 (BlobFetchPolicy), forwarded to data ingestion design |
| Both | Grok + ChatGPT | — | The multi-validator suite had an invisible single-validator assumption at the storage layer | Lesson captured in docs/lessons.md (cross-node content availability audit requirement). This design closes the gap. | `docs/lessons.md` addition 1 |
| New | Synthesis | — | The original draft put liveness chatter on the DAG, which was a factoring error | Lesson captured in docs/lessons.md (ledger state vs control plane state distinction) with explicit test: "does a replaying node need to know this happened?" | `docs/lessons.md` addition 2 |
| 9a | Grok Q9 | Medium | BlobDemandConsumer must use always-ready pattern, not deferred activation | Already addressed in §6.1 item 1: consumer is explicitly always-ready per lesson from commit `0e6d8cc`. Verified in Prompt 3 tests. | §6.1, Prompt 3 |
| 9b | Grok Q9 | Medium | Origin must persist blob to local BlobStore before publishing TaskSubmitted | Already true in existing API handler but now stated as explicit invariant in §6.2 (BlobStore extensions). Per design principle 9 (persist before publish). | §6.2, invariant documentation |
| 9c | Grok Q9 | Medium | Abstain-due-to-unavailability must not penalize Q-score or distort Generation Ledger royalties | Addressed in §6.4: abstain-due-to-unavailability excluded from agreement rate; Generation Ledger computed over participating voters only. | §6.4, reputation and royalty rules |
| SP-1 | ChatGPT 2nd | High | RoundProgress must not become authoritative consensus input — progress is liveness-only | Added authority boundary invariant at top of §6.4: progress can shorten waiting or trigger expiry-to-dispute, but accept/reject derive from durable votes only. Step 4 rewritten to explicitly prevent stale validators from fabricating weight. | §6.4 (authority boundary invariant, step 4, abstain handling) |
| SP-2 | ChatGPT 2nd | High | ProgressEvidence is underspecified — phase-specific evidence rules needed | Added per-phase evidence table to §5.3 defining acceptable evidence for each ProgressPhase, max lease extension per phase, and defense against fabricated evidence. | §5.3 (phase-specific evidence table) |
| SP-3 | ChatGPT 2nd | Medium | RoundPolicy must be generalized into its own package, not buried in taskverification | Added `internal/roundpolicy/` to package layout. Generic timing logic (all terminal? secured? active leases? stale? backstop?) lives there. Task-specific rules (diversity floor, median score, dispute) stay in taskverification. Finalizer calls `roundpolicy.Evaluator`. | §6.4 (placement), §8 (package layout) |

---

## 3. Areas of Agreement

Both reviewers independently converged on the following points. These are consensus architecture and should be treated as settled.

**The larger scope is correct.** BlobSync alone is insufficient because it leaves round finalization on static timeout mechanics, violating design principles 2 and 3. The ValidatorRoundState primitive (by whatever name, in whatever form) must be built in the same workstream as blob replication because the two concerns are coupled: fetches take real time, rounds must know whether a validator's silence means "working honestly on a slow fetch" or "byzantine and ignoring the round." Splitting them into separate workstreams would require rebuilding round timing twice.

**Reuse transport mechanism, separate transport channel.** The body plane's peer pool, framing, backpressure, and connection management are sound infrastructure. BlobSync and RoundProgress should reuse them. But the wire-level *channels* (message types, queues, flow control) must be distinct so that blob transfer cannot block body-plane traffic and progress messages cannot block blob transfer. This is principle 7 applied consistently: one transport implementation, many channels.

**Self-reported state cannot be trusted blindly.** Grok found the attack directly (fake ETA DoS); ChatGPT found the architectural remedy (ProgressLease with monotonic advancement). The two findings converge on a single new design principle, which has been added to `docs/design-principles.md` as principle 15: observable evidence beats self-reported claims. The ProgressLease primitive is the protocol-level enforcement of this principle for round state.

**Broadcast discovery cannot be the default at scale.** At 5-node testnet scale, broadcasting "who has blob hash X" to all validators is acceptable. At 5.4-million-blob ingestion scale, it melts. The default must be origin-first with explicit holder hints, and broadcast must be a bounded-fanout fallback only. The data ingestion design will carry explicit blob locators in submission manifests, so most fetches on the ingestion path will not require discovery at all.

**Partition behavior demands the BFT supermajority rule.** Verification rounds cannot finalize on local majority during partition; they must require BFT supermajority of the *full* active validator set. This preserves safety at the cost of liveness during partition, which is the correct tradeoff for a BFT protocol. It also matches the existing OCS invariant (per CLAUDE.md key invariants) that BFT supermajority is computed over total active validator stake, not received votes.

**All blob references are not equal.** Evidence blobs required for scoring are consensus-blocking: no validator can vote without them. Auxiliary blobs (diagnostic artifacts, archival completeness, informational citations) are not. The BlobClass primitive distinguishes them so BlobDemandConsumer knows which fetches are critical and RoundPolicy knows which absences can be tolerated versus which must block finalization.

**The recognition fabric owns durable fact recognition, not liveness chatter.** The fabric's contract is "every committed event of interest reaches every consumer that cares." That contract is valuable precisely because the fabric handles committed events, not ephemeral coordination. Putting progress pulses through the fabric would dilute its purpose and pollute its performance characteristics. The fabric handles BlobDemandConsumer (a new consumer that reacts to committed events referencing blobs) and TaskVerificationConsensusConsumer (existing). Progress messages do not touch the fabric.

**Content addressing is the integrity model for blobs.** Hash-verified on receipt, mismatch means drop and retry, no trust assumptions about peers serving bytes. This is existing protocol practice and both reviewers explicitly confirmed it as correct for BlobSync.

---

## 4. Areas of Divergence and Resolution

Where the two reviews proposed different fixes for the same underlying issue, or where one reviewer went further than the other. Each divergence is resolved with explicit reasoning.

| Divergence | Grok Position | ChatGPT Position | Final Resolution | Reasoning |
|------------|---------------|------------------|------------------|-----------|
| **Byzantine state-update DoS fix** | Rate-limit state updates per (validator, round, family). Soft-slash reputation on chronic ETA violations (>30% miss rate across rounds). | ProgressLease primitive with monotonic advancement requirement. Self-reports are hints, not authority. Observable progress wins when self-report and evidence disagree. | Adopt ChatGPT's structural fix (ProgressLease) as primary mechanism, Grok's rate limiting and reputation tracking as hardening on top. Combining both is stronger than either alone: the lease prevents the attack structurally; rate limiting caps the overhead of even attempted attacks; reputation tracking makes repeat attacks economically irrational. | Structural fixes close classes of attack; defensive fixes harden the implementation. A design that does both is more robust than one that picks. Principle 15 is the general form. |
| **Partition healing resolution** | Deterministic tiebreak by CVD-weighted vote sum. On repair, fabric re-evaluates round if a higher-quality consensus event arrives. | Progress is control-plane only, so partition heal is trivial for progress. For finalized outcomes, normal BFT safety: late votes recorded but don't change finality. | Resolve via the full-set BFT supermajority rule: no round finalizes on less than 2/3+1 of the full active validator set. During partition, neither half finalizes unless it has the full-set supermajority. On healing, there is nothing to reconcile because there was no divergent finalization. This is stronger than either proposed fix and reuses existing OCS infrastructure. | Grok's CVD-weighted tiebreak depends on infrastructure that doesn't exist yet (full Q-score formula requires CVD tracking which is future work). ChatGPT's approach is sound for progress but doesn't directly address finalization under partition. The full-set supermajority rule is a protocol-level invariant that makes the divergence impossible in the first place, which is cleaner than reconciling after the fact. |
| **Blob serving incentive** | Micro-payment from treasury for successful serves, tracked via existing settlement paths. | Not addressed directly in first pass. | Adopt serving reputation tracking now (new dimension on ValidatorReputationStore). Defer micro-payment to future economic workstream. Flag as known gap in §11. | Tracking costs nothing and creates accountability. Micro-payment requires economic design work that shouldn't block this workstream. The reputation mechanism alone closes the free-riding vector because chronic non-servers lose Q-weighted fee share, which is economic feedback even without direct payment. |
| **Cold-start bootstrap for new validators** | CVD-prioritized bootstrap: fetch high-CVD (most referenced) blobs first. Use existing checkpoint bootstrap to include a recent blob manifest. | Not addressed in first pass beyond the general partition/bootstrap answer. | Simpler: new validators fetch only blobs for events they are actively voting on. Historical blobs fetched lazily on reference. No eager backfill. No CVD prioritization in v1. | CVD prioritization depends on the full Q-score infrastructure which is future work. The simpler rule (fetch only what you vote on) is sufficient for testnet and for the initial data ingestion workload. It can be upgraded to CVD-prioritized bootstrap later if measurement shows it's needed. Simpler answer first, per principle 12. |
| **Should RoundProgress live in its own package or start inside taskverification?** | Not addressed. | Own package: `internal/roundprogress/`. | Own package from day one. Data ingestion will introduce a second round type (claim verification) within weeks, and the progress primitive should already be general when the second consumer arrives. | Per principle 6 (generalize the primitive). The cost of generalizing now is small; the cost of extracting later is larger and risks coupling to taskverification specifics during the transition. |
| **Is BlobSync one subsystem or two?** | Not addressed directly. | Distinguish availability detection from transfer execution conceptually, even if packaged together. | Single package `internal/blobsync/` with two clearly-separated internal concerns: the `BlobSyncEngine` (fetch coordinator) uses the `HolderHintCache` (availability) and the `BlobTransport` (transfer). Same package, distinct files, clear boundary. | Principle 12 (beauty is a correctness signal). Packaging them together keeps the integration simple while the conceptual separation stays visible in the file layout. If the concerns need to split into separate packages later, the internal boundary makes the split clean. |

---

## 5. New Primitives Introduced

Full specifications for each of the six new primitives.

### 5.1 BlobRef / BlobDescriptor

A first-class typed reference to a blob, replacing raw hash strings wherever the protocol needs to know about a blob's existence, class, and metadata.

```go
// package internal/blobstore

type BlobRef struct {
    Hash                 [32]byte  // SHA-256 of canonical blob bytes
    Kind                 BlobKind  // see §5.2
    SizeHint             uint64    // bytes, 0 if unknown
    RequiredForConsensus bool      // derived from Kind but stored for quick access
    OriginNodeHint       string    // validator ID that published the referencing event, empty if unknown
    PersistenceClass     uint8     // see below
}

type BlobKind uint8

const (
    BlobKindEvidence      BlobKind = iota // submission evidence, consensus-blocking
    BlobKindManifest                      // data ingestion manifest, consensus-blocking
    BlobKindMethodology                   // methodology documentation, informational
    BlobKindTrajectory                    // trajectory artifact, consensus-blocking if primary
    BlobKindCitation                      // referenced prior work, informational
    BlobKindDiagnostic                    // analyzer output artifact, informational
    BlobKindArchival                      // historical record, optional
)
```

**Persistence class** is a hint to BlobSync about how aggressively to cache and replicate:

- `0 = hot`: fetch immediately on demand, cache indefinitely, replicate eagerly
- `1 = warm`: fetch on demand, cache with LRU eviction, replicate lazily
- `2 = cold`: fetch only when explicitly needed, may be garbage collected after use

For v1, all consensus-blocking blobs are hot and all others are warm. Cold-class blobs are a future optimization.

**Rule**: events carry BlobRef lists via the extractor registry (§6.1), not direct fields on the canonical event struct. The canonical event struct stays clean; blob references are derived from event payloads by extractors registered per `(event_type, payload_version)`.

### 5.2 BlobClass (Consensus-Blocking Flag)

A derived property of every BlobRef, computed from its `Kind`. Determines how the protocol reacts to the blob being unavailable.

```go
func (b BlobRef) ConsensusBlocking() bool {
    switch b.Kind {
    case BlobKindEvidence, BlobKindManifest, BlobKindTrajectory:
        return true
    default:
        return false
    }
}
```

**Consensus-blocking blobs**: a validator cannot emit a non-abstain vote without having local access to the content. Absence of the blob means the validator either abstains (after BlobSync exhausts fetch retries) or waits (while BlobSync is still trying). The round's RoundPolicy waits on consensus-blocking blobs per the adaptive finalization rule.

**Non-consensus-blocking blobs**: the validator can proceed without them. Absence affects observability, archival completeness, or analyzer quality, but does not prevent a vote. RoundPolicy does not wait on these.

This distinction prevents BlobDemandConsumer from over-fetching and lets RoundPolicy know which absences must block finalization versus which are cosmetic.

### 5.3 ProgressLease

The primitive that defeats self-reported DoS attacks. A validator cannot keep a round open indefinitely by claiming "still working" — the lease enforces monotonic advancement.

```go
// package internal/roundprogress

type ProgressLease struct {
    RoundID            string
    ValidatorID        string
    AnalyzerFamily     string // empty if round-level rather than family-level
    Phase              ProgressPhase
    PhaseEnteredUnix   int64
    LeaseExpiryUnix    int64  // absolute expiry, extended only by monotonic advancement
    LastObservedProgress []byte // hash of last observed progress evidence (see §6.3)
    ProgressGeneration uint64 // incremented on every monotonic advancement
}

type ProgressPhase uint8

const (
    ProgressPhaseAcknowledged ProgressPhase = iota
    ProgressPhaseFetchingBlob
    ProgressPhaseAnalyzing
    ProgressPhaseScorePending
    ProgressPhaseVoteEmitted
    ProgressPhaseAbstained
    ProgressPhaseFailed
)
```

**Lease rules**:

1. A lease is created when a validator first emits an `Acknowledged` state for a round.
2. A lease's `LeaseExpiryUnix` extends only when the validator advances `ProgressGeneration` with observable evidence of forward progress. Re-emitting the same phase with the same generation does not extend the lease.
3. Phase transitions are monotonic: `Acknowledged → FetchingBlob → Analyzing → ScorePending → VoteEmitted` (or `Abstained`, or `Failed`). Backwards transitions invalidate the lease.
4. `ProgressGeneration` is a monotonically increasing counter. Evidence that must accompany each generation bump:
   - `FetchingBlob`: generation may bump on evidence of new bytes received for the target blob (hash of bytes received so far is included in the progress message).
   - `Analyzing`: generation may bump on evidence that an analyzer step has completed (intermediate hash of analysis artifact).
   - `ScorePending`: generation may bump once, on the transition into this phase. No further bumps allowed.
5. An expired lease means the validator's state is stale. RoundPolicy treats stale-lease validators as "silent without state update" for finalization purposes.
6. Leases are enforced by the `RoundProgressStore` (§6.3). Out-of-order or non-monotonic updates are rejected at ingestion, not at finalization.

**This is the protocol-level enforcement of design principle 15.** Self-reports are hints; leases require corroboration by monotonically advancing evidence.

**Phase-specific observable evidence rules for lease renewal**:

| ProgressPhase | Acceptable Evidence for Generation Bump | Max Lease Extension | Notes |
|---------------|----------------------------------------|---------------------|-------|
| `FetchingBlob` | Hash of cumulative verified bytes received so far (must increase monotonically across bumps). Evidence that actual bytes are arriving, not just a repeated claim. | Clamped ETA or 30s, whichever is shorter | A validator that claims "fetching" but whose cumulative byte hash never changes has fabricated the claim. Lease expires without extension. |
| `Analyzing` | Hash of intermediate analysis artifact (must differ from previous bump). Evidence that the analyzer has produced new intermediate output. | Clamped ETA or 60s, whichever is shorter | Analyzer stage completion markers. A validator that claims "analyzing" but produces the same artifact hash repeatedly is not progressing. |
| `ScorePending` | No repeated lease extensions allowed. Single bounded lease window on entry to this phase. | 10s fixed, non-renewable | Near-terminal phase. The validator either emits a vote or transitions to Failed/Abstained. |
| `VoteEmitted` | Terminal. No lease extension. | N/A | Vote is on the DAG. |
| `Abstained` | Terminal. No lease extension. | N/A | Signed terminal state in snapshot. |
| `Failed` | Terminal. No lease extension. | N/A | Logged for diagnostics. |

A byzantine validator that fabricates "progress-shaped" evidence (random hashes on each bump) will produce generation bumps that look superficially valid. The defense is that fabricated progress does not produce a vote, so the lease eventually expires to ScorePending (bounded) or is caught by the absolute backstop. The cost to the attacker is the lease extension window (at most 30-60 seconds per phase, capped); the cost to the protocol is a slightly delayed round finalization, bounded by the cumulative maximum lease time across all phases. This is acceptable because the attacker cannot extend the round indefinitely — each phase has a hard maximum, and the phases are monotonic (no backwards transitions).

### 5.4 HolderHint

A signed peer advertisement that a given peer has a given blob, with a TTL. Used by BlobSync's `HolderHintCache` to skip discovery in the common case.

```go
// package internal/blobsync

type HolderHint struct {
    BlobHash       [32]byte
    PeerID         string
    ValidFromUnix  int64
    ValidUntilUnix int64
    Signature      []byte // peer's signature over (hash, peer_id, valid_from, valid_until)
}
```

**Rules**:

1. Hints are produced by peers on two paths: (a) voluntary advertisement when a peer has just finished fetching or producing a blob, and (b) in response to a `BlobQuery` broadcast.
2. Hints are *non-authoritative*: a peer claiming to have a blob may be lying or may have had the blob and since deleted it. The fetcher verifies by attempting the fetch.
3. Hints expire at `ValidUntilUnix`. Expired hints are evicted from the cache.
4. A peer that repeatedly emits hints but fails to serve the corresponding blobs has its hints ignored for a cooldown period, and its serving reputation is decremented.
5. Hints are stored in an in-memory cache with bounded size and LRU eviction. Cache is non-authoritative; a missing hint means "try discovery," not "the blob doesn't exist."
6. Signatures prevent peer spoofing: a malicious peer cannot advertise a blob as held by someone else.

**Advisory-only invariant**: HolderHints are advisory routing inputs only. They cannot mark a blob as available, satisfy an EvidenceReady condition, extend a validator's progress lease, or affect round state except by causing a fetch attempt. Any code path that checks "is this blob available" must query the local BlobStore directly, never the HolderHintCache.

**Scale rule**: at ingestion scale (millions of blobs), the HolderHintCache may become a memory concern. The cache is capped at a configurable size (default 1M entries for testnet, to be tuned based on mainnet measurement). When full, LRU eviction applies. Cache misses fall through to discovery.

### 5.5 RoundProgressSnapshot

The persisted latest state per `(round_id, validator_id, analyzer_family)`. This is what the finalizer reads to make adaptive timing decisions.

```go
// package internal/roundprogress

type RoundProgressSnapshot struct {
    RoundID            string
    ValidatorID        string
    AnalyzerFamily     string
    CurrentPhase       ProgressPhase
    PhaseEnteredUnix   int64
    LastUpdateUnix     int64
    LeaseExpiryUnix    int64
    ProgressGeneration uint64
    EstimatedReadyUnix int64 // validator's self-reported ETA, clamped to protocol maximum
    ReasonCode         uint16 // enumerated reason code, see §6.3
    DiagnosticText     string // free text, not protocol-significant
}
```

**Storage**: BadgerDB under prefix `rp:snap:<round_id>:<validator_id>:<analyzer_family>`. Writes are atomic. Reads support prefix scans to get all snapshots for a given round.

**Update rules**:

1. A new snapshot replaces any existing snapshot for the same `(round_id, validator_id, analyzer_family)` tuple, provided the update is valid per ProgressLease rules.
2. Invalid updates (non-monotonic phase, regression in generation, stale generation) are rejected at the store level and logged.
3. On node restart, the snapshot store is loaded from BadgerDB; no replay is required because the snapshot is the authoritative state.
4. Snapshots for finalized rounds can be garbage collected after a retention window (default 7 days for diagnostic purposes).

**The finalizer reads the snapshot, never a stream of updates.** This is the architectural resolution of ChatGPT's factoring concern in 8.1.5.

### 5.6 BlobFetchPolicy

The parameters governing how BlobSync fetches a blob: fanout, retry budget, origin-first vs broadcast-first, chunk size, deadline class.

```go
// package internal/blobsync

type BlobFetchPolicy struct {
    MaxRetries            uint32
    InitialBackoffMs      uint32
    MaxBackoffMs          uint32
    OriginFirst           bool
    DiscoveryFanout       uint32 // how many peers to query in broadcast discovery
    ChunkSizeBytes        uint32
    PerFetchTimeoutMs     uint32 // per-attempt timeout, not total
    TotalDeadlineMs       uint32 // total time budget across all retries
    AbstainOnExhaustion   bool   // if true, emit abstain when exhausted; if false, fail hard
}

var (
    ConsensusBlockingPolicy = BlobFetchPolicy{
        MaxRetries:          5,
        InitialBackoffMs:    100,
        MaxBackoffMs:        2000,
        OriginFirst:         true,
        DiscoveryFanout:     3,
        ChunkSizeBytes:      256 * 1024,
        PerFetchTimeoutMs:   10000,
        TotalDeadlineMs:     30000,
        AbstainOnExhaustion: true,
    }
    InformationalPolicy = BlobFetchPolicy{
        MaxRetries:          2,
        InitialBackoffMs:    500,
        MaxBackoffMs:        5000,
        OriginFirst:         true,
        DiscoveryFanout:     1,
        ChunkSizeBytes:      256 * 1024,
        PerFetchTimeoutMs:   15000,
        TotalDeadlineMs:     60000,
        AbstainOnExhaustion: false,
    }
)
```

**Policy selection**: `BlobSyncEngine.Fetch(ref BlobRef)` selects the policy based on `ref.Kind`. Consensus-blocking blobs get `ConsensusBlockingPolicy`. Informational blobs get `InformationalPolicy`. Policies are tunable per deployment via config (testnet, staging, mainnet may each have different numbers).

**Total deadline rule**: `TotalDeadlineMs` is the absolute upper bound on how long BlobSync spends on any single blob before giving up. For consensus-blocking blobs, exhaustion triggers an `Abstained` progress phase on the validator, which the RoundPolicy treats as a terminal state and the round can proceed to finalization without waiting for the abstaining validator further.

---

## 6. The Locked Architecture

Three subsystems, each with a single concern, composed via clean interfaces. The architecture replaces the two-subsystem draft with three. This is the core factoring change.

### 6.1 BlobSync — Data Availability Plane

**Responsibility**: ensure that any node that needs a blob's content can obtain it within a bounded time budget, using origin hints and peer discovery over a dedicated transport channel.

**Components**:

1. **`BlobDemandConsumer`**: a `CommitConsumer` registered on the recognition fabric. On any committed event, it looks up the event's BlobRefs via the extractor registry, checks the local `BlobStore` for each, and for each missing blob enqueues a fetch request via `BlobSyncEngine`. The consumer is always-ready (no deferred activation — per the lesson from commit `0e6d8cc`) and performs its check synchronously at consume time. Idempotent: re-consuming the same committed event does not duplicate fetch requests because the engine's in-flight tracking deduplicates by hash.

2. **`BlobSyncEngine`**: the fetch coordinator. Holds the demand queue (a bounded priority queue, consensus-blocking blobs prioritized), the in-flight fetch table (keyed by hash, deduplicates concurrent requests), the HolderHintCache (§5.4), retry/backoff state per blob, and the BlobFetchPolicy selector. Single instance per node. Lifecycle managed by the same signal wait loop that manages the commit bus (per the lesson from commit `1cfb8ed`).

3. **`BlobTransport`**: the wire-level protocol handler. Reuses the existing peer connection pool, framing, identity verification, and rate limiting from the body plane. New message types: `BlobQuery` (do you have hash X?), `BlobQueryResponse` (yes/no, with optional HolderHint signature), `BlobRequest` (send me hash X), `BlobChunk` (streaming chunk of hash X), `BlobComplete` (transfer done, here is total hash). Operates on its own logical channel within the existing transport — a distinct message type namespace and its own flow control queue.

4. **`HolderHintCache`**: in-memory cache of signed holder hints (§5.4), bounded size with LRU eviction, hints expire at their `ValidUntilUnix`. On cache miss, BlobSyncEngine falls through to discovery.

5. **`BlobRefRegistry`**: the extractor registry keyed by `(event_type, payload_version)`. Each registered extractor takes a raw event payload and returns a `[]BlobRef`. New event types register their extractors at startup. This keeps `event.Event` clean of blob-specific methods (resolving ChatGPT 8.1.2).

6. **`BlobServingReputation`**: a new dimension on `ValidatorReputationStore`, tracking per-peer serve success rates. Peers with chronic serving failures lose reputation, which propagates to the existing Q-weighted fee distribution. This is the v1 incentive for honest serving; direct micro-payments are deferred.

**Discovery algorithm** (origin-first, bounded fallback):

1. BlobSyncEngine receives a fetch request for `BlobRef{hash, origin_hint, ...}`.
2. If the HolderHintCache has a non-expired hint for this hash, use it directly (step 4).
3. Otherwise, if `origin_hint` is non-empty, send `BlobRequest(hash)` directly to the origin. Skip discovery.
4. Send `BlobRequest` to the chosen peer.
5. If the request fails (timeout, bad hash, peer unreachable), decrement that peer's serving reputation and fall through to discovery.
6. Discovery: broadcast `BlobQuery(hash)` to at most `DiscoveryFanout` peers, chosen by lowest-latency or highest-reputation heuristic (v1: random choice from active peers; v2: reputation-weighted).
7. First peer to respond with an affirmative `BlobQueryResponse` becomes the fetch target.
8. Retry with backoff up to `MaxRetries` or until `TotalDeadlineMs` elapses.
9. On success, verify hash, write to `BlobStore`, emit a voluntary `HolderHint` to the cache marking the local node as a holder, and signal any pending progress-aware consumers.
10. On exhaustion, mark the fetch as failed; if the blob is consensus-blocking, signal the relevant validator's MultiVoter to transition to `Abstained`.

**Cold-start rule**: new validators joining the network do not eagerly backfill historical blobs. They fetch only blobs referenced by events they are actively voting on, which is determined by the round committee selection. Historical blobs (referenced by events from before the validator joined) are fetched lazily on reference — for example, if a challenge re-opens an old round and the validator is asked to re-score, the fetch happens then. This bounds the bootstrap fetch load and prevents the "new validator overwhelmed" scenario from Grok's H3 finding.

**Scale behavior**: at testnet scale (5 nodes, thousands of blobs), the HolderHintCache is trivially small and discovery broadcasts are acceptable. At data ingestion scale (millions of blobs), the cache is capped at 1M entries and most fetches proceed via explicit origin hints provided by the data ingestion manifest (a forward-compatibility hook that the data ingestion design will exercise). Broadcast discovery is a fallback, not the default.

### 6.2 BlobStore (Extensions)

**Responsibility**: local content-addressed storage of blob bytes. Existing interface plus extensions for BlobSync integration.

**Existing (preserved)**:
- `Get(hash [32]byte) ([]byte, error)`
- `Put(bytes []byte) ([32]byte, error)`
- `Has(hash [32]byte) bool`
- FSStore implementation persisting to disk under content-addressed paths

**New**:
- `StreamingPut(hash [32]byte, chunks <-chan []byte) error`: used by BlobTransport to write chunks as they arrive, with hash verification at completion
- `PutVerified(ref BlobRef, bytes []byte) error`: writes bytes and verifies they match `ref.Hash`; rejects on mismatch
- `Subscribe(hash [32]byte) <-chan struct{}`: returns a channel that closes when the blob becomes available locally; used by MultiVoter's retry loop to be woken immediately when BlobSync completes a fetch
- `WaitForBlob(ctx context.Context, hash [32]byte) error`: blocks until the blob is locally available or the context expires

**Persistence is unchanged**: blobs continue to live in `/data/aethernet/blobs/` under content-addressed paths. BlobSync writes are no different from existing local writes from the BlobStore's perspective.

**Origin persist-before-publish invariant**: the node that receives a worker's evidence submission (the origin) MUST persist the blob to its local BlobStore BEFORE publishing the `TaskSubmitted` event that references it. This is already true in the existing API handler (`api/server.go` step 2: BlobStore stores evidence body locally, step 3: TaskSubmitted event emitted), but it is stated here as an explicit invariant because BlobSync depends on it. If the origin crashes between persist and publish, the blob is safe on disk and the event was never published. If the origin crashes after publish but before any peer has fetched the blob, the blob is recoverable from the origin's disk on restart. If the origin's disk fails, the blob is lost and the round will resolve as dispute — which is correct behavior for irrecoverable data loss. Per design principle 9 (persist before publish), this ordering is mandatory and must be verified in any new code path that creates blob-referencing events.

**On restart**: BlobStore loads from disk as before. The HolderHintCache is cold. Incoming events that reference blobs the local store already has do not trigger fetches (BlobDemandConsumer checks `Has` first). Incoming events referencing blobs the store lacks trigger lazy fetches through the normal BlobSync path.

### 6.3 RoundProgress — Control Plane

**Responsibility**: signed durative validator state for any long-running operation inside a round, persisted as a latest-state snapshot, transported over a dedicated control-plane channel.

**Key architectural decision**: RoundProgress is **not** a DAG event type. It is a signed control-plane protocol with a persisted snapshot store. This is the correction of the factoring error in the original draft. See lessons.md addition 2 for the rationale.

**Components**:

1. **`RoundProgressStore`**: the persisted snapshot store (§5.5). BadgerDB-backed. Key prefix `rp:snap:`. Supports atomic writes, prefix scans by round ID, and expiry for finalized rounds.

2. **`RoundProgressProtocol`**: the wire-level control-plane protocol. Signed messages carrying `ProgressUpdate` payloads. Reuses the existing peer transport machinery on its own channel — same connection pool, framing, and identity verification as the body plane, but a distinct message type and queue. Rate-limited per `(validator, round, family)` at one update per 10 seconds maximum.

3. **`ProgressAggregator`**: reconciles incoming progress updates against the snapshot store, enforcing ProgressLease rules (§5.3). Rejects non-monotonic updates, stale generations, and regressions. Valid updates replace the snapshot for the corresponding key. Invalid updates are logged and increment the offending validator's anomaly counter.

4. **`LeaseEnforcer`**: background goroutine that scans the snapshot store periodically (default: every 5 seconds) for leases past their expiry. Expired leases are marked as stale; RoundPolicy treats stale leases as "validator silent" for finalization purposes.

5. **`ProgressUpdate` message structure**:

```go
type ProgressUpdate struct {
    RoundID            string
    ValidatorID        string
    AnalyzerFamily     string
    Phase              ProgressPhase
    ProgressGeneration uint64
    ProgressEvidence   [32]byte  // hash of observable progress per §5.3 rules
    EstimatedReadyUnix int64     // self-reported ETA, subject to clamping
    ReasonCode         uint16    // enumerated
    TimestampUnix      int64
    Signature          []byte    // validator's Ed25519 signature over canonical bytes
}
```

**ETA clamping rule**: the `EstimatedReadyUnix` field is a self-report and is clamped by the protocol before being stored in the snapshot:
- Maximum reasonable ETA for `FetchingBlob`: `now + max(observed_network_fetch_p99, 30s)`
- Maximum reasonable ETA for `Analyzing`: `now + max(observed_analyzer_p99_for_family, 60s)`
- ETAs beyond the maximum are clamped to the maximum, and the anomaly is logged for reputation tracking
- `EstimatedReadyUnix = 0` means "unknown," which is acceptable and does not extend leases

Observed network and analyzer p99 latencies are tracked in rolling windows in the node's metrics and updated as the network operates. At cold start, the hardcoded maximums (30s, 60s) apply.

**Per-validator rate limit**: at most 1 progress update per (validator, round, family) per 10 seconds. Excess updates are dropped at the aggregator and logged.

**Enumerated reason codes** (examples, not exhaustive):

```go
const (
    ReasonCodeUnknown               uint16 = 0
    ReasonCodeStartingRound         uint16 = 1
    ReasonCodeFetchingEvidenceBlob  uint16 = 2
    ReasonCodeFetchingManifestBlob  uint16 = 3
    ReasonCodeAnalyzerWarmup        uint16 = 4
    ReasonCodeAnalyzerRunning       uint16 = 5
    ReasonCodeExternalDBQuery       uint16 = 6
    ReasonCodeAPICallInProgress     uint16 = 7
    ReasonCodeScoring               uint16 = 8
    ReasonCodeVoteEmitted           uint16 = 9
    ReasonCodeBlobUnavailable       uint16 = 10
    ReasonCodeAnalyzerFailure       uint16 = 11
    ReasonCodeAbstained             uint16 = 12
)
```

Reason codes are the machine-readable part of progress state. `DiagnosticText` is a free-form human-readable field that the protocol ignores.

**Generality**: RoundProgress is not specific to BlobSync. It serves any long-running validator operation: blob fetching (first consumer), analyzer warmup, external database queries, API-dependent analyzers, model sandbox execution, ingestion-side expensive verification. The primitive is general from day one per principle 6.

### 6.4 RoundPolicy — The Finalization Decision Layer

**Responsibility**: decide when a verification round should finalize. Reads the RoundProgressSnapshot store and the existing vote aggregator. Adaptive — not timeout-primary.

**Authority boundary invariant**: RoundProgressSnapshot is a **liveness input only**. It cannot by itself create an accept or reject verdict. Accept and reject are functions only of durable `TaskVerificationVote` events, the validator-set snapshot at round open, the diversity floor, and score aggregation — all of which are DAG-derived per design principle 5 (the protocol is the source of truth). Progress state may only shorten waiting or trigger expiry-to-dispute. A stale or absent progress state can justify "stop waiting for this validator," but it cannot fabricate vote weight, cannot convert silence into an abstain tally entry, and cannot cause a verdict that would not be derivable from the durable votes alone. The only non-vote terminal path is Expired → Dispute.

**Placement**: the generic progress-aware round timing logic is extracted into a reusable `RoundPolicyEvaluator` in `internal/roundpolicy/`. Task-specific verdict rules (diversity floor, median score threshold, dispute semantics) remain in `internal/taskverification/finalizer.go`, which calls the generic evaluator for timing decisions. This separation ensures the data ingestion workstream can reuse the timing logic without cloning the taskverification finalizer. See package layout in §8.

**Finalization decision algorithm** (evaluated by the finalizer on each tick or on each incoming vote/progress event):

1. **Immediate finalization — all votes in**: if every active validator in the committee (or the full active set, if no committee) has emitted a terminal vote (Pass/Fail/Abstain) for the round, finalize immediately. This is the fast path.

2. **Early finalization — outcome mathematically secured**: if the current vote tally is such that the outcome cannot change regardless of how remaining validators vote (e.g., pass weight already exceeds BFT threshold + diversity floor + median score requirement), finalize immediately. Do not wait for remaining votes. They are logged but do not affect the verdict.

3. **Adaptive wait — progress is active**: if some validators have not voted but have active, non-stale progress leases, wait for them. The wait duration is bounded by the maximum `LeaseExpiryUnix` among waiting validators, plus a small margin. When the last active lease expires without a vote, transition to step 4.

4. **Stale-aware finalization**: validators with stale or absent progress are treated as silent — the round stops waiting for them. However, stopping the wait does NOT add any vote weight for these validators. The round evaluates only the durable votes it has received. If the received durable votes already satisfy BFT supermajority of the *full active validator set* (not the voted subset), the round finalizes with the verdict those votes support. If the received votes do not satisfy BFT supermajority, the round cannot finalize and waits for the absolute backstop. Stale/silent validators cannot be counted as abstains or as any other vote — their weight simply remains uncounted. This preserves the CLAUDE.md invariant that BFT supermajority is computed over total active validator stake, not received votes.

5. **Absolute backstop**: 5 minutes from round open. Hard upper bound. If this expires without BFT supermajority being reached in any direction (pass, fail), the round resolves as `Expired` and goes through the dispute resolution path (36.5/36.5/27 split). Expired is the only non-vote terminal path. This should be rare in practice — if it happens regularly, the network is in a degraded state worth investigating.

**Crucial invariant**: the BFT threshold is always computed against the **full active validator set**, not the subset that has voted. This preserves safety under partition per the Grok H4 resolution: neither half of a partition can finalize unless it has full-set supermajority, which is impossible for both halves simultaneously. Progress state can shorten the wait but never fabricates the weight. Verdicts derive from durable votes only.

**Diversity floor**: unchanged from the existing multi-validator consensus final design. Acceptance requires at least 2 distinct analyzer families contributing pass-weight. This interacts with the finalization algorithm as follows: step 2's "mathematically secured" check includes the diversity floor — the outcome is only mathematically secured if both the vote weight and the analyzer family count cannot be overturned by remaining votes.

**Median score threshold**: unchanged. Acceptance requires the median score of pass-voting validators to meet the acceptance threshold. This is computed only over voted-pass validators, not over silent ones.

**Progress-aware abstain handling**: a validator that emits a signed terminal `ProgressPhaseAbstained` state with `ReasonCodeBlobUnavailable` via the RoundProgress control plane is counted as having explicitly abstained for weight-tally purposes. This is distinct from a stale/silent validator: an explicit abstain is a signed terminal state persisted in the snapshot; silence is the absence of any state. Only the signed explicit abstain counts as an abstain in the tally (zero pass-weight, zero fail-weight). Silence does not count as anything — the validator's weight remains uncounted. This distinction is deterministic across all nodes because the abstain is a signed, persisted snapshot state, not an inference from silence.

**Abstain-due-to-unavailability and reputation**: validators that abstain because of blob unavailability (`ReasonCodeBlobUnavailable`) are NOT penalized in the reputation store's agreement rate. Blob unavailability is a network condition, not a quality judgment. The agreement rate tracks verdict consistency (pass/fail alignment with final consensus), not participation rate. A validator that abstains due to unavailability has no verdict to compare against the final consensus, so it is excluded from the agreement rate computation for that round. This prevents a systemic BlobSync failure from cascading into reputation degradation for honest validators.

**Abstain-due-to-unavailability and Generation Ledger**: if a round finalizes with a reduced voter set due to blob-unavailability abstains, the Quality Score for the submission is computed over the voters who actually participated, not the full committee. The Generation Ledger royalties for causal ancestors are therefore based on the quality of the votes that were cast, not penalized by the validators who couldn't participate. This is correct because the depth of verification is reduced (fewer independent verifiers) but the quality of the verifications that occurred is unaffected.

---

## 7. Composition with Existing Protocol Surface

This section audits every existing subsystem that the new design touches and confirms that the composition is clean.

**Recognition fabric** (`internal/recognition/`):
- **What the design adds**: one new consumer (`BlobDemandConsumer`) registered on the existing commit bus. Consumes committed events, looks up BlobRefs via the registry, enqueues fetches for missing blobs.
- **What the design does NOT add**: no progress events on the fabric. Progress is control plane only.
- **Compatibility check**: the consumer follows the always-ready pattern per lesson from commit `0e6d8cc`. Lifecycle is managed by the existing signal wait loop per lesson from commit `1cfb8ed`. Idempotent per the existing consumer contract.
- **No modifications required to the fabric itself.**

**OCS engine** (`internal/ocs/`):
- **What the design adds**: nothing directly. The finalizer's new logic lives in `internal/taskverification/finalizer.go` and reads from both the OCS VotingRound (existing) and the RoundProgressStore (new). OCS doesn't need to know about progress.
- **Compatibility check**: the existing OCS invariants (BFT supermajority over full active validator stake, validator eligibility from seat snapshot at round open, persistence-before-publish) are all preserved.
- **No modifications required to OCS.**

**BlobStore** (`internal/blobstore/`):
- **What the design adds**: new methods (`StreamingPut`, `PutVerified`, `Subscribe`, `WaitForBlob`) and a new type (`BlobRef` with helpers). See §6.2.
- **Compatibility check**: existing callers of `Get`, `Put`, `Has` are unaffected. New methods are purely additive.
- **Modifications**: file additions (`refs.go`, possibly new methods on `store.go`). No removals or breaking changes.

**Body plane / transport** (`internal/network/`):
- **What the design adds**: new message types for BlobTransport (`BlobQuery`, `BlobQueryResponse`, `BlobRequest`, `BlobChunk`, `BlobComplete`) and for RoundProgress (`ProgressUpdate`). Both operate on new logical channels within the existing transport framework.
- **Compatibility check**: the existing body plane message types are unaffected. Connection pooling, framing, and rate limiting are reused. No new transport implementation, just new message types on existing infrastructure.
- **Modifications**: message type registration, handler wiring. No changes to existing messages or their handlers.

**Settlement applicator** (`internal/settlement/`):
- **What the design adds**: nothing. Settlement remains driven by finalized `TaskVerificationConsensus` events. The finalization decision has changed (adaptive, progress-aware) but the event it emits and the settlement that follows are unchanged.
- **Compatibility check**: the v4.1 economic model (73/23/2/2 on accept, 73/23/4 on reject, 36.5/36.5/27 on dispute) is unchanged. Generation Ledger royalties unchanged. Q-weighted distribution unchanged.
- **No modifications required to settlement.**

**Reputation store** (`internal/autovalidator/reputation.go`, existing `ValidatorReputationStore`):
- **What the design adds**: a new dimension — blob serving reputation — tracking serve success rate per peer. Reuses the existing BadgerDB-backed store with a new key prefix.
- **Compatibility check**: existing reputation dimensions (per-validator per-family per-category agreement rates) are unaffected. Q-score computation naturally incorporates the new dimension because chronic non-servers lose overall reputation.
- **Modifications**: new key prefix, new methods on `ValidatorReputationStore` for serving reputation updates. No schema changes to existing entries.

**Slashing engine** (`internal/validator/slashing.go`):
- **What the design adds**: nothing directly. Hard slashing conditions (equivocation, systematic divergence) are unchanged. Soft slashing via reputation decay handles chronic blob-serving dishonesty and chronic progress lease violations.
- **Compatibility check**: slashing remains best-effort from consumer's perspective per the existing lesson.
- **No modifications required.**

**Multi-voter** (`internal/autovalidator/auto.go`, `processSubmittedTaskMultiVoter`):
- **What the design adds**: emits progress updates at each phase transition (Acknowledged → FetchingBlob → Analyzing → VoteEmitted). Uses `BlobStore.Subscribe` to be woken when BlobSync completes a fetch, rather than polling. On fetch exhaustion, transitions to Abstained via progress lease.
- **Compatibility check**: the existing retry-on-empty-content logic (commit `4119b12`) stays. The new composition is that the retry loop emits `FetchingBlob` progress updates while retrying, and BlobSync populates the BlobStore which ends the retry loop cleanly.
- **Modifications**: progress emission calls added, subscribe-based wakeup replaces polling, abstain path added.

**Task verification finalizer** (`internal/taskverification/finalizer.go`):
- **What the design adds**: the adaptive finalization algorithm (§6.4). Reads from RoundProgressStore in addition to the existing VotingRound state. BFT threshold computation against full active validator set.
- **Compatibility check**: the full active validator set rule is already a CLAUDE.md invariant; this design explicitly honors it. Diversity floor and median score thresholds are unchanged.
- **Modifications**: finalization decision logic. The structure of a finalized round (state, transitions, persistence) is unchanged.

---

## 8. Package Layout

```
internal/
  blobstore/
    store.go                  # existing, extended
    fsstore.go                # existing
    refs.go                   # NEW: BlobRef, BlobKind, BlobClass types
    subscribe.go              # NEW: Subscribe and WaitForBlob implementations
    refs_test.go              # NEW

  blobsync/
    engine.go                 # NEW: BlobSyncEngine
    policy.go                 # NEW: BlobFetchPolicy and selection
    demand_consumer.go        # NEW: BlobDemandConsumer
    ref_registry.go           # NEW: BlobRef extractor registry
    holder_cache.go           # NEW: HolderHintCache
    protocol.go               # NEW: wire message types
    transport.go              # NEW: BlobTransport on reused body plane machinery
    handlers.go               # NEW: message handlers
    fetch_session.go          # NEW: per-fetch state and retry tracking
    reputation.go             # NEW: BlobServingReputation dimension
    metrics.go                # NEW: observability
    engine_test.go            # NEW
    demand_consumer_test.go   # NEW
    transport_test.go         # NEW
    integration_test.go       # NEW

  roundprogress/
    types.go                  # NEW: ProgressPhase, ProgressLease, RoundProgressSnapshot, ProgressUpdate
    store.go                  # NEW: in-memory snapshot interface
    badger_store.go           # NEW: BadgerDB-backed implementation
    protocol.go               # NEW: wire message types
    aggregator.go             # NEW: ProgressAggregator
    lease.go                  # NEW: LeaseEnforcer
    eta_clamp.go              # NEW: ETA clamping rules with observed latency windows
    store_test.go             # NEW
    aggregator_test.go        # NEW
    lease_test.go             # NEW

  roundpolicy/
    evaluator.go              # NEW: generic RoundPolicyEvaluator (progress-aware timing)
    types.go                  # NEW: RoundState, TerminalCondition, WaitDecision
    evaluator_test.go         # NEW

  taskverification/
    round.go                  # existing
    finalizer.go              # existing, EXTENDED — calls roundpolicy.Evaluator for timing, owns task-specific verdict rules
    analyzer_policy.go        # existing
    progress_adapter.go       # NEW: bridge between RoundProgress and finalizer
    finalizer_test.go         # EXTENDED

  autovalidator/
    auto.go                   # existing, EXTENDED with progress emission and subscribe-based wakeup
    reputation.go             # existing, EXTENDED with serving dimension
    auto_test.go              # EXTENDED

  network/
    (no new files; BlobSync and RoundProgress register message handlers via existing transport)

  recognition/
    (no new files; BlobDemandConsumer is registered from cmd/node/main.go on existing bus)

cmd/
  node/
    main.go                   # EXTENDED: wire BlobSync, RoundProgress, and progress-aware finalizer

docs/
  blobsync-design.md          # this document (after lock)
  design-principles.md        # principle 15 added
  lessons.md                  # 2 additions captured separately
```

Rationale for the layout:

- **`internal/blobsync/` is its own package** even though it integrates closely with `internal/blobstore/`. The concerns are different: BlobStore is "bytes on disk with content addressing," BlobSync is "how to get bytes across the network." Separating them keeps BlobStore free of networking dependencies, which is important because BlobStore is used by code paths (API handlers, worker integration) that should not pull in network code.

- **`internal/roundprogress/` is its own package** even though its first consumer is `internal/taskverification/`. Principle 6 (generalize the primitive) plus the known imminent second consumer (data ingestion claim verification) justify the separation from day one. The cost of a separate package is small; the cost of extracting later is larger.

- **`internal/roundpolicy/` is its own package** containing the generic progress-aware round timing logic (all terminal? mathematically secured? active leases? stale? backstop reached?). Task-specific verdict rules (diversity floor, median score, dispute semantics) remain in `internal/taskverification/finalizer.go`, which calls `roundpolicy.Evaluator` for timing decisions. This ensures the data ingestion workstream can reuse the timing logic without cloning taskverification. Per ChatGPT second-pass question 3 and principle 6.

- **No modifications to `internal/event/`**: the BlobRef registry lives in `internal/blobsync/ref_registry.go` and extractors are registered there. `event.Event` stays clean. This resolves ChatGPT 8.1.2.

- **No new network package**: BlobSync and RoundProgress register message handlers on the existing transport. They do not need their own top-level network package. This reuses mechanism per principle 7.

- **`cmd/node/main.go` wiring**: the startup wiring for BlobSyncEngine and the RoundProgressStore follows the startup ordering rule from lessons.md (infrastructure running before `node.Start()`). Both components are instantiated, wired into the recognition fabric and transport layer, and started BEFORE any network-facing services accept traffic.

---

## 9. Behavior Under Edge Conditions

Every distributed system design must specify its behavior in edge cases. This section enumerates them with explicit answers.

**Partition — network splits in half**: verification rounds cannot finalize unless one side has the BFT supermajority of the *full active validator set*. In a symmetric 3-2 split on a 5-node testnet, neither side has 4+ votes (the supermajority threshold), so no round finalizes during the partition. When the partition heals, votes and progress snapshots from both sides converge; any round that was open during the partition continues with the combined state. Late votes that arrive after the round has finalized (because the healed side had a slight time advantage) are logged for reputation but do not change the verdict.

**Partition — asymmetric (A can reach B but B cannot reach A)**: BlobSync's retry-with-backoff handles this. A requests blobs from B and succeeds; B cannot request blobs from A and will retry through discovery, eventually finding them via another peer or via the original event's origin hint. If no alternative path exists, B abstains after the fetch deadline.

**Origin crashes after publishing TaskSubmitted but before any other node has fetched the blob**: the event is in the DAG because it was published via `localpub.Publisher.Publish` which persists before publishing. Other nodes see the event and attempt to fetch the blob. All fetches fail because the origin is down. They retry with backoff. If the origin does not come back within `TotalDeadlineMs`, all validators abstain on the consensus-blocking blob, the round goes to dispute resolution, and the escrow is split per the v4.1 dispute rules. The submission is not lost — it's recorded on the DAG — but without evidence, it cannot be verified. This is the correct behavior: losing evidence to an origin crash is a real failure mode and the protocol surfaces it as a dispute rather than silently failing.

**Origin is also a validator (self-loop avoidance)**: BlobSync's discovery explicitly skips the local node. When evaluating the origin hint, if `origin_hint == local_node_id`, BlobSyncEngine does not send a BlobRequest to itself; it consults the local BlobStore directly. This is a one-line check.

**Malicious peer claims to have a blob and serves garbage**: BlobTransport verifies the hash on every received blob. Mismatched hash causes the blob to be dropped, the peer's serving reputation to be decremented, and the fetch to be retried from a different source. Repeated violations blacklist the peer for a cooldown period.

**Malicious peer emits conflicting progress updates**: the ProgressAggregator enforces monotonic state transitions and monotonic generation counters. Conflicting updates (same validator, same round, same family, different generation going backwards) are rejected at the aggregator and logged as anomalies for reputation decay. The snapshot store is not affected by rejected updates.

**Byzantine validator emits progress updates with enormous ETAs**: ETA clamping (§6.3) caps the reported ETA at observable network norms. A claim of "ready in 3600 seconds" is stored as "ready in 30 seconds" (the maximum for FetchingBlob) and the anomaly is logged. Combined with ProgressLease monotonic advancement requirement, the validator cannot extend its lease beyond the clamped ETA without showing actual progress evidence.

**New validator joins existing network with 5.4M historical blobs**: cold-start bootstrap mode. The new validator does NOT fetch all 5.4M blobs. It only fetches blobs for events it is actively voting on (per committee selection). Historical blobs are fetched lazily on reference. This means a new validator is immediately productive on new rounds without being overwhelmed by historical fetch demand. If an old round is re-opened (e.g., via a challenge) and the new validator is asked to participate, the fetch happens then.

**HolderHintCache fills up at ingestion scale**: the cache is capped at 1M entries for testnet (configurable for mainnet). On overflow, LRU eviction applies. Cache miss falls through to discovery. The cache is non-authoritative — missing a hint does not mean the blob is unavailable, just that discovery is needed. At 5.4M-blob ingestion, most lookups will miss the cache, but the ingestion workstream will provide explicit origin hints in submission manifests, so discovery broadcasts are avoided for the bulk of the workload.

**Recognition fabric overloaded by blob demand**: BlobDemandConsumer uses its own bounded queue, separate from the main bus worker pool. When the queue is full, it signals backpressure via the existing `MsgOverloaded` mechanism. The recognition fabric handles this by deferring new committed events to BlobDemandConsumer until the queue drains. This resolves Grok M6.

**Validator's BlobSync fetch exhausts retries on a consensus-blocking blob**: the validator transitions to `ProgressPhaseAbstained` via its progress lease, emits a final progress update with `ReasonCodeBlobUnavailable`, and the round counts it as abstain for tally purposes. The round can proceed to finalize without waiting for this validator further, provided other validators have reached BFT supermajority of the full active set. If too many validators abstain on the same blob, the round fails to reach supermajority and expires as a dispute.

**Abstain cascade — most validators abstain due to shared blob unavailability**: this is a failure mode that must be surfaced. If a majority of validators cannot fetch the blob, the round will fail to reach supermajority and will hit the absolute backstop as an expired dispute. The worker loses escrow to the dispute split. This is correct behavior: the protocol cannot verify submissions it cannot see. However, this is also diagnostic — if this happens frequently, it indicates a systemic BlobSync failure (network issue, origin persistently down, DoS attack on blob serving) that operators should investigate. Metrics on abstain cascades should be surfaced prominently.

---

## 10. The Seven Implementation Prompts

Implementation is staged into seven prompts following the pattern used for the multi-validator consensus suite. Each prompt has a goal, files to inspect, files to modify, tests to run, acceptance criteria, explicit "do not" constraints, and post-run invariants. Prompts are implemented sequentially; each prompt's completion includes live testnet verification where applicable.

### Prompt 1 — BlobRef Extraction and Classes

**Goal**: create the typed blob reference model and extractor registry. Establish blob references as a first-class primitive.

**Inspect first**:
- `internal/event/` (all event types, payload versions)
- `internal/blobstore/` (existing store interface)
- `internal/recognition/` (consumer patterns)
- `internal/tasks/` (TaskSubmitted payload)
- `internal/trajectory/` (trajectory artifacts if present)

**Modify**:
- `internal/blobstore/refs.go` (NEW): BlobRef, BlobKind, BlobClass types
- `internal/blobsync/ref_registry.go` (NEW): extractor registry
- Register the TaskSubmitted extractor as the first (and only in this prompt) extractor

**Tests**:
- Extractor returns correct BlobRef for a TaskSubmitted payload with evidence
- Extractor returns empty list for events with no blobs
- BlobKind → ConsensusBlocking derivation is correct
- BlobRef round-trip serialization preserves all fields
- Registry rejects duplicate registrations for the same (event_type, payload_version)

**Test commands**:
```
go test -race ./internal/blobstore/... ./internal/blobsync/... ./internal/event/... -count=1
```

**Acceptance criteria**:
- BlobRef is a first-class primitive
- event.Event is unmodified
- TaskSubmitted events have their blobs extractable via the registry
- No networking, no consumers, no round logic in this prompt

**DO NOT**:
- Do not modify event.Event to add BlobReferences() or any blob-specific method
- Do not build BlobSyncEngine yet
- Do not add a consumer yet

**Post-run invariants**:
- Primitive established
- Extensible to future event types via registration

### Prompt 2 — BlobSync Transport Channel and Engine

**Goal**: build blob fetch machinery as a separate transport concern, reusing body-plane mechanics on a distinct channel.

**Inspect first**:
- `internal/network/` (body plane, connection pool, message framing)
- `internal/blobstore/` (Subscribe/WaitForBlob additions from Prompt 1)
- Sync architecture and startup ordering lessons from `docs/lessons.md`
- `internal/autovalidator/reputation.go` (existing reputation store)

**Modify**:
- `internal/blobsync/engine.go` (NEW): BlobSyncEngine
- `internal/blobsync/policy.go` (NEW): BlobFetchPolicy and selection
- `internal/blobsync/protocol.go` (NEW): wire message types
- `internal/blobsync/transport.go` (NEW): BlobTransport on body plane machinery
- `internal/blobsync/handlers.go` (NEW): message handlers
- `internal/blobsync/holder_cache.go` (NEW): HolderHintCache
- `internal/blobsync/fetch_session.go` (NEW): per-fetch state
- `internal/blobsync/reputation.go` (NEW): serving reputation dimension
- `internal/blobstore/subscribe.go` (NEW): Subscribe and WaitForBlob methods
- Extensions to `ValidatorReputationStore` for serving dimension
- Wiring in `cmd/node/main.go` (engine lifecycle managed by signal wait loop)

**Tests**:
- Origin-first request succeeds with valid origin hint
- Broadcast discovery succeeds when origin is unreachable
- Bounded fanout discovery does not exceed DiscoveryFanout peers
- Hash verification rejects garbage responses, blacklists offending peer
- Duplicate concurrent fetches are coalesced
- HolderHintCache hits skip discovery
- ETA clamping is applied to incoming hints
- Cold-start: new node with empty cache can fetch blobs via discovery
- No `n.mu` held during network sends (principle from CLAUDE.md)
- Engine lifecycle: started before `node.Start()`, stopped during shutdown

**Test commands**:
```
go test -race ./internal/blobsync/... ./internal/network/... ./internal/autovalidator/... -count=1
```

**Acceptance criteria**:
- A node lacking a blob can fetch it from a peer that has it
- Verified bytes land in BlobStore
- HolderHintCache is non-authoritative (missing entry falls through to discovery)
- Serving reputation is tracked per peer

**DO NOT**:
- Do not wire BlobDemandConsumer yet (Prompt 3)
- Do not touch round finalization yet (Prompt 6)
- Do not make broadcast discovery the primary path — origin-first

**Post-run invariants**:
- Separate channel, shared transport machinery
- Content addressing enforced
- Cold-start tractable

**Grok hardening slotted here**:
- C2 (index poisoning): promise-with-timeout, blacklist on failure, origin-first default
- M5 (serving incentive): reputation tracking now
- H3 (cold start): bootstrap mode — fetch on demand only, no eager backfill

### Prompt 3 — BlobDemandConsumer

**Goal**: wire BlobSync to the recognition fabric so that committed events automatically trigger fetches for missing consensus-blocking blobs.

**Inspect first**:
- `internal/recognition/` (CommitConsumer interface, bus contract)
- `internal/blobsync/` (engine and registry from Prompts 1 and 2)
- Recognition fabric lessons from `docs/lessons.md` (always-ready, no deferred activation retroactive, lifecycle management)

**Modify**:
- `internal/blobsync/demand_consumer.go` (NEW): BlobDemandConsumer
- Wiring in `cmd/node/main.go` to register the consumer on the bus
- Separate queue for BlobSync demand with backpressure via MsgOverloaded

**Tests**:
- Committed TaskSubmitted with missing blob triggers a fetch
- Committed TaskSubmitted with already-local blob does not trigger a fetch
- Replayed events during startup trigger lazy fetches for missing blobs
- Consumer is idempotent (same event committed twice does not duplicate fetches, deduplicated by in-flight table in engine)
- Backpressure: when demand queue is full, consumer signals MsgOverloaded
- Consumer follows always-ready pattern (no deferred activation)
- Consumer registered before `node.Start()`

**Test commands**:
```
go test -race ./internal/recognition/... ./internal/blobsync/... ./internal/tasks/... -count=1
```

**Acceptance criteria**:
- Missing blobs are detected via the fabric, not polling
- Replay path covered
- Backpressure works under load

**DO NOT**:
- Do not change round finalization yet
- Do not emit progress updates from the consumer (that's the multi-voter's job in Prompt 5)

**Post-run invariants**:
- Blob availability is source-agnostic (local, remote, replay all flow through the same path)
- Recognition fabric is not overloaded by blob demand

**Grok hardening slotted here**:
- M6 (recognition fabric overload): separate queue + backpressure via MsgOverloaded

### Prompt 4 — RoundProgress Control Plane and Snapshot Store

**Goal**: add the signed validator progress control plane with a persisted latest-state snapshot store. No DAG event type. Enforce ProgressLease invariants.

**Inspect first**:
- `internal/taskverification/` (existing round model and finalizer)
- `internal/network/` (transport reuse patterns)
- `internal/consensus/` (existing vote aggregation)

**Modify**:
- `internal/roundprogress/types.go` (NEW): ProgressPhase, ProgressLease, RoundProgressSnapshot, ProgressUpdate
- `internal/roundprogress/store.go` (NEW): in-memory store interface
- `internal/roundprogress/badger_store.go` (NEW): BadgerDB-backed implementation
- `internal/roundprogress/protocol.go` (NEW): wire message types and signing
- `internal/roundprogress/aggregator.go` (NEW): ProgressAggregator with lease enforcement
- `internal/roundprogress/lease.go` (NEW): LeaseEnforcer background goroutine
- `internal/roundprogress/eta_clamp.go` (NEW): ETA clamping with observed latency windows
- Transport message handler registration in `cmd/node/main.go`
- Rate limiting per (validator, round, family)

**Tests**:
- Snapshot store persists latest state per (round, validator, family)
- Monotonic advancement enforced: regression in phase or generation is rejected
- Lease expiry works: stale leases are marked stale after their expiry
- On restart, latest snapshot is restored from BadgerDB
- ETA clamping: out-of-range ETAs are clamped to observable norms
- Rate limiting: >1 update per 10s per (validator, round, family) is dropped
- Conflicting updates (different generations, same phase) are rejected
- Signatures are verified on incoming updates, invalid signatures rejected

**Test commands**:
```
go test -race ./internal/roundprogress/... -count=1
```

**Acceptance criteria**:
- RoundProgress exists as control plane + snapshot, never touches the DAG
- ProgressLease defeats byzantine state update attacks structurally
- Rate limiting and ETA clamping are hardening on top
- Snapshot store is the authoritative state

**DO NOT**:
- Do not add any DAG event type for progress
- Do not wire the finalizer to read progress yet (Prompt 6)
- Do not emit progress updates from the multi-voter yet (Prompt 5)

**Post-run invariants**:
- Machine-readable progress exists
- No ledger pollution
- Byzantine state updates have no effect on finalization timing

**Grok hardening slotted here**:
- C1 (byzantine state-update DoS): ProgressLease with monotonic advancement, ETA clamping, rate limiting, signatures
- L7 (state update volume): rate limiting
- L8 (spam cycle detection): monotonic state machine with generation counter

### Prompt 5 — Wire MultiVoter to RoundProgress and BlobSync

**Goal**: emit progress updates during fetch/analyze/vote phases. Consume BlobSync status via Subscribe. Handle abstain on blob exhaustion.

**Inspect first**:
- `internal/autovalidator/auto.go` (existing multi-voter, retry loop from commit 4119b12)
- `internal/taskverification/` (round context for progress emission)
- `internal/blobsync/` (engine for fetch status)
- `internal/roundprogress/` (protocol for progress emission)

**Modify**:
- `internal/autovalidator/auto.go`: extend `processSubmittedTaskMultiVoter` to emit progress updates at phase transitions; replace polling retry with Subscribe-based wakeup
- Progress emission calls on: Acknowledged (entry), FetchingBlob (on blob miss), Analyzing (on content available), VoteEmitted (after vote published), Abstained (on fetch exhaustion)

**Tests**:
- Phase transitions emit correct progress updates in correct order
- Subscribe-based wakeup fires when BlobSync completes a fetch
- Blob fetch exhaustion leads to abstain via progress lease
- Retry loop still enforces content gate (no scoring on empty content)
- No infinite "fetching" without lease renewal
- Progress updates include correct generation increments with evidence

**Test commands**:
```
go test -race ./internal/autovalidator/... ./internal/taskverification/... ./internal/roundprogress/... ./internal/blobsync/... -count=1
```

**Acceptance criteria**:
- Validator work is machine-readable during rounds
- Subscribe-based wakeup replaces polling
- Abstain on exhaustion works

**DO NOT**:
- Do not make the finalizer progress-aware yet (Prompt 6)
- Do not remove existing safety backstops yet

**Post-run invariants**:
- State, not silence
- Content gating preserved

### Prompt 6 — Progress-Aware Round Finalizer

**Goal**: replace timeout-primary finalization with progress-aware adaptive finalization.

**Inspect first**:
- `internal/taskverification/finalizer.go` (existing finalizer)
- `internal/roundprogress/` (snapshot store)
- `internal/consensus/` (vote aggregation, supermajority check)
- CLAUDE.md invariants (BFT threshold over full active validator set)

**Modify**:
- `internal/taskverification/finalizer.go`: extend with adaptive finalization algorithm (§6.4)
- `internal/taskverification/progress_adapter.go` (NEW): bridge between RoundProgress snapshot store and finalizer
- Absolute backstop of 5 minutes as the outer safety cap

**Tests**:
- All voted → immediate finalize
- One slow validator with active lease → bounded wait
- Stale/no progress → finalize if BFT threshold over full active set is secured
- Outcome mathematically secured → finalize early
- Partition scenario: neither half finalizes unless it has full-set supermajority
- Absolute backstop: round expires as dispute if no finalization possible
- Diversity floor and median score checks integrated with adaptive finalization
- Progress-driven abstain counts as abstain vote for tally

**Test commands**:
```
go test -race ./internal/taskverification/... ./internal/roundprogress/... ./internal/consensus/... -count=1
```

**Acceptance criteria**:
- Backstop timeout is no longer the primary mechanism
- Liveness improves under honest delay
- Byzantine silence cannot stall rounds beyond the backstop
- Safety under partition preserved

**DO NOT**:
- Do not weaken BFT threshold
- Do not let fake ETAs extend rounds (Prompt 4 already ensures this)
- Do not change the existing diversity floor or median score rules

**Post-run invariants**:
- Adaptive timing at machine speed
- Hard backstop exists but is rare in practice
- BFT threshold over full active validator set is honored

**Grok hardening slotted here**:
- H4 (partition): BFT supermajority over full active set
- C1 (reinforcement): finalizer does not trust self-reported ETAs

### Prompt 7 — End-to-End Verification on Live Testnet

**Goal**: prove the reference accept-path test on the live testnet. This is the gate that has never been passed.

**Inspect first**:
- All packages modified in Prompts 1-6
- CLAUDE.md deploy verification protocol
- The reference test specification

**Modify**:
- Any final hardening revealed during end-to-end testing
- Observability additions (metrics, structured logging for the new subsystems)
- Live testnet deployment via standard protocol

**Tests**:
- Full `go test -race ./... -count=1` passes across all packages
- Live testnet verification:
  - Register fresh poster agent and worker agent
  - Verify balances > 0 after 30s
  - Post the reference task (BFT vs Nakamoto explainer, 1500-2500 words, 4 sections, 3 citations, research category)
  - Worker claims task, executes via Claude API, produces real content
  - Verify TaskSubmitted propagates to all 5 nodes
  - Verify BlobSync fetches evidence to all validators
  - Verify each validator emits progress updates during fetch/analyze phases
  - Verify votes emitted across configured analyzer families
  - Verify round finalizes with BFT pass supermajority + diversity floor + median score
  - Verify TaskVerificationConsensus with FinalVerdict = pass
  - Verify v4.1 settlement: worker receives 73,000 µAET, validators 23,000 Q-weighted, gen ledger 2,000 (or treasury fallback), treasury 2,000, total = escrow
  - Verify worker balance delta equals 73% of escrow to the µAET
  - Verify reputation store updated
  - Verify no slashing (calibration period, diversity floor met)
  - Pull logs from all 5 nodes and surface any errors, retries, or warnings

**Test commands**:
```
go test -race ./... -count=1
# Then full live testnet verification per CLAUDE.md deploy protocol
```

**Acceptance criteria**:
- Reference accept path passes on live testnet
- Blob replication verified working
- Progress-aware finalization verified working
- No regression to existing invariants
- Lessons from the run captured in `docs/lessons.md` if applicable

**DO NOT**:
- Do not ship on "tests pass" alone
- Do not bypass live verification
- Do not claim success without worker balance delta verification

**Post-run invariants**:
- All validators can score the same content
- Rounds finalize adaptively
- Settlement is exact and idempotent
- The accept path is empirically demonstrated for the first time

---

## 11. Future Work and Known Gaps

Items explicitly out of scope for this design, flagged for future workstreams.

**Blob serving micro-payments**: the v1 design tracks serving reputation but does not pay validators for serving blobs. Direct payment is a future economic workstream that needs its own design pass. The reputation-only mechanism is sufficient for testnet and creates correct incentives via Q-weighted fee distribution, but mainnet may want direct payment to further incentivize serving.

**Stake-bonded blob serving**: for mainnet permissionless participation, blob serving may need a stake bond to prevent spam. Deferred to mainnet hardening.

**CVD-prioritized cold-start bootstrap**: new validators in v1 fetch only blobs for events they actively vote on. A smarter bootstrap that pre-fetches high-CVD ancestors would improve liveness for new joiners but requires the full Q-score infrastructure (CVD tracking) which is itself future work.

**Blob eviction and tiering**: v1 stores all fetched blobs indefinitely. At mainnet scale, hot/cold tiering and eviction policies become necessary. Flagged as a data ingestion workstream concern because the data ingestion workload will be the first to exercise real eviction needs.

**Cycle detection in Generation Ledger**: unchanged from the multi-validator consensus design; still flagged as future work in commit `7964e84`.

**Full Q-score formula activation**: only Consistency (α₄) is wired. The other three terms (CVD_norm, ChallengeSurvival, ReplicationRate) need infrastructure. This is unchanged by the BlobSync design but affects how reputation is computed across all validator work.

**Challenge path for hard slashing**: the 60-second challenge window auto-applies hard slashing. A real challenge path where a slashed validator can submit counter-evidence is future work.

**Data ingestion layer**: the next workstream after BlobSync locks. Will introduce: claim-level verification granularity, manifest-declared verification policies, developer-registered analyzer families, independence-weighted verification (contributor-validator affiliation tracking), stratified sampling, batch admission. Most of this will build on top of the primitives locked here (RoundProgress, BlobRef, BlobClass).

**Methodology advisory system**: informational tier for analyzer families that flag methodology concerns without rejecting claims. Future work.

**Validator onramp**: the streamlined setup for new validators to join the network. Includes installation, key generation, peer discovery, stake acquisition, analyzer family selection, and monitoring. Depends on BlobSync being live so that new validators can actually verify submissions.

---

## 12. Second-Pass Review Questions

After this locked design is written but before implementation begins, both reviewers perform a second-pass review to confirm the fixes close the first-pass findings without introducing regressions.

### For Grok (adversarial red-team, second pass)

1. Does the ProgressLease primitive with monotonic advancement and ETA clamping actually close the fake-ETA DoS attack (C1)? Can you construct a new attack against the hardened design?

2. Does the origin-first + bounded-fanout discovery with signed HolderHints close the index poisoning + broadcast storm (C2)? What new attack surfaces did HolderHint signing introduce?

3. Does the full-set BFT supermajority rule close the partition-healing race (H4) without introducing a liveness problem that attackers can exploit (e.g., partitioning to stall rounds deliberately)?

4. Does the reputation-only serving incentive (without micro-payment) actually create correct incentives, or can a rational validator still refuse to serve without economic penalty?

5. Cold-start bootstrap: does fetching only blobs for active votes actually work for a new validator joining a live network, or does it create a silent liveness failure where the new validator cannot contribute meaningfully until enough rounds have happened?

6. At 5.4-million-blob ingestion scale with explicit origin hints in the manifest, what is the actual failure mode? If the manifest is missing or stale, does the broadcast fallback melt?

7. The BlobFetchPolicy has a 30-second total deadline for consensus-blocking blobs. What happens when a large blob (say, a 500MB dataset from the ingestion workstream) cannot be fetched in 30 seconds? Is the policy correctly tunable per BlobKind?

8. The reference test uses a 1500-2500 word task. What happens on the first 10MB-scale or 100MB-scale submission? Is the design correct at scales the testnet hasn't exercised?

9. What do you find that we have not asked about?

### For ChatGPT (architectural rigor, second pass)

1. Is the three-subsystem factoring (BlobSync + RoundProgress + RoundPolicy) correctly applied throughout the locked design, or are there places where concerns have leaked across boundaries?

2. The BlobRef extractor registry keeps event.Event clean. Are there any places where the locked design accidentally pollutes event.Event or other canonical types with blob-specific behavior?

3. The RoundPolicy logic lives in `internal/taskverification/finalizer.go` as an extension. Is this the right location, or should RoundPolicy be its own package?

4. The package layout has `internal/roundprogress/` as its own package from day one. Is this correct given that the first consumer is taskverification, or should it start inside taskverification and extract later?

5. The ProgressLease primitive enforces monotonic advancement via generation counters with evidence hashes. Is the evidence hash design robust — can a byzantine validator forge evidence that looks like progress without actually progressing?

6. The HolderHint primitive is signed but non-authoritative. Is the non-authoritative property correctly enforced at every use site, or are there places where a cached hint is trusted more than it should be?

7. The adaptive finalization algorithm has five steps with explicit fallthrough rules. Is the algorithm correctly handling every combination of vote state and progress state, or are there edge cases where it would make the wrong decision?

8. Does the design generalize cleanly to the data ingestion workstream? Specifically: when data ingestion introduces claim-level verification rounds with their own analyzer families and their own consensus requirements, do RoundProgress and BlobSync need any changes, or do they compose cleanly?

---

## 13. Acceptance Criteria for Final Lock

This design transitions from LOCKED (pending second-pass review) to LOCKED FINAL (implementation may begin) only when all of the following are satisfied:

1. Both ChatGPT and Grok have completed second-pass reviews and confirmed that the fixes close the first-pass findings without introducing new critical issues.

2. Any new findings from second-pass reviews have been resolved in this document with explicit reconciliation entries.

3. The design has been audited one final time against all 15 design principles with no unresolved violations.

4. The design is consistent with CLAUDE.md, lessons.md, the multi-validator consensus final design, and the multi-validator scoring audit.

5. The reference test (BFT-vs-Nakamoto accept path) is explicitly specified with clear verification steps.

6. Mike has reviewed and approved the locked design.

7. This document is committed to the repo as `docs/blobsync-design.md` alongside the principle 15 addition to `docs/design-principles.md` and the two lesson additions to `docs/lessons.md`.

Only after all seven conditions are met do implementation prompts get written and handed to Claude Code.

---

## 14. Implementation Order Reminder

Once locked, the seven prompts are implemented sequentially in the order given in §10. Each prompt completes with:

- All tests passing (`go test -race ./... -count=1`)
- Commit with descriptive message
- Push to origin/main
- For Prompts 2-7, where applicable: deploy to all 5 testnet nodes via standard protocol
- For Prompt 7 specifically: live testnet verification including the reference accept-path test with worker balance delta verification

Between prompts, no work on subsequent prompts begins until the current prompt has passed its acceptance criteria. The plan mode discipline from CLAUDE.md applies: any deviation from the locked design requires pausing, updating this document, getting re-approval, and then continuing.

---

*End of locked design document. Ready for second-pass review.*
