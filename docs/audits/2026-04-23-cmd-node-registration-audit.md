# cmd/node consumer registration audit

**Status**: F4B §5.1 prerequisite — completed 2026-04-23 against `feat/selection-consistency-fix` @ `e912305`.
**Plan reference**: F4 plan v2 §5.1.
**Purpose**: enumerate every consumer registration in `cmd/node/main.go`, document startup ordering, identify migration paths for the 4 consumers changing to logical-key admission (§5.2.1–§5.2.4), and flag any load-bearing ordering that the migration must preserve.

`cmd/node/main.go` is 3,364 LoC with 0.06 test ratio — the highest-regression-risk surface in the codebase (F4A quality audit §2). This audit is read-only; no code changes.

---

## 1. Startup ordering (load-bearing phases)

Sequential ordering in `startStack()`. Each phase must complete before the next begins.

| Phase | Line | Purpose |
|---|---:|---|
| `dag.LoadFromStore(s)` | 816 | Reconstruct DAG from persistence |
| `ledger.LoadTransferLedgerFromStore` | 821 | Reconstruct transfer ledger |
| `ledger.LoadGenerationLedgerFromStore` | 826 | Reconstruct generation ledger |
| `identity.LoadRegistryFromStore` | 831 | Reconstruct identity registry |
| `stakeMgr.LoadFromStore` | 898 | Reconstruct staked amounts |
| `svcReg.LoadFromStore` | 916 | Reconstruct service registry |
| `escrow.LoadFromStore` | 943 | Reconstruct in-flight escrows |
| `reputationMgr.LoadFromStore` | 960 | Reconstruct reputation state |
| `platformKeys.LoadFromStore` | 974 | Reconstruct platform API keys |
| `validator.LoadFromStore` | 1035 | Reconstruct validator registry |
| `settlementApp.LoadApplied` | 1622 | **A-1** — restore applied-settlement set BEFORE any listener can deliver events |
| Bus + consumer construction | 1796–2024 | See §2 below |
| Dispatcher construction + Register + Recover + SetDispatcher on recognition consumer | 1946–1962 | See §3 below |
| `commitBus.Start()` | 2026 | Workers begin draining the queue |
| `stack.dag.SetOnCommit(...)` | 2032 | DAG commits now emit to the bus |
| `recognition.ReplayHistoricalToBusConsumers` | 2051 | **F4A §8.1** — replay historical DAG events through the bus with source=Replay |
| `node.Start()` | 2048 | Network listener accepts inbound events |

**Load-bearing ordering properties**:

- `LoadApplied` MUST precede any path that can deliver events to consumers (F3-B invariant A-1).
- All `commitBus.Register(...)` calls MUST complete before `commitBus.Start()` — workers snapshot the consumer set at dispatch time; late registration is not tested and would race with events already in the queue.
- `SetOnCommit` MUST happen AFTER `commitBus.Start()` — otherwise commits arrive before workers are ready. Also MUST happen BEFORE `node.Start()` — otherwise remote events enter the DAG without bus notification (the "no commit events dispatched" production bug that surfaced in F3-B).
- `ReplayHistoricalToBusConsumers` MUST happen AFTER `SetOnCommit` (bus is wired) and BEFORE `node.Start()` (to avoid interleaving historical and live events, breaking the replay topological-order invariant).
- `Dispatcher.Recover` MUST happen BEFORE the first `Admit` can be called. Currently it is called at line 1957 before the bus is started (line 2026), so no live events can trigger Admit prior to Recover completing.
- `tvConsensusConsumer.SetDispatcher(eventDispatcher)` MUST happen BEFORE `commitBus.Register(tvConsensusConsumer)` — otherwise a live event arriving after `commitBus.Start()` could fire Consume with `c.dispatcher == nil`, taking the F3-B legacy direct-settler path. Currently line 1961 (SetDispatcher) precedes line 1964 (Register).

---

## 2. Bus consumer registrations (11 total)

Listed in registration order. All current consumers use the recognition bus' `CommitConsumer` interface. None currently use logical-key admission — all recognition-layer routing is content-agnostic; admission-strategy distinctions live in the dispatcher layer.

| # | Line | Consumer | Event types handled | Has dispatcher routing? | Logical-key migration target? |
|---:|---:|---|---|---|---|
| 1 | 1802 | `recognition.NewOCSSubmitConsumer(stack.engine)` | Transfer, Generation, TaskSettlement → OCS pending | No | No (routes to OCS engine, not through dispatcher) |
| 2 | 1806 | `recognition.NewOCSVoteConsumer(stack.engine)` | VerificationVote → AcceptPeerVote | No | No |
| 3 | 1820 | `recognition.NewTaskLifecycleConsumer(stack.taskMgr)` | TaskPosted/Claimed/Submitted/Approved/Disputed | No | No |
| 4 | 1828 | `recognition.NewEvidenceReadinessConsumer(stack.taskMgr, nil)` | TaskSubmitted → evidence readiness | No | No |
| 5 | 1856 | `recognition.NewTaskVerificationRoundConsumer(...)` | TaskSubmitted → open TVRound | No | No (opens rounds; not the canonical-effect path) |
| 6 | 1894 | `recognition.NewTaskVerificationVoteConsumer(...)` | TaskVerificationVote → aggregate + trigger finalize | No | No |
| 7 | 1964 | `recognition.NewTaskVerificationConsensusConsumer(...)` | TaskVerificationConsensus → routes to dispatcher via `SetDispatcher`-injected ref | **YES — tvConsensusConsumer.SetDispatcher at line 1961** | **YES — §5.2.1 target (see §4.1 below)** |
| 8 | 1978 | `epoch.NewRoundCountConsumer(roundCounter)` | TaskVerificationConsensus → increment epoch counter | No | No (node-local projection; non-canonical) |
| 9 | 2023 | `recognition.NewSettlementConsumer(settlementConsumerAdapter)` | Settlement → settlementApp.Apply | No (direct call today) | **YES — §5.2.2 + §5.2.4 target (see §4.2)** |
| 10 | 2369 | `blobsync.NewBlobDemandConsumer(...)` | All event types → extract BlobRefs → fetch | No | No |
| 11 | (not yet registered in default startStack; conditional on blob subsystem) | RoundProgress consumer chain | Various | No | No |

---

## 3. Dispatcher consumer registrations (1 currently)

Lines 1946–1962.

| Slot | Consumer | Strategy today | Migration target? |
|---|---|---|---|
| 1 | `dispatch.NewTVConsensusConsumer(tvSettler, tvStore, stack.taskMgr, stack.escrowMgr)` | Content-hash | **YES — replaced by logical-key variant in §5.2.1** |

The dispatcher has one consumer registered today. After F4B Part D, TVConsensusConsumer's Apply path will run as a `LogicalKeyConsumer` with `RoundID` as the key. Two migration shapes are possible:

- **(A)** Remove the content-hash `dispatch.TVConsensusConsumer`, add a new `dispatch.TVConsensusLogicalKeyConsumer` via `eventDispatcher.RegisterLogicalKey(...)`. The content-hash path never fires for TVConsensus events after migration.
- **(B)** Keep both: content-hash fires first (admits per-event), logical-key fires in parallel (admits per-key). Both run independently per the dispatcher's dual-path design (slice 1.2 at `internal/dispatch/dispatcher.go` `Admit`). The content-hash path would still hit the per-task mutex selection race; the logical-key path would dedupe correctly. Cross-node divergence would persist because the content-hash path's Apply would run first on each node and set task terminal status before the logical-key path completed.

**(A) is the only correct migration.** Both paths wired simultaneously would not fix the selection race — the content-hash path's early-fire would still cause divergence. The audit recommends (A) exclusively.

---

## 4. Migration paths per plan §5.2

### 4.1 §5.2.1 — TaskVerificationConsensus → RoundID logical-key

**Current**:
- Recognition bus: `tvConsensusConsumer` (`internal/recognition/task_verification_consensus_consumer.go`) — handles round state, calibration, slashing projection; routes canonical effect via `dispatcher.Admit`.
- Dispatcher: `tvDispatchConsumer` (`internal/dispatch/tv_consensus_consumer.go`) — content-hash admission; Apply calls `settler.Settle` through the per-task mutex.

**After migration**:
- Recognition bus: `tvConsensusConsumer` UNCHANGED. Still handles per-node-side-effects (round state, calibration, slashing). Still routes via `dispatcher.Admit`. The recognition consumer layer is not a canonical-effect path; it remains content-agnostic.
- Dispatcher: `tvDispatchConsumer` REMOVED from `eventDispatcher.Register(...)`. REPLACED by a new `dispatch.TVConsensusLogicalKeyConsumer` wired via `eventDispatcher.RegisterLogicalKey(...)`. New consumer:
  - `Name()` = `"tv_consensus_settlement_lk"` (distinct from old content-hash consumer's `"tv_consensus_settlement"`)
  - `Interested(ev)` = `ev.Type == EventTypeTaskVerificationConsensus`
  - `Key(ev)` = extract `RoundID` from payload
  - `RoundState(ctx, key)` = fetch round from `tvStore.LoadRound(ctx, roundID)`; surface `Votes` field
  - `IsComplete(rs)` = supermajority seal per plan §4.7 (`passWeight >= threshold && passWeight > failWeight + maxRemaining` or symmetric for fail)
  - `DeriveOutcome(rs)` = compute verdict + scoreBP + participating validators from the canonical vote set
  - `Apply(ctx, key, outcome)` = invoke `settler.Settle` with the derived outcome, not the triggering event's payload (C-17)

**Dependencies available at registration site** (line 1946):
- `tvStore` (line 1833) — BadgerStore for `LoadRound` in `RoundState`
- `tvSettler` (line 1929) — for `Apply`
- `stack.taskMgr` (line 1817) — for task-terminal pre-check
- `stack.escrowMgr` (line 943) — for escrow pre-check and settlement release
- `activeWeightFn` (line 1862) — for supermajority threshold computation
- `stack.roundCounter` (line 1972) — for epoch (unused in TVConsensus but available)

All dependencies are in scope at the migration site. **No startup-ordering change required.**

### 4.2 §5.2.2 — Settlement → TargetEventID logical-key + §5.2.4 — SettlementConsumer refactor

**Current**:
- Recognition bus: `settlementConsumer` (line 2023) — adapter wraps `settlementApp.Apply`. Direct invocation; no dispatcher routing.

**After migration** (per plan §5.2.4):
- Recognition bus: `settlementConsumer` REMOVED from direct-invocation path. Replaced by:
  - A new logical-key dispatcher consumer (`settlement_lk`) registered via `eventDispatcher.RegisterLogicalKey(...)` with `TargetEventID` as key.
  - The recognition-side adapter becomes a thin shim that routes Settlement events via `dispatcher.Admit` (mirror of how TVConsensusConsumer currently works). OR: the recognition consumer is removed entirely and the bus no longer sees Settlement events as a separate consumer registration — the dispatcher is the only path.
- **Recommended shape**: parallel to TVConsensus — keep a recognition consumer shim that routes via Admit. Rationale: preserves the "no canonical effects outside the dispatcher" invariant (C-10), and the shim can do any per-node bookkeeping that needs to happen regardless of dispatcher outcome.

**Dependencies available**:
- `settlementApp` (line 1620) — for `Apply`
- Canonical attestation set — fetched from the DAG; needs a helper analogous to `settlement.LookupEscrowLockTransfer` (line 1658), to find attestations referencing the target event. TBD whether this helper exists today.

**Non-trivial dependency**: the attestation query. If today's Settlement path assumes single-canonical-event-per-target, the attestation-set query may not have an efficient implementation. This is flagged for §5.2.2 to resolve; out of scope for §5.2.1.

### 4.3 §5.2.3 — TaskSettlement → TaskID logical-key

**Current**: TaskSettlement events flow through the `settlementApp.SetTaskSettler(...)` callback (line 1630). This is invoked by the settlement applicator when a Settlement event with a task-settler payload arrives. Not a bus consumer; it's an injected callback in the settlement applicator.

**After migration**:
- The callback becomes a logical-key dispatcher consumer with `TaskID` as key.
- `IsComplete` = derived from the task's verification round outcome (itself logical-key-admitted per §5.2.1, so cluster-uniform)
- The migration fires only after §5.2.1 lands — TaskSettlement outcomes depend on TVConsensus outcomes.

### 4.4 Ordering constraint across §5.2.1 → §5.2.2 → §5.2.3 → §5.2.4

- §5.2.1 MUST land first. Part D's halt-trigger fires on §5.2.1's dispatcher-integrated tied-weight harness run; no downstream migration makes sense if §5.2.1 doesn't close the bug.
- §5.2.2 / §5.2.4 can land together (same surface).
- §5.2.3 lands AFTER §5.2.1 (TaskSettlement outcome derives from TVConsensus outcome).
- All four must complete before F4C (integer-migration merge + testnet deploy).

---

## 5. Load-bearing dependencies the migration MUST preserve

| # | Dependency | Location | Why preserve |
|---:|---|---|---|
| 1 | Dispatcher.Register BEFORE Recover | 1953, 1957 | Recover scans admission records; consumers must be registered so orphan-consumer warnings fire correctly |
| 2 | Dispatcher.Recover BEFORE SetDispatcher | 1957, 1961 | Any deferred records in non-terminal state must be processed before the recognition consumer starts admitting live events |
| 3 | SetDispatcher BEFORE bus.Register(tvConsensusConsumer) | 1961, 1964 | Live events arriving after bus.Start() would race; without SetDispatcher, the recognition consumer takes the legacy direct-settler path (the bypass F3-B closed) |
| 4 | bus.Register(*) BEFORE bus.Start() | 1802–2023, 2026 | Late registration races with dispatch |
| 5 | SetOnCommit AFTER bus.Start() | 2026, 2032 | Commits fire into a running bus |
| 6 | ReplayHistoricalToBusConsumers AFTER SetOnCommit, BEFORE node.Start() | 2032, 2051, 2048 | F4A §8.1 replay-path invariant |

**None of these dependencies are broken by the §5.2.1 migration.** The only registration-site change is:

```go
// BEFORE:
tvDispatchConsumer := dispatch.NewTVConsensusConsumer(tvSettler, tvStore, stack.taskMgr, stack.escrowMgr)
if err := eventDispatcher.Register(tvDispatchConsumer); err != nil { ... }

// AFTER:
tvDispatchLKConsumer := dispatch.NewTVConsensusLogicalKeyConsumer(
    tvSettler, tvStore, stack.taskMgr, stack.escrowMgr, activeWeightFn,
)
if err := eventDispatcher.RegisterLogicalKey(tvDispatchLKConsumer); err != nil { ... }
```

The `Register` call becomes `RegisterLogicalKey`. The surrounding ordering (`Recover`, `SetDispatcher`, `bus.Register`) stays the same.

---

## 6. Pre-migration checklist

Items that MUST hold for §5.2.1 to be safe. Each is satisfied today unless noted.

- [x] `dispatch.RegisterLogicalKey` exists and validates cross-kind name uniqueness — **YES** (landed in slice 1.2)
- [x] `LogicalKeyConsumer` interface defined and tested — **YES** (landed in slice 1.2)
- [x] Admission store schema supports Strategy + LogicalKey fields — **YES** (v2 schema landed in slice 1.2)
- [x] `admitLogicalKey` path operational in dispatcher — **YES** (landed in slice 1.2)
- [x] Dispatcher-integrated harness reproduces bug on current code — **YES** (F4B step 0.1 baseline)
- [x] Performance baseline captured — **YES** (F4B step 0.3)
- [x] FINDINGs #5/#6 coupling gates in place — **YES** (slice 1.1)
- [ ] `tvStore.LoadRound` usable for `RoundState` fetching inside the dispatcher's Admit path — **needs check**: the dispatcher's Admit runs synchronously from the recognition consumer's `Consume` (goroutine from the bus worker pool). `LoadRound` reads from BadgerDB — blocking but bounded. Should be fine; no known lock-reentrancy issues (the recognition consumer does not hold any TaskManager locks during Admit).
- [ ] Supermajority threshold + active weight are cluster-uniform at the moment Admit fires — **needs check**: `activeWeightFn` closes over `stack.lifecycleReducer.Snapshot()`, which is a snapshot of validator registry state. Each node computes its own snapshot from local state. If two nodes disagree on active weight at the same round, `IsComplete` could diverge. However, active weight derives from validator registrations in the DAG, which is cluster-uniform. The snapshot's freshness may vary per node but converges to the same value.

Two **needs-check** items flagged. Neither is a blocker, but both need explicit verification during the §5.2.1 implementation. If either fails, halt-and-surface.

---

## 7. Out of scope for this audit

- Code changes — none; audit is read-only.
- Implementation decisions beyond the migration shape (e.g., the exact `Outcome` field layout for TVConsensus — delegated to §5.2.1).
- Part D §5.2.2 / §5.2.3 / §5.2.4 detailed migration paths — covered at the required detail for gating order; not for implementation.
- cmd/node refactoring — `cmd/node/main.go`'s 3,364 LoC and `startStack` function's 1,511 LoC are flagged in the F4A quality audit. Neither is touched by Part D.

---

**End of cmd/node registration audit.**
