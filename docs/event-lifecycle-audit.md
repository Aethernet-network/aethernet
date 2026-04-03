# AetherNet Event Lifecycle & Cross-Node Recognition Audit

**Date:** 2026-04-02
**Scope:** Complete lifecycle trace of every event type from creation to cross-node subsystem recognition
**Status:** READ-ONLY investigation. No code changes.

---

## Part 1: Event Creation Paths

There are **five distinct paths** by which events enter the DAG. Each path has different notification semantics.

### Path 1: Local Emission via localpub.Publisher

**Entry point:** `Publisher.Publish(ev)` in `internal/localpub/publisher.go:107-127`

**Callers (14 production call sites):**
- `cmd/node/main.go:1236` — genesis funding
- `cmd/node/main.go:1305` — registration
- `cmd/node/main.go:1652` — settlement event (finalization handler)
- `internal/api/server.go:1351,2200,2374,3751` — HTTP API operations
- `internal/autovalidator/auto.go:813` — task settlement
- `internal/autovalidator/auto.go:1133` — verification vote
- `internal/protocol/client.go:137` — transfer
- `internal/trajectory/service.go:271` — trajectory commit

**Exact sequence:**
```
Publisher.Publish(ev)
  1. dag.Add(ev)                         → persists to DAG + BadgerDB
  2. disseminator.SubmitLocalEvent(ev)    → Fast Path V2 ingest pipeline
  3. disseminator.Broadcast(ev)           → legacy V1 MsgEvent to all peers
```

**Notification behavior:** After `dag.Add` succeeds, the event is in the local DAG. The two disseminator calls propagate to remote peers. **No local subsystem notification fires.** The caller (autovalidator, API server, etc.) is responsible for any local side effects.

**Critical detail:** When the autovalidator emits a vote at `auto.go:1132-1160`, after Publish it **immediately** calls `engine.ProcessVote()` at line 1154. This is the ONLY mechanism by which a locally-emitted vote reaches the OCS consensus engine. Without this explicit call, the vote would exist in the DAG but the OCS would never learn about it.

```go
// auto.go:1131-1160 — vote emission
if av.publisher != nil {
    if err := av.publisher.Publish(voteEvent); err != nil { ... }
} else {
    if err := av.dag.Add(voteEvent); err != nil { ... }
}
// Register the vote with the OCS engine
_ = av.engine.ProcessVote(ocs.VerificationResult{
    EventID:       targetEventID,
    VerifierID:    av.validatorID,
    Verdict:       verdict == string(settlement.VerdictAccepted),
    VerifiedValue: verifiedValue,
    Timestamp:     time.Now(),
})
```

### Path 2: Fast Path Body Completion (Remote V2 Peers)

**Entry point:** `materializeEvent(id)` in `internal/network/materialize.go:39-106`

**Called from:** `materializeWorker()` which drains `validateQ` (line 24-36).

**Exact sequence:**
```
materializeWorker drains validateQ
  → materializeEvent(id)
    1. n.ingest.GetReconstructedEvent(id)   → get validated event
    2. n.dag.Add(ev)                        → persists to DAG
    3. n.syncHandler(ev)                    → routes event to subsystems
    4. n.ingest.MarkMaterialized(id)        → advances tracking stage
```

**Notification behavior:** `syncHandler(ev)` fires after `dag.Add` succeeds (`materialize.go:89-95`). This is the same handler that fires for all remote paths.

### Path 3: Legacy Sync Batch (Remote V1 Peers)

**Entry point:** `handleMessage` MsgSyncBatch case in `internal/network/node.go:1242-1301`

**Exact sequence:**
```
handleMessage receives MsgSyncBatch
  → JSON unmarshal SyncBatchPayload
  → sort by CausalTimestamp
  → verify signatures
  → multi-pass insertion loop:
      for each event:
        1. n.dag.Add(e)                   → persists to DAG
        2. if sh != nil: sh(e)            → routes to subsystems
```

**Code** (`node.go:1276-1287`):
```go
for _, e := range pending {
    if err := n.dag.Add(e); err == nil {
        added++
        progress = true
        if sh != nil {
            sh(e)
        }
    }
}
```

### Path 4: Legacy V1 Single Event

**Entry point:** `handleMessage` MsgEvent case in `internal/network/node.go:1207-1233`

**Exact sequence:**
```
handleMessage receives MsgEvent
  → JSON unmarshal event
  → crypto.VerifyEvent check
  1. n.dag.Add(&e)
  2. if sh != nil: sh(&e)
```

### Path 5: Repair Path (Gap Recovery)

**Entry point:** `handleRepairResponse` in `internal/network/repair.go:266-321`

**Exact sequence:**
```
handleRepairResponse receives RepairResponse
  → sort by CausalTimestamp
  → for each event:
      1. crypto.VerifyEvent(ev)
      2. n.dag.Add(ev)
      3. if sh != nil: sh(ev)             → routes to subsystems
      4. n.retryBlockedChildren(ev.ID)     → unblocks dependent events
```

### Path 6: DAG Replay from Persistence (Startup)

**Entry point:** `dag.LoadFromStore(s)` in `internal/dag/dag.go:210-234`

**Exact sequence:**
```
LoadFromStore(s)
  → s.AllEvents()                          → read all persisted events
  → topoSort(events)                       → Kahn's algorithm
  → for each sorted event:
      d.addFromStore(e)                    → inserts without signature re-check
```

**Critical notification behavior:** `LoadFromStore` uses `addFromStore` (not `Add`), which skips signature verification for performance. **No syncHandler fires during replay.** Subsystems must reconstruct their state independently:
- **Validator lifecycle:** Reconstructed via `replayLifecycleEventsFromDAG()` at `cmd/node/main.go:1108-1117`
- **OCS pending items:** Restored via `LoadPendingFromStore()` at `cmd/node/main.go:764`
- **Settlement applied set:** Restored via the `Applicator.applied` map and store
- **Task state:** Restored via `tasks.LoadTaskManagerFromStore()` at `cmd/node/main.go:839`
- **Votes in consensus rounds:** **NOT restored from DAG** — only from VotingRound persistence

---

## Part 2: Event Materialization

### Convergence Point

There is **no single convergence function**. There are **two convergence points** with different notification semantics:

1. **`dag.Add(ev)`** — the authoritative state mutation. Defined at `internal/dag/dag.go:150-203`. Every path calls this. It stores the event, updates tips, writes to persistence. **It has ZERO callbacks or hooks.**

2. **`syncHandler(ev)`** — the subsystem notification callback. Defined as a field on `Node` at `internal/network/node.go:228-232`. Registered at `cmd/node/main.go:1669`. Fires after `dag.Add` on all **remote** paths (Fast Path materialization, legacy sync, repair). **Does NOT fire on local emission or DAG replay.**

### What syncHandler Does

Registered at `cmd/node/main.go:1669-1788`, it routes by event type:

| Event Type | Subsystem Call | File:Line |
|---|---|---|
| Transfer, Generation, TaskSettlement | `engine.SubmitFromSync(ev)` → adds to OCS pending | main.go:1672 |
| VerificationVote | `engine.AcceptPeerVote(...)` → registers in consensus round | main.go:1685 |
| Settlement | `settlementApp.Apply(&sp)` → mutates ledger | main.go:1696 |
| Registration | `reg.Register(fp)` + `stakeManager.Stake()` | main.go:1714 |
| GenesisFunding | `transfer.TransferFromBucket(...)` | main.go:1746 |
| TaskPosted/Claimed/Submitted/Approved/Disputed | `taskMgr.ApplyDAGEvent(ev)` | main.go:1768 |
| Validator lifecycle (12 types) | `applyLifecycleEventFromSync(ev, reducer, round)` | main.go:1786 |

### Local vs Remote Asymmetry

This is the fundamental architectural pattern:

- **Local emission:** The caller is responsible for all side effects. `Publisher.Publish()` does `dag.Add` + disseminate. The caller then explicitly calls whatever subsystem needs to know (e.g., `engine.ProcessVote` for votes, `engine.Submit` for transfers).

- **Remote arrival:** `syncHandler` centralizes routing. After `dag.Add`, the handler routes by event type to the appropriate subsystem.

- **DAG replay:** Neither mechanism fires. Each subsystem has its own startup restoration path.

---

## Part 3: The OCS (Ordering/Consensus Service)

### Package Structure

All files in `internal/ocs/`:

| File | Description |
|---|---|
| `engine.go` | Core OCS engine: Submit, ProcessVote, AcceptPeerVote, ProcessResult, expiry sweep |
| `consensus_test.go` | BFT consensus integration tests |
| `engine_test.go` | Engine lifecycle and functional tests |
| `finalization_test.go` | Finalization handler exactly-once tests |
| `supply_test.go` | Supply invariant tests |

### Data Model

**Engine struct** (`engine.go:194-242`):
```go
type Engine struct {
    config           *EngineConfig
    transfer         *ledger.TransferLedger
    generation       *ledger.GenerationLedger
    identity         *identity.Registry
    eventBus         *eventbus.Bus
    nodeMetrics      *metrics.AetherNetMetrics
    voting           *consensus.VotingRound         // BFT consensus
    broadcastVote    func(eventID, verdict, voterID) // P2P vote propagation
    onFinalized      func(targetID, verdict, value, order) // settlement trigger
    pending          map[event.EventID]*PendingItem  // in-flight events
    processed        map[event.EventID]struct{}       // idempotency guard
    processedAt      map[event.EventID]time.Time      // GC timestamps
    results          chan VerificationResult
    store            ocsPersistence
    mu               sync.RWMutex
}
```

**PendingItem** (`engine.go:125-151`): EventID, EventType, AgentID (economic actor), Amount, RecipientID, OptimisticAt, Deadline.

### How Events Become Pending

**Local submission:** `engine.Submit(ev)` at `engine.go:493-567`
- Validates stake >= MinStakeRequired
- For Transfers: `BalanceCheck()` (no ledger mutation)
- Creates PendingItem with current time + VerificationTimeout deadline
- **Only Transfer, Generation supported** (+ TaskSettlement via SubmitFromSync)

**Remote submission:** `engine.SubmitFromSync(ev)` at `engine.go:569-638`
- Same as Submit but idempotent (returns nil if already pending/processed)
- Filters to Transfer, Generation, TaskSettlement types
- Only processes events with `SettlementState == Optimistic`

### Vote Discovery — THE CRITICAL SECTION

#### How the OCS learns about votes

There are **two parallel vote delivery paths** plus **one dead path**:

**Path A — Local autovalidator vote (WORKS):**
```
autovalidator.processPending()                    [auto.go:929]
  → autovalidator.emitVote(targetEventID, ...)    [auto.go:1098]
    → publisher.Publish(voteEvent)                [auto.go:1133] → DAG + disseminate
    → engine.ProcessVote(result)                  [auto.go:1154] → consensus round
      → processVoteInternal(result, broadcast=true)
        → voting.RegisterVote(...)                → tally
        → broadcastVote(...)                      → MsgVote to peers
        → check finalization → onFinalized()
```

**Path B — Remote MsgVote wire message (WORKS for MsgVote):**
```
peer sends MsgVote
  → node.handleMessage → MsgVote case           [node.go:1378]
    → authenticate signature                     [node.go:1384]
    → verify voter eligibility                   [node.go:1396]
    → n.voteHandler(vp.VoterID, vp.EventID, vp.Verdict)  [node.go:1472]
      → engine.AcceptPeerVote(eventID, voterID, verdict)
        → processVoteInternal(result, broadcast=false)
          → voting.RegisterVote(...)             → tally
          → check finalization → onFinalized()
```

**Path C — Remote VerificationVote DAG event via syncHandler (WORKS):**
```
remote VerificationVote arrives via Fast Path / legacy sync / repair
  → dag.Add(ev)
  → syncHandler(ev)                               [main.go:1674]
    → parse VerificationVotePayload
    → engine.AcceptPeerVote(targetID, voterID, verdict)  [main.go:1685]
      → processVoteInternal(result, broadcast=false)
        → voting.RegisterVote(...)                → tally
        → check finalization → onFinalized()
```

#### The Dual-Path Design

Votes propagate via **two independent channels**:
1. **MsgVote wire messages** (immediate, via P2P vote broadcast)
2. **VerificationVote DAG events** (via standard DAG sync)

Both paths converge at `AcceptPeerVote()`. The MsgVote path is faster (direct P2P message), while the DAG event path is more durable (persisted, synced via repair).

#### voted_count / Vote Tracking

The `voting` field on Engine is a `*consensus.VotingRound` (`internal/consensus/voting.go`). Each pending event has a `VoteRecord` containing:
- `Votes map[crypto.AgentID]bool` — per-voter verdicts
- `TotalWeight`, `YesWeight`, `NoWeight` — computed during tally
- `TotalActiveWeight` — from bound validator snapshot
- `Finalized bool`, `FinalVerdict`, `FinalVerifiedValue`, `FinalOrder`

#### DAG Scanning for Votes

**The OCS does NOT scan the DAG for votes.** It relies entirely on:
- `ProcessVote()` being called explicitly by the local autovalidator
- `AcceptPeerVote()` being called by the syncHandler or voteHandler

There is no periodic DAG scan, no subscription, no callback from `dag.Add`.

### BFT Threshold & Finalization

**Supermajority formula** (`consensus/voting.go:571-580`):
```go
yesRatio := float64(yesWeight) / float64(denominator)
if yesRatio >= vr.config.SupermajorityThreshold {  // 0.667
    record.Finalized = true
    record.FinalVerdict = true
    record.FinalVerifiedValue = computeWeightedMedian(record)
}
```

Where:
- `denominator = record.TotalActiveWeight` (from bound validator snapshot) or `totalWeight` (fallback)
- Weight per voter: `(ReputationScore * StakedAmount) / 10000`
- Rejection threshold: `noRatio > (1 - 0.667) = 0.333`

**Finalization trigger:** Every call to `RegisterVote()` immediately calls `tallyVotesLocked()`. If the tally crosses the supermajority threshold, `processVoteInternal` detects this via `MarkCallbackFired()` (exactly-once guard) and fires `onFinalized()`.

**Expiry mechanism:** Background goroutine calls `checkExpired()` every 5 seconds (`engine.go:477`). Events where `now - OptimisticAt > Deadline` are removed with `Verdict=false`. Default `VerificationTimeout` is 30 seconds.

### The Gap (if any)

Based on this investigation, **all three vote delivery paths are wired**. Remote votes reach the OCS via:
1. MsgVote → voteHandler → AcceptPeerVote
2. DAG sync → syncHandler → AcceptPeerVote

The potential issue is **timing and ordering**:
- A Transfer event must be in `engine.pending` before votes for it can be tallied
- If the Transfer arrives via syncHandler, `SubmitFromSync` adds it to pending
- If a vote arrives before the target event, `RegisterVote` creates a VoteRecord even without a PendingItem — BUT when finalization fires, `ProcessResult` requires the event to be in `pending` (returns `ErrNotPending` at `engine.go:680`)

This means: **if a VerificationVote DAG event arrives and is processed by syncHandler BEFORE the target Transfer/Generation event arrives and is processed by syncHandler, the vote is registered in the consensus round but finalization cannot fire because ProcessResult will fail with ErrNotPending.** The event would then expire after 30 seconds.

However, because DAG causal ordering means the target event is always a parent or predecessor of the vote event, and `dag.Add` enforces causal reference integrity, the target event should always be in the DAG before the vote. The syncHandler processes events in causal order (Fast Path materializes in pipeline order; legacy sync sorts by CausalTimestamp). So this race should not occur in practice.

---

## Part 4: The Autovalidator

### Vote Emission

**Decision:** `processPending()` at `auto.go:929-966` iterates `engine.Pending()`, checks `av.voted` for deduplication, calls `verifyPendingEvent()` for each, then `emitVote()`.

**emitVote()** at `auto.go:1098-1161`:
1. Creates `VerificationVotePayload` with TargetEventID, VoterID, Verdict, VerifiedValue
2. Creates `event.New(EventTypeVerificationVote, tips, payload, ...)`
3. Signs with `crypto.SignEvent(voteEvent, av.kp)`
4. `publisher.Publish(voteEvent)` → dag.Add + disseminate
5. **Immediately calls** `engine.ProcessVote(result)` → consensus round registration

**Event type:** `event.EventTypeVerificationVote`

**Fields identifying target:** `TargetEventID string` in `settlement.VerificationVotePayload`

### Task Scoring

**Polling-based:** `processSubmittedTasks()` at `auto.go:405` is called every tick (5 seconds in production). It calls `taskMgr.Search(TaskStatusSubmitted, "", 0)` to find all submitted tasks, filters by staleness cutoff, then scores each.

**EvidenceReady gate** (`auto.go:414-421`):
```go
if task.EvidenceBodyHash != "" && !task.EvidenceReady {
    slog.Debug("auto-validator: skipping task — evidence not ready", ...)
    continue
}
```

---

## Part 5: The TaskManager

### Event Application

**Mechanism:** The TaskManager learns about task events exclusively through `ApplyDAGEvent(ev)` at `tasks.go:1045-1082`. This is called from:
1. `syncHandler` in `cmd/node/main.go:1768` — for remote events
2. **NOT called for local events** — local task state changes happen via direct method calls (PostTask, ClaimTask, SubmitResult, etc.)

**Local vs remote:** Local task operations are applied directly by the API server calling TaskManager methods. Remote task events arrive via syncHandler → `ApplyDAGEvent`. Both paths are idempotent — if the local operation already applied the state change, `ApplyDAGEvent` skips it.

**On restart:** Task state is restored from persistence via `tasks.LoadTaskManagerFromStore(s)` at `cmd/node/main.go:839`. No DAG replay needed — task state is independently persisted.

---

## Part 6: The Settlement Layer

### SetFinalizationHandler

**Defined:** `engine.go:289-309`
**Signature:** `func(targetID event.EventID, verdict bool, verifiedValue uint64, finalOrder uint64)`
**Registered:** `cmd/node/main.go:1587-1662`

**Complete finalization path:**
```
processVoteInternal detects supermajority (engine.go:375-390)
  → MarkCallbackFired() returns true (exactly-once)
  → ProcessResult() clears from pending, updates metrics
  → onFinalized(targetID, verdict, verifiedValue, finalOrder)
    → main.go:1587 handler:
      → settlementApp.IsApplied(targetID) check (idempotency)
      → votingRound.GetRecord(targetID) for attestations
      → event.New(EventTypeSettlement, ...) with SettlementPayload
      → pub.Publish(settlementEvent) → dag.Add + disseminate
      → settlementApp.Apply(&sp) → LEDGER MUTATION
```

**Settlement on remote nodes:**
```
Settlement DAG event arrives via sync
  → dag.Add(ev)
  → syncHandler(ev) → EventTypeSettlement case
    → settlementApp.Apply(&sp) → LEDGER MUTATION
```

---

## Part 7: Existing Notification/Callback Infrastructure

### Complete Catalog

#### 1. syncHandler (Node-level)
- **Defined:** `internal/network/node.go:228` (field), `:652` (setter)
- **Registered:** `cmd/node/main.go:1669`
- **Fired by:** materializeEvent, MsgEvent handler, MsgSyncBatch handler, handleRepairResponse
- **Data:** `func(ev *event.Event)` — receives the full event after dag.Add
- **NOT fired by:** local Publisher.Publish, DAG replay

#### 2. voteHandler (Node-level)
- **Defined:** `internal/network/node.go:192` (field), `:614` (setter)
- **Registered:** `cmd/node/main.go:1796`
- **Fired by:** MsgVote handler in handleMessage (`node.go:1472`)
- **Data:** `func(voterID, eventID, verdict)`
- **Purpose:** Routes authenticated MsgVote wire messages to OCS

#### 3. broadcastVote (OCS-level)
- **Defined:** `internal/ocs/engine.go:224` (field), `:285` (setter)
- **Registered:** `cmd/node/main.go:1793`
- **Fired by:** `processVoteInternal` when `broadcast=true` (local votes only)
- **Data:** `func(eventID, verdict, voterID)`
- **Purpose:** Propagates local votes to peers via MsgVote

#### 4. onFinalized (OCS-level)
- **Defined:** `internal/ocs/engine.go:236` (field), `:289` (setter)
- **Registered:** `cmd/node/main.go:1587`
- **Fired by:** `processVoteInternal` after MarkCallbackFired (exactly-once)
- **Data:** `func(targetID, verdict, verifiedValue, finalOrder)`
- **Purpose:** Creates Settlement DAG event and applies to ledger

#### 5. eventBus (Infrastructure)
- **Defined:** `internal/eventbus/bus.go`
- **Publishers:** OCS engine (`engine.go:705`), SettlementApplicator (`applicator.go:258`), API server (multiple endpoints)
- **Subscribers:** WebSocket handler (`api/websocket.go:34`)
- **Data:** `eventbus.Event{Type, Timestamp, Data map[string]any}`
- **Purpose:** Real-time UI streaming only. NOT used for core event routing.

#### 6. evidenceBlobFetcher (TaskManager)
- **Defined:** `internal/tasks/tasks.go:298` (field), `:337` (setter)
- **Registered:** `cmd/node/main.go` (wired with blobstore.Get)
- **Purpose:** Fetches evidence blob on TaskSubmitted application

#### 7. PendingItem results channel
- **Defined:** `internal/ocs/engine.go:234` — `results chan VerificationResult`
- **Purpose:** Async verification result ingestion (capacity 256)
- **Used by:** background goroutine in engine.Start

#### Patterns NOT present:
- No `dag.OnAdd` or `dag.Subscribe` mechanism
- No global event bus for core routing
- No channel-based event flow between subsystems
- No observer/listener pattern on DAG mutations
- No pubsub for internal event propagation

---

## Part 8: Cross-Cutting Analysis

### 1. The Convergence Question

**No, there is no single point.** The architecture has two distinct notification needs:

a) **Remote event routing** — already handled by `syncHandler`, which fires after every remote `dag.Add`. Adding a callback to `dag.Add` itself would give a single convergence point, but it would also fire during DAG replay (requiring idempotency guards on every subscriber) and during local emission (causing double-notification since the caller already handles local side effects).

b) **Local event routing** — currently handled ad-hoc by each caller. The autovalidator calls `engine.ProcessVote` after publishing. The API server calls `engine.Submit` after publishing. There is no unified "post-publish" hook.

The most impactful single change would be: **add an optional callback list to `dag.Add`** that fires after successful insertion. This would unify paths (a) and (b) and eliminate the need for syncHandler + caller-side notification. BUT it requires careful design to handle replay correctly.

### 2. The Existing Infrastructure Question

**The codebase is 90% there.** The `syncHandler` already does exactly what's needed for remote events — it routes every event type to the correct subsystem. The gap is that `syncHandler` is only wired to fire from the network layer, not from `dag.Add` itself. If `dag.Add` had a post-commit hook, `syncHandler` could be registered once and would fire for ALL paths.

The `eventbus` infrastructure is also underutilized — it currently only serves WebSocket streaming, but its pub/sub architecture could serve internal routing if performance permits.

### 3. The Performance Question

At 278 events/second:
- **Hot paths:** `dag.Add` (mutex-protected, ~microseconds), `syncHandler` routing switch (nanoseconds), `processVoteInternal` with tally (microseconds for vote counting)
- **Safe to add notification:** A simple function callback on `dag.Add` would add ~100ns per event. At 278/sec, that's 28 microseconds/second — negligible.
- **Dangerous to add:** Anything that holds `dag.mu` while doing I/O, anything that iterates all events on every add, anything that blocks on channel sends in the hot path.

### 4. The Consistency Question

**Yes, ordering matters.** A vote notification MUST arrive after the voted-on event is fully materialized. This is naturally guaranteed by DAG causal ordering — the vote event references the target event as a causal ancestor, so `dag.Add(vote)` will fail with `ErrMissingCausalRef` if the target is not yet in the DAG.

However, `engine.SubmitFromSync` (which adds the target to OCS pending) and `engine.AcceptPeerVote` (which registers the vote) are called sequentially from `syncHandler` for different events. There is no explicit ordering guarantee that the target's `SubmitFromSync` completes before the vote's `AcceptPeerVote`. In practice, DAG causal ordering ensures the target event arrives first, but this is an implicit rather than explicit guarantee.

### 5. The Idempotency Question

**Mostly correct, with one gap:**

- **OCS pending:** Restored from store via `LoadPendingFromStore`. Idempotent.
- **Settlement applied set:** Restored from store. `IsApplied` prevents double-application.
- **Task state:** Restored from store via `LoadTaskManagerFromStore`. Idempotent.
- **Validator lifecycle:** Replayed from DAG via `replayLifecycleEventsFromDAG`. Deterministic.
- **Consensus votes:** Stored in VotingRound persistence. `RegisterVote` is idempotent (rejects duplicates).

**Gap:** If a node crashes after `dag.Add(vote)` succeeds but before `engine.ProcessVote` is called (local vote path), the vote is in the DAG but not in the consensus round. On restart, the vote will NOT be replayed to the OCS because there is no mechanism to scan the DAG for VerificationVote events and re-register them. The OCS restores its pending items from store, but not its vote state from DAG. However, since `VotingRound` has its own persistence (`persistence.PutVotes`), this is mitigated.

### 6. The Scale Question

**10x (2,780 events/sec):** No issues. All paths are O(1) per event.

**100x (27,800 events/sec):** `dag.Add` mutex contention becomes significant. The single global `dag.mu` lock serializes all insertions. The `syncHandler` is called under no lock but processes synchronously — a slow subsystem (e.g., `SubmitFromSync` doing `BalanceCheck`) would block the handler.

**1000x (278,000 events/sec):** The `dag.events` map (in-memory) grows to millions of entries. `dag.All()` and `dag.Tips()` become expensive. The `engine.pending` map under `engine.mu` becomes a contention point. The `voting.mu` lock in VotingRound serializes all vote tallies.

**First bottleneck:** The DAG's single global mutex. At high throughput, reader-writer contention between `Add` (write lock) and `Get`/`Tips` (read lock) will serialize.

### 7. The Innovation Question

**The `syncHandler` is more powerful than it's being used for.**

Currently, `syncHandler` is a simple `func(ev *event.Event)` callback that fires after remote events are added to the DAG. It is registered once and routes all event types. But it's only wired to the network layer.

**The insight:** If `dag.Add` itself had a post-commit callback (or if `syncHandler` were renamed to `onEventCommitted` and wired to fire from ALL paths including local emission), it would become a **universal event bus for the entire system**. Every subsystem could register interest, every event path would be covered, and the architecture would shift from "caller is responsible for side effects" to "the DAG is the source of truth and notifies all observers."

This would:
1. Eliminate the local vs. remote asymmetry
2. Make DAG replay automatically re-notify all subsystems (with idempotency guards)
3. Allow new subsystems to be added without modifying every caller
4. Provide a single integration test surface (fire an event, verify all subsystems react)

**The `eventbus` package is also underutilized.** It has a clean pub/sub interface with type-filtered subscriptions and bounded channels. Currently it only serves WebSocket clients, but it could serve as the notification backbone if adapted for internal use (adding event.Event as a payload type, not just map[string]any).

**The `ProcessVote` / `AcceptPeerVote` split is elegant.** The `broadcast` parameter controls whether the vote propagates to peers. This cleanly prevents vote echo storms. The same pattern could be applied to a general `onEventCommitted` callback: `commitLocal(ev, propagate=true)` vs `commitRemote(ev, propagate=false)`.

---

## Observations

### Surprising

1. **`dag.Add` has zero callbacks.** The most fundamental state mutation in the system has no hook mechanism. Every consumer must be explicitly wired. This was a deliberate design choice (simplicity, testability) but creates the asymmetry between local and remote paths.

2. **Votes travel two independent paths simultaneously** — as MsgVote wire messages AND as VerificationVote DAG events. Both converge at `AcceptPeerVote`. This is redundant but resilient — if one path fails, the other delivers.

3. **The autovalidator explicitly calls `engine.ProcessVote` after publishing a vote** (`auto.go:1154`). This is the ONLY way a locally-emitted vote enters the OCS. Without this line, the vote would exist in the DAG, propagate to peers, and be counted by their OCS engines — but never by the emitting node's own OCS.

### Fragile

1. **The absence of a `dag.OnAdd` hook means every new event type requires modifying the syncHandler in `cmd/node/main.go`.** Miss one, and remote events of that type are silently dropped. The switch statement at lines 1669-1788 is a 120-line routing table that must be kept in sync with every new event type.

2. **Startup ordering is critical and implicit.** The comment at `main.go:1462-1467` documents that `av.Start()` must be called AFTER `SetFinalizationHandler`. This ordering is enforced only by line order in a 2000+ line function, not by type system or dependency injection.

3. **`SubmitFromSync` filters on `SettlementState == Optimistic`** (`engine.go:577`). If a remote event arrives with a different settlement state (e.g., already settled from the remote node's perspective), it will be silently skipped. This is correct for the current protocol but fragile if settlement state semantics change.

### More Powerful Than Currently Used

1. **The `syncHandler` pattern.** A single callback registered once that routes all event types. If elevated from "network layer callback" to "dag post-commit hook," it solves cross-node recognition for all subsystems.

2. **The `eventbus` package.** A well-implemented pub/sub with filtering, bounding, and subscription management. Currently only for WebSocket streaming. Could be the internal notification backbone.

3. **The causal DAG itself.** Because `dag.Add` enforces causal reference integrity, the ordering of events is guaranteed by construction. This means a "fire callbacks after Add" mechanism would automatically deliver events in causal order — votes after their targets, settlements after their votes, etc. No separate ordering logic needed.

4. **The SettlementApplicator's deferred reconciliation pattern** (`applicator.go` — retries deferred settlements every 30 seconds). This pattern of "try now, defer if not ready, retry later" could be generalized for any subsystem that needs to react to events whose dependencies haven't arrived yet.

### Pay Attention To

1. **The local emission gap:** When the autovalidator publishes a vote, it must explicitly call `engine.ProcessVote`. When the API server publishes a transfer, it must explicitly call `engine.Submit`. If any caller forgets this step, events are in the DAG but invisible to the OCS. A `dag.OnAdd` hook would eliminate this entire class of bugs.

2. **The replay gap:** On restart, DAG events are loaded but syncHandler doesn't fire. Each subsystem has its own restoration path. If a new subsystem is added and forgets to implement startup restoration, it will have no state after restart until new events arrive.

3. **The VerificationVote → AcceptPeerVote path in syncHandler** (`main.go:1674-1689`). This is the key integration point for cross-node vote recognition. It parses the vote payload and calls `AcceptPeerVote`. If this parsing fails (e.g., payload format change), remote votes would silently stop being counted.
