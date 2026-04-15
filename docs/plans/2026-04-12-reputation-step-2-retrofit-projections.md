# Step 2 Implementation Plan — Retrofit Existing Stores into the Projection Registry

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `internal/projections/` (shipped in Step 1) into `cmd/node/main.go`, retrofit every existing consensus-adjacent durable store into the registry with a live `StateProbe`, a `LiveConsumerRef`, a `ReplayConsumerRef`, and a passing integration test, and close the second known "writer exists, caller doesn't" instance by wiring a production caller for `CalibrationStore.Increment`.

**Architecture:** The registry is constructed from `cmd/node/main.go` once all durable stores exist and before the commit bus starts dispatching events. Each Canonical store gains a minimal public `Empty(ctx) (bool, error)` method that the registry's `StateProbe` closes over. Calibration-increment wiring is added inside `TaskVerificationConsensusConsumer.Consume()` between the settlement step and the slashing step, idempotency-guarded by a new `CalibrationIncremented` boolean on `TaskVerificationRound`. Periodic `HealthCheck` runs from the signal-wait loop; results surface on the existing `/v1/status` endpoint and as `slog.Warn` lines.

**Tech Stack:** Go stdlib, BadgerDB v4, existing `internal/recognition/` fabric, `internal/api/server.go`. No new dependencies.

**Source plans**:
- `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` §9 (registry — authoritative), §8 (slashing + calibration), §17 step 2 (retrofit list).
- `docs/plans/2026-04-12-reputation-step-1-projection-registry.md` (the primitive you are consuming).

---

## Scope alignment with the binding plan

**Implemented in this step**:
- Plan §9.6 initial retrofit list (see §"Store inventory" below).
- Plan §9.4 PR-1..PR-4 enforcement at startup via `MustRegister`.
- Plan §9.5 runtime `HealthCheck` at startup and periodically.
- Plan §9.7 defense item 2 (registry entry with every required field) and item 4 (startup health check).
- Plan §9.7 defense item 3 (integration test driving real events through live consumer) — one per Canonical projection.
- Closing the second known writer-without-caller instance: wire a production caller for `CalibrationStore.Increment` inside `TaskVerificationConsensusConsumer.Consume()`.

**Not implemented in this step (explicit deferrals)**:
- CI type-graph scan for writer-without-caller pattern (plan §9.7 item 1) — Step 3.
- PR-3 CI-level "referenced test symbol exists" check — Step 3.
- Code-review checklist (plan §9.7 item 5) — workstream closeout.
- The new reputation `EvidenceStore` and `ChallengeResolutionStore` — Step 4.
- Deletion of existing `internal/taskverification/reputation.go` — Step 3.
- Full observability endpoints with tier-3 delayed aggregates — Step 8.
- Wiring a production caller for the existing `ValidatorReputationStore.RecordVote`. That store is deleted in Step 3; wiring a writer to a soon-deleted store is churn. Evidence-store writing is the replacement per Step 6.

---

## Design decisions needing sign-off before code

### D1 — Epoch clock source (REDIRECTED: build a real round counter)

**Resolution**: founder redirected. Do not use `ValidatorSnapshot.SetVersion()` as a placeholder — its cadence is unrelated to the PR-5 window and would render the health check meaningless in practice. Build a real round-counter primitive as part of Step 2.

**New primitive — `internal/epoch/RoundCounter`**:

- Package: new `internal/epoch/` (keeps the counter stack self-contained — counter + consumer + tests in one place).
- Persistence: BadgerDB via the shared `store.Store` handle. Two key shapes:
  - `"ec:total"` → big-endian `uint64` holding total finalized rounds.
  - `"ec:counted:<roundID>"` → presence marker (empty value) so replay is idempotent. `Apply(roundID)` is a single BadgerDB `Update` transaction that checks the marker and either no-ops or increments+sets atomically.
- API:
  - `Apply(ctx, roundID) (epochChanged bool, err error)` — idempotent; returns true only when the atomic increment crossed a multiple of `EpochLength`.
  - `Total(ctx) (uint64, error)` — read through (or cached behind an in-memory `atomic.Uint64` refreshed on every `Apply`).
  - `CurrentEpoch() uint64` — pure-memory read of `Total / EpochLength`.
  - `Empty(ctx) (bool, error)` — `Total == 0` (the `StateProbe` for the counter itself, which is a registered Canonical projection).
  - `OnEpochChange(cb func(epoch uint64))` — register an observer callback, fired (on a fresh goroutine) after each `Apply` that returns `epochChanged=true`. Used by D6.
- Consumer: `internal/epoch/RoundCountConsumer` implementing `recognition.CommitConsumer`. Interests: `event.EventTypeTaskVerificationConsensus`. `Ready` always true (consensus events are always ready). `Consume` calls `counter.Apply(ctx, payload.RoundID)`. No locks held across BadgerDB I/O beyond the counter's own txn; no reentrancy with the recognition bus.
- Constant: `const EpochLength uint64 = 1000` (plan §16 locked value).
- `epochFn` closure passed to `NewProjectionRegistry` — lazy:
  ```go
  epochFn := func() uint64 { return counter.CurrentEpoch() }
  ```
  Resolved on each registry call. Chicken-and-egg solved: the counter is constructed first, then the closure, then `NewProjectionRegistry(epochFn)`, then `MustRegister(counter's CanonicalProjection)` along with every other projection. The counter is registered as the **first** Canonical projection (conceptually "the clock is a projection too").
- Registration: `CanonicalProjection` for the counter itself. `LiveConsumerRef = "internal/epoch.RoundCountConsumer"`. `ReplayConsumerRef = "internal/epoch.RoundCountConsumer"` (same — replay flows through the recognition bus; the consumer's idempotency guard handles replay identically to live commit).
- **Consumer-path conflict check**: the existing `TaskVerificationConsensusConsumer` (`internal/recognition/task_verification_consensus_consumer.go`) also consumes `EventTypeTaskVerificationConsensus`. The recognition fabric dispatches to all interested consumers; having two consumers interested in the same event is the normal pattern (see `OCSSubmitConsumer` + `SettlementConsumer` both caring about settlement events). No architectural reconciliation required. Counter runs under its own lock; `TaskVerificationConsensusConsumer` runs under its own slashing/settlement path; no shared mutable state.
- **Early-finalization edge case check**: mathematically-secured early finalization emits exactly one `TaskVerificationConsensus` event per round (plan §2 event flow, confirmed in `internal/taskverification/finalizer.go`). Count is one per round regardless of finalization path. Replay produces identical count because the idempotency marker is keyed on `roundID`, which is deterministic across nodes.

### D2 — Calibration-apply wiring site and idempotency guard (ACCEPTED with rename)

**Resolution**: founder accepted the field-on-round approach with a semantic rename: `CalibrationApplied bool \`json:"calibration_applied,omitempty"\`` (was `CalibrationIncremented`). The rename names the round state, not the writer mechanism — future implementations may batch-apply or transaction-scope the update and the flag remains meaningful.

**Wiring site**: inside `TaskVerificationConsensusConsumer.Consume()` at `internal/recognition/task_verification_consensus_consumer.go:112`, **after** the settlement block (`:91–108`) and **before** the slashing-evaluation block (`:112–124`). Matches plan §8 and multi-validator design §8: calibration counter increments once per round finalization per (category, family) tuple, and `SlashingEvaluator.EvaluateRound` reads the post-increment state.

**Iteration semantic**: round's `Votes []TaskVerificationVoteRecord` slice can contain multiple votes per family across multiple validators. Extract distinct `AnalyzerFamily` strings from `round.Votes`; call `Increment(ctx, round.Category, verification.FamilyID(family))` once per distinct family.

**Idempotency guard** (D2-a, accepted): add `CalibrationApplied bool` to `TaskVerificationRound` in `internal/taskverification/round.go`. Consumer reads the round under load, skips the whole apply block if `CalibrationApplied == true`, otherwise iterates families, increments each, sets `CalibrationApplied = true`, and saves the round atomically with the settlement/slashing path. Existing persisted rounds deserialize with zero-value `false`; a single replay catches up; thereafter the flag stays true. JSON field is additive and backward-compatible.

### D3 — Additional stores discovered beyond plan §9.6 (ACCEPTED)

Investigation surfaced stores not on the plan §9.6 list. Per the Step 2 prompt ("If the retrofit discovers additional consensus-adjacent stores not listed above, they must also be registered"), each needs a classification decision. Proposals:

| Store | Package | File | Recommended Classification | Rationale |
|---|---|---|---|---|
| `TaskVerificationRound` BadgerStore | `internal/taskverification` | `badger_store.go` | **Canonical (RECOMMEND REGISTER)** | Stores the consensus-finalized round + verdict. Downstream settlement reads it. Plan §9.6 omits it, but it is the most central consensus-adjacent store in the protocol. |
| OCS `PendingItem` store | `internal/ocs` | `engine.go` — `ocsPersistence` interface | **Canonical (RECOMMEND REGISTER)** | Gates settlement state machine; consensus-critical. |
| `StakeManager` | `internal/staking` | `staking.go` — `stakeStore` interface | **Canonical (RECOMMEND AUDIT then register if confirmed Canonical)** | Stake amounts affect validator weight in BFT. Agent A flagged "stake management is separate from reputation"; plan §9.6 does not list it. |
| Identity Registry | `internal/identity` | `registry.go` | **Canonical (RECOMMEND AUDIT)** | Identity validation can gate participation. |
| RoundProgress snapshot store | `internal/roundprogress` | `badger_store.go` | **Advisory** | Per BlobSync design §6.4, progress is liveness input only; cannot itself create verdicts. |

**Resolved**: register `TaskVerificationRound` BadgerStore, `OCSPending`, and `RoundProgress` (Advisory) in Step 2. **Defer** `StakeManager` and Identity Registry to a dedicated follow-up retrofit pass; add a `docs/lessons.md` entry documenting the deferral so a future session does not lose track of them.

### D4 — `AgentReputation` classification (ACCEPTED with reclassification warning)

**Resolution**: classify `AgentReputation` as Advisory, and use the existing `AllowIdleWithJustification` + `IdleJustification` pair to carry a standing reclassification warning. The registry entry sets `AllowIdleWithJustification: true` with `IdleJustification: "informational only; no canonical consumer; reclassify to Canonical if any settlement or consensus path reads it in the future"`. This surfaces on every future review and is visible in `HealthCheck` output as a `HealthAllowedIdle` reason.

**Semantic stretch note**: the Step-1 CR-9 named-exception semantics for `AllowIdleWithJustification` were specified with Canonical projections in mind (the `ChallengeResolutionStore` exception). Using the same pair on an Advisory projection is a semantic stretch — Advisory projections are informational by classification and don't need an idle exception. The V9/V10 coupling validation permits this (the coupling rule applies to both classifications), and it's honest: an Advisory entry using this pair declares "intentionally idle, here's why." If future Advisory entries start using this pattern more than once, we should consider renaming the field to `ClassificationNote` (or adding a new parallel field). For Step 2's single case (`AgentReputation`), proceed with the existing fields. **Flagged as a field-naming question for a future step if the pattern grows.**

### D5 — Health endpoint surface (ACCEPTED with summary/verbose refinement)

**Resolution**: integrate registry health into `/v1/status` as a new `projections` field. Response modes:

- **Default (summary)** — on `GET /v1/status` with no query params:
  ```json
  "projections": {
      "overall": "OK",
      "counts": {
          "OK": 2, "NotYetEligible": 3, "AllowedIdle": 1,
          "Empty": 0, "ProbeFailed": 0, "Advisory": 3
      }
  }
  ```
- **Verbose** — on `GET /v1/status?verbose=true`: include the full per-entry `Checks` list alongside `overall` + `counts`.

Server caches the latest `HealthStatus` (guarded by a read-write mutex), populated by the epoch-boundary-triggered goroutine (D6) and by the startup invocation. `/v1/status` reads the cached value; probes are not called in the HTTP hot path.

`slog.Warn` lines also emitted for any Canonical entry that flips to `HealthEmpty` or `HealthProbeFailed`.

### D6 — Epoch-boundary-triggered HealthCheck (REDIRECTED: no wall clock)

**Resolution**: founder redirected. No `time.Ticker`. No `time.Now()`. The health check is PR-5-semantic and must fire only when PR-5 state can actually change, which is at epoch boundaries.

**Mechanism**:
1. At startup, after all `MustRegister` calls complete, invoke `HealthCheck(ctx)` once. Log entries. Cache the result on `api.Server` via `SetProjectionHealth(status)`.
2. Register an `OnEpochChange` callback with the `RoundCounter` (D1). The callback runs on a fresh goroutine (fire-and-forget, not blocking the counter's BadgerDB transaction) and re-runs `HealthCheck(ctx)`, logs any non-OK Canonical status at WARN, and updates the cached `api.Server` health.
3. On shutdown, the main context cancels and any in-flight HealthCheck returns early.

No polling loop. No timer. The trigger is the counter's deterministic crossing of `totalFinalizedRounds % EpochLength == 0`.

**Lifecycle**: the callback list is held on the `RoundCounter`. Registration happens once at startup (in `cmd/node/main.go`, after `projReg` construction and the first `HealthCheck`). Callbacks fire on a fresh goroutine per event; the goroutine accepts the main `ctx` so shutdown cancels any in-flight probe.

### D7 — `Empty(ctx)` method additions + closure indirection (ACCEPTED with clarification)

**Resolution**: add `Empty(ctx context.Context) (bool, error)` to each Canonical store. Six-package touch footprint accepted.

**Clarification**: registry entries do NOT pass `store.Empty` as a method value directly; they wrap it in a closure:
```go
StateProbe: func(ctx context.Context) (bool, error) {
    return calibrationStore.Empty(ctx)
}
```
The closure absorbs any future probe-signature evolution (e.g., returning a richer status than a single bool) without requiring every store's method to change. Also makes the dependency on the store explicit at the registry-entry definition site.

Implementation: BadgerDB-backed stores iterate the key prefix with a bounded iterator and return on first hit; in-memory stores check `len(map) == 0`. Under 15 lines per method, with a dedicated unit test per store.

### D8 — Integration test location + IntegrationTestRef meta-assertion (ACCEPTED)

**Resolution**: one integration test file per Canonical projection under `internal/integration/projection_*_test.go`. Each test:
1. Builds a minimal in-process node fixture with a temp BadgerDB.
2. Triggers the store's live consumer via a real DAG event.
3. Asserts `Empty(ctx)` flips from `true` to `false`.
4. For calibration specifically: also asserts that two consumer invocations with the same event produce exactly one apply (replay idempotency per D2).
5. **Meta-assertion** (new): asserts that the registry entry's `IntegrationTestRef` string matches the test's own fully-qualified symbol path. This is the Step-2-side groundwork for Step 3's CI existence check — today we catch drift by self-reference, Step 3 adds the codebase-wide scan.

**Implementation of the meta-assertion**: each store package exposes an entry constructor, e.g.:
```go
// internal/taskverification/calibration_projection.go
func CalibrationProjection(store *CalibrationStore) projections.CanonicalProjection { ... }
```
Integration tests call the same constructor and compare `.IntegrationTestRef` against the expected symbol string. Main.go's `MustRegister` wiring uses these constructors too — single source of truth for each entry. This eliminates duplicated entry definitions between production and test.

**Target file / symbol paths**:
| Projection | File | Test symbol |
|---|---|---|
| `RoundCounter` | `internal/integration/projection_round_counter_test.go` | `TestRoundCounter_IncrementsOnConsensus` |
| `CalibrationStore` | `internal/integration/projection_calibration_test.go` | `TestCalibration_AppliesOnRoundFinalization` |
| `Escrow` | `internal/integration/projection_escrow_test.go` | `TestEscrow_HoldsOnTransferOptimistic` |
| `TransferLedger` | `internal/integration/projection_ledger_test.go` | `TestTransferLedger_AccumulatesOnSettlement` |
| `TaskVerificationRound` BadgerStore | `internal/integration/projection_tvround_test.go` | `TestTaskVerificationRound_PersistsOnTaskSubmitted` |
| `OCSPending` | `internal/integration/projection_ocs_pending_test.go` | `TestOCSPending_AccumulatesOnOptimistic` |

Advisory stores (`AgentReputation`, `BlobServingReputation`, `RoundProgress`) are exempt from PR-3 integration-test requirement per plan §9.4 V11 (Advisory can opt out). They do still need a `ProjectionEntry` constructor so main.go can register them uniformly.

---

## Store inventory — final registry entries

| Name | Package | StoreType | Class. | Empty() addition | LiveConsumerRef | IntegrationTestRef |
|---|---|---|---|---|---|---|
| `RoundCounter` | `internal/epoch` | `RoundCounter` | Canonical | built-in | `internal/epoch.RoundCountConsumer` | `internal/integration.TestRoundCounter_IncrementsOnConsensus` |
| `CalibrationStore` | `internal/taskverification` | `CalibrationStore` | Canonical | add | `internal/recognition.TaskVerificationConsensusConsumer` | `internal/integration.TestCalibration_AppliesOnRoundFinalization` |
| `Escrow` | `internal/escrow` | `Escrow` | Canonical | add | `internal/settlement.Applicator` | `internal/integration.TestEscrow_HoldsOnTransferOptimistic` |
| `TransferLedger` | `internal/ledger` | `TransferLedger` | Canonical | add | `internal/settlement.Applicator` | `internal/integration.TestTransferLedger_AccumulatesOnSettlement` |
| `TaskVerificationRound` | `internal/taskverification` | `BadgerStore` | Canonical | add | `internal/recognition.TaskVerificationRoundConsumer` | `internal/integration.TestTaskVerificationRound_PersistsOnTaskSubmitted` |
| `OCSPending` | `internal/ocs` | `Engine` (pending state) | Canonical | add | `internal/recognition.OCSSubmitConsumer` | `internal/integration.TestOCSPending_AccumulatesOnOptimistic` |
| `AgentReputation` | `internal/reputation` | `ReputationManager` | **Advisory** | optional | `internal/settlement.Applicator` (via `identity.RecordTaskCompletion`) | (Advisory — not strictly required per PR-3) |
| `BlobServingReputation` | `internal/blobsync` | `BlobServingReputation` | **Advisory** | optional | `internal/blobsync.BlobTransport` | (Advisory) |
| `RoundProgress` | `internal/roundprogress` | `BadgerSnapshotStore` | **Advisory** | optional | (control-plane, not a DAG consumer) | (Advisory) |

Replay consumer references: for BadgerDB-backed stores, the replay path is BadgerDB-load-at-startup (no DAG-replay rebuild); `ReplayConsumerRef` points to the package's `LoadFromStore` or constructor function. For in-memory stores (AgentReputation's map, BlobServingReputation), the replay reference points to their startup loader (e.g., `ReputationManager.LoadFromStore`). Full list in the task-by-task plan below.

**`GenerationLedgerCalculator` is NOT registered** — it is a stateless pure function per agent-A audit (`internal/settlement/generation_ledger_calculator.go:41`). Documented in the plan as "audited, not registered" with reference to §"What this document does not say."

---

## Wiring plan — `cmd/node/main.go`

All sites cited relative to current `main` state; exact line numbers will shift slightly as edits land.

1. **Registry construction** (new block, immediately after `tvCalibrationStore` at `cmd/node/main.go:1871`):
   ```go
   projReg := projections.NewProjectionRegistry(func() uint64 {
       if stack.lifecycleReducer == nil {
           return 0
       }
       snap := stack.lifecycleReducer.Snapshot()
       if snap == nil {
           return 0
       }
       return snap.SetVersion()
   })
   stack.projReg = projReg
   ```

2. **Consolidated `MustRegister` block** (immediately after step 1, before `commitBus.Register(tvConsensusConsumer)` at `:1906`): one `MustRegister` call per projection. Each block includes the `StateProbe` closure capturing the store handle.

3. **Startup `HealthCheck`** (immediately after step 2): call `projReg.HealthCheck(ctx)`, iterate `hs.Checks`, `slog.Info`/`slog.Warn` per status. Overall is expected to be `HealthOK` (every Canonical is `HealthNotYetEligible` at startup because `ageEpochs == 0 <= EligibilityWindow`).

4. **Periodic `HealthCheck`** (in `runLoop` at `cmd/node/main.go:2549`): add `time.NewTicker(60 * time.Second)` to the existing select loop. On each tick, call `HealthCheck(ctx)`, log WARN on any non-OK Canonical status.

5. **API integration** (in `internal/api/server.go` `handleStatus` at `:2718`): add `projections` field to the JSON response carrying `Overall` and `len(Checks)`.

6. **Calibration-increment wiring** (in `internal/recognition/task_verification_consensus_consumer.go:108` between settlement and slashing): see §D2. New consumer field `calibration *taskverification.CalibrationStore` added to the struct; `NewTaskVerificationConsensusConsumer` gains a new parameter; `cmd/node/main.go:1906` updates the constructor call.

---

## Task-by-task implementation — seven ordered commits

Founder-approved commit strategy (multi-commit, not squashed). Each commit compiles cleanly and its tests pass in isolation. Later commits may add tests but must not break earlier ones. Branch: `feat/projections-registry-step-2` (already created).

**Seven-commit sequence**:

1. **Round counter primitive + first Canonical registration.** `internal/epoch/` package (counter, consumer, tests). No wiring into main.go yet — that commit comes at step 4. Includes the counter's `CanonicalProjection` constructor `RoundCounterProjection(counter *RoundCounter) projections.CanonicalProjection`.
2. **`Empty(ctx)` methods on each Canonical store** (CalibrationStore, Escrow, TransferLedger, TaskVerificationRound BadgerStore, OCS Engine) + dedicated unit test per store. No registry wiring yet.
3. **`CalibrationApplied` field** on `TaskVerificationRound` + calibration-writer wiring inside `TaskVerificationConsensusConsumer.Consume()` with idempotency guard. Updates the consumer constructor signature and its sole call site. Updates unit tests.
4. **`cmd/node/main.go` wiring**: construct registry with lazy-epoch closure, `MustRegister` all eight projections via per-package entry constructors, run startup `HealthCheck` and cache result on api.Server.
5. **Epoch-boundary hook**: `OnEpochChange` observer on `RoundCounter` that re-runs `HealthCheck` and updates cached status; registration happens in main.go.
6. **`/v1/status` `projections` field** with summary (default) and verbose (`?verbose=true`) modes. api.Server gains `SetProjectionHealth` setter and cached-status reader.
7. **Integration tests** per Canonical projection under `internal/integration/projection_*_test.go` with meta-assertion on `IntegrationTestRef`.

### Task 0 — Add `Empty(ctx) (bool, error)` to each Canonical store

One task group because these are five near-identical additions.

- [ ] **Step 0.1** — `internal/taskverification/calibration.go`: add method `func (s *CalibrationStore) Empty(ctx context.Context) (bool, error)` that iterates keys with prefix `"cal:"` and returns on first hit. Test: `calibration_test.go::TestCalibrationStore_Empty` covers (a) empty store, (b) one key, (c) many keys.
- [ ] **Step 0.2** — `internal/escrow/escrow.go`: add `func (e *Escrow) Empty(ctx context.Context) (bool, error)` under the existing `mu` RLock, returning `len(e.entries) == 0`. Test: `escrow_test.go::TestEscrow_Empty`.
- [ ] **Step 0.3** — `internal/ledger/transfer.go`: add `func (l *TransferLedger) Empty(ctx context.Context) (bool, error)` returning `len(l.entries) == 0 && len(l.archivedNetSettled) == 0`. Test: `ledger_test.go::TestTransferLedger_Empty`.
- [ ] **Step 0.4** — `internal/taskverification/badger_store.go`: add `func (s *BadgerStore) Empty(ctx context.Context) (bool, error)` iterating round keys. Test: `badger_store_test.go::TestBadgerStore_Empty`.
- [ ] **Step 0.5** — `internal/ocs/engine.go`: add `func (e *Engine) PendingEmpty(ctx context.Context) (bool, error)` — named distinctly because `Empty` could be ambiguous on the engine type. Iterates pending via the existing `AllPending` under read lock. Test: `engine_test.go::TestEngine_PendingEmpty`.
- [ ] **Step 0.6** — Commit: `feat(stores): add Empty() probes for projection-registry StateProbe`.

### Task 1 — `TaskVerificationRound.CalibrationIncremented` field

- [ ] **Step 1.1** — `internal/taskverification/round.go`: add `CalibrationIncremented bool \`json:"calibration_incremented,omitempty"\`` to the struct. Update any canonical serialization if present (the struct uses `encoding/json`; confirm no JCS path needs adjustment).
- [ ] **Step 1.2** — `internal/taskverification/round_test.go`: add `TestRound_CalibrationIncrementedFlag_RoundTrip` verifying the field serializes and deserializes.
- [ ] **Step 1.3** — Commit: `feat(taskverification): add CalibrationIncremented field for idempotency guard`.

### Task 2 — Calibration-increment wiring in the consensus consumer

- [ ] **Step 2.1** — Write `internal/recognition/task_verification_consensus_consumer_test.go` test `TestConsensusConsumer_IncrementsCalibrationOnce`: build a fake calibration store, a round with votes from two distinct families, call `Consume` twice (simulating replay), assert `Increment` was called exactly twice (once per family, not per replay).
- [ ] **Step 2.2** — Run; expect FAIL (field and wiring absent).
- [ ] **Step 2.3** — Modify `internal/recognition/task_verification_consensus_consumer.go`:
   - Add field `calibration *taskverification.CalibrationStore` to the consumer struct.
   - Extend `NewTaskVerificationConsensusConsumer` signature with a new `calibration` parameter.
   - After the settlement block and before the slashing block, insert the family-iteration increment loop guarded by `!round.CalibrationIncremented`. Set `round.CalibrationIncremented = true` and `SaveRound` on success.
- [ ] **Step 2.4** — Update `cmd/node/main.go:1906` constructor call to pass `tvCalibrationStore`.
- [ ] **Step 2.5** — Run; expect PASS.
- [ ] **Step 2.6** — Commit: `feat(recognition): wire CalibrationStore.Increment into consensus consumer`.

### Task 3 — Wire `ProjectionRegistry` into `cmd/node/main.go`

- [ ] **Step 3.1** — Add import `"github.com/Aethernet-network/aethernet/internal/projections"` at the top of `cmd/node/main.go`.
- [ ] **Step 3.2** — Add `projReg *projections.ProjectionRegistry` field to `stack` struct (currently line ~317).
- [ ] **Step 3.3** — Construct registry immediately after `tvCalibrationStore` per §Wiring plan #1.
- [ ] **Step 3.4** — Add consolidated `MustRegister` block for all five Canonical projections plus three Advisory projections. Each entry fully populated per §"Store inventory" with `StateProbe` closures capturing the concrete store handle from surrounding scope.
- [ ] **Step 3.5** — Add startup `HealthCheck` invocation + logging.
- [ ] **Step 3.6** — `go build ./...` must succeed.
- [ ] **Step 3.7** — Commit: `feat(node): construct and populate projection registry at startup`.

### Task 4 — Periodic `HealthCheck` in `runLoop`

- [ ] **Step 4.1** — Modify `runLoop` signature (`cmd/node/main.go:2549`) to accept `projReg *projections.ProjectionRegistry`.
- [ ] **Step 4.2** — Add `time.NewTicker(60 * time.Second)` to the select loop; on each tick call `projReg.HealthCheck(ctx)` and log WARN for any `HealthEmpty`/`HealthProbeFailed`.
- [ ] **Step 4.3** — Update the `runLoop` call sites (should be one at the bottom of `cmdStart`) to pass `stack.projReg`.
- [ ] **Step 4.4** — Commit: `feat(node): periodic HealthCheck in runLoop`.

### Task 5 — `/v1/status` integration

- [ ] **Step 5.1** — Add `projReg *projections.ProjectionRegistry` field to the API `Server` struct; add `SetProjectionRegistry` setter following the existing `SetReputationManager` pattern (`internal/api/server.go`).
- [ ] **Step 5.2** — Wire the setter in `cmd/node/main.go:2195` adjacent to the existing `SetReputationManager` call.
- [ ] **Step 5.3** — Modify `handleStatus` at `internal/api/server.go:2718` to include a `projections` field in the response when `s.projReg != nil`: `{"overall": "OK", "entries": 8}` shape.
- [ ] **Step 5.4** — Add `TestServer_StatusExposesProjectionHealth` in `internal/api/server_test.go`.
- [ ] **Step 5.5** — Commit: `feat(api): expose projection registry health on /v1/status`.

### Task 6 — Integration tests (one per Canonical projection)

Five tests total, one per Canonical projection, each under `internal/integration/`.

For each test:
- [ ] Build a minimal node fixture (temp BadgerDB, no network peers) matching the pattern used in existing integration tests.
- [ ] Wire only the subsystems required to exercise the specific live consumer.
- [ ] Assert the store's `Empty(ctx)` returns `true` before the triggering event and `false` after.
- [ ] For calibration specifically: assert that two replays of the same consensus event produce exactly one increment per family (idempotency).

- [ ] **Step 6.1** — `projection_calibration_test.go::TestCalibration_IncrementsOnRoundFinalization` (including idempotency).
- [ ] **Step 6.2** — `projection_escrow_test.go::TestEscrow_HoldsOnTransferOptimistic`.
- [ ] **Step 6.3** — `projection_ledger_test.go::TestTransferLedger_AccumulatesOnSettlement`.
- [ ] **Step 6.4** — `projection_task_verification_round_test.go::TestTaskVerificationRound_PersistsOnTaskSubmitted`.
- [ ] **Step 6.5** — `projection_ocs_pending_test.go::TestOCSPending_AccumulatesOnOptimistic`.
- [ ] **Step 6.6** — Commit: `feat(integration): add per-projection mutation tests`.

### Task 7 — Full-repo verification + squash to final commit

Per Step 2 prompt, step 2 is shipped as a series of commits on the feature branch (this is multi-subsystem and larger than Step 1; one squashed commit is discouraged because review granularity suffers). The branch lands via fast-forward merge to main.

- [ ] **Step 7.1** — `go test -race -count=1 ./...` — full repo, zero failures.
- [ ] **Step 7.2** — `go vet ./...` — clean.
- [ ] **Step 7.3** — `go build ./...` — clean.
- [ ] **Step 7.4** — Review commit history, ensure each commit is reviewable in isolation.
- [ ] **Step 7.5** — Live testnet verification: deploy per CLAUDE.md §4. Nodes come up; `/v1/status` shows `projections.overall = "OK"`; a fresh agent-registration → task → settlement flow runs; `CalibrationStore.Empty()` on each node returns `false` after first round finalization.

---

## Verification plan

Per CLAUDE.md §4:

1. `go test -race ./...` — zero failures across the full repo.
2. Every integration test referenced by a registry entry exists and passes.
3. Node starts without panics; `HealthCheck` returns `HealthOK` (all Canonical projections registered, all at `HealthNotYetEligible` at startup per the `registeredAtEpoch == 0` condition) for a fresh node.
4. Live testnet per CLAUDE.md §4 standard protocol:
   - Build on 44.200.60.102, push to ECR.
   - Wipe DBs, redeploy all 5 nodes.
   - Verify each node logs `projection registered` for all 8 entries.
   - Register fresh agent, wait 30s, verify balance > 0.
   - Post a task; wait for settlement.
   - SSH to each node; hit `/v1/status`; verify `projections.overall == "OK"`.
   - SSH to each node; copy BadgerDB to `/tmp`; inspect keys under `cal:` prefix — must be non-empty on the node that participated in round finalization.
5. Plan ↔ implementation drift check — any deviations noted in this doc in a final "deltas" section before the merge commit lands.

---

## Explicit deferrals (flagged for future steps)

| Item | Reason | Target step |
|---|---|---|
| Real epoch clock | D1 placeholder; Step 4 introduces `ReputationEvidence.EpochIndex` | Step 4 |
| `ValidatorReputationStore` registration | Store is deleted in Step 3 | Step 3/4 (replaced by EvidenceStore) |
| `ChallengeResolutionStore` registration | Does not exist yet; uses `AllowIdleWithJustification` per CR-9 | Step 4+ |
| `StakeManager` + Identity Registry registration | Canonical but scope-expansion; needs its own audit | Follow-up retrofit |
| CI type-graph scan | Tooling | Step 3 |
| `IntegrationTestRef` existence CI check | Tooling | Step 3 |
| Full observability endpoints (tier-3 delayed aggregates, CLI `aet reputation inspect`) | Step 8 | Step 8 |
| Principle 16 amendment to `docs/design-principles.md` | Step 10 | Step 10 |
| `docs/projection-registry.md` | Step 11 | Step 11 |

---

## Sign-off — complete

All eight decisions (D1–D8) plus commit strategy have been resolved by the founder. The resolved form is recorded in each D section above. Implementation proceeds under the seven-commit sequence.

**Historical sign-off prompt (retained for reference)**:

1. **D1** — Use `ValidatorSnapshot.SetVersion()` as the `epochFn` placeholder, with explicit note that Step 4 replaces it? (Alternatives: build a round-height counter in Step 2, or return 0 until Step 4.)
2. **D2** — Add `CalibrationIncremented bool` to `TaskVerificationRound` for idempotency? (Alternative: parallel BadgerDB marker.)
3. **D3** — Which additional stores to register in Step 2: `TaskVerificationRound` BadgerStore + OCS pending (yes), `RoundProgress` as Advisory (yes), `StakeManager` + Identity Registry (defer to follow-up)?
4. **D4** — Classify `AgentReputation` as Advisory?
5. **D5** — Integrate health into existing `/v1/status` response (rather than a new endpoint)?
6. **D6** — Periodic `HealthCheck` at 60-second cadence from `runLoop`?
7. **D7** — Add `Empty(ctx) (bool, error)` to each Canonical store (touches six packages, minimal additive surface per store)?
8. **D8** — Integration tests at `internal/integration/projection_*_test.go`, one file per Canonical projection, each asserting `Empty` flips on the triggering event (plus idempotency for calibration)?

Also please confirm:
- **Commit strategy**: multiple focused commits on the feature branch (not one squashed commit), per §Task 7 rationale? (Step 1 was one squashed commit because it was a single tight primitive; Step 2 spans seven logical units and multi-commit history is friendlier for review.)

**No code will be written until sign-off.**
