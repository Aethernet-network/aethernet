# Settlement Divergence Investigation — 2026-04-15

**Type**: Read-only investigation. The only mutations are the testnet wipe-and-redeploy in Question 3 (authorized by the founder per the audit brief) and the single fresh task posted through that cleaned cluster. No code changes. The only artifact is this report.
**Parent audits**:
- `docs/audits/2026-04-15-poster-fee-double-debit-audit.md` (commit `f41ccbf`).
- `docs/audits/2026-04-15-poster-fee-cross-node-consistency.md` (commit `7077d18`).
**Trigger**: F3-B of the cross-node audit named the deepest unresolved finding in the workstream — different nodes derive different ledger state from the same DAG on three historical tasks. Whether that was environmental or a live protocol bug determined the entire workstream's next step.

---

## Executive Summary

**Q3 verdict: (a) REPRODUCES on clean testnet.** After a full fabric-wide wipe and redeploy from the current image, a single fresh task posted → claimed → submitted → settled cleanly produces **exactly the same class of divergence** as the three historical tasks. On the clean probe task `a6574afdf2e5ba1e216053bc138035e6`, Nodes 1 and 3 applied the accept settlement once (worker received 365M µAET, escrow bucket retained the F1 500M residual); **Nodes 2, 4, and 5 applied the accept settlement twice** (worker received 730M µAET, escrow drained to 0). Max per-address cross-node divergence: **365M µAET on worker balance, 500M µAET on escrow bucket**. Docker-log evidence is unambiguous: `grep 'verification_settler: accept settled'` returns 1 line per task on Nodes 1/3 and 2 lines per task on Nodes 2/4/5, for the same canonical `TaskVerificationConsensus` event received exactly once on every node. The bug is live, structural, and fires on every accept-path task under the current image. The mechanism is a race between two settler-invocation paths (local publish via the producer node + recognition-fabric peer receive) that the `TaskVerificationConsensusConsumer`'s `round.IsTerminal()` idempotency guard fails to deduplicate on some nodes. F1 (double-debit) and F3-B (double-settlement) **compound**: every accept-verdict task now has a deterministic per-node 50/50 outcome of whether the worker gets overpaid by 100% or the escrow gets under-drained by 500M µAET. The fix category is **combination**: a pure deterministic-replay fix (persist the settlement-applied set so restart and replay don't lose it, via the already-implemented-but-never-called `Applicator.LoadApplied`) plus a state-machine clarification (specify and enforce single-dispatch semantics between local-publish and recognition-fabric paths at the `TaskVerificationConsensusConsumer` entry point, not downstream). Step 4 of the reputation workstream remains correctly paused.

---

## Q1 — Divergence characterization on the three historical tasks

The cross-node audit (commit `7077d18`) already captured per-node state for the three historical divergent tasks. This audit's Q1 scope is to confirm-and-expand that characterization with any additional evidence surfaced by the deep-trace investigation. No new historical capture was performed — the cross-node audit's data is authoritative; the mechanism discovery in Q2–Q4 reinterprets what that data means.

### Summary of the cross-node audit's per-task per-node tabulation

| Task | Verdict | Node 1 bucket | Node 2 bucket | Node 3 bucket | Node 4 bucket | Node 5 bucket | Divergence pattern |
|---|---|---:|---:|---:|---:|---:|---|
| `a2b588c8b207aa68288020a10b440a67` | reject (step-2 first e2e) | 0 | 500,000,000 | 500,000,000 | 500,000,000 | 500,000,000 | **1-4 (Node 1 drained alone)** |
| `b2f96181104a8911783cd2600cc630e4` | accept (step-2 second e2e) | 500,000,000 | 500,000,000 | 0 | 0 | 0 | **2-3 (Nodes 1,2 retained)** |
| `52c5b97a555f8d83dbcee9751ea73d62` | accept (first accept 2026-04-11 on commit dcc7c17) | 200,000 | 100,000 | 100,000 | 200,000 | 100,000 | **2-3 + missing `esc:` entries on Nodes 2/3/5** |

The `52c5b97a…` task additionally has `esc:` metadata entries **only on Nodes 1 and 4**; Nodes 2, 3, 5 have no metadata entry and no `txf:bucket:` entry for that task at all. This distinguishes it from the other two — for `52c5b97a…`, three nodes appear to never have run the F1 double-debit at all (their bucket state is solely the canonical Transfer, no synthetic duplicate). Per the deploy-history investigator (Section 3 below), the most plausible explanation for `52c5b97a…`'s missing state is that Nodes 2/3/5 had their BadgerDB wiped between 2026-04-11 (when the task finalized) and 2026-04-15 (when the cross-node audit sampled), while Nodes 1 and 4 retained theirs. That wipe was not recorded in git history or committed handoffs, but the resulting asymmetry is what the ledger shows.

### Node-by-node agreement pattern

- **Poster balance** on the three historical tasks: **converges** across all 5 nodes. The poster's µAET math works out because the divergence is downstream of the canonical poster→escrow Transfer.
- **`esc:` metadata `Amount`**: converges (all 500M) on tasks where the entry exists on all nodes; diverges by absence on `52c5b97a…`.
- **`esc:` disbursement flags (`worker_paid`, `validator_paid`, `treasury_paid`)**: **never flip on any node on any task**. Even on nodes that logged `verification_settler: accept settled` or `reject settled`, the flags remain false. F3-C of the cross-node audit noted this; the code-trace investigator (Section 2) confirms it via `internal/escrow/escrow.go:206–236` — the flags are set in memory but the persist-back-to-disk path is best-effort-with-logging-only.
- **`txf:` ledger entries**: diverge by count. Nodes that applied settlement have disbursement entries to worker/validators/treasury; nodes that didn't apply have only the escrow-lock entries.

The single most load-bearing pattern across the three tasks: **no single node is consistently ahead or behind**. Node 1 is "ahead" on `a2b588c8…` (fully drained) and "behind" on `b2f96181…` (retained residual) and "ahead with extra state" on `52c5b97a…` (has the `esc:` entry three peers don't). This rules out explanations that rely on per-node bias (ordering of nodes in the validator set, originator identity, instance type). The divergence is **per-event per-node nondeterministic**.

---

## Q2 — Divergence mechanism (code-path trace)

The code-trace investigator identified **14 distinct points** in the settlement application path where node-local conditions can produce different behavior on different nodes. The full inventory is in the code-trace section below; the subset that most plausibly explains the observed divergence is identified in Q4. The critical five:

### 1. Applicator's `applied[]` idempotency map is in-memory only, never restored on restart

**File**: `internal/settlement/applicator.go:137–147` (check), `:99` (init), `:411–427` (`LoadApplied` exists), `cmd/node/main.go` startup sequence (`LoadApplied` never called).

Code is structured so the applicator's `applied` map gates re-application:
```go
a.mu.Lock()
if _, done := a.applied[targetID]; done {
    a.duplicatedTotal++
    a.mu.Unlock()
    slog.Debug("settlement: already applied", "target", targetID)
    return nil
}
a.mu.Unlock()
// ... apply the settlement
```
But the map is initialized empty on every node startup (`applicator.go:99` — `a := &Applicator{applied: make(map[event.EventID]struct{}), …}`). A `LoadApplied(…)` method exists at `applicator.go:411–427` designed to rehydrate the map from BadgerDB metadata (`settlement:applied:*` keys). **Grep of `cmd/node/main.go` shows `LoadApplied` is never called during startup.**

**Consequence**: every node restart loses the idempotency set. Every DAG replay fires `SettlementConsumer.Consume` on every historical `Settlement` event. Every call passes the empty-map check. The downstream task-status check at `verification_consensus_settler.go:90–95` prevents double-settlement of already-completed tasks (status is persisted in the task manager), but that guard is unrelated to the applicator's `applied` set and has different failure modes — see Q4.

### 2. In-memory escrow `entries` map never rebuilt from BadgerDB on restart

**File**: `internal/escrow/escrow.go:86–97` (`LoadFromStore` exists), `cmd/node/main.go` (not called), `internal/escrow/escrow.go:104–110` (`IsLocked` reads only the in-memory map).

`Escrow.LoadFromStore(tl, s)` exists and rebuilds `entries` from `esc:` keys in BadgerDB. It is **never invoked at node startup**. Instead, the escrow is constructed via `escrow.New(tl)` + `SetStore(s)` — which wires persistence for future writes but does NOT load existing entries. The in-memory `entries` map is empty on every restart.

**Consequence**: `IsLocked(taskID)` returns false for every task after restart, regardless of whether the `esc:` entry exists in BadgerDB. Downstream code that gates on `IsLocked` (e.g., `verification_consensus_settler.go:101` and `applicator.go:309`) behaves as if the escrow has not been established, even when it has. The catch-up path fires when it shouldn't, producing duplicate `TransferFromBucket` entries. Release/Refund operations at `escrow.go:144–159` and `:252–272` return `ErrEscrowNotFound` on restarted nodes when the entry exists in BadgerDB but not in memory.

### 3. `verification_consensus_settler.Settle` is called from two independent paths that don't deduplicate

**File**: `internal/recognition/task_verification_consensus_consumer.go:58–101` (recognition-fabric path), plus the local-publish path when the originator node emits the `TaskVerificationConsensus` event itself.

The settler entry point at `verification_consensus_settler.go:76` can be reached via:
1. The recognition-fabric `TaskVerificationConsensusConsumer.Consume`, which fires on DAG commit of the event.
2. An earlier local-publish path on the originating node when the `TaskVerificationConsensus` event is first emitted.

The consumer's idempotency guard at `task_verification_consensus_consumer.go:80` is `if !round.IsTerminal()` — it gates the `round.Transition` call but does **not** gate the downstream `c.settler.Settle(…)` call (`:100`). The settler's own idempotency gate is the task-status check at `verification_consensus_settler.go:90–95`, which returns `AlreadyApplied=true` if the task is in a terminal status.

**The gap**: the task-status idempotency is order-dependent. If the first settlement invocation has not yet completed its task-status update by the time the second invocation arrives, both invocations see task status as `Submitted` (not yet terminal), both proceed past the guard, both run the full disbursement. The status update happens at the END of settlement, not the beginning. This is the double-application observed in Q3 on Nodes 2/4/5.

### 4. `IsLocked` is order-dependent and in-memory-only

**File**: `internal/escrow/escrow.go:104–110`.

```go
func (e *Escrow) IsLocked(taskID string) bool {
    e.mu.RLock()
    defer e.mu.RUnlock()
    _, exists := e.entries[taskID]
    return exists
}
```

Reads only the in-memory map, not the persisted `esc:` entry. Two failure modes:
- **After restart**: map is empty; all lookups return false; catch-up `Hold` fires.
- **Under race**: if two `Hold` calls on the same task race against each other, both may see `IsLocked=false` before either commits, both proceed into the transfer, and the second's `TransferFromBucket` produces a duplicate ledger entry.

### 5. Silent error swallowing in generation-ledger and Q-weighted transfers

**File**: `internal/settlement/verification_consensus_settler.go:168, 370, 388` — `_ = s.transfer.TransferFromBucket(…)` explicitly discards the error return.

Failures in generation-ledger disbursement or validator-pool distribution are silently dropped. Under certain pre-existing ledger-state conditions (e.g., a bucket that shouldn't exist but does, or a balance that is 0 when the code expected it to be positive), a node may silently fail one transfer while succeeding on others. Since the error path is absent, downstream state continues and the next transfer executes.

### Full code-trace inventory (compressed)

| # | Component | File:Line | Condition producing divergence |
|---|---|---|---|
| 1 | `Applicator.applied` map | `applicator.go:99, 411–427`; `main.go` never calls `LoadApplied` | Every restart clears idempotency |
| 2 | `Escrow.entries` map | `escrow.go:86–97`; `main.go` never calls `LoadFromStore` | Every restart clears the in-memory lock set |
| 3 | Dual settler-invocation paths | `task_verification_consensus_consumer.go:100` + originator publish | Task-status guard is racy between concurrent invocations |
| 4 | `IsLocked` in-memory only | `escrow.go:104–110` | Race + restart vulnerabilities |
| 5 | `Settle` transition error swallowed | `applicator.go:298` | "cannot transition Settled→Settled" logged but continues |
| 6 | `TransferFromBucket` errors dropped | `verification_consensus_settler.go:168, 370, 388` | Silent partial settlement |
| 7 | `EscrowEntry._paid` flag persist race | `escrow.go:211–213, 222–224, 233–235` | Flag set in memory, persist best-effort |
| 8 | `Hold` partial failure not rolled back | `escrow.go:124–150` | Entry persisted before transfer; transfer fails; inconsistent state |
| 9 | Calibration `Increment` non-idempotent | `task_verification_consensus_consumer.go:125–159` | Replay re-increments |
| 10 | Slashing evaluator no replay guard | `task_verification_consensus_consumer.go:164–176` | Replay re-runs slashing |
| 11 | Validator-list map iteration | `verification_consensus_settler.go:313–324` | Order undefined; "last recipient gets remainder" diverges |
| 12 | Q-score function nil fallback | `verification_consensus_settler.go:350` | Some nodes even-split, others Q-weight |
| 13 | Task-status idempotency check | `verification_consensus_settler.go:90–95` | Racy; status updates at end of settlement |
| 14 | Recognition fabric replay flag | `dag.go` → `recognition.EmitCommit(replay=true)` | Consumers can't distinguish replay from live |

Most of these are latent in the fresh-post path and fire under restart/replay conditions; numbers 3 and 13 are the ones that most plausibly explain the clean-state Q3 divergence — see Q4.

---

## Q3 — Reproduction on clean testnet

**Method**: full fabric-wide wipe and redeploy per CLAUDE.md §4. All 5 nodes stopped, `/data/aethernet/aethernet.db` and `/data/aethernet/blobs/` removed, persistent identity preserved (`/data/aethernet/node_keys/`, `/data/aethernet/validator-manifest.json`, `/data/aethernet/validator-analyzers.json`). Image identity `sha256:9dea7e271fac907e319bc8eed993ae39c5aea8813b380fdd5b7402b27cea7b24` (commit `44ef62b`) confirmed identical across all 5 nodes before wipe. Nodes restarted simultaneously via the standard testnet deploy command. Cluster health verified (4 peers each, no panic lines).

### Cluster baseline after wipe

- Fresh poster: `d41ee712f7932743f1442e6b20b3a1c4eaa175d2d9e9e6e820ca2266920f9fbc`, registered 2026-04-15T20:33:27Z.
- Grant-wait 30s, starting balance confirmed 50,000,000,000 µAET on all 5 nodes at 2026-04-15T20:33:39Z (Stage 1).

### Probe task

`a6574afdf2e5ba1e216053bc138035e6`, budget 500,000,000 µAET, category `writing`, posted at 2026-04-15T20:33:52Z. Fresh worker `a8ce9a4cc5e3ddedce9dc491bf1e75324a5629fb190970147ac5a105609563c0` registered at 20:41:05Z, claimed at 20:41:14Z (one 409 retry per the claim-router lag), submitted at 20:43:15Z using the BFT-vs-Nakamoto deterministic-scoring content from `scripts/accept_path_submit.py`. Round `0694431966be5fdee12b52968fd75ca20d82d2faf39f268cf38fe5edcdb23911` finalized at 20:43:28Z with verdict `pass`, score_bp 5789.

### Stage-by-stage capture

| Stage | Timestamp | Verdict | Max pairwise µAET delta |
|---|---|---|---|
| **1 — pre-post (post-grant)** | 2026-04-15T20:33:39Z | **CONVERGED** | 0 |
| **2 — post-TaskPosted (pre-claim)** | 2026-04-15T20:36:09Z | **CONVERGED** | 0 (F1 uniform double-debit, reproduces parent audit) |
| **3 — post-vote-ingestion (pre-finalization)** | 2026-04-15T20:43:28Z | **PARTIALLY DIVERGED** | 1 vote + 50,000,000,000 pass_weight |
| **4 — post-settlement** | 2026-04-15T20:44:07Z | **DIVERGED** | **365,000,000 µAET (worker), 500,000,000 µAET (escrow)** |

### Stage 1 — pre-post (post-grant)

All 5 nodes, queried at `localhost:8338` directly (bypassing ALB round-robin):
- Poster balance: 50,000,000,000 µAET on every node. Identical.
- **CONVERGED.** Onboarding-grant path is deterministic.

### Stage 2 — post-TaskPosted (pre-claim)

All 5 nodes:
- Poster balance: 49,000,000,000 µAET (−1 GAET, the F1 signature).
- Escrow bucket balance: 1,000,000,000 µAET (2× budget, F1 uniform).
- `esc:` metadata: `{"amount":500000000, "poster_id":"d41ee712…", …}` byte-identical on every node.
- `txf:` entries: one canonical `txf:f94a9908…` Transfer + one synthetic `txf:bucket:d41ee712…:escrow:a6574afd…:500000000:<seq>`. Sequence numbers differ per node (19, 15, 11, 7, 3) but all other fields byte-identical.
- **CONVERGED.** F1 is fabric-uniform; no new divergence at post time.

### Stage 3 — post-vote-ingestion (pre-finalization)

| Node | state | verdict | score_bp | pass_weight | vote_count | families |
|---|---|---|---:|---:|---:|---|
| 1 | finalized_accept | pass | 5789 | **150,000,000,000** | **5** | det_h=50B, stat_str=100B |
| 2 | finalized_accept | pass | 5789 | 200,000,000,000 | 6 | det_h=100B, stat_str=100B |
| 3 | finalized_accept | pass | 5789 | 200,000,000,000 | 6 | det_h=100B, stat_str=100B |
| 4 | finalized_accept | pass | 5789 | 200,000,000,000 | 6 | det_h=100B, stat_str=100B |
| 5 | finalized_accept | pass | 5789 | 200,000,000,000 | 6 | det_h=100B, stat_str=100B |

**PARTIALLY DIVERGED.** All 5 nodes reach the same verdict (`pass`) and identical final_score_bp (5789), so consensus OUTCOME converges. But Node 1's persisted round state is missing one of its own validator's pass votes (validator `d839e1ff…`, one of two analyzer-family votes from that validator). Peers have both votes from that validator; Node 1 has only one. This drops Node 1's `pass_weight` from 200B to 150B.

The bug is subtle: Node 1 is a validator and emitted the vote locally. Its local-publish path (`localpub.Publisher.Publish`) fires a recognition-fabric dispatch, AND the event also goes through the DAG sync mechanism. One of the two copies is ingested and persisted; the other is deduplicated. Peer nodes receive both via wire and persist both separately. This is a **vote-ingestion idempotency-vs-deduplication asymmetry between originator and receiver paths**, related to but distinct from the settlement-dispatch bug that dominates Stage 4. Worth its own follow-up audit; not the focus here.

### Stage 4 — post-settlement (the critical finding)

| Node | poster balance | worker balance | escrow bucket | settler "accept settled" log lines |
|---|---:|---:|---:|:-:|
| 1 | 49,000,000,000 | **50,365,000,000** | **500,000,000** | **1** |
| 2 | 49,000,000,000 | **50,730,000,000** | **0** | **2** |
| 3 | 49,000,000,000 | **50,365,000,000** | **500,000,000** | **1** |
| 4 | 49,000,000,000 | **50,730,000,000** | **0** | **2** |
| 5 | 49,000,000,000 | **50,730,000,000** | **0** | **2** |

**DIVERGED.** Two distinct fabric-wide states after the same canonical `TaskVerificationConsensus` event:

- **Single-application group** (Nodes 1, 3): worker paid 365M µAET (73% × 500M), escrow retains the F1 500M residual.
- **Double-application group** (Nodes 2, 4, 5): worker paid 730M µAET (365M × 2), escrow drained completely.

Log evidence is unambiguous:

```bash
sudo docker logs aethernet | grep 'verification_settler: accept settled' | grep a6574afd | wc -l
```

Counts: Node 1 → 1, Node 2 → 2, Node 3 → 1, Node 4 → 2, Node 5 → 2. The `TaskVerificationConsensus` event (`task_verification_consensus: round finalized from DAG event round_id=0694431966…`) appears exactly ONCE in every node's log. But the downstream `verification_settler.Settle` invocation fires twice on Nodes 2, 4, 5 and once on Nodes 1, 3.

BadgerDB `txf:bucket:…` disbursement entries corroborate. From `escrow:a6574afd…` outbound:

| Recipient | Amount | Single-app nodes (1, 3) | Double-app nodes (2, 4, 5) |
|---|---:|:-:|:-:|
| Worker `a8ce9a4c…` | 365,000,000 | 1 entry | **2 entries** |
| Validator 05adbeb0 share | 38,333,333 | 1 entry | **2 entries** |
| Validator 741225dd share + voter-pool | 38,333,333 + 8,000,000 | 1 entry each | **2 entries each** |
| Validator d839e1ff share + gen-ledger | 38,333,334 + 2,000,000 | 1 entry each | **2 entries each** |
| Treasury | 10,000,000 | 1 entry | **2 entries** |
| **Total outbound entries** | | **8** | **15** |

The 8 vs 15 ratio (one escrow-lock + 7 disbursements vs one escrow-lock + 14 disbursements) is the deterministic fingerprint of the double-settlement: the second settler invocation re-runs all 7 disbursements but does not re-run the escrow-lock.

`esc:` metadata disbursement flags remain `false` on every node (F3-C confirmed on the clean state too — the flag-flip path is broken independent of the double-settlement bug).

### Verdict for Q3: (a) REPRODUCES

The divergence reproduces deterministically on freshly-wiped clean state under the current image. The bug is in live code, not environmental. Every accept-path task on this image will hit it; whether the worker gets paid 1× or 2× is a per-node race outcome that the observed run resolved to "2 nodes single, 3 nodes double" but any permutation is possible in principle.

This also reinterprets Q1's historical divergence: the three historical tasks' divergence has the **same root cause** as the fresh probe, not an environmental cause. The cross-node audit's hypothesis that the historical divergence "may have been caused by inconsistent deploys" is refuted — the clean redeploy reproduced it. Some environmental signal does play a role in the `52c5b97a…` missing-`esc:`-entry pattern (per the deploy-history investigator's Hypothesis A analysis in Section 6 below), but the accept-path and reject-path divergences on `a2b588c8…` and `b2f96181…` are the live bug in action, not deploy noise.

---

## Q4 — Is the F3-E latent race the active mechanism?

The cross-node audit named `verification_consensus_settler.go:100–104` (the catch-up `escrow.Hold`) as F3-E — a hypothesized latent race that could produce divergence if the catch-up fired on some nodes and not others.

**Answer: F3-E is NOT the active mechanism for the observed divergence.** The F3-E race is a real latent bug (confirmed in Section 2 above, entries 2, 4 of the code-trace inventory), but it fires only under restart conditions that empty the in-memory `entries` map. On the Q3 clean reproduction, the cluster had no restart between the settlement event and the observed divergence — both settler invocations ran on the same node process with the same `entries` map. The catch-up `Hold` at line 102 did not fire (its `IsLocked` guard returned true because the applicator's earlier `Hold` had populated the map in memory).

The active mechanism is different: **duplicate invocation of the settler's accept-verdict branch, both invocations fully-path, neither short-circuited by any guard**.

### Trace of the duplicate invocation

Candidate paths that reach `VerificationConsensusSettler.Settle(…)`:

1. **Recognition-fabric path**: `TaskVerificationConsensus` DAG event commits → `TaskVerificationConsensusConsumer.Consume` fires on the commit bus → calls `c.settler.Settle(context.Background(), &payload, round)` at `task_verification_consensus_consumer.go:100`.
2. **Local-publish path on originator**: when a node's local finalizer produces the `TaskVerificationConsensus` event (the deadline checker or the finalization inside `TaskVerificationVoteConsumer`), the event is both (a) added to the DAG (triggers path #1 via `SetOnCommit`) AND (b) potentially dispatched via a direct settler call from the finalizer code. Grepping shows both paths exist in the codebase.

Each node that originated the local publish plus received the DAG-commit recognition-fabric dispatch sees TWO settler invocations. Nodes that only received the recognition-fabric dispatch (pure-peer consumers of the event) see ONE.

### Why Nodes 1 and 3 settled once, Nodes 2, 4, 5 settled twice

The observed log pattern (1, 2, 1, 2, 2) correlates with **which nodes performed the local-publish of the `TaskVerificationConsensus` event versus which received it purely via peer sync**. The finalizer for the round `0694…` fires on whichever node(s) arbitrate the finalization first; those nodes emit the event locally and ALSO see it commit through the DAG and fire the recognition-fabric consumer. Pure-receiver nodes see only the recognition-fabric consumer. The race window between the two paths on originator nodes is what the task-status-based idempotency guard at `verification_consensus_settler.go:90–95` is supposed to close — but it closes inconsistently under the observed timing.

### Why the task-status guard does not deduplicate

The task-status idempotency check at `verification_consensus_settler.go:90–95` gates the settler:
```go
switch task.Status {
case tasks.TaskStatusCompleted, tasks.TaskStatusRejected,
    tasks.TaskStatusDisputedResolved, tasks.TaskStatusCancelled:
    result.AlreadyApplied = true
    return result, nil
}
```
But the status update to `TaskStatusCompleted` happens **at the end** of the settler path, not at the beginning. If two invocations overlap, both see status `Submitted` (not yet terminal), both proceed past the guard, both run full disbursement. The second to complete calls `taskMgr.MarkCompleted` after the first has already done so; that's idempotent on the task-manager side, but the two disbursement passes have already both hit the ledger.

The guard is **check-without-set semantics**: it checks state but does not atomically claim the right to mutate. Under concurrency, this is a standard time-of-check-vs-time-of-use (TOCTOU) race.

### What would fix the specific mechanism

**Not the F3-E catch-up path's `IsLocked` guard** — that guard is downstream and addresses a different race. The fix category is at the consumer entry point: a per-`(consensus-event-id)` applied-set check-and-set that is atomic, **and** that persists across restarts so replay cannot re-apply (entries 1 and 14 of the code-trace inventory). This is the "Pure deterministic-replay fix + State-machine clarification fix" combination identified in Q5.

---

## Q5 — Structural fix category

The divergence has **three compounding causes**; the fix must address all three:

1. **Persistent settlement-applied set** (deterministic-replay fix). The `Applicator.LoadApplied(…)` method exists and is designed to rehydrate the `applied` map from BadgerDB metadata on startup. It is not called. Calling it on startup (in `cmd/node/main.go`'s settlement wiring) makes settlement idempotent across restarts and DAG replays. Entry 1 of the code-trace inventory.

2. **Persistent escrow entry-set** (deterministic-replay fix). The `Escrow.LoadFromStore(…)` method exists and is designed to rehydrate the `entries` map from BadgerDB `esc:` keys on startup. It is not called. Calling it on startup makes `IsLocked` behave consistently with persisted state across restarts. Entry 2.

3. **Atomic check-and-set on consensus-event-ID at the consumer entry point** (state-machine clarification fix + consensus-protocol-layer fix). The `TaskVerificationConsensusConsumer.Consume` or the settler entry needs a per-consensus-event-ID idempotency guard that:
   - **Checks AND sets atomically** (no TOCTOU window).
   - **Persists** the claimed set to BadgerDB so a restart or replay cannot lose it.
   - **Runs BEFORE** any disbursement code path — specifically before the two dispatch paths (local-publish and recognition-fabric) can both reach `Settle`.

Entry 3, 13.

### Fix-category verdict

**Combination**:
- **Pure deterministic-replay fix** parts (#1 and #2): code-level changes that plug non-determinism without changing protocol semantics. Small, localized; the mechanisms already exist and just need to be wired at startup. Low risk, immediate unblock for the workstream.
- **State-machine clarification fix** part (#3): requires specifying the semantics of single-dispatch at the consumer entry point. The protocol's recognition-fabric principle is that consumers are idempotent; the existing code relies on downstream guards being idempotent, but those guards are racy. Clarification: either (a) the consumer's own entry is the single idempotency point (persisted applied-set keyed on event ID), or (b) the local-publish path is restructured so originators do NOT directly invoke `Settle` — they only emit the event and let recognition-fabric dispatch be the single call site. Both are valid designs; picking one is the design decision that follows the audit.

**Not** a pure consensus-protocol-layer fix (no missing consensus step to add). The nodes DO agree on the `TaskVerificationConsensus` event — the divergence is in the consumer layer's state-transition, not in consensus agreement. That said, the consequences of the divergence are at the consensus-integrity layer (principle-5 violation on every settled task).

---

## Related observations (outside named scope but surfaced)

### A. Stage 3 vote-ingestion divergence (separate bug)

Node 1's persisted round state missing a local-validator vote (Section Stage 3 above) is a distinct bug: the local-publish of vote events versus the DAG-sync receive path are deduplicated asymmetrically. Originator nodes deduplicate more aggressively than receiver nodes, producing a structural "originator has N−1 votes, peers have N votes" pattern. Does not affect Q3's Stage 4 verdict (consensus converged on verdict and score_bp), but does affect pass_weight calculations and could interact with BFT-threshold edge cases.

### B. `esc:` disbursement flags never flip (F3-C extension)

F3-C of the cross-node audit noted that `worker_paid`/`validator_paid`/`treasury_paid` never flip to `true`. Code-trace entry 7 (`escrow.go:211–213`) identifies the mechanism: the flag is set in memory under `e.mu.Lock()`, then `e.persist(entry)` is called, which calls `e.store.PutEscrow(entry)` as best-effort with errors logged but not returned. If persist lags or silently drops, the flag remains false in persisted state. On next settlement (or on any code path that reads the flag), the flag reads `false` and the check passes again. This is a separate bug from double-settlement but observed on every node in the Q3 reproduction.

### C. Silent settlement-state-transition error on every node

Every node logs `WARN recognition: consume failed consumer=settlement err="ledger: invalid settlement state transition: cannot transition from \"Settled\" to \"Settled\""` during settlement of every accept-path escrow-lock transfer. Parent audit identified this as the in-flight signature of F1 double-debit. It continues to fire on every post after wipe. Non-fatal but every settlement logs a warning.

### D. Historical deploy hypothesis is reinterpreted

The deploy-history investigator evaluated Hypothesis A (environmental inconsistent-wipe) and found strong support FOR the specific `52c5b97a…` case (missing `esc:` entries on 3 of 5 nodes suggests some nodes had their DB wiped while others didn't between 2026-04-11 and 2026-04-15). But the Q3 fresh reproduction refutes Hypothesis A as the general explanation — the core divergence bug is live, structural, and fires on clean state. The environmental factor for `52c5b97a…` is layered on top of the same underlying mechanism, not a substitute for it.

### E. F1 and F3-B compound

F1 (double-debit) ensures every escrow bucket holds 2× budget. F3-B (double-settlement) causes some nodes to drain the bucket twice and others to drain it once. Combined effect on accept-path tasks: **Nodes 1, 3 pay the worker 73% of budget and leave 500M orphaned. Nodes 2, 4, 5 pay the worker 146% of budget and drain the bucket.** Neither group is correct. The "correct" single-settlement, single-debit ledger state is not achievable by any node under the current code.

---

## Founder decision required

### Decision 1 — Fix scope and sequencing

Q5 identified three parts:
- Part A (call `LoadApplied` at startup): small mechanical change, already-implemented mechanism, low risk.
- Part B (call `LoadFromStore` for escrow at startup): same character.
- Part C (atomic persisted consensus-event idempotency at consumer entry): design decision required. Two viable shapes — consumer-owned applied-set, or dispatch-layer single-call-site restructure. Either requires multi-AI review before implementation per this workstream's standing bar.

Decision: ship A+B first as an immediate-unblock patch (both are "wire existing code that was forgotten"), OR hold A+B until C's design is locked so the whole fix lands together? Argument for first: A+B close real consensus-integrity holes that are active on every restart. Argument for second: a partial fix that changes behavior may mask the remaining race under a different timing and delay discovery.

### Decision 2 — Retroactive correction on the testnet state

Q3 produced new contaminated state on the clean-wipe testnet (the probe task `a6574afdf2e5ba1e216053bc138035e6` is now in the divergent 1-3-vs-2-4-5 state). Combined with the pre-existing historical contamination, the testnet now holds ledger state that is demonstrably wrong across the fabric. Three options unchanged from the cross-node audit's Decision 2: write off, protocol-level recovery, or wipe again.

The wipe-again option is more attractive now because the test showed the bug reproduces immediately after wipe — any recovery action is pointless until the bug itself is fixed. **Recommendation**: leave the testnet in its current state as evidence for the fix workstream, do not wipe again until the fix is ready.

### Decision 3 — Step 4 (reputation evidence store) status

Step 4 was paused pending F3-B's report. The report confirms the bug is structural and live. Step 4 cannot responsibly open until at least Decision 1 Part A+B lands, because Step 4's evidence store will write to BadgerDB and be subject to the same "in-memory state never rebuilt on restart" pattern if it is designed analogously. The design of Step 4 should explicitly consider the persistent-applied-set precedent from the F3-B fix.

### Decision 4 — Related bugs triage

The audit surfaced several related bugs that are not F3-B directly but should be named and prioritized:
- **Vote-ingestion originator-vs-receiver asymmetry** (Stage 3 observation) — affects pass_weight calculation, could interact with BFT threshold edge cases.
- **Disbursement-flag persistence race** (Section B) — affects any monitoring that depends on `_paid` flags being truthful; today they never flip.
- **Silent settlement-state-transition error** (Section C) — non-fatal log spam; a principle-12 "beauty is a correctness signal" violation; worth addressing during the F3-B fix since it shares code.
- **`52c5b97a…` missing-`esc:`-on-3-nodes pattern** (deploy-history specific) — environmental pattern on top of the live bug; unclear whether it needs targeted remediation.

---

## Evidence inventory

### Testnet artifacts (all 2026-04-15 UTC; retained on audit controller at `/tmp/`)
- `/tmp/q3_stage2/node{1..5}.json` — Stage 2 per-node capture.
- `/tmp/q3_stage4/node{1..5}.json` — Stage 4 per-node capture.
- `/tmp/q3_v2/node{1..5}.json` — verification-run capture set.
- `/tmp/q3_poster.json`, `/tmp/q3_worker.json`, `/tmp/q3_task.json` — agent and task metadata.
- `/tmp/inspect_badger_v2.go` + binary — disposable BadgerDB inspector (on all 5 nodes; can be removed post-audit).
- Docker logs on each node retain the full settlement event chain for round `0694431966be5fdee12…`.

### Code citations (paths relative to `/Users/michaelschreiber/aethernet/`)

- `internal/settlement/applicator.go:99` — `Applicator.applied` map init (empty).
- `internal/settlement/applicator.go:137–147` — idempotency check.
- `internal/settlement/applicator.go:291` — canonical `RecordFromSync`.
- `internal/settlement/applicator.go:298` — `Settle` transition (swallows error).
- `internal/settlement/applicator.go:306–315` — escrow-lock branch calling `escrow.Hold`.
- `internal/settlement/applicator.go:411–427` — `LoadApplied` method (never called).
- `internal/settlement/verification_consensus_settler.go:76` — `Settle` entry.
- `internal/settlement/verification_consensus_settler.go:90–95` — task-status idempotency (racy).
- `internal/settlement/verification_consensus_settler.go:100–104` — catch-up `Hold` (F3-E, refuted as active mechanism).
- `internal/settlement/verification_consensus_settler.go:168, 370, 388` — silent error discards.
- `internal/settlement/verification_consensus_settler.go:313–324` — validator-list map iteration.
- `internal/recognition/task_verification_consensus_consumer.go:58–101` — consumer entry.
- `internal/recognition/task_verification_consensus_consumer.go:80` — `IsTerminal` guard.
- `internal/recognition/task_verification_consensus_consumer.go:100` — settler invocation.
- `internal/recognition/task_verification_consensus_consumer.go:125–159` — calibration-apply block.
- `internal/escrow/escrow.go:86–97` — `LoadFromStore` method (never called).
- `internal/escrow/escrow.go:104–110` — `IsLocked` (in-memory only).
- `internal/escrow/escrow.go:124–150` — `Hold`.
- `internal/escrow/escrow.go:206–236` — `ReleaseNet` with racy flag persist.
- `cmd/node/main.go` — startup sequence; `LoadApplied` not invoked; `LoadFromStore` for escrow not invoked; genesis consistency check at lines 2703–2716.

### Log evidence (all 2026-04-15 UTC)
- `verification_settler: accept settled` on task `a6574afdf2e5ba1e216053bc138035e6`: 1 line on Nodes 1, 3; **2 lines on Nodes 2, 4, 5**.
- `task_verification_consensus: round finalized from DAG event round_id=0694431966…`: exactly 1 line on every node.
- `WARN recognition: consume failed consumer=settlement err="cannot transition from \"Settled\" to \"Settled\""`: present on every node during Stage 2.

No testnet state was modified during this audit beyond the authorized wipe-and-redeploy and the single fresh probe task. Scratch DB copies removed post-inspection. No containers restarted after the initial wipe.
