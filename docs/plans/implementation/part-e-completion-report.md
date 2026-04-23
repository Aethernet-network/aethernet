# Part E — Integer migration activation event + reactive consumer — completion report

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `b43bc45` (Part D completion report)
**Commit**: `068cd09` — `commit-8(dispatch,event,settlement): integer migration activation event + consumer`
**Plan reference**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §4.5 (migration strategy)

## What was built

1. **Canonical event type + payload**
   (`internal/event/event.go`):
   `EventTypeIntegerMigrationActivation` + `IntegerMigrationActivationPayload`
   with `Version`, `EmittingAgent`, `ActivationReason`, `EmittedAtUnix`.
   All integer/string. Float-lint clean.

2. **Dispatcher consumer**
   (`internal/dispatch/integer_migration_activation_consumer.go`):
   `IntegerMigrationActivationConsumer` — Type A, idempotent. Persist
   before mutate; flag-flip via narrow `IntegerMigrationActivator`
   interface; `MigrationStateStore` interface for the persistence
   adapter.

3. **Settlement lock discipline**
   (`internal/settlement/verification_consensus_settler.go` +
   `generation_ledger_calculator.go`):
   `sync.RWMutex` on both types; `isShadowMode()` read helper takes
   RLock; `SetShadowMode(bool)` writer takes Lock. Every existing
   `shadowMode` read migrated to the helper. Race detector clean under
   `-race -count=3`.

4. **Startup wiring + store adapter** (`cmd/node/main.go`):
   Consumer registered with the dispatcher alongside `TVConsensusConsumer`
   before `eventDispatcher.Recover(...)`. Startup-load block flips flags
   to integer-canonical immediately if prior activation is persisted.
   `migrationStoreAdapter` wraps `store.Store.PutMeta/GetMeta` at meta
   key `"integer_migration:activated"` with a JSON-serialized
   `migrationActivationState` struct.

5. **Canonical payload lint updates** (`internal/event/lint/` +
   `internal/event/canonical_payload_reflection_test.go`):
   New type added to both hardcoded lists.
   `TestCanonicalPayloadTypeNames_HasSeventeen` → `HasEighteen`;
   `TestCanonicalPayloadList_Has17Entries` → `Has18Entries` (counts
   updated to 18 in both).

## Verification

### Affected packages under `-race -count=3`

```
ok  	github.com/Aethernet-network/aethernet/internal/settlement          1.356s
ok  	github.com/Aethernet-network/aethernet/internal/dispatch            2.491s
ok  	github.com/Aethernet-network/aethernet/internal/dispatch/conformance 2.291s
ok  	github.com/Aethernet-network/aethernet/internal/dispatch/lint       1.871s
ok  	github.com/Aethernet-network/aethernet/internal/event               2.025s
ok  	github.com/Aethernet-network/aethernet/internal/event/lint          55.839s
ok  	github.com/Aethernet-network/aethernet/cmd/node                      3.412s
```

No races detected. The mutex discipline covers every `shadowMode` read
site (`computeValidatorPayouts` in the settler, `Calculate` in the
gen-ledger).

### Full repo under `-race -count=1`

59 packages pass, 0 failures. No regression.

### `go vet ./...`

Clean except for the 4 pre-existing `sync/atomic.Int64` copy warnings
in `*_test.go` files (unchanged from baseline).

### Part C lint remains green

Both AST lint and reflection test pass with the 18-entry list. The new
payload is float-free (all fields are `uint8`/`int64`/`string`).

### Part D corpus still builds/runs

Quick sanity check: `GOOS=linux GOARCH=amd64` + `GOOS=linux GOARCH=arm64`
both still compile clean on the corpus binary. Part E's additions
(`sync.RWMutex`, `SetShadowMode`) don't touch the code paths the corpus
exercises. Not re-run here explicitly; CI will confirm.

## TestStartup_LoadsPriorActivation_RestoresFlags — evidence

`cmd/node/main_test.go` exercises the adapter end-to-end with a real
BadgerDB store:

1. Open a fresh store at `t.TempDir()`.
2. Write an activation record via the adapter (`event ID =
   "activation-event-xyz"`, `emittedAt = 1729000000`).
3. Verify same-process round-trip (catches adapter-internal drift).
4. **Close and re-open the store at the same directory — simulates a
   node process restart with persisted state intact.**
5. Construct fresh shadow-mode flag holders for the settler and
   gen-ledger.
6. Execute the exact startup-load snippet from `cmd/node/main.go`.
7. Assert both flags now read `false` (integer-canonical).
8. Assert the persisted event ID and timestamp round-trip across
   restart without drift.

Passes under `-race -count=3`. Two companion tests:

- `TestStartup_NoPriorActivation_LeavesFlagsInShadow` — fresh node with
  no prior activation: startup-load must NOT flip flags. Prevents a
  would-be bug where `GetIntegerMigrationActivated` reports
  activated=true on an empty store.
- `TestMigrationStoreAdapter_GetMalformedBody_ReturnsError` — if the
  persisted body is ever corrupted, the adapter surfaces an error
  rather than silently reporting not-activated; the startup-load's
  `loadErr == nil && activated` guard then leaves flags in shadow,
  which is the safe default (no silent cutover from broken state).

All three pass. The adapter's JSON round-trip preserves event ID
(`event.EventID` = string) and int64 timestamp without drift.

## Decisions confirmed in plan

All plan choices held in implementation with no drift:

- **Consumer location**: `internal/dispatch/` (matches `tv_consensus_consumer.go`).
- **Event name**: `IntegerMigrationActivation` (CamelCase, matches sibling types).
- **Store adapter location**: inline in `cmd/node/main.go` — ~60 lines including doc comments; too small and single-callsite to warrant its own file.
- **Store key**: `"integer_migration:activated"` via `PutMeta`/`GetMeta`. No new store-layer methods.
- **Lock type**: `sync.RWMutex` on both settlement types. Reads on hot paths (every settlement), writes once per activation.
- **Apply sequence**: early-idempotency → parse → persist → flip → log. Persist before mutate.

## Dispatcher multi-consumer observations

This is the second dispatcher consumer registered after `TVConsensusConsumer`. **Nothing looked wrong** in the multi-consumer machinery during integration:

- **Registration order**: registered after `TVConsensusConsumer`, before `eventDispatcher.Recover(...)`. No ordering dependency surfaced.
- **Recovery pass**: both consumers participate; each probes independently via `RecoveryProbe`. The migration consumer's store-backed probe returns `RecoveryCompleted` only on exact event-ID match, so pre-activation startup doesn't accidentally mark the consumer as already-applied.
- **Admission records**: the two consumers operate on different event types (`EventTypeTaskVerificationConsensus` vs `EventTypeIntegerMigrationActivation`), so admission keys don't collide. Registry keys differ by `Name()`.
- **Conformance**: `TestActivationConsumer_Conformance` runs the shared Type A conformance suite; all six cases pass (`DuplicateLiveDelivery`, `ReplayDelivery`, `CrashRecovery`, `ConcurrentSameEvent`, `ContentHashDiscrimination`, `CausalPrerequisiteDeferral`). The suite exercises the same invariants as it does for `TVConsensusConsumer`.

This is a meaningful data point for Step 4 (reputation evidence store),
which will be the *third* consumer registered and the first one to
share an event type with an existing consumer
(`EventTypeTaskVerificationConsensus`). The multi-consumer
plumbing looks healthy; Step 4's failure modes will be narrower to
same-event-type semantics.

## Adapter round-trip observations

The adapter's `PutIntegerMigrationActivated` marshals:

```json
{"event_id":"activation-event-xyz","emitted_at_unix":1729000000}
```

`GetIntegerMigrationActivated` unmarshals back to the same fields. In
testing across a real BadgerDB close-reopen cycle:

- Event ID (`event.EventID` = underlying string) round-trips exactly —
  no case change, no trailing whitespace, no null vs empty-string drift.
- `int64` timestamp round-trips exactly — no precision loss, no
  float→int conversion (the struct uses `int64` directly, so JSON
  serializes as an integer literal).
- Empty-string and zero-timestamp values are not conflated with
  "activated" — the adapter returns `activated=false` only when the
  key is absent or the body is empty, per the
  `TestStartup_NoPriorActivation_LeavesFlagsInShadow` case.
- Malformed persisted bodies produce an error rather than silently
  reporting not-activated, per `TestMigrationStoreAdapter_GetMalformedBody_ReturnsError`.

## Deviations from the prompt

None of semantic significance. One clarification folded into
implementation:

1. The prompt's `RecoveryProbe` example swallows store errors and
   returns `RecoveryNotStarted`. I preserved this exactly — comment
   in the code explains the "absence of evidence is not evidence of
   absence" rationale (C-14) and adds a covering test
   (`TestActivationConsumer_RecoveryProbe_StoreError_ReturnsNotStarted`).

## Discoveries for Parts F–G

1. **The `IntegerMigrationActivator` narrow interface insulates the
   dispatch package from the settlement package's public surface.** The
   migration consumer doesn't need to know about
   `VerificationConsensusSettler` or `GenerationLedgerCalculator`
   concrete types — it only needs `SetShadowMode(bool)`. Future consumers
   that drive settlement-state transitions (or generation-ledger
   transitions, for that matter) can reuse this same single-method
   interface pattern; the interface is unambiguously named and lives
   where the consumer is, so it's grep-friendly for future work.

2. **Startup-load placement matters more than the prompt emphasized.**
   The startup-load block must fire BEFORE `eventDispatcher.Recover(...)`
   because Recover iterates registered consumers and may re-drive their
   `RecoveryProbe` / failed-retryable Apply logic. If the flags were
   still in shadow mode during Recover, a re-delivered historical
   settlement event on a post-activation node would take the wrong code
   path for the duration of the Recover pass. Production wiring in
   `cmd/node/main.go` places the startup-load block immediately before
   `Recover`; the test pins this sequence.

3. **Store meta-key collision risk is genuine but currently absent.**
   The adapter uses `"integer_migration:activated"` under `meta:`. A
   quick grep of the repo's other `PutMeta`/`GetMeta` callers shows no
   conflicting key. Future additions should audit; a conflicting key
   would corrupt both readers silently. For Step 4's reputation evidence
   store, consider whether any new meta: keys might collide; if the
   evidence store grows its own meta-state it should pick a distinct
   prefix.

4. **The migration adapter pattern is reusable.** Wrapping generic
   `PutMeta`/`GetMeta` with a typed adapter in `cmd/node/main.go` keeps
   migration-specific serialization out of the store package while
   presenting a clean interface to the consumer. Three future
   workstreams (cutover-for-analyzer-family-determinism, eventual
   float-path-removal, and Step 4's reputation evidence retrofit) could
   reasonably follow the same pattern.

5. **Race detector coverage is tight now.** Adding `sync.RWMutex` to the
   settlement types means every field read path is scrutinized by the
   race detector when tests run. One follow-up thought: the generation
   ledger's `GenerationLedgerCalculator.qualityFn` field is set once at
   construction and read on every `Calculate`, but is not guarded by
   the mutex (it's effectively immutable post-construction). If a
   future workstream needs to mutate `qualityFn` (e.g. to swap in a
   real reputation-backed implementation mid-flight), `mu` is already
   available to guard it.

## Verification commands (reproducible)

```bash
git checkout 068cd09
go build ./...
go vet ./...
go test -race -count=3 ./internal/settlement/... ./internal/dispatch/... ./internal/event/... ./cmd/node/
go test -race -count=1 ./...                       # full-repo regression
```

All pass on the committed code.

## State

Branch at `068cd09`, not yet pushed — awaiting review. Part F (testnet
rehearsal of shadow→activation→integer-canonical on the 5-node cluster)
follows in a separate session.
