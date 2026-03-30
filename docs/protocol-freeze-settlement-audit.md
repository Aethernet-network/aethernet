# Protocol Freeze — Phase 3 Audit: Settlement Rules

**Date:** 2026-03-29
**Scope:** Complete inventory of settlement authority, escrow lifecycle, fees, supply management, slashing, staking, and cross-node consistency.
**Prerequisites:** Phase 1 (event format) and Phase 2 (consensus rules) are frozen.

---

## Task 1: Settlement Authority

### What Is the SettlementApplicator?

The `Applicator` (`internal/settlement/applicator.go:50-85`) is the ONLY component authorized to mutate ledger state in response to consensus-finalized Settlement events. It is deterministic: given the same SettlementPayload and pre-state, every node produces the same post-state.

### Is It the Sole Mutation Path?

**No.** The grep verification reveals several ledger mutation paths outside `internal/settlement/`:

**Direct ledger mutations found:**

| Location | Method | Purpose | Concern |
|---|---|---|---|
| `fees/collector.go:176,182` | `FundAgent` | Legacy `CollectFee` — mints tokens | **VIOLATION** — creates AET outside genesis. Has WARNING in docstring. |
| `fees/collector.go:243,246` | `TransferFromBucket` | `CollectFeeFromRecipient` — moves existing tokens | Acceptable — invoked by SettlementApplicator |
| `staking/staking.go:243` | `TransferFromBucket` | `Stake` — moves to staking-pool | **VIOLATION** — direct ledger mutation outside settlement path |
| `staking/staking.go:271` | `TransferFromBucket` | `Unstake` — moves from staking-pool | **VIOLATION** — direct ledger mutation outside settlement path |
| `escrow/escrow.go:128,152,197,208,219,250` | `TransferFromBucket` | Escrow hold/release/refund | Acceptable — invoked by task settler (via SettlementApplicator) |
| `api/server.go:2317` | `TransferFromBucket` | Onboarding allocation | **VIOLATION** — direct ledger mutation in API handler |

**Assessment:** The "sole authority" invariant is **partially violated**. Staking operations and onboarding allocations bypass the settlement path. The fee collector's `CollectFee` method (which uses `FundAgent`) has a WARNING but is still callable.

### Event Types Handled

| Event type | Handler | Balance changes |
|---|---|---|
| `Transfer` | `applyTransfer` (line 287) | RecordFromSync → Settle(Settled or Adjusted). Accepted: debit FromAgent, credit ToAgent. Adjusted: no-op (entry marked Adjusted). |
| `Generation` | `applyGeneration` (line 333) | RecordFromSync → Verify(verifiedValue) or Reject. Accepted: generation entry Settled with verified value. Rejected: entry Adjusted. |
| `TaskSettlement` | `applyTaskSettlement` (line 344) | Delegates to `taskSettlerFn` (injected). Only for accepted verdicts. |

### Exact Balance Changes per Type

**Transfer (accepted):** `Record` creates an entry with `FromAgent → ToAgent` for `Amount`. `Settle(Settled)` transitions the entry to Settled. The balance formula counts Settled inflows as spendable and reserves Settled+Optimistic outflows.

**Transfer (rejected/adjusted):** `Settle(Adjusted)` marks the entry Adjusted. Adjusted entries are excluded from both inflow and outflow calculations, effectively reversing the transfer.

**Generation (accepted):** `Verify(verifiedValue)` sets `VerifiedValue` and transitions to Settled. No direct balance mutation — generation is an accounting metric, not a token mint.

**Generation (rejected):** `Reject()` transitions to Adjusted. `VerifiedValue` stays zero.

**TaskSettlement (accepted):** The task settler function (`cmd/node/main.go:1502-1551`) computes fee splits and calls `escrowMgr.ReleaseNet()` to distribute: net amount → claimer, validator share → validator, treasury share → treasury. All from the escrow bucket.

### Idempotency

**Yes.** The `applied` map (`applicator.go:72,140-146`) is keyed by `TargetEventID`. Once any settlement for a target is applied, all future records for the same target are silently skipped. The map is persisted to BadgerDB under `settlement:applied:` prefix and restored on startup via `LoadApplied` (`applicator.go:411-424`).

Additionally, `RecordFromSync` on both ledgers is idempotent — returns nil if the event is already recorded (`transfer.go:241`, `generation.go:160`).

---

## Task 2: Escrow Lifecycle

### When Is Escrow Locked?

Escrow is locked at **task post time**. The API handler `handlePostTask` calls `protoClient.SubmitEscrowLock()` which creates a Transfer event with `Reason: "escrow-lock"` from the poster to `escrow:<taskID>` bucket (`internal/protocol/client.go:81-83`).

The SettlementApplicator's `applyTransfer` registers the escrow entry when the escrow-lock transfer settles on peer nodes (`applicator.go:306-315`).

### What Is Locked?

The **full budget** is locked. No fees are deducted at lock time. Fees are computed and distributed at release time.

### When Is Escrow Released?

| Trigger | Method | Destination |
|---|---|---|
| Task approved (direct) | `handleApproveTask` → `ReleaseNet` | Worker (net), validator (fee share), treasury (fee share) |
| Task settlement (consensus) | `taskSettlerFn` → `ReleaseNet` | Same split via consensus path |
| Task cancelled | `handleCancelTask` → `Refund` | Full amount back to poster |

### Where Does Released Escrow Go?

For approved tasks, `ReleaseNet` (`escrow/escrow.go:176-238`) distributes:
1. **Worker (claimer):** `budget - fee` (net amount)
2. **Validator:** `fee * 80 / 100` (80% of fee)
3. **Treasury:** `fee * 20 / 100` (20% of fee)
4. **Burned:** `fee - validatorAmount - treasuryAmount` (rounding remainder, always 0 with current BurnShare=0)

### Partial Release

**Yes.** `ReleaseNet` tracks per-disbursement completion via `WorkerPaid`, `ValidatorPaid`, `TreasuryPaid` flags (`escrow.go:27-33`). If a release fails mid-way, retry skips already-completed transfers. This is the CRITICAL-3 idempotency fix.

### Expired Tasks

There is **no automatic escrow expiry**. If a task is never approved, cancelled, or disputed, the escrow stays locked indefinitely. This is a gap — see Task 8.

### Double-Lock Vulnerability

**Mitigated.** `Hold` records the entry before the ledger transfer (`escrow.go:117-124`), and rolls back the entry if the transfer fails (`escrow.go:129-137`). However, `Hold` does not check if an entry already exists for the same `taskID` — calling `Hold` twice with the same `taskID` would overwrite the first entry. The `IsLocked` check in `applyTransfer` (`applicator.go:309`) prevents double-locking from the settlement path, but direct API calls could theoretically double-lock.

### Separate Balance Tracking

Yes. Escrow balances exist in the transfer ledger as synthetic `escrow:<taskID>` agent IDs. These are distinct from any real agent's balance. Funds in escrow are not spendable by any agent — they can only be released or refunded through the escrow manager.

---

## Task 3: Fee Computation

### Settlement Fee Formula

```
fee = amount * FeeBasisPoints / 10_000
```

Where `FeeBasisPoints = 10` (0.1% = 10 basis points). Source: `fees/collector.go:33,128-130`.

### Assurance Lane Fees

| Lane | Fee rate | Fee floor |
|---|---|---|
| Standard | 3% | 2 AET (2,000,000 µAET) |
| High Assurance | 6% | 4 AET (4,000,000 µAET) |
| Enterprise | 8% | 8 AET (8,000,000 µAET) |

Source: `internal/config/config.go` (AssuranceConfig defaults).

Note: Assurance lane fees are applied to task budgets at the assurance layer. The 0.1% settlement fee (FeeBasisPoints=10) is a separate, additional protocol fee applied to all settled transactions.

### Fee Distribution

| Recipient | Share | Source |
|---|---|---|
| Validator | 80% | `fees.ValidatorShare = 80` (`collector.go:37`) |
| Treasury | 20% | `fees.TreasuryShare = 20` (`collector.go:40`) |
| Burned | 0% | `fees.BurnShare = 0` (`collector.go:43`) |

### When Are Fees Deducted?

**At settlement time**, not at post time. The task settler function (`cmd/node/main.go:1525-1535`) computes the fee split and releases from escrow after consensus finalizes.

For direct transfers (non-task), the SettlementApplicator's fee collection path (`applicator.go:217-229`) calls `CollectFeeFromRecipient` which deducts from the recipient's balance after settlement.

### Negative Balance Risk

**Low.** `CollectFeeFromRecipient` uses `TransferFromBucket` which checks `balanceLocked(fromID) >= amount` (`transfer.go:465-468`). If the recipient doesn't have enough balance (e.g., they spent it between settlement and fee collection), the transfer silently fails (errors are ignored at `collector.go:243,246`).

### Integer Arithmetic

**Yes, entirely integer.** All fee calculations use `uint64` division: `amount * bps / 10_000` (`collector.go:93`). No floating-point math anywhere in the fee path. Source: verified by reading all of `collector.go`.

### Rounding Behavior

Truncation (round down). `amount * 10 / 10_000` truncates via integer division. For amounts below 10,000 µAET (0.01 AET), the fee is zero. The remainder after splitting validator/treasury shares is assigned to "burned" (`collector.go:163,231`), which with `BurnShare=0` is always 0 or a negligible rounding residual.

---

## Task 4: Faucet and Supply

### How the Faucet Creates AET

The faucet **does not mint tokens**. It submits a canonical Transfer event from `genesis:faucet` bucket to the requesting agent (`api/server.go:3051-3137`). The `genesis:faucet` bucket was pre-funded during genesis from the ecosystem allocation.

### Faucet Limits

| Limit | Value | Source |
|---|---|---|
| Amount per grant | 5,000 AET (5,000,000,000 µAET) | `genesis.FaucetGrantAmount` |
| Cooldown per agent | 24 hours | `genesis.FaucetCooldownHours` |
| IP rate limiting | Shared with registration limiter | `api/server.go:3064-3070` |
| Testnet only | `AETHERNET_TESTNET=true` required | `api/server.go:3055` |

### Faucet Farming Vulnerability

**Partially mitigated.** The cooldown is per-agent (checked via `LastTransferByReason` in the transfer ledger). IP rate limiting provides Sybil resistance. However, an attacker with multiple IPs could register new keys and drain the faucet bucket. The mitigation: the faucet bucket has a finite balance (funded from genesis ecosystem allocation). Once drained, no more faucet grants are possible. The faucet is testnet-only.

### Total Supply Cap

**Fixed at 1 billion AET** (1,000,000,000,000,000 µAET). Source: `ledger/supply.go:18`.

**Enforced by:** `TransferLedger.SetMintCap()` (`transfer.go:152-156`). After genesis completes, `SetMintCap(totalMinted)` is called (`cmd/node/main.go:2162`), freezing the supply. Any subsequent `FundAgent` call returns `ErrMintCapExceeded` (`transfer.go:405-408`).

### AET Creation Paths

All production `FundAgent` callers:

| Location | Purpose | When called | Supply-safe? |
|---|---|---|---|
| `cmd/node/main.go:2439` | Genesis bucket seeding (auto) | Once at genesis | Yes — before mint cap is set |
| `cmd/node/main.go:2523` | Genesis bucket seeding (CLI) | Once at genesis | Yes — before mint cap is set |
| `fees/collector.go:176,182` | Legacy `CollectFee` | Potentially at settlement | **NO** — mints new tokens, violates supply invariant |

The legacy `CollectFee` method has a WARNING in its docstring (`collector.go:132-141`) stating it must NOT be called in settlement paths. The settlement applicator uses `CollectFeeFromRecipient` (which moves existing tokens) and `TrackFee` (stats-only) instead.

**No code path creates AET beyond genesis** when the mint cap is set, because `FundAgent` enforces it. The legacy `CollectFee` would fail with `ErrMintCapExceeded` if called after genesis.

---

## Task 5: Slashing

### What Triggers a Slash

Slashing is triggered **externally** — there is no automatic detection mechanism within consensus. The `SlashEngine.Slash()` method (`validator/slashing.go:146`) is called by operator-level or governance-level code.

### Slash Amounts

| Offense | Slash % | Cooldown | Source |
|---|---|---|---|
| Fraudulent Approval | 30% | 30 days | `config.SlashFraudulentApproval`, `CooldownTier1Days` |
| Dishonest Replay | 40% | 60 days | `config.SlashDishonestReplay`, `CooldownTier2Days` |
| Collusion | 75% | 180 days | `config.SlashCollusion`, `CooldownTier3Days` |
| Collusion (repeat) | 75% + permanent exclusion | N/A | When `CollusionRepeatExclusion=true` and `SlashCount >= 1` |

Source: `validator/slashing.go:125-136`, `slashing.go:168-170`.

### Where Slashed Funds Go

```
ChallengerShare = SlashAmount / 2     (50%)
ReserveShare    = SlashAmount - ChallengerShare  (50%, gets rounding remainder)
```

Source: `slashing.go:178-179`. The `SlashResult` struct reports these amounts but the `SlashEngine` does **not** execute ledger transfers. The caller is responsible for executing the transfers.

### Negative Stake

**Prevented.** `slashAmount = uint64(pct * float64(v.StakeAmount))` (`slashing.go:173`). If `v.StakeAmount > slashAmount`, remaining = `v.StakeAmount - slashAmount`. Otherwise remaining = 0 (`slashing.go:174-176`). All uint64 arithmetic, underflow clamped.

**NOTE:** The slash amount computation uses `float64` multiplication (`pct * float64(v.StakeAmount)`). The `pct` comes from config as `float64`. This is a **cross-node consistency risk** — different CPU architectures could produce different rounding. This was NOT caught in Phase 1 because it's in the validator package, not in event payloads.

### Slashing During Active Rounds

Slashing calls `registry.ApplySlash` which changes the validator's status to Suspended or Excluded. In consensus:
- If the validator's vote is in a bound snapshot, the vote retains its weight for that round (bound snapshot is immutable)
- If a new snapshot is created after the slash, the validator has zero weight in future rounds
- This is correct BFT behavior — the slash affects future rounds, not retroactively

### Slashing Path vs Settlement

Slashing is applied through the `ValidatorRegistry` and `StakeManager`, **not** through the SettlementApplicator. The `ValidatorSlashApplied` DAG event is a lifecycle event processed by the `validatorlifecycle.Reducer`, not a settlement event. The actual stake reduction happens in-memory via `StakeManager.Slash()` or the `ValidatorRegistry.ApplySlash()`.

---

## Task 6: Staking and Unstaking

### How Staking Works

`StakeManager.Stake()` (`staking/staking.go:234-253`):
1. Records first-stake timestamp if new agent
2. When `TransferLedger` is wired: calls `TransferFromBucket(agentID, "staking-pool", amount)` to debit the agent's balance
3. If debit fails (insufficient balance): returns error, no stake recorded
4. Increments in-memory `stakes[agentID]` by amount
5. Persists to store

### How Unstaking Works

`StakeManager.Unstake()` (`staking/staking.go:261-287`):
1. Checks `amount <= stakes[agentID]` — returns false if insufficient stake
2. When `TransferLedger` is wired: calls `TransferFromBucket("staking-pool", agentID, amount)` to credit back
3. If credit fails: aborts with no state change (CRITICAL-2 atomicity fix)
4. Decrements in-memory stake
5. **No unbonding period** — unstaking is immediate

### Can Staked Funds Be Spent?

**No.** Staked funds are transferred to the `staking-pool` bucket. They are not in the agent's balance and cannot be spent. The `staking-pool` is a synthetic agent ID, inaccessible to normal transfers.

### Minimum Stake for Validator Seat

There is **no enforcement at the staking layer** preventing an agent from unstaking below the minimum required for their validator seat. The `ValidatorLifecycle.Reducer` does not check the agent's current stake before accepting lifecycle events. This is a gap — see Task 8.

### Stake During Slashing

`StakeManager.Slash(agentID, percentage)` (`staking.go:367-383`) reduces `stakes[agentID]` by `current * percentage / 100`. The slashed amount is reported but the funds are **not transferred to any destination** — they simply disappear from the stake balance. The caller (SlashEngine) reports the amounts but must execute the transfers separately.

---

## Task 7: Cross-Node Consistency

### Settlement Determinism

**Settlement is deterministic** for the same event and pre-state. The SettlementApplicator processes SettlementPayloads which contain all inputs needed for deterministic application:
- `TargetEventID` → lookup event from DAG
- `Verdict` → accepted or rejected
- `VerifiedValue` → deterministic consensus value (Phase 2: weighted median)

However, fee collection via `CollectFeeFromRecipient` is **best-effort** (errors ignored at `collector.go:243,246`). If the recipient's balance is insufficient on one node but sufficient on another (due to timing), fee collection results could differ. This is mitigated by fees being small relative to balances.

### DAG Replay Ordering

Events are replayed in **topological order** (Kahn's algorithm + `(CausalTimestamp, EventID)` sort) via `dag.TopologicalSort()` (`dag/dag.go:609-657`). This guarantees that parents are applied before children. Concurrent events (same timestamp) are ordered by EventID (deterministic tie-breaking).

Settlement events that reference target events will always be applied after their targets, since the settlement event references the target via CausalRefs.

### Race Conditions

**Balance reads during settlement:** The `TransferLedger.Balance()` method acquires a read lock (`transfer.go:341-343`). Settlement mutations acquire a write lock. These are mutually exclusive, so no torn reads. However, an API balance query could return a stale value if the settlement is being applied concurrently.

**Escrow operations:** `Escrow.ReleaseNet` acquires the escrow mutex briefly for each disbursement flag update, but the actual `TransferFromBucket` calls happen outside the mutex. This is intentional — the paid flags provide idempotency.

---

## Task 8: Gap Analysis

### GAPS THAT BLOCK PROTOCOL FREEZE

**1. `CollectFee` (legacy) uses FundAgent — potential supply inflation**

`fees/collector.go:176,182` calls `FundAgent` which mints new tokens. Although there's a WARNING in the docstring and the settlement path uses `CollectFeeFromRecipient` instead, `CollectFee` is still a public method that could be called. After genesis with mint cap set, `FundAgent` would return `ErrMintCapExceeded`, so the practical risk is limited. But the method should be removed or made private to prevent accidental use.

**2. Staking operations bypass settlement path**

`StakeManager.Stake()` and `Unstake()` directly call `TransferFromBucket` (`staking.go:243,271`). These are NOT consensus-finalized operations — they happen locally when the API receives a stake/unstake request. Different nodes could process stake/unstake requests in different orders, leading to divergent balances.

The `RecordCanonicalStake` and `RecordCanonicalUnstake` methods exist for the settlement path (`staking.go:292-325`), but they only update metadata — the actual ledger transfer already happened via the direct path. This dual-path is confusing and potentially inconsistent.

**3. Onboarding allocation bypasses settlement**

`api/server.go:2317` calls `TransferFromBucket` directly during registration to fund new agents from the ecosystem bucket. This is not consensus-finalized. If two nodes process the same registration at slightly different times, one might succeed while the other fails (if the ecosystem bucket is near-empty).

**4. Float64 in slash amount computation**

`slashAmount = uint64(pct * float64(v.StakeAmount))` (`slashing.go:173`). The config values `SlashFraudulentApproval`, `SlashDishonestReplay`, `SlashCollusion` are `float64`. Float multiplication is not guaranteed to produce identical results across CPU architectures. For protocol freeze, slash percentages should use uint32 basis points (consistent with Phase 1 payload changes).

**5. No escrow expiry mechanism**

Tasks that are never approved, cancelled, or disputed keep funds locked in escrow indefinitely. There is no timeout or cleanup mechanism. Over time, this could lock up significant AET.

**6. `TransferFromBucket` ID generation uses `len(l.entries)`**

`transfer.go:471`: `eid := event.EventID(fmt.Sprintf("onboarding:%s:%s:%d:%d", fromID, toID, amount, len(l.entries)))`. After a node restart with persisted entries, `len(l.entries)` may differ from the original call, potentially generating the same ID for different transfers or a different ID for the same transfer. This makes `TransferFromBucket` non-idempotent across restarts.

### RISKS FOR MAINNET

**1. Faucet farming on testnet**

Multiple IP addresses can drain the faucet bucket. Mitigated by finite bucket size and testnet-only flag. Not a mainnet concern.

**2. Fee rounding accumulation**

With `FeeBasisPoints=10` and integer truncation, small transactions pay zero fees. Over millions of transactions, the treasury receives less than the intended 0.1%. This is acceptable — the fee floor on assurance lanes (2+ AET) ensures meaningful fee collection for marketplace tasks.

**3. Escrow `Hold` double-lock**

`Hold` does not check if an entry already exists for the same `taskID`. The `IsLocked` check in the settlement applicator prevents this from the consensus path, but a direct API call sequence (post → cancel → re-post with same task ID) could theoretically create issues.

**4. No unbonding period for unstaking**

Unstaking is immediate. A validator could unstake, vote on a pending round, and escape slashing. The bound snapshot prevents them from voting in new rounds, but their existing votes in already-open rounds retain their weight.

### THINGS ALREADY SOLID

**1. Settlement idempotency via `applied` map**

Triple protection: OCS `processed` map, finalization handler `IsApplied`, Applicator `applied` map with crash-recovery persistence. DAG replay safely re-applies settlements.

**2. Escrow ReleaseNet idempotency**

Per-disbursement `WorkerPaid/ValidatorPaid/TreasuryPaid` flags enable safe retry of partial releases (CRITICAL-3 fix). Persisted to store between retries.

**3. Fixed supply with enforced mint cap**

`SetMintCap(totalMinted)` after genesis prevents any code path from minting beyond the genesis allocation. `ErrMintCapExceeded` is a hard error. `totalMinted` is persisted and restored on restart.

**4. Integer-only fee arithmetic**

All fee calculations use `uint64` multiplication and division. No floating-point anywhere in the fee path. Rounding is always truncation (round down), which is the safe direction (protocol never overcollects).

**5. Balance formula is OCS-conservative**

`Balance = Settled_inflows - (Settled + Optimistic)_outflows`. Optimistic outflows are reserved immediately, preventing double-spend. Adjusted entries are excluded from both sides. Clamped to zero (no negative balances).

**6. Escrow uses synthetic bucket IDs**

`escrow:<taskID>` as the agent ID creates a virtual account that no real agent can access. Funds can only be moved via the escrow manager's `Release`, `ReleaseNet`, or `Refund` methods.

**7. Comprehensive ledger persistence**

Both transfer and generation ledgers write through to BadgerDB on every mutation. Entries survive node restarts. Store operations happen before in-memory state changes (persist-before-mutate rule).

**8. `TransferFromBucket` validates balance before transfer**

`balanceLocked(fromID) >= amount` check (`transfer.go:465-468`) prevents overdraft from any bucket, including escrow and staking-pool.

---

## Summary of Action Items (Priority Order)

| # | Action | Severity | Effort |
|---|---|---|---|
| 1 | Make `CollectFee` private or remove it; only `CollectFeeFromRecipient` and `TrackFee` should be public | **Blocks freeze** | Low |
| 2 | Route staking through consensus (canonical stake-lock/unlock transfers) instead of direct ledger mutation | **Blocks freeze** | Medium |
| 3 | Route onboarding allocation through consensus (canonical transfer from ecosystem bucket) | **Blocks freeze** | Medium |
| 4 | Replace float64 in slash amount computation with basis-point integer arithmetic | **Blocks freeze** | Low |
| 5 | Fix `TransferFromBucket` ID generation to be deterministic across restarts | **Blocks freeze** | Low |
| 6 | Add escrow expiry mechanism (e.g., 30-day timeout → auto-refund to poster) | Pre-mainnet | Medium |
| 7 | Add `Hold` double-lock guard (check `IsLocked` before creating entry) | Pre-mainnet | Low |
| 8 | Add unbonding period for unstaking (e.g., 7-day cooldown) | Pre-mainnet | Medium |
| 9 | Enforce minimum stake for validator seat at the staking layer | Pre-mainnet | Low |
