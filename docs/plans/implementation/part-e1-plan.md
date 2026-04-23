# Part E.1 — General dispatcher admission router — implementation plan

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `c4fc190` (Part F completion report)
**Scope**: Single commit. Closes the bug class discovered by Part F Phase D.
**Source prompt**: architect session output, 2026-04-22.

---

## 0. Problem reminder

Part F Phase D: `IntegerMigrationActivationConsumer` is registered on the dispatcher, but no recognition-layer code forwards `EventTypeIntegerMigrationActivation` events into `dispatcher.Admit()`. The only existing admission path is the one inline in `recognition.TaskVerificationConsensusConsumer.Consume` (line 120), which is hard-coded to forward TaskVerificationConsensus events. Every new canonical event type with a dispatcher consumer needs an equivalent hand-written recognition-layer partner, or its dispatcher consumer never runs. That asymmetry is the bug class.

Fix: one recognition-layer `DispatcherAdmissionConsumer` that forwards *every* committed event to `dispatcher.Admit(ev)`. The dispatcher's per-consumer `Interested()` method routes each event to the right consumers or discards it. New dispatcher consumers become additive: `dispatcher.Register(consumer)` and nothing else.

---

## 1. Code to add — `DispatcherAdmissionConsumer`

**File**: `internal/recognition/dispatcher_admission_consumer.go` (new).

**Interface**: `recognition.CommitConsumer` (confirmed from `internal/recognition/consumer.go:34-56`). Required methods: `Name()`, `Interested(ev)`, `Ready(ctx, ev, view)`, `Consume(ctx, ev)`. **Not** `Handle(...)` — the prompt's sketch used `Handle`; the actual interface uses `Consume`.

**Shape**:

```go
package recognition

import (
    "context"
    "log/slog"

    "github.com/Aethernet-network/aethernet/internal/event"
)

// DispatcherAdmitter is the subset of dispatch.Dispatcher this consumer
// calls. Defined locally so the recognition package does not import the
// dispatch package. (Moved here from task_verification_consensus_consumer.go
// as part of Part E.1.)
type DispatcherAdmitter interface {
    Admit(ctx context.Context, ev *event.Event) error
}

// DispatcherAdmissionConsumer is a recognition-layer CommitConsumer that
// forwards every committed event to the dispatcher's admission surface.
// Per-event-type routing is the dispatcher's job via per-consumer
// Interested() methods; this consumer deliberately does not filter.
//
// Closes the bug class surfaced by Part F Phase D: canonical event types
// with dispatcher consumers but no recognition-layer responsibility
// previously had no admission pathway (the only forwarding call lived
// inside recognition.TaskVerificationConsensusConsumer and was hard-coded
// to TaskVerificationConsensus events). Dispatcher consumers for any
// other event type — including EventTypeIntegerMigrationActivation —
// received zero events and their Apply methods never ran.
//
// Idempotency: dispatch.Dispatcher.Admit is idempotent per (event_id,
// consumer_name) via its admission-state-machine. Re-admission during
// replay or duplicate commits is safe.
//
// Ordering: the recognition bus invokes consumers for a single event
// sequentially inside one worker goroutine but in non-deterministic
// order across consumers (map iteration). This is safe here because the
// admission router's work is independent of every other recognition
// consumer's work — the dispatcher consumers it feeds read from the
// payload and from replay-safe ledger state, not from recognition-
// consumer side effects.
type DispatcherAdmissionConsumer struct {
    dispatcher DispatcherAdmitter
}

// NewDispatcherAdmissionConsumer constructs the router.
func NewDispatcherAdmissionConsumer(d DispatcherAdmitter) *DispatcherAdmissionConsumer {
    return &DispatcherAdmissionConsumer{dispatcher: d}
}

// Name returns the unique consumer identifier.
func (c *DispatcherAdmissionConsumer) Name() string { return "dispatcher_admission_router" }

// Interested returns true for every event. Per-type routing is the
// dispatcher's responsibility via per-consumer Interested().
func (c *DispatcherAdmissionConsumer) Interested(_ *event.Event) bool { return true }

// Ready is always true. The dispatcher has its own prerequisite system
// (C-8 consumers declare prerequisites; the dispatcher gates admission
// on DAG presence). The recognition bus must not defer here.
func (c *DispatcherAdmissionConsumer) Ready(_ context.Context, _ *event.Event, _ ReadModel) (bool, string, error) {
    return true, "", nil
}

// Consume forwards the event to dispatcher.Admit. Errors are logged at
// WARN level but not returned: recognition-layer commit processing must
// not be blocked by dispatcher issues. The dispatcher has its own
// recovery path (RecoveryProbe at startup) which re-attempts admission
// after a node restart.
func (c *DispatcherAdmissionConsumer) Consume(ctx context.Context, ev *event.Event) error {
    if err := c.dispatcher.Admit(ctx, ev); err != nil {
        slog.Warn("dispatcher_admission_router: admit failed",
            "event_id", ev.ID,
            "event_type", ev.Type,
            "err", err,
        )
    }
    return nil
}

// Compile-time assertion.
var _ CommitConsumer = (*DispatcherAdmissionConsumer)(nil)
```

Key deliberate choices:

- **No event-type filter.** `Interested` returns `true`. The dispatcher filters per-consumer. This is the whole point of the refactor.
- **No Ready-gate.** The bus's deferral mechanism (`SetDeferred`) is for consumers with explicit prerequisites in the recognition layer. The dispatcher has its own prereq system (C-8). Layering them would double-count; returning `(true, "", nil)` keeps the router simple.
- **Log-and-swallow errors.** Per the prompt's rule 2: recognition should not be blocked by dispatcher issues. The `slog.Warn` line preserves operator visibility.
- **Signature of `Consume(ctx, ev)` matches `CommitConsumer`.** No `CommitRecord` parameter on this method — the interface passes only the event to `Consume`. Verified in `consumer.go:55`.

## 2. Refactor `TaskVerificationConsensusConsumer`

**File**: `internal/recognition/task_verification_consensus_consumer.go`.

Remove:

- The `DispatcherAdmitter` interface definition (lines 20-25) — **moved to the new file**, so delete from here.
- The `dispatcher DispatcherAdmitter` field on the struct (line 30).
- The `SetDispatcher` method (lines 51-56).
- **The entire settlement-invocation block at lines 114-139** (both the `if c.dispatcher != nil` branch AND the `else if c.settler != nil` fallback). Settlement invocation is no longer this consumer's responsibility; it happens exclusively via admission-router → dispatcher → `dispatch.TVConsensusConsumer.Apply` → settler.

Keep:

- Round state finalization (lines 93-112). This is local-node replay-safe work.
- Calibration counters (lines 147-182). Independent of settlement; runs best-effort.
- Slashing evaluation (lines 186-198). Independent of settlement; runs best-effort.
- The `settler` field and its constructor parameter? **No — remove.** Once the dispatcher/fallback block is gone, the field is dead code. Removing it shrinks the constructor signature cleanly.

### Rationale for the Option X decision (settler field removed)

The prompt says "Keep everything else." Reading that literally would have me keep the `settler` field unused. But: leaving an unused field on the struct and an unused constructor parameter is strictly worse than removing it — it confuses readers about what the consumer does and it persists the pre-commit-9 compatibility surface that no longer has any caller. The refactor's whole intent is to make TV consumer responsibility narrower (round + calibration + slashing only); the cleanest expression is removing the field. I'm flagging this as a deliberate deviation from the prompt's exact wording in §4 below so the review catches it.

Alternative kept on the table: if the reviewer prefers strict "keep everything else," the alternative is to leave the field and constructor parameter but remove only the Admit + fallback. In that case the constructor call site doesn't change. Either is fine — my recommendation is remove.

### Interaction-safety verification

The refactor separates two consumers that previously ran sequentially inside one `Consume` call (recognition TV consumer): round-finalization and dispatcher-admit. Post-refactor, they run as two independent bus consumers on the same event, in non-deterministic order.

Confirmed safe by inspection:

- `dispatch.TVConsensusConsumer.Apply` (at `internal/dispatch/tv_consensus_consumer.go:130-182`) reads from:
  - the event payload (`payload.TaskID`, `payload.RoundID`, `payload.FinalVerdict`, `payload.FinalScoreBP` — all on the event, not on the round struct);
  - `c.taskMgr.Get(payload.TaskID)` — task status, set by TaskLifecycle consumer earlier in the chain;
  - `c.escrowMgr.HasSettlementStarted(...)` — escrow state, set by prior settlements;
  - `c.rounds.LoadRound(ctx, roundID)` — the round, which contains `round.Category` (set at creation) and `round.Votes` (populated by vote consumer before TVConsensus arrives).
  - The settler (line 174) uses `round.Category` (line 222, 293 of settler) and `round.Votes` (line 370 of settler). **Neither depends on the TVConsensus recognition consumer having already transitioned the round to terminal state on *this* event.**
- The recognition TV consumer's work (round-finalization + calibration + slashing) reads from the event payload and the round, writes round state via SaveRound, but does not read from any state that the dispatcher would mutate first.
- The recognition TV consumer calls `c.rounds.SaveRound(...)` at three sites (line 101 for finalization, line 176 for calibration, plus one on error). The dispatcher-side TVConsensusConsumer does NOT call SaveRound — it only LoadRounds. So no Save-vs-Save race.

Order-of-execution therefore doesn't matter for correctness. Confirmed.

## 3. Startup wiring in `cmd/node/main.go`

Current structure (lines 1960-2006):

```go
tvConsensusConsumer := recognition.NewTaskVerificationConsensusConsumer(tvStore, tvSettler, tvSlashingEvaluator, tvCalibrationStore)

eventDispatcher := dispatch.NewDispatcher(...)
if err := eventDispatcher.Register(tvDispatchConsumer); err != nil { ... }
// ... migConsumer setup + startup-load ...
if err := eventDispatcher.Register(migConsumer); err != nil { ... }
if err := eventDispatcher.Recover(context.Background()); err != nil { ... }
tvConsensusConsumer.SetDispatcher(eventDispatcher)   // ← DELETE
stack.dispatcher = eventDispatcher

_ = commitBus.Register(tvConsensusConsumer)
```

New structure:

```go
// tvSettler dropped from constructor (see Option X in plan §2).
tvConsensusConsumer := recognition.NewTaskVerificationConsensusConsumer(tvStore, tvSlashingEvaluator, tvCalibrationStore)

eventDispatcher := dispatch.NewDispatcher(...)
if err := eventDispatcher.Register(tvDispatchConsumer); err != nil { ... }
// ... migConsumer setup + startup-load ...
if err := eventDispatcher.Register(migConsumer); err != nil { ... }
if err := eventDispatcher.Recover(context.Background()); err != nil { ... }
// tvConsensusConsumer.SetDispatcher deleted — admission router handles it.
stack.dispatcher = eventDispatcher

// Part E.1: general recognition→dispatcher admission router. Every
// committed event is forwarded to dispatcher.Admit; per-consumer
// Interested() filtering inside the dispatcher handles routing.
admissionRouter := recognition.NewDispatcherAdmissionConsumer(eventDispatcher)
if err := commitBus.Register(admissionRouter); err != nil {
    slog.Error("recognition: register DispatcherAdmissionConsumer failed", "err", err)
    os.Exit(1)
}

_ = commitBus.Register(tvConsensusConsumer)
```

**Variable name confirmed**: the recognition bus variable is `commitBus` (from `stack.commitBus` and the bus-construction at `cmd/node/main.go:1800`). Matches existing pattern.

**Ordering rule**: admission router registered AFTER all dispatcher consumers are registered AND `dispatcher.Recover()` has run. This matches the prompt's requirement and is satisfied by placing the `commitBus.Register(admissionRouter)` call AFTER the `stack.dispatcher = eventDispatcher` assignment and BEFORE `commitBus.Start()` (line 2068). No event is admitted before its downstream consumer is ready because `commitBus.Start()` is called after all consumers are registered.

## 4. Test strategy

**New test file**: `internal/recognition/dispatcher_admission_consumer_test.go`.

Three tests — these are the regression guard for the bug class:

### 4.1 `TestDispatcherAdmissionRouter_ForwardsToDispatcher_IntegerMigrationActivation`

Purpose: directly exercises the Part F Phase D failure mode. If this test passes, the bug is closed.

Setup: construct a recognition `Bus` (or use the router in isolation), a real `dispatch.Dispatcher`, register a real `dispatch.IntegerMigrationActivationConsumer` with stub `settler` + `genLedger` + `store` (matching the pattern in `internal/dispatch/integer_migration_activation_consumer_test.go`), wire the admission router between them.

Exercise: construct an `EventTypeIntegerMigrationActivation` event, emit through the bus (or call the router directly if bus-level assertion is awkward), wait for dispatch.

Assert:

- The stub settler's `SetShadowMode(false)` was called.
- The stub gen-ledger's `SetShadowMode(false)` was called.
- The stub store's `PutIntegerMigrationActivated` was called with the event's ID.

### 4.2 `TestDispatcherAdmissionRouter_ForwardsToDispatcher_TaskVerificationConsensus`

Purpose: proves the refactor preserves the existing TVConsensus flow. Before: TV consumer forwarded to dispatcher. After: admission router forwards to dispatcher. Both routes must reach `dispatch.TVConsensusConsumer.Apply`.

Setup: admission router wired, dispatcher with a stub `dispatch.Consumer` that records `Apply` invocations, admission router emits a TVConsensus event.

Assert: stub consumer's Apply was called with the event.

### 4.3 `TestDispatcherAdmissionRouter_ForwardsToDispatcher_UnknownType_DispatcherFilters`

Purpose: proves the router does not incorrectly apply dispatcher logic to events no consumer cares about. This is the "admission router forwards blindly, dispatcher's Interested() filters" invariant.

Setup: admission router wired, dispatcher with a consumer that only declares `Interested` for IntegerMigrationActivation (i.e., stubs `Interested` to return `true` only for that type). Emit a `TaskPosted` event through the router.

Assert: dispatcher's `Admit` ran (log-level or observable), but the stub consumer's `Apply` was NOT called (the dispatcher's internal `snapshotInterestedConsumers` at `dispatcher.go:168-176` correctly filtered).

Note on assertion method: `dispatcher.Admit()` returns `nil` when no consumer is interested (line 113-115 of dispatcher.go). That behavior means the caller can't distinguish "forwarded and ignored" from "forwarded and applied" without observing downstream side effects. The stub consumer's apply-count remaining at 0 is the observable.

### 4.4 Updates to existing tests

Grep confirmed: `internal/recognition/task_verification_consensus_consumer_test.go` does NOT reference `SetDispatcher`, `DispatcherAdmitter`, or `dispatcher.Admit`. So the TV consumer's existing test file needs no changes for the dispatcher removal.

However, if I remove the `settler` parameter from the constructor (Option X), tests that call `NewTaskVerificationConsensusConsumer(tvStore, tvSettler, ...)` will break. Grep expected in the test file; I'll update those callsites to match the new signature.

If I keep the `settler` parameter (Option Y — strict prompt compliance), test callsites remain unchanged. This is the fall-back if the reviewer prefers literal prompt adherence.

### 4.5 Existing `internal/dispatch/` tests

None should change. The admission router lives in `recognition`, not `dispatch`. The dispatcher's interface is unchanged.

## 5. Deviations from the prompt (to flag for review)

1. **`settler` field removed from `TaskVerificationConsensusConsumer`** (Option X). The prompt says "Keep everything else," which literally would leave the field dead. I'm proposing removing it to keep the consumer's surface clean. Reviewer: flip to Option Y (keep the field unused) if strict prompt adherence preferred; the constructor-call-site updates in `cmd/node/main.go:1960` would be unchanged in that case.

2. **Interface method `Consume`, not `Handle`.** The prompt's code sketch used `Handle(ctx, record, ev)`; the actual `recognition.CommitConsumer` interface uses `Consume(ctx, ev)` (verified in `consumer.go:55`) and `Ready(ctx, ev, view)`. The prompt flagged this possibility ("The exact method name (`Handle` above) depends on the existing CommitConsumer interface. Read the interface in consumer.go ... and conform exactly."). Conforming.

3. **`CommitRecord` not threaded through.** The prompt's sketch had `Handle(ctx, record, ev)`. The real interface's `Consume` takes only `(ctx, ev)`. The admission router does not need the record's source/replay metadata for its forwarding logic; the dispatcher treats replay and live events identically via its own idempotency (verified in `dispatcher.go:122-124`: `if rec.State == StateApplied { return nil }`).

## 6. Completion criteria (mapped from prompt §6)

| # | Criterion | How to verify |
|---|---|---|
| 1 | `go build ./...` clean | `go build ./...` |
| 2 | `go vet ./...` clean | `go vet ./...` (expect 4 pre-existing atomic.Int64 warnings, unchanged) |
| 3 | Affected-packages race tests clean | `go test -race -count=3 ./internal/recognition/... ./internal/dispatch/... ./internal/settlement/...` |
| 4 | Full-repo regression clean | `go test -race -count=1 ./...` modulo pre-existing flakes |
| 5 | 3 integration tests pass | Explicit assertions in test §4.1-4.3 |
| 6 | Existing TV consumer tests pass | After constructor-signature update if Option X; no changes if Option Y |
| 7 | Single commit on feat branch | Staged artifacts list in plan §7 |
| 8 | Completion report written | `docs/plans/implementation/part-e1-completion-report.md` — see prompt §Completion criteria |

## 7. Staged artifacts for the commit

Files touched:

- **New**:
  - `internal/recognition/dispatcher_admission_consumer.go` (~70 lines)
  - `internal/recognition/dispatcher_admission_consumer_test.go` (~250 lines, 3 tests)
- **Modified**:
  - `internal/recognition/task_verification_consensus_consumer.go` — remove DispatcherAdmitter, field, SetDispatcher, and settlement-invocation block. Net ~–35 lines.
  - `internal/recognition/task_verification_consensus_consumer_test.go` — update constructor signature callsites (Option X only). Net ~±5 lines.
  - `cmd/node/main.go` — delete `SetDispatcher` line, add admission-router registration block, update `NewTaskVerificationConsensusConsumer` call if Option X. Net ~±8 lines.
- **Docs**:
  - `docs/plans/implementation/part-e1-completion-report.md` (new, written after implementation)
  - This plan doc (`docs/plans/implementation/part-e1-plan.md`) can be committed now for review trail, or held in the completion commit.

Single commit, message per the prompt.

## 8. Choice points for founder before I implement

1. **Option X vs Y on the `settler` field** (plan §2 + §5.1). X removes the field cleanly; Y preserves the exact prompt wording at the cost of dead code. I recommend X. Happy to do Y if preferred.
2. **Plan doc in this commit or held separately?** This plan doc (`part-e1-plan.md`) can ship alongside the completion-report commit, or in a docs-only pre-commit for review trail. Default: include in the single commit per the prompt's "single commit" rule.

No other choice points — the rest of the design is locked by the architect session's decisions.

## 9. What NOT to do (reinforcement of prompt §9)

- No event-type filter on the admission router.
- No changes to `dispatch.Dispatcher` API.
- No registering the router as a dispatcher consumer — it's a recognition consumer whose downstream happens to be the dispatcher.
- No fatal-level error handling on admission failures. Log and continue.
- No moving dispatcher consumer registration to the recognition layer.
- No modifications to `protocolmath`, `settlement`, `event`, `taskverification`, or any non-recognition/non-dispatch-wiring code.
- No Part F retry in this session.

## 10. Estimated time + scope

- Code: ~60 min to write consumer + 3 integration tests + refactor TV consumer + wire in main.go.
- Local validation: `go build` + `go vet` + affected-package race tests + full-repo test = ~10 min.
- Completion report: ~30 min.
- Single commit + push: ~5 min.

Total: ~105 min.

---

## Approval gate

This plan is **v1 of Part E.1 implementation**. Founder decision required: approve as written, approve with adjustments on §5 (Option X vs Y) or §8, or kick back with specific concerns.

On approval: execute the plan, produce the commit + completion report, push the branch, do NOT merge to main.

Part F retry is queued for a subsequent session after this lands and is reviewed.

---

**End of Part E.1 plan v1.**
