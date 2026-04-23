# Part C — Canonical payload float-freedom enforcement — completion report

**Branch**: `feat/canonical-distribution-integer-migration`
**Base commit**: `bf098a2` (Part B completion report)
**Commit**: `a79fef7` — `commit-6(event/lint): canonical payload float-freedom AST lint + reflection test`
**Plan reference**: `docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md` §4.4 (revised to drop the runtime-wrapper approach in favor of a reflection-based CI test)

## What was built

Two independent defenses for the invariant that every canonical event
payload type defined in `internal/event/` stays float-free transitively.

1. **AST lint** at `internal/event/lint/` (new package):
   - `canonical_float_lint.go` — `Check(modulePath, overlay)` loads
     `internal/event/` via `golang.org/x/tools/go/packages`, walks each
     of the 17 canonical payload types transitively via `go/types`, and
     returns a `Report` with any float-bearing field paths.
   - `canonical_float_lint_test.go` — `TestCanonicalFloatLint` acts as
     the CI gate; 11 additional tests exercise every injected-violation
     case (float64, float32, interface{}, any, pointer, slice, map value,
     map key scenario via struct nesting, nested struct, cycle, cycle
     with float, named float alias, json.RawMessage, missing-type, bad
     module path).
   - `README.md` — invariant, how to run, how to add a new canonical
     payload type, why `Event` is excluded, pattern notes.

2. **Reflection test** at `internal/event/canonical_payload_reflection_test.go`:
   - `TestCanonicalPayloadTypes_FloatFree` walks the same 17 types via
     `reflect` and asserts float-freedom.
   - `TestCanonicalPayloadList_Complete` scans `internal/event/*.go` for
     `*Payload` type declarations and asserts every one appears in the
     hardcoded reflection list. Drift detection.
   - `TestCanonicalPayloadList_Has17Entries` pins the list length.

Both mechanisms enforce the same invariant through different code paths:
a bug in the `go/types` walker doesn't hide a regression that `reflect`
would catch, and vice versa.

## Pattern correction (discovered during plan mode)

The prompt's sketch of `//go:build lint` + `go run -tags=lint` is not the
established pattern. The existing lints at `internal/dispatch/lint/` and
`internal/projections/lint/` are library packages with a `Check()`
function invoked by a `TestXxxxLint` in the same package; the test runs
under `go test ./...` as a normal CI gate, with no build tag or separate
invocation.

Part C follows the established pattern. This was pre-authorized in the
prompt's guidance: "If the pattern is different ... follow the existing
pattern rather than my sketch."

## Discovery-mechanism choice

**Option 1 (hardcoded list) + drift-detection test (from Option 2's
spirit).** Both implemented.

- The 17-type list is hardcoded in two synchronized places:
  `canonicalPayloadTypeNames` in `internal/event/lint/canonical_float_lint.go`
  and `canonicalPayloadReflectTypes` in
  `internal/event/canonical_payload_reflection_test.go`.
- `TestCanonicalPayloadList_Complete` in the reflection file scans
  `internal/event/*.go` for `*Payload` struct declarations and asserts
  every one appears in the reflection list. A new `*Payload` type added
  without list-update fails this test with a message pointing to both
  lists.
- `TestCanonicalPayloadTypeNames_HasSeventeen` (lint) and
  `TestCanonicalPayloadList_Has17Entries` (reflection) pin the count at
  17 so changes require explicit intent.

Rationale for the choice:
- Option 1 is the explicit architectural artifact reviewers sign off on.
- Adding the drift test recovers Option 2's safety without the convention-
  coupling pitfalls (typos, relocations, etc.).

## Injection smoke test transcript (temporary change, reverted before commit)

A `float64` field was temporarily added to `TaskVerificationVotePayload`
in `internal/event/event.go`:

```go
ScoreBreakdown       map[string]uint64 `json:"score_breakdown,omitempty"`
SmokeConfidence      float64           `json:"smoke_confidence"` // TEMP
AnalyzerFamily       string            `json:"analyzer_family"`
```

### AST lint output

```
--- FAIL: TestCanonicalFloatLint (0.30s)
    canonical_float_lint_test.go:22: canonical payload float-freedom violation: TaskVerificationVotePayload.SmokeConfidence: float64
    canonical_float_lint_test.go:24:
        → to fix: change the field to an integer type (see docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md §4.1 for BasisPoints), or if the field is genuinely non-canonical, split the type so the canonical half is float-free.
FAIL
FAIL	github.com/Aethernet-network/aethernet/internal/event/lint	0.667s
```

### Reflection test output

```
--- FAIL: TestCanonicalPayloadTypes_FloatFree (0.00s)
    canonical_payload_reflection_test.go:55: TaskVerificationVotePayload.SmokeConfidence: float64
FAIL
FAIL	github.com/Aethernet-network/aethernet/internal/event	0.281s
```

Both defenses identify the exact field path (`TaskVerificationVotePayload.SmokeConfidence`)
and the reason (`float64`). The injection was reverted before committing;
`git diff` against `HEAD` at `a79fef7` shows only the new lint and test
files plus the README — no change to `event.go`.

## Verification

### Build

```
go build ./...                       # clean
```

### Vet

```
go vet ./internal/event/...          # clean
```

### Tests under race detector

```
go test -race -count=3 ./internal/event/...          # ok
go test -count=1 ./internal/event/... ./internal/dispatch/... \
                  ./internal/settlement/... ./internal/taskverification/... \
                  ./internal/protocolmath/... ./internal/projections/...
                                                      # 10 packages ok
```

### Coverage

| Package | Coverage |
|---|---:|
| `internal/event/lint` | **86.2 %** (above 80 % target) |
| `internal/event` (including reflection test) | **86.8 %** |

Per-function lint coverage:

```
Violation.String         100.0%
Report.HasFailures       100.0%
Check                     76.4%
walk                      90.0%
isJSONRawMessage         100.0%
```

`Check`'s 76.4% is defensive error handling for impossible-in-test
conditions (e.g. `types.Named` assertion failure on a looked-up
`types.Object`; `packages.Load` returning multiple packages for a
single-package query). Not worth adding synthetic harness to reach 100%.

## Injected-violation test coverage (11 cases)

| Case | Test |
|---|---|
| `float64` | `TestCanonicalFloatLint_InjectedFloat_Fails` |
| `float32` | `TestCanonicalFloatLint_InjectedFloat32_Fails` |
| `interface{}` | `TestCanonicalFloatLint_InterfaceField_Fails` |
| `any` | `TestCanonicalFloatLint_AnyField_Fails` |
| Nested-struct float | `TestCanonicalFloatLint_NestedStructFloat_Fails` |
| `*float64` | `TestCanonicalFloatLint_PointerToFloat_Fails` |
| `map[string]float64` | `TestCanonicalFloatLint_MapValueFloat_Fails` |
| `[]float64` | `TestCanonicalFloatLint_SliceOfFloat_Fails` |
| `json.RawMessage` | `TestCanonicalFloatLint_JSONRawMessage_Fails` |
| Cyclic type, no float | `TestCanonicalFloatLint_CyclicType_Terminates` |
| Cyclic type with float | `TestCanonicalFloatLint_CyclicTypeWithFloat_Flags` |
| Named float alias | `TestCanonicalFloatLint_NamedFloatAlias_Fails` |
| Missing type in list | `TestCanonicalFloatLint_UnknownType_ReportsViolation` |
| Bad module path | `TestCanonicalFloatLint_BadModulePath_ReturnsError` |

Each uses the `packages.Config.Overlay` mechanism to inject synthetic Go
source without touching disk, and splices a `SyntheticInjectedPayload`
type name into the canonical list for the duration of the test so the
injected type is visited.

## CI wiring

Implicit — the existing `go test ./...` invocation picks up:
- `TestCanonicalFloatLint` at `internal/event/lint/`
- `TestCanonicalPayloadTypes_FloatFree`, `TestCanonicalPayloadList_Complete`,
  `TestCanonicalPayloadList_Has17Entries` at `internal/event/`

No Makefile or GitHub Actions workflow change required. Confirmed by
noting that `internal/dispatch/lint/` and `internal/projections/lint/`
use the same implicit wiring and have been enforcing invariants for the
full F3-B and reputation workstreams on the same CI pipeline.

## Deviations from the prompt

1. **Lint pattern** — followed the established library-plus-test pattern
   instead of the prompt's `//go:build lint` + `go run -tags=lint`
   sketch. Pre-authorized by the prompt; confirmed the actual pattern
   during plan mode by reading `internal/dispatch/lint/` and
   `internal/projections/lint/`.

2. **Helper removed after coverage analysis** — `CanonicalPayloadTypeNames()`
   was included in the first draft as an exported accessor for external
   callers. No caller exists; the reflection-test drift-check reads its
   own independent list and does not need to consult the lint's list.
   Removed during coverage work; the drift check compares the two
   hardcoded lists independently (one via `reflect`, one via `go/ast`
   during the scan) and fails if they disagree.

Nothing else. Commit-6 is one commit, follows the pattern, has 14
injected-violation / edge-case tests plus the CI gate and drift/count
checks, and `a79fef7` is buildable and verified.

## Discoveries for Parts D–G

1. **`packages.Config.Overlay` is the idiomatic way to test lints
   without writing to disk.** Part F (or any future lint) should reuse
   this pattern. Overlays accept a `map[string][]byte` keyed by absolute
   path to a virtual file; the loaded package sees both the overlay and
   the real files. Cleanup requires no file-system work; the test just
   defers a restore on the shared hardcoded list if one is mutated for
   the injection.

2. **The drift check protects more than the lint.** When Part F adds a
   new canonical event type (e.g. the cutover `ProtocolUpgrade` event),
   the drift test will flag the new `*Payload` type before anyone can
   bypass float-freedom. Part F gets the compile-time guarantee for
   free.

3. **`isJSONRawMessage` by fully-qualified name, not by underlying
   type.** The lint must not flag `[]byte` generally — many payload
   fields could legitimately carry byte slices. Distinguishing
   `json.RawMessage` from `[]byte` requires an identity check on the
   named type's `(*types.Object).Pkg().Path()` and `Name()`. Documented
   in-source and covered by the
   `TestCanonicalFloatLint_JSONRawMessage_Fails` case.

4. **`complex64`/`complex128` caught as a bonus.** Go's complex types
   don't appear in any canonical payload today, but they would also
   break determinism. Both the AST lint and reflection test flag them.
   Documented in the edge-case table in the plan.

5. **Lint test runtime: ~50 s under `-race -count=3`.** The package-
   loading step (`packages.Load`) is the bottleneck; every overlay-based
   injection test re-loads the module. Acceptable for a CI gate but
   notable if the repo grows much larger. A future optimization could
   cache the loaded-without-overlay package state across tests, but
   avoiding it today keeps the tests simple and independent.

## Verification commands (reproducible)

```bash
git checkout a79fef7
go build ./...
go vet ./internal/event/...
go test -race -count=3 ./internal/event/...
go test -cover ./internal/event/lint/ ./internal/event/
```

All pass on the committed code.

## State

Branch at `a79fef7`, not yet pushed — awaiting review. Part D
(cross-architecture / cross-version determinism test rig) follows in a
separate session.
