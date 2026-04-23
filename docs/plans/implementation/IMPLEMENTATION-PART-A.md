# Part A — `internal/protocolmath` primitive package

**Session**: Claude Code, starting from `main` @ commit `603bd9b`
**Branch**: `feat/canonical-distribution-integer-migration` (create if not exists, otherwise checkout)
**Scope**: Single commit (`commit-1`). New package `internal/protocolmath`. No callers use it yet. No other files modified.
**Deliverable**: A well-tested deterministic allocation primitive that subsequent Parts will wire into the settlement path.
**Plan source**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §4.1.

---

## IMPORTANT: plan mode first

Do NOT write any code until you have produced a plan and received founder approval. Follow the discipline in `CLAUDE.md`:

1. Read this prompt in full.
2. Read `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §4.1 (the package design section) in full.
3. Read `docs/design-principles.md` — Principles 6, 10, 11, 12 are most relevant.
4. Inspect `internal/escrow/escrow.go` and `internal/projections/registry.go` for the codebase's idiomatic patterns on error types, documentation, and struct design.
5. Produce an implementation plan that covers:
   - Exact file layout of the new package
   - Exact type signatures of all exported identifiers
   - Unit test matrix (what tests, what they cover)
   - Any deviations from v2 §4.1 or clarifications you propose
6. Wait for founder approval.

Only after approval: implement.

---

## What this Part produces

A new Go package at `internal/protocolmath/` containing:

### Exported types

```go
package protocolmath

// BasisPoints is the integer representation of a ratio. 10000 == 1.0.
// Valid range: [0, MaxBasisPoints]. Negative values are invariant violations.
type BasisPoints int64

// MicroAET is the protocol's canonical unit of value. 1 AET = 10^6 µAET.
type MicroAET uint64

// Recipient represents one payee in a proportional allocation.
// CanonicalKey is used for deterministic ordering; callers commonly use AgentID bytes.
type Recipient struct {
    CanonicalKey []byte
    Weight       BasisPoints
}
```

### Exported constants

```go
// NeutralBP is the "Q = 1.0" convention in basis points.
const NeutralBP BasisPoints = 10000

// MaxBasisPoints is the protocol-enforced ceiling for any Q-like score.
const MaxBasisPoints BasisPoints = 100000
```

### Exported functions

```go
// Allocate distributes pool among recipients weighted by Weight.
// Returns a map keyed by stringified CanonicalKey.
//
// Invariants:
//   - Recipients with Weight == 0 receive 0.
//   - Sum of returned amounts equals pool exactly.
//   - Ordering: recipients are sorted by CanonicalKey (bytes.Compare ascending) before allocation.
//     Last recipient of sorted order absorbs remainder.
//   - Negative Weight returns ErrInvariantViolation (never silently absorbed).
//   - Total Weight == 0 returns even-split (pool / N, last absorbs remainder).
//   - All internal arithmetic uses math/big.Int; no float64; no int64 overflow.
//   - len(recipients) == 0 with pool > 0 returns ErrEmptyRecipients.
func Allocate(recipients []Recipient, pool MicroAET) (map[string]MicroAET, error)

// AllocateWithCeiling is Allocate but clamps Weight to [0, MaxBasisPoints] before
// computation. Weights above ceiling are clamped to ceiling; negatives still return
// ErrInvariantViolation (no silent conversion to zero).
func AllocateWithCeiling(recipients []Recipient, pool MicroAET) (map[string]MicroAET, error)
```

### Exported errors

```go
var (
    ErrInvariantViolation = errors.New("protocolmath: invariant violation: negative weight")
    ErrEmptyRecipients    = errors.New("protocolmath: empty recipient set with nonzero pool")
)
```

### Internal helper (unexported)

```go
// mulDivBig computes (a * b) / c using math/big.Int, returning MicroAET.
// Panics if the result does not fit in uint64.
// In protocolmath's usage, the result is bounded by pool (MicroAET), which fits in uint64
// by construction, so the panic is a true impossibility-check rather than a runtime concern.
func mulDivBig(a, b, c *big.Int) MicroAET
```

---

## Design rules

1. **No `float32` or `float64` anywhere in the package.** Not in implementation, not in tests, not in comments as "approximately equals X." This is a canonical-math package; floats are forbidden per Principle 11.

2. **`math/big.Int` unconditionally for the multiply-divide step.** Do NOT use `int64` or `uint64` arithmetic for the `pool * weight / totalWeight` computation. Allocate `big.Int` values per call. The performance cost is acceptable at settlement speed.

3. **Deterministic sort by `CanonicalKey` before distribution.** Use `sort.Slice` with `bytes.Compare(a.CanonicalKey, b.CanonicalKey) < 0`. Do not rely on caller ordering.

4. **Last-of-sorted-order absorbs remainder.** After all preceding recipients receive their floor shares, the last recipient receives `pool - sum(preceding)`. This is exact conservation.

5. **Invariant violation is loud, not silent.** If any `Weight < 0`, return `ErrInvariantViolation` without computing any output. Do not convert to zero. Do not fall back to even-split. The caller upstream has a bug and must be told.

6. **Zero total weight is a fallback, not an error.** If all weights are zero (or no weights exist but recipients do), return even-split: `pool / len(recipients)`, last-of-sorted absorbs remainder.

7. **Empty recipient set with zero pool is a no-op.** Return empty map with no error. Only nonzero pool with empty recipients is `ErrEmptyRecipients`.

8. **Named types throughout public API.** Use `BasisPoints`, `MicroAET`, `Recipient` — not raw `int64`, `uint64`, struct literals. Makes domain mistakes harder at the type level.

9. **All docs comments follow Go conventions** — start with the identifier name, describe behavior not implementation, note invariants.

10. **No logging in this package.** `protocolmath` is a pure-function utility. Callers do the logging (e.g., the settler will log on `ErrInvariantViolation` in Part B).

---

## Required unit tests

Create `internal/protocolmath/allocate_test.go` (and other test files as needed for organization).

Minimum test matrix (the `TestAllocate_` prefix is a naming suggestion; name tests idiomatically):

### Correctness

1. **TestAllocate_SingleRecipient**: one recipient with weight 10000, pool 1000000 → recipient gets 1000000.
2. **TestAllocate_TwoEqualRecipients**: two recipients, both weight 10000, pool 1000000 → each gets 500000. Conservation exact.
3. **TestAllocate_UnequalWeights_Basic**: recipients with weights 10000, 20000, 30000 on pool 600000 → amounts 100000, 200000, 300000.
4. **TestAllocate_Rounding_LastRecipientAbsorbs**: pool 100, three recipients with equal weight → outputs sum to exactly 100 (some recipient gets one more µAET than the others; specifically the last of sorted-order).
5. **TestAllocate_ConservationOnManyRecipients**: 10 recipients, various weights, pool 1000003 (a prime-ish number) → sum of outputs exactly equals pool.

### Determinism

6. **TestAllocate_PermutationInvariance**: same recipient set in three different input orders → same output map in all three cases. This is the core cross-node determinism property.
7. **TestAllocate_ThousandRuns_Identical**: run allocate 1000 times on the same input, assert all 1000 outputs are byte-identical. Guards against RNG / iteration-order contamination.

### Fallbacks

8. **TestAllocate_ZeroTotalWeight_EvenSplit**: all recipients have weight 0, pool 1000 → even split with last-absorbs.
9. **TestAllocate_EmptyRecipientsZeroPool**: empty slice, pool 0 → empty map, no error.
10. **TestAllocate_EmptyRecipientsNonzeroPool**: empty slice, pool 100 → ErrEmptyRecipients.

### Invariants

11. **TestAllocate_NegativeWeight_ReturnsError**: recipient with weight -1 → ErrInvariantViolation. Output map should NOT be populated (don't return partial results).
12. **TestAllocateWithCeiling_ClampsHighWeights**: weight 999999 clamped to MaxBasisPoints; result equal to allocation with MaxBasisPoints.
13. **TestAllocateWithCeiling_NegativesStillError**: clamping does NOT convert negatives to zero; invariant violation still fires.

### Overflow boundaries

14. **TestAllocate_NearMaxPool**: pool at 10^18 µAET (near uint64 max / typical weight), weights at MaxBasisPoints → no panic, conservation holds, output correct. This is the test that validates `big.Int` is doing its job.
15. **TestAllocate_MulDivBig_PanicOnOverflow**: internal test (using a local wrapper if mulDivBig isn't exported): pass values whose quotient exceeds uint64 max; assert panic.

### Zero-weight mixed

16. **TestAllocate_SomeRecipientsZeroWeight**: 4 recipients, weights [0, 10000, 0, 20000], pool 900 → zero-weight recipients get 0, others split 900 proportionally (300, 600 or similar with last-absorbs).

### Edge cases

17. **TestAllocate_SingleRecipientZeroPool**: one recipient, pool 0 → output is `{recipient: 0}` or empty map; document and test the chosen behavior.
18. **TestAllocate_DuplicateCanonicalKeys**: two recipients with the same CanonicalKey → document behavior (either both appear summed in the output, or the second overwrites, or returns error — pick one, test it, document it in the function doc comment).

---

## Additional requirements

### `doc.go`

Create `internal/protocolmath/doc.go` with a package-level doc comment explaining:
- The package's purpose (canonical deterministic allocation for settlement paths).
- Why it exists (Principle 6 — shared primitive instead of bespoke fixes; Principle 11 — integer-only canonical math).
- How callers should use it (pass recipients, get allocation; don't do their own proportional math).
- The determinism guarantees.
- A reference to the plan document.

### No external dependencies

The package should only import:
- `bytes`
- `errors`
- `math/big`
- `sort`

No external modules. No `log/slog`. No `time`. Pure arithmetic.

### Test file organization

Suggested layout (you may organize differently if you have a clear rationale):

- `internal/protocolmath/allocate.go` — main Allocate, AllocateWithCeiling
- `internal/protocolmath/types.go` — BasisPoints, MicroAET, Recipient definitions
- `internal/protocolmath/muldiv.go` — internal big.Int helper
- `internal/protocolmath/doc.go` — package doc
- `internal/protocolmath/allocate_test.go` — correctness, determinism, fallback tests
- `internal/protocolmath/invariants_test.go` — invariant violation tests
- `internal/protocolmath/overflow_test.go` — overflow boundary tests

---

## Completion criteria

You are done with Part A when ALL of the following hold:

1. **Code compiles**: `go build ./internal/protocolmath/...` returns exit 0.
2. **Tests pass**: `go test -race -count=10 ./internal/protocolmath/...` returns exit 0 with all listed tests above (plus any you added) passing. The `-count=10` is important — it catches nondeterminism that single runs miss.
3. **Vet passes**: `go vet ./internal/protocolmath/...` returns exit 0.
4. **No float anywhere**: `grep -rn "float" internal/protocolmath/ --include="*.go"` returns no results. This is a hard check; the package must be float-free including in tests and comments.
5. **No floats in imports**: inspect each file's import block; only the four imports listed above should appear.
6. **Coverage**: `go test -cover ./internal/protocolmath/` reports >90% statement coverage.
7. **Commit produced**: a single commit on `feat/canonical-distribution-integer-migration` with message following the F3-B convention (e.g., `commit-1(protocolmath): add deterministic allocation primitive`).
8. **Completion report written**: a report at `docs/plans/implementation/part-a-completion-report.md` documenting:
   - What was built
   - Test results (all test names, all passing)
   - Coverage percentage
   - Any deviations from this prompt (there should be none unless the founder approved them)
   - Any discoveries that should influence Parts B–G

---

## What NOT to do in Part A

- Do not modify any files outside `internal/protocolmath/`.
- Do not wire `protocolmath` into the settler or generation ledger. That's Part B.
- Do not add the canonical-payload lint. That's Part C.
- Do not add cross-architecture tests. That's Part D.
- Do not run `go test ./...` on the whole repo as part of verification — that's unnecessary noise. Only `./internal/protocolmath/...` matters for this Part.
- Do not add benchmarks unless something in implementation genuinely warrants one; correctness tests are sufficient for Part A.

---

## If you find a plan defect

If during implementation you discover that v2 of the plan is wrong about something — a type signature doesn't work, an invariant is inconsistent, the test matrix has a gap that matters — STOP and report it. Do not silently adapt the plan. The discipline is: plan is locked; if it's wrong, the plan changes, not the implementation.

Report the defect in the completion report, and pause the session so the founder can decide whether to revise the plan (returning to architect session) or approve the deviation as a minor correction.

---

**End of Part A prompt. Start with plan mode. Do not write code until the plan is approved.**
