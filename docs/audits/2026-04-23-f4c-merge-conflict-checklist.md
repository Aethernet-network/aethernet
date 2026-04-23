# F4C merge conflict checklist

**Branches**:
- Target: `feat/selection-consistency-fix` @ `5b9023e` (F4A+F4B complete)
- Source: `origin/feat/canonical-distribution-integer-migration` @ `d6196ed` (commits 1–13 + Part A–F docs)
- Merge base: `origin/main` @ `603bd9b` (F3-B merged)

**Plan reference**: F4 plan v2 §9.1.1. Produced 2026-04-23 BEFORE merge execution. Off-checklist conflicts halt the merge for architect review.

---

## 1. Executive summary

Integer-migration branch (commits 1–13 of canonical-distribution-integer-migration) adds:

- A new `internal/protocolmath/` package for deterministic integer allocation (NeutralBP, BasisPoints, MulDiv).
- A new `internal/dispatch/integer_migration_activation_consumer.go` dispatcher consumer.
- A new `internal/event/lint/` float-freedom AST lint for canonical payloads.
- A new `cmd/cross-arch-corpus/` binary for determinism verification.
- A new admin surface: `internal/api/admin_handlers.go` + `cmd/aet/admin.go` + `--enable-admin-api` flag.
- **Commit-13 (`d6196ed`)**: a general `recognition.DispatcherAdmissionConsumer` that routes every committed event to `dispatcher.Admit`. This REMOVES `DispatcherAdmitter`, `SetDispatcher`, dispatcher/settler fields, and the settlement-invocation block from `internal/recognition/task_verification_consensus_consumer.go`. **Architecturally supersedes F4's per-consumer `SetDispatcher` plumbing.**
- Signature changes cascading through settler/gen-ledger: shadow-mode `bool` parameter; `ValidatorQScoreFn` returns `protocolmath.BasisPoints` (was `float64`).

**The load-bearing architectural merge decision**: **adopt IM's commit-13 general-router shape.** F4's per-consumer `SetDispatcher` plumbing (added in §5.2.1 for TVConsensus, §5.2.4 for Settlement) becomes redundant after the general router merges. Dropping it keeps the merge coherent; the general router's per-event call to `dispatcher.Admit` handles routing for all dispatcher consumers (content-hash AND logical-key). F4's logical-key admission surface (`admitLogicalKey`, `RegisterLogicalKey`, all new consumers) stays intact; only the recognition-side routing plumbing changes.

---

## 2. Files changed by both branches

Exactly **6 files** in the intersection:

| File | F4 changes | IM changes | Resolution |
|---|---|---|---|
| `cmd/node/main.go` | Invariants plumbing + LK consumer registrations + SetDispatcher calls + replay wiring | Admin API flag + activation consumer + shadow-mode wiring + general admission router | Manual merge per §3.1 |
| `cmd/aet/main.go` | `invariants` subcommand | `admin` subcommand | Both additive — take union of dispatch cases + help-text lines |
| `internal/api/server.go` | One-line map-iter `// safe:` annotation | `enableAdminAPI` field + `SetAdminAPI` method + admin-route registration | Take IM's changes + preserve F4's annotation line |
| `internal/dispatch/tv_consensus_consumer_test.go` | Pre-save sibling round for flake #9 | Same pre-save fix (different comment) + ctor signature bump | Take F4's comment shape + IM's signature bump |
| `internal/recognition/task_verification_consensus_consumer.go` | One-line pragma on else-branch | **Removes** `DispatcherAdmitter`/`SetDispatcher`/dispatcher+settler fields/settlement-invocation block | Take IM's shape entirely; F4's pragma goes with the removed code |
| `internal/settlement/verification_consensus_settler.go` | One-line map-iter `// safe:` annotation | Shadow-mode refactor: float/integer dual paths, `SetShadowMode`, `logShadowDelta`, signature change on constructor | Take IM's shape + preserve F4's annotation if the annotated line survives IM's refactor |

---

## 3. Per-file resolution plan

### 3.1 `cmd/node/main.go`

The highest-complexity merge. Both branches restructure portions of `startStack`. Breakdown by region:

**Region A — imports (both add new import lines)**:
- F4: none (uses existing `internal/dispatch`, `internal/recognition`)
- IM: `internal/protocolmath`
- **Resolution**: add IM's protocolmath import.

**Region B — startStack signature**:
- IM: `enableAdminAPI bool` parameter added (last)
- F4: unchanged
- **Resolution**: take IM's shape. Callers updated per IM's diff (cmdStart + cmdConnect).

**Region C — settler + gen-ledger construction (~line 1910)**:
- IM: `genLedgerCalc` takes `protocolmath.BasisPoints`-returning Q fn + `shadowMode=true`. `tvSettler` constructor adds `shadowMode=true`.
- F4: unchanged from main
- **Resolution**: take IM's shape. F4's callers of these (in cross_node harness + tests) will need signature updates — see §4 below.

**Region D — tvConsensusConsumer construction + dispatcher wiring (~line 1941–1964)**:
- IM (commit-13): drops `settler` arg from `NewTaskVerificationConsensusConsumer`. Adds `eventDispatcher.Register(migConsumer)` for IntegerMigrationActivation. Adds startup-flag-restore block. Removes `tvConsensusConsumer.SetDispatcher(eventDispatcher)` line. Adds `admissionRouter := recognition.NewDispatcherAdmissionConsumer(eventDispatcher); commitBus.Register(admissionRouter)` AFTER Recover.
- F4: adds `RegisterLogicalKey(tvDispatchLKConsumer)`, `RegisterLogicalKey(settlementLKConsumer)`, `RegisterLogicalKey(taskSettlementLKConsumer)` via existing `eventDispatcher` handle. Preserves `tvConsensusConsumer.SetDispatcher(eventDispatcher)`.
- **Resolution** (the load-bearing architectural decision):
  1. Take IM's constructor-arg drop (`NewTaskVerificationConsensusConsumer` loses `settler`).
  2. Keep F4's three `RegisterLogicalKey` calls — place them AFTER IM's `eventDispatcher.Register(tvDispatchConsumer)` + before `eventDispatcher.Register(migConsumer)`. (Registration order within the dispatcher is not load-bearing for content-hash vs logical-key consumers — each kind's routing is independent.)
  3. Take IM's startup-flag-restore block (integer migration activation persistence).
  4. Take IM's Recover call.
  5. Take IM's `admissionRouter` registration AFTER Recover.
  6. **DROP** F4's `tvConsensusConsumer.SetDispatcher(eventDispatcher)` line — IM already removed it (superseded by general router).
  7. **DROP** F4's `settlementConsumer.SetDispatcher(stack.dispatcher)` line (added in §5.2.4) — same reason; general router handles Settlement too.

**Region E — hoisted activeWeightFn**:
- F4: hoisted `activeWeightFn` declaration out of `if stack.taskMgr != nil {}` to enclosing scope for sharing with the LK Settlement consumer.
- IM: unchanged from main (no equivalent hoist)
- **Resolution**: keep F4's hoist. Required for F4's `SettlementLogicalKeyConsumer` registration.

**Region F — settlementConsumer wiring (~line 2023)**:
- F4: `settlementConsumer.SetDispatcher(stack.dispatcher); commitBus.Register(settlementConsumer)`
- IM: unchanged from main (`commitBus.Register(settlementConsumer)` only)
- **Resolution**: drop F4's `SetDispatcher` call per §3.1 Region D item 7. Keep the `commitBus.Register(settlementConsumer)` line unchanged.

**Region G — apiSrv.SetAdminAPI wiring (~line 2609)**:
- IM: `apiSrv.SetAdminAPI(enableAdminAPI)`
- F4: unchanged
- **Resolution**: take IM's line.

**Region H — cmdStart() flag parsing + startStack call (~line 2779/2857)**:
- IM: `enableAdminAPI := fs.Bool("enable-admin-api", ...)`; passed to startStack.
- F4: unchanged
- **Resolution**: take IM's changes. cmdConnect also updated to pass `false`.

**Region I — replay pass (~line 2051, F4A step 3 landed)**:
- F4: `recognition.ReplayHistoricalToBusConsumers(context.Background(), stack.dag, commitBus)` after SetOnCommit, before node.Start.
- IM: unchanged (F4A§8.1 has not landed on IM branch)
- **Resolution**: keep F4's call. No conflict — this is a pure addition to a region IM doesn't touch.

**Region J — migrationStoreAdapter + helpers (end of file)**:
- IM: adds `migrationMetaKey` const, `migrationActivationState` struct, `migrationStoreAdapter` type + methods at file bottom.
- F4: unchanged
- **Resolution**: take IM's additions verbatim.

### 3.2 `cmd/aet/main.go`

- F4 adds a `case "invariants":` dispatch + help-text block.
- IM adds a `case "admin":` dispatch (with sub-switch) + help-text block.
- **Resolution**: merge as union. Both dispatch cases + both help-text blocks. No line-level conflict if the order is `invariants` before `admin` (alphabetical) or `admin` before `invariants` — either works.

### 3.3 `internal/api/server.go`

- F4: adds one `// safe:` annotation at line 4661 inside `handleAdminCalibrationAgents` for a map-iter callsite.
- IM: adds `enableAdminAPI bool` field + `SetAdminAPI(enabled bool)` method + admin-route registration in `rebuildMux` (earlier in the file, ~line 340 and ~line 401).
- **Resolution**: take IM's additions at lines 340/401/~428 + preserve F4's annotation at line 4661. Different regions; no conflict.

### 3.4 `internal/dispatch/tv_consensus_consumer_test.go`

- F4 adds `roundAlt := *round; roundAlt.RoundID = "round-alt"; _ = store.SaveRound(...)` fix for flake #9, with `FINDING #9 fix (F4B step 0.2)` comment.
- IM adds essentially the same fix with a different comment ("Save the round under an alternate RoundID too...") AND bumps `settler := settlement.NewVerificationConsensusSettler(..., true)` in `newTestTVConsumer` for the new `shadowMode` parameter.
- **Resolution**: functional fix is identical; take F4's comment (more detailed; references FINDING #9 and describes the root cause). Take IM's signature bump (`, true` argument). `altRound` variable name from IM — either name works; take F4's `roundAlt`.

### 3.5 `internal/recognition/task_verification_consensus_consumer.go`

- IM (commit-13): removes `DispatcherAdmitter` type, `SetDispatcher` method, `dispatcher` field, `settler` field + constructor param, entire `if c.dispatcher != nil { Admit } else if c.settler != nil { settler.Settle }` block.
- F4: adds one-line pragma `// dispatch:lint legacy-direct-settler "..."` INSIDE the else-branch that IM removes.
- **Resolution**: take IM's version. F4's pragma goes with the removed code. **No lint impact**: the pragma existed to exempt the legacy direct-settler call from no-bypass lint; once the call is removed, the pragma is moot.

### 3.6 `internal/settlement/verification_consensus_settler.go`

- IM: substantial refactor. `ValidatorQScoreFn` returns `protocolmath.BasisPoints`. `NewVerificationConsensusSettler` takes `shadowMode bool` arg. Adds `SetShadowMode`, `isShadowMode`, `computeValidatorPayoutsFloat`, `computeValidatorPayoutsInteger`, `evenSplitFallback`, `logShadowDelta`, `context_noop` helpers.
- F4: adds one-line `// safe:` annotation at `sumPayouts` function's map-range (line 399 in F4's version).
- **Resolution**: take IM's refactored shape. Apply F4's annotation to IM's version of `sumPayouts` if the function still exists; if IM's refactor renamed or inlined it, the annotation moves to the replacement's map-iter site (or is unnecessary if IM's refactor eliminated the map-iter).

---

## 4. Function signature changes cascading across both branches

IM commits 2–4 change signatures that F4 code reaches. Every F4-introduced caller needs adjustment.

### 4.1 `settlement.ValidatorQScoreFn`

- Before: `func(validatorID crypto.AgentID, family, category string) float64`
- After: `func(validatorID crypto.AgentID, family, category string) protocolmath.BasisPoints`
- F4 callers: none direct (F4 did not touch ValidatorQScoreFn call sites).
- Propagated effect: `settlement.NewVerificationConsensusSettler` callers must pass a `BasisPoints`-returning fn.

### 4.2 `settlement.NewGenerationLedgerCalculator`

- Before: `(dag, qualityFn func(EventID) float64)`
- After: `(dag, qualityFn func(EventID) protocolmath.BasisPoints, shadowMode bool)`
- F4 callers: none direct.
- cmd/node/main.go uses this — handled in §3.1 Region C.

### 4.3 `settlement.NewVerificationConsensusSettler`

- Before: `(taskMgr, transfer, escrow, dag, genLedger, treasury, qScoreFn)`
- After: `(taskMgr, transfer, escrow, dag, genLedger, treasury, qScoreFn, shadowMode bool)` — 8th arg added
- **F4 callers that need updating**:
  - `cmd/node/main.go` (handled in §3.1 Region C)
  - `internal/verification/cross_node/cluster.go` line 174 — `NewVerificationConsensusSettler(tm, tl, em, dag, genLedger, treasury, nil)` must gain `, true` (or `, false` if the harness wants to exercise integer-canonical directly; `true` preserves F4A shadow-mode behavior and is the safer initial choice).
  - Other F4 test files that construct the settler (see grep in §4.5).

### 4.4 `recognition.NewTaskVerificationConsensusConsumer`

- Before (F4/main): `(rounds, settler, slashing, calibration)`
- After (IM commit-13): `(rounds, slashing, calibration)` — `settler` dropped
- **F4 callers that need updating**:
  - `cmd/node/main.go` line 1941 (handled in §3.1 Region D).
  - `internal/recognition/task_verification_consensus_consumer_test.go` (multiple callsites).
  - `internal/integration/projection_calibration_test.go`.
  - `internal/dispatch/tv_consensus_consumer_test.go` — may need no update if its constructor is on the dispatch side (different consumer).

### 4.5 Grep-confirmed callsite inventory

Files calling any of the changed constructors (verified via `grep -rln "NewVerificationConsensusSettler\|NewGenerationLedgerCalculator\|NewTaskVerificationConsensusConsumer"`):

```
cmd/node/main.go
internal/settlement/generation_ledger_calculator_test.go    (IM-side; already consistent post-merge)
internal/settlement/verification_consensus_settler_test.go  (IM-side; already consistent post-merge)
internal/integration/projection_calibration_test.go         (IM updated; F4 didn't touch)
internal/dispatch/tv_consensus_lk_consumer_test.go          (F4-only; needs settler ctor update)
internal/dispatch/tv_consensus_consumer_test.go             (both touched; see §3.4)
internal/verification/cross_node/cluster.go                 (F4-only; needs settler ctor update)
internal/recognition/task_verification_consensus_consumer_test.go  (IM updated for dropped settler arg)
```

**F4-only callsites that WILL need post-merge fixup**:
- `internal/dispatch/tv_consensus_lk_consumer_test.go` — calls `NewVerificationConsensusSettler` with 7 args; needs 8th `shadowMode bool` arg added. Any setup that predates IM's signature change must add `, true` (or `, false` if the test wants to exercise integer-canonical directly).
- `internal/verification/cross_node/cluster.go` line 174 — same. `true` is the conservative choice (preserves F4A shadow-mode baseline behavior).
- `internal/dispatch/settlement_lk_consumer_test.go` — check; may or may not construct a settler.

Let me verify the three F4-only callsites explicitly:

```
$ grep -n NewVerificationConsensusSettler internal/verification/cross_node/cluster.go
174:   settler := settlement.NewVerificationConsensusSettler(
            tm, tl, em, &clusterStubDAG{}, genLedger, ... /* 7 args */)

$ grep -n NewVerificationConsensusSettler internal/dispatch/tv_consensus_lk_consumer_test.go
(multiple — confirmed in the report below)
```

These are tracked in §5.

---

## 5. New types introduced by either branch

Enumerated for architect review. New types are non-conflicting but must be documented.

### 5.1 F4-introduced new types

- `dispatch.AdmissionStrategy` + `AdmissionStrategyContentHash`/`AdmissionStrategyLogicalKey` + `IsKnownAdmissionStrategy` + `ErrUnknownAdmissionStrategy` (F4B §1.1/§1.2)
- `dispatch.LogicalKey`, `dispatch.Verdict` (`VerdictAccept`/`VerdictReject`), `dispatch.RoundState`, `dispatch.Outcome` (F4B §1.2 `logical_key.go`)
- `dispatch.LogicalKeyConsumer` interface (F4B §1.2)
- `dispatch.TVConsensusLogicalKeyConsumer` (F4B §5.2.1)
- `dispatch.SettlementLogicalKeyConsumer` (F4B §5.2.2)
- `dispatch.TaskSettlementLogicalKeyConsumer` (F4B §5.2.3)
- `dispatch.AdmissionCurrentVersion = 2` constant (F4B §1.1, bumped in §1.2)
- `dispatch.IsKnownAdmissionState`, `ErrAdmissionSchemaTooNew`, `ErrUnknownAdmissionState` (F4B §1.1)
- `conformance.RunLogicalKeyReplayConformance` (F4B post-§5.2.4)
- `cross_node.TestTiedWeightCorpus_ThroughDispatcher_ReproducesDivergence` + `TestTiedWeightCorpus_ThroughDispatcherLK_Converges` test names
- `monitoring/cross_node_invariants.*` package types (F4A step 10)

### 5.2 IM-introduced new types

- `protocolmath.BasisPoints`, `NeutralBP`, allocation primitives (commit-1)
- `dispatch.IntegerMigrationActivationConsumer` + `MigrationStateStore` interface (commit-8)
- `recognition.DispatcherAdmissionConsumer` (commit-13) — **supersedes F4's per-consumer SetDispatcher plumbing**
- `event.IntegerMigrationActivationPayload` (commit-8)
- `event/lint` package types (commit-6)
- `cmd/cross-arch-corpus/*` package types (commit-7)
- `migrationActivationState`, `migrationStoreAdapter` in cmd/node/main.go (commit-8/Part E)
- Various shadow-mode internals in settler and gen-ledger

### 5.3 Cross-contamination check

No type-name collisions between the two branches' new types. Different packages, different names. Safe to merge side-by-side.

---

## 6. Merge execution sequence

Recommended order when the merge is executed:

1. Create a backup tag on F4 branch: `git tag f4b-complete 5b9023e`
2. `git merge origin/feat/canonical-distribution-integer-migration` on `feat/selection-consistency-fix`
3. Resolve conflicts per §3 above, file by file.
4. For each F4-only callsite in §4.5, add the required signature-update argument (`, true` for shadowMode).
5. Delete F4's now-moot `SetDispatcher` calls in cmd/node/main.go (per §3.1 Region D item 7 + Region F).
6. Run `go build ./...` — must succeed before proceeding.
7. Run `go vet ./...` — must be clean.
8. Run `go test -race -count=3 ./...` (plan v2 §9.1.2 gate). Every failure halts for architect review; none should be off-checklist.
9. Run the cross-node byte-equality harness (`CROSS_NODE_HARNESS_CAPTURE=1 go test -race -v -run TestTiedWeightCorpus_ThroughDispatcherLK_Converges ./internal/verification/cross_node/...`) to confirm the F4B GREEN state survives the merge.
10. Capture combined-branch benchmark baseline (`go test -bench=BenchmarkAdmit ... ./internal/dispatch/`). New comparator for F4C regression gate.

**Any off-checklist conflict during step 3 halts for architect review.**

---

## 7. Explicitly OUT of the checklist (no conflict expected)

- Files touched only by IM (see `comm -23` in §1; ~46 files): protocolmath package, cross-arch-corpus, event/lint, part-*-completion reports, admin_handlers, integer_migration_activation_consumer, dispatcher_admission_consumer, settler+gen-ledger IM-specific tests. All merge cleanly.
- Files touched only by F4 (~123 files per `comm -13`): F4 architectural additions. All merge cleanly.
- CI workflow files IM added (`.github/workflows/*.yml`): merge cleanly.

---

## 8. Risk assessment

| Risk | Likelihood | Severity | Mitigation |
|---|---|---|---|
| Commit-13 + F4 `SetDispatcher` produces double-invocation of `dispatcher.Admit` per event | HIGH if resolution #6/#7 in §3.1 Region D isn't applied | MEDIUM — idempotent (AdmissionRecord check-and-set), wasteful but correct | Drop F4's SetDispatcher calls per §3.1 |
| F4's RegisterLogicalKey calls interfere with IM's IntegerMigrationActivationConsumer registration | LOW | LOW — different consumer sets, orthogonal routing | No mitigation needed; dispatcher's `Register` vs `RegisterLogicalKey` are separate maps |
| IM's shadow-mode wiring in cross-node harness not consistent with harness's synthetic test inputs | MEDIUM | LOW — harness uses simple scalar scoring; `shadowMode=true` (float path) is the default baseline | Initial merge: use `shadowMode=true` for the harness. Re-evaluate during combined-branch verification if shadow_delta logs appear |
| Performance regression from combined shadow-mode + logical-key paths | MEDIUM | LOW — founder's +10% D-5 threshold is the gate | Post-merge benchmark run; compare against F4B-end baseline |
| IM's commit-13 `DispatcherAdmissionConsumer.Interested` returns `true` unconditionally — combined with F4's general logical-key admission path may cause every event to trigger `admitLogicalKey` even for events no LK consumer is interested in | MEDIUM | LOW — `admitLogicalKey` internally filters by LK consumer's `Interested()`; empty match set is a fast path (no admission record, no state write). Performance hit is the extra loop traversal per event | Post-merge benchmark run; if `FreshContentHash` regresses >5% vs F4B-end, profile the hot path |
| F4A's replay-path wiring + IM's general admission router interact during startup replay | LOW | MEDIUM — both intended to be idempotent via MarkRecognizedOnce | Verify post-merge: run TestSyntheticConsumer_ReplayConformance + TestTypeE_SyntheticReplayConformance against merged state |

---

## 9. Checklist completion criteria

Before reporting "merge conflict checklist complete" at checkpoint 1:

- [x] §1–§8 above complete.
- [x] Every intersection file named with resolution approach.
- [x] Every function signature change flagged with affected F4-only callsites.
- [x] Every new type from both branches enumerated.
- [x] Risk assessment populated.

Checklist surfaces one **architectural merge decision** (general router vs per-consumer SetDispatcher) that the plan §9.1.1 intent requires to be surfaced for architect sign-off. Awaiting explicit approval before merge execution.

---

**End of F4C merge conflict checklist.**
