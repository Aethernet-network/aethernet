# Codebase quality audit — 2026-04-22

**Branch**: `feat/canonical-distribution-integer-migration` @ `48619f6`.
**Scope**: production codebase under `internal/`, `cmd/`, `pkg/`. Read-only.
**Method**: standard Unix tools + Go toolchain. No `gocyclo`/`deadcode`/`dupl`/`staticcheck`/`golangci-lint` installed; analyses substituted with manual or Python-aided proxies and noted explicitly.

---

## 0. Executive summary

This codebase is **substantively production-grade in its discipline and on a credible trajectory toward production-grade in its scope**. Test discipline is strong (1.03 test/prod LoC ratio overall, with 11 integration tests + a Type A/B/C/D conformance suite + cross-arch QEMU corpus). Architecture coherence is high: the design-principles document is treated as load-bearing and most code can be traced back to specific principles. Logging is uniformly structured. Concurrency primitives are used at appropriate scale and the locking discipline learned from past lessons (Go-mutex non-reentrancy, `taskMu` per-task serialization) is internalized.

The codebase is **not yet production-ready** — and the team knows it. The most recent workstream (Part F retry) discovered a class of consensus-event selection-race bugs that escaped F3-B's verification despite a 19-criterion success matrix. This audit confirms that the bug-class is real (5 HIGH-risk emission sites enumerated in the recent multi-emit audit) and finds two further structural concerns: an over-large `cmd/node/main.go` (3469 LoC, 1511-line `startStack`) that concentrates wiring complexity in one place, and a lightly-tested CLI surface (`cmd/aet` at 2% test/prod ratio).

This is a **prototype with production discipline**, not a "prototype with production pretensions." The discipline is real: test ratios match production codebases, design principles are cited and enforced, lessons get written down and re-read, audit documents accumulate. But scope-wise this is the second-half of a Bitcoin/geth-stage codebase, not the production-stage version. Continuing the current discipline gets it to production-grade for a credible mainnet-launch target. The one concrete risk is that the Part F retry's selection-race finding may have peers — bug classes the verification matrix isn't watching for — and the discipline of finding them depends on continuing to do exercises like Part F retry rather than deferring them under deadline pressure.

---

## 1. Scope and method

**Audited**: all Go code under `internal/`, `cmd/`, `pkg/`. Auxiliary `examples/`, `deploy/`, `configs/`, `Dockerfile*`, and other non-Go assets enumerated structurally but not deeply assessed. `vendor/` excluded entirely.

**Not audited**: dependencies (the codebase uses BadgerDB, ed25519, x/net/websocket, BLAKE3 — quality of these is taken as the upstream's). Frontend (`explorer/`) not assessed. Smart-contract / SDK Python code (`sdk/python/`) not assessed.

**Method**: file/line counts via `wc`, `find`, and inline Python; per-file branch density via Python regex; package import graph via `go list`; `go vet` for static analysis. Manual reading of critical packages (`internal/recognition`, `internal/dispatch`, `internal/settlement`, `internal/api`, `internal/taskverification`, `cmd/node/main.go`).

**Tooling limitations**: no `gocyclo` (cyclomatic complexity is approximated via per-function line count + per-file branch density); no `deadcode` (dead-code analysis is partial — exported symbols can't be ruled dead by reading alone); no `dupl` (duplication detection is by manual reading only). The audit notes where these tools would have given more confidence.

**Time**: ~75 minutes of automated probing + manual reading; ~45 minutes of writing.

---

## 2. Quantitative metrics

### 2.1 Aggregate LoC

| Category | Files | Lines |
|---|---:|---:|
| Production Go | 264 | 65,577 |
| Test Go (`*_test.go`) | 226 | 67,836 |
| Generated code | 0 | 0 |
| Testdata directories | 0 | n/a |
| **Test/prod ratio (overall)** | | **1.03** |

Healthy ratio. For comparison: Kubernetes ~1.2, CockroachDB ~1.1, Bitcoin Core ~0.8.

### 2.2 Top 20 packages by production LoC

| Package | prod LoC | files | test LoC | ratio | notes |
|---|---:|---:|---:|---:|---|
| `internal/network` | 5,552 | 17 | 5,998 | 1.08 | Fast Path 3-plane impl, well-decomposed across files |
| `internal/api` | 4,993 | 4 | 5,745 | 1.15 | **server.go alone is 4,703 LoC** — largest single file |
| `cmd/node` | 3,469 | 1 | 200 | **0.06** | main.go monolith; near-zero test coverage |
| `internal/validatorlifecycle` | 3,066 | 9 | 5,562 | 1.81 | Best-tested critical-path package |
| `internal/taskverification` | 2,414 | 14 | 1,999 | 0.83 | Round state + finalizer + slashing |
| `internal/recognition` | 2,354 | 18 | 3,664 | 1.56 | Commit bus + consumers; per-consumer modular |
| `internal/replay` | 1,994 | 12 | 3,920 | 1.97 | Test-heavy (good); also: missing pkg doc |
| `internal/settlement` | 1,760 | 7 | 1,524 | 0.87 | Float + integer paths under shadow gate |
| `internal/tasks` | 1,706 | 1 | 1,425 | 0.84 | TaskManager monolith file |
| `internal/validator` | 1,697 | 4 | 1,697 | 1.00 | |
| `internal/evidence` | 1,649 | 8 | 1,068 | 0.65 | |
| `internal/store` | 1,569 | 1 | 326 | **0.21** | BadgerDB wrapper, monolith file, low test coverage |
| `internal/autovalidator` | 1,563 | 2 | 1,759 | 1.13 | |
| `internal/ledger` | 1,465 | 5 | 1,484 | 1.01 | |
| `cmd/aet` | 1,463 | 6 | 34 | **0.02** | CLI; effectively untested |
| `internal/dispatch` | 1,403 | 11 | 2,160 | 1.54 | F3-B canonical event dispatcher |
| `internal/canary` | 1,215 | 7 | 1,333 | 1.10 | |
| `pkg/sdk` | 1,182 | 2 | 327 | **0.28** | External-facing SDK |
| `internal/projections/lint` | 1,157 | 5 | 1,285 | 1.11 | AST lint for projection registry |
| `internal/blobsync` | 1,150 | 11 | 1,165 | 1.01 | |

### 2.3 Under-tested packages (prod LoC > 100, test/prod ratio < 0.5)

`cmd/node` (0.06), `internal/store` (0.21), `cmd/aet` (0.02), `pkg/sdk` (0.28), `internal/harness` (0.27), `cmd/aet-loadtest` (0.00), `internal/marketplace` (0.00), `internal/config` (0.00), `internal/verification/families` (0.39), `cmd/aet-e2e` (0.00), `internal/cloudmap` (0.00), `internal/dispatch/conformance` (0.33 — but it's a test framework itself), `internal/metrics` (0.46), `cmd/marketplace` (0.00), `internal/fees` (0.49), `cmd/aet-harness` (0.00), `internal/jcs` (0.00), `examples/demo` (0.00), `internal/demo` (0.00).

The CLI binaries (`cmd/*`) being untested is structurally common — they're thin wrappers. The concerning ones are `cmd/node` (the main entry, 3469 LoC, only 200 LoC tests), `internal/store` (the BadgerDB persistence layer, 1569 LoC, 326 LoC tests), `internal/jcs` (canonical JSON serialization — the foundation of content-addressing — 0 tests), and `internal/marketplace` (956 LoC, 0 tests).

### 2.4 Cyclomatic complexity (proxied)

Tool unavailable. Substituted: function length distribution + per-file branch density.

**Function length distribution** (production code):

| Lines | Count |
|---|---:|
| 0–30 | 1,554 |
| 31–60 | 216 |
| 61–100 | 61 |
| 101–150 | 19 |
| 151–300 | 10 |
| **>300** | **4** |

Top of distribution dominated by 4 outliers; the 1,554-function 0–30-line bucket suggests the codebase generally favors short functions.

**Functions > 150 lines** (refactor candidates):

| LoC | Function | Reason |
|---|---|---|
| 1,511 | `cmd/node/main.go:1146 startStack` | Wiring monolith; 1500 lines of `stack.Foo = thing.New(...)`-style assembly. Probably acceptable as wiring (no logic, no branching), but it's the kind of "monolith-by-accumulation" that gets harder to refactor over time. |
| 698 | `internal/harness/corpus.go:12 DefaultCorpus` | Test fixture data table. Acceptable. |
| 322 | `internal/network/node.go:1280 handleMessage` | Real complexity. Refactor candidate. |
| 318 | `cmd/node/main.go:804 buildStack` | Wiring + persistence-recovery. Splittable. |
| 275 | `internal/canary/corpus.go:15 DefaultCanaryCorpus` | Fixture data. Acceptable. |
| 262 | `cmd/aet-e2e/main.go:239 main` | E2E test driver. Acceptable. |
| 213 | `internal/api/server.go:2330 handleRegisterAgent` | Real complexity. Refactor candidate. |
| 191 | `cmd/node/main.go:2773 cmdStart` | Startup orchestration. Splittable. |
| 178, 171 | loadtest helpers | Test code, acceptable. |
| 161 | `internal/api/server.go:902 ServeHTTP` | The auth-routing middleware. Real complexity; refactor candidate. |
| 160 | `internal/trajectory/service.go:141 EmitCommit` | Real complexity. |
| 158 | `internal/network/node.go:385 Connect` | Real complexity. |

The two clearly-large refactor candidates are `network/node.go:handleMessage` (322 LoC) and `api/server.go:handleRegisterAgent` (213 LoC). Both are core protocol paths; their length suggests they're handling multiple concerns each.

**Branch density** (per-function average of `if`/`for`/`switch`/`case` per function, per file): only 4 production files have >8 branches per function on average:

- `internal/dispatch/conformance/type_a.go` — single function, conformance template. Justified.
- `internal/verification/consensus_check.go` — single 19-branch function. Worth a read.
- `internal/auth/txverify.go` — single 18-branch function. Auth verification has many cases. Read recommended.
- `internal/canary/evaluator.go` — 13 branches/function. Canary scoring logic.

### 2.5 Package count and import depth

71 packages with Go code. No circular imports (verified by `go vet ./...` running clean modulo 4 pre-existing `atomic.Int64` test-only warnings).

**Hub packages** (most imported-by counts, inferred from `go list`):

- `internal/event` (event types and canonical serialization) — imported by ~25 packages
- `internal/crypto` (key/sig primitives) — imported by ~20 packages
- `internal/ledger` (transfer + generation ledgers) — imported by ~15 packages
- `internal/dag` — imported by ~12 packages

These are appropriate hubs. No "god-package" pattern (a single package other code can't avoid importing).

### 2.6 Dead code

Tool unavailable. Manual reading found a few candidates:

- `internal/recognition/types.go:35 SourceReplay` — defined but **never referenced in production**. Documented in the Part F retry's characterization document as part of an architectural gap.
- `internal/eventbus/bus.go:12` — comment-only `fmt.Printf` example, not real dead code.
- `internal/demo/`, `examples/demo/` — example/scaffold code, presumed intentional.

A real `deadcode` run would surface more. Confidence: low.

### 2.7 Code duplication

Tool unavailable. Manual scanning of similar files (multiple `*_consumer.go` in `recognition`, multiple `family_*.go` in `verification`) shows clean structural symmetry rather than copy-paste — each consumer/family is its own type with shared interface, not duplicated logic. This is the right pattern.

A real `dupl` run would identify low-level similar blocks. Confidence: medium.

### 2.8 TODO/FIXME/HACK/XXX/BUG

| Marker | Count |
|---|---:|
| `TODO` | 18 |
| `FIXME` | 0 |
| `HACK` | 0 |
| `XXX` | 0 |
| `BUG` | 0 |

The 18 TODOs are mostly real ("TODO: verify HolderHint signatures before mainnet" at `blobsync/holder_cache.go:48`; "TODO prompt 08: replace neutral qualityFn with real Quality Score lookup" at `settlement/generation_ledger_calculator.go:87`). They're enumerated, scoped, and reference future workstreams. None are stale.

The absence of FIXME/HACK/XXX is striking — either the team writes clean code or doesn't admit ugliness in comments. The function-length distribution (98% of functions ≤ 60 lines) suggests the former.

### 2.9 Interface count per package

| Package | Interfaces |
|---|---:|
| `internal/recognition` | 13 (justified — consumer pattern hub) |
| `internal/replay` | 11 (justified — coordinator + replayer + processor abstractions) |
| `internal/api` | 10 (justified — adapters for taskMgr, escrow, lifecycle, etc.) |
| `internal/autovalidator` | 6 |
| `internal/dispatch` | 5 |

No interface abuse. The two double-digit packages have justifying patterns.

### 2.10 Other smell metrics

- `panic()` in production: **9** instances. All are defensive (overflow check in `protocolmath/muldiv.go:24`, `MustRegister` constructor failures in `projections/registry.go:34,82`, nil-counter fail-fast in `epoch/consumer.go:24`). None look like legitimate runtime panics.
- `fmt.Println`/`fmt.Printf`/`log.Print*` in production: **2** instances. One is a docstring example (`eventbus/bus.go:12`); the other is `roundprogress/emitter.go:96` — a real production-side `fmt.Printf` that ships log spam ("rate limited: validator ..., round ..., family all"). This was visibly present in Phase C-sanity logs as the noisy line. Should be `slog.Warn`. Minor.
- `time.Sleep` in production (smell): **8** instances. 2 in production-path code (`tasks/tasks.go:1290` — likely backoff; `network/discovery.go:124` — jitter). Acceptable. The other 6 are in CLI tools (`cmd/aet-loadtest`, `cmd/aet-e2e`) — also acceptable.
- `context.Background()` inside production functions: **46** instances. About 30 are inside `slog.Log(...)` calls where context cancellation isn't relevant (slog uses context for attribute extraction, which is a no-op here). The remaining ~16 are mostly inside `taskverification/badger_store.go` (5×) and `taskverification/calibration.go`/`deadline_checker.go` — these are real context-drops worth review.
- Ignored errors (`_ = `): **148** instances. Heaviest in `cmd/node/main.go` (33), `cmd/aet/main.go` (16), `internal/api/server.go` (8), `internal/autovalidator/auto.go` (7). Most cmd-level `_ = ` are explicit acknowledgment that a startup-flag-parse or final-status-write is best-effort. The autovalidator and api ones deserve individual review.
- Package-level mutable state: ~20 instances, mostly enum-name maps and validation tables (acceptable). One concerning: `internal/ledger/transfer.go:49 var bucketCounter atomic.Uint64` — package-level atomic counter for bucket-naming. Probably fine but worth confirming it isn't a hidden source of cross-node non-determinism.
- `context.TODO()`: **0** in production. Good.
- `slog` adoption: **76** files use `slog`; 0 use stdlib `log`. Uniform.

---

## 3. Architecture coherence

`docs/design-principles.md` is 174 lines, 15 numbered principles. It's cited by file path from `CLAUDE.md`'s "required reading" list. `docs/architecture.md` (174 lines) is the system overview.

Per-principle assessment (PASS / PARTIAL / FAIL / UNKNOWN):

| # | Principle | Status | Evidence |
|---|---|---|---|
| 1 | The thesis is load-bearing | PASS | Compound verification is in the critical path; every settlement flows through verification + voting + consensus + settlement. No "skip verification for speed" pathways found. |
| 2 | Machine speed is the standard | PASS | No human-tolerance timeouts in the hot path. Voting rounds finalize on supermajority detection (immediate). Consensus expiry is 30s — a backstop, not a wait. |
| 3 | Validators communicate state, not just verdicts | PARTIAL | `internal/roundprogress/` exists and is wired (see `cmd/node/main.go:2055-ish`). Progress emitter at `roundprogress/emitter.go` produces structured updates. But the corresponding consumer-side adaptation (does the protocol actually wait adaptively based on progress?) is partial — the `local apply warning: rate limited` log noise observed in Phase C-sanity is the round-progress emitter rate-limited; whether that progress is read on the receive side and used adaptively is unclear from this audit. Worth a follow-up. |
| 4 | Compound verification requires structural independence on every axis | PARTIAL | Multi-voter (`internal/autovalidator/multi_voter.go`) runs N analyzer families per submission with per-family vote-weight tracking. Family diversity is enforced (`taskverification/round.go DistinctPassFamilies`). However, the live Phase C-sanity showed the protocol declaring `agreeing_validators=4` while different nodes finalized different verdicts — independence on the family-axis is computed; independence on the *finalization-event-selection-axis* is not. |
| 5 | The protocol is the source of truth | PARTIAL | F3-B's commit-9/10/11 enforced the dispatcher as the single admission point for canonical events. **But** the recently-discovered selection race violates this principle: per-node ledger state diverges from the canonical DAG when multiple consensus events for the same key exist. The principle is correctly stated; the implementation has a known gap. |
| 6 | Generalize the primitive, not the fix | PARTIAL | Multiple successful examples: dispatcher primitive (Part C of F3-B), `protocolmath` allocator (Part A of integer migration), Part E.1's general admission router. **But** the multi-emit bug audit (`docs/plans/implementation/multi-emit-bug-class-audit.md`) just discovered that F3-B's per-task mutex was scoped to TVConsensus admission and didn't generalize to the broader bug class. The team correctly applied the principle in re-design (Part E.1 was a general router rather than a per-event-type adapter); the principle was violated in the original F3-B scope. |
| 7 | Reuse mechanism, separate concern | PASS | Recognition bus is reused across consumers, each with its own subscription channel. localpub publisher is a single mechanism with multiple consumers. No "extend channel X to also do Y" patterns found. |
| 8 | No human-in-the-loop in any protocol path | PASS | No admin-approve gates in the hot path. The Part F admin endpoint (`POST /v1/admin/integer-migration/activate`) is operator-driven but not in any consensus path; it's a one-shot operator action. |
| 9 | Persist before publish | PASS | Examples cited: `taskverification/deadline_checker.go:408` "Persist BEFORE publish (ordering invariant)" comment; `dispatch/integer_migration_activation_consumer.go:111-113` persists activation state before flipping flags. |
| 10 | Content addressing is the integrity model | PASS | DAG events are BLAKE3-content-addressed. Dispatcher admission key is content-hash (`internal/dispatch/dispatcher.go:100`). Blobs are content-addressed. JCS canonicalization (`internal/jcs/`) is the deterministic-serialization base. **Weak spot**: `internal/jcs/` has 0 tests (130 LoC). The integrity model's foundation is untested. |
| 11 | Integer canonical state, no exceptions | PASS for new code, IN-PROGRESS for legacy | `internal/protocolmath/` exists. Part C added an AST lint enforcing float-freedom in canonical event payloads. Settlement still has 13 float references in its file but they're in the legacy float path (`computeValidatorPayoutsFloat`) under the shadow-gate; they're scheduled for removal in a future workstream. |
| 12 | Beauty is a correctness signal | PASS | Function length distribution is excellent (98% ≤ 60 lines). Naming is clear. Comments explain why, not what. One ugly section: `cmd/node/main.go:1146 startStack` — 1511-line wiring function; not "ugly" exactly but not "beautiful." |
| 13 | Tests are necessary, live testnet is sufficient | PASS | F3-B's 19-criterion testnet verification demonstrates the discipline. Part F retry surfaced a real bug only catchable on live infrastructure (not unit-tested), per the principle. The discipline is being practiced. |
| 14 | The standard is permanent | PASS | Branch hasn't been merged despite the integer-migration code being correct in itself, because the protocol-layer issue surfaces and the standard says "verified working on the live 5-node testnet across all 5 nodes" — current status is "verified working on 4 of 5 nodes," so the standard rejects the merge. Discipline observed. |
| 15 | Observable evidence beats self-reported claims | UNKNOWN | The principle exists; whether it's structurally enforced (i.e., do consumer Apply paths verify state via observable mechanisms rather than trusting payload self-reports) requires deeper inspection than this audit performed. The blobsync subsystem is the most relevant; observable progress is partly implemented per Principle 3's PARTIAL. |

**Documentation vs. implementation drift**: minimal. The principles are short and operationally testable. The places where implementation lags principles are explicitly flagged in the codebase (TODOs reference future workstreams) or in `docs/lessons.md` (which captures the "why" of past corrections).

**Module boundaries**: clean. The package graph respects the `internal/` boundary uniformly (no `internal/` packages imported from outside the module). No god-packages. Test files generally co-locate with the package under test (`_test` files in same dir). Some integration tests in `internal/integration/` correctly cross package boundaries.

---

## 4. Per-package quality assessment

### 4.1 `internal/network` (5,552 LoC, 17 files, A−)

Fast Path 3-plane implementation: causality announce → body fetch → repair fallback. Decomposed across 17 files with clear separation: `node.go` (lifecycle), `peer.go`, `materialize.go`, `repair.go`, `ingest.go`, `discovery.go`, `mesh.go`, `relay.go`, `completion.go`, `tracking.go`, `validation.go`, `protocol.go`, `compat.go`, `legacy.go`, `backpressure.go`, `scoring.go`, `checkpoint.go`. Test ratio 1.08, healthy. **Concerns**: `node.go:handleMessage` is 322 lines — the central message-routing function; is a refactor candidate (split per-message-type handlers). Also `node.go:Connect` at 158 lines. These aren't terrible but they violate Principle 12 (beauty). Not protocol-correctness concerns. **Grade: A−**.

### 4.2 `internal/api` (4,993 LoC, 4 files, B−)

The API server is a 4,703-LoC monolith file (`server.go`) with 126 functions, 68 of which are HTTP handlers. The other 3 files (`admin_handlers.go` 83 LoC, `trajectory_handler.go` 131 LoC, `websocket.go` 76 LoC) are recent additions. **Test ratio 1.15** (5,745 LoC tests), reasonable. **Concerns**: file size. 4,703 lines in one file is a structural smell — not because the code is bad but because it concentrates all HTTP concerns into one place where mistakes have a wider blast radius. The 213-line `handleRegisterAgent` and 161-line `ServeHTTP` are real complexity. Refactor candidate: split per-resource handler files (`task_handlers.go`, `agent_handlers.go`, `transfer_handlers.go`, etc.) — the pattern is already started by `admin_handlers.go`. **Concrete weakness**: 8 instances of `_ = ` ignored errors in this file are worth individual review. **Grade: B−** — works, well-tested, but file structure invites future bugs.

### 4.3 `cmd/node` (3,469 LoC, 1 file, C+)

Single `main.go`. The `startStack` function (line 1146) is **1,511 lines** of wiring: every sub-system gets constructed, every consumer gets registered, every adapter gets wired. Test coverage is **200 LoC of tests for 3,469 LoC of code (0.06 ratio)** — effectively untested. **Concerns**: this file is where the dispatcher gets wired, where the recognition fabric gets the SetOnCommit hook attached, where startup ordering happens. Bugs here are systemic. Recent example: the SetOnCommit-runs-after-LoadFromStore architectural gap (Part F retry Path B finding) lives in this file's startup ordering. **The biggest single quality concern in the codebase.** Not because the code is bad — most of it is repetitive wiring — but because it concentrates startup risk in one untested 3,469-LoC file. **Grade: C+** — works, but is the single most fragile place in the codebase.

### 4.4 `internal/validatorlifecycle` (3,066 LoC, 9 files, A)

Validator-set state machine: `committee.go`, `eligibility.go`, `events.go`, `genesis.go`, `manifest.go`, `reducer.go`, `snapshot.go`, `startup.go`, `types.go`. Clear file responsibilities, clean state machine in `types.go:127 var validTransitions = map[SeatStatus]map[SeatStatus]bool{...}`. **Test ratio 1.81** — best-tested critical-path package. State transitions are explicit and table-driven. **Grade: A**.

### 4.5 `internal/taskverification` (2,414 LoC, 14 files, B+)

Round state machine + finalizer + slashing + reputation + calibration. 14 files for 2,414 LoC = small files (~170 LoC average). Test ratio 0.83. **Concerns**: 5 instances of `context.Background()` inside `badger_store.go` look like context-drops worth review (the persistence layer should propagate context for cancellation). `deadline_checker.go:399 applyFinalization` is the function that triggers the multi-emit selection race characterized in the recent bug-class audit — not a quality issue with this file's code, but the design embeds an architectural gap. **Grade: B+**.

### 4.6 `internal/recognition` (2,354 LoC, 18 files, A−)

Causal Commit Bus with per-consumer registration. 18 files = excellent decomposition (one consumer per file). Test ratio 1.56. The Part E.1 admission router lives here. **Concerns**: 13 interfaces defined here is the highest in the codebase, but justified by the consumer-pattern hub role. Has the SourceReplay/LoadFromStore architectural gap (`types.go:35 SourceReplay` defined but unused; described in detail in the Part F retry's characterization document). **Grade: A−** — minor architectural gap noted, otherwise textbook.

### 4.7 `internal/dispatch` (1,403 LoC, 11 files, A)

CanonicalEventDispatcher (F3-B Part C). Per-(event, consumer) admission state machine. 11 files including conformance suite, deferral logic, the dispatcher itself, and consumers. Test ratio 1.54. The Type A/B/C/D conformance test suite (`conformance/`) is the most mature test infrastructure in the codebase — every dispatcher consumer must pass it in CI. **Concerns**: F3-B's per-task mutex in `tv_consensus_consumer.go` was the under-generalized fix that left the multi-emit bug class open. Not a quality problem with the dispatcher code; a scope problem with the F3-B plan. **Grade: A** for code quality, separate from the F3-B-scope critique.

### 4.8 `internal/settlement` (1,760 LoC, 7 files, B+)

Two-path settlement: legacy float + integer-canonical (Part B of integer migration). Shadow-gate wrapper compares both. 7 files. Test ratio 0.87. Code quality is high — the shadow-gate pattern is clean, the integer math is correct (Part A's protocolmath unit-tested, cross-arch-verified by Part D's QEMU corpus). **Concerns**: 13 float references in `verification_consensus_settler.go` are all in the legacy path and documented as such. The `computeValidatorPayoutsFloat` function is verbatim-preserved per the migration plan. Will be removed in a future workstream. **Grade: B+** — the float path is a known liability, scheduled for removal.

### 4.9 `internal/store` (1,569 LoC, 1 file, C)

BadgerDB wrapper. 1,569 LoC in a single file (`store.go`) with **326 LoC of tests (0.21 ratio)**. **Concerns**: this is the persistence foundation for every consumer. Low test coverage on a 1,569-LoC critical-path file is the second-biggest test-discipline gap in the codebase (after `cmd/node`). Should be split (e.g., `store.go` for the wrapper, `meta.go` for `PutMeta/GetMeta`, `admission.go` for AdmissionStore impl, `events.go` for event persistence). **Grade: C** — works in practice but inadequately tested for what it does.

### 4.10 `internal/tasks` (1,706 LoC, 1 file, B)

TaskManager. 1,706 LoC in a single file (`tasks.go`) with 1,425 LoC of tests (0.84 ratio). The "TaskManager methods cannot be called while holding m.mu" lesson lives here (per `docs/lessons.md`). **Concerns**: monolith file but well-tested. The `tasks.go:1290 time.Sleep(500*time.Millisecond)` is a smell worth investigating. **Grade: B**.

### 4.11 `cmd/aet` (1,463 LoC, 6 files, D+)

The `aet` CLI tool. 6 files including `main.go` (dispatcher), `wallet.go` (key management), `client.go` (HTTP client), `signing.go` (request signing), `format.go` (output), `admin.go` (Part F admin command). **Test ratio 0.02** (34 LoC tests for 1,463 LoC of CLI). The CLI is the operator's primary interface for the protocol. Its near-zero test coverage means every operator action is implicitly trusted to work. **Grade: D+** — functionally adequate, structurally untested. CLI tests are inherently awkward (subprocess invocation), but 2% is below the bar.

### 4.12 `pkg/sdk` (1,182 LoC, 2 files, C)

External-facing SDK (presumably Python users wrap this; or Go callers). 2 files for 1,182 LoC. Test ratio 0.28. **Concerns**: this is the *external* API. External APIs should have *higher* test coverage than internal packages, not lower, because external users will hit edge cases internal callers don't. **Grade: C** — works, under-tested for its role.

### 4.13–4.20 (briefer assessments)

- `internal/replay` (1,994 LoC, 12 files, **A−**): test ratio 1.97 — most-tested major package. Replay coordinator + processor + outcome-handling. Missing pkg doc (only package without one). Otherwise excellent.
- `internal/validator` (1,697 LoC, 4 files, **A−**): test ratio 1.00. Validator-set logic, well-organized.
- `internal/evidence` (1,649 LoC, 8 files, **B+**): test ratio 0.65 — the analyzer-family hub. `content_quality.go` has 32 float references (legitimate — analyzer scoring is float-internal, integer-output at the canonical boundary).
- `internal/autovalidator` (1,563 LoC, 2 files, **B**): `auto.go` 1322 LoC monolith + `multi_voter.go` 241 LoC. Test ratio 1.13. The 7 ignored errors here deserve review.
- `internal/ledger` (1,465 LoC, 5 files, **A−**): test ratio 1.01. Transfer + generation ledger. The package-level `bucketCounter atomic.Uint64` worth confirming non-determinism-free.
- `internal/canary` (1,215 LoC, 7 files, **A−**): test ratio 1.10. Canary-corpus + evaluator. Well-decomposed.
- `internal/blobsync` (1,150 LoC, 11 files, **B+**): test ratio 1.01. 11 files for 1,150 LoC = excellent decomposition. The `holder_cache.go:48 TODO: verify HolderHint signatures before mainnet` is a real pre-mainnet item.
- `internal/jcs` (130 LoC, 1 file, **D**): canonical JSON serialization. **0 tests for the foundation of content-addressing**. Per Principle 10, this code's correctness is what makes content-addressing work; per Principle 13, "tests are necessary." This package has neither testnet exercise (it's used everywhere by transitive callers) nor unit tests. Highest-leverage place to add tests.

---

## 5. Cross-cutting concerns

**Error handling**: error-wrapping (`fmt.Errorf("foo: %w", err)`) is uniform. 148 ignored errors (`_ = f()`) is high in absolute number but mostly clustered in `cmd/node/main.go` (33) and `cmd/aet/main.go` (16) where they're explicit best-effort markers in startup paths. The 8 in `api/server.go` and 7 in `autovalidator/auto.go` deserve case-by-case review — these are protocol-layer paths where silent error-swallow is more concerning. No `panic-on-error` patterns found.

**Logging**: uniform `slog`. Zero `stdlib log` imports. Log-level discipline appears good — `Info` for happy-path lifecycle events, `Warn` for retryable failures, `Error` for bugs. Two minor exceptions: `roundprogress/emitter.go:96` uses `fmt.Printf` instead of `slog.Warn` (this is the noisy log line visible on testnet) and the rate-limited warnings in roundprogress would benefit from rate-limited log emission as well.

**Concurrency**: 77 `sync.Mutex/RWMutex` references, 6 `sync.WaitGroup`, 11 `sync/atomic`, 1 `sync.Map`, 26 `go func` goroutine spawns, 78 channel declarations. Reasonable mix. The `taskMu` per-task mutex pattern in `dispatch/tv_consensus_consumer.go` is internalized (a lesson learned). The 8 production `time.Sleep` calls are the most concerning concurrency smell — only 2 are in non-CLI production code (`tasks.go:1290`, `network/discovery.go:124`); both are likely backoff/jitter and acceptable. No obvious deadlock patterns visible.

**Context propagation**: 46 `context.Background()` inside production functions. ~30 are inside `slog.Log(...)` calls (no-op). The remaining ~16 in `taskverification/badger_store.go` (5×) and related stores look like real context-drops in the persistence layer — the BadgerDB caller can't cancel storage operations. Worth a follow-up audit specifically for the persistence layer.

**Resource cleanup**: hard to assess without running. `sync.WaitGroup`-protected goroutine pools in the recognition bus look correct (`bus.go:97-112 Start/Stop`). Channel-based shutdowns are present but require deeper review than this audit gave.

**Magic numbers**: scattered. `tasks.go:1290 time.Sleep(500*time.Millisecond)` — magic. `network/discovery.go:124 time.Sleep(jitter)` — `jitter` is named, good. `internal/genesis/` constants are named — total supply, faucet allocation, etc. The 30-second consensus expiry from `CLAUDE.md` is referenced in code; couldn't find it as a single named constant though — could be an `OCSConfig.RoundTimeoutSec` or similar.

**Global state**: ~20 package-level vars, mostly read-only enum-name maps and validation tables (acceptable). Two concerning candidates: `internal/ledger/transfer.go:49 var bucketCounter atomic.Uint64` (atomic counter at package level — verify non-determinism-free) and `internal/blobsync/policy.go:18` two policy var blocks (configuration; acceptable).

**Panic usage**: 9 instances. All are defensive (overflow check, MustRegister failure, nil-counter fail-fast in constructor). None are runtime-error panics. Defensive panics in constructors that happen at startup are acceptable per Go idiom.

---

## 6. Known bug classes and their containment

### 6.1 Selection race in canonical-event multi-emit (open, characterized)

- **Where discovered**: Part F retry Phase C-sanity, 2026-04-22.
- **File:line of the bug source**: per the multi-emit audit, 5 HIGH-risk emission sites: `internal/autovalidator/auto.go:981` (TaskSettlement), `internal/taskverification/deadline_checker.go:414` (TaskVerificationConsensus deadline path), `internal/recognition/task_verification_vote_consumer.go:210` (TaskVerificationConsensus vote path), `cmd/node/main.go:1770` (Settlement), and the SettlementConsumer at `internal/recognition/settlement_consumer.go`.
- **Status**: open. Characterized in `docs/plans/implementation/selection-race-characterization.md`. Audited in `docs/plans/implementation/multi-emit-bug-class-audit.md`. Frozen-state evidence in `docs/historical/part-f-retry-evidence/`.
- **Structural invariants preventing recurrence**: NONE currently. F3-B's per-task mutex prevents local-double-apply but does not prevent cross-node selection divergence.
- **Confidence that similar bugs don't exist elsewhere**: medium. The audit enumerated 21 emission sites; the 5 HIGH-risk ones share the structural shape. Other event types (Registration, Generation, Trajectory) don't share the shape (single-emitter). But the `verification_settler` calling pattern was not audited for its recovery-from-DAG-replay behavior — the SourceReplay/LoadFromStore gap could surface other bug classes.

### 6.2 Cross-node ledger divergence — pre-existing 50 AET stake-state divergence (open)

- **Where discovered**: Part F retry Phase B-verify, 2026-04-22.
- **Hypothesis**: same selection-race mechanism applied to the Settlement event-emit path (`cmd/node/main.go:1717-1792`). Not yet directly traced to a specific stake event with the same rigor as the TVConsensus instance.
- **Status**: open. Characterized in §6 of the selection-race document.

### 6.3 Recognition bus does not replay historical commits to newly-registered consumers (open)

- **Where discovered**: Part F retry Path B investigation, 2026-04-22.
- **File:line**: `cmd/node/main.go:2089-2091` (SetOnCommit attached after LoadFromStore in `cmd/node/main.go:buildStack`); `internal/dag/dag.go:392-395` (addFromStore fires onCommit hook **if hook is set**, but it isn't set yet during LoadFromStore); `internal/recognition/types.go:35` (SourceReplay constant defined but never used).
- **Status**: open, architectural finding. Not in any current workstream's scope.
- **Effect**: a bus consumer added in a newer binary version cannot observe commits that happened under the older binary. Manifested in Part F retry Phase B-verify where the historical activation event stayed inert on the new E.1 binary.

### 6.4 F3-B multi-validator-emit-causes-cluster-divergence (closed-but-known-incomplete)

The original F3-B framing scoped "double-settlement on producer node" — same event delivered twice. F3-B's commit-9/10/11 closed that. The audit now identifies F3-B did not address "different events for the same logical key from different validators." This is the parent of #6.1.

### 6.5 SettlementConsumer selection logic (open, unverified)

Suspected analogue of #6.1 for `EventTypeSettlement` events (Transfer/Generation/etc settlement). Not yet directly traced. Strong hypothesis for the 50-AET stake divergence.

### 6.6 Are there other bug classes not yet found?

This audit suggests:

- **Non-determinism beyond float and selection-race**: 508 `range over map` calls in production. Most are likely safe (iteration over a configured set in O(1) lookup style). But `range over map` is the canonical Go non-determinism source. If any consumer's state mutation order matters and depends on map iteration order, that's a latent bug. Not flagged by current invariants.
- **Unchecked assumptions**: the 148 ignored errors include several in protocol-path code. Worth a manual review specifically for "this `_ =` is hiding a real failure."
- **Version handling**: protocol version checks look present (`SchemaVersion` fields in payloads, `PrerequisiteSchemaVersion` per consumer per F3-B). Whether they're enforced uniformly across all event types is unclear without deeper inspection.
- **Replay safety**: 67 files mention idempotency; 56 mention replay. F3-B's load-before-listener invariant is documented and the dispatcher's recovery probe machinery is mature. **But** the SourceReplay/LoadFromStore gap (#6.3) is a concrete replay-safety violation. Other replay paths may have similar gaps.

---

## 7. Test discipline

**Test types present**: unit (everywhere), integration (`internal/integration/` with 11 files, 3,837 LoC), conformance (`internal/dispatch/conformance/` with Type A/B/C/D templates), cross-architecture (`cmd/cross-arch-corpus` from Part D), live-infrastructure (Part F first-attempt and retry, frozen as `docs/historical/part-f-retry-evidence/`). Property-based tests: not found. Fuzz: not found. Benchmarks: not surveyed but `go test -bench` would surface any `Benchmark*` functions.

**Invariant testing**: present at the unit level (e.g., `internal/protocolmath/invariants_test.go` tests permutation-invariance, conservation, ceiling clamp). Integration tests check projection-registry invariants. **Missing**: cross-node consensus-uniformity invariant. The selection-race bug demonstrates the gap — there was no test that asserted "given the same DAG of canonical events, all 5 nodes produce byte-equal projected ledger state." This is the verification gap that allowed the bug to ship through F3-B's 19-criterion success matrix.

**Cross-node testing**: integration tests in `internal/integration/two_node_test.go` exist but only test 2 nodes. The conformance suite exercises individual consumer behavior under simulated multi-delivery, not cross-node-uniform-selection. Live-testnet verification (5 nodes) is the only place cross-node behavior is tested, and the verification matrix didn't include "same-task per-node ledger byte-equality" as a check.

**Failure-mode coverage**: dispatcher's conformance suite includes crash recovery, concurrent same-event delivery. Recognition bus tests include backpressure, deferred activation. Network tests include legacy-fallback. Coverage is good for the cases that have been imagined; the gap is in cases that haven't (the selection race wasn't imagined when F3-B's tests were written).

**Test flakiness**: `scripts/deploy-testnet.sh:21` enumerates known flakes: `TestAutoValidator_FeeOnTaskSettlement` and `TestNextCanary_CopiesExpectedEvidence`. Plus `internal/network` and `internal/canary` are observed flaky under `-race -count=3` (per Part B's completion report). Real but bounded.

**Mock discipline**: the dispatcher's conformance suite uses synthetic consumers (not mocks of real consumers) — correct pattern. Recognition bus tests use `testConsumer` and `testReadModel` minimal implementations — also correct. The integration tests cross real package boundaries with real implementations — correct. No mock-the-internal-collaborator antipatterns observed.

**Test organization**: tests are co-located with code. Test names are descriptive (`TestAdmissionState_FirstDeliveryCreatesRecord`, `TestComputeValidatorPayouts_ShadowMode_ReturnsFloat`). Generally well-structured.

**The verification gap that allowed the selection race**: the test that would have caught it — "for every settled task in a verification corpus, query every node's ledger for the per-recipient delta and assert byte-equality across nodes" — is straightforward to write. F3-B's 19 criteria included settlement byte-identity *for the corpus they ran* (`TestNetCriteria.criterion-7: 10 reject-path tasks × 5 nodes byte-identical`). What was missing was the *test invariant* generalized — "for ANY corpus ran on the testnet, this property must hold," not just "for this specific corpus." A continuous monitoring check would surface drift; a randomized-corpus test would surface the multi-emit case in CI.

---

## 8. Operational readiness

**Configuration**: externalized via `internal/config/config.go` (815 LoC, 0 tests). All major tuning exposed (rate limits, queue depths, timeouts, faucet amounts). Hard-coded values are mostly genesis allocations (constant by design) and the 30-second consensus expiry (could be a config but reasonably static).

**Observability**: `internal/metrics/` (257 LoC, 119 LoC tests, ratio 0.46). Prometheus-style metrics with Aethernet-specific gauges. Metrics endpoint at `/metrics` on every node. Logging is structured (slog) and includes event IDs, task IDs, validator IDs in every relevant context. Diagnosability is good.

**Graceful degradation**: not deeply audited but the patterns observed: `recognition.Bus.Emit` returns `ErrQueueFull` on backpressure (caller must not block); dispatcher's `RecoveryProbe` is replay-safe per C-14; `dag.Add` failures are recoverable via sync. Network partitions are handled by Fast Path's three-plane design (causality/body/repair). What happens on BadgerDB corruption: per `dispatch/anchor.go:9-12`, `ErrCorruptedAdmissionState` exists but the recovery path requires manual intervention. Whether automatic re-bootstrap-from-genesis is supported: not assessed.

**Upgrade path**: rolling-upgrade not formally supported. The Part F retry deploy was sequential per-node SSH-based redeploys with `--force-new-deployment` semantics. CLAUDE.md notes the deploy-script drift (ECS-targeted script doesn't match EC2-direct deployment). No documented "rolling upgrade protocol" for cluster-wide binary changes that change consensus-affecting behavior. Practical issue: the integer-migration cutover required a designed coordination mechanism (the activation event), but routine binary upgrades that *don't* change consensus behavior have no documented protocol.

**Monitoring invariants**: the most important runtime invariant — cross-node ledger byte-equality — is not continuously checked. There's no metric "cross-node-ledger-divergence" or alert "validator-stake-balance-differs-from-peers." This is the gap that allowed Divergence A (the pre-existing 50 AET) to accumulate silently.

**Incident tooling**: `aet status`, `aet agents`, `aet task list/status` provide read-only diagnostics. `aet admin activate-integer-migration` is an example of an admin endpoint (gated behind `--enable-admin-api`). No rich incident runbook. Per Part F retry, most diagnosis happened via `ssh ... docker logs aethernet 2>&1 | grep ...` — adequate for a 5-node testnet, inadequate for a 50- or 500-node mainnet.

**Documentation for operators**: `CLAUDE.md` itself is the operator-facing runbook (atypical but functional given the multi-AI-agent workflow). `scripts/check-testnet.sh`, `scripts/deploy-testnet.sh` are the deploy/check tools. No standalone `docs/operations/` or runbook directory.

Most of these gaps are appropriate for the current workstream stage (pre-mainnet, single-developer-plus-AI-agents, 5-node private testnet). They become blockers at "trillions of transactions" scale.

---

## 9. Honest assessment

**Is this codebase on a trajectory to production-grade?** Yes. The discipline is there. The standard ("verified working on the live 5-node testnet" not "tests pass") is internalized. Lessons get captured and re-read. Audits accumulate and inform decisions. The integer-migration workstream and the F3-B workstream both shipped substantial protocol-correctness improvements via methodical multi-part plans with founder approval gates. The Part F retry caught a real bug exactly the way the discipline is designed to. **Continuing the current discipline gets this codebase to a credible mainnet-launch standard.** What needs to change: (a) test invariants need to be generalized (not just "this corpus passes" but "any corpus must pass these properties"); (b) the cmd/node main.go monolith needs decomposition before it eats further startup-ordering bugs; (c) operational tooling needs a real operator-runbook, not just CLAUDE.md.

**The single biggest risk in the codebase right now** (not the known bugs): **the test infrastructure doesn't enforce cross-node consensus uniformity as a continuous invariant**. The selection race shipped through F3-B's 19-criterion matrix because no test asserted "same DAG → same ledger state on every node, for any corpus, every time." Until this becomes a CI-enforced invariant (or a continuous testnet-monitoring alert), there's no guarantee that future commits don't introduce more selection-race-shaped bugs. The bug found this week was the visible manifestation of a verification discipline gap; the gap is the risk.

**The single biggest strength**: **the design-principles document is treated as load-bearing**, and the team enforces it. When Principle 6 ("generalize the primitive, not the fix") was violated by the original F3-B scope, the correction (Part E.1's general admission router rather than a per-event-type adapter) re-applied the principle. When Principle 13 ("tests are necessary, live testnet is sufficient") was put under deadline pressure (Part F retry could have skipped the live verification phase), the discipline held. The principles aren't aspirational; they're what's actually being followed. Preserve and expand: keep writing principles when patterns recur, keep auditing existing code against them, keep refusing to merge code that violates them.

**What would surprise a senior distributed-systems engineer positively**: the conformance test suite at `internal/dispatch/conformance/` (Type A/B/C/D consumer templates with shared CI tests) is excellent; not many protocols this size have a structured conformance discipline. The audit-document accumulation in `docs/audits/` (6 files, all dated, all action-oriented) is a sign of operational maturity. The integer-migration workstream's structure (Parts A-E.1 with explicit completion reports per part) is what a well-run protocol-engineering team looks like. **What would concern them**: (a) the cmd/node main.go monolith — they'd ask "what happens when this file accumulates the next bug?"; (b) the absence of a cross-node consensus-uniformity invariant in CI — they'd recognize this as the verification gap that the selection race surfaced; (c) the under-tested critical surfaces (`internal/store`, `internal/jcs`) — they'd flag these as "the foundation isn't tested as well as the buildings on top."

**Are there parts of the codebase that are structurally different in quality**: yes, distinctly.

- **Highest quality** (A or A−): `internal/dispatch`, `internal/recognition`, `internal/validatorlifecycle`, `internal/replay`, `internal/network`, `internal/protocolmath`. These are the post-F3-B and post-integer-migration packages where the design-principles discipline was applied throughout. They have good test ratios, clean file decomposition, consistent error handling, and explicit doc comments.
- **Average quality** (B or B+): `internal/api`, `internal/settlement`, `internal/taskverification`, `internal/autovalidator`. Functionally correct, well-tested, but show signs of file-size growth and accumulated complexity. Refactoring opportunities exist.
- **Concerning quality** (C or below): `cmd/node` (3469 LoC monolith with 0.06 test ratio), `internal/store` (1569 LoC monolith with 0.21 test ratio), `cmd/aet` (1463 LoC with 0.02 test ratio), `pkg/sdk` (1182 LoC with 0.28 test ratio), `internal/jcs` (130 LoC with 0 tests). These are the structural-quality outliers; they're the places where the next regression is most likely to live.

**Compare to reference points**: closer to **early CockroachDB** (2016-era) than to **mature CockroachDB** (2022-era). Test discipline matches Cockroach's; the pre-stable-release scope and not-yet-runnable-as-production-cluster status also matches. **Closer to Bitcoin Core c. 2011-2012** than Bitcoin Core 2024 in operational tooling: working multi-node deployment but bespoke per-deploy scripting, no rolling upgrade protocol, no continuous monitoring. **Ahead of geth at a similar stage** in test discipline (geth's early version was less rigorously tested) but **behind geth** in API stability and external-developer onboarding (the SDK is under-tested; the API is one giant file). **On par with or ahead of TiKV** at an analogous stage in design-document discipline; behind in operational maturity.

The honest read: **this is a competent protocol team's serious effort, in the second-half of the prototype-to-production transition**. It is not a research project pretending to be production-ready (the design-principles + lessons + audit discipline rule that out). It is also not yet production-ready (operational tooling, CI invariants, and structural consolidation of `cmd/node` + `internal/store` + `internal/jcs` need work). The trajectory is positive; the gaps are knowable; the fix-workstream queue makes sense.

---

## 10. Recommendations

### Immediate (do before fix workstream starts)

1. **Add a cross-node ledger byte-equality invariant test.** Either as a CI test that runs against a multi-node integration setup, or as a continuous testnet-monitoring check that compares per-node ledger state and alerts on drift. Without this, the next selection-race-shaped bug ships unnoticed. Cost: 1-2 days. Why: closes the verification-discipline gap the bug surfaced.

### Short-term (include in the fix workstream)

2. **Fix the multi-emit bug class structurally, not per-event-type.** Per the `multi-emit-bug-class-audit.md`, the 5 HIGH-risk emission sites need a single structural fix (e.g., deterministic event-selection rule per logical key, or single-emitter restriction with deterministic fallback). The architect-session decision on fix shape should explicitly rule out "fix TVConsensus alone." Cost: estimated 2-4 weeks. Why: the bug class is the active blocker.

3. **Wire the missing replay path** for newly-registered bus consumers (`SourceReplay` + a `replayHistoricalToBusConsumers()` pass after `SetOnCommit`). Per the Part F retry Path B finding. Without this, any future consumer added against a populated DAG silently misses historical events. Cost: ~1 day. Why: closes the architectural gap that defeated Sub-scenario A in Part F retry.

### Medium-term (next 2-3 workstreams)

4. **Decompose `cmd/node/main.go`.** Split `startStack` (1511 lines) into per-subsystem wiring functions in their own files (`startup_dag.go`, `startup_recognition.go`, `startup_dispatcher.go`, etc.). Test the wiring functions individually. Cost: 1-2 weeks. Why: this file is where the SetOnCommit-after-LoadFromStore bug lived; future startup-ordering bugs will live here too unless decomposed.

5. **Decompose `internal/api/server.go`.** Split the 4,703 LoC monolith into per-resource handler files matching `admin_handlers.go`'s pattern. Cost: 1 week. Why: file-size-driven complexity in the API layer.

6. **Add unit tests to `internal/jcs` and `internal/store`.** These are the foundations of content-addressing and persistence respectively. Both have <300 LoC of tests for ~1500 LoC of code. Cost: 1-2 weeks. Why: foundation-layer test discipline is the precondition for trusting everything built on top.

7. **Build the rolling-upgrade protocol.** A documented mechanism for cluster-wide binary upgrades that *don't* change consensus-affecting behavior. The integer-migration cutover designed activation-event coordination for the special case; the routine case needs a generalized version. Cost: 2-3 weeks. Why: production operations require it.

### Long-term (before mainnet)

8. **Operator runbook.** Standalone `docs/operations/` with: incident response, common-failure recovery, deploy/upgrade procedures, monitoring setup. Currently CLAUDE.md does double-duty; production needs a separate operator-facing manual. Cost: 1 week (assuming the operational patterns are stable). Why: external operators (i.e., people not in this Slack thread) need it.

9. **Continuous invariant monitoring** beyond just the cross-node ledger check. Other invariants worth continuously asserting: total_supply equals across all nodes; treasury_accrued equals; validator_seat_set equals (which would have caught Divergence A). Cost: 1-2 weeks for the monitoring infrastructure + ongoing for new invariants. Why: production divergence detection without this is detective rather than preventive.

10. **External SDK test suite.** `pkg/sdk` at 0.28 test ratio is too low for an external API. Add property-based tests, error-path tests, version-compat tests. Cost: 1-2 weeks. Why: external users will hit edges internal callers don't.

Not on this list because they're either too speculative or too small: deeper context-propagation review (`taskverification/badger_store.go` has 5 `context.Background()` instances worth fixing — small enough for a one-off PR); the `roundprogress/emitter.go:96 fmt.Printf` → `slog.Warn` swap (5-minute fix); the missing `internal/replay` package doc (5-minute fix). Worth doing, not worth elevating.

---

**End of audit.**

This document represents one engineer's read at one point in time. It's anchored in concrete file:line citations and quantitative metrics where possible, and explicit about uncertainty where measurement was substituted by manual reading. Multi-AI review (Grok, ChatGPT) of these findings is the next step per the founder's process.
