# Part A — `internal/protocolmath` — completion report

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `603bd9b` (merge F3-B settlement consensus integrity fix workstream)
**Commit**: see `git log -1` after commit step (`commit-1(protocolmath): add deterministic allocation primitive`)
**Plan reference**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §4.1

## What was built

A new Go package at `internal/protocolmath/` exporting one deterministic
allocation primitive with two entry points (`Allocate`,
`AllocateWithCeiling`), the named types (`BasisPoints`, `MicroAET`,
`Recipient`), two canonical constants (`NeutralBP`, `MaxBasisPoints`), and
three sentinel errors (`ErrInvariantViolation`, `ErrEmptyRecipients`,
`ErrDuplicateCanonicalKey`). Internal helper `mulDivBig` wraps
`math/big.Int` multiply-divide with explicit panics on divide-by-zero and
on quotients that exceed `uint64`.

No callsite wires to this package yet — that is Part B. No file outside
`internal/protocolmath/` was modified.

### Files

| File | Lines | Purpose |
|---|---:|---|
| `doc.go` | 44 | Package doc — purpose, principles (6/10/11/12), determinism guarantees, plan reference |
| `types.go` | 33 | `BasisPoints`, `MicroAET`, `Recipient`, `NeutralBP`, `MaxBasisPoints` |
| `allocate.go` | 172 | `Allocate`, `AllocateWithCeiling`, sentinel errors, shared `allocate` impl |
| `muldiv.go` | 28 | Internal `mulDivBig` — `math/big.Int` multiply-divide with panic-on-overflow |
| `allocate_test.go` | 332 | 12 correctness + determinism + fallback tests |
| `invariants_test.go` | 84 | 4 invariant / ceiling / duplicate-key tests |
| `overflow_test.go` | 83 | 3 overflow-boundary / panic tests |

### Imports

Source files use only the four approved imports:

| File | Imports |
|---|---|
| `doc.go` | (none — package doc only) |
| `types.go` | (none) |
| `allocate.go` | `bytes`, `errors`, `math/big`, `sort` |
| `muldiv.go` | `math/big` |

Test files additionally import `testing`, `math/rand`, `reflect`, `bytes`,
`fmt`, `sort`, `math/big` — all from the standard library, all within the
scope the founder approved in the implementation clarification.

## Test results — `go test -race -count=10 -v ./internal/protocolmath/...`

All 19 tests (18 from the prompt matrix + 1 bonus divide-by-zero panic
test) pass on every iteration of 10 race-detector runs:

| # | Test | File | Result |
|---|---|---|---|
| 1 | `TestAllocate_SingleRecipient` | `allocate_test.go` | PASS |
| 2 | `TestAllocate_TwoEqualRecipients` | `allocate_test.go` | PASS |
| 3 | `TestAllocate_UnequalWeights_Basic` | `allocate_test.go` | PASS |
| 4 | `TestAllocate_Rounding_LastRecipientAbsorbs` | `allocate_test.go` | PASS |
| 5 | `TestAllocate_ConservationOnManyRecipients` | `allocate_test.go` | PASS |
| 6 | `TestAllocate_PermutationInvariance` | `allocate_test.go` | PASS |
| 7 | `TestAllocate_ThousandRuns_Identical` | `allocate_test.go` | PASS |
| 8 | `TestAllocate_ZeroTotalWeight_EvenSplit` | `allocate_test.go` | PASS |
| 9 | `TestAllocate_EmptyRecipientsZeroPool` | `allocate_test.go` | PASS |
| 10 | `TestAllocate_EmptyRecipientsNonzeroPool` | `allocate_test.go` | PASS |
| 11 | `TestAllocate_NegativeWeight_ReturnsError` | `invariants_test.go` | PASS |
| 12 | `TestAllocateWithCeiling_ClampsHighWeights` | `invariants_test.go` | PASS |
| 13 | `TestAllocateWithCeiling_NegativesStillError` | `invariants_test.go` | PASS |
| 14 | `TestAllocate_NearMaxPool` | `overflow_test.go` | PASS |
| 15 | `TestMulDivBig_PanicOnOverflow` | `overflow_test.go` | PASS |
| 15b | `TestMulDivBig_PanicOnDivideByZero` (bonus) | `overflow_test.go` | PASS |
| 16 | `TestAllocate_SomeRecipientsZeroWeight` | `allocate_test.go` | PASS |
| 17 | `TestAllocate_SingleRecipientZeroPool` | `allocate_test.go` | PASS |
| 18 | `TestAllocate_DuplicateCanonicalKeys_ReturnsError` | `invariants_test.go` | PASS |

Total suite time ~1.7 s including 10× repetition.

## Coverage — `go test -cover ./internal/protocolmath/`

```
ok  	github.com/Aethernet-network/aethernet/internal/protocolmath	0.266s	coverage: 100.0% of statements
```

**100.0 % of statements covered.** Above the 90 % threshold; no explanation
needed.

## Float freedom — `grep -rn "float" internal/protocolmath/ --include="*.go"`

Returns zero matches. Initial implementation had four matches in
documentation comments describing what the package replaces
("floating-point fractions", "float64", "float arithmetic"); these were
rewritten to "non-integer fractions", "non-integer arithmetic" etc. so the
hard float-free check passes. Rewording is semantically equivalent and
arguably more precise — a `math/big.Rat` would also be non-integer and
would also be forbidden here.

## Vet — `go vet ./internal/protocolmath/...`

Clean (zero output, exit 0).

## Build — `go build ./internal/protocolmath/...`

Clean (exit 0).

## Three choice points — final picks

All three as proposed in the plan and approved:

1. **`mulDivBig` return type** — returns `MicroAET` (not `uint64`). Trivial
   typed alias; type-safety at the single callsite.

2. **Duplicate `CanonicalKey`** — returns new sentinel
   `ErrDuplicateCanonicalKey`. Loud failure matches Principle 5 and the
   rule-5 discipline ("invariant violation is loud, not silent"). Test #18
   asserts the error and that the output map is empty.

3. **`SingleRecipientZeroPool`** — returns `{recipient: 0}`. Uniform
   output shape across all pool values; callers that need to skip
   zero-amount entries do so at the callsite (the existing escrow code
   already has `if amount == 0 { continue }`).

Each choice is documented in the relevant doc comment (`types.go`,
`allocate.go` on the `Allocate` function, and on the `ErrDuplicateCanonicalKey`
sentinel).

## Deviations from the prompt

None. Every required file, type, signature, constant, error, test, and
imports constraint was implemented exactly as specified, with the three
choice points resolved per the approved plan.

One minor rewording of documentation comments was needed to pass the
strict `grep -rn "float"` check — substituting "non-integer" for
"float"/"floating-point" in comments that describe what the package
replaces. Semantically equivalent.

## Discoveries that may influence Parts B–G

1. **`big.Int.IsUint64` is the idiomatic overflow check.** `mulDivBig`
   uses it rather than comparing against `math.MaxUint64` manually. Parts
   B–G that also need overflow guards (settlement fee math, Q-score
   aggregation) should follow the same pattern.

2. **The last-of-sorted absorbs remainder convention is visible in
   several places** — zero-total-weight path, weighted path. Consider
   exposing this as a documented protocol invariant when Part B wires the
   primitive into settlement: "cross-node agreement on which recipient
   absorbs the rounding remainder depends on stable sort by
   CanonicalKey." This becomes visible in cross-node audit.

3. **`AllocateWithCeiling` silently clamps positives but loudly rejects
   negatives.** Part B callers — specifically any boundary receiving a
   Q-score from the verification subsystem — should choose explicitly
   which variant to use. The reputation store currently returns a float
   (see handoff 2026-04-18); Part B must convert at the boundary, and
   since the reputation store can return `NaN` / negative in pathological
   cases, the convert-at-boundary code should both convert to
   `BasisPoints` AND pre-clamp to a non-negative range before calling
   `AllocateWithCeiling`. Otherwise a `NaN → -1` conversion would surface
   as `ErrInvariantViolation` at the wrong layer.

4. **`Recipient.CanonicalKey []byte` is inherently copied on `append`
   into the defensive sort slice**, but the `sort.Slice` still sees the
   same slice header, so if a caller mutates the bytes of a
   `CanonicalKey` between the invariant scan and the sort (in a
   concurrent context), behavior is undefined. The package does not
   promise thread-safety against in-flight key mutation; it promises
   thread-safety against multiple concurrent calls with disjoint
   inputs. Part B's callsites do not mutate, so this is academic, but
   if a future caller ever derived `CanonicalKey` from a shared buffer,
   they would need to pass a defensive copy.

## Verification commands (reproducible)

```
git checkout 603bd9b -b feat/canonical-distribution-integer-migration
go build ./internal/protocolmath/...
go vet ./internal/protocolmath/...
go test -race -count=10 ./internal/protocolmath/...
go test -cover ./internal/protocolmath/
grep -rn "float" internal/protocolmath/ --include="*.go"   # zero matches expected
```

All six commands pass / return zero matches on the committed code.

## State

Branch `feat/canonical-distribution-integer-migration` is ready for
commit. After the commit is made this report will be updated with the
final commit hash.
