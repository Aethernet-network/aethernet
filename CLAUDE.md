# CLAUDE.md — AetherNet Protocol

## What This Project Is

AetherNet is a production-grade Layer 1 blockchain protocol built on a causal DAG architecture, serving as the trust and settlement layer for the AI agent economy. Written in Go, the protocol verifies AI agent work and settles economic transactions through BFT consensus with compound verification. The northstar is creating an environment where AI and humanity improve synergistically through protocol-enforced accountability rather than model iteration.

This is not a prototype. This is not a research project. This is production-grade infrastructure intended to scale to millions of events per hour and billions of users (human and agent). Every line of code must meet the standard "world's best engineers built this — impressive on first encounter and on the thousandth."

## Engineering Standard

**No bandaids. No shortcuts. No special cases. No fixes that only satisfy tests.**

Every change must be:
- Architecturally correct, not symptom-targeted
- Generalizable across all event types and use cases, not narrowly scoped to the current bug
- Verified working in the target environment (live testnet), not just in unit tests
- Beautiful enough that experienced distributed systems engineers would study it

If a fix feels hacky, stop and ask: "Knowing everything I know now, what is the elegant solution?" If a fix only addresses one event type when the same problem affects all event types, generalize it. If a fix only makes a test pass, it is not a fix.

The standard is not "tests pass." The standard is "verified working on the live testnet across all 5 nodes, with autonomous end-to-end pipeline completion."

## Workflow Discipline

### 1. Plan Mode Required

For any non-trivial change, enter plan mode before writing code. A change is non-trivial if any of the following are true:
- Touches more than one file
- Modifies any interface, contract, or public API
- Changes state mutation paths or event handling
- Could affect cross-node behavior, consensus, settlement, or networking
- Adds or removes a callback, hook, or notification
- Affects startup ordering or initialization sequence
- Modifies any file under `internal/dag`, `internal/network`, `internal/ocs`, `internal/recognition`, `internal/consensus`, `internal/tasks`, `internal/autovalidator`, `internal/settlement`, `internal/identity`, `internal/ledger`, or `cmd/node/main.go`

In plan mode:
1. Write a detailed plan to `docs/plans/YYYY-MM-DD-<short-description>.md`
2. Show the plan to the user before implementing
3. Wait for sign-off
4. Implement against the approved plan
5. If implementation reveals the plan was wrong, STOP, update the plan, and re-confirm

Trivial changes (typo fixes, log message wording, single-function bug fixes that don't change observable behavior) skip plan mode but are still subject to verification before completion.

### 2. Subagent Boundaries

When investigating or implementing changes that span multiple subsystems, spawn one subagent per affected boundary. The boundaries are:

**State & Consensus** — anything that mutates protocol state or affects what nodes agree on.
Includes: `internal/dag`, `internal/event`, `internal/ocs`, `internal/consensus`, `internal/identity`, `internal/ledger`, `internal/settlement`, `internal/validator`, validator lifecycle, staking, slashing.

**Transport & Recognition** — how events move between nodes and how subsystems learn about them.
Includes: `internal/network` (fastpath, repair, materialize, sync, ingest, completion), `internal/localpub`, `internal/recognition`, `internal/blobstore`.

**Application & Interface** — how the protocol is exposed and how application logic responds to protocol events.
Includes: `internal/api`, `internal/tasks`, `internal/autovalidator`, `internal/trajectory`, `internal/marketplace`, SDK integration points.

A change to vote handling touches all three (consensus rules, transport, autovalidator) so spawn three subagents in parallel, each reading their layer fully, then synthesize. A change purely in the API layer gets one subagent.

When spawning a subagent, give it: the specific question to answer, the boundary it owns, and the constraint that it must read the actual code, not assume.

### 3. Self-Improvement Loop

After ANY correction from the user or any time a fix reveals a deeper problem than initially diagnosed, update `docs/lessons.md` with the pattern. Lessons must be:
- Specific (not "be careful with locks" — "TaskManager methods cannot be called while holding m.mu because Go mutexes are not reentrant")
- Actionable (the lesson must prevent the same mistake)
- Reviewed at the start of every session for relevance to the current work

Read `docs/lessons.md` at the start of every session. If a current task touches a known lesson area, surface the lesson explicitly before implementing.

### 4. Verification Before Done

A task is not complete until it is verified working in the target environment.

**For protocol changes**, the verification protocol is:
1. `go test -race ./...` passes across all packages with zero failures
2. Build on EC2 build node (44.200.60.102), push to ECR
3. Wipe and redeploy all 5 testnet nodes
4. Register a fresh agent, wait 30 seconds, verify balance > 0
5. Post a task, verify it appears via ALB GET /v1/tasks
6. If applicable to the change, verify the autonomous worker pipeline produces a settlement

If step 1-6 don't all pass, the task is not done. Do not say "tests pass, deployed successfully" without running this protocol.

**For application/API changes**, verify the change end-to-end through the API on the live testnet, not just in unit tests.

**For SDK or worker changes**, verify the change against the live testnet with a real agent flow.

Never mark a task complete by saying "the integration tests pass." Tests are necessary but not sufficient.

### 5. Demand Elegance

For non-trivial changes, pause before presenting and ask:
- Is this the architecturally correct solution, or is it a symptom fix?
- Does this generalize to all cases, or only the current bug?
- Would a staff engineer at a top-tier protocol company approve this?
- Is there a more elegant solution I'm avoiding because it's harder?
- Am I adding a special case where I should be generalizing the existing pattern?

If any answer is "no" or "I'm not sure," stop and reconsider. The right fix is usually one level deeper than the obvious one.

### 6. Autonomous Bug Fixing

When given a bug report, just fix it. Don't ask the user for clarification about logs, errors, or which files to look at unless the request is genuinely ambiguous. SSH into nodes, read logs, trace code paths, run tests, find the root cause, fix it, verify the fix, deploy if applicable, report the result.

Zero context switching required from the user. The user should be able to say "the grant Transfer isn't finalizing" and walk away, and come back to either a fix that's working or a clear explanation of why the problem requires their input.

## Critical Architectural Rules

These rules were learned the hard way and must not be re-violated.

### Event Causal References

Events MUST reference only their semantically relevant parent — the event that causally triggered them. NEVER reference arbitrary DAG tips, frontier events, or recent unrelated events.

Correct semantic parents:
- `TaskClaimed` → the `TaskPosted` event being claimed
- `TaskSubmitted` → the `TaskClaimed` event
- `TaskApproved` → the `TaskSubmitted` event
- `VerificationVote` → the target event being voted on
- `Settlement` → the target event being settled
- `TaskPosted`, `Registration`, `GenesisFunding` → root events with no parent (or genesis only)

Why: events that reference parents the receiving node doesn't have yet cannot materialize via Fast Path. They go to repair, repair is too slow relative to the 30-second consensus expiry, and the event expires without consensus. The semantic parent is guaranteed to exist on every node because it is the event that semantically caused this one.

### Local vs Remote Event Recognition

Both the recognition fabric AND the syncHandler must handle consensus-critical events synchronously. The recognition fabric's async dispatch is correct for most cases, but votes and OCS submissions race against the 30-second consensus expiry window. Keep both paths active with idempotent handlers — duplicate registration is safe and is the design.

The syncHandler must synchronously call:
- `engine.SubmitFromSync()` for Transfer/Generation/TaskSettlement
- `engine.AcceptPeerVote()` for VerificationVote

The recognition fabric also handles these via OCSSubmitConsumer and OCSVoteConsumer. Both paths are active. Idempotency in `engine.SubmitFromSync` and `RegisterVote` makes the duplication safe.

### Lock Reentrancy

Go's `sync.Mutex` is NOT reentrant. A method holding `m.mu.Lock()` via `defer m.mu.Unlock()` cannot call any other method on the same struct that also acquires `m.mu`. This causes permanent deadlock.

When refactoring or adding methods to TaskManager, OCS Engine, recognition Index, or any other struct with internal locking:
- Audit every called method to verify it does not try to reacquire the same lock
- Release the lock before calling methods that may acquire it
- Use lock-free helper functions (named with `Locked` suffix to indicate caller must hold the lock) for shared logic

Example: `applyTaskSubmitted` previously held `m.mu.Lock()` and called `fetchEvidenceBlob` which internally called `SetSubmittedEvidence`, `SetResultContent`, and `MarkEvidenceReady` — each tried to reacquire `m.mu`, causing 60-second deadlocks until ALB timeout. Fix was to release the lock before calling `fetchEvidenceBlob`.

### Deploy Verification

Always verify the build node's git state matches `origin/main` before building. Build artifacts deployed to nodes may not include the fix you think they include.

Standard deploy sequence:
```bash
ssh ubuntu@44.200.60.102 "cd /tmp/aethernet && git fetch origin && git reset --hard origin/main && git log --oneline -3"
```

The `git log --oneline -3` confirms what commits are in the build. Match against the commits you expect. If they don't match, investigate before building.

Always wipe `/data/aethernet/aethernet.db` and `/data/aethernet/blobs` on testnet redeploy (state may be incompatible with new schemas). NEVER wipe `/data/aethernet/node_keys/` or `/data/aethernet/validator-manifest.json` — these are persistent identity.

After deploying, verify on ALL 5 nodes that the latest commit is running. A common failure mode is deploying to Node 1 only and leaving Nodes 2-5 on stale images.

### Generalization Over Special-Casing

When a bug affects one event type, ask: "Does this same problem affect other event types?" Almost always, the answer is yes. Fix the underlying pattern, not the specific instance.

The vote materialization stall (commit `c6defe8`) was correctly fixed for vote events. But the same bug affected TaskPosted, Transfer, and every other event type that referenced arbitrary DAG tips. Three more iterations were spent re-fixing the same bug for each event type before the generalized fix (`f234b6b` — semantic parents for all event types) was implemented. The general fix should have been the first fix.

Whenever Claude Code is about to implement a per-event-type fix, stop and ask: "Should this be a per-event-type fix or a general fix?"

## Project-Specific Knowledge

### Multi-AI Workflow

This project uses multiple AI agents in coordination:
- **Claude (R&D chat)**: Invention, theory, prompt generation, creative direction
- **Claude Code**: Implementation — full codebase access, writes and tests all code
- **ChatGPT**: Architecture design, prompt structuring for staged implementation
- **Grok**: Adversarial red-teaming, edge case discovery, multi-agent review

When given a task by Mike, Claude Code is the implementer. When investigating architectural questions, Claude Code may be asked to produce audit reports that other AIs review.

### Repository Layout

```
internal/
  dag/              # Causal DAG storage and reference resolution
  event/            # Event types and canonical serialization
  ocs/              # Ordering/Consensus Service (BFT consensus engine)
  consensus/        # VotingRound, supermajority calculation
  recognition/      # Causal Recognition Fabric (commit bus + consumers)
  network/          # Fast Path three-plane networking, repair, sync
  localpub/         # Local event publication path
  identity/         # Agent identity and validator-seat snapshots
  ledger/           # Token ledger (Transfer, Generation)
  settlement/       # Settlement applicator
  validator/        # Validator lifecycle (seats, key rotation, slashing)
  blobstore/        # Content-addressed evidence blob storage
  api/              # HTTP API server
  tasks/            # Task lifecycle manager
  autovalidator/    # Automatic verification and scoring
  trajectory/       # Trajectory commits and evidence anchoring
  marketplace/      # Task marketplace logic
  protocol/         # Protocol client (escrow, fees, settlements)
cmd/
  node/             # Node entrypoint and wiring
  aet/              # CLI tool
  marketplace/      # Marketplace standalone tool
sdk/
  python/           # Python SDK (PyPI: aethernet-sdk)
docs/
  lessons.md        # Architectural lessons learned (read at session start)
  plans/            # Approved implementation plans
```

### Testnet Infrastructure

5 nodes (3x m7i.xlarge + 2x m7i.large) on AWS:
- Node 1: 44.200.60.102 (private 172.31.12.70) — also the build node
- Node 2: 3.87.68.158 (private 172.31.93.186)
- Node 3: 100.27.227.231 (private 172.31.17.237)
- Node 4: 3.232.95.111 (private 172.31.4.3)
- Node 5: 32.195.67.127 (private 172.31.13.36)

ALB: testnet.aethernet.network
ECR: 435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet
SSH key: ~/.ssh/aethernet.pem

Build on EC2 (44.200.60.102) — never build from MacBook (slow upload). Push to ECR within AWS for near-instant uploads.

### Key Invariants That Must Remain True

- The DAG is append-only and content-addressed
- BFT supermajority is computed over total active validator stake (not received votes)
- Validator eligibility is checked against the validator-seat snapshot at round open time, not current state
- Vote events reference only their target as parent
- Task lifecycle events reference their predecessor in the chain (Posted → Claimed → Submitted → Approved/Disputed)
- Settlement events fire exactly once per finalized target
- All consumers in the recognition fabric are idempotent
- `localpub.Publisher.Publish` is the only sanctioned local event creation path
- `dag.Add` is the single convergence point for all event commits
- The Fast Path three-plane architecture (causality/body/repair) is preserved
- No `dag.Add` calls from any new path
- No network sends while holding `n.mu`
- No blob fetches under TaskManager lock
- No work performed under DAG write lock by recognition consumers

### Agent Worker Repository

The agent worker is a separate repo: `github.com/Aethernet-network/agent-worker`. It is a Python project using `aethernet-sdk` to claim, execute, and submit tasks. When working on the worker, `cd ~/agent-worker`, not `~/aethernet`.

### Skills Library (Superpowers)

This project uses the Superpowers plugin for Claude Code, which provides:
- `brainstorming` — extracts a real spec from conversation before writing code
- `systematic-debugging` — 4-phase root cause process
- `verification-before-completion` — ensures fixes actually work
- `subagent-driven-development` — fresh subagent per task with two-stage review
- `using-git-worktrees` — isolated workspaces for parallel work
- `test-driven-development` — RED-GREEN-REFACTOR enforcement
- `writing-plans` — detailed implementation plans
- `requesting-code-review` — pre-review checklist
- `finishing-a-development-branch` — merge/PR decision workflow

These are not optional. They are mandatory workflows for any non-trivial work. The skills trigger automatically. Do not bypass them.

## Communication Style

When reporting work to the user:
- Lead with the result, not the process
- State the commit hash, what changed, and what was verified
- If verification failed, say so directly and explain why
- Do not say "tests pass" as proof of correctness — say "verified working on testnet at X timestamp with Y outcome"
- Do not pad responses with restating the task or summarizing the user's request
- Do not use exclamation points or hype language
- If you made a mistake, own it directly and fix it

When the user pushes back on a decision:
- Take it seriously
- Don't capitulate just to appease
- Explain your reasoning
- If they're right, change course; if you're right, defend it with specific evidence

The user values: directness, technical depth, architectural correctness, and follow-through. The user does not value: hedging, disclaimers, performative effort, or "the integration tests pass" as a closing argument.

## Final Note

This project is being built to scale to billions of users and become the trust layer for the AI agent economy. The work being done here is foundational infrastructure that will outlast any single feature or sprint. Treat it accordingly.

When in doubt, the answer is: read the code, find the root cause, generalize the fix, verify on the live testnet, document the lesson.
