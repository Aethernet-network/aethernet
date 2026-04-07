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
