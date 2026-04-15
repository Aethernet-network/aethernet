# Poster Fee Double-Debit Audit — 2026-04-15

**Type**: Read-only audit. No code changes, no config changes, no testnet mutations beyond the public-ALB task-post API used to reproduce the bug. The only artifact produced is this report.
**Scope target**: the reproducible 2× balance decrement observed on live testnet when a poster posts a task. Prior observations cited in `docs/lessons.md` "Poster fee double-debit on task post" (step-2 workstream, tasks `a2b588c8…` and `b2f96181…`).
**Trigger**: lessons.md entry on step-2 merge (`64c31e0`) explicitly opened this audit as a blocker for any further economic-layer workstream.

---

## Executive Summary

**The extra 500M µAET per task post is a duplicate `TransferFromBucket` call inside `settlement.Applicator.applyTransfer` on the escrow-lock settlement path.** When the canonical `escrow-lock` Transfer event settles, the applicator correctly records the poster → escrow-bucket ledger move (first 500M debit), then immediately calls `escrow.Hold` to register the escrow metadata entry — but `Hold` also performs a `TransferFromBucket` (second 500M debit), because it is the same method used by the test-fallback path where the canonical Transfer has NOT already moved funds. The escrow bucket ends up holding 2× the task budget; the `esc:` metadata entry truthfully records `Amount = budget`; the ledger's `txf:` prefix contains both a canonical Transfer and a synthetic bucket-prefixed entry for the same movement. On settlement (accept / reject / dispute / refund), `ReleaseNet` / `Refund` move only 1× budget out of the bucket, leaving 500M µAET **permanently orphaned** in a no-key synthetic address per task — a silent total-supply-invariant violation on every settled task posted through the production protocol-client path since the escrow-lock Transfer path was introduced.

---

## Q1 — Actual poster-side debit on a task post

Reproduced on the live testnet on 2026-04-15, 19:30:07–19:31:10 UTC, via ALB `https://testnet.aethernet.network`. Driver: raw HTTPS POSTs with AETHERNET-REQUEST-V1 signing from a fresh agent.

| Step | Value | Timestamp (UTC) |
|---|---|---|
| Poster registered (`POST /v1/agents/register`) | `359e98716a6c087472659d9c92b58b49a47089308942c4b33afa8287eec285d3` | 2026-04-15 19:30:07 |
| Grant-wait (30s) completed | — | 2026-04-15 19:30:37 |
| Starting balance | `{"balance": 50,000,000,000, "currency": "AET"}` | 2026-04-15 19:30:42 |
| Task1 posted (`POST /v1/tasks`, budget=500,000,000, category=writing) | task1 = `29771d72c60efedb1d90c193005911be`, HTTP 201 | 2026-04-15 19:30:42 |
| Escrow entry on Node 1's BadgerDB (`esc:29771d72…`) | `{"amount":500000000, "poster_id":"359e98716a…", "task_id":"29771d72…", "treasury_paid":false, "validator_paid":false, "worker_paid":false}` | 2026-04-15 19:30:46 |
| Post-post balance | `{"balance": 49,000,000,000, "currency": "AET"}` | 2026-04-15 19:30:50 |

- **Starting balance**: 50,000,000,000 µAET (the 50 GAET onboarding grant).
- **Task budget specified**: 500,000,000 µAET.
- **Escrow metadata (`esc:` key) amount**: 500,000,000 µAET — consistent with the budget.
- **Actual escrow-bucket ledger balance (`balance(escrow:29771d72…)`)**: 1,000,000,000 µAET. **Double the budget.**
- **Final balance after post**: 49,000,000,000 µAET.
- **Exact delta**: 50,000,000,000 − 49,000,000,000 = **1,000,000,000 µAET lost (2× the budget)**.
- **Match verdict**: the poster lost **twice** the specified budget.

The delta scales with the budget (not a fixed amount): prior runs with the same 500M budget showed the same 1 GAET loss (tasks `a2b588c8…` and `b2f96181…` per `docs/lessons.md`). The second task in this audit (task2 = `319295147190397f417a528e5df7af84`, same poster) also consumed 1 GAET of balance for a 500M budget — linear 2× behavior, not fixed-fee behavior. This rules out a fixed posting fee and points at a duplicated budget-sized transfer.

---

## Q2 — Where the value goes

### Path trace — production (protocol-client) posting path

Every balance-mutating call that fires when `POST /v1/tasks` executes with `protoClient != nil`, in order:

1. **`internal/api/server.go:1419–1529`** — `handlePostTask` handler.
   - Line 1467: `s.transfer.BalanceCheck(posterID, req.Budget)` — validation only, no mutation.
   - Line 1472: `s.taskMgr.PostTask(…)` — creates task metadata in `internal/tasks/tasks.go:514–645`; no balance-mutating calls inside.
   - Line 1500–1504: `s.protoClient.SubmitEscrowLock(posterID, task.ID, req.Budget)` — the production path. No immediate ledger mutation.
   - Line 1514: `s.emitDAGEvent(event.EventTypeTaskPosted, …)` — metadata-only DAG event; no balance impact.

2. **`internal/protocol/client.go:81–82`** — `SubmitEscrowLock` calls `SubmitTransfer(posterID, "escrow:"+taskID, amount, "escrow-lock", taskID)` which, at `internal/protocol/client.go:103–139`:
   - Creates a canonical `EventTypeTransfer` event with `Reason="escrow-lock"`, `FromAgent=posterID`, `ToAgent="escrow:"+taskID`, `Amount=budget`, `SettlementState=SettlementOptimistic`.
   - Line 126: `engine.Submit(ev)` — OCS pending queue (no ledger move).
   - Line 131–132: `publisher.Publish(ev)` — DAG append → `dag.Add` → `SetOnCommit` hook fires recognition dispatch.

3. **Recognition bus dispatch** fires every `Interested` consumer:
   - `OCSSubmitConsumer.Consume` (`internal/recognition/ocs_submit_consumer.go:75–94`): calls `engine.SubmitFromSync(ev)` — idempotent pending-queue add, **no ledger move**.
   - No other consumer is interested in `EventTypeTransfer` at post time.

4. **OCS consensus** finalizes the Transfer → emits `EventTypeSettlement` with `TargetEventID` pointing at the escrow-lock Transfer and `Verdict=Accepted`.

5. **`internal/recognition/settlement_consumer.go:52–71`** — `SettlementConsumer` is `Interested` in `EventTypeSettlement` and calls `settlement.Applicator.ApplyFromEvent(ev)`.

6. **`internal/settlement/applicator.go:287–330`** — `applyTransfer(targetID, verdict, target)` for the escrow-lock Transfer. **This is where both debits live:**

   - **Line 291 — FIRST DEBIT (500M µAET, canonical):**
     ```go
     if err := a.transfer.RecordFromSync(target); err != nil { … }
     ```
     Records the canonical Transfer in the ledger under `txf:<eventID>`. `EventID` = `661e8c71789e21f38d33fa45ebd4b9c510312d9c214a9e72442fd226d786014e` (observed; see Q3). This debits the poster and credits `escrow:taskID`.

   - Line 298: `a.transfer.Settle(targetID, event.SettlementSettled)` — state transition only; no additional balance move.

   - **Lines 306–315 — SECOND DEBIT (500M µAET, synthetic):**
     ```go
     case "escrow-lock":
         // Register escrow entry so release/refund can find it.
         if a.escrow != nil && tp.TaskID != "" {
             if !a.escrow.IsLocked(tp.TaskID) {
                 if err := a.escrow.Hold(tp.TaskID, crypto.AgentID(tp.FromAgent), tp.Amount); err != nil { … }
             }
         }
     ```
     The comment reads "Register escrow entry so release/refund can find it" — the intent is metadata registration only. But `escrow.Hold` at `internal/escrow/escrow.go:124–150` does BOTH:
     1. Line 128–136: writes the `EscrowEntry` to `e.entries` map and persists it.
     2. **Line 138**: `e.ledger.TransferFromBucket(posterID, bucketID(taskID), amount)` — a second, synthetic balance move that debits the poster AGAIN and credits `escrow:taskID` AGAIN.

   The `IsLocked` guard at line 309 cannot help on the canonical path: on a fresh task, no prior `esc:` entry exists, so the guard is always false, and `Hold` always runs its full body — including the duplicate `TransferFromBucket`.

### Call-site summary table

| # | File:Line | Call | Amount | From → To | Debit counted? |
|---|---|---|---|---|---|
| 1 | `protocol/client.go:81` | `SubmitEscrowLock` → creates Transfer event | 500M | poster → escrow:taskID | Pending (Optimistic) only |
| 2 | `settlement/applicator.go:291` | `transfer.RecordFromSync(target)` | 500M | poster → escrow:taskID | **YES — 1st debit (canonical)** |
| 3 | `settlement/applicator.go:298` | `transfer.Settle(targetID, Settled)` | — | state transition | no |
| 4 | `settlement/applicator.go:310` → `escrow/escrow.go:138` | `escrow.Hold` → `TransferFromBucket` | 500M | poster → escrow:taskID | **YES — 2nd debit (synthetic)** |

### Undocumented transfer — the bug

`docs/multi-validator-consensus-final-design.md` §5 documents only the 73/23/2/2 split of the escrow at settlement. There is no documented post-time fee and no documented "escrow-registration" transfer. The `TransferFromBucket` at `escrow/escrow.go:138`, when invoked from the applicator's escrow-lock branch, is the undocumented transfer that causes the 2× debit.

---

## Q3 — Whether the extra debit is settled correctly

**Answer: (c), with a twist.** The extra 500M is NOT going to a documented recipient (not treasury, not validator pool, not generation ledger, not fee bucket). It goes into the **same escrow bucket** that received the canonical 500M — so the bucket ends up holding 2× the budget. Sink-level accounting balances (the poster's 1 GAET loss equals the escrow bucket's 1 GAET gain), but the 500M surplus in the escrow bucket is inaccessible on every non-"pay-out-everything" settlement path.

### Per-sink before/after accounting

Sink balances queried via `GET /v1/agents/<id>/balance` before task1 post and immediately after (all amounts µAET):

| Sink | Before | After | Δ |
|---|---:|---:|---:|
| genesis:founders | 150,000,000,000,000 | 150,000,000,000,000 | 0 |
| genesis:investors | 150,000,000,000,000 | 150,000,000,000,000 | 0 |
| genesis:ecosystem | 259,550,000,000,000 | 259,550,000,000,000 | 0 |
| genesis:rewards | 198,950,000,000,000 | 198,950,000,000,000 | 0 |
| genesis:treasury | 100,000,040,000,000 | 100,000,040,000,000 | 0 |
| genesis:public | 100,000,000,000,000 | 100,000,000,000,000 | 0 |
| genesis:faucet | 39,995,000,000,000 | 39,995,000,000,000 | 0 |
| testnet-validator | 750,000,000,000 | 750,000,000,000 | 0 |
| escrow:29771d72c60efedb1d90c193005911be | (non-existent) | **1,000,000,000** | **+1,000,000,000** |
| poster 359e98716a… | 50,000,000,000 | 49,000,000,000 | −1,000,000,000 |

Total accounted for: poster loses 1,000,000,000; escrow bucket gains 1,000,000,000. **Delta = 0.** No value is being burned at the moment of the post. But the bucket contents are 2× what the escrow entry (`esc:`) claims.

### What happens on settlement

The escrow entry's `Amount` field is 500M. When the task settles:

- **Accept** — `ReleaseNet` (`internal/escrow/escrow.go:186`) moves 73% / 23% / 2% / 2% of `entry.Amount = 500M` out of the bucket. Total moved: 500M. The other 500M **remains in the bucket permanently** — no key holds that address, no future settlement will touch it.
- **Reject** — same issue with the 73/23/4 split; 500M remains.
- **Dispute** — 36.5 / 36.5 / 27 split; 500M remains.
- **Refund** (cancellation) — `Refund` at `internal/escrow/escrow.go:252` moves `entry.Amount` = 500M back to the poster. Poster recovers 500M of their 1 GAET loss. The other 500M **remains in the bucket permanently**.

**Every settled task posted via the production protocol-client path leaves 500M µAET permanently orphaned in `escrow:<taskID>`.** The address is a synthetic bucket; no keypair controls it; the funds are unrecoverable without a protocol change. This is a silent total-supply-invariant violation.

Over the two testnet verification tasks (`a2b588c8…`, `b2f96181…`) plus the two audit reproductions (`29771d72…`, `319295…`), **at least 2 GAET (2,000,000,000 µAET) has been orphaned on this testnet alone**. Additional pre-audit tasks would have done the same.

---

## Q4 — Per-post or per-something-else

Same poster (`359e98716a…`) posted a second task immediately after task1:

| | Before task2 | After task2 | Δ |
|---|---:|---:|---:|
| Poster balance | 49,000,000,000 | 48,000,000,000 | **−1,000,000,000** |
| escrow:319295147190397f417a528e5df7af84 | (non-existent) | 1,000,000,000 | +1,000,000,000 |

Task2 showed the identical 2× pattern. The bug is **per-post, linear in budget, and not first-post-only**. Every task post on the production path triggers the duplicate `TransferFromBucket`. This aligns with Q2's structural analysis — the bug is in the escrow-lock settlement path, which fires on every post independent of the poster's history.

---

## Q5 — SDK vs protocol layer

Not exercised in this audit. The repo's Python SDK (`sdk/python/`) is worker-side; there is no SDK wrapper for task posting. Both prior verification runs (`a2b588c8…`, `b2f96181…`) and this audit's reproductions posted directly against the ALB via raw HTTPS with AETHERNET-REQUEST-V1 signing. The bug is confirmed at the **protocol layer** (`internal/settlement/applicator.go`, `internal/escrow/escrow.go`) — no SDK is involved.

SDK parity verification is **unknown — evidence required**: a Python (or other) SDK with a `PostTask` method would need to be built or located; the current worker SDK does not cover the posting path. Given the bug is demonstrably in protocol-layer code, SDK parity testing is not a gating concern for the follow-up fix, but closing the loop would require a parallel-path comparison in a later session.

---

## Q6 — Historical scope

### When was this introduced?

The code paths involved are stable:
- `internal/escrow/escrow.go:124–150` `Hold` method — its `TransferFromBucket` call at line 138 has been the fund-moving mechanism since the escrow package was introduced.
- `internal/settlement/applicator.go:306–315` `case "escrow-lock"` branch — introduces the applicator-side `Hold` call that re-moves funds after `RecordFromSync` already did.
- `internal/protocol/client.go:81` `SubmitEscrowLock` — the protocol-client path that publishes the canonical Transfer event.

The three together produce the double debit whenever the protocol-client path is active. On single-node fallback paths (where `protoClient == nil`, `internal/api/server.go:1507` direct call to `escrow.Hold`) the behavior is different — there, `Hold` is the ONLY path that moves funds, and the canonical Transfer never exists. So the double-debit is specific to production multi-validator testnet/mainnet deployments.

### Prior occurrences on the current testnet

The first observed accept verdict (2026-04-11, task `52c5b97a…`, `docs/handoff-2026-04-11-blobsync-accept-path.md`) reported exact 73/23/2/2 settlement math on the escrow. The handoff did not call out a poster-fee anomaly, but the settlement was accept-path — 73% went back to the worker via `ReleaseNet`, leaving a 500M surplus **already trapped in the bucket** at that point. The lessons.md entry captures the first two post-step-2 observations but the bug was live the whole time.

### Principle 6 generalization check — does this pattern affect other paths?

Spot-check of other balance-mutating operations in the protocol:

- **Claim path**: `internal/tasks/tasks.go` and `internal/api/server.go` claim handling do not invoke an escrow-lock analog; claims are task-state transitions without value transfers. No double-debit surface there.
- **Vote stakes**: `internal/taskverification` vote recording does not debit validators on a per-vote basis; vote weight derives from validator-set stake, computed at round open. No per-vote balance mutation.
- **`internal/settlement/verification_consensus_settler.go:100–104`**: reports a third `escrow.Hold` catch-up path for peer nodes that missed the post-time escrow lock. This is structurally the same as the applicator's escrow-lock branch. If it fires on a peer node, the peer node's local ledger would see the same double-debit pattern. **Whether peer-node ledgers are also over-charged by 500M is unknown — evidence required**: per-node balance comparison across all 5 testnet nodes for the same poster after a post. If all nodes see the same 2× debit, the bug affects consensus state uniformly; if only the originating node sees it, the ledgers diverge per node (which would itself be a CLAUDE.md principle-5 violation).
- **Fee collection** (`internal/fees/collector.go`): correctly exempts escrow-lock from post-time fees via `isProtocolInternalTransfer` (line 440). The fee path is clean. The post-time settlement fee is zero (as designed).
- **Generation ledger distribution**: no post-time transfer; generation-ledger royalties only move at settlement. Clean.
- **Stake operations** (`internal/staking/staking.go:243, 271`): stake lock/unlock calls `TransferFromBucket` once per operation; no double-move observed in static analysis (not verified on testnet in this audit).

**Pattern assessment**: the bug is **path-specific** to `case "escrow-lock"` in the applicator. It is not a general "every `TransferFromBucket` is double-called" problem. The fix is localized to the applicator's escrow-lock branch or to `escrow.Hold`'s contract (decoupling registration from fund movement).

### Synthetic ledger entry mislabeling — related observation

The duplicate `TransferFromBucket` entry observed on testnet:
```
txf:bucket:359e98716a...:escrow:29771d72c60efedb1d90c193005911be:500000000:11 =
  {"Amount":500000000, "FromAgent":"359e98716a…",
   "ToAgent":"escrow:29771d72…", "Reason":"",
   "IsGenesis":true, "Memo":"onboarding allocation",
   "Settlement":"Settled", …}
```

Two field values are wrong:
- `IsGenesis=true` — this is not a genesis funding event.
- `Memo="onboarding allocation"` — this is not an onboarding allocation; it is an escrow lock.

The defaults appear to come from `ledger.TransferFromBucket` populating the synthetic entry with hardcoded `IsGenesis=true` and a fallback memo. This actively misleads anyone auditing the ledger after the fact (e.g., the testnet subagent investigating the poster fee initially saw "IsGenesis / onboarding allocation" entries and had to dig deeper to realize they were escrow-lock duplicates). Flagged as a related bug; fix belongs in the same commit that addresses the double-debit.

---

## Flagged for follow-up

### Primary finding

**F1 — Duplicate `TransferFromBucket` in the escrow-lock settlement path.** Location: `internal/settlement/applicator.go:306–315` calls `escrow.Hold` (which invokes `TransferFromBucket` at `internal/escrow/escrow.go:138`) after `RecordFromSync` at line 291 has already performed the canonical poster → escrow-bucket move. The intent per the code comment ("Register escrow entry so release/refund can find it") is metadata registration only; the implementation includes a fund movement because `Hold` is the same method the test-fallback path uses where the canonical Transfer does NOT exist. The protocol has no "register-only" escrow variant. Blocks all further economic-layer workstreams.

**F2 — Permanent orphaning of 500M µAET per settled task on the production path.** On every settled task posted via `protoClient.SubmitEscrowLock`, the escrow bucket holds 2× the budget but only 1× is paid out / refunded at settlement. The remaining 500M sits in a synthetic `escrow:<taskID>` address with no key. Silent total-supply-invariant violation. Severity: critical — principle 11 ("integer canonical state, no exceptions") and CLAUDE.md invariant "integer arithmetic only for economic calculations — exact to the µAET" both violated at scale.

### Related findings

**F3 — Synthetic `TransferFromBucket` entries mislabeled as `IsGenesis=true` with memo "onboarding allocation".** Observed on testnet via raw `txf:bucket:…` key dumps. Misleads post-hoc audits. Likely a default-initialization bug in `internal/ledger/transfer.go`'s `TransferFromBucket`. Fix belongs in the same commit that addresses F1.

**F4 — Peer-node ledger consistency unknown.** `internal/settlement/verification_consensus_settler.go:100–104` contains a third `escrow.Hold` catch-up call for peer nodes that missed the post-time escrow lock. Whether peer nodes reach the same doubled state or diverge from the originating node's ledger is **unknown — evidence required**: a cross-node balance comparison for the same poster/task across all 5 testnet nodes. If ledgers diverge, the bug threatens consensus integrity (principle 5: "the protocol is the source of truth"). If they agree on the doubled state, the bug is uniform but the consensus-level damage is already baked in.

**F5 — `escrow.Hold`'s method-level overloading of "register entry" and "move funds".** The method mixes two semantically distinct operations: populating the `esc:` metadata entry, and performing a ledger transfer. Callers in different contexts want one or the other. A proper fix likely splits the method into `Hold` (moves + registers, for single-node / test paths) and `RegisterEscrow` (metadata-only, for the protocol-client path where the canonical Transfer already moved funds). Design decision deferred to the follow-up workstream.

### Principle & invariant violations

- **Principle 1 ("the thesis is load-bearing")**: compound verification depends on precise economic gradients. A 500M orphan per task is a silent tax that corrupts the economic model. Threatens thesis credibility.
- **Principle 11 ("integer canonical state, no exceptions")**: economic math must be exact to the µAET. 500M orphaned per task is not exact math.
- **Principle 12 ("beauty is a correctness signal")**: the applicator's escrow-lock branch has a comment ("Register escrow entry so release/refund can find it") that is semantically incorrect given the actual behavior — the code does more than the comment claims. Beauty violation is a correctness signal per principle 12, and it was here.
- **CLAUDE.md invariants**: "Settlement events fire exactly once per finalized target" holds (the Transfer event fires once); but "All consumers in the recognition fabric are idempotent" is preserved in letter while violated in spirit — the applicator's `Hold` call is idempotent (guarded by `IsLocked`) but the fund movement it performs is a duplicate of what `RecordFromSync` already did.

### Related subsystems worth a separate audit

- **Cross-node ledger consistency on the escrow-lock path** — F4 above. Not covered here because it would expand scope beyond the poster-fee question, but it is a load-bearing concern and should be run before the fix lands. Specifically: after a fresh task post on the testnet, query `balance(escrow:<taskID>)` on all 5 nodes and compare. Disagreement = principle-5 violation.
- **Total-supply accounting audit** — F2 above creates an ongoing drain of minted supply into orphan addresses. A one-shot audit to measure cumulative orphaned µAET across the testnet's lifetime would quantify the damage and inform whether recovery is possible (protocol-change required to move value out of no-key addresses).
- **`ledger.TransferFromBucket` default-field behavior** — F3 above. Every synthetic bucket transfer in the ledger inherits `IsGenesis=true` / `Memo="onboarding allocation"` which is wrong for most of them. Quick static sweep of all call sites would reveal how wide the mislabeling spreads.

---

## Evidence inventory

**Code citations** (all paths relative to `/Users/michaelschreiber/aethernet/`):
- `internal/api/server.go:476` — POST /v1/tasks route registration.
- `internal/api/server.go:1419–1529` — handlePostTask full body.
- `internal/api/server.go:1500–1504` — protocol-client path: `SubmitEscrowLock`.
- `internal/api/server.go:1506–1510` — test-fallback path: direct `escrow.Hold`.
- `internal/api/server.go:1514` — `emitDAGEvent(TaskPosted)`.
- `internal/tasks/tasks.go:514–645` — `PostTask` method: metadata only, no balance mutation.
- `internal/protocol/client.go:81–82` — `SubmitEscrowLock` constructs + publishes the canonical Transfer.
- `internal/protocol/client.go:103–139` — `SubmitTransfer` path.
- `internal/recognition/ocs_submit_consumer.go:31–98` — OCS submit consumer (no ledger mutation).
- `internal/recognition/settlement_consumer.go:39–75` — Settlement consumer: delegates to applicator.
- `internal/settlement/applicator.go:287–330` — `applyTransfer` full body.
- `internal/settlement/applicator.go:291` — **1st debit: `transfer.RecordFromSync(target)`**.
- `internal/settlement/applicator.go:306–315` — escrow-lock branch calling `escrow.Hold`.
- `internal/settlement/verification_consensus_settler.go:100–104` — third `escrow.Hold` catch-up path (peer-node scenario).
- `internal/escrow/escrow.go:124–150` — `Hold` method full body.
- `internal/escrow/escrow.go:138` — **2nd debit: `TransferFromBucket`** inside `Hold`.
- `internal/ledger/transfer.go:475–509` — `TransferFromBucket` implementation; emits synthetic `txf:bucket:…` entries.
- `internal/fees/collector.go:440` — `isProtocolInternalTransfer` correctly exempts `escrow-lock` from fee collection.
- `docs/multi-validator-consensus-final-design.md:97–126` — v4.1 economic model §5: 73/23/2/2 accept, 73/23/4 reject, 36.5/36.5/27 dispute; no post-time fee documented.

**Testnet citations** (all 2026-04-15 UTC):
- ALB: `https://testnet.aethernet.network`.
- Poster: `359e98716a6c087472659d9c92b58b49a47089308942c4b33afa8287eec285d3`, registered 19:30:07.
- Starting balance query 19:30:42: `{"balance":50000000000}`.
- Task1 post 19:30:42: `{"id":"29771d72c60efedb1d90c193005911be","budget":500000000,"status":"open"}`.
- Post-post balance 19:30:50: `{"balance":49000000000}` — delta 1,000,000,000 µAET.
- Node 1 BadgerDB (`/data/aethernet/aethernet.db` copied read-only to `/tmp/escrow_audit_<taskid>`, inspected, removed):
  - `esc:29771d72…` → `{"amount":500000000, …}` (metadata: 1× budget).
  - `txf:661e8c71789e21f38d33fa45ebd4b9c510312d9c214a9e72442fd226d786014e` → canonical Transfer, Settled, 500M, `evt:…` counterpart confirmed in DAG.
  - `txf:bucket:359e98716a…:escrow:29771d72…:500000000:11` → synthetic, Settled, 500M, `IsGenesis=true`, `Memo="onboarding allocation"` (mislabeled), **no corresponding `evt:bucket:…` in DAG**.
  - Two `txf:` entries recorded 220 microseconds apart (19:30:46.110945366Z and 19:30:46.111167826Z).
- Task2 = `319295147190397f417a528e5df7af84`: post-post balance 48,000,000,000 (another 1 GAET debit, same pattern).
- Sink-level balance snapshots before/after task1 post: no delta on any documented recipient (treasury, validator, ecosystem, faucet, generation-ledger) except the poster (−1 GAET) and the escrow bucket (+1 GAET). Total accounted: 0 net outside the poster/escrow pair.

No testnet state was modified during the audit beyond the normal public-ALB post operations used to reproduce the bug. DB copies were read-only and removed post-inspection.
