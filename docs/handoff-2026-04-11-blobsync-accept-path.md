# AetherNet Architect Handoff — 2026-04-11

## What This Document Is

This is the handoff from the architect session that ran on April 10-11, 2026. It covers the BlobSync + RoundProgress + RoundPolicy design and implementation, the first observed accept verdict in the protocol's history, and everything the next architect session needs to continue the work. Read this entire document before starting any work.

## Required Reading at Session Start

Read these files in this order before engaging with any task:

1. This handoff document (you're reading it)
2. `CLAUDE.md` — engineering standards, plan mode, deploy protocol
3. `docs/design-principles.md` — 15 standing meta-rules (principle 15 added this session)
4. `docs/lessons.md` — hard-won lessons, format: Context→Wrong→Right→Why
5. `docs/multi-validator-consensus-final-design.md` — locked multi-validator design (§4 updated this session)
6. `docs/blobsync-design.md` — locked BlobSync + RoundProgress + RoundPolicy design (§6.4 updated this session)

---

## Project Context

AetherNet is a production-grade Layer 1 (Go, BFT, causal DAG) serving as the trust and settlement layer for an AI agent economy. The thesis: structurally independent compound verification creates trust that grows over time.

**Founder**: Mike (sole founder, direct communicator, machine-speed standard only, no bandaids). When Mike says something is wrong, he's right. When he pushes back, take it seriously. He holds the standard higher than anyone and that's why the protocol works.

**Team**: Claude (R&D/architect), Claude Code (implementation with Superpowers plugin), ChatGPT (architectural review), Grok (adversarial red-team).

**Multi-AI workflow**: Claude drafts → ChatGPT reviews for architectural rigor → Grok red-teams for attack surfaces → Claude synthesizes → Mike approves → Claude Code implements → live testnet verifies.

**Repo**: github.com/Aethernet-network/aethernet

---

## What Was Built This Session

### BlobSync + RoundProgress + RoundPolicy (7 implementation prompts)

Cross-node blob replication, signed validator progress control plane, and progress-aware adaptive round finalization. This was the infrastructure required to unblock the accept path — before this session, evidence blobs only existed on the originating node, remote validators couldn't score, and all rounds expired as disputes.

**Commits in order**:

| Commit | Prompt | What |
|--------|--------|------|
| `6f7bb5e` | Docs | BlobSync locked design, design principles v2 (principle 15), two lessons |
| `0cb73d9` | 1 | BlobRef, BlobKind, ConsensusBlocking, BlobRefRegistry, TaskSubmitted extractor. 15 tests. |
| `802f227` | 2 | BlobSync engine, transport, HolderHintCache, BlobFetchPolicy, serving reputation, BlobStore Subscribe/WaitForBlob, startup wiring. 16 files, 1954 insertions. |
| `b6e2359` | 3 | BlobDemandConsumer on recognition fabric. Always-ready pattern. 3 files, 525 insertions. |
| `4fdcc47` | 4 | RoundProgress control plane. Types, lease, ETA clamping, BadgerDB snapshot store, aggregator, rate limiter, lease enforcer, emitter. MsgProgressUpdate wire type. 14 files, 41 tests. NO DAG event type — control plane only. |
| `4245c9b` | 5 | MultiVoter wiring. Goroutine-based subscribe-wait-analyze-vote for missing blobs. Progress emission at each phase. 5 files, 734 insertions. |
| `0fc7ade` | 6 | Progress-aware finalizer. RoundPolicy package with 5-step evaluator. DeadlineChecker dispatches to progress-aware path when wired. 5 files, 571 insertions. |
| `dcc7c17` | Fix | Acceptance threshold fix. Removed cross-family median score threshold (category error). Added DistinctParticipatingFamilies ≥ 3 gate. Strengthened OutcomeSecured to account for active progress leases. Updated design docs and lessons. |
| `032a286` | Verify | First observed accept verdict verification. Worker balance delta +73,000 µAET confirmed. Settlement 73/23/2/2 verified to the µAET. |

### First Observed Accept Verdict

**Date**: 2026-04-11T17:52:30Z
**Commit**: `dcc7c17`
**Task ID**: `52c5b97a555f8d83dbcee9751ea73d62`
**Content**: BFT vs Nakamoto technical explainer, real Claude API generation, 1994 words
**Time to finalize**: ~11 seconds
**Verdict**: pass (accept_supermajority)
**Settlement**: worker 73,000 + validators 23,000 + generation ledger 2,000 + treasury 2,000 = 100,000 µAET exact
**Worker balance delta**: +73,000 µAET verified

This is the first time AetherNet paid a worker for verified work. The compound verification thesis is empirically validated.

---

## Testnet Infrastructure

### Topology

5 EC2 instances running Docker directly (NOT ECS Fargate — there is a stale ECS cluster `aethernet-testnet` with 3 services scaled to 0; ignore it, do not deploy to it).

| Node | Public IP | Private IP | Instance Type |
|------|-----------|------------|---------------|
| 1 (build node) | 44.200.60.102 | 172.31.12.70 | m7i.large |
| 2 | 3.87.68.158 | 172.31.93.186 | m7i.xlarge |
| 3 | 100.27.227.231 | 172.31.17.237 | m7i.xlarge |
| 4 | 3.232.95.111 | 172.31.4.3 | m7i.large |
| 5 | 32.195.67.127 | 172.31.13.36 | m7i.xlarge |

SSH: `ssh -i ~/.ssh/aethernet.pem ubuntu@<ip>`
ALB: `testnet.aethernet.network`
ECR: `435998721364.dkr.ecr.us-east-1.amazonaws.com/aethernet`
Build node has IAM instance profile `aethernet-build-node-ecr-push` for ECR push.

### Deploy Pattern

The working deploy uses direct Docker on EC2 with static peer lists. The exact command (from journalctl, verified working):

```bash
sudo docker run -d --name aethernet --restart unless-stopped \
  -p 8337:8337 -p 8338:8338 \
  -v /data/aethernet:/data \
  -e AETHERNET_DATA=/data \
  -e AETHERNET_LISTEN=0.0.0.0:8337 \
  -e AETHERNET_API=0.0.0.0:8338 \
  -e AETHERNET_TESTNET=true \
  -e AETHERNET_CONSENSUS_MIN_PARTICIPANTS=2 \
  -e AETHERNET_VALIDATOR_MANIFEST=/data/validator-manifest.json \
  -e AETHERNET_ANALYZER_CONFIG=/data/validator-analyzers.json \
  -e AETHERNET_PEER=<comma-separated private IPs of other 4 nodes>:8337 \
  aethernet:latest start --marketplace --no-auth
```

Each node's `AETHERNET_PEER` contains the other 4 nodes' private IPs (not its own). Peer list per node:

- Node 1: `172.31.93.186:8337,172.31.17.237:8337,172.31.4.3:8337,172.31.13.36:8337`
- Node 2: `172.31.12.70:8337,172.31.17.237:8337,172.31.4.3:8337,172.31.13.36:8337`
- Node 3: `172.31.12.70:8337,172.31.93.186:8337,172.31.4.3:8337,172.31.13.36:8337`
- Node 4: `172.31.12.70:8337,172.31.93.186:8337,172.31.17.237:8337,172.31.13.36:8337`
- Node 5: `172.31.12.70:8337,172.31.93.186:8337,172.31.17.237:8337,172.31.4.3:8337`

### Persistent State (NEVER wipe)

- `/data/aethernet/node_keys/` — Ed25519 keypairs
- `/data/aethernet/validator-manifest.json` — 5-validator manifest
- `/data/aethernet/validator-analyzers.json` — per-node analyzer family assignments

### Wipeable State (wipe on clean redeploy)

- `/data/aethernet/aethernet.db` — BadgerDB (DAG state, balances, rounds)
- `/data/aethernet/blobs/` — blob storage

### Critical Lesson from This Session

During this session, Claude Code stopped the running testnet by mistaking the pre-existing containers for orphans during an investigative phase. The DAG state and blob storage from the multi-validator verification era (including the first observed reject verdict from 2026-04-08T15:55:13Z) were lost when the databases were wiped during redeploy. Node keys and validator manifests survived because the CLAUDE.md deploy protocol explicitly prohibits wiping them.

**Standing rule for all future sessions**: before stopping, modifying, or removing any Docker container on the testnet hosts, verify whether it is a legitimate running container from a prior deploy or an orphan from the current session. Check `docker inspect` for creation time and compare against session start time. When in doubt, do not stop it — ask first.

---

## v4.1 Economic Model (verified this session)

On **accept**: 73% worker / 23% validators (Q-weighted) / 2% generation ledger / 2% treasury
On **reject**: 73% poster / 23% validators / 4% treasury (gen ledger's 2% redirected)
On **dispute** (expired): 36.5% poster / 36.5% worker / 27% protocol/validators

All percentages are of the task escrow amount. Settlement is exact to the µAET — no rounding, no float64. Verified end-to-end on the first accept verdict with 100,000 µAET escrow: 73,000 worker + 23,000 validators + 2,000 generation ledger + 2,000 treasury = 100,000.

---

## Current Acceptance Rule (updated this session)

```
PassWeight ≥ BFT threshold (over full active validator set)
AND DistinctPassFamilies() ≥ DiversityFloor (default 2)
AND DistinctParticipatingFamilies() ≥ ParticipationFloor (default 3, adjusted for small networks)
```

The median score threshold was removed as a category error — see `docs/lessons.md` for the full lesson. Score values stay in the consensus payload as non-consensus observability metadata.

The "mathematically secured" early-finalization check accounts for active progress leases as worst-case opposing votes (Grok hardening 4).

---

## Key Architectural Invariants (must not violate)

- Events reference semantic parents, never arbitrary DAG tips
- `localpub.Publisher.Publish` is the only sanctioned local event creation path
- `dag.Add` is the single convergence point; no new `dag.Add` callers
- No network sends holding `n.mu`
- No blob fetches under TaskManager lock
- BFT supermajority computed over total active validator stake, not received votes
- All consumers idempotent
- Progress state is liveness input only; verdicts derive from durable votes only (authority boundary)
- Content addressing is the integrity model
- No `float64`, no `time.Now()` in canonical state
- Integer arithmetic only for economic calculations — exact to the µAET

---

## Pending Follow-Up Items (non-blocking)

1. **Task status state machine lag** — settlement applies and worker balance updates, but task status field stays `submitted` instead of transitioning to `completed`. State machine update lags the economic settlement. Needs fix before mainnet.

2. **Progress update rate limiter over-aggressive** — 10-second rate limit in the progress aggregator blocks most phase transitions from being visible on fast-finalizing rounds (the 11-second accept verdict had no visible progress updates beyond the first). Consider 1-second rate limit or allowing phase-transition updates to bypass the rate limit while only rate-limiting same-phase repeats.

3. **Q-score family-entropy term** — Grok's hardening 2 from the threshold review. Deferred to a future Q-score extension design pass. Submissions that only hit the two most common families for their category should get a Q-score penalty to make cartel coordination economically expensive.

4. **`llm_semantic` analyzer family not configured** — the current 5-node testnet runs three bootstrap families (deterministic_heuristic, statistical_structural, embedding_similarity) but `llm_semantic` does not appear to be configured on any node. Worth adding for diversity and for testing the semantic analysis path.

5. **BlobSync chunking** — deferred from Prompt 2. Current v1 uses single-message transfers, which is fine for 10-50KB evidence blobs. When data ingestion hits with multi-MB payloads, chunking needs to be added. The wire protocol types already have the foundation (BlobChunk, BlobComplete).

6. **HolderHint signature verification** — the Signature field on HolderHint is populated on emit and stored on receipt, but `ed25519.Verify` on receipt is skipped with a TODO. Turn on verification before mainnet.

7. **Reputation store update after accept verdict** — no reputation log entries were visible after the accept verdict. The reputation store update may be silent at INFO level or not firing. Needs investigation and confirmation that validator agreement rates are actually being updated.

8. **Generation ledger on zero-ancestor DAG** — the settler allocated 2,000 µAET to `gen_ledger` on the first task (no causal ancestors on a fresh DAG). The allocation was logged but may not have resolved to any actual ancestor payouts. Edge case for first-task-only; on a mature DAG with real causal chains, this would resolve correctly. Worth auditing.

---

## Named Workstreams (priority order)

### Workstream 1: Data Ingestion Engine (next priority)

Developer with 5.4M-node OSINT corruption graph is waiting. BlobSync unblocks ingestion at scale — cross-node blob replication lets large datasets be verified by validators without every validator needing the submitter's private API keys.

Key architectural pieces: claim-level verification granularity, manifest-declared policies, developer-registered analyzers, independence-weighted verification with contributor-validator affiliation tracking. The stale ChatGPT prompts from the previous architect session had good conceptual contributions on contributor-validator independence — those should be preserved and integrated.

### Workstream 2: Validator Onramp

Streamlined setup for new validators: installation, key generation, peer discovery, stake acquisition, analyzer family selection, monitoring. BlobSync being live now satisfies the dependency — new validators can actually verify submissions.

### Workstream 3: Grab Bag

Cycle detection in Generation Ledger. Full Q-score formula (CVD_norm, ChallengeSurvival, ReplicationRate — currently only α₄ is implemented, plus the family-entropy term from Grok hardening 2). Challenge path for hard slashing. Mainnet planning. Docs and website. Pharma R&D pilot.

### Workstream 4: Decentralized Persistence Layer

Mike's concern: APIs and databases sunset; the protocol needs storage nodes storing actual bytes (not just hashes) with protocol-paid incentives for long-term durability. Separate role from validators. Validators verify work; storage nodes preserve evidence. Different economics, different hardware profiles. Needs its own multi-AI design cycle.

### Workstream 5: Clearing-House Settlement — Rejected

Considered and rejected this session. The reasoning: in an AI agent economy where settlement velocity is at machine speed, reversal after provisional settlement is infeasible — agents can move AET through downstream economic activity (transfers, royalties, new task escrows) faster than the protocol can reverse. This makes the funds unrecoverable at machine speed, and therefore verify-before-settle is the architecturally correct model. The 11-second accept verdict demonstrated that verify-before-settle CAN run at machine speed without shortcuts, reinforcing the rejection.

### Workstream 6: Machine-Speed Verification Refinement (scope reduced)

Originally conceived as tiered analyzer families with provisional/confirming verdicts, reputation-weighted early finalization, and structural pre-verification at submission. The 11-second accept verdict result partially obsoleted the urgency — current architecture already achieves machine speed for fast-analyzer-only rounds. Remaining scope: verify and accommodate slow analyzer families (llm_semantic, external-API-dependent) when they're in the participating set. May fold into the data ingestion workstream rather than standing alone.

---

## Communication Patterns with Mike

Direct, no hedging. Push back when wrong. Defend when right. Machine speed only. No bandaids. Generalize fixes. Production-grade as if handling 1M events/hour. Tests necessary but live testnet is the real bar. Multi-AI workflow for design decisions. Hold the engineering standard — every compromise creates technical debt that compounds.

---

## Verification Discipline (standing instruction)

Mid-session, the architect made two specific errors that damaged trust: (1) claimed a message was not received when it had been; (2) stated "treasury gets 4%" on the accept path when the actual v4.1 split is 73/23/2/2 (treasury gets 2% on accept, 4% only on reject where generation ledger redirects).

**Standing rules for all future architect sessions**:

1. Verify against the documents before stating any specific number, constant, fee split, threshold, commit hash, or referenced fact. Cite the document and section when quoting load-bearing details.
2. When uncertain, say "I'm uncertain — let me verify" explicitly instead of asserting from memory.
3. Do not pattern-match from working memory on load-bearing details at delicate architectural moments. The cost of verifying is seconds; the cost of being confidently wrong is trust.
4. When Mike pushes back or corrects an error, take it seriously — his corrections have been right every time this session.

---

## Design Documents in Repo (binding)

| Document | Status |
|----------|--------|
| `CLAUDE.md` | Binding. Engineering standards, plan mode, deploy protocol. Updated this session (required reading line). |
| `docs/design-principles.md` | Binding. 15 standing meta-rules. Created this session with principle 15 added after review. |
| `docs/lessons.md` | Binding. Hard-won lessons. Multiple entries added this session. |
| `docs/multi-validator-consensus-final-design.md` | Binding. §4 updated this session (median threshold removed, participation floor added). |
| `docs/blobsync-design.md` | Binding. Created and locked this session. §6.4 updated (threshold fix). |

Any change to a locked design document requires: pause, update the document, get multi-AI review if the change is architectural, get Mike's approval, then continue. Do not modify locked designs without this process.
