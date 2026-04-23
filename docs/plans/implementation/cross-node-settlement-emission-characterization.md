# Cross-node settlement emission — characterization

**Discovered**: 2026-04-23, during F4C step 5 §11.15 accept-corpus run on the fresh 5-AWS-node testnet (image `f4-combined-0e93f48`).
**Frozen-state evidence**: `/tmp/f4c-halt-evidence/` plus per-node docker logs preserved on the 5 AWS nodes. Cluster kept running (no wipe) for architect inspection.
**Branch context**: `feat/selection-consistency-fix` @ `0e93f48`. Combined-branch merge @ `114d304` + docs.
**Scope**: this document characterizes a newly-surfaced cross-node-divergence bug class, orthogonal to but adjacent to the selection-race bug class F4B closed. It does not design a fix.

---

## 1. Executive summary

Per-node local settlement for `TaskVerificationConsensus` events contains an unserialized concurrent-Apply race: on live 5-AWS-node testnet, a single node that receives two or more multi-emit `TVConsensus` events for the same `RoundID` can execute two concurrent `settler.Settle → escrow.ReleaseSettlement` call stacks on the same task. The per-recipient `WorkerPaid` / `ValidatorsPaid` / `TreasuryPaid` flags in the escrow entry guard individual payouts (first-wins-skip-second), but they do NOT serialize the two concurrent execution paths — both paths advance into the per-validator distribution loop, and one of them hits `ErrInsufficientBalance` on a subsequent validator payment as the other concurrent path partially drains the bucket. The erroring path transitions the local logical-key admission record to `StateFailedRetryable`, aborts before the task-terminal state update, and never re-runs successfully (retries hit the same partial-drain state). The consequence is that **different nodes complete different subsets of the 10-task accept-corpus** — Nodes 1/3 completed 8 settlements, Nodes 2/4 completed 7, Node 5 completed 6 — producing a permanent three-way cross-node divergence in `treasury_balance` (180 K / 160 K / 120 K µAET on top of a 100T µAET base) and a two-way divergence in the worker's balance projection. **F4's LogicalKey fix is correct and closes its own bug**; this bug is upstream: the `per-(consumer, key) admission provides per-RoundID serialization` claim in F4 plan v2 §4.5 was empirically false — the admission record serializes dedup AFTER the first Apply completes, but does NOT serialize concurrent first-Applies.

---

## 2. Evidence

### 2.1 Frozen per-node ledger state at halt time

Captured 2026-04-23 12:51 UTC after `go test`-simulated accept corpus of 10 tasks (9 finalized: 7 accepts + 1 reject + 1 stuck in `submitted`; 1 task 532cc1d2 still mid-flight on all nodes):

| Node | IP | Private IP | AgentID (first 16) | dag_size | treasury_balance (delta from 100T base) | onboarding_allocated | settler firings (accept / reject) | admit-failed WARN count |
|---|---|---|---|---:|---:|---:|---:|---:|
| 1 | 44.200.60.102 | 172.31.12.70 | d839e1ffed3bf84d | 279 | **+180,000** | 50,000,000,000 | 7 / 1 | 12 |
| 2 | 3.87.68.158 | 172.31.93.186 | 741225dd78889ad0 | 275 | **+160,000** | 50,000,000,000 | 6 / 1 | 12 |
| 3 | 100.27.227.231 | 172.31.17.237 | 05adbeb0174e6cc5 | 271 | **+180,000** | 0 | 7 / 1 | 11 |
| 4 | 3.232.95.111 | 172.31.4.3 | d4cfeca78f4a91c1 | 267 | **+160,000** | 0 | 6 / 1 | 16 |
| 5 | 32.195.67.127 | 172.31.13.36 | 5df098cffaf1f2d0 | 263 | **+120,000** | 0 | 6 / 0 | 17 |

Treasury balance shows **three distinct values across five nodes** on a canonical-state projection. Worker balance shows **two distinct values**: Node 1 = 50,005,110,000 µAET (= 50B + 7×730K), Nodes 2–5 = 50,005,840,000 µAET (= 50B + 8×730K). Poster balance is byte-identical across all five nodes (49,990,730,000 µAET = 50B − 10×1M). Validator-manifest digest and active-weight are byte-identical across all five nodes.

### 2.2 Per-task settler-fired matrix

Rows are tasks; columns are N1..N5; `X` = local `verification_settler: accept settled` / `reject settled` log emitted on that node; `-` = no settler firing, implying either the task was never locally settled or the settler partially ran and errored before reaching the terminal-status log.

| Task ID (first 12) | verdict | N1 | N2 | N3 | N4 | N5 |
|---|---|:---:|:---:|:---:|:---:|:---:|
| 0a8a3f2e1cc3 (pub-key crypto) | accept | − | X | − | X | − |
| 1246cb59b97f (git)            | accept | X | − | X | X | X |
| 4c5c6e13d904 (Merkle)         | accept | X | X | X | X | X |
| 7119d0f3948d (quicksort)      | **reject** | X | X | X | X | − |
| 9e375395c7e6 (actor model)    | accept | X | X | X | X | X |
| bf356b28feb3 (DNS)            | accept | X | X | X | X | − |
| c1e5abf29d39 (CAP)            | accept | X | X | X | − | X |
| d0190b31115f (HTTP/2)         | accept | X | X | X | − | X |
| e988fa7791ed (TLS)            | accept | X | − | X | − | X |
| 532cc1d28185 (TCP/UDP)        | (pending) | − | − | − | − | − |

Each node has a DIFFERENT subset of tasks for which its local settler reached the terminal `accept settled`/`reject settled` log. No two nodes have the same subset. One task (532cc1d2) is pending on all nodes (round still in-flight as of halt).

### 2.3 Representative failure log — Node 5, task `bf356b28` (DNS)

```
2026/04/23 17:33:35 WARN dispatcher_admission_router: admit failed
  event_id=a023e640fe7f86a991dcee1edbc6ee650eee58ba0b97c4331e2677e70bae79a5
  event_type=TaskVerificationConsensus
  err="dispatch: logical-key Apply for tv_consensus_settlement_lk/
       990c0b167dcf0a38b7371a4be374bf7f82f059ba6c7ffdfe3e8821e4189cf792:
    tv_consensus_lk: settle round 990c0b16…:
    verification_settler: release settlement:
    escrow: settlement validator d839e1ffed3bf84d3fb7794ea25acb9858a9eddc244afa7d844a4b0c0a1f5aff
      for task bf356b28feb316ed317d3eaa706fdb73:
    ledger: insufficient balance:
    bucket escrow:bf356b28feb316ed317d3eaa706fdb73 balance 40002 < allocation 76668"

2026/04/23 17:33:35 WARN dispatcher_admission_router: admit failed
  event_id=8dd6250590ba99901193dc829f483ea4b16c04756c29e4f694118465ae4b2cb0
  event_type=TaskVerificationConsensus
  err=<same round 990c0b16, same task bf356b28, same "balance 40002 < allocation 76668">
```

**Two TVConsensus events for round 990c0b16 arrived on Node 5 within the same wall-clock second.** Both failed with identical error. Node 5's settler never reached the `accept settled` terminal log for task `bf356b28`.

The "40002" residual is the exact arithmetic after three validators are paid from a freshly-funded 1,000,000 µAET escrow: `1,000,000 − 730,000 (worker) − 3 × 76,666 (validator 1/2/3) = 40,002`. The validator pool for this round is 230,000 split Q-weighted across three agreeing validators as `76,666 + 76,666 + 76,668 = 230,000`. The WARN's "allocation 76,668" is validator `d839e1ff`'s share in that split — the validator that would be the fourth in the lex-sorted iteration order (after the three at 76,666).

**Inference**: at the time of both WARNs, the worker was already paid (730 K drained) and three validators were already paid (229,998 drained). A concurrent `escrow.ReleaseSettlement` call stack on the same node — from the first (now-lost) Apply — advanced three validators deep into the payout loop before the second Apply's call stack attempted validator `d839e1ff` at allocation 76,668, found only 40,002 in the bucket, and returned the insufficient-balance error. The per-recipient `ValidatorsPaid` map guards against DUPLICATE payouts to the same validator but does not prevent the two concurrent call stacks from collectively draining the bucket past the point where either could complete.

Same shape repeats on Node 5 for tasks `0a8a3f2e` and `7119d0f3` (timestamps 17:34:05 and 17:34:29). Every node's 11–17 admit-failed WARNs decompose into this shape (TVConsensus-layer concurrent Apply) plus a separate shape on Settlement events (discussed in §3.3).

### 2.4 Settlement-layer parallel error shape

Settlement events (type `Settlement`, `settlement_lk` consumer — F4B §5.2.2) show a parallel but distinct error:

```
2026/04/23 17:26:49 WARN dispatcher_admission_router: admit failed
  event_type=Settlement
  err="dispatch: logical-key Apply for settlement_lk/55e9798e…:
       settlement_lk: apply 55e9798e…:
       ledger: invalid settlement state transition:
       cannot transition from \"Settled\" to \"Settled\""
```

This is the benign-idempotency shape: two concurrent `settlementApp.Apply` calls for the same `TargetEventID`. One succeeds, sets the transfer entry to `Settled`; the other re-attempts and is rejected by `validLedgerTransitions[Settled][Settled] == false`. No state corruption, no balance drift — the ledger's transition guard catches it. But the error propagates back to the admission router as a FailedRetryable result, polluting the logs and the admission-record state.

### 2.5 ERROR-level logs

Zero `ERROR`-level log lines across all five nodes during the corpus run. All failures surface at `WARN` level via the admission router's log-and-swallow pattern — which founder's halt criterion "any ERROR-level logs during corpus runs" did not directly trigger. The halt was triggered by the separate criterion "any cross-node ledger divergence on any task in any corpus," via observed `treasury_balance` divergence.

---

## 3. The mechanism

### 3.1 TVConsensus path (the load-bearing failure)

File:line walkthrough of the concurrent-Apply race on a single node observing two `TVConsensus` events for the same `RoundID`:

1. **`internal/recognition/dispatcher_admission_consumer.go` Consume** (IM Part E.1, commit-13): every committed event reaches `dispatcher.Admit`. Two TVConsensus events for RoundID X arrive via fastpath within ~100 ms of each other. Both enter `Admit`.

2. **`internal/dispatch/dispatcher.go` Admit** → **`admitLogicalKey(ctx, ev)` at `internal/dispatch/logical_key_admit.go:32`**: both goroutines snapshot the interested-consumer list (read-only, `d.mu.RLock()`) and call `admitOneLogicalKey(ctx, ev, tvConsensusLKConsumer)` concurrently.

3. **`admitOneLogicalKey` at `internal/dispatch/logical_key_admit.go:109`**:
   - Both goroutines compute `key = consumer.Key(ev) = RoundID`.
   - Both goroutines compute `storeKey = "dispatch:lk:tv_consensus_settlement_lk:<RoundID>"`.
   - Both call `reserveOrLoadLogical(storeKey, ev, c, key)` at line 117.

4. **`reserveOrLoadLogical` at `internal/dispatch/logical_key_admit.go:186`**: reads the admission record from the store. With no synchronization on the per-key code path, **both goroutines can observe `isNotFound`** (if the first goroutine's write-back hasn't yet persisted the StateProcessing record when the second goroutine's read happens). Both construct fresh `AdmissionRecord` instances at `StateProcessing`. Alternatively, the second goroutine reads the record the first one wrote — but still sees `State = StateProcessing`, not `StateApplied`.

5. **`admitOneLogicalKey` line 127**: the StateApplied short-circuit check. Both goroutines evaluate `rec.State == StateApplied` to false (record is Processing, not Applied). Both proceed past the short-circuit.

6. **`admitOneLogicalKey` lines 131–154**: both goroutines call `c.RoundState`, `c.IsComplete`, `c.DeriveOutcome`. These are pure reads on canonical state; no per-node interleaving issue.

7. **`admitOneLogicalKey` line 160** → `safeApplyLogicalKey` line 220 → `c.Apply(ctx, key, outcome)`. Both goroutines enter `TVConsensusLogicalKeyConsumer.Apply` in parallel.

8. **`internal/dispatch/tv_consensus_lk_consumer.go` `Apply`** (F4B §5.2.1): calls `settler.Settle(ctx, &syntheticPayload, round)`.

9. **`internal/settlement/verification_consensus_settler.go:132` `Settle`**:
   - Line 146: checks task-terminal short-circuit. Task is NOT yet terminal (neither goroutine has set it) — both proceed.
   - Line 154: `escrowMgr.Get(taskID)` — returns the escrow entry with all paid flags false.
   - Line 187: calls `settleAccept(...)`.

10. **`settleAccept` at line 202**: computes payouts (worker, validators, gen-ledger, treasury), then calls `escrowMgr.ReleaseSettlement(...)` at line 243.

11. **`internal/escrow/escrow.go:468` `ReleaseSettlement`** (the load-bearing race site):
    - Step 1 (line 486–494) worker payout: `if workerAmount > 0 && !entry.WorkerPaid { ledger.TransferFromBucketLabeled(...); entry.WorkerPaid = true; persist }`. The `entry.WorkerPaid` read and write are each under `e.mu.Lock()` individually, but the **check-then-transfer-then-set sequence is NOT atomic**. Goroutine A: observes `WorkerPaid=false`. Releases lock. Calls `TransferFromBucketLabeled` (drains bucket by 730,000). Re-acquires lock, sets `WorkerPaid=true`. Goroutine B: may have observed `WorkerPaid=false` before A's set, already called `TransferFromBucketLabeled` a second time — draining bucket a SECOND 730,000 if balance allows. (On live testnet observation: both `WorkerPaid=false` observations happen before either transfer completes, so both call `TransferFromBucketLabeled` on the same freshly-funded 1M bucket. First call succeeds with 730K debit → 270K remaining. Second call succeeds with 730K debit → wait, 270 < 730, so the SECOND worker call actually errors at `ledger/transfer.go:496` "insufficient balance". Error propagates back up, admission goes to FailedRetryable. This is ONE of the observed failure shapes.)
    - Step 3 (line 507–538) per-validator payouts — the more common observed shape. The `validatorIDs := sortedAgentIDs(validators)` at line 516 is deterministic (E.P3 fix). Both goroutines iterate the same sorted list. For each `vid`: read `alreadyPaid` under lock, release lock, if not paid call `TransferFromBucketLabeled`, re-acquire lock, mark paid. Interleaving: goroutine A observes `alreadyPaid=false` for vid_1, releases lock. Goroutine B also observes `alreadyPaid=false` for vid_1, releases lock. A: transfer succeeds, marks paid. B: transfer succeeds (or fails on balance), marks paid. If B's transfer succeeded, the validator was paid TWICE — 153,332 µAET out of the 230 K pool to one validator. If B's transfer failed, B returns an error up the stack; A continues. Observed shape: one goroutine advances through worker + three validators, partially draining the bucket. Other goroutine tries validator d839e1ff (fourth in sort order) and finds 40,002 remaining — insufficient for 76,668 allocation. B errors.

12. **Escrow error propagates back up**: `ReleaseSettlement` returns `"escrow: settlement validator <X> for task <T>: ledger: insufficient balance: …"`. Settler wraps: `"verification_settler: release settlement: <…>"`. TV LK consumer wraps: `"tv_consensus_lk: settle round <R>: <…>"`. admitOneLogicalKey line 161 sets `rec.State = StateFailedRetryable` and persists.

13. **Task status never transitions to terminal** because the settler's final step (line 259-ish, `taskMgr.ApplyVerificationConsensusResolution`) is never reached on the erroring goroutine. If the OTHER goroutine also errored, the task's terminal-status update is never applied on this node. Task stays at `submitted` locally forever.

14. **Partial state**: worker paid (possibly twice on some nodes), three validators paid (some twice if the race interleaved in their favor), treasury NOT paid, gen-ledger NOT paid, escrow entry has `WorkerPaid=true` + three `ValidatorsPaid[vid]=true` flags set, bucket balance is 40,002 residual. Task local-status is not-terminal.

15. **Retry on next TVConsensus arrival**: any later Settlement event for round X arrives, admitOneLogicalKey loads the admission record — state is `StateFailedRetryable`, not `StateApplied`, so retry proceeds. `reserveOrLoadLogical` returns the existing record. Apply is called. `settler.Settle` pre-checks: task-terminal? no. `escrowMgr.HasSettlementStarted(taskID)`? — need to check this pre-check's implementation to see if it catches the partially-drained state. If the pre-check short-circuits based on any Paid flag set → nil returned, retry quiet. If not → settler.Settle proceeds, `escrow.ReleaseSettlement` iterates, skips worker (already paid), skips paid validators, hits validator d839e1ff again, finds the same 40,002 residual, errors again. Infinite retry loop of WARNs on every subsequent event arrival.

### 3.2 The pre-check gap

The claim in F4B §5.2.1 `TVConsensusLogicalKeyConsumer` doc was: "per-task mutex (taskMu) from legacy consumer NOT needed — per-(consumer, key) admission provides per-RoundID serialization; RoundID → TaskID is 1:1, so per-task serialization is automatic."

Empirically this is wrong at the concurrency level: `admitOneLogicalKey` has NO per-key lock (see `internal/dispatch/logical_key_admit.go:109–174` above — all writes go through `d.store.PutAdmission`, which persists to BadgerDB; there is no in-memory mutex serializing concurrent calls for the same storeKey). The StateApplied short-circuit is a POST-condition of a successful Apply, not a PRE-condition of entering Apply. Concurrent first-Applies for the same key both pass the short-circuit because neither has finished yet.

The legacy `dispatch.TVConsensusConsumer` used `taskMu sync.Map` per-task mutex exactly to serialize this. F4B removed it on the basis of an incorrect claim about admission semantics.

### 3.3 The Settlement-path concurrent-Apply (benign shape)

For `settlement_lk` (F4B §5.2.2), the same admission race happens, but the downstream ledger path is idempotent-guarded at a stronger level: `transfer.Settle` (`internal/ledger/transfer.go:297`) checks `validLedgerTransitions[entry.Settlement][state]`. On `Settled → Settled` this returns `false` and `transfer.Settle` errors `ErrInvalidTransition`. The second concurrent `Apply` errors out without mutating state. Benign log noise; no drift. F4B's reliance on ledger-level idempotency was correct for THIS path.

### 3.4 Cross-node divergence composition

For any given task, each node independently runs this race. The PER-NODE OUTCOME depends on the timing of `TVConsensus` event arrival on that specific node. Nodes that happened to process only one TVConsensus event for a round settle correctly. Nodes that processed two or more racing events may enter the partial-drain state. Different nodes have different per-task outcomes, therefore different `treasury_balance` accruals. Over 10 tasks, the permanent per-node drift becomes observable.

The settler emissions (`TransferFromBucketLabeled`) are LOCAL-ONLY — no canonical Transfer event is published, no cross-node propagation. Each node's ledger projection is independent. When some tasks fail to fully settle on some nodes, those nodes' projections diverge permanently.

---

## 4. Why F4 didn't break this — did F4 cause, worsen, or expose it?

**F4 EXPOSED this bug.** Verified via the following audit trail:

### Pre-F4 behavior (F3-B + main @ 603bd9b)

- `dispatch.TVConsensusConsumer` (content-hash) — the pre-F4 consumer — had `taskMu sync.Map` per-task mutex. See the pre-F4 file content still preserved on `feat/selection-consistency-fix` until F4C final main-merge: `internal/dispatch/tv_consensus_consumer.go:42-43`: `taskLocks sync.Map` + `taskMu()` helper at line 63. The pre-F4 Apply at line 136 acquired the per-task mutex BEFORE the pre-checks, which serialized concurrent Apply calls for the same task. Two TVConsensus events for the same task queue at the mutex; the first one completes (setting task terminal); the second one observes terminal status and returns nil.
- Settlement events had no F4B-style logical-key admission; they took the legacy `recognition.SettlementConsumer` → `settlementApp.Apply` path. `settlementApp.Apply` at `internal/settlement/applicator.go:140` checks `IsApplied(targetID)` first — guards against re-apply at the whole-Settlement granularity, not the per-recipient granularity.
- The selection-race bug (F4's original target) was present: different nodes selected different byte-distinct TVConsensus events via per-node first-event-past-the-mutex, yielding different verdicts on different nodes.

### Post-F4 behavior (what F4B changed)

- F4B §5.2.1 REPLACED `dispatch.TVConsensusConsumer` with `dispatch.TVConsensusLogicalKeyConsumer`. Per-task mutex REMOVED. The substitute "serialization" mechanism was the per-(consumer, key) admission record's StateApplied short-circuit — which is a POST condition, not a pre-condition (see §3.2).
- F4B + IM commit-13 combined: every committed event flows through `recognition.DispatcherAdmissionConsumer` → `dispatcher.Admit` → both content-hash AND logical-key admission paths. The general router increases the rate at which TVConsensus events reach the dispatcher (no recognition-layer filtering specific to TV anymore).
- Multi-emit TVConsensus events STILL arrive (five nodes, each emits once per round they finalize). On live testnet with fastpath latency variance, two or more events for the same round can be in-flight on the same destination node's bus workers simultaneously.

### Net: F4 did not CAUSE the concurrent-Apply race. The race was present in `escrow.ReleaseSettlement` pre-F4 too. But pre-F4:

1. `taskMu` in `dispatch.TVConsensusConsumer` serialized Apply calls for the same task BEFORE they reached `settler.Settle`. Concurrent Apply did not happen.
2. Even if the mutex contention timed out (which it didn't in practice), the original selection race would have chosen a single winner event per node, and the task-terminal pre-check would have idempotency-guarded retries.

F4 REMOVED the mutex on an incorrect assumption. The concurrent-Apply surface became reachable.

**F4 also EXPOSED this via additional event-arrival paths**: IM Part E.1 commit-13 (general admission router) routes every committed event through `dispatcher.Admit`. Pre-Part-E.1, only `TaskVerificationConsensusConsumer` explicitly forwarded TVConsensus events to the dispatcher (via the now-deleted SetDispatcher plumbing), with the side effect of being serialized by the recognition bus worker pool. Post-Part-E.1, events hit the dispatcher via the general router — the bus's worker pool size is 4 (`recognition.DefaultWorkers`, per startup log `workers=4`), so up to 4 events can be processing concurrently. On live testnet, bursts of fastpath-arriving TVConsensus events for the same round routinely saturate that pool.

This is a COMPOUND regression: F4B removed the mutex AND IM commit-13 increased concurrent arrival. Neither change on its own would have been enough to expose the race at observable scale; together they did.

---

## 5. Why F3-B didn't close this

F3-B's per-task mutex (`dispatch.TVConsensusConsumer.taskMu`) was specifically intended to serialize concurrent Apply calls for the same task. Quote from the existing file comment at `internal/dispatch/tv_consensus_consumer.go:95-128` (which F4 left intact for the legacy path, now unused):

> "Task-level idempotency: the dispatcher dedupes by canonical event hash, but each finalizing validator emits its own TaskVerificationConsensus event with a distinct hash, and all pass admission. Without a task-level guard, the second and subsequent events each drive a settler.Settle → ReleaseSettlement against the same escrow bucket; the per-recipient paid flags dedupe within a single event's recipient set but do not dedupe across different events with different recipient sets, so the interleaved partial drains diverge across nodes (most visibly: treasury may not receive its share because another event's validator-payout step drained the bucket first)."

F3-B got the diagnosis right and closed it for the content-hash consumer. F4B removed the closure on a faulty architectural claim. F3-B's `taskMu` was not at fault; its removal was.

**F3-B's per-task mutex + terminal-status pre-check were LOCAL-node guards, correctly scoped for LOCAL-node concurrency. They don't need to be cross-node because the bug IS per-node concurrency — each node's settler independently produces partial-drain state on its own escrow bucket.** The cross-node divergence is a downstream consequence of per-node failure, not a cross-node-coordination problem.

---

## 6. Relationship to Divergence (A) — the pre-existing 50 AET stake-state divergence

**Likely SAME mechanism**, applied to a different event type. Evidence:

- Multi-emit audit §1.4 row 20 identifies `EventTypeSettlement` as **HIGH-risk** with the note "Strong suspect for Divergence (A) (the pre-existing 50 AET stake-state divergence) because Transfer events going through OCS finalization and being settled via this path is the staking flow."
- Settlement events emitted per-node at `cmd/node/main.go:1770` (in the `engine.SetFinalizationHandler` callback) — multiple validators' emits for the same `TargetEventID`. 
- Pre-F4B, Settlement flowed through `recognition.SettlementConsumer` → `settlementApp.Apply`. `settlementApp.Apply` at line 140 is protected by the `applied` set check (IsApplied). Two concurrent calls: both see false, both proceed, first one succeeds and adds to applied set + mutates ledger, second one ALSO proceeds, ledger.Settle rejects with `ErrInvalidTransition` — the SAME benign-idempotency shape we see in §3.3 post-F4B.
- BUT if the ledger's state-transition guard were ever absent OR if the target event's apply path had any non-ledger side effect, the race would manifest as real divergence.
- The staking flow at `internal/settlement/applicator.go:323-331` calls `stakeManager.RecordCanonicalStake(...)` and `RecordCanonicalUnstake(...)`. These are NOT state-transition-guarded in the same way. Two concurrent Apply calls for a stake-related Settlement could both reach the stake-manager calls. Whether `RecordCanonicalStake` is itself idempotent needs verification — if not, the 50 AET stake-state divergence is a straight-line consequence.

**Recommended diagnostic**: trace `stakeManager.RecordCanonicalStake` / `RecordCanonicalUnstake` for idempotency on repeated calls with the same (agentID, amount). If non-idempotent, Divergence (A) is this same race at the stake layer.

Not verified in this document — requires code-reading under the cluster-is-frozen constraint. Flagged as open question §10.

---

## 7. Fix space — candidates only, no recommendation

Each option sketched below addresses the concurrent-Apply race at a different architectural layer. No recommendation; these inform the architect-session decision.

### 7.1 Per-key lock in `admitOneLogicalKey`

Add `sync.Map` of per-logical-key `*sync.Mutex` to `*Dispatcher`. Acquire the per-key mutex at the top of `admitOneLogicalKey` (after `Key(ev)` extraction, before `reserveOrLoadLogical`); release on function exit. Serializes concurrent Apply calls for the same key within a single node.

Pros: tiny surgical fix; matches the pre-F4 `taskMu` shape at a more general layer; no consumer-side changes.

Cons: (a) restores the mutex the F4B plan said wasn't needed — an explicit admission the plan claim was wrong; (b) doesn't address the cross-node settlement-effect divergence if settler.Settle itself ever had non-idempotent side-effects (it doesn't today, but the absence isn't a protocol invariant).

### 7.2 Cluster-uniform settler election

Change settlement-emission semantics so only ONE cluster-agreed node executes the settler per round, and all other nodes receive the settlement effects via canonical Transfer events (the "propagation-not-emission" model). The settler-executor is chosen cluster-uniformly (e.g., round-robin on round epoch, or the validator with lex-smallest AgentID among the agreeing set).

Pros: eliminates per-node settlement races entirely; ledger state converges via canonical Transfer event sync, not local settler runs; cluster-uniform by construction.

Cons: large architectural change; requires canonical Transfer emission from the settler (currently synthetic non-canonical via `TransferFromBucketLabeled`); introduces a new failover question (what if the elected node is down or deferred — who re-elects); breaks the invariant that every node independently projects ledger state from canonical events.

### 7.3 Canonical-derived settlement state

Make settlement effects a PURE PROJECTION from canonical Vote + attestation events, with no per-node imperative settler. Each node computes the settlement transfers deterministically from the canonical vote set (RoundState) and applies them to a canonical-derived ledger projection. The escrow "bucket" becomes a virtual ledger computed from (funding Transfer event − sum of authorized recipient Transfers implied by finalized rounds).

Pros: aligns with Serialization-2 (C-17) — canonical state derived from canonical underlying state; eliminates imperative settlement entirely; no race possible.

Cons: largest architectural change; requires re-defining "ledger state" as a pure projection; requires all fee/stake/treasury mutations to flow through the same derivation; touches the assurance fee collector + stake manager + generation ledger in addition to escrow; deepest refactor of the three candidates.

### 7.4 Explicit settlement-started canonical event

Introduce a canonical `SettlementStarted` event emitted by the dispatcher's admitOneLogicalKey BEFORE calling consumer.Apply. The dispatcher admits this event via OCS consensus. Only the node whose SettlementStarted event wins consensus runs the settler. All nodes apply the resulting effects via Transfer event sync.

Pros: preserves per-node settler execution for non-canonical bookkeeping while making the SETTLEMENT DECISION cluster-uniform; explicit canonical surface for the coordination.

Cons: adds consensus latency to every settlement; new event type; new consumer type; added admission-store volume.

### 7.5 Escrow as derived state

Same shape as 7.3 but narrower — make only the escrow a derived projection from the funding Transfer + finalized-round events, while leaving the settler imperative. The settler computes payouts from the derived escrow balance; the escrow doesn't store `WorkerPaid` etc. flags at all.

Pros: narrower change than 7.3; removes the per-recipient-paid-flag race surface.

Cons: still has the concurrent-Apply race for the TaskManager terminal-status transition and for the treasury/stake-manager side effects; doesn't fully eliminate the race surface.

### 7.6 Settlement atomicity via a ledger-level transaction

Wrap `escrow.ReleaseSettlement` in a BadgerDB transaction that locks the entire escrow-entry row while the payout loop runs. Concurrent calls serialize at the BadgerDB transaction level.

Pros: leverages existing durability machinery; small code change.

Cons: transaction-hold duration grows linearly with recipient count (validators, gen-ledger recipients); other node subsystems may queue behind it; BadgerDB's transaction model is optimistic concurrency — conflicts retry, which produces the same problem at a different layer.

---

## 8. Verification discipline implications

The F4B cross-node byte-equality harness (`internal/verification/cross_node/`) did NOT catch this bug. Two architectural reasons:

### 8.1 The harness synchronizes event delivery

`internal/verification/cross_node/testnetwork/transport.go` delivers events to nodes via `DeliverInOrderViaDispatcher` — a synchronous for-loop calling each node's dispatcher.Admit sequentially. There's no concurrent-arrival of multiple events on the same node. Each node processes events one at a time. **The concurrent-Apply race cannot surface in this harness**.

### 8.2 The harness uses isolated ledgers

Each node in the harness has its own `TransferLedger` + `Escrow` with no cross-node Transfer event propagation. This matches the production behavior of settlement (local-only mutations via `TransferFromBucketLabeled`), so it's not a mismatch per se. But the harness also has no multiple-events-arriving-concurrently path, which is the real gap.

### 8.3 What test would have caught this

A harness extension that:

1. Delivers multiple `TVConsensus` events for the same `RoundID` to a single node CONCURRENTLY (via `go consumer.Apply(ctx, ev1); go consumer.Apply(ctx, ev2)` in the same test goroutine setup).
2. Asserts: after all concurrent deliveries complete, the node's local ledger state is byte-equal to what a single delivery would have produced. Concurrent-safe property check.
3. Runs under `-race` to catch the data-race on `entry.ValidatorsPaid`/`entry.WorkerPaid` (may or may not surface depending on Go's race-detector coverage of map writes under brief lock scopes).

A harness that exercises the per-node concurrency surface would have caught the race. The F4A harness explicitly chose synchronous in-process delivery to eliminate timing variability as a source of test flakiness — the decision was sound for its stated purpose (reproduce selection-race divergence deterministically) but left a different bug class uncovered. **Founder's F4A completion gate §5 meta-finding "verification discipline — the harness tests one axis at a time; concurrent-arrival is a separate axis" applies in retrospect.**

### 8.4 A second verification-discipline gap

The harness asserts cross-node ledger byte-equality AFTER a single settlement cycle. It does not assert PER-NODE ledger byte-equality under repeated events. The F4B slice 1.2 conformance suite exercises `ApplyOncePerKey` (good), `ApplyAfterIsCompleteOnly` (good), but not `ApplyConcurrentlyOnSameKey`. The test surface matches the LK admission design intent; that design intent had the serialization gap.

**Corollary**: the F4B plan v2 §4.5's "per-(consumer, key) admission provides per-RoundID serialization" claim should have been tested with an explicit concurrent-Apply conformance test. Its absence let the claim go unchallenged.

---

## 9. F4 workstream status

**F4's LogicalKey admission fix is CORRECT and closes its own target bug** (selection-race: different nodes selecting different byte-distinct TVConsensus events as canonical). The in-process tied-weight harness proves this; the halt-trigger fires would have fired on that axis if F4B's fix were broken, and it didn't fire on that axis.

**The bug characterized in this document is UPSTREAM of F4's scope.** It is a per-node concurrency race in `escrow.ReleaseSettlement` that was present in the code since F3-B, serialized by F3-B's per-task mutex in the content-hash consumer, and EXPOSED (not caused, not worsened in any new structural way) by F4B's removal of that mutex combined with IM Part E.1's general admission router increasing event-arrival concurrency.

**F4 did not close this bug because its scope was the selection-race, not the ReleaseSettlement concurrency. The plan v2's §4.5 "per-(consumer, key) serialization" claim was a mistake that allowed the mutex removal to pass review. F4's architectural diff (LogicalKeyConsumer interface, logical-key admission path, Type E taxonomy, Serialization-2, C-17) remains sound.**

**Next workstream ("F5" or similar) should fix this bug class directly**, with the verification-discipline lesson that concurrent-arrival is a distinct test axis from cross-node-arrival and requires its own harness shape. The characterization of divergence (A) as likely the same race at the stake-manager layer (§6) means the fix should be GENERAL (apply to all per-node effect paths), not TV-consensus-specific.

---

## 10. Open questions (for architect-session review)

1. **Is `stakeManager.RecordCanonicalStake` / `RecordCanonicalUnstake` idempotent on repeated calls?** If not, Divergence (A) is this same race at the staking layer. §6 flagged; not resolved here because code-read was not the scope.

2. **Does the pre-check at `settler.Settle` line 146 (task-terminal) interact with the `HasSettlementStarted` pre-check at `tv_consensus_lk_consumer.go`?** Need to verify that once the first concurrent Apply completes SOME but not all payouts, the pre-check correctly short-circuits the next arrival before re-entering ReleaseSettlement. If the pre-check doesn't cover the partial-state, the retry loop is infinite (as observed — 17 WARNs on Node 5).

3. **Does the FailedRetryable → retry loop ever converge?** If the escrow partial-drain is permanent and retries always hit the same insufficient-balance error, the admission record stays StateFailedRetryable forever. Is this a permanent operational issue (leaks storage; never-terminating retries)? Observed: Node 5 has 17 admit-failed WARNs accumulated but the event arrival rate slowed (no new TVConsensus events for these rounds after settlement attempt window closed). If new events for the same round arrive later (never expected in normal operation, but replay / sync-repair could trigger), retries would fire.

4. **Does the `admission-schema-no-gate` fix (F4B FINDING #5) interact?** Post-F4B, the admission store rejects unknown SchemaVersion. Doesn't interact with this race, but worth confirming no unintended side effects. Preliminary read: orthogonal; the race is in-memory + in-BadgerDB entries that pass schema validation.

5. **Does the `admission-state-no-gate` fix (F4B FINDING #6) interact?** `IsKnownAdmissionState` covers StateFailedRetryable as a known state; retry loop's record-writes pass validation. Again orthogonal.

6. **For the Settlement path (§3.3), is the "cannot transition from Settled to Settled" WARN truly benign?** Needs explicit verification: after the error propagates up, is any partial state left behind (e.g., `settlementApp.applied` set membership, `stakeManager` counters)? If the order is `transfer.Settle` FIRST, then stake-manager side effect — concurrent Apply #1 sets Settled → side effect; #2 fails at Settle before reaching side effect, leaves partial state. If the order is reversed or the stake-manager call is after an early-return — different.

7. **Does the general admission router (IM commit-13) need backpressure?** Under burst load (multi-emit + fastpath peers), the worker pool dispatches events concurrently. Adding per-key serialization (fix candidate 7.1) would queue-of-one per key, bounded. Alternatively, the recognition bus could serialize per-logical-key at the BUS layer — another fix surface not listed in §7.

8. **Are there OTHER event types with the same race?** Any `LogicalKeyConsumer.Apply` that has non-idempotent side effects over multiple sub-operations is at risk. F4B has three: `TVConsensusLogicalKeyConsumer` (confirmed buggy), `SettlementLogicalKeyConsumer` (benign due to ledger state-transition guard), `TaskSettlementLogicalKeyConsumer` (no-op Apply — safe). Future Type E consumers need the per-key serialization built-in.

9. **F4 verification-discipline implications doc (§8) suggests the F4A harness missed this.** Should the F4B completion gate report be AMENDED to reflect this finding, or is it a standalone new finding? Architect decision.

10. **F4C gate status.** With this characterization, F4C's `§11.15 accept-corpus` halt-trigger is definitive: the per-node divergence is real and permanent. F4C cannot proceed on the current branch without a fix. Whether the fix belongs IN F4 (extend F4's scope) or in a sibling workstream (preserve F4's completion, start F5) is the architect-session decision.

---

**End of characterization v1. Read-only; no code changes; cluster preserved; awaiting architect review.**
