# Part E.1 — General dispatcher admission router — completion report

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `c4fc190` (Part F completion report)
**Plan reference**: `docs/plans/implementation/part-e1-plan.md`
**Bug of origin**: Part F Phase D, documented in `docs/plans/implementation/part-f-phase-d-failure-brief.md`

---

## What was built

A single recognition-layer `CommitConsumer` that forwards every committed event to `dispatch.Dispatcher.Admit`. Per-event-type routing is the dispatcher's job via its existing per-consumer `Interested()` filter. New dispatcher consumers now land via `dispatcher.Register(consumer)` alone — no hand-written recognition-layer adapter required.

### Files touched

| # | File | Change |
|---|---|---|
| 1 | `internal/recognition/dispatcher_admission_consumer.go` | **new** — the router (~95 lines incl. doc comments) |
| 2 | `internal/recognition/dispatcher_admission_consumer_test.go` | **new** — 3 integration tests + in-package in-memory fixtures (~280 lines) |
| 3 | `internal/recognition/task_verification_consensus_consumer.go` | **modified** — removed `DispatcherAdmitter` type, `dispatcher` field, `settler` field, `SetDispatcher` method, and the entire settlement-invocation block (both the dispatcher-Admit branch and the pre-commit-9 settler fallback). Net −46 lines. |
| 4 | `internal/recognition/task_verification_consensus_consumer_test.go` | **modified** — updated 4 constructor callsites to drop the removed `settler` parameter |
| 5 | `internal/integration/projection_calibration_test.go` | **modified** — same constructor-signature update, 1 callsite |
| 6 | `cmd/node/main.go` | **modified** — removed `SetDispatcher` call, updated `NewTaskVerificationConsensusConsumer(...)` to drop `tvSettler` arg, added `recognition.NewDispatcherAdmissionConsumer(eventDispatcher)` + `commitBus.Register(admissionRouter)` block with an explanatory comment |
| 7 | `docs/plans/implementation/part-e1-plan.md` | **new** — plan doc (included in this commit per the prompt's single-commit rule) |
| 8 | `docs/plans/implementation/part-e1-completion-report.md` | **new** — this file |

## How the refactor closes the bug class

**Before Part E.1.** The only call to `dispatcher.Admit(ev)` outside the dispatch package lived in `internal/recognition/task_verification_consensus_consumer.go:120`, inside `TaskVerificationConsensusConsumer.Consume`. That call was hard-coded to fire only when the consumer processed an event — i.e., only for `EventTypeTaskVerificationConsensus`. The architectural consequence: every new dispatcher consumer that wanted events of a different type required a hand-written recognition-layer partner. Part E omitted that partner for `EventTypeIntegerMigrationActivation`, and no automated test caught it because unit tests exercised the consumer's `Apply` directly without going through the admission path.

**After Part E.1.** `recognition.DispatcherAdmissionConsumer` is registered on the commit bus and forwards every committed event to `dispatcher.Admit` unconditionally (its `Interested()` returns `true` for every event). The dispatcher's per-consumer `Interested()` method is the actual routing filter — exactly the mechanism Part E's `IntegerMigrationActivationConsumer.Interested` was designed to use. `TaskVerificationConsensusConsumer` no longer touches the dispatcher at all; it retains only its local-node replay-safe work (round-state finalization, calibration counters, slashing evaluation).

The 3 integration tests assert on the full seam (bus → router → real dispatcher → registered dispatch.Consumer's Apply), so a regression that re-introduces any form of per-event-type recognition-layer filtering would fail the tests immediately.

## Verification

### Build + vet

```
go build ./...                                    clean
go vet ./...                                      clean (4 pre-existing atomic.Int64 warnings in *_test.go files, unchanged from baseline)
```

### Tests under `-race -count=3` (affected packages)

```
ok  github.com/Aethernet-network/aethernet/internal/recognition          16.384s
ok  github.com/Aethernet-network/aethernet/internal/dispatch              2.398s
ok  github.com/Aethernet-network/aethernet/internal/dispatch/conformance  1.696s
ok  github.com/Aethernet-network/aethernet/internal/dispatch/lint         2.326s
ok  github.com/Aethernet-network/aethernet/internal/settlement            2.503s
```

### Full-repo regression under `-race -count=1`

All 59+ packages pass. No FAIL output. No regression.

### Integration tests added

`internal/recognition/dispatcher_admission_consumer_test.go` adds three tests that exercise the full seam:

1. `TestDispatcherAdmissionRouter_ForwardsToDispatcher_IntegerMigrationActivation` — directly re-creates the Part F Phase D failure mode. Asserts that an `EventTypeIntegerMigrationActivation` event committed to the bus causes the registered dispatcher consumer's `Apply` to run exactly once, with the event's ID recorded as the applied event. **This is the regression guard for the Phase D bug.**
2. `TestDispatcherAdmissionRouter_ForwardsToDispatcher_TaskVerificationConsensus` — proves the refactor preserves the pre-existing admission flow. Asserts that a `TaskVerificationConsensus` event reaches its registered dispatcher consumer via the new router rather than via the now-deleted inline `TaskVerificationConsensusConsumer.Admit` call.
3. `TestDispatcherAdmissionRouter_ForwardsToDispatcher_UnknownType_DispatcherFilters` — proves the router forwards blindly and the dispatcher's `Interested()` filter is what decides whether `Apply` runs. Emits a `TaskPosted` event while only an `IntegerMigrationActivation`-interested consumer is registered, and asserts the consumer's `applyCount` stays at 0 after a conservative wait. If the router ever acquires an inappropriate filter, or if the dispatcher's filter regresses, this test fails.

Each test uses an in-package `memAdmitStore` (mirrors the unexported `memAdmissionStore` in `internal/dispatch/dispatcher_test.go`) and a minimal `DAGAnchorReader` satisfying `dispatch.VerifyAnchor` with any-tip pass-through. A `recordingConsumer` with `atomic.Int64` counters provides the observable side effect.

### TV consumer tests (existing)

All pre-existing tests in `internal/recognition/task_verification_consensus_consumer_test.go` pass with the updated constructor signature. None of them exercised the dispatcher-forwarding path (the `nil` settler passed in every test callsite confirmed they never hit the pre-commit-9 settler fallback either), so the refactor simplifies rather than changes test semantics.

`internal/integration/projection_calibration_test.go` also passes with the updated constructor.

## Interface corrections applied (vs. the prompt's code sketch)

The prompt flagged these as expected-but-possible discoveries during reading. Confirmed from `internal/recognition/consumer.go:34-56`:

1. The primary method on `CommitConsumer` is **`Consume(ctx, ev)`**, not `Handle(...)`.
2. `Consume` takes **no `CommitRecord` parameter**. The admission router does not need the record's source/replay metadata because the dispatcher's admission state machine is idempotent — replay and live events are identical from the dispatcher's perspective (`dispatcher.go:122-124`: `if rec.State == StateApplied { return nil }`).
3. The interface also requires **`Ready(ctx, ev, view) (bool, string, error)`**. The router returns `(true, "", nil)` unconditionally — it does not participate in the recognition bus's deferral mechanism. The dispatcher has its own prereq system (C-8) and gates admission accordingly; layering both would double-count.

## Deviations from the prompt

1. **Option X on `TaskVerificationConsensusConsumer.settler`** (approved during planning). The `settler` field, constructor parameter, and the pre-commit-9 `else if c.settler != nil { settler.Settle(...) }` fallback branch were removed entirely. The alternative (Option Y — keep the field unused) would have left dead code and a misleading constructor signature. The 5 test callsites were updated to drop the `nil` second arg; the main.go callsite dropped its `tvSettler` reference. No downstream caller was silently rewired — `tvSettler` remains used at three other wiring sites in `cmd/node/main.go` (the dispatcher-side `TVConsensusConsumer`, the migration consumer, and the startup-load flag flip), all of which are correct.
2. **Plan doc in this commit** (default choice). `part-e1-plan.md` is included alongside the completion report per the prompt's single-commit rule.

No other deviations.

## Safety verification that was load-bearing for approval

The plan's §2 analysis of parallel bus-consumer execution was approved as the correctness check for the refactor:

- `dispatch.TVConsensusConsumer.Apply` (at `internal/dispatch/tv_consensus_consumer.go:130-182`) reads from the event payload and from `round.Category` + `round.Votes`, both of which are set BEFORE any `TaskVerificationConsensus` event is emitted (round creation and vote accumulation happen on earlier events).
- The round's `FinalVerdict` and `State` fields (transitioned by the recognition TV consumer on the TVConsensus event) are NOT read by `TVConsensusConsumer.Apply` or by `settler.Settle` (`verification_consensus_settler.go:222, 293, 370` all read `round.Category` or `round.Votes`, never `round.FinalVerdict` or `round.State`).
- `dispatch.TVConsensusConsumer` does not call `rounds.SaveRound`; only the recognition consumer does. No Save-vs-Save race.

Therefore the order of execution between the new admission router and the recognition TV consumer on the same TVConsensus event is correctness-irrelevant. Either can run first and the other's inputs are unchanged.

## Unblocks

Part F retry is no longer blocked on this wiring bug. On redeploy:
- An `aet admin activate-integer-migration` invocation will emit the activation event as before.
- The event propagates through the recognition fabric to all 5 nodes' commit buses.
- On each node, the `DispatcherAdmissionConsumer` forwards the event to `dispatcher.Admit`.
- The dispatcher's `Interested()` check routes it to `IntegerMigrationActivationConsumer.Apply`, which persists the activation state via `migrationStoreAdapter`, flips `tvSettler.SetShadowMode(false)` and `genLedgerCalc.SetShadowMode(false)`, and logs `integer_migration_activation: activated` at INFO.
- Subsequent settlements use the integer-canonical path (Part B commit-3/4 flow).

Part F Phase E (post-activation 10-task corpus) and Phase F (restart test) can then proceed as the original plan anticipated.

## State

Branch `feat/canonical-distribution-integer-migration` at the new commit (this commit lands on top of `c4fc190`), not pushed yet — awaiting founder review before push/redeploy.

---

**End of Part E.1 completion report.**
