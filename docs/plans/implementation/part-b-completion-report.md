# Part B — Settlement + generation-ledger shadow-gated migrations — completion report

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `c1089ab` (Part A — `internal/protocolmath` primitive)
**Commits produced**: four, in sequence below
**Plan reference**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §4.2–4.5, §5 commits 2–5

| # | Hash | Subject |
|---|---|---|
| 2 | `8ce19bd` | `commit-2(taskverification): AgreementRate and ValidatorQScore return BasisPoints` |
| 3 | `2510ed2` | `commit-3(settlement): wire settler to protocolmath (shadow-gated)` |
| 4 | `2d70848` | `commit-4(settlement): wire generation ledger to protocolmath (shadow-gated)` |
| 5 | `d8505ee` | `commit-5(dispatch): fix flaky ConcurrentSameTask test surfaced by Part B scheduling` |

Each commit builds standalone, passes vet, passes its package tests under `-race`. The branch is buildable at every commit for clean bisect (option (b) decision from plan mode).

## Compile-discipline choice (option b confirmed)

Option (b) was chosen: every commit compiles. Realized by a 4-line BP→float adapter in `cmd/node/main.go` and a 2-line BP→float in `internal/taskverification/slashing.go` introduced in commit-2 and removed in commit-3. Commit-3 and commit-4 each touched `cmd/node/main.go` and the settlement test fixtures to unwind adapters and introduce the `shadowMode bool` parameter progressively; the net effect is a continuously-green branch.

The plan anticipated a separate `commit-5` for wiring sweep; after commits 3 and 4 each propagated type changes through their respective callsites to maintain buildability, a final grep surfaced no remaining un-migrated callsites. The `commit-5` slot instead carries a narrow test-stability fix (see below).

## Floor-claim verification (pre-approval gate)

Before starting commit 2 I re-read `internal/taskverification/reputation.go:192–194` at the branch's current HEAD and confirmed the existence of the explicit `return 0.01` line with the comment "minimum floor to avoid zero-weight (allows recovery)." The migration replaces this with `return 100` (= `0.01 × 10000` in basis points). Behavior preserved exactly.

## Verification results

### Build

```
go build ./...                        # clean, exit 0
```

### Vet

```
go vet ./...                          # clean except for 4 pre-existing atomic.Int64 warnings in *_test.go files, unchanged from baseline
```

### Tests under race detector

```
go test -race -count=3 ./...          # 54 packages ok, 2 pre-existing flakes (see below)
```

The two transient failures in repeated runs are `internal/canary` and `internal/network`. Both flaked on `c1089ab` (the branch HEAD before Part B) when tested under the same `-count=3` conditions. Network tests exercise TCP listener bring-up timing; canary tests have analogous IO-timing sensitivities. Neither failure involves settlement code, `protocolmath`, `taskverification`, `dispatch`, or any package Part B modified. Documented as pre-existing; not in Part B's scope to fix.

### Coverage (affected packages)

| Package | Baseline | Post-Part-B | Δ |
|---|---:|---:|---:|
| `internal/settlement` | 66.1 % | **72.8 %** | +6.7 % |
| `internal/taskverification` | 75.2 % | **75.4 %** | +0.2 % |
| `internal/protocolmath` | (Part A: 100.0 %) | 100.0 % | unchanged |

Both migrated packages show coverage gains driven by the shadow-gate tests (both paths now exercised). No regression.

### Float freedom (integer-path code)

Grepping `internal/settlement/verification_consensus_settler.go` and `internal/settlement/generation_ledger_calculator.go` for `float` produces matches only in:
- The preserved-verbatim `computeValidatorPayoutsFloat` / `calculateFloat` bodies (the legacy path — intentionally untouched).
- Doc comments that describe *what the migration replaces* or that reference the float path by name.
- The 1-line `float64(bp)/10000.0` conversion inside the legacy `computeValidatorPayoutsFloat`, documented as "removed when the float path itself is removed."

**No float appears in the new integer-path code** (`computeValidatorPayoutsInteger`, `calculateInteger`, `logShadowDelta`, `logGenLedgerShadowDelta`, `evenSplitFallback`). Verified by inspection.

### Shadow-delta log format (sample)

Captured from `TestComputeValidatorPayouts_ShadowMode_LogsDelta` / `TestCalculate_ShadowMode_LogsDelta`:

```
level=INFO msg=shadow_delta context=validator_distribution task_id=task-xyz recipient_count=3 float_sum=1000 int_sum=1000 sum_delta=0 max_per_recipient_delta=0 pool=1000
level=INFO msg=shadow_delta context=generation_ledger task_id=c-log recipient_count=1 float_sum=1000 int_sum=1000 sum_delta=0 max_per_recipient_delta=0 pool=1000
```

Format matches plan §4.5 exactly. Elevates to `level=WARN` if `sum_delta != 0` (conservation mismatch in one of the two paths — always a bug).

## Test matrix added in Part B

### Commit 2 (taskverification)

- `TestAgreementRate_IntegerQuantization` — 1/3 → 3333 (integer, not 3333.33 float)
- `TestReputation_ValidatorQScore_ZeroRateFloor` — fully-deviating validator returns 100 BP (1% floor)
- 5 existing tests updated to assert exact integer outputs instead of float ranges

### Commit 3 (settler)

- `TestComputeValidatorPayouts_ShadowMode_ReturnsFloat`
- `TestComputeValidatorPayouts_ShadowMode_LogsDelta`
- `TestComputeValidatorPayoutsInteger_Correctness`
- `TestComputeValidatorPayouts_NonShadowMode_ReturnsInt`
- `TestComputeValidatorPayouts_NegativeQ_ClampedToZero`
- 3 existing Q-weighted tests updated to the new `BasisPoints` signature

### Commit 4 (generation ledger)

- `TestCalculate_ShadowMode_ReturnsFloat`
- `TestCalculate_ShadowMode_LogsDelta`
- `TestCalculateInteger_Correctness`
- `TestCalculate_NonShadowMode_ReturnsInt`
- `TestCalculateInteger_NegativeQ_ClampedToZero`
- `TestCalculateInteger_QAboveCeiling_ClampedToMax`
- `TestCalculate_NeutralQualityFn_IntegerMatchesFloat_WithinTolerance`
- 6 existing tests updated to the new `qualityFn` signature + `shadowMode` parameter

## Deviations from the prompt

**None of semantic significance.** The four explicit plan points are preserved:

1. Float path untouched except for extraction into `computeValidatorPayoutsFloat` / `calculateFloat` (verbatim-copied bodies).
2. Integer path unconditionally routes through `protocolmath.Allocate` / `AllocateWithCeiling`.
3. `shadowMode` is a hardcoded `true` constructor parameter; never flipped in Part B.
4. Pre-clamp negatives at the settler/genledger callsites; `protocolmath.ErrInvariantViolation` remains a true-impossibility signal.

One minor scope shift noted:

- The prompt anticipated `commit-5` as a wiring-sweep commit. After commits 3 and 4 propagated type changes through all callsites to maintain buildability, no wiring work remained for commit-5. The slot was used instead for a narrow test-stability fix in `internal/dispatch/` (documented in the commit message — a pre-existing concurrent-test flake that Part B's timing changes surfaced). This is a test-file-only change; no production code affected.

## ⚠️ Prominent — latent float-path remainder-absorption non-determinism (discovery for Part F)

**Part F's testnet rehearsal must look for this.**

The legacy float path's `computeValidatorPayoutsFloat` absorbs its rounding remainder at the *caller-slice-last* recipient — the last entry in the `recipients` slice passed to it. That slice is built by `collectAgreeingValidators(round, verdict)`, which iterates `round.Votes` in receive-order. Receive-order is determined by local vote-event fastpath arrival and is **not guaranteed stable across nodes**. Nodes that observe vote events in different orders will attribute the rounding remainder to different recipients.

**Concrete failure mode in the pre-migration code**: five nodes, three agreeing validators, pool=23,115, per-validator share = floor(23,115 × q / totalQ). If the remainder is 2 µAET, it lands on whichever validator happens to be last in each node's `recipients` slice. If node A sees votes in order [v1, v2, v3] and node B sees them in order [v3, v1, v2], v3 gets the remainder on A and v2 gets it on B. Per-node ledger state diverges by up to the remainder (typically 1–2 µAET per recipient, small enough not to show up in coarse audits but real).

The integer path **fixes** this: `protocolmath.AllocateWithCeiling` sorts by `CanonicalKey` (AgentID bytes) before allocation and routes remainder to the sorted-last recipient. Deterministic across nodes.

**Implication for Part F**: the cutover corpus should look for shadow-delta log lines where `max_per_recipient_delta > 0` on the **same task** across different nodes. Those lines correspond to exactly the non-determinism above. If the corpus shows them in the float path, that's evidence the bug was live in production and Part E's cutover will simultaneously fix it. If the corpus doesn't show them, the non-determinism was latent in practice (vote receive-order happened to converge) and the cutover is purely a defensive move.

The doc comment on `computeValidatorPayoutsFloat` now records this observation inline in the source so future readers reach the same conclusion.

## Additional discoveries for Parts C–G

1. **`RoyaltyRecipient.Weight` type shift, not schema migration.** The field's type changed from `float64` to `protocolmath.BasisPoints`, and its semantics from "normalized fraction in [0, 1]" to "share of pool in basis points in [0, 10000]". `GenerationLedgerDistribution` is confirmed in-memory-only (lives only in `SettleResult`); never persisted, never transported on the wire. Part C's AST lint should therefore not flag this field as a canonical-payload regression — it never reaches canonical serialization.

2. **Policy-type BP migration is its own workstream.** `internal/taskverification/slashing.go` retains `SystematicDivergenceThreshold float64` in the policy struct with a temp BP→float conversion at the comparison callsite. A separate future workstream should migrate policy thresholds from float to BP; out of scope for the current migration. The temp conversion is documented at the callsite with a "Part B commit-2" reference so a future editor knows where the thread leads.

3. **HTTP API exposed via `AgreementRate` is now integer-quantized.** Any HTTP response that formatted `AgreementRate()` as a percentage produced ~15-digit float output before Part B and will produce integer-basis-point output after. No API handler was grepped that serializes the method directly today, but a follow-up review of `internal/api/` output shapes should confirm no downstream consumer (dashboard, external client) relied on the float precision. If external consumers care, the API can expose both representations.

4. **`protocolmath.AllocateWithCeiling`'s silent-positive-clamp + loud-negative-error contract** plays cleanly with the pre-clamp-at-callsite discipline (Part A discovery #3). Part B's settler and generation ledger both pre-clamp negatives with a WARN log and let `AllocateWithCeiling` do the positive clamp. `ErrInvariantViolation` becomes unreachable in the production happy path — it would only fire if the pre-clamp were bypassed, which is itself a bug signal rather than routine traffic. Part C's runtime assertion can treat this error as a panic-worthy invariant failure.

5. **Shadow-delta log expected magnitudes.** In the common case (neutral Q across all recipients, small pool), per-recipient delta is 0 and sum delta is 0. In the Q-weighted case at non-round-basis-point weights, per-recipient delta is 0–2 µAET (rounding) and sum delta is 0. The only observed non-zero sum delta would be a conservation bug. Part F's alerting threshold on `sum_delta != 0` should page; threshold on `max_per_recipient_delta > 5` is a reasonable sanity check but not an incident trigger.

6. **Test-helper caveat surfaced by commit-5.** `TestTVConsensusConsumer_Apply_ConcurrentSameTask_Serialized` demonstrated that latent test flakiness can be hidden by Go's goroutine scheduler tending one way until something perturbs the schedule (in this case, Part B's shadow-mode insertion adding ~µs of settler work). Future concurrent tests should save state for every possible race-winner, not rely on a particular winner. The commit-5 fix restores that discipline for this one test.

## Verification commands (reproducible)

```bash
git checkout d8505ee
go build ./...
go vet ./...
go test -race -count=3 ./internal/settlement/... ./internal/taskverification/... ./internal/protocolmath/... ./internal/dispatch/...
go test -cover ./internal/settlement/... ./internal/taskverification/... ./internal/protocolmath/...
grep -n "float" internal/settlement/verification_consensus_settler.go internal/settlement/generation_ledger_calculator.go
```

All pass / produce the documented matches on the committed code.

## State

Branch: `feat/canonical-distribution-integer-migration` at `d8505ee`, not yet pushed — awaiting review.
Part C (AST lint + runtime assertion for canonical-payload float-freedom) follows in a separate session.
