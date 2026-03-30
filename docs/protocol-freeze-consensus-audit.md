# Protocol Freeze — Phase 2 Audit: Consensus Rules

**Date:** 2026-03-29
**Scope:** Complete inventory of voting, finalization, OCS integration, validator snapshots, committee selection, and edge cases.
**Prerequisite:** Phase 1 (event format) is complete — all payloads have schema versions, no floats, JCS canonicalization on EventID.

---

## Task 1: Voting Weight Computation

### Formula

```
weight(agent) = (ReputationScore * StakedAmount) / 10000
```

**Source:** `internal/consensus/voting.go:30-36` (package doc), `voting.go:360-393` (`computeWeight` and `computeWeightFromRegistry`).

ReputationScore is in basis points (0-10000). StakedAmount is in micro-AET. Division by 10000 normalizes reputation to [0, 1], so effective weight scales linearly with stake, dampened by track record.

Computation uses `math/big` to avoid uint64 overflow: `(big.Int(rep) * big.Int(stake)) / big.Int(10000)`. If the result exceeds uint64 max, it saturates at `^uint64(0)` (`voting.go:389-391`).

### Data Source: Two Paths

**Primary (snapshot path):** When a `ValidatorSetSource` is bound via `SetValidatorSet()`, weight is read directly from the validator seat snapshot (`voting.go:362-367`). The snapshot's `VoteWeightByKey()` method (`validatorlifecycle/snapshot.go:133-154`) performs a linear scan of seats, returning the pre-computed `seat.Weight` for any seat where:
- The seat's `OperatorKey` matches the voter's AgentID
- `seat.Status.IsParticipating()` is true (Active or Probationary)
- `seat.EffectiveFromVersion <= snapshot.Version`

**Fallback (registry path):** When no snapshot is bound, falls back to the identity registry (`voting.go:370-393`). Reads `ReputationScore` and `StakedAmount` from the registered `identity.Fingerprint` and applies the formula directly.

### Zero Weight Conditions

A voter has zero weight (and is effectively excluded) when:

| Condition | Path | Code reference |
|---|---|---|
| Not in snapshot at all | Snapshot | `snapshot.go:149` — returns `0, false` |
| Seat is not participating (Suspended, CoolingDown, Exited, Excluded, PendingJoin) | Snapshot | `snapshot.go:142-144` — `!seat.Status.IsParticipating()` returns `0, false` |
| Seat's EffectiveFromVersion > snapshot version | Snapshot | `snapshot.go:147-149` — prevents retroactive participation |
| Seat has zero weight | Snapshot | `snapshot.go:145` — `seat.Weight == 0` returns `0, false` |
| Agent not registered | Registry | `voting.go:378-380` — returns error |
| ReputationScore == 0 | Registry | `voting.go:381-383` — returns 0 |
| StakedAmount == 0 | Registry | `voting.go:381-383` — returns 0 |

### Special Cases

**Genesis validators:** Enter Active status immediately (bypass PendingJoin and Probationary). Their EffectiveFromVersion is set to 1 (the initial version). They have full weight from the first snapshot. (`validatorlifecycle/events.go:370-391`, `reducuer.go` applyJoin with `IsGenesis: true`).

**Probationary validators:** Participate with half weight. When a validator is reinstated from Suspended → Probationary, their weight is set to `StakeAmount / 2` (`reducer.go` applyReinstate).

**Bootstrap mode (MinParticipants=1):** In single-node testnet, `AETHERNET_CONSENSUS_MIN_PARTICIPANTS=1` (`cmd/node/main.go:755-770`). A single validator with any positive weight can finalize immediately, preserving backward compatibility with pre-consensus direct settlement.

---

## Task 2: Quorum and Finalization

### Threshold

**Supermajority threshold:** `YesWeight / TotalWeight >= 0.667` (approximately 2/3).

**Source:** `voting.go:524-527` (tallyVotesLocked), configured in `ConsensusConfig.SupermajorityThreshold` (default 0.667 at `voting.go:231`).

### What the Threshold Is Computed Over

The threshold is computed over **only the weight of votes actually received**, NOT total possible weight in the network. Specifically:

```go
var totalWeight, yesWeight uint64
for voterID, yesVote := range record.Votes {
    w, err := vr.computeWeight(voterID)
    if err != nil { continue }  // voter disappeared — excluded
    totalWeight += w
    if yesVote { yesWeight += w }
}
// ...
ratio := float64(yesWeight) / float64(totalWeight)
```

**Source:** `voting.go:503-527`

This means `TotalWeight` is the sum of weights of voters who actually voted, not the sum of all eligible validators. A unanimous vote from 2 out of 100 validators would show `ratio = 1.0`, satisfying the threshold — but would be blocked by `MinParticipants` if fewer than 3 (or the configured minimum) voted.

### Single Vote Finalization

**Yes, finalization can happen with 1 vote** when `MinParticipants = 1` (single-node testnet). The condition at `voting.go:524` is:

```go
if numVoters >= vr.config.MinParticipants && totalWeight > 0 {
```

With `MinParticipants=1`, a single vote from a validator with positive weight (ratio = 1.0 >= 0.667) triggers finalization.

With the default `MinParticipants=3`, at least 3 distinct voters must participate regardless of weight.

### Inclusive vs Exclusive Threshold

**Inclusive.** The check is `ratio >= vr.config.SupermajorityThreshold` (`voting.go:526`). Exactly 2/3 (0.667) satisfies the threshold.

### Timeout Behavior

**VotingRound itself does NOT enforce timeouts.** The `ConsensusConfig.RoundTimeout` field (`voting.go:218-220`) exists for external orchestrators but is not used by VotingRound internally.

The **OCS engine's checkExpired** (`ocs/engine.go:734-810`) is the actual timeout mechanism. It fires every 5 seconds (`expiredCheckInterval`), sweeping pending items whose `OptimisticAt + Deadline` has passed. Default `VerificationTimeout` is 30 seconds (`ocs/engine.go:188`).

When an item expires:
- **With consensus (voting != nil):** If votes exist but no supermajority, a simple head-count majority decides: `yesCount > noCount → accept, otherwise reject` (`engine.go:786-799`). If no votes at all, conservative reject (`engine.go:766-776`).
- **Without consensus:** Always reject with "verification deadline exceeded" (`engine.go:802-807`).

### Round Exhaustion

If a tally doesn't reach supermajority, `record.Round++` (`voting.go:543`). When `Round > MaxRounds` (default 10), subsequent `RegisterVote` calls return `ErrRoundExhausted` (`voting.go:443-446`). The event is permanently failed — `Finalized` stays false, `FinalOrder` stays 0. **However:** in practice, the OCS expiry sweep will resolve the event via majority or rejection before 10 rounds elapse, since each `RegisterVote` triggers an immediate tally (each vote = 1 round increment).

### Reversibility

**Finalization cannot be reversed.** Once `record.Finalized = true` (`voting.go:527`), the field never reverts. The comment at `voting.go:193` confirms: "Once true it never reverts." Subsequent `RegisterVote` calls return `ErrAlreadyFinalized` (`voting.go:440-442`). Subsequent `tallyVotesLocked` calls are no-ops (`voting.go:493-495`).

### Exact Code Path

```
Vote arrives (DAG sync or P2P MsgVote)
  → engine.AcceptPeerVote() or engine.ProcessVote()        [ocs/engine.go:329/322]
    → engine.processVoteInternal()                           [ocs/engine.go:341]
      → votingRound.RegisterVote(eventID, voterID, vote)    [voting.go:403]
        → computeWeight(voterID) — validate eligibility      [voting.go:406]
        → committeeSource.SelectForRound() — if set          [voting.go:414-418]
        → vr.mu.Lock()                                       [voting.go:421]
        → Create VoteRecord if first vote                    [voting.go:424-438]
        → Check Finalized, RoundExhausted, DuplicateVote    [voting.go:440-449]
        → record.Votes[voterID] = vote                       [voting.go:451]
        → persistence.PutVote() — if set                     [voting.go:455-462]
        → tallyVotesLocked(record)                           [voting.go:464]
          → Recompute totalWeight, yesWeight from votes      [voting.go:503-516]
          → Check numVoters >= MinParticipants               [voting.go:524]
          → Check yesWeight/totalWeight >= 0.667             [voting.go:526]
          → If both: record.Finalized = true, assign FinalOrder [voting.go:527-529]
          → Delete persisted votes                            [voting.go:532-534]
      → voting.IsFinalized(eventID) — check if we just finalized [engine.go:364]
      → If finalized: read VoteRecord, compute consensusVerdict  [engine.go:370-375]
      → engine.ProcessResult() — clear from OCS pending          [engine.go:378-385]
      → engine.onFinalized() — fire finalization handler         [engine.go:391-393]
        → cmd/node/main.go:1563 — creates Settlement DAG event
        → settlementApp.Apply() — mutates ledger                [applicator.go:137]
```

---

## Task 3: OCS Engine Integration

### OCS ↔ VotingRound Interaction

The OCS Engine holds an optional `*consensus.VotingRound` (`ocs/engine.go:215`). When set (via `SetConsensus`), verdicts route through reputation-weighted BFT consensus. When nil, `ProcessVote` delegates directly to `ProcessResult` for single-node backward compatibility (`engine.go:342-346`).

### SetFinalizationHandler

**When it fires:** Immediately after `processVoteInternal` detects that `voting.IsFinalized(eventID)` returns true (`engine.go:364-393`). This can be triggered by any vote — local or peer — that pushes the tally over the supermajority threshold.

**Parameters received:**
- `targetID event.EventID` — the event that was finalized
- `verdict bool` — true if accepted (supermajority yes)
- `verifiedValue uint64` — from the triggering vote's `VerifiedValue`
- `finalOrder uint64` — monotonic finalization sequence from `VoteRecord.FinalOrder`

**Source:** `engine.go:296-309` (SetFinalizationHandler), `engine.go:391-393` (invocation).

### Expiry/Sweeping Behavior

**Background goroutine:** `engine.run()` (`engine.go:462-484`) drains the results channel and fires `checkExpired()` every `CheckInterval` (default 5s, `engine.go:180`).

**Pending item lifetime:** `VerificationTimeout` (default 30s, `engine.go:188`). An item expires when `time.Now() - item.OptimisticAt > item.Deadline`.

**On expiry without consensus:**
- **No votes at all:** Conservative reject (`engine.go:766-776`).
- **Has votes, not finalized:** Simple head-count majority: `yesCount > noCount → accept, else reject` (`engine.go:786-799`). This is a **fallback degradation** — NOT the consensus threshold. It allows the network to make progress on stalled items.
- **Already finalized:** Silently skipped — idempotency via `processed` set (`engine.go:779-783`).

**On expiry without consensus engine (voting == nil):** Always reject with "verification deadline exceeded" (`engine.go:802-807`).

### Exactly-Once Finalization

**Double-finalization prevention has two layers:**

1. **VotingRound level:** Once `record.Finalized = true`, subsequent `RegisterVote` calls return `ErrAlreadyFinalized`. `tallyVotesLocked` is a no-op for finalized records. The finalization handler is invoked from `processVoteInternal` only when `IsFinalized` returns true after the latest `RegisterVote`.

2. **OCS Engine level:** The `processed` map (`engine.go:232`) tracks event IDs that have been through `ProcessResult`. Duplicate calls return nil silently (`engine.go:674-677`).

3. **Settlement Applicator level:** `IsApplied(targetID)` check (`cmd/node/main.go:1572-1574`) in the finalization handler callback, plus `applied` map in `Applicator.Apply()` (`applicator.go:140-146`).

**However, there is a subtle race:** Multiple vote delivery paths (local `ProcessVote`, peer `AcceptPeerVote`, DAG sync handler) can all trigger `processVoteInternal` concurrently. The first to call `RegisterVote` that triggers finalization will invoke `onFinalized`. But if a second path checks `IsFinalized` shortly after (before the first path completes), it could also see `finalized = true` and invoke `onFinalized` again. The `IsApplied` check in the callback handles this, but **`onFinalized` itself may fire more than once for the same event**. The callback must be idempotent.

---

## Task 4: Validator Seat Snapshots

### Snapshot Creation and Versioning

**Reducer** (`validatorlifecycle/reducer.go:72-75`) is a deterministic state machine maintaining `seats map[ValidatorID]*ValidatorSeat` and a monotonic `version ValidatorSetVersion`. Every successful `Apply()` that changes the active validator set increments `version`.

**Snapshot()** (`validatorlifecycle/snapshot.go:18-36`) creates an immutable point-in-time copy. Only seats with `Status.IsParticipating() && EffectiveFromVersion <= r.version` are included in `TotalActiveWeight` and `ActiveSeatCount`.

**Version assignment:** The snapshot's `Version` field equals `r.version` at the time `Snapshot()` is called.

### EffectiveFromVersion

Each lifecycle event handler computes `nextVersion = r.version + 1` and assigns `seat.EffectiveFromVersion = nextVersion` before incrementing `r.version`. This means:

- A validator that joins at version N has `EffectiveFromVersion = N`
- Any snapshot taken at version < N will **exclude** this validator from participation
- The snapshot evaluation (`snapshot.go:147-149`): `if seat.EffectiveFromVersion > vs.Version { return 0, false }`

**This prevents retroactive corruption:** A validator cannot vote in rounds that were opened before they joined.

### When Changes Take Effect

| Event | Status transition | When effective |
|---|---|---|
| Join | → PendingJoin | Not participating until activated |
| Activate | PendingJoin → Active | Next snapshot after activation |
| Suspend | Active → Suspended | Immediately excluded from next snapshot |
| Resume | Suspended → Probationary | Next snapshot, at half weight |
| Exit (begin cooldown) | Active → CoolingDown | Immediately excluded |
| Exit (complete) | CoolingDown → Exited | Already excluded |
| Key Rotate | No status change | New key effective at next version |
| Slash | → Suspended or Excluded | Immediately excluded |

### Can a Snapshot Change During an Active Voting Round?

**The snapshot bound to VotingRound is immutable.** `SetValidatorSet()` replaces the pointer atomically (`voting.go:291-294`), but existing `VoteRecord` entries capture `ValidatorSetVersion` at creation time (`voting.go:433-436`). All votes for an event are evaluated against the snapshot that was current when the first vote for that event arrived.

**However:** The `ValidatorSetVersion` on the `VoteRecord` is informational only — it is not used to look up a specific historical snapshot. The actual weight computation uses whatever snapshot is currently bound to the VotingRound (`computeWeight` at `voting.go:360-372`). If `SetValidatorSet` is called with a new snapshot between votes, subsequent tallies for already-open rounds will use the new snapshot's weights.

**This is a gap** — see Task 7.

### Votes from Removed Validators

If a validator was in the snapshot when the round started but has since been removed (new snapshot bound), their vote is silently excluded from the tally. In `tallyVotesLocked` (`voting.go:504-511`):

```go
w, err := vr.computeWeight(voterID)
if err != nil {
    continue // voter disappeared — silently excluded
}
```

Their vote remains in `record.Votes` but contributes zero weight. This is deterministic: all correct nodes with the same snapshot will skip the same missing voter.

---

## Task 5: Committee Selection

### Selection Algorithm

**SHA-256 based deterministic sortition** (`validatorlifecycle/committee.go:107-146`).

For each participating seat, a sortition score is computed:

```
score = SHA-256(roundID || seatID || version_bytes)
```

Where `version_bytes` is the snapshot version as 8-byte big-endian uint64. Seats are sorted by score ascending; the first `MaxSize` seats are selected.

### Inputs

- `roundID` (string) — typically the EventID of the event being voted on
- `snap.Version` — the validator set version number
- Set of participating seats (Active or Probationary with `EffectiveFromVersion <= version`)

### Determinism

**Yes, fully deterministic.** SHA-256 is deterministic; the inputs are canonical (EventID is content-addressed, version is monotonic, seatIDs are derived from join events). Same inputs → same committee on every node.

### Committee Size

- **Small network path:** If `len(participating) <= policy.MaxSize` (default 21), all participating seats are included.
- **Large network path:** Exactly `policy.MaxSize` seats are selected via sortition.
- **Minimum:** `policy.MinSize` (default 3). If fewer than MinSize participating seats exist, all are included (no minimum enforcement at selection time — the MinParticipants check in VotingRound handles this).

**Default policy** (`committee.go:28-33`): MinSize=3, MaxSize=21.

### Offline Committee Members

**No special handling.** If all selected committee members are offline and never vote, the event will eventually expire via the OCS checkExpired sweep (30s default timeout). At expiry, the head-count majority of whatever votes arrived decides — or if zero votes, conservative reject.

There is no mechanism to detect that a committee member is offline and re-select. The timeout is the only backstop.

---

## Task 6: Edge Cases and Failure Modes

### 1. Network Partition

**Scenario:** Two groups of validators each have < 2/3 weight.

**Behavior:** Neither group can reach the 0.667 supermajority threshold. Finalization does not happen on either side. Events remain pending. After 30 seconds (VerificationTimeout), the OCS expiry sweep fires on each partition. Each partition independently applies head-count majority of whatever votes it received.

**Risk:** The two partitions can reach **different finalization decisions** for the same event — one accepts, the other rejects. When the partition heals and DAGs merge, both Settlement events will be in the DAG. The Settlement Applicator's idempotency (`applied` map keyed by TargetEventID) means the first Settlement applied wins; the second is silently skipped. **But "first applied" may differ across nodes depending on DAG sync order.**

**Verdict:** This is a **known BFT limitation** — safety is only guaranteed when honest nodes control > 2/3 of weight. The 30s expiry with head-count majority fallback is a **liveness optimization** that sacrifices safety under partition. This must be documented as a known trade-off.

### 2. Duplicate Vote

**Behavior:** `RegisterVote` returns `ErrDuplicateVote` and the second vote is rejected (`voting.go:447-449`). The map `record.Votes[voterID]` stores only one vote per voter. The first vote wins; there is no mechanism to change a vote.

**Tested:** `TestRegisterVote_DuplicateVote` (`voting_test.go:134`).

### 3. Late Vote (After Finalization)

**Behavior:** `RegisterVote` returns `ErrAlreadyFinalized` and the vote is rejected (`voting.go:440-442`). No side effects. The finalized state is immutable.

**Tested:** Implicitly by any test that registers votes after finalization check — the `ErrAlreadyFinalized` sentinel is tested.

### 4. Key Rotation Mid-Round

**Scenario:** Validator casts vote with key K1, then rotates to K2.

**Behavior:** The vote was registered under AgentID K1 in `record.Votes`. On subsequent tallies, `computeWeight(K1)` is called. If the new snapshot maps the seat to K2 instead of K1, then K1 will return `ErrVoterNotInSnapshot` and the vote is **silently excluded from weight computation** (`voting.go:505-511`).

**Consequence:** The validator's vote effectively disappears after key rotation. The round may lose a previously counted vote. This is the correct security behavior (old keys should not retain authority), but it can reduce the tally below supermajority for already-open rounds.

### 5. All Validators Offline

**Behavior:** No votes arrive. The event remains pending for 30 seconds. The OCS expiry sweep fires, sees zero votes (`engine.go:764-776`), and conservatively rejects: "consensus: timeout with no votes". The pending item is removed and `ProcessResult(verdict=false)` is called.

### 6. Byzantine Validator (Fraudulent Approval)

**Detection:** AetherNet does not have an automated Byzantine fault detection mechanism within the consensus layer itself. The `SlashEngine` (`internal/validator/slashing.go`) can apply slashes for specific offenses (FraudulentApproval, DishonestReplay, Collusion), but these must be **triggered externally** — there is no protocol-level monitor that automatically detects and reports fraudulent votes.

**Slashing trigger:** The `Slash()` method (`slashing.go:146`) is called by operator-level or governance-level code. The penalty is computed from config parameters (`SlashFraudulentApproval`, `SlashDishonestReplay`, `SlashCollusion`) and applied via `registry.ApplySlash`.

**Post-slash effects:** The validator's stake is reduced, they are suspended or permanently excluded, and a `ValidatorSlashApplied` DAG event is emitted to propagate the state change.

### 7. MinParticipants Not Met

**Behavior:** The supermajority check at `voting.go:524` requires `numVoters >= vr.config.MinParticipants`. If fewer validators vote, finalization does not occur. The round counter increments on each tally (`voting.go:543`). After `MaxRounds` (10 by default) tallies, `RegisterVote` returns `ErrRoundExhausted`.

**In practice:** The OCS expiry sweep (30s timeout) will fire before 10 rounds elapse (since each RegisterVote = 1 tally = 1 round increment, and votes must arrive within 30s). At expiry, the event is resolved by head-count majority of available votes or rejected if zero votes.

**The round does not hang forever** — the OCS timeout is the backstop.

---

## Task 7: Gap Analysis

### GAPS THAT BLOCK PROTOCOL FREEZE

**1. Tally computes weight over received votes only, not total eligible weight**

The supermajority threshold is `yesWeight / totalWeight >= 0.667` where `totalWeight` is the sum of weights of voters who actually cast votes (`voting.go:503-516`). This means 2 validators unanimously agreeing satisfies the threshold even if 100 validators are eligible — as long as `MinParticipants` is met.

This is **not standard BFT**. In PBFT and Tendermint, the threshold is over total possible weight (all active validators), ensuring that a quorum represents a genuine supermajority of the network. The current design means a small subset of validators can finalize if others don't participate.

**Risk:** With `MinParticipants=2` (3-node testnet), 2 out of 3 validators can finalize. With `MinParticipants=3` and 100 validators, just 3 validators unanimously agreeing would finalize. This is a liveness optimization but a safety regression for larger networks.

**Recommendation:** For protocol freeze, the threshold should be computed over total active weight from the snapshot: `yesWeight / snapshot.TotalActiveWeight >= 0.667`. Add a `TotalActiveWeight` field to `VoteRecord` captured from the snapshot at round-open time.

**2. Snapshot can change mid-round**

`computeWeight` reads the current snapshot via `vr.validatorSet.VoteWeightByKey()` (`voting.go:362-367`). If `SetValidatorSet()` is called between votes for the same event, different tallies for the same round use different weight functions. Two nodes that received the same votes but switched snapshots at different times could compute different tally results.

The `ValidatorSetVersion` captured on `VoteRecord` (`voting.go:433-436`) is informational — it does not bind the tally to a specific snapshot.

**Recommendation:** Either (a) bind each VoteRecord to a specific snapshot and use that snapshot for all tallies of that record, or (b) ensure `SetValidatorSet` is never called while any un-finalized VoteRecords exist (which is impractical).

**3. Expiry sweep uses head-count majority, not weighted supermajority**

When an event expires with votes but no supermajority, `checkExpired` uses a simple `yesCount > noCount` head-count (`engine.go:786-799`). This ignores weight entirely — a low-weight validator's vote counts equally to a high-weight validator's. This contradicts the BFT model where weight reflects stake and reputation.

**Recommendation:** Use weighted majority (or reject if weighted supermajority not reached) for expired events. The current approach is a liveness hack that weakens safety.

**4. `onFinalized` callback can fire multiple times**

The `processVoteInternal` method calls `onFinalized` whenever `IsFinalized` returns true (`engine.go:391-393`). Multiple concurrent vote delivery paths can each see finalized=true and invoke the callback. The callback must be idempotent (the current implementation is, via `IsApplied` check), but the contract is undocumented and fragile.

**Recommendation:** Move the finalization check and callback invocation inside the `RegisterVote` method itself, under the VotingRound's mutex, so that the callback fires exactly once. Alternatively, add a `finalizedFired` flag to VoteRecord.

**5. `verifiedValue` in finalization handler comes from triggering vote, not consensus**

When `onFinalized` fires (`engine.go:391-393`), `result.VerifiedValue` is the value from the **single vote that triggered finalization**, not a consensus-aggregated value. If different validators submit different `VerifiedValue`s, the final settlement uses whichever vote happened to push the tally over the threshold.

**Recommendation:** Aggregate `VerifiedValue` from all votes (e.g., median or stake-weighted average) rather than taking the triggering vote's value.

### RISKS FOR MAINNET

**1. Scalability of linear scan in VoteWeightByKey**

`VoteWeightByKey` (`snapshot.go:133-154`) performs a linear scan of all seats to find a matching `OperatorKey`. With 1000+ validators, this becomes O(N) per vote per tally. Should be replaced with a `map[AgentID]*ValidatorSeat` index.

**2. No equivocation detection**

The protocol rejects duplicate votes (`ErrDuplicateVote`) but does not detect or report **equivocation** — a validator signing conflicting votes for the same event. In other BFT protocols, equivocation is cryptographic proof of Byzantine behavior and triggers automatic slashing. AetherNet's duplicate rejection means the second conflicting vote is simply dropped, with no record or penalty.

**3. Head-count majority on expiry enables economic attacks**

An attacker with many low-stake validators could flood votes to influence the head-count majority used on expiry. Since expiry ignores weight, N cheap validators each with 1 µAET stake could outvote 1 honest validator with 1,000,000 µAET stake.

**4. Potential deadlock: synchronous settlement in finalization handler**

The finalization handler (`cmd/node/main.go:1563-1638`) performs synchronous DAG operations (Tips, event creation, Publish, Apply) while the OCS engine's `processVoteInternal` holds no lock but is called from the background goroutine. If Publish blocks (e.g., waiting for DAG lock), and the DAG is waiting for an event that requires OCS settlement, a circular wait could occur. Not observed in testing but architecturally possible under load.

**5. No view-change or leader rotation**

Classic BFT protocols (PBFT, Tendermint) have view-change mechanisms when the leader fails. AetherNet's virtual voting has no concept of a leader, which eliminates leader-based failures but means there is no mechanism to force progress when the network is stuck (beyond the OCS expiry fallback).

### THINGS ALREADY SOLID

**1. Deterministic weight computation**

The weight formula `(rep * stake) / 10000` using `math/big` is overflow-safe, deterministic, and identical across all nodes. The snapshot path eliminates races from registry updates during tally. **Well-tested:** 6 weight computation tests including overflow and large values.

**2. Duplicate vote rejection**

`ErrDuplicateVote` is returned for same-voter same-event votes (`voting.go:447-449`). The vote map uses `crypto.AgentID` as key, preventing any form of double-counting. **Tested:** `TestRegisterVote_DuplicateVote`.

**3. Immutable finalization**

Once `record.Finalized = true`, it never reverts. Late votes are rejected with `ErrAlreadyFinalized`. The `FinalOrder` monotonic counter establishes a total order over finalized events. **Tested:** Multiple tests verify finalization persistence.

**4. Three-layer idempotency for settlement**

Settlement has triple protection against double-application:
- OCS `processed` map (`engine.go:674-677`)
- Finalization handler `IsApplied` check (`cmd/node/main.go:1572-1574`)
- Applicator `applied` map with crash-recovery persistence (`applicator.go:140-146`, `applicator.go:277-279`)

**5. Committee selection determinism**

SHA-256 sortition with canonical inputs (EventID + seatID + version) produces identical committees on every node. The small-network optimization (include all seats when count <= MaxSize) avoids unnecessary filtering for testnets. **Tested:** `TestSnapshot_SelectCommittee_Deterministic`.

**6. EffectiveFromVersion prevents retroactive corruption**

The `EffectiveFromVersion` mechanism on validator seats ensures that newly joined validators cannot retroactively participate in already-open consensus rounds. The invariant is enforced at both the Reducer level (set on apply) and the Snapshot level (filtered on creation). **Tested across multiple lifecycle tests.**

**7. Vote persistence survives node restarts**

Votes are written through to `VotePersistence` on every `RegisterVote` (`voting.go:455-462`) and deleted on finalization (`voting.go:532-534`). `LoadPersistedVotes` replays pending votes on restart (`voting.go:317-335`). **This ensures consensus rounds survive node restarts without losing in-progress votes.**

**8. Comprehensive test coverage**

- 28 tests in `voting_test.go` covering weight, registration, tally, concurrency, snapshots, and committees
- 23 tests in `engine_test.go` covering submit, expiry, double-settlement, and concurrency
- 27 tests across snapshot and eligibility test files
- Edge cases (duplicate vote, single voter, round exhaustion, expiry) all have dedicated tests

---

## Summary of Action Items (Priority Order)

| # | Action | Severity | Effort |
|---|---|---|---|
| 1 | Compute supermajority over total active weight, not just received-vote weight | **Blocks freeze** | Medium |
| 2 | Bind VoteRecord tally to the snapshot version captured at round-open time | **Blocks freeze** | Medium |
| 3 | Replace head-count majority on expiry with weighted majority or reject | **Blocks freeze** | Low |
| 4 | Ensure onFinalized fires exactly once (move into mutex or add flag) | **Blocks freeze** | Low |
| 5 | Aggregate verifiedValue from all votes instead of using triggering vote | **Blocks freeze** | Low |
| 6 | Add equivocation detection and automated slashing trigger | Pre-mainnet | Medium |
| 7 | Index VoteWeightByKey by AgentID for O(1) lookup | Pre-mainnet | Low |
| 8 | Audit finalization handler for potential deadlock under load | Pre-mainnet | Medium |
| 9 | Document head-count-on-expiry as a known safety/liveness trade-off | Pre-mainnet | Low |
