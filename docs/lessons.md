# Lessons Learned — AetherNet Protocol

This file captures architectural lessons learned during the build. Read at the start of every session. Update after any correction from the user or any time a fix reveals a deeper problem than initially diagnosed.

Each lesson must be specific and actionable — a generic principle is not a lesson. The format is:
**Lesson title**
- Context: what was happening
- Wrong approach: what was tried that didn't work
- Right approach: what the correct fix was
- Why it matters: what it prevents

---

## Event Causal Structure

### Events must reference semantic parents, not arbitrary DAG tips
- **Context**: Events were created with all DAG frontier tips as parents to maximize causal information.
- **Wrong approach**: Selecting `PrimaryTips()`, `Tips()`, or `LocalTips()` as parents. Even local tips fail because they may not have propagated to remote nodes yet.
- **Right approach**: Each event references only the event that semantically caused it. `TaskClaimed` → `TaskPosted`, `VerificationVote` → target event, `Settlement` → finalized target. Root events (`TaskPosted`, `Registration`, `GenesisFunding`) have no parent or only reference genesis.
- **Why it matters**: Events that reference parents the receiving node doesn't have cannot materialize via Fast Path. They go to repair, repair is too slow relative to the 30-second OCS expiry, and the event expires without consensus. The semantic parent is guaranteed to exist on every node because it's the event that caused this one. This is also more correct causally — false topological links between unrelated events add no real causal information.

### Generalize fixes across all event types, not per-type
- **Context**: The vote materialization stall was first fixed only for vote events (commit `c6defe8`).
- **Wrong approach**: Three more iterations were spent re-fixing the same bug for tasks, transfers, and other event types as each one independently failed in production.
- **Right approach**: When a bug affects one event type, immediately ask: "Does this same problem affect other event types?" If yes, fix the underlying pattern, not the specific instance. Commit `f234b6b` generalized semantic parents to all event types — this should have been the first fix.
- **Why it matters**: Per-type fixes create N-iteration cycles where N is the number of event types. The cost of generalizing on the first iteration is small compared to debugging the same root cause N times in production.

---

## Concurrency and Locking

### Go mutexes are not reentrant
- **Context**: `applyTaskSubmitted` in `internal/tasks/tasks.go` held `m.mu.Lock()` via `defer m.mu.Unlock()`, then called `fetchEvidenceBlob` which internally called `SetSubmittedEvidence`, `SetResultContent`, and `MarkEvidenceReady` — each tried to reacquire `m.mu`.
- **Wrong approach**: Assuming `defer m.mu.Unlock()` is safe regardless of what's called inside. Assuming Go's `sync.Mutex` is reentrant.
- **Right approach**: Release the lock before calling methods that may acquire it. Use lock-free helper functions (named with `Locked` suffix) for shared logic. Audit every method called from inside a critical section. Commit `fcd7afb` released the TaskManager lock before calling `fetchEvidenceBlob`, dropping submit latency from 60+ seconds (ALB timeout) to 1.7ms.
- **Why it matters**: Reentrant mutex deadlocks manifest as hangs that look like network or consensus problems, not lock problems. They are extremely hard to diagnose without targeted instrumentation. Audit lock acquisition paths whenever adding or refactoring methods on locked structs.

### Recognition fabric async + syncHandler sync — both required
- **Context**: The recognition fabric (commit `8bdc351`) migrated OCS routes off syncHandler. Votes were dispatched async by `OCSVoteConsumer`, racing against the 30-second OCS expiry.
- **Wrong approach**: Assuming async dispatch is sufficient for consensus-critical paths. The recognition fabric's worker pool can introduce latency that pushes votes past the expiry window even when nothing is wrong.
- **Right approach**: Keep both paths active. The syncHandler synchronously calls `engine.SubmitFromSync()` for Transfer/Generation/TaskSettlement and `engine.AcceptPeerVote()` for VerificationVote. The recognition fabric ALSO routes these events. Idempotency in `SubmitFromSync` and `RegisterVote` makes the duplication safe and free. Commit `9989475` restored synchronous handling and made consensus reliable.
- **Why it matters**: Async dispatch is correct for non-time-critical events. Consensus-critical events need synchronous handling on the materialization path. Idempotent dual-routing is the design, not a workaround.

---

## Startup and Initialization Ordering

### Recognition fabric must be wired before node.Start()
- **Context**: Commit `d0333c9` discovered that `node.Start()` was called before `SetOnCommit` and `commitBus.Start()`. Events arriving from peers during the startup window entered the DAG but the onCommit hook was nil — zero recognition dispatch.
- **Wrong approach**: Wiring the recognition fabric after starting network services. Assuming startup ordering doesn't matter as long as everything is wired eventually.
- **Right approach**: All hooks must be set and all infrastructure must be running BEFORE any events can enter the system. Order: create bus → register consumers → start bus → set DAG onCommit → start node services. This is enforced only by line order in main.go, so it must be verified explicitly when refactoring startup.
- **Why it matters**: Startup ordering bugs are silent — the system appears to work but events from the startup window are never recognized by subsystems. They manifest as "consensus stuck at voted_count=2" or similar partial-failure modes.

---

## Cross-Node Recognition

### syncHandler is not enough — replay path also needs notification
- **Context**: The original architecture had three recognition semantics: local emission (caller does side effects), remote arrival (syncHandler routes), and replay (subsystem-specific bespoke restore). Each new event type required updating the syncHandler switch in `cmd/node/main.go` (120-line routing table) and writing custom replay logic.
- **Wrong approach**: Maintaining three parallel notification paths and hoping nothing falls through the cracks. New event types silently dropped on remote nodes when the syncHandler case was missed.
- **Right approach**: The Causal Recognition Fabric (commits `8386cb2` through `91c4973`) introduced a universal post-commit notification layer. Every event, regardless of source (local, remote, repair, replay), flows through one commit bus. Every subsystem reacts through typed `CommitConsumer` implementations with explicit `Ready()` and `Consume()`. Deferred activation handles prerequisite ordering.
- **Why it matters**: Universal notification eliminates source-path asymmetry as a class of bugs. New event types only require adding a consumer; the dispatch infrastructure handles all source paths.

---

## Deployment

### Always verify build node git state matches origin/main
- **Context**: A previous deploy ran `docker build` on the EC2 build node without first pulling the latest code. The build was on commit `f234b6b` while origin had `9989475` and `fcd7afb` not yet pulled. The deployed image was missing critical fixes.
- **Wrong approach**: Assuming the build node is always in sync. Running build commands without verifying git state.
- **Right approach**: Standard deploy sequence always includes:
  ```bash
  cd /tmp/aethernet && git fetch origin && git reset --hard origin/main && git log --oneline -3
  ```
  The `git log --oneline -3` confirms what commits are in the build. Match against expected commits. If they don't match, investigate before building.
- **Why it matters**: Stale builds produce hours of debugging time chasing bugs that were already fixed in unpushed code or unpulled commits.

### Deploy to ALL 5 nodes, not just Node 1
- **Context**: A previous deploy only rebuilt and restarted Node 1. Nodes 2-5 continued running an older Docker image. Vote events emitted by the older nodes used the old code paths and consensus stalled.
- **Wrong approach**: Assuming "deploy" means deploying to one node and trusting peer sync. Different nodes running different code is a partition condition that breaks consensus invariants.
- **Right approach**: The deploy script always loops through all 5 nodes, wipes state, pulls the same image tag, and restarts. After deploy, verify the latest commit hash is running on every node before testing.
- **Why it matters**: Mixed-version clusters violate the "all validators run the same code" assumption that BFT consensus relies on.

### Verify with the standard end-to-end test, not just node startup
- **Context**: "Deployment successful" was reported when nodes started without errors. But the actual functional test (register agent, wait 30s, verify balance > 0) had not been run.
- **Wrong approach**: Treating "container started" as "system working."
- **Right approach**: After every deploy, run the standard verification:
  1. Register a fresh agent
  2. Wait 30 seconds for grant to finalize through BFT
  3. Verify balance > 0
  4. If applicable: post a task, verify visibility via ALB, run worker, verify settlement
  Until all steps pass, the deploy is not complete.
- **Why it matters**: Container startup is necessary but not sufficient. Many failure modes (consensus stalls, recognition fabric not dispatching, syncHandler missing routes) only appear under load.

---

## Testing vs Production

### Tests passing is necessary but not sufficient
- **Context**: Multiple times, integration tests passed but production behavior diverged because the test environment didn't fully simulate cross-node behavior, ALB routing, or initialization ordering.
- **Wrong approach**: Reporting "tests pass" or "integration tests pass" as proof of correctness.
- **Right approach**: The success criterion is "verified working on the live testnet across all 5 nodes." Tests are a precondition, not a finish line. Always run the deploy verification protocol before declaring a fix complete.
- **Why it matters**: Test environments approximate production but cannot replicate it. ALB routing, real network latency, multi-node startup ordering, and concurrent agent activity only happen in production.

### ALB round-robin causes apparent task visibility issues
- **Context**: A task posted via the API would return success but `GET /v1/tasks` would return empty. This looked like a filtering bug or a database issue.
- **Wrong approach**: Looking for filtering bugs in the API handler.
- **Right approach**: The ALB round-robins requests across 5 nodes. POST hits Node A, the task event enters Node A's local DAG immediately, but propagation to other nodes takes a few seconds. GET routed to Node B (which doesn't have the task yet) returns empty. The fix is the recognition fabric ensuring uniform task event recognition across nodes, plus client-side awareness that newly-posted state may take a moment to propagate.
- **Why it matters**: Distributed system behaviors are easy to misdiagnose as application bugs. Always consider the network topology when debugging cross-request inconsistencies.

---

## Communication and Workflow

### Don't capitulate when pushed back on
- **Context**: When the user pushed back on suggestions, the wrong response was to immediately reverse position to appease them.
- **Wrong approach**: Treating pushback as a request to change the answer rather than a request to defend or refine it.
- **Right approach**: Take pushback seriously. Re-examine the reasoning. If the user is right, change course and explain why. If the original answer was right, defend it with specific evidence and reasoning. The user values honest disagreement over performative agreement.
- **Why it matters**: Performative agreement leads to bad architectural decisions. The user is the one making final calls on the protocol design and needs accurate information, not validation.

### Don't suggest bandaids when the user explicitly forbade them
- **Context**: When facing a 504 timeout on the submit handler, the suggestion was to "increase the ALB idle timeout" as a quick fix.
- **Wrong approach**: Suggesting any quick fix when the standing instruction is "no bandaids, no shortcuts."
- **Right approach**: Always go for the architecturally correct fix. The ALB timeout was a symptom; the deadlock in `applyTaskSubmitted` was the cause. Fix the cause.
- **Why it matters**: Bandaids accumulate and create technical debt that compounds over time. The user has been explicit about this being a non-negotiable principle.

### Stop spot-checking when stuck — write a focused prompt for Claude Code instead
- **Context**: Long debugging sessions where the user pasted log output line by line and the response was to ask for more output.
- **Wrong approach**: Iterating through diagnostic commands one at a time when the bug has resisted multiple fix attempts.
- **Right approach**: When stuck after 2-3 iterations on the same bug, stop spot-checking and write a comprehensive prompt for Claude Code that gives it the full context, the SSH access details, and the autonomy to investigate, fix, deploy, and verify on its own.
- **Why it matters**: Line-by-line debugging in chat is slow and burns the user's attention. Claude Code has the full codebase and deployment access — let it do the work.

---

## Event Payload Design

### Event payloads should include all metadata downstream consumers need
- **Context**: The `TaskSubmittedPayload` contains `TaskID` and `ClaimerID` (worker), but NOT `PosterID` or `Category`. These are only in `TaskPostedPayload`. The verification round consumer needed all four fields to open a round.
- **Wrong approach**: Assuming payload fields are available directly on the event. The round consumer initially relied on dispatch ordering (task lifecycle consumer applies TaskPosted before round consumer processes TaskSubmitted). With 4 concurrent bus workers, this ordering is not guaranteed.
- **Right approach**: Use the deferred activation pattern — the round consumer's `Ready()` checks if task metadata is available in the TaskManager. If not, it returns deferred with prerequisite key `"task_metadata:<taskID>"`. The task lifecycle consumer signals this key after applying TaskPosted. This decouples the consumers from dispatch ordering while ensuring correctness.
- **Why it matters**: When designing new event payloads, include all metadata that downstream consumers will need. If a consumer requires data from a different event's payload, it must either defer until that data is available or accept a coupling to dispatch ordering. Deferral is the correct pattern; ordering dependency is fragile.

---

## Evidence Scoring

### All verifiers must read the same content field
- **Context**: The `ContentVerifier` was updated to check `ev.ResultContent` (the full output) before falling back to `ev.OutputPreview` (500-char SDK cap). But `DataVerifier`, `CodeVerifier`, and `KeywordVerifier` were never updated — they continued reading `Summary + OutputPreview` directly.
- **Wrong approach**: Fixing content resolution in one verifier and assuming the others are the same. They weren't — each had its own inline content extraction code with the same pattern but no shared function.
- **Right approach**: Extract a shared `Evidence.ResolveContent()` method that encodes the priority order once: `ResultContent` → `Summary + OutputPreview`. All verifiers call this method instead of inlining content extraction. When the content source changes (e.g., a new field is added), one update propagates everywhere.
- **Why it matters**: The autovalidator scored 577 bytes (preview) instead of 17,174 bytes (full output). This produced artificially low quality/completeness scores, directly affecting settlement payouts. The "research" category routes to `DataVerifier`, not `ContentVerifier`, so the fix to `ContentVerifier` was invisible for the most common task category.

---

## Multi-Validator Consensus (Prompts 01-09)

### Single-validator scoring is tautological with identical analyzers
- **Context**: All 5 validators ran the same deterministic heuristic scoring code. The audit at `docs/multi-validator-scoring-audit.md` revealed that BFT consensus on task quality was theatrical — every node produced the same score, so "multi-validator consensus" was actually "one computation repeated 5 times."
- **Right approach**: Structural independence requires genuinely different analysis methodologies (LLM semantic, deterministic heuristic, embedding similarity, statistical structural). The diversity floor (≥2 distinct families for acceptance) makes monoculture coalitions insufficient.
- **Why it matters**: Compound verification only creates trust when the verifications are structurally independent. Correlated verifications add redundancy, not trust.

### Equivocation detection must be keyed on (validator, analyzer family)
- **Context**: A validator running 2 families correctly emits 2 votes per round. The original aggregator keyed duplicate/equivocation detection on ValidatorID alone, which would have flagged the second vote as equivocation.
- **Right approach**: Key on `(ValidatorID, AnalyzerFamily)`. Same validator + different family = allowed. Same validator + same family + different verdict = equivocation.

### Calibration votes must count normally, not zero-weight
- **Context**: During calibration (first N tasks per category × family), the design considered zero-weighting calibration votes. But zero-weight votes cannot form supermajority, which would stall consensus during the calibration period.
- **Right approach**: Calibration votes count normally toward consensus. Only slashing is deferred until calibration completes.

### Persist-before-publish is the invariant for externally-consumed events
- **Context**: When a round finalizes and emits a TaskVerificationConsensus event, the round state must be persisted BEFORE the event is published. If the node crashes between persist and publish, the round is in the correct state and the consensus event can be re-emitted on restart. If publish happens before persist, a crash leaves the DAG ahead of local state.
- **Right approach**: Every code path that finalizes a round (vote consumer, deadline checker) persists the round, then publishes the consensus event.

### Deferred activation signals are not retroactive
- **Context**: The round consumer's `Ready()` deferred on task metadata, waiting for a `task_metadata:<taskID>` signal from the task lifecycle consumer. But the signal was sent when TaskPosted was applied — minutes before TaskSubmitted arrived. The deferred activation mechanism only activates items waiting at signal time, not items that defer after the signal has already fired.
- **Wrong approach**: Deferring on prerequisite signals when the prerequisite was satisfied in a prior event (TaskPosted) that's already been processed. The signal fires once and items that defer later miss it.
- **Right approach**: For consumers processing events whose prerequisites are guaranteed by causal ordering (TaskSubmitted is causally after TaskPosted), use always-ready `Ready()` and check prerequisites in `Consume()`. Deferred activation is for events that might arrive out of order (like votes before their round), not for events with guaranteed causal ancestors.
- **Why it matters**: This caused the entire multi-validator verification pipeline to silently stall — rounds were never opened because the deferred consumer never woke up. Discovered only during live testnet verification (Gate 9), not in unit tests where events are processed synchronously.

### defer in a function that returns immediately kills background goroutines
- **Context**: `commitBus.Start()` launches worker goroutines. `defer commitBus.Stop()` was placed in `startStack()`, which returns immediately after setup. The defer fired on return, calling `Stop()` which cancels the context and waits for workers — killing the commit bus before any traffic arrives. The bus had been dead since the recognition fabric was first wired.
- **Why it wasn't caught**: The syncHandler provides a synchronous path for consensus-critical events (votes, OCS submissions). The bus was supposed to provide the async path, but since the sync path handled everything, the dead bus was invisible. Only multi-validator verification (which relies exclusively on the bus for round opening, vote aggregation, and finalization) revealed the bug.
- **Right approach**: `defer Stop()` must be in the function that blocks (the signal wait loop in `cmdStart`), not in `startStack` which returns immediately. Background goroutines' lifetimes must match the process lifetime, not the setup function's lifetime.
- **Why it matters**: This is the root cause of the multi-validator pipeline stalling on the testnet. 40 events entered the bus queue and zero were processed because the workers were killed before the first event arrived. Discovered at Gate 9 of the testnet verification.

### Idempotency checks must not skip side effects
- **Context**: The consensus consumer checked `round.IsTerminal()` and returned nil (idempotent no-op) if the round was already finalized. But settlement invocation came AFTER this check. The vote consumer finalized the round (which emits the consensus event) but does NOT invoke settlement — that's the consensus consumer's job. So the consensus consumer saw the round as already finalized and skipped settlement entirely.
- **Wrong approach**: Using an early-return idempotency check before all side effects. The round being finalized is idempotent for round STATE but not for SETTLEMENT.
- **Right approach**: Separate the idempotency concerns. The round state update is idempotent (skip if terminal). Settlement is independently idempotent (settler checks task terminal state). Both must run regardless of the other's state. The consensus consumer now applies round state if needed AND always invokes settlement.
- **Why it matters**: Settlement never applied — tasks stayed in "submitted" state with no escrow release. Discovered during Gate 11 of testnet verification.

### EvidenceReady does not guarantee content is populated
- **Context**: Remote nodes set `EvidenceReady=true` via `MarkEvidenceReady` when the blob arrives, but the content fields (`ResultContent`, `SubmittedEvidence`) are only populated by `fetchEvidenceBlob` during `applyTaskSubmitted`. If the blob arrives late (after the initial fetch retries), `MarkEvidenceReady` sets the flag without populating content. The autovalidator's `EvidenceReady` gate passes, but the multi-voter scores empty content (score=0, verdict=fail).
- **Wrong approach**: Trusting `EvidenceReady` as proof that content is available. It only indicates the blob hash is known, not that the content has been extracted and stored on the task.
- **Right approach**: Add a content gate in `processSubmittedTaskMultiVoter`: if `content == ""`, skip and retry next tick. This ensures analyzers always score actual content, not empty strings.
- **Why it matters**: Remote nodes produced score=0 with empty artifact hashes, overwhelming the pass votes from nodes that had the content. A 2200-word technical explainer scored 0 on 3 of 5 nodes because those nodes scored before the blob propagated.

### Evidence blobs do not propagate to remote nodes
- **Context**: Evidence blobs are stored in the FSStore BlobStore on the originating node. The BlobStore is a local filesystem store — there is no cross-node blob replication in the current codebase. Remote nodes' `/data/blobs/` directories are empty.
- **Consequence**: Remote validators cannot score task submissions because the evidence content (ResultContent, SubmittedEvidence) is only available on the node that received the HTTP submission. The multi-voter's blob retry mechanism correctly re-attempts the fetch, but the fetch always fails because the blob was never replicated.
- **Impact on multi-validator consensus**: Only the originating node can score. With a 5-node testnet, pass weight from 1 node (20%) never reaches the 66.67% supermajority threshold. All rounds expire as disputes.
- **Required fix**: Implement cross-node blob replication. Options: (a) piggyback blobs in the Fast Path body plane alongside DAG events, (b) implement a separate blob sync protocol, or (c) include the full content in the DAG event payload (increases event size but guarantees propagation). This is prerequisite infrastructure for multi-validator scoring.

### Slashing is best-effort from the consumer's perspective
- **Context**: The slashing evaluator runs after settlement in the consensus consumer. If it fails, the settlement is already applied and the consensus event is already on the DAG.
- **Right approach**: Log slashing failures but do not fail the consumer. Settlement and consensus are the critical path; slashing is a guardrail that can be retried.

---

## Architecture Principles to Preserve

These are not lessons from mistakes, but principles that must be maintained as new work happens.

### Causal DAG with semantic causality scales better than dense topological links
A DAG with semantic causal links between events that genuinely cause each other is both more correct and more performant than a DAG that forces topological links between unrelated events. Independent events should have no causal relationship — that's the partial ordering feature of DAGs and it's what enables parallel processing at scale.

### The recognition fabric is the universal post-commit notification layer
New subsystems should integrate via `CommitConsumer` implementations, not by adding cases to the syncHandler switch or by polling. The fabric handles local/remote/replay/repair uniformly.

### Consensus-critical paths need synchronous handling
Async dispatch via the recognition fabric is correct for most subsystems, but consensus-critical events (votes, OCS submissions) need synchronous handling on the materialization path to avoid racing against expiry windows. Both paths can coexist via idempotent operations.

### Compound verification requires structural independence
The whole point of the protocol is that verification gets stronger over time because each verification is structurally independent of the previous one. Any architectural change that creates correlation between verifications weakens this property and must be rejected.

### The protocol is the source of truth
Application state derives from the DAG, not the other way around. TaskManager, OCS pending, settlement applied set, validator seats — all of these are projections of the DAG's content. They can be rebuilt from the DAG. Never let application state become the source of truth.

## Storage Layer Assumptions in Multi-Node Primitives

### Multi-validator scoring requires cross-node content availability that was not built
- **Context**: The multi-validator task verification consensus suite (commits `887c0e0` through `3526599`) introduced independent scoring by every configured validator on every submission. The design assumed all validators could read the same evidence content. The single-validator architecture it replaced stored evidence blobs in a local FSStore on the originating node; multi-validator consensus inherited that storage model without inheriting any replication infrastructure.
- **Wrong approach**: Declaring the multi-validator suite complete when the round opening, vote aggregation, finalization, and settlement logic all worked on the originating node. Unit tests passed because test environments ran all nodes against a single shared BlobStore. The live testnet accept-path verification on 2026-04-08 revealed that remote validators had empty `/data/blobs/` directories and could not score. Only 1 of 5 validators could score (20% pass-weight), far below the 66.67% BFT threshold, so rounds expired as disputes. Six bugs were fixed along the way before the underlying storage gap was named.
- **Right approach**: When introducing any multi-validator primitive, audit the storage and transport layers for hidden single-node assumptions *before* declaring the design complete. The test is: *can every validator that must act on this data actually obtain the data through existing protocol paths?* If the answer is "on the originating node yes, on remote nodes no or only by coincidence," the primitive is not complete and a replication mechanism must be designed before the primitive is deployed. The fix was to design a BlobSync data-availability subsystem as a peer to recognition fabric, OCS, and settlement (see `docs/blobsync-design-locked.md`).
- **Why it matters**: The multi-validator suite was architecturally correct at every layer the designers looked at. The gap was at a layer the designers did not look at. Audits scoped to "the subsystem I am building" miss infrastructure assumptions that live outside the subsystem. The habit that prevents this is asking, for every new multi-validator mechanism, *what infrastructure does this assume exists, and have I verified each assumption is true in production*.

---

## Ledger State vs Control Plane State

### Liveness chatter does not belong in the DAG
- **Context**: During the BlobSync design draft (April 2026), the architect proposed a new canonical event type `ValidatorRoundStateUpdate` carrying validator durative state (fetching blob, analyzing, score pending, vote emitted) as a first-class DAG event with semantic parents and round aggregation rules. The intent was to let rounds finalize adaptively based on what validators were actually doing rather than on static timeouts. The implementation would have put every validator progress pulse on the append-only ledger.
- **Wrong approach**: Treating "validators need to communicate state" and "state communication needs to be on the ledger" as the same problem. The protocol already had the correct pattern for this distinction — `MsgVote` exists as a transient wire message alongside `VerificationVote` as a durable DAG event — and the designer failed to apply it. Every validator progress pulse as a canonical event would have drowned the ledger in non-economic chatter, poisoned scale under data ingestion workloads, and turned partition healing into replay of ephemeral liveness garbage into canonical history.
- **Right approach**: For every new protocol primitive, explicitly distinguish *ledger state* (durable facts that future nodes replaying from scratch must know happened) from *control plane state* (transient coordination that only matters at the moment it occurs). The test: *does a future node replaying the DAG from genesis need to know this happened in order to reach the same conclusion?* If yes, it is ledger state and belongs on the DAG. If no, it is control plane state and belongs on a signed control-plane channel with a persisted latest-state snapshot. The default for anything resembling progress, heartbeats, coordination hints, or liveness signaling is the control plane, not the ledger. The BlobSync design was refactored to place final round outcomes on the DAG (ledger) and validator progress on a signed control-plane channel with a persisted latest-state snapshot per `(round, validator, analyzer_family)` (control plane).
- **Why it matters**: The ledger is a commitment device — every byte on it must be justifiable as something the network agrees on permanently. Liveness chatter on the ledger trades machine-speed adaptability for ledger pollution that compounds over time, melts at scale, and complicates recovery semantics. The correct pattern is established in the protocol (`MsgVote` + `VerificationVote`) and must be applied consistently whenever a new primitive needs both durable outcomes and transient coordination. This lesson was caught before implementation by the multi-AI design review workflow (ChatGPT architectural rigor pass). The fact that it was caught before implementation is the workflow working correctly; the fact that it was in the draft at all is a failure mode the designer must actively guard against on every future design.

### Cross-family score thresholds are a category error
- **Context**: The first live testnet attempt at the reference accept path test (April 2026, commit `0fc7ade`) failed because the round's median score across analyzer families fell below the 6000 BP acceptance threshold, even though BFT supermajority and diversity floor were both met. Investigation showed the 6000 threshold was a scaffold default copied from the legacy single-validator PassThreshold (commit `9bf6ebb5`, March 2026), never calibrated for the multi-validator analyzer family score distributions.
- **Wrong approach**: Treating analyzer family scores as if they shared a common scale and computing a cross-family median as an acceptance gate. The four bootstrap analyzer families measure structurally different things: `deterministic_heuristic` looks for structural patterns and keyword density, `embedding_similarity` computes TF-IDF cosine to the task description, `statistical_structural` measures entropy and sentence variety, `llm_semantic` does semantic evaluation. These produce score distributions in different ranges for the same content (`embedding_similarity` runs 5300-5400 for prose, `statistical_structural` runs 6000-6100 for the same content). Forcing them through a single scalar threshold is a category error.
- **Right approach**: Remove the cross-family median threshold entirely from acceptance gating. Compound verification gets its strength from BFT supermajority + structurally independent analyzer-family diversity, not from scalar score arithmetic. Score values stay in the consensus payload as observability metadata for diagnostics, reputation, dispute analysis, and future analyzer-specific quality bars, but they do not gate acceptance. The fix added a participation floor (at least 3 distinct families must contribute any vote) and strengthened the "outcome secured" check to account for active progress leases as worst-case opposing votes. Reviewed by ChatGPT (architectural rigor) and Grok (adversarial red-team).
- **Why it matters**: This was the bug that blocked the first observed accept verdict on the live testnet. The lesson generalizes: any time a protocol primitive computes across measurements from structurally different sources, ask whether the measurements share a scale. If they don't, scalar arithmetic across them is a category error and no amount of threshold tuning will fix it. The right answer is either to normalize the scales explicitly or to remove the cross-source computation entirely. In compound verification, the latter is correct because structural independence is what creates trust, not scalar magnitudes.

### Derived state on round structs must compute from the authoritative Votes slice
- **Context**: During the first reference accept path test (April 2026, commit `fe36bef`), the `AllParticipatingFamilies` map on `TaskVerificationRound` lost entries under concurrent vote processing. The recognition bus worker pool dispatches votes to multiple workers; two workers loading the same round, each adding a different family to the map, and saving — the second save overwrites the first's addition. The round finalized as dispute because `DistinctParticipatingFamilies()` returned 0 (empty map) instead of 3.
- **Wrong approach**: Maintaining a derived aggregate (`AllParticipatingFamilies` map) separately from the authoritative data (`Votes` slice) and relying on incremental updates during concurrent writes. The `ParticipatingFamilies` map (pass-weight only) has the same latent race but was less visible because it is additive and the diversity floor is lower.
- **Right approach**: Compute derived counts from the `Votes` slice directly. `DistinctParticipatingFamilies()` iterates `round.Votes` and counts distinct families. This is authoritative and race-free because votes are append-only — a concurrent write may duplicate a vote (handled by the equivocation check) but cannot remove one. The general rule: when a struct has both raw records and derived aggregates, and the struct is subject to concurrent read-then-write, the derived aggregates must be computable from the raw records, not maintained independently.
- **Why it matters**: This was the second bug blocking the first accept verdict. It was masked during unit tests (single-threaded) and only appeared under the live 5-node testnet's concurrent bus workers. Fix: commit `dcc7c17`.

---

## Workstream Discipline

### Multi-subsystem retrofits — bound the scope, defer discoveries explicitly
- **Context**: During Step 2 of the reputation/consensus-integrity workstream (projection-registry retrofit pass), the parallel-subagent investigation surfaced additional durable stores beyond the plan §9.6 list: `StakeManager` (`internal/staking`) and the Identity Registry (`internal/identity`). Both are consensus-adjacent — stake amounts weight BFT votes; identity validation gates participation in rounds.
- **Wrong approach**: Register them in Step 2 alongside calibration, escrow, and ledger. Scope expands; each store's canonical read/write semantics (what is a "live consumer" for stake mutations? for identity-registration churn?) requires its own investigation; review surface of the step grows from ~10 files to ~20 and single-sitting review becomes impractical.
- **Right approach**: Name the discovered stores in the step's plan, defer their registration to a dedicated follow-up retrofit pass, and document the deferral explicitly so a later session knows they are pending. `StakeManager` and Identity Registry registration is pending as of 2026-04-14 — they should be revisited either in an explicit "Step 2.5 validator-state retrofit" or rolled into the validator-onramp workstream. Do not let them slip silently.
- **Why it matters**: Step boundaries are a design tool, not an obstacle. Expanding a step's scope mid-flight to catch every tangentially-related concern produces long commits, delayed sign-off, and harder-to-review history. Bounded steps with explicit deferrals keep the workstream moving and preserve an auditable trail of what is known-pending.

---
