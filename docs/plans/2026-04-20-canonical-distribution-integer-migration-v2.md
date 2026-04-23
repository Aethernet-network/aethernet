# Canonical Distribution Integer Migration — v2

**Location**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md`
**Status**: DRAFT v2 — awaiting founder approval
**Author**: Claude (architect session, 2026-04-20)
**Base commit**: `603bd9b` (merge F3-B settlement consensus integrity fix)
**Synthesizes**: v1 draft + ChatGPT rigor review + Grok red-team review + canonical-payload closure audit
**Related**: `docs/design-principles.md` (Principles 5, 6, 7, 10, 11, 12, 14, 15); `docs/plans/2026-04-15-settlement-consensus-integrity-fix.md` (F3-B, dispatcher primitive); `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` (reputation workstream blocked on this plan)

---

## 0. Change log from v1

Substantive changes made in response to multi-AI review:

1. **Scope framing rewritten**: this is not "remove floats" — it is "define a canonical quantization rule for the settlement distribution path." Protocol semantics, not implementation detail. (ChatGPT §3, §8-A)
2. **Shared primitive introduced**: new `internal/protocolmath` package carrying `BasisPoints` type, `MicroAET` type, canonical proportional allocator, deterministic remainder policy, wide multiply/divide helper. Satisfies Principle 6 structurally. (ChatGPT §7)
3. **Canonical recipient sort before distribution**: sort by `AgentID` bytes ascending. Remainder goes to last recipient *of the sorted order*, not last of inherited iteration order. (Both reviewers)
4. **`math/big.Int` unconditionally** at the multiply-divide step. Not "if needed." (Both reviewers)
5. **Q-ceiling enforced** at the settler boundary, not documented as convention. Reject negatives, clamp positives above ceiling. (Both reviewers)
6. **`totalQ == 0` triggers fallback**; negative Q is an invariant violation, not a fallback case. (ChatGPT §2)
7. **Overflow analysis corrected**: v1 had it off by a factor of 10 (Q_max=100000 → safe pool 9.2e13, not 9.2e14). (ChatGPT §2)
8. **Migration approach rewritten**: shadow-compare during transition, not "≤1 µAET tolerance." Old and new code run in parallel; canonical path switches at an explicit DAG-epoch feature gate. Deltas are logged, not silently accepted. (Both reviewers)
9. **Heterogeneous hardware verification included in this workstream**, not deferred. Docker-based ARM runner in CI. (Both reviewers)
10. **Canonical-payload lint strengthened**: AST lint + runtime assertion at serialization path. Belt and suspenders. (Grok §4)
11. **Commits 1 and 3 no longer called "type-only"**: correctly labeled as numeric-representation-changes with explicit acknowledgment of quantization semantics. (ChatGPT §6)
12. **§6.3 criterion 13 rewritten**: instead of "≤1 µAET tolerance," dual-path shadow compare with explicit conservation + determinism + documented allocation changes. (ChatGPT §5)
13. **Boundary audit appendix added** (§10) with grep-cited proof that analyzer floats do not reach canonical payloads. Closes Grok §2 attack.
14. **Generation ledger scope simplified**: `qualityFn` is currently `always 1.0` (confirmed by code inspection). Migration is representational-only today; the integer scheme is locked now so that prompt-08's real Q values land on correct infrastructure. (Code inspection outcome)

---

## 1. Mission anchor

AetherNet is the trust and settlement layer for the AI agent economy. The protocol's unique value is a dataset of verified work that downstream systems consume for years, whose integrity rests on compound verification producing byte-identical results across structurally independent nodes running on heterogeneous hardware.

Content-addressing (Principle 10) is the integrity mechanism. Integer canonical state (Principle 11) is what makes content-addressing actually work — floats are non-deterministic across hardware, and a protocol that uses floats in consensus-affecting computations produces different hashes on different CPUs for identical inputs.

F3-B proved byte-identical ledger convergence on the 5-node testnet. That proof is conditional on every node running identical hardware (AWS m7i, x86-64, same Go version). The settlement distribution computation currently uses `float64` arithmetic. On the current testnet this is invisible; on a production validator set with ARM instances, different Go versions, or future CPU generations that reorder float operations, it becomes a cross-node divergence bug that corrupts the dataset the protocol exists to produce.

This plan eliminates the `float64` usage in canonical settlement distribution before the reputation evidence store (Step 4) begins writing records derived from those distributions. Evidence records are the first durable artifact downstream systems will consume; their cross-node byte-identity cannot depend on homogeneous hardware.

## 2. The finding

A systematic grep of `internal/` for `float` identified float usage across the codebase. Analysis against the canonical-replayed-state boundary resolved the findings into four buckets.

### 2.1 Floats in canonical replayed state (must be integers) — THIS PLAN'S SCOPE

Three call sites across two files compute canonical settlement distribution amounts using `float64`:

**`internal/settlement/verification_consensus_settler.go`**
- Line 30: `type ValidatorQScoreFn func(validatorID crypto.AgentID, family string, category string) float64`.
- Lines 352–387: `computeValidatorPayouts` uses `float64` throughout — stores per-validator Q as `float64`, sums to `totalQ float64`, computes per-validator share as `uint64(float64(pool) * (e.q / totalQ))`.

**`internal/settlement/generation_ledger_calculator.go`**
- Line 20: `Weight float64` in `RoyaltyRecipient`.
- Lines 43, 57: `qualityFn func(event.EventID) float64` — quality function type.
- Lines 107–155: weight accumulation and distribution use `float64`; final share computed as `uint64(float64(poolMicroAET) * (a.weight / totalWeight))`.

**`internal/taskverification/reputation.go`**
- Lines 165–186: `ValidatorQScore` function returns `float64`. Concrete implementation behind `ValidatorQScoreFn`.

These three call sites produce amounts that flow into `Escrow.ReleaseSettlement` → `TransferLedger` → ledger state. Ledger state is canonical replayed state. Therefore the inputs to `ReleaseSettlement` must be deterministic across hardware.

### 2.2 Floats in local computation that produces integer canonical output (may stay)

The analyzer families and verification service use `float64` internally but their outputs are converted to integer basis-points at the event-serialization boundary. Floats never cross the consensus boundary from these subsystems.

**This claim is proven by the boundary audit in §10, not asserted.** See §10 for the grep-cited evidence that the 17 canonical event payload types contain no float fields transitively.

Files in this bucket (no changes proposed):
- `internal/evidence/*.go`
- `internal/verification/service.go` and children (`deterministic.go`, `subjective.go`, `inprocess.go`, `consensus_check.go`)
- `internal/verification/families/*.go`

The integer boundary is held at:
- `internal/event/event.go`: `TaskVerificationVotePayload.ScoreBP uint64`, `ScoreBreakdown map[string]uint64`.
- `internal/event/event.go`: `TaskVerificationConsensusPayload.FinalScoreBP uint64`.
- `internal/event/trajectory.go`: `TrajectoryCommitPayload.QualityScoreBP uint32` (validated `[0, 10000]`).

**Future-hardening note**: an analyzer family whose float computation is non-deterministic across hardware could produce different integer basis-points on different nodes, even though the canonical field is integer. This is a latent correctness concern for the analyzer family layer, not for the settlement distribution layer. If it manifests on heterogeneous hardware, it surfaces a distinct workstream (analyzer-family determinism audit) that is independent of this plan's merge.

### 2.3 Floats in advisory / node-local / operational paths (may stay)

Metrics, routing, discovery, rate limiting, canary, replay sampling, validator assignment, reputation aggregation, assurance fee splits, harness, network peer-selection, `internal/consensus/voting.go` ratio thresholds. These are not canonical replayed state. Not in scope.

### 2.4 JCS / canonicalization float handlers

`internal/jcs/jcs.go` and `internal/auth/canonical.go` contain `float64` handlers in the JSON canonicalization path. Given §2.2's proven boundary, these branches never execute on canonical events.

**v2 decision**: leave handlers in place; add a runtime assertion (§4.4) that fails canonical serialization if a float field is ever present in a canonical payload. Belt-and-suspenders with the AST lint. Removal of the handlers is a separate future workstream for non-canonical JCS consumers, out of scope.

## 3. Principle analysis

**Principle 5 (protocol is source of truth).** Ledger state is the DAG's projection; if distribution computation is non-deterministic, ledger state diverges and the DAG's authority is undermined. This plan restores determinism.

**Principle 6 (generalize the primitive, not the fix).** v1 proposed two bespoke integer migrations. v2 introduces `internal/protocolmath` — a shared primitive carrying `BasisPoints`, `MicroAET`, canonical proportional allocator, deterministic remainder policy, wide multiply/divide helper. Both settlement sites call into it. This is the principle's structural satisfaction, not rhetorical.

**Principle 7 (reuse mechanism, separate concern).** Shared allocator mechanism; separate policy at validator payout vs. generation attribution call sites. Each caller defines its own recipient set and pool; the mechanism handles the arithmetic.

**Principle 10 (content addressing is the integrity model).** Distribution amounts serialize into `Transfer` events which are content-addressed. v2 eliminates the float-driven divergence source; the boundary audit (§10) closes the remaining concern about analyzer-derived integers drifting on heterogeneous hardware (separate workstream).

**Principle 11 (integer canonical state, no exceptions).** Direct enforcement for the settlement distribution path. Boundary audit proves §2.2's dismissal is factually correct for the canonical event layer. Analyzer-family internal determinism is a separate concern tracked as a future workstream; it is not a v2 violation because analyzer outputs are already integer-typed at the canonical boundary.

**Principle 12 (beauty is correctness).** v2's `protocolmath` primitive is short, composable, and makes failure modes enumerable. The canonical-sort + last-recipient-absorbs + big.Int scheme is the minimal correct design. Ugliness of v1 (order dependency, hand-waved overflow, tolerance theater) is eliminated.

**Principle 14 (the standard is permanent).** v2 does not defer the fix. The shared primitive, the heterogeneous hardware verification, and the shadow-compare migration are all done in this workstream, not queued as "later."

**Principle 15 (observable evidence beats self-reported claims).** Tangentially relevant: honest nodes producing identical canonical state from identical inputs is a baseline assumption of the protocol's byzantine-tolerance model. Floats break that baseline silently. Integer math with deterministic ordering restores it.

## 4. Design

### 4.1 `internal/protocolmath` — shared deterministic allocation primitive

New package. Single place where all canonical proportional allocation happens.

**Types:**

```go
package protocolmath

// BasisPoints is an integer representation of a ratio, where 10000 = 1.0.
// Range: [0, MaxBasisPoints]. Negative values are invariant violations.
type BasisPoints int64

// MicroAET is the protocol's canonical unit of value. 1 AET = 10^6 µAET.
type MicroAET uint64

const (
    // NeutralBP is the "Q = 1.0" convention in basis points.
    NeutralBP BasisPoints = 10000
    
    // MaxBasisPoints is the protocol-enforced ceiling for any Q-like score.
    // Enforced at producer boundary and at every allocator callsite.
    MaxBasisPoints BasisPoints = 100000
)
```

**Proportional allocator:**

```go
// Recipient represents one payee in a proportional allocation.
// CanonicalKey is used for deterministic ordering; commonly AgentID bytes.
type Recipient struct {
    CanonicalKey []byte
    Weight       BasisPoints
}

// Allocate distributes pool among recipients weighted by Weight.
// Returns a map keyed by stringified CanonicalKey.
//
// Invariants:
//   - Recipients with Weight == 0 receive 0.
//   - Sum of returned amounts equals pool exactly (last-sorted-recipient absorbs remainder).
//   - Ordering is deterministic: recipients are sorted by CanonicalKey ascending before allocation.
//   - Negative Weight returns ErrInvariantViolation (never silently absorbed).
//   - Total Weight == 0 returns even-split (pool / len, last absorbs remainder).
//   - All internal arithmetic uses math/big.Int; no float64; no int64 overflow.
//
// Returns ErrEmptyRecipients if len(recipients) == 0 and pool > 0.
// (Caller must route pool to treasury in that case — this function does not know about treasury.)
func Allocate(recipients []Recipient, pool MicroAET) (map[string]MicroAET, error)

// AllocateWithCeiling is Allocate but clamps every Weight to [0, MaxBasisPoints] before
// computation. Weights above ceiling are clamped; negatives still return ErrInvariantViolation.
// Used at boundaries that receive external Q values and want defensive clamping.
func AllocateWithCeiling(recipients []Recipient, pool MicroAET) (map[string]MicroAET, error)
```

**Wide multiply-divide helper** (internal, not exported; used by `Allocate`):

```go
// mulDivBig computes (a * b) / c using math/big.Int, returning uint64.
// Panics if the result does not fit in uint64. Callers must prove non-overflow.
// In practice: pool * weight / totalWeight where pool is MicroAET and weight sums
// are BasisPoints; the result is bounded by pool.
func mulDivBig(a, b, c *big.Int) uint64
```

**Errors:**

```go
var (
    ErrInvariantViolation = errors.New("protocolmath: invariant violation (negative weight)")
    ErrEmptyRecipients    = errors.New("protocolmath: empty recipient set with nonzero pool")
)
```

**Determinism tests** (required as part of `protocolmath`'s own unit suite):
- Permutation invariance: same recipients in shuffled order produce the same output map.
- Conservation: sum of outputs equals input pool exactly.
- Zero-weight handling: recipients with Weight=0 get 0.
- Even-split fallback: totalWeight=0 → each recipient gets pool/N, last absorbs remainder.
- Invariant rejection: negative weight returns error; does not silently become zero.
- Overflow bound: corpus of near-max inputs (pool close to uint64 max, weights at ceiling) produces correct output without panic.
- Ceiling clamp: `AllocateWithCeiling` with weight > MaxBasisPoints produces the same result as weight = MaxBasisPoints.

### 4.2 Settlement distribution rewrite

`computeValidatorPayouts` becomes a thin wrapper around `protocolmath.AllocateWithCeiling`:

```go
func (s *VerificationConsensusSettler) computeValidatorPayouts(
    recipients []crypto.AgentID,
    pool uint64,
    category string,
) map[crypto.AgentID]uint64 {
    if len(recipients) == 0 || pool == 0 {
        return map[crypto.AgentID]uint64{}
    }
    
    pmRecipients := make([]protocolmath.Recipient, 0, len(recipients))
    for _, v := range recipients {
        q := protocolmath.NeutralBP
        if s.qScoreFn != nil {
            q = s.qScoreFn(v, "", category)
        }
        pmRecipients = append(pmRecipients, protocolmath.Recipient{
            CanonicalKey: []byte(v),
            Weight:       q,
        })
    }
    
    result, err := protocolmath.AllocateWithCeiling(pmRecipients, protocolmath.MicroAET(pool))
    if err != nil {
        // Invariant violation: log and fall back to even-split via a defensive retry
        // with all weights normalized to NeutralBP. Log as warn — this indicates a
        // bug in the upstream qScoreFn.
        slog.Warn("settlement: qScoreFn invariant violation, using even-split", "err", err)
        return evenSplitFallback(recipients, pool)
    }
    
    out := make(map[crypto.AgentID]uint64, len(result))
    for k, v := range result {
        out[crypto.AgentID(k)] = uint64(v)
    }
    return out
}
```

The fallback on invariant violation is defensive and logged. Principle 12: if the upstream producer is sending negative Q, the protocol doesn't crash; it degrades to even-split and logs loudly so operators see it.

### 4.3 Generation ledger migration

Current state (confirmed by code inspection): `qualityFn` always returns `1.0`. The migration today is representational — replace `1.0` with `NeutralBP` and run the same weighted distribution through `protocolmath.Allocate`.

```go
// Weights: weight = NeutralBP / (depth * depth)
// At depth 1: weight = 10000
// At depth 2: weight = 2500
// At depth 3: weight = 1111
// (GenerationLedgerMaxDepth = 3)

for _, a := range ancestors {
    weight := protocolmath.BasisPoints(a.qualityBP) / protocolmath.BasisPoints(a.depth*a.depth)
    pmRecipients = append(pmRecipients, protocolmath.Recipient{
        CanonicalKey: []byte(a.eventID),
        Weight:       weight,
    })
}

result, _ := protocolmath.Allocate(pmRecipients, protocolmath.MicroAET(poolMicroAET))
```

**Why no SCALE constant in v2**: because `qualityFn` is `NeutralBP` today and max depth is 3, weights fit comfortably in int64 without scaling. When prompt-08 wires real Q from the reputation store, quality will arrive as `BasisPoints` (protocol-enforced `[0, MaxBasisPoints]`) and the existing scheme handles it — weight at max Q, depth 3 is `100000/9 = 11111`, still trivially bounded. The `protocolmath` arithmetic uses `big.Int` internally, so even pathological future cases don't overflow.

**Decay function preserved**: `1/depth²` is the v4.1 economic model. This plan does not change economics. It changes numeric representation.

**`RoyaltyRecipient.Weight`** changes from `float64` to `BasisPoints`. Persisted normalized weight (the `a.weight / totalWeight` expression) becomes `BasisPoints` representing the recipient's share of the pool in basis points. This is a schema change for persisted distribution records if any exist — confirmed by code inspection that `GenerationLedgerDistribution` is not persisted (it lives only in `SettleResult` which is an in-memory return type).

### 4.4 Canonical-payload float prevention (AST lint + runtime assertion)

**AST lint** (`internal/event/lint/` or extended from existing lint):
- Walks the type declaration of every concrete payload type in `internal/event/`.
- Transitively checks all field types: no `float32`, `float64`, `interface{}`, `any`, `json.RawMessage` (in user-visible fields), untyped `map` values, or `*float*`.
- Generic types: reject if any type parameter could be instantiated as float.
- Embedded structs: recurse through.
- Fails the build on violation.

**Runtime assertion** at canonical serialization:
- `jcs.Canonicalize` receives `[]byte` so can't directly inspect types. Add a wrapper `jcs.CanonicalizeCanonical([]byte, payloadType reflect.Type)` used by the canonical event construction path.
- The wrapper reflects on the payload type and asserts no float fields. Panics on violation (caught at test time; shouldn't run in production because the AST lint prevents regression).
- Overhead: one reflection pass per canonical event construction. Measured in nanoseconds; acceptable at protocol speed.

This is defense-in-depth: AST lint catches regressions at compile time; runtime assertion catches anything the lint missed (e.g., dynamic `interface{}` sneaking through).

### 4.5 Migration strategy — shadow compare with explicit cutover

**No "≤1 µAET tolerance" handwaving.** Replaced with a disciplined three-phase rollout:

**Phase 1 — Implement and Shadow (commits 1–6).** All integer math implemented in `protocolmath` and wired in the settler and generation ledger. But: the code path is behind a feature gate. When the gate is OFF (default), the old float math runs and writes the canonical output. The new integer math runs in parallel and the delta is logged (not persisted, not consensus-affecting). Every node logs `settlement.shadow_delta task_id=X float_amount=Y int_amount=Z delta=Δ`. Operators observe the delta distribution across a corpus of real settlements.

**Phase 2 — Flip at an explicit DAG epoch.** A governance-level decision fixes a specific DAG epoch for cutover. At that epoch, every node's feature gate flips from OFF to ON. Validators upgrade to the new binary *before* the epoch. The flip is canonical: events with `finalization_time_unix >= cutover_epoch_unix` use integer math; events before use float math. The cutover epoch is recorded in a new canonical event type (`EventTypeProtocolUpgrade` or similar — specify the exact type in implementation) so replay always produces the correct output for historical events.

**Phase 3 — Remove float code (future workstream).** Once phase 2 has run without incident for an observation period, a follow-up workstream removes the float code path. This is not in this plan's scope but is committed in the pending items list.

**Why this is necessary**: a BFT DAG cannot tolerate cross-version output divergence mid-round. A 1 µAET delta between nodes on old vs. new code is a fork. Shadow mode lets the new code run and be validated without producing canonical output until every node has upgraded. The explicit epoch cutover makes the switchover an event the protocol can replay deterministically.

**This is the single biggest change from v1.** v1's "≤1 µAET tolerance" was a consensus-code disaster waiting to happen.

## 5. Sequencing

Branch: `feat/canonical-distribution-integer-migration`

**Commits:**

1. **commit-1** (`internal/protocolmath/`): new package — types, `Allocate`, `AllocateWithCeiling`, wide multiply-divide, full unit test suite including determinism tests. No integration with settler yet.

2. **commit-2** (`internal/taskverification/reputation.go`): `ValidatorQScore` returns `BasisPoints` instead of `float64`. This is a **numeric-representation change**, not type-only — `AgreementRate` goes from `float64(a)/float64(t)` to `(a * 10000) / t`, which quantizes earlier and may differ from float at fractions near boundaries. Tests updated to assert exact integer outputs. Acknowledged in the commit message.

3. **commit-3** (`internal/settlement/verification_consensus_settler.go`): `ValidatorQScoreFn` signature changes to `BasisPoints`. `computeValidatorPayouts` becomes the thin wrapper over `protocolmath.AllocateWithCeiling`. **Feature-gated**: old float path remains; new integer path runs in shadow. Emits `shadow_delta` log when deltas are nonzero. Tests cover both paths and compare outputs.

4. **commit-4** (`internal/settlement/generation_ledger_calculator.go`): `qualityFn` signature changes to `BasisPoints`. Distribution math migrated to `protocolmath.Allocate`. Same shadow-gate pattern. `RoyaltyRecipient.Weight` field changes type from `float64` to `BasisPoints`.

5. **commit-5** (`cmd/node/main.go` and wiring): callsites updated to pass `ValidatorQScore` (now returning `BasisPoints`) as `qScoreFn`. Feature gate default: OFF (shadow mode).

6. **commit-6** (`internal/event/lint/` + `internal/jcs/canonical_assert.go`): AST lint for canonical payload float-freedom; runtime assertion wrapper. Both enabled in CI.

7. **commit-7** (heterogeneous hardware test rig): Docker setup that runs the corpus replay test on both x86 and ARM emulated targets. One test binary, two architectures, byte-identical output assertion. Added to CI.

8. **commit-8** (shadow observation period): infrastructure only — `shadow_delta` metric exposed, dashboard/alerting spec. Actual observation is operational, not a commit.

9. **commit-9** (cutover event type): define `EventTypeProtocolUpgrade` or equivalent canonical event type that records the integer-math activation epoch. Add to event package, event registry, and the consumers that read from it (the shadow-gate check).

10. **commit-10** (testnet cutover rehearsal): on the 5-node testnet, perform a full shadow-then-flip cycle. Shadow log for a corpus of tasks, verify delta distribution is as expected, emit cutover event, verify flip, verify post-flip settlements use integer math byte-identically across all 5 nodes.

11. **commit-11** (docs): `docs/lessons.md` updated with the finding and the fix; handoff document workstream queue updated.

Each commit compiles, passes tests, and preserves correctness. Commits 2–4 are NOT "type-only" — each changes numeric representation in ways that may produce different amounts. The shadow gate prevents any canonical behavior change until the cutover event is issued.

## 6. Verification

### 6.1 Unit tests (in `protocolmath` + callsite tests)

- Permutation invariance (shuffled recipients → identical output).
- Conservation (sum exactly equals pool).
- Zero-weight handling.
- Even-split fallback on totalWeight == 0.
- Invariant rejection on negative weight.
- Overflow-bound corpus (near-max pool, weights at ceiling).
- Ceiling clamp behavior.
- Shadow-delta correctness (new path produces output within expected bounds vs. old path on a known corpus).

### 6.2 Cross-run determinism test

Part of `protocolmath` unit suite. Run `Allocate` 1000× with identical inputs; assert byte-identical map output every time. Catches any residual nondeterminism (RNG, map iteration that slipped through).

### 6.3 Heterogeneous hardware test (commit-7)

Docker-based. Builds two images: `aethernet-test:x86` and `aethernet-test:arm64` (via `docker buildx --platform`). Each runs the same corpus replay:
- 100 tasks with varied recipient counts (1, 3, 5, 10, 20).
- Varied Q distributions (neutral, skewed, at ceiling, at zero, mixed).
- Varied pool sizes (small, medium, near-overflow bound).

Output: JSON file of per-task per-recipient amounts. Test asserts byte-identical output between the two architectures. This is the definitive proof that the settlement distribution produces identical results across hardware.

Runs in CI on every commit. Failure blocks merge.

### 6.4 Live testnet verification (§10-style)

| # | Criterion |
|---|-----------|
| 1 | `go test -race ./...` clean |
| 2 | `go vet ./...` clean |
| 3 | `go build ./...` clean |
| 4 | Docker image built + pushed |
| 5 | All 5 nodes deploy + startup clean |
| 6 | Shadow-mode phase: 20-task corpus runs; every node logs `shadow_delta`; deltas distribution across all tasks is within expected bounds; no task produces a shadow delta greater than N µAET per recipient (N to be set per theoretical bound, not per convenience) |
| 7 | Cutover event emitted at planned epoch; all 5 nodes observe it; feature gate flips atomically at that epoch |
| 8 | Post-cutover: 10-task corpus runs with integer math; all 5 nodes produce byte-identical ledger state |
| 9 | Q-weighted distribution produces byte-identical per-validator amounts across 5 nodes for tasks with 3+ validators |
| 10 | Generation ledger distribution produces byte-identical per-recipient amounts across 5 nodes |
| 11 | Canonical payload float lint passes |
| 12 | Runtime assertion wrapper: no canonical event serialization triggers the float assertion |
| 13 | Cross-run determinism test (6.2) passes |
| 14 | Heterogeneous hardware test (6.3) passes: x86 and ARM produce byte-identical output |
| 15 | Historical replay: tasks settled under old float math replay byte-identically from genesis on new binary (replay consults the cutover event and uses float math for pre-cutover tasks) |
| 16 | Zero-Q fallback: test task with all-zero Q produces even-split |
| 17 | Invariant rejection: test with negative Q triggers warn-log + even-split fallback (no panic, no divergence) |
| 18 | Conservation: every settled task's sum of payouts + treasury equals budget exactly |
| 19 | No regression on F3-B §10 criteria 6, 7, 8 (convergence, restart, replay) |

Success threshold: 19 PASS. Same "non-F3-B-attributable, non-this-plan-attributable" carve-outs policy.

## 7. Resolved open questions

v1 listed 8 open questions. v2 answers all of them (post-review + post-audit):

1. **Q-score ceiling**: `MaxBasisPoints = 100000` (10× neutral). Enforced in `AllocateWithCeiling`; negatives rejected outright.

2. **SCALE constant for generation ledger**: not needed. `qualityFn` is currently constant (`NeutralBP`), max depth is 3, arithmetic fits trivially in `BasisPoints`. `protocolmath` uses `big.Int` internally regardless, so even when prompt-08 wires real Q, the scheme handles it without a SCALE constant.

3. **Vote ordering determinism**: `round.Votes` is a slice (code inspection confirmed). `Canonical()` sorts by `ValidatorID` for serialization. The settler currently uses insertion order; v2 changes this by going through `protocolmath.Allocate` which sorts by `CanonicalKey` ([]byte of AgentID) before distribution. Determinism is structural, not inherited.

4. **Migration tolerance**: eliminated. Replaced with shadow-compare + explicit epoch cutover (§4.5).

5. **JCS float handler removal**: leave in place. AST lint + runtime assertion are sufficient guards. Removal is a separate future workstream.

6. **Generation ledger `qualityFn` source**: confirmed `always 1.0 (neutral)` today. Prompt-08 will wire real quality from reputation store as `BasisPoints`. Clean.

7. **Overflow handling policy**: `math/big.Int` unconditionally inside `protocolmath`. Allocations are fine at protocol speed.

8. **Hardware coverage**: heterogeneous hardware test (commit-7) runs in CI. x86 + ARM byte-identical assertion on every commit. Not deferred.

## 8. Workstream sequencing update

**This plan merges first. Then reputation Step 4 resumes.**

Updated pending items list (replaces the handoff's current list):

- [NEXT] Canonical distribution integer migration (this plan, v2)
- [BLOCKED on above] Reputation Step 4 — evidence store
- [PENDING] Analyzer-family determinism audit (follow-up: verify `internal/evidence/` and `internal/verification/families/` produce hardware-deterministic integer `ScoreBP` outputs; this is distinct from this plan and sequenced after Step 4 unless heterogeneous-hardware testing surfaces an issue sooner)
- [PENDING] JCS float handler removal (follow-up to this plan)
- [PENDING] Accept-path live verification (after third analyzer family ships)
- [PENDING] Trajectory integration workstream design
- [PENDING] Stage 3 vote-ingestion originator-vs-receiver asymmetry audit
- [PENDING] StakeManager + Identity Registry retrofit pass
- [PENDING] Claim-path 60-second router-assignment lag fix
- [PENDING] Slashing consumer for `PrerequisiteWithholding` evidence events
- [PENDING] Marketplace escrow integration workstream
- [PENDING] `SetDispatcher` nil-fallthrough cleanup in recognition consumer
- [PENDING] V2-only fastpath sync gap (BlobSync workstream)
- [PENDING] Docker bridge partition testing infrastructure

## 9. What NOT to do (forward guardrails)

- **Do not expand scope during implementation** to cover §2.3 advisory surfaces. Each is its own decision.
- **Do not skip the shadow phase.** Feature-gated cutover is load-bearing. A direct flip on mainnet without shadow observation is a reckless consensus-code rollout.
- **Do not remove the float code path in this workstream.** Phase 3 is a separate workstream scoped after mainnet observation. Leaving the float path means replay of pre-cutover events produces correct historical amounts.
- **Do not use int64 arithmetic "because it fits"** inside `protocolmath`. Use `big.Int` unconditionally. The allocation cost is irrelevant at settlement speed; the correctness guarantee is not.
- **Do not rewrite the analyzer families** in `internal/evidence/` or `internal/verification/families/`. Future workstream.
- **Do not approve this plan without confirming the shadow + cutover flow can be implemented on the testnet**. Testnet cutover rehearsal (commit-10) is the founder-visible verification that the migration mechanism works before mainnet hardening begins.

## 10. Boundary audit (required appendix)

**Purpose**: prove §2.2's dismissal of analyzer-family floats is factually correct — that no float value from the analyzer or verification layer reaches a canonical event payload field.

### 10.1 Canonical payload type inventory

Evidence from `grep -rn "Payload" internal/event/ --include="*.go"`, cross-referenced with `internal/event/event.go` lines 226–232 (event type → payload type dispatch table).

The 17 registered canonical event payload types are:

1. `TransferPayload` (`event.go:511`) — sender, recipient, amount (uint64), reason, timestamp. Integer + string.
2. `GenerationPayload` (`event.go:551`) — claimed value (uint64), evidence hash, metadata. Integer + string.
3. `AttestationPayload` (`event.go:585`) — target event, verdict, staked amount (uint64). Integer + bool + string.
4. `VerificationPayload` (`event.go:613`) — verifying agent, target event, verdict (bool), evidence hash, staked amount (uint64). Integer + bool + string.
5. `DelegationPayload` (`event.go:643`) — delegator, delegate, spending limit (uint64), permitted categories ([]string). Integer + string + []string.
6. `RegistrationPayload` (`event.go:669`) — reputation score (uint64), metadata. Integer + string.
7. `GenesisFundingPayload` (`event.go:682`) — recipient, amount (uint64), metadata. Integer + string.
8. `TaskPostedPayload` (`event.go:695`) — task metadata; integer budget, string fields.
9. `TaskClaimedPayload` (`event.go:712`) — claimer, task ID, timestamp. Integer + string.
10. `TaskSubmittedPayload` (`event.go:720`) — submitter, task ID, evidence hash. Integer + string.
11. `TaskApprovedPayload` (`event.go:732`) — approver, task ID. Integer + string.
12. `TaskDisputedPayload` (`event.go:740`) — disputer, task ID, reason. Integer + string.
13. **`TaskVerificationVotePayload`** (`event.go:749`): `ScoreBP uint64`, `ScoreBreakdown map[string]uint64`, plus integer + string + []string fields. **No floats.**
14. **`TaskVerificationConsensusPayload`** (`event.go:767`): `FinalScoreBP uint64`, weights (uint64), counts (int), timestamp (int64), bool, []string. **No floats.**
15. `SlashingChallengePayload` (`event.go:789`) — validator, offense type, stake (uint64). Integer + string.
16. `PrerequisiteWithholdingPayload` (`event.go:808`) — missing prerequisites, evidence. Integer + string.
17. `TrajectoryCommitPayload` (`trajectory.go:41`): `QualityScoreBP uint32` with validation `[0, 10000]`. **No floats.**

**No canonical payload type contains a `float32`, `float64`, `interface{}`, `any`, or untyped map value.**

### 10.2 Analyzer / verification struct types are NOT payloads

`VerificationResult` (in `internal/verification/service.go`) contains `Confidence float64`, `DeterministicReport.NumericScores map[string]float64`, `SubjectiveReport.Relevance/Completeness/Quality/Overall float64`. These fields are confirmed by grep to exist in `internal/verification/` and `internal/harness/`, but are **not serialized into any of the 17 canonical event payload types**. `VerificationResult` is a local return type from `VerificationService.Verify` consumed by `autovalidator` and converted to integer fields (`ScoreBP`) before being serialized into a `TaskVerificationVotePayload`.

### 10.3 Conclusion of boundary audit

The canonical event payload surface is float-free today. §2.2's dismissal of analyzer-family floats from this workstream's scope is factually correct. The §4.4 AST lint + runtime assertion prevent regression. Future analyzer-family determinism concerns (whether the float-internal analyzer produces the same integer `ScoreBP` on different hardware) are a distinct workstream and do not affect this plan's scope.

---

## Approval gate

This document is **v2**. It has been synthesized from multi-AI review and closed open questions via code inspection.

**Founder decision required**: approve v2 as the locked plan to send to Claude Code for implementation, or kick back with specific concerns.

If approved, Claude Code receives:
- This document as the plan.
- A plan-mode-first implementation prompt that requires founder sign-off on the Claude Code plan before code is written.
- The testnet cutover rehearsal (commit-10) as the final verification before merge.

If kicked back: Claude revises into v3 and returns for review. Subsequent revisions do not re-run ChatGPT/Grok unless the scope changes materially.

---

**End of draft v2.**
