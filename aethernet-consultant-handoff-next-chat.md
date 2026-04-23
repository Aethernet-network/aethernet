# AetherNet Consultant Handoff — Next Chat

**Date:** 2026-04-14  
**Purpose:** Carry forward the full architectural context, current decisions, active workstreams, and locked constraints for continuing AetherNet work in the next chat.

---

## 1. What AetherNet Is

AetherNet is a new category of protocol infrastructure: the **trust and settlement layer for the AI agent economy**.

It is not a blockchain clone and not a conventional marketplace backend. It is a **causal DAG protocol** with:

- BFT consensus
- cryptographic settlement
- event-sourced state
- compound verification across structurally independent analyzer families
- trajectory capture / negative knowledge preservation
- validator lifecycle with seat-based snapshots
- local-first protocol-native event publication
- deterministic replay and topological convergence

The protocol must scale to **billions of users** and **millions of events per hour**.

---

## 2. My Role in This Project

In prior chats I have been acting as an **architectural consultant** on the full AetherNet build. The working posture has been:

- challenge draft designs hard
- preserve the project’s design principles and invariants
- push toward production-grade, replay-safe, protocol-native architecture
- avoid bandaids and callsite patches when a missing primitive is the real issue
- turn successful architecture reviews into staged implementation prompt suites for Claude Code

This handoff assumes that posture should continue.

---

## 3. Binding Standards and Non-Negotiables

These have been treated as binding across the project:

### Core protocol invariants
- `event.Event` remains the canonical ledger object
- `dag.AddEvent()` / `dag.Add()` remains strict append-only DAG materialization
- deterministic replay and topological convergence must hold
- settlement must be safe and exactly-once
- all validator lifecycle transitions must be auditable via DAG events
- votes validate against **validator-seat snapshots**, not just identity registry
- local event creation must use one authoritative local publication path
- remote/synced/replayed events must not be republished
- finalization-owning path must trigger settlement

### Architectural standards
- build the finished system, not a prototype
- preserve layer boundaries
- every trust decision must be cryptographically grounded
- every deterministic state transition must be reproducible from DAG history
- one authoritative path beats many ad hoc paths
- generalize the primitive, not the bug fix
- beauty is a correctness signal

### Important design-principle decisions surfaced in review
- static timeout is not an acceptable primary coordination mechanism
- validators must be able to communicate **durative state** during long-running rounds
- self-reported state is not enough: **observable evidence beats self-report**
- do not pollute the canonical DAG with ephemeral control-plane chatter

---

## 4. Major Subsystems Already Designed / Shipped

### 4.1 Protocol / DAG foundation
Already established:
- DAG topological sort replay
- event-sourced task state
- causal genesis infrastructure

### 4.2 Auth / TX-V1 / identity
Already hardened:
- AETHERNET-TX-V1 signing
- signer=actor enforcement
- bech32 identities (`aet`, `taet`)
- replay protection
- rate limiting
- payload ceilings
- no `--no-auth`
- shared API key read-only

### 4.3 Fast Path v1 networking
Already designed and shipped as the production networking layer:
- three-plane architecture:
  - causality plane
  - body plane
  - repair/proof plane
- five-stage ingest pipeline
- scored mesh dissemination
- bounded relay
- backpressure
- peer scoring
- repair and checkpoint bootstrap
- V1/V2 negotiation

A separate prompt suite was created for the sync architecture fix and delivered as `.md` files.

### 4.4 Trajectory layer
Already designed and shipped:
- `trajectory_commit` event
- BlobStore integration
- `PrimaryTips()`
- trajectory service + API
- external content-addressed checkpoint blobs
- evidence anchoring via `ExplorationRoot` / `ExplorationSample`

### 4.5 Validator lifecycle
Architecturally settled:
- seat-based
- snapshot-bound
- event-sourced
- deterministic committee selection
- key-epoch aware
- join/leave/suspend/resume/slash/rotate modeled as future-snapshot-aware transitions

A prompt suite for validator-only key compromise handling was also produced earlier.

---

## 5. Multi-Validator Task Verification Consensus

This is one of the most important recent design areas.

### What was found
The original system was **not truly compound verification**:
- effectively single-validator scoring
- first validator to score could decide the outcome
- BFT on task settlement was theatrical because validators were rubber-stamping settlement propagation, not independently agreeing on work quality
- rejection path was local-only and not consensus-bound

### What was designed
A full **multi-validator task verification consensus** architecture was designed and then implemented on testnet.

Key ideas:
- `TaskVerificationRound`
- multi-validator voting
- analyzer-family diversity floor
- BFT verdict by pass/fail vote weight
- scores retained for diagnostics / reputation / calibration, not authoritative cross-family consensus arithmetic
- local unilateral settlement/rejection removed from the authority path

### Important later correction
After the first real accept-path test, we determined that the **cross-family median score threshold was architecturally wrong**.

The accepted architectural position is:

- **Remove the cross-family median score threshold from pass-finalization**
- Keep:
  - BFT pass-weight threshold
  - analyzer-family diversity floor
- Scores remain:
  - non-consensus metadata
  - useful for diagnostics
  - calibration
  - reputation
  - dispute analysis
  - analytics

This is important and should be preserved in future chats.

---

## 6. Recognition Fabric

A major architectural review concluded that the system needed a **universal post-commit recognition fabric** rather than path-specific ad hoc notifications.

### Why
Previously:
- local events: caller manually notified subsystems
- remote events: `syncHandler` routed to subsystems
- replay: bespoke subsystem restoration paths

This asymmetry caused real correctness risks.

### What was designed
A **Recognition Fabric** based on:
- Event Commit Bus
- Recognition Index
- `CommitConsumer` interface
- targeted deferred activation by prerequisite

### Key insight
The correct abstraction is not just “callback on DAG add.” It is:

1. **commit recognition**
2. **consumer readiness recognition**

So a committed event can be:
- recognized now
- deferred with explicit reason
- activated later when prerequisites become satisfied

A prompt suite for the recognition fabric was produced and saved as `.md` files.

---

## 7. Sync Architecture Fix

A major networking bug was fixed architecturally.

### Problem
- all-V2 clusters were still running legacy sync polling
- full-DAG resync storm
- evidence blobs piggybacked on legacy sync
- nodes were overwhelmed

### Architecture decision
- V2 peers use Fast Path only
- legacy sync is V1-only and frontier/delta-based
- evidence blobs move onto the body plane with fallback fetch
- autovalidator gates on `EvidenceReady`

A full prompt suite for this was created and saved as `.md` files.

---

## 8. BlobSync + RoundProgress + RoundPolicy

This is the most recent major architecture review area.

### Trigger
The first full multi-validator accept-path run on live testnet showed:
- blob replication was missing across validators
- only the origin node had the evidence blob
- only that validator could score
- rounds expired as disputes even though the rest of the pipeline was correct

### First-pass review outcome
The initial draft proposed `BlobSync + ValidatorRoundState`, but I rejected one key part:
- **durative validator state must not be modeled as canonical DAG events**

That would have polluted append-only ledger history with ephemeral liveness chatter.

### Locked architectural factoring
The corrected factoring is:

1. **BlobSync**
   - data availability and cross-node blob transfer

2. **RoundProgress**
   - signed control-plane state for validator durative progress
   - persisted as latest-state snapshot
   - not a DAG event type

3. **RoundPolicy**
   - adaptive finalization / waiting policy

### Important missing primitives that were identified
- `BlobRef` / `BlobDescriptor`
- `BlobClass`
- `ProgressLease`
- `HolderHint`
- `RoundProgressSnapshot`
- `BlobFetchPolicy`

### Important second-pass correction I required
Before final lock, I required these changes:

1. **Progress state must be liveness input only**
   - it cannot become a second source of truth
   - accept/reject still derive only from durable votes and BFT rules

2. **`ProgressEvidence` must be phase-specific and observable**
   - a raw hash is not enough to prove real progress
   - lease renewal must be grounded in phase-specific observable evidence

3. **RoundPolicy should be generalized**
   - not buried only inside taskverification finalizer logic
   - it must be reusable for ingestion-side verification rounds later

### Current status
A “locked pending second-pass review” design document was drafted and I answered the 8 second-pass architectural questions. My conclusion was:

- the major fixes are correct
- **not fully final yet** until the above three items are explicitly corrected in the locked design text

This is important context for the next chat.

---

## 9. Data Ingestion Workstream

A large amount of design work has already been done for the future **agnostic data ingestion** layer.

### Context
Developer partners have large causal graphs and datasets (including a 5.4M-node OSINT corruption graph) that they want to ingest into AetherNet and verify through validators.

### Core ingestion philosophy
Depth comes from **independent verification**, not self-certification.

### Major ingestion design concepts already established
- claims as the trust-bearing unit
- manifest schema + validation + registry
- independence-weighted verification
- claim/record semantics
- versioned manifests
- data enters at depth zero
- progressive trust and sampling planned downstream

### Important review correction already incorporated
The ingestion design originally used float-based canonical scoring state. That was rejected and corrected:
- integer fixed-point only
- `int64` unix timestamps
- no `time.Time` or `float64` in canonical protocol state
- map canonicalization rule added

A prompt suite for ingestion items 1–3 was already produced and saved as `.md` files.

---

## 10. Prompt Suites Already Produced

The following prompt suites were created and saved as downloadable `.md` bundles in this environment:

### A. Ingestion items 1–3 prompt suite
Folder / zip:
- `aethernet-ingestion-items-1-3-prompt-suite`

Files:
- `00-master-preamble.md`
- `01-claim-unit-semantics.md`
- `02-manifest-schema-validation-registry.md`
- `03-independence-weighted-verification.md`
- `04-integration-boundary-audit-hardening.md`

### B. Sync architecture fix prompt suite
Folder / zip:
- `aethernet-sync-architecture-fix-prompt-suite`

Files:
- `00-master-preamble.md`
- `01-stop-sync-storm-v2-gating.md`
- `02-frontier-based-delta-sync.md`
- `03-remove-blob-piggyback-from-legacy-sync.md`
- `04-body-plane-blob-sidecar.md`
- `05-blob-fallback-fetch-path.md`
- `06-evidence-ready-gating.md`
- `07-final-integration-hardening.md`

### C. Recognition fabric prompt suite
Folder / zip:
- `aethernet-recognition-fabric-prompt-suite`

Files:
- `00-master-preamble.md`
- `01-recognition-scaffolding.md`
- `02-emit-commit-records-from-all-paths.md`
- `03-ocs-submission-consumer.md`
- `04-ocs-vote-consumer-with-deferred-readiness.md`
- `05-targeted-deferred-activation.md`
- `06-task-lifecycle-and-evidence-consumers.md`
- `07-settlement-consumer.md`
- `08-migrate-off-synchandler-and-manual-notifications.md`
- `09-final-hardening-and-full-e2e.md`

### D. Sync architecture fix review note
- `aethernet-sync-architecture-fix-and-validate.md`

These should be treated as prior deliverables and available context if the next chat needs to reference implementation staging.

---

## 11. Active Architectural Positions That Should Carry Forward

These are the most important conclusions from prior consulting work.

### A. Compound verification is real only if structural independence is real
- Same analyzer with different seeds is not sufficient as the long-term thesis
- Minimum viable real independence means analyzer-family diversity
- Diversity floor is load-bearing

### B. Do not use cross-family score arithmetic as a pass gate
- Cross-family median threshold was removed conceptually
- Verdict comes from BFT + diversity floor
- Scores are diagnostics/calibration/reputation inputs

### C. The DAG should not absorb ephemeral liveness chatter
- Progress belongs on a control plane or snapshot layer
- Durable facts belong on the DAG

### D. The protocol needs generalized primitives, not repeated bug fixes
- Recognition fabric
- RoundProgress
- BlobSync
- analyzer policy
- committee/round models
must be designed so they can be reused by future workstreams

### E. Replay-safe architecture matters
- Anything that matters after restart must have a proper durable representation
- Anything ephemeral should not be mistaken for ledger state

### F. Backstops are allowed, but not as the primary mechanism
- static timeout can exist as an outer safety cap
- it must not be the primary coordination primitive

---

## 12. What Still Needs Attention

These are unresolved or near-term items likely to come up in the next chat.

1. **Finalize BlobSync + RoundProgress + RoundPolicy locked design**
   - ensure progress state is liveness-only, not consensus-authoritative
   - strengthen phase-specific `ProgressEvidence`
   - extract/generalize RoundPolicy

2. **Write implementation prompts for BlobSync workstream**
   - after design lock

3. **Continue task accept-path proof on live testnet**
   - target remains:
     - real task
     - real content
     - all validators score
     - BFT accept
     - exact settlement math

4. **Advance into agnostic data ingestion**
   - downstream of BlobSync
   - blob transfer and round-progress primitives will be load-bearing there

5. **Potential future architectural pressure points**
   - committee-based scaling for verification rounds
   - analyzer registry / analyzer family onboarding
   - serving incentives / micro-payments for blob availability
   - Q-score full activation
   - validator onramp / cold start

---

## 13. Recommended Starting Point for the Next Chat

If the next chat is continuing protocol architecture work, the highest-value starting points are:

### If working on BlobSync
Start with:
- the locked design doc
- my second-pass answers
- the three unresolved architectural edits listed above

### If working on implementation staging
Start with:
- the relevant prompt suite
- current repo/testnet status
- whether design is fully locked or still pending edits

### If shifting to ingestion
Start with:
- the ingestion prompt suite
- the corrected design principles:
  - integer-only canonical state
  - map canonicalization
  - claims as trust-bearing units
  - independence weighting
- and immediately check how BlobSync / RoundProgress will support ingestion blobs and claim-verification rounds

---

## 14. Consultant Position Summary

The strongest architectural contributions I have made in this project so far are:

- rejecting category errors where the protocol tries to average incomparable things
- insisting on generalized primitives instead of path-specific hacks
- separating durable ledger facts from ephemeral control-plane state
- pushing toward source-agnostic, replay-safe recognition
- preserving structural independence as the core of the compound verification thesis

If the next chat needs to continue with me in the same role, that posture should continue.

---

## 15. One-Screen Summary

If only a short summary is needed for the next chat:

- AetherNet is a causal-DAG BFT trust/settlement protocol for AI work.
- Fast Path, trajectory, auth, and validator lifecycle architecture are already mature.
- Multi-validator task verification consensus is live in principle, but operational gaps surfaced in live testing.
- Recognition Fabric was designed to unify local/remote/replay event recognition.
- Sync architecture was fixed with V2-only Fast Path, delta legacy sync, blob sidecar + fallback, and `EvidenceReady`.
- The next critical infrastructure layer is **BlobSync + RoundProgress + RoundPolicy**.
- Correct factoring is:
  - BlobSync = data availability
  - RoundProgress = signed durative control-plane state
  - RoundPolicy = adaptive wait/finalization
- Progress must **not** be a DAG event type.
- Progress must be **liveness input only**, not a second consensus truth source.
- Cross-family median score threshold for pass-finalization should be removed; keep BFT + diversity floor.
- Data ingestion is the next major workstream and will depend on these same primitives.
- Multiple prompt suites are already produced and available as `.md` files.

---

*End of handoff.*
