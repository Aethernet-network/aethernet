# Phase D failure brief — integer-migration activation wiring gap

**Date**: 2026-04-22
**Branch**: `feat/canonical-distribution-integer-migration`
**Binary under test**: `integer-migration-part-f-600f606` (commit `600f606`, image digest `sha256:f006efdb905559b7ee223b20c8d6e26137402b5f4c551a76cae2b1ad33fe68c4`)
**Cluster**: 5-node AWS testnet (all 5 running the Part F binary with `--enable-admin-api`)

---

## 1. Observed sequence

All timestamps UTC on 2026-04-22.

1. **`aet admin activate-integer-migration --reason "..."` invoked** @ 18:02:48 against ALB (`testnet.aethernet.network`).
2. **API responded 201 Created** with:
   - `event_id=2a513ac6b17112b9bb75a0c46d5ce5ab0f16da12b5e24e02d52a66e4fac23761`
   - `emitting_agent=68778249080a8eb0ff20b359baf520ca2c2178ecdf3b1be7510d047be0bee35a` (part-f-operator)
   - `emitted_at_unix=1776880969` (18:02:49 UTC)
   - `activation_reason=Part F rehearsal 2026-04-22: 20-task shadow corpus, 178 shadow_delta lines across 5 nodes, 36/36 int_sum cross-node equivalent, 0 sum_delta violations, 0 max_per_recipient_delta observed`
3. **Local publisher on Node 1 committed the event** @ 18:02:49:
   ```
   2026/04/22 18:02:49 INFO recognition: commit emitted event_id=2a513ac6... type=IntegerMigrationActivation source=local replay=false
   ```
4. **Fast Path propagation to all 4 remote nodes succeeded** within ~1s; each remote node's log contains a matching `network: V1 event received and materialized event_id=2a513ac6... type=IntegerMigrationActivation path=legacy_v1` line.
5. **Expected: `IntegerMigrationActivationConsumer.Apply` runs on each of 5 nodes** producing `slog.Info("integer_migration_activation: activated", ...)` with `event_id=2a513ac6...` — per `internal/dispatch/integer_migration_activation_consumer.go:119`.
6. **Observed: `Apply` never ran on any node.** Monitored 3.5+ minutes post-emit; zero `integer_migration_activation: activated` log lines cluster-wide.

### Per-node scorecard (at 18:06:32 UTC, ~3.5 min post-emit)

| Node | `event_id=2a513ac6...` log mentions | `integer_migration_activation: activated` |
|---|---:|---:|
| 1 (44.200.60.102) | 2 | **0** |
| 2 (3.87.68.158)   | 5 | **0** |
| 3 (100.27.227.231)| 2 | **0** |
| 4 (3.232.95.111)  | 2 | **0** |
| 5 (32.195.67.127) | 2 | **0** |

Node 2's 5 mentions reflect its role as the ALB target that received the POST + the fastpath relay; other nodes show 2 mentions (commit + materialize). Every node has the event in its local DAG; no node ran the consumer's Apply.

No `ERROR`-level log lines reference the event ID or the migration consumer on any node.

## 2. Root cause — missing recognition → dispatcher admission adapter

The IntegerMigrationActivationConsumer is registered with the `dispatch.Dispatcher`, but no code path routes `EventTypeIntegerMigrationActivation` events *into* the dispatcher. The consumer exists in the dispatcher's consumer registry but receives no events to Apply.

### Code evidence — Part E's registration wiring (present and correct)

`cmd/node/main.go:1965-2004`:

```go
eventDispatcher := dispatch.NewDispatcher(stack.store, stack.dag, func() uint64 {
    ...
})
if err := eventDispatcher.Register(tvDispatchConsumer); err != nil {
    ...
}
migStore := &migrationStoreAdapter{store: stack.store}
migConsumer := dispatch.NewIntegerMigrationActivationConsumer(tvSettler, genLedgerCalc, migStore)
if err := eventDispatcher.Register(migConsumer); err != nil {
    slog.Error("dispatch: register IntegerMigrationActivationConsumer failed", "err", err)
}
...
if err := eventDispatcher.Recover(context.Background()); err != nil {
    ...
}
tvConsensusConsumer.SetDispatcher(eventDispatcher)
stack.dispatcher = eventDispatcher
```

Note the last two lines: the *recognition-layer* `tvConsensusConsumer` gets a reference to the dispatcher via `SetDispatcher`. That's how TaskVerificationConsensus events reach the dispatcher. No analogous `SetDispatcher` call exists for IntegerMigrationActivation events because no analogous recognition-layer consumer exists.

### Code evidence — the only dispatcher admission path currently wired

`internal/recognition/task_verification_consensus_consumer.go:20-24`:

```go
// DispatcherAdmitter is the subset of dispatch.Dispatcher used by the
// consensus consumer to route settlement through the dispatcher's
// exactly-once admission machinery.
type DispatcherAdmitter interface {
    Admit(ctx context.Context, ev *event.Event) error
}
```

`internal/recognition/task_verification_consensus_consumer.go:120`:

```go
if err := c.dispatcher.Admit(context.Background(), ev); err != nil {
    ...
}
```

This is the ONLY call to `dispatcher.Admit(...)` in the codebase (grep for `\.Admit(` returns exactly one production callsite outside the dispatch package itself). It fires for `EventTypeTaskVerificationConsensus` events inside the recognition-layer consensus consumer.

### Code evidence — zero routing for IntegerMigrationActivation

```
$ grep -rn "EventTypeIntegerMigrationActivation" internal/ cmd/ --include='*.go' | grep -v _test.go
internal/settlement/generation_ledger_calculator.go:72:  // Driven by the IntegerMigrationActivationConsumer; see SetShadowMode
internal/settlement/verification_consensus_settler.go:70:  // is not run). Called by the IntegerMigrationActivationConsumer when
internal/settlement/verification_consensus_settler.go:92:  // EventTypeIntegerMigrationActivation event is projected, and startup
internal/dispatch/integer_migration_activation_consumer.go:35:  // IntegerMigrationActivationConsumer is the Type A dispatcher consumer
internal/dispatch/integer_migration_activation_consumer.go:36:  // for EventTypeIntegerMigrationActivation events. On Apply it durably
internal/dispatch/integer_migration_activation_consumer.go:77:  return ev.Type == event.EventTypeIntegerMigrationActivation
```

Matches are in settler doc comments, the consumer's own `Interested(ev)` check, and the consumer's doc comment. **No match in the recognition layer.** No code path detects "this is an IntegerMigrationActivation event, route it to the dispatcher" anywhere in the production wire-up.

### Why Part E's unit tests didn't catch this

`TestStartup_LoadsPriorActivation_RestoresFlags` (`cmd/node/main_test.go`) and the consumer's unit test suite (`internal/dispatch/integer_migration_activation_consumer_test.go`) both exercise the consumer's `Apply` method **directly** — they construct an event, pass it to `consumer.Apply(ctx, ev)`, and assert on the post-state. Neither test goes through the dispatcher's admission path, and neither test exercises the recognition-layer routing that would be required to connect an arriving DAG event to the dispatcher's Apply. The gap is between two layers both of which are tested in isolation.

## 3. Impact

**Cluster is safe.** All 5 nodes remain in shadow mode (settler + gen-ledger flag holders both at `shadowMode=true`). The canonical settlement path is still the float arithmetic; the integer arithmetic continues running in shadow for comparison. No ledger divergence, no fork, no consensus issue.

**The emitted activation event sits in the DAG as semantically inert data.** It is a valid canonical event signed by Node 2 (the ALB-routed emitter), content-addressed, present on every node. Any future binary that correctly wires the recognition → dispatcher adapter for `EventTypeIntegerMigrationActivation` will observe this event during its recovery pass, route it through the dispatcher to the consumer's Apply, persist the activation state, and flip the flags — at that point the cutover takes effect.

Alternatively, a new activation event can be emitted after the fix ships; the consumer's early-idempotency check (`c.store.GetIntegerMigrationActivated()` short-circuit) makes double-activation safe — the second Apply is a no-op.

## 4. Blocked downstream work

- **Phase D verification** (the normal "all 5 nodes applied" check) cannot complete.
- **Phase E** (post-activation 10-task corpus showing zero `shadow_delta` lines and byte-identical per-recipient amounts) is blocked — nodes are still in shadow mode.
- **Phase F** (single-node restart test to exercise `migrationStoreAdapter.GetIntegerMigrationActivated` → `SetShadowMode(false)` during startup) is blocked for the same reason; the store has no activation record to restore.

None of Phases E/F would produce meaningful data while the cluster is still in shadow mode.

## 5. What Part F did validate before the block

Phase C's 20-task corpus produced 178 `shadow_delta` log lines across all 5 nodes covering 36 unique settlements (35 observed by all 5; 1 observed by 3). All 36 had byte-identical `int_sum` across every observer, and every line reported `sum_delta=0`. Part F empirically validated the integer path's cross-node determinism claim on live infrastructure — that work is not invalidated by the wiring gap. The activation mechanism is what broke; the math the activation would have switched to is sound.

## 6. Recommended next action — architect session

Design the fix as a scoped follow-up:

- **Option 1 (narrow)**: add a `recognition.IntegerMigrationActivationRoute` consumer that listens for `EventTypeIntegerMigrationActivation` on the commit bus and calls `dispatcher.Admit(ev)`. Mirrors the shape of `TaskVerificationConsensusConsumer` but one-event-type-specific. ~30 LoC + a test + wire-in at `cmd/node/main.go`. Fastest path to unblock.
- **Option 2 (general)**: introduce a recognition-layer "dispatcher-admit-any" consumer that routes every event type with a registered dispatcher consumer, inverted from today's per-consumer-registers-its-own-adapter pattern. Higher-leverage — retires the current asymmetry where each new dispatcher consumer needs a hand-written recognition adapter. Bigger scope, deserves its own plan.

Either option requires the architect session to produce a plan before implementation. Not Part F work.

---

**End of Phase D failure brief.**
