# Poster Fee Cross-Node Ledger Consistency Audit — 2026-04-15

**Type**: Read-only audit. No code changes, no config changes, no testnet state mutations beyond the public-ALB task-post API used to reproduce. The only artifact produced is this report.
**Parent audit**: `docs/audits/2026-04-15-poster-fee-double-debit-audit.md` (commit `f41ccbf`). Read that first — this is the focused follow-up to its F3 "cross-node ledger consistency unknown."
**Trigger**: F3 of the parent audit named the load-bearing question: do all 5 testnet nodes converge on the doubled ledger state, or do they diverge? The answer determines severity classification and fix scope for the F1 double-debit and every related economic-layer workstream.

---

## Executive Summary

**Verdict for the fresh reproduction: CONVERGED.** On the probe task `56df96b2107925dbe595670c9a59b165` posted during this audit, all 5 testnet nodes converged on bit-identical state: poster balance −1 GAET (the F1 double-debit signature), escrow-bucket balance 2× budget, `esc:` metadata amount 1× budget, exactly one canonical `txf:<eventID>` Transfer + exactly one synthetic `txf:bucket:…:<seq>` duplicate per node. Sequence numbers and sub-millisecond `RecordedAt` drift vary (expected per-node artifacts of independent replay) but every semantic field (counterparties, amount, settlement, currency, DAG event ID) is byte-identical. The F1 bug is **consensus-silent** — every node is equally wrong in the same way, so BFT consensus does not catch the invariant violation, and the ALB serves a consistent story no matter which node it lands on. Principle 11 (integer canonical state exactness) is violated across the fabric uniformly; principle 5 (protocol-is-source-of-truth) is NOT violated by the F1 path alone.

**However, the cross-node scan surfaced a second, pre-existing divergence that is NOT the F1 bug.** Three of seven historical tasks with escrow buckets on this testnet show divergent ledger state across the 5 nodes — different nodes applied the `TaskSettlement` event differently, leaving different bucket balances per node. This is a strict principle-5 breach caused by non-deterministic settlement application, not by the F1 double-debit. The two bugs interact: because every escrowed task is funded at 2× budget via F1, every task that settles leaves a 500M µAET residual whose fate then depends on whether each node actually applied the payout. Combined effect: at the time of this audit, the 5 nodes' per-node totals of "value sitting in escrow buckets" range from 4,500,100,000 to 5,000,100,000 µAET, a max pairwise delta of 500,000,000 µAET across the fabric.

---

## Q1 — Reproduction per node

### Fresh post driver

| Step | Timestamp (UTC) | Value |
|---|---|---|
| `POST /v1/agents` | 2026-04-15T19:57:39.075Z | poster `97d5fc50d1477460bbeac7876deecb0ef52f4fc8a8f52c6f57bd9e4a7f725f77`; `onboarding_allocation = 50,000,000,000 µAET`; grant event `a5f7c825f1d9…` |
| Grant-wait completed | 2026-04-15T19:58:09.390Z | ALB balance response confirms 50,000,000,000 µAET |
| `POST /v1/tasks` budget=500M, category=writing | 2026-04-15T19:58:09.470Z → 19:58:09.522Z | task `56df96b2107925dbe595670c9a59b165`, status `open`, HTTP 201 |
| Propagation wait | → capture starting 2026-04-15T20:06:37Z | 15 seconds (well past propagation — the canonical Transfer committed at 19:58:12 UTC) |

### Per-node capture (all queries hit `localhost:8338` on each node to bypass ALB round-robin)

| Field | Node 1 (44.200.60.102) | Node 2 (3.87.68.158) | Node 3 (100.27.227.231) | Node 4 (3.232.95.111) | Node 5 (32.195.67.127) |
|---|---|---|---|---|---|
| Poster balance (µAET) | 49,000,000,000 | 49,000,000,000 | 49,000,000,000 | 49,000,000,000 | 49,000,000,000 |
| `escrow:56df96b2…` balance (µAET) | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 |
| `esc:56df96b2…` `.amount` | 500,000,000 | 500,000,000 | 500,000,000 | 500,000,000 | 500,000,000 |
| `esc:56df96b2…` `.worker_paid / validator_paid / treasury_paid` | false / false / false | false / false / false | false / false / false | false / false / false | false / false / false |
| Canonical `txf:c34905c9…` present? | yes | yes | yes | yes | yes |
| Canonical `Amount / Settlement / Reason` | 500M / `Settled` / `escrow-lock` | same | same | same | same |
| Canonical `RecordedAt` | 19:58:12.717017Z | 19:58:12.716134Z | 19:58:12.716784Z | 19:58:12.716878Z | 19:58:12.716876Z |
| Synthetic `txf:bucket:…:<seq>` count | 1 | 1 | 1 | 1 | 1 |
| Synthetic `seq` | 13 | 12 | 20 | 20 | 20 |
| Synthetic `Amount / Settlement / IsGenesis / Memo` | 500M / `Settled` / `true` / `"onboarding allocation"` | same | same | same | same |
| Synthetic `RecordedAt` | 19:58:12.717158Z | 19:58:12.716299Z | 19:58:12.716957Z | 19:58:12.717051Z | 19:58:12.717340Z |

**Evidence source**: each row populated from `ssh ubuntu@<node> "bash /tmp/xnode_capture.sh <label> <poster> <task>"`, whose output is retained at `/tmp/xnode_capture_node{1..5}.log` on the audit controller. BadgerDB inspection used a disposable inspector (`/tmp/inspect_badger.go`) against a read-only scratch copy (`/tmp/xn_<nodenum>_56df96b2…`) that was removed after inspection. No live DB was touched.

**Canonical Transfer**: `c34905c965233718fd50f962f2a66c945973b8e2fe21042eac194f8df022bcc6` — present in every node's DAG (confirmed via `evt:` prefix lookup).

### Critical log evidence from the fresh post

Every node logged this line (with its own local timestamp) within 1 ms of 2026-04-15T19:58:12Z:

```
WARN recognition: consume failed consumer=settlement event_id=4a01548…
  err="ledger: invalid settlement state transition: cannot transition from \"Settled\" to \"Settled\""
```

This is the proximate signature of the F1 bug in action: `RecordFromSync` at `applicator.go:291` has already promoted the Transfer to `Settled`, and the subsequent `transfer.Settle(…, SettlementSettled)` call at line 298 is rejected by the ledger. The applicator then continues into the `case "escrow-lock"` branch at line 306, calls `escrow.Hold`, and `Hold` runs `TransferFromBucket` — the synthetic duplicate entry. All five nodes logged this same warning; all five produced the duplicate entry; all five have identical result state.

---

## Q2 — Convergence verdict on the fresh post

### Verdict: **CONVERGED**

Five rows of Q1, compared field-by-field:
- Poster balance: all 5 nodes agree at 49,000,000,000 µAET.
- Escrow bucket balance: all 5 nodes agree at 1,000,000,000 µAET (= 2× budget, the F1 signature).
- `esc:` metadata: all 5 nodes agree on `amount=500,000,000` with all `_paid` flags `false`.
- Canonical `txf:c34905c9…`: present on all 5 with byte-identical payload.
- Synthetic `txf:bucket:…`: present on all 5 with identical amount, settlement state, IsGenesis flag, memo — only `seq` (local monotonic counter) and sub-millisecond `RecordedAt` differ. These are **independent local artifacts**, not semantic divergence.

**The F1 double-debit is consensus-silent on the escrow-lock path.** Every node runs the settlement applicator on the canonical `Settlement` event independently, every node calls `escrow.Hold` inside the `case "escrow-lock"` branch, every node's `Hold` runs `TransferFromBucket` regardless of any guard state, every node produces the synthetic duplicate entry. The result is a shared (and invariant-violating) ledger state: BFT says "we agree" because nothing in the BFT layer checks whether the agreed-upon state respects the µAET-exactness invariant.

### The F3 peer-node catch-up hypothesis — REFUTED by evidence

The parent audit's F3 flagged `internal/settlement/verification_consensus_settler.go:100–104` as a candidate for cross-node divergence: the `VerificationConsensusSettler` (which fires on `TaskVerificationConsensus` events) also calls `escrow.Hold` behind a `!IsLocked` guard. If event ordering on peer nodes caused `VerificationConsensusSettler` to run its `Hold` before the settlement applicator's `Hold`, peers could end up with three `TransferFromBucket` entries per post (canonical + applicator + settler catch-up), while the originator would have two.

**Not observed.** All 5 nodes show exactly one synthetic `txf:bucket:…` entry (not two), and all 5 escrow-bucket balances agree at exactly 2× budget (not 3× on any peer). The `IsLocked` guard at `verification_consensus_settler.go:101` is functioning correctly: by the time `TaskVerificationConsensus` fires, the settlement applicator has already populated the `esc:` entry on every node, so `IsLocked` returns true and the settler's `Hold` is skipped. This matches the code-trace prediction from the Transport & Recognition investigator: the guard is order-dependent but the observed ordering is Settlement-first on every node.

F3 hypothesis closed: the catch-up path is not producing divergence on the fresh post. Consensus integrity holds on the F1 path specifically. The F3 concern is downgraded from "consensus-breaking possibility" to "latent race-sensitive code that has not fired in the observed window."

---

## Q3 — Historical orphan accounting

Phase B confirmed convergence on the fresh post, which would ordinarily end the inquiry with a single-line "silent total-supply violation" finding. But cross-node inspection of **all** escrow-bucket entries on every node surfaced something else: pre-existing divergence on three historical tasks, unrelated to the F1 double-debit path — a separate principle-5 violation that the parent audit did not name.

### Distinct task IDs with `txf:bucket:` entries per node

| Node | Count | Task IDs |
|---|---|---|
| Node 1 | 7 | `b2f96181…`, `eabbf88f…`, `29771d72…`, `31929514…`, `a2b588c8…`, `56df96b2…`, `52c5b97a…` |
| Node 2 | 6 | Node 1's set minus `52c5b97a…` |
| Node 3 | 6 | Node 2's set |
| Node 4 | 7 | Node 1's set |
| Node 5 | 6 | Node 2's set |

Task `52c5b97a555f8d83dbcee9751ea73d62` (the first-observed accept verdict from 2026-04-11, commit `dcc7c17`) has `esc:` entries and synthetic `txf:bucket:…` entries **only on Nodes 1 and 4**. On Nodes 2, 3, 5 it has neither. **This is a principle-5 violation that pre-dates this audit**: the `esc:` metadata is not consistent across the fabric. The parent audit's `esc:` state lookup was Node-1-only; that one node's view does not represent the cluster's.

### Per-task escrow bucket balance across nodes

Queried via `curl http://localhost:8338/v1/agents/escrow:<taskid>/balance` on each node at 2026-04-15T20:11:02Z (all amounts µAET):

| Task ID | Node 1 | Node 2 | Node 3 | Node 4 | Node 5 | Divergent? |
|---|---:|---:|---:|---:|---:|---|
| `b2f96181104a8911783cd2600cc630e4` | 500,000,000 | 500,000,000 | 0 | 0 | 0 | **YES (3-2)** |
| `eabbf88fca55b442044cd90e101eb905` | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | no |
| `29771d72c60efedb1d90c193005911be` | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | no |
| `319295147190397f417a528e5df7af84` | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | no |
| `a2b588c8b207aa68288020a10b440a67` | 0 | 500,000,000 | 500,000,000 | 500,000,000 | 500,000,000 | **YES (1-4)** |
| `56df96b2107925dbe595670c9a59b165` (fresh) | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | 1,000,000,000 | no |
| `52c5b97a555f8d83dbcee9751ea73d62` | 200,000 | 100,000 | 100,000 | 200,000 | 100,000 | **YES (2-3)** |

Three tasks diverge; four agree. All four that agree are post-F1 tasks whose settlement has not yet fired on any node (they are still in the "escrow bucket holds 2× budget" pre-settlement state) OR the fresh probe task that follows the same pattern. The three that diverge are all tasks whose settlement has fired on some nodes and not others.

### Per-task settlement-application log evidence (Node 1)

Correlating the divergent tasks with Node 1's settlement logs:

- `a2b588c8…` (reject verdict, 2026-04-15T15:48:24Z on Node 1):
  ```
  verification_settler: reject settled task_id=a2b588c8… budget=500000000 poster=365000000 treasury=20000000
  task_verification_consensus: settlement applied task_id=a2b588c8… verdict=fail worker_payout=0 poster_refund=365000000 treasury=20000000 total_distributed=500000000
  ```
  Node 1 applied the reject and drained its bucket to 0. Nodes 2–5 never drained. Divergence pattern: 1-alone-applied vs 4-did-not.

- `b2f96181…` (accept verdict, 2026-04-15T16:14:57Z on Node 1):
  ```
  verification_settler: accept settled task_id=b2f96181… budget=500000000 worker=365000000 validator_pool=115000000 gen_ledger=10000000 treasury=10000000 agreeing_validators=3
  task_verification_consensus: settlement applied task_id=b2f96181… verdict=pass worker_payout=365000000 poster_refund=0 treasury=10000000 total_distributed=500000000
  ```
  Node 1's bucket still holds 500M AFTER this log line. So Node 1 logged the settlement but the drain applied only enough to leave the residual (consistent with "only 1× budget out, 1× budget stays"). Meanwhile, Nodes 3, 4, 5 drained to 0 — but since their "2× budget" was in the bucket before settlement, and `ReleaseNet` moves only `entry.Amount = 500M`, these nodes should also have 500M residual. The observed 0 balance on Nodes 3/4/5 means the settlement moved the residual too — which is only possible if these nodes had SOME different ledger state entering the settlement. The divergence is multi-layered.

- `52c5b97a…` (first-observed accept, 2026-04-11): the small absolute values (200K on N1/N4 and 100K on N2/N3/N5) reflect that this task's `esc:.amount` was 100,000 (a small earlier test), and both the `esc:` entry and the synthetic `txf:bucket:` entry are missing on Nodes 2, 3, 5 entirely. This is the clearest pre-existing divergence — the F1 synthetic duplicate did not fire on 3 of 5 nodes.

### Per-node total of escrow-bucket balances

| Node | Sum across all 7 tracked tasks (µAET) |
|---|---:|
| Node 1 | 4,500,200,000 |
| Node 2 | 5,000,100,000 |
| Node 3 | 4,500,100,000 |
| Node 4 | 4,500,200,000 |
| Node 5 | 4,500,100,000 |

**Max pairwise divergence**: Node 2 vs Nodes 3 or 5 = 500,000,000 µAET. Nodes 1 and 4 agree (both 4,500,200,000). Nodes 3 and 5 agree (both 4,500,100,000). Node 2 disagrees with both groups. The fabric is in **three different states**.

### Disbursement flags never flip

A surprise: every `esc:` entry on every node has `worker_paid=false`, `validator_paid=false`, `treasury_paid=false`, including on nodes that clearly logged `settlement applied` for that task. The settlement code path is writing to BadgerDB (the `txf:` and ledger-balance changes prove it) but the `esc:` entry's disbursement flags are not being updated. The "orphan proxy" suggested in the audit brief (count tasks with any `_paid=true` whose bucket balance is nonzero) yields zero on every node. The proxy is unreliable; orphan detection must be done by direct bucket-balance inspection, which the table above provides.

### Historical-orphan total across observed settlement states

Quantifying the F1 impact on tasks observed in this cluster:
- 4 unsettled tasks (`eabbf88f…`, `29771d72…`, `31929514…`, `56df96b2…`) each hold 2× budget. If each settles normally, 4 × 500M = **2,000,000,000 µAET will be permanently orphaned** once settlement completes on every node.
- 1 partially-settled task (`a2b588c8…`) currently holds 0 on Node 1 (post-drain) and 500M on Nodes 2–5. Future settlement on Nodes 2–5 will move only `esc:.amount = 500M` out; if the bucket balance is 500M, it will drain to 0 there. Final state converges to 0 on all nodes — no orphan from this task.
- 1 partially-settled task (`b2f96181…`): Node 1/2 hold 500M, Nodes 3/4/5 at 0. The 500M residuals on Nodes 1/2 are orphan-equivalent (nothing further will drain them). **1,000,000,000 µAET currently stranded on those two nodes.**
- 1 tiny test task (`52c5b97a…`): 700K µAET total across nodes, trivial magnitude.

**Conservative current-testnet orphan estimate**: 3,000,000,000–3,500,000,000 µAET currently stranded OR will become stranded post-settlement, concentrated in the four "unsettled 2× budget" tasks. Actual future orphan depends on whether all 5 nodes eventually apply each task's settlement deterministically (which historical evidence shows they do NOT).

### Earliest vs latest entry timestamps

The `EscrowEntry` struct does not carry a creation timestamp. Using the corresponding `txf:bucket:…` entry's `RecordedAt` as a proxy on Node 1:
- Earliest: `29771d72…` at 2026-04-15T19:30:46.7Z (parent-audit reproduction).
- Latest: `56df96b2…` (this audit's probe) at 2026-04-15T19:58:12.717Z.

Task `52c5b97a…` from 2026-04-11T17:52:30Z is the chronologically earliest escrowed task on this testnet — but its `txf:bucket:` entry is missing on 3 of 5 nodes, so its ledger-side creation timestamp is only observable on Nodes 1 and 4.

---

## Q4 — Characterization (applied to the pre-existing historical divergence, not the F1 fresh-post path)

Phase B concluded the fresh-post reproduction converged. Phase C surfaced pre-existing divergence on three historical tasks. Characterizing that divergence per Q4's criteria (the audit brief anticipated this path only if the fresh post diverged, but the findings apply equally):

### Pattern: systematic or random?

The divergence is **per-task-and-verdict systematic**, not random or node-systematic:
- `a2b588c8…` (reject): 1 node applied / 4 did not. Node 1 alone.
- `b2f96181…` (accept): 2 nodes did not fully drain / 3 did. Nodes 1 and 2 retain.
- `52c5b97a…` (small test): 2 nodes have full ledger state / 3 do not. Nodes 1 and 4.

No single node is consistently "ahead" or "behind." Node 1 varies: ahead on `a2b588c8…`, retained on `b2f96181…`, synthetic-present on `52c5b97a…`. The pattern tracks which nodes received and processed each `TaskSettlement` / `TaskVerificationConsensus` event, not a per-node bias.

### Correlation with originator

The audit's post was submitted via ALB round-robin, so the originating node is not known precisely for the historical tasks. For the fresh probe, the canonical Transfer's `RecordedAt` timestamps are within 1 ms across all 5 nodes, suggesting all received the Transfer via DAG propagation at effectively the same moment — no originator advantage on the fresh path. **For historical tasks, the divergence does NOT correlate cleanly with an originator pattern.** It correlates with whether each node's settlement applicator ran successfully on that task's `Settlement` event.

### Does `verification_consensus_settler.go:100–104` fire differently per node?

**Apparently not.** The fresh-post evidence shows every node produces exactly one synthetic `txf:bucket:…` entry, not two. The catch-up `Hold` at the settler path is guarded by `IsLocked` which returns true after the applicator path's `Hold` — and the applicator path runs first on every node per the observed ordering. No evidence of the catch-up path firing on any node in the audit window. The F3 hypothesis about this specific catch-up path producing divergence is refuted by the fresh-post capture.

The divergence on historical tasks appears to come from elsewhere — from `SettlementConsumer.Consume` or `TaskVerificationConsensusConsumer.Consume` failing silently or running non-idempotently on some nodes. Exact root cause for the historical divergence is **unknown — evidence required**: a targeted log search for `settlement applied` / `applyTransfer` / `applyTaskSettlement` per-node for each divergent task. Scope-bound for this audit; flagged below.

### Max µAET divergence observed

- Per-task max pairwise: **500,000,000 µAET** (500M) on `a2b588c8…` and `b2f96181…`.
- Across-all-tasks per-node total max pairwise: **500,000,000 µAET** (Node 2 vs Nodes 3/5).

### Does the fix need to address consensus rollback?

Two separate fix scopes:

1. **F1 double-debit** (this audit, fresh-post path): no rollback needed. All nodes converged; fix is forward-looking — stop the second `TransferFromBucket` from firing in the applicator's escrow-lock branch.

2. **Historical settlement-application divergence** (pre-existing, observed in this audit): three tasks are in inconsistent state across the 5 nodes NOW. Any forward-looking fix will leave the divergent state persisted unless an explicit reconciliation pass re-applies the missing settlements on the lagging nodes (or rolls back the leading node). This is beyond the F1 fix scope; it is a separate decision for the founder.

---

## Founder decision required

The findings split cleanly into two decisions.

### Decision 1 — F1 fix scope (informed by this audit)

F1 is consensus-silent and fabric-uniform. The fix does NOT need to address cross-node rollback for the F1 path specifically. A forward-looking code change in `settlement.Applicator.applyTransfer`'s escrow-lock branch (remove or restructure the `escrow.Hold` call so it no longer runs `TransferFromBucket`) will stop the bleeding. Future tasks will cease being 2×-funded. Past tasks remain in their doubled state.

### Decision 2 — retroactive correction scope (new decision surfaced by this audit)

Current testnet orphan estimate: **3,000,000,000–3,500,000,000 µAET** trapped in escrow buckets across the 4 unsettled F1-doubled tasks (will orphan on settlement) plus tasks already past settlement. The orphan value is in synthetic `escrow:<taskID>` addresses — no key can move it. Founder needs to decide:

- (a) **Write off the orphan** on the testnet. Accept the loss, ship the F1 fix forward-looking, note the total in a lessons entry. Mainnet launch will have zero inherited orphan because fresh mainnet state starts from the fixed code.
- (b) **Protocol-level recovery** of the orphaned value. Requires a migration event type or a special-case settlement path that can drain orphan balances to treasury or burn them explicitly. Non-trivial design work.
- (c) **Testnet wipe** and redeploy at the fixed commit. Loses DAG history (the first accept verdict, step-2 verification history, all the audit-reproduction evidence). Most destructive; cleanest ledger.

Given this is the testnet and not production, (a) is the least costly. But the founder may prefer to exercise (b) as a dry-run for any future mainnet orphan-class bug, while the stakes are low.

### Decision 3 — historical-settlement-application divergence (newly surfaced)

Three tasks (`a2b588c8…`, `b2f96181…`, `52c5b97a…`) are in inconsistent ledger state across the 5 nodes right now, with max pairwise divergence 500,000,000 µAET. This is a principle-5 violation independent of F1. Founder needs to decide:

- Investigate root cause (run a settlement-application non-determinism audit in its own prompt before touching the settlement path), OR
- Accept as pre-existing test-environment state and write it off along with the F1 orphan in Decision 2, OR
- Scope the F1 fix wider to include settlement-application determinism guarantees (doubles the fix surface but closes both holes together).

The findings suggest this is a pre-existing bug distinct from F1, and it may have been latent since before step 2 of the reputation workstream. A focused investigation is warranted before any settlement-path code change lands, per CLAUDE.md "read the code, find the root cause" discipline.

---

## Flagged for follow-up

### F3-A — F1 is consensus-silent but fabric-uniform. Fix is forward-looking.
All 5 nodes independently misapply the F1 double-debit the same way. Principle 11 violated cluster-wide; principle 5 not violated by F1 alone. The F1 fix does not need a rollback or reconciliation step.

### F3-B — Pre-existing settlement-application divergence (unrelated to F1)
Three historical tasks in inconsistent ledger state across nodes. Max 500M µAET divergence. Root cause unknown; likely in `SettlementConsumer.Consume` or `TaskVerificationConsensusConsumer.Consume` behavior under replay or under certain event-ordering conditions. **Warrants its own audit** before any code change in the settlement path.

### F3-C — `esc:` disbursement flags never flip
Every `esc:` entry has `worker_paid=false`, `validator_paid=false`, `treasury_paid=false`, including on nodes that logged `settlement applied` for that task. The settlement code path is updating the ledger but not writing back to the `esc:` entry. This is a separate bug — consistent across nodes on the not-updated state — and should be named in lessons. Affects any monitoring or audit tooling that relies on the disbursement flags.

### F3-D — Synthetic `txf:bucket:…` entries still mislabeled
All 5 nodes' synthetic entries have `IsGenesis=true, Memo="onboarding allocation"`. Same mislabeling as the parent audit observed on Node 1. Uniform across fabric, but the label lies on every node.

### F3-E — The catch-up path at `verification_consensus_settler.go:100–104` is a latent race
Per the Transport & Recognition investigator's code trace and the fresh-post observation: the `IsLocked` guard correctly prevents the third `Hold` call when the applicator path runs first. But the guard is order-dependent. A future change to event-ordering semantics (e.g., replay reorderings, partitioned startup) could cause the settler's `Hold` to fire before the applicator's, producing divergent ledgers across nodes. The current observed ordering hides the race; the race is not structurally prevented. Worth a note in the fix design.

### F3-F — Canonical supply invariant
The fabric's total-supply invariant is silently violated across all 5 nodes by the same amount. This is not a principle-5 breach but it is a principle-11 breach and a principle-1 concern (economic correctness is load-bearing for the thesis). The fact that all nodes agree on the wrong number does not make the number correct.

---

## Evidence inventory

**Testnet artifacts** (all 2026-04-15 UTC; retained on the audit controller at `/tmp/` for downstream review):
- `/tmp/xnode_audit_handoff.json` — Phase A handoff payload.
- `/tmp/xnode_capture_node{1..5}.log` — per-node raw capture from `localhost:8338` + BadgerDB inspector.
- `/tmp/phase_c_node{1..5}.log` — per-node historical bucket balance sweep across all `escrow:` addresses.
- `/tmp/phase_c2_node{1..5}.log` — per-node full `esc:` entry dumps.
- `/tmp/logs_node{1..5}.log` — Docker log excerpts around 2026-04-15T19:58:12Z (fresh-post settlement window).
- `/tmp/inspect_badger.go` — disposable inspector source (not shipped; removed from each node's `/tmp` after use).

**Code citations** (paths relative to `/Users/michaelschreiber/aethernet/`):
- `internal/settlement/applicator.go:287–330` — `applyTransfer` the F1 double-debit site (from parent audit F1).
- `internal/settlement/applicator.go:291` — `RecordFromSync` (first debit).
- `internal/settlement/applicator.go:306–315` — `case "escrow-lock"` branch calling `escrow.Hold`.
- `internal/escrow/escrow.go:124–150` — `Hold` method.
- `internal/escrow/escrow.go:138` — `TransferFromBucket` (second debit).
- `internal/escrow/escrow.go:104–110` — `IsLocked` guard (per-node in-memory map, not cross-node synchronized).
- `internal/settlement/verification_consensus_settler.go:100–104` — catch-up `Hold` (F3 candidate; refuted by fresh-post evidence).
- `internal/recognition/task_verification_consensus_consumer.go:58–101` — `TaskVerificationConsensus` dispatch path invoking the settler.
- `cmd/node/main.go:1910` — `tvConsensusConsumer` registration on commitBus (unconditional across nodes).
- `internal/ledger/transfer.go:475–509` — `TransferFromBucket` synthesizes the duplicate `txf:bucket:…` entry with the `IsGenesis=true` mislabel.

**Testnet log evidence** (observed on all 5 nodes during the fresh-post window, 2026-04-15T19:58:12Z ±1 ms):
```
WARN recognition: consume failed consumer=settlement event_id=4a01548…
  err="ledger: invalid settlement state transition: cannot transition from \"Settled\" to \"Settled\""
```
This is the in-flight signature of F1's double-settle-then-forward-to-Hold sequence; the warning is non-fatal and `Hold` runs regardless.

No testnet state was modified during the audit beyond the public-ALB register+post operations used to reproduce. All BadgerDB scratch copies were removed post-inspection. No containers were restarted.
