# Step 3 Implementation Plan — Projection Registry Lint Check

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close plan §9.7 defense item 1 (CI static check) and the second half of PR-3 (test-reference existence) by shipping a compile-time lint that fails `go test ./...` when (a) a consensus-adjacent store type is not registered in the projection registry, or (b) a registered `IntegrationTestRef` does not resolve to a real test symbol in the codebase. The lint is the structural complement to Step 2's per-test meta-assertions: Step 2 catches rename drift from the test side; Step 3 catches missing tests from the registry side, plus the whole "writer exists, caller doesn't" class Grok round 2 attack 8 named.

**Architecture:** A `internal/projections/lint/` package containing a `go/analysis`-shaped checker plus a `TestLintRepository` harness that loads the full module via `golang.org/x/tools/go/packages`, extracts the registered set by static-parsing every package-local `*Projection()` constructor function, matches suspect store types against the heuristic, and verifies every Canonical `IntegrationTestRef` resolves to a real `Test*` function. The test function fails if any suspect is unregistered or any ref is broken; `go test ./...` runs it by default.

**Tech Stack:** Go stdlib (`go/ast`, `go/types`, `go/token`, `go/build`) plus one new module dep: `golang.org/x/tools/go/packages` for module-scope loading. No third-party linter frameworks; lint logic is ours.

---

## Required reading (re-verified before coding)

1. `CLAUDE.md` §1 (plan mode) and §4 (verification).
2. `docs/plans/2026-04-12-reputation-and-consensus-integrity.md` §9.3 (structural definition, principle vs heuristic) and §9.7 (the defense stack).
3. `docs/plans/2026-04-12-reputation-step-1-projection-registry.md` (registry primitive, PR-1..PR-5).
4. `docs/plans/2026-04-12-reputation-step-2-retrofit-projections.md` (retrofitted entries, per-package `Projection()` constructor pattern).
5. `docs/lessons.md` — two new entries just landed on `main@64c31e0` (poster fee, claim router lag). Not directly relevant to lint design, but part of the pre-read per the step prompt.

---

## Scope alignment with plan §9.3 and §9.7

**Implemented in this step**:
- §9.7 defense item 1 — CI static check for writer-without-caller pattern (via the heuristic below).
- §9.4 PR-3 complement — test-reference existence check. Step 2 validates the field non-empty; Step 3 validates the symbol resolves.
- Suppression annotation for legitimate false positives (a comment pragma requiring a non-empty reason).
- Build-tag enumeration: default tags only in Step 3; non-default tags flagged as "not scanned, review required."
- `go test ./...` integration via a top-level `TestLintRepository` so the check cannot be skipped by omitting a vet flag.

**Not implemented in this step (explicit deferrals)**:
- SSA-level taint analysis for interface-indirect persistence (would catch evasion via interface fields). Principle-level review covers this.
- Automatic fix generation. The lint diagnostics tell the contributor exactly what to do; fixing is manual.
- Integration with `golangci-lint` distribution. The test-harness entry point suffices for CI.
- Extending the check to Advisory projections' test refs. Advisory entries may have empty `IntegrationTestRef` per plan §9.4 V11; Step 3 only enforces the check on Canonical entries.
- The poster-fee accounting audit (separate workstream; flagged in lessons.md).
- The claim-router-lag fix (separate workstream).

---

## Design decisions (D1–D7)

### D1 — Static-analysis framework: `go/packages` + custom AST walk

**Chosen**: add dep `golang.org/x/tools/go/packages` (stdlib-adjacent, maintained by the Go team), use it to load the entire module with full type info, walk the AST and type info directly. No `go/analysis.Analyzer` framework wrapper because we're not distributing as a `golangci-lint` plugin in this step — the code runs inside a regular Go test.

**Alternatives considered**: `go vet`-registered custom analyzer (requires `go test -vet=...` or a build step — skippable); `staticcheck` SA-style (more ceremony, not needed); plain `go/parser` without type info (can't resolve cross-package types, fragile for interface-indirect persistence).

**Justification**: `go/packages` gives full type info in one call; custom walk gives us exactly the control we need for the heuristic without adopting a framework's diagnostic-severity conventions. One new module dep; the x/tools module is the canonical place for this kind of analysis and has stable API guarantees.

**D1 refinement (founder)**: the dep lands as a first-class direct require in `go.mod`, not as a `_test.go`-only or `tools.go` indirect dep. The lint runs in the standard build pipeline (`go test ./...`), so it is part of the build.

### D2 — Registered-set extraction: parse `*Projection()` constructors

The per-package `ProjectionEntry` constructor pattern from Step 2 (`epoch.RoundCounterProjection`, `escrow.Projection`, `taskverification.CalibrationProjection`, etc.) is the single source of truth for registered projections. Production wiring (`cmd/node/main.go`) and Step-2 integration tests both call these functions.

**Extraction algorithm**:
1. Load every package in the module via `go/packages`.
2. For each package, find every exported function whose return type is `projections.CanonicalProjection` and whose name ends in `Projection` or is `Projection`.
3. For each found function, AST-walk its body to find the composite-literal `projections.CanonicalProjection{...}` and read the string-literal values of `Name`, `StoreType`, `Classification`, and `IntegrationTestRef`.
4. Build a set keyed on `(Package, StoreType)` for the "is this type registered?" check.
5. Build a list of `(Name, IntegrationTestRef, Classification)` for the test-ref existence check.

**Edge cases**:
- A `Projection` function that returns a non-literal (e.g., computed fields) — the extractor can't fold; emit a warning that the entry was found but couldn't be inspected. Counts as registered for Step 3; principle-level review catches if it's fake.
- Multiple `*Projection` functions in one package (e.g., `taskverification` has `CalibrationProjection` and `RoundStoreProjection`) — iterate, extract each independently.

**D2 limitation (founder refinement — surfaced in diagnostic)**: registration via dynamic struct construction (loops, conditionals, helper functions instead of a `*Projection()` constructor that returns a struct literal) defeats the AST-based extractor. Future contributors who deviate from the constructor convention require explicit code-review justification. Every lint diagnostic includes the sentence:

> "This lint relies on registration via a `*Projection()` constructor with a literal `CanonicalProjection{...}` struct. Dynamic registration patterns defeat the static analysis and require code-review confirmation of registration completeness."

### D3 — Heuristic matching rules for suspect store types

Per plan §9.3 CI heuristic, tightened per the step-3 prompt to flag non-exported writers as suspect.

**A struct type T in any package under `internal/` is SUSPECT if all of the following hold**:
1. T has a persistence dependency on BadgerDB, detected via ANY of:
   - **Direct field**: at least one field of concrete type `*badger.DB`.
   - **Embedding or composition** (founder refinement): at least one field (named or anonymous, embedded or composed) whose type is a named struct that itself recursively contains `*badger.DB`. Recursion capped at depth 3 to bound analysis cost and prevent cycles. This catches the wrapper-type evasion pattern where a contributor wraps a BadgerDB-holding type in another struct to sidestep the field-name heuristic.
   - Interface-typed persistence fields are **NOT** scanned for BadgerDB-backed implementations — deferred to Step 3.5 per the D3 cost gate (see "D3 cost-gate decision" below). Logged as `interface-indirect persistence; review required` warning.
2. T has at least one method (exported OR non-exported) that:
   - Takes a pointer receiver on T, AND
   - Calls `.Update(`, `.Set(`, `.Delete(`, or writes to a `*badger.DB` field. Detected by type-checking the called method's receiver.
3. T is not a test-only type (package path does not contain `testdata/`, and T is not declared in a `_test.go` file).

**D3 cost-gate decision (founder refinement — resolved)**: ship **embedding + direct-field detection** in Step 3. Interface-resolution (resolving which concrete types satisfy a persistence interface field in production wiring) is deferred to Step 3.5 because full implementation requires SSA-level or assignment-graph analysis across the entire module — materially more than the "~50 LOC / 1 day" threshold the founder set for shipping in Step 3. A `TODO(step-3.5)` comment in the matcher code flags the deferral at the code site. The Step-3 warning path already surfaces every interface-typed persistence field observed, so nothing is silently missed — interface-indirect types get a warning, and principle-level review confirms their registration status.

Being SUSPECT triggers the registration check: T must have `(T's package path, T's name)` present in the registered-set from D2. If not present, FAIL with diagnostic.

**Explicitly out-of-scope for automatic detection**:
- In-memory-only stores with `map[...]...` fields but no `*badger.DB`. These can still be consensus-adjacent (the original `ReputationManager` is one). The heuristic does NOT fire on them. Principle-level review owns this case. If the in-memory store later adds a `*badger.DB` for persistence, the heuristic fires and forces registration.

**False-positive suppression**: a type-declaration-level pragma:
```go
// projections:lint ignore "<reason — ≥20 characters and ≥3 words>"
type SomeType struct { ... }
```

**D4 stricter reason format (founder refinement)**: the reason string must contain **at least 20 characters** and **at least 3 whitespace-separated words**. A reason like `"false positive"` (14 chars, 2 words) is rejected; a reason like `"this is a test fixture for the lint itself; the matched type is intentionally synthetic"` passes both gates. The lint parser rejects insufficient reasons with:

```
projections/lint: INSUFFICIENT SUPPRESSION JUSTIFICATION
  Pragma:   // projections:lint ignore "<reason>"
  Location: <file>:<line>
  Required: at least 20 characters AND at least 3 words
  Got:      "<reason>" (<N> chars, <M> words)
  Required: replace with an actionable justification a future reviewer can evaluate
```

The presence of the pragma remains a code-review signal — the tightened reason format makes that signal actionable rather than just visible.

### D4 — IntegrationTestRef existence check

For every Canonical entry in the registered-set whose `IntegrationTestRef` is non-empty:
1. Parse the ref as `<import-path>.<symbol>`.
2. Load the package at `<import-path>` via `go/packages`.
3. Verify a `func <symbol>(t *testing.T)` exists in the package's test files.
4. If not, FAIL with diagnostic naming the entry and the missing symbol.

Advisory entries with empty `IntegrationTestRef` pass trivially (V11 permits).

### D5 — Build-tag handling

Default build tags only in Step 3. The `packages.Config.BuildFlags` is set to empty. If any `.go` file in the module has a `//go:build` constraint that excludes it under default tags, the lint:
- Logs a warning naming the file.
- Treats the file as not-scanned and records this fact.
- Does NOT fail the check, but the warning list is included in the test output.

Rationale: the AetherNet protocol does not currently use build tags for consensus-adjacent code (verified by `grep -rn "//go:build" internal/`). A future tag-gated store would require a Step 3.5 extension.

### D6 — Test organization

- **Unit tests** for the extractor and matcher use `testdata/` subdirectories with tiny Go packages exercising each branch. The `testdata/` convention is special to `go test` — contents are not compiled into the main build.
- **End-to-end test** `TestLintRepository` in `internal/projections/lint/` runs the full check against the actual module root (discovered via `runtime.Caller` → walking up to find `go.mod`). This is the test that fails in CI when a contributor adds an unregistered store.
- **Deliberate-unregistered test** creates a temp module in `t.TempDir()` with a synthetic unregistered store, runs the extractor + matcher against it, asserts the expected diagnostic fires.
- **Missing-testref test** same pattern with a Canonical entry pointing to a non-existent test symbol.

### D7 — CI integration & failure-mode specification

The lint is a Go test — `TestLintRepository` under `internal/projections/lint/`. `go test ./...` invokes it; there is no flag to skip it.

**Failure output format** (per diagnostic — D6 enhanced per founder refinement):
```
projections/lint: UNREGISTERED STORE
  Type:         <pkg.Type>
  File:         <absolute-or-repo-relative path>
  Line:         <line number of the struct declaration>
  Evidence:     has *badger.DB field (or embedded persistence type); writer method(s): <list>
  Remediation:  EITHER
                  (a) register this type. Add a <pkg>.<Type>Projection() constructor
                      returning a literal projections.CanonicalProjection{...} and
                      wire it via projReg.MustRegister in cmd/node/main.go — see
                      existing step-2 examples under internal/epoch/, internal/escrow/,
                      internal/ledger/, internal/ocs/, internal/reputation/,
                      internal/taskverification/ for the pattern.
                  OR
                  (b) suppress the lint. Add immediately above the type declaration:
                          // projections:lint ignore "<reason>"
                      The reason must be at least 20 characters AND at least 3 words,
                      and must describe why the type is NOT consensus-adjacent in a
                      way a reviewer can evaluate. Insufficient reasons are rejected.
  Heuristic:    this check is the CI heuristic from plan §9.3, an approximation of
                the principle-level definition (any durable state projection whose
                outputs can affect canonical protocol behavior). The heuristic catches
                the common shapes; principle-level coverage is the reviewer's job.
  Dynamic ok:   this lint relies on registration via a *Projection() constructor with
                a literal CanonicalProjection{...} struct. Dynamic registration
                patterns defeat the static analysis and require code-review
                confirmation of registration completeness.
```

Test-ref failure:
```
projections/lint: MISSING INTEGRATION TEST
  Entry:        <Name>
  Declared ref: <IntegrationTestRef>
  Resolution:   could not find Test function at that symbol path
  Required:     either create the test at the declared path, or update the
                entry's IntegrationTestRef to point at an existing test
```

---

## Package layout

```
internal/projections/lint/
  doc.go                 package overview
  extractor.go           parse *Projection() constructors, build registered-set
  matcher.go             heuristic for suspect store types
  testref.go             existence check for IntegrationTestRef
  lint.go                top-level Check() function that composes all three
  lint_test.go           unit tests for Check() and helpers
  repository_test.go     TestLintRepository — runs Check() against module root
  extractor_test.go      unit tests with testdata/
  matcher_test.go        unit tests with testdata/
  testref_test.go        unit tests with testdata/
  testdata/
    unregistered_store/  sample package with an unregistered suspect type
    registered_store/    sample package with a fully registered type
    suppressed_store/    sample with the // projections:lint ignore pragma
    missing_testref/     sample Projection() with broken IntegrationTestRef
```

---

## Task-by-task implementation

Each task follows TDD: write failing test, run, implement, run, commit. Branch `feat/projections-registry-step-3` (already created).

### Commit 1 — Package skeleton + `go.mod` dep

- [ ] Add `golang.org/x/tools` to `go.mod` (run `go get golang.org/x/tools/go/packages@latest`).
- [ ] Create `internal/projections/lint/doc.go` with package overview.
- [ ] Create `internal/projections/lint/lint.go` with stub `Check(modulePath string) (*Report, error)` that returns an empty Report.
- [ ] Create `internal/projections/lint/lint_test.go` with a smoke test confirming `Check()` returns a non-nil empty Report.
- [ ] `go test -race ./internal/projections/lint/...` passes.
- [ ] Commit: `feat(projections/lint): package skeleton and module dep`.

### Commit 2 — Extractor: parse `*Projection()` constructors

- [ ] Create `internal/projections/lint/extractor.go` implementing `extractRegisteredSet(pkgs []*packages.Package) RegisteredSet`. The implementation:
  1. Iterates every package.
  2. For each package, iterates exported functions returning `projections.CanonicalProjection`.
  3. AST-walks the function body to find the returned composite literal.
  4. Reads string-literal field values (Name, StoreType, IntegrationTestRef) and the Classification const.
  5. Populates `RegisteredSet` (map from `(pkgPath, storeType) → RegisteredEntry`).
- [ ] `RegisteredSet` type and `RegisteredEntry` (name, storeType, classification, integrationTestRef, srcLocation).
- [ ] `testdata/registered_store/` with a minimal package containing an exported `Projection()` returning a valid composite literal.
- [ ] `extractor_test.go` asserting extraction against testdata returns the expected entry.
- [ ] Also test: extraction against the real `internal/epoch/` package correctly identifies `RoundCounter`.
- [ ] Commit: `feat(projections/lint): extractor for per-package Projection() constructors`.

### Commit 3 — Matcher: heuristic for suspect store types

- [ ] Create `internal/projections/lint/matcher.go` implementing `findSuspectTypes(pkgs []*packages.Package) []SuspectType`:
  1. Iterates every package under `internal/`.
  2. For each struct type T, checks if T has a `*badger.DB` field (type check).
  3. If yes, checks methods on T (including pointer-receiver methods) for write operations (any method body that contains `Update(`, `Set(`, `Delete(` calls on a persistence field, or that mutates a field of T).
  4. Skips types with the `// projections:lint ignore "<reason>"` pragma; the pragma parser returns the reason or an error on empty/missing reason.
  5. Returns `[]SuspectType` with package path, type name, source location, evidence (which field / which methods).
- [ ] `SuspectType` struct.
- [ ] `testdata/unregistered_store/` with a suspect type and no `Projection()`.
- [ ] `testdata/suppressed_store/` with a suspect type plus the ignore pragma.
- [ ] `matcher_test.go` asserting the matcher catches unregistered_store and skips suppressed_store.
- [ ] Also test: pragma without a reason returns an error, forcing the contributor to think.
- [ ] Commit: `feat(projections/lint): matcher for suspect store types`.

### Commit 4 — IntegrationTestRef existence check

- [ ] Create `internal/projections/lint/testref.go` implementing `verifyTestRefs(pkgs []*packages.Package, set RegisteredSet) []MissingRef`:
  1. For each entry in `set` where `Classification == Canonical` and `IntegrationTestRef != ""`:
     - Parse the ref as `<import-path>.<symbol>`.
     - Locate the package in `pkgs`; if not loaded, flag.
     - Verify a `func <symbol>(t *testing.T)` exists in the package's Go files (test files included).
  2. Return the list of missing-ref diagnostics.
- [ ] `testdata/missing_testref/` with a `Projection()` whose `IntegrationTestRef` points at a non-existent symbol.
- [ ] `testref_test.go` asserting the check catches the miss and ignores Advisory entries.
- [ ] Commit: `feat(projections/lint): IntegrationTestRef existence check`.

### Commit 5 — Compose: `Check()` + `Report` + diagnostic formatting

- [ ] Flesh out `lint.go`:
  - `Check(modulePath string) (*Report, error)` — loads packages, runs extractor, matcher, testref, composes diagnostics.
  - `Report` type with `UnregisteredTypes []SuspectType`, `MissingRefs []MissingRef`, `Warnings []string` (for interface-indirect cases, build-tag-excluded files).
  - `Report.HasFailures() bool`, `Report.Format() string` for the failure-format in D7.
- [ ] `lint_test.go` extended with composition tests: full Check() invoked against the real module under `../../../` (walk up from the test file), asserts currently-registered projections pass.
- [ ] Commit: `feat(projections/lint): top-level Check() and Report formatting`.

### Commit 6 — TestLintRepository end-to-end harness

- [ ] Create `internal/projections/lint/repository_test.go` with `TestLintRepository(t *testing.T)`:
  1. Finds the module root via `runtime.Caller` walking up to `go.mod`.
  2. Runs `Check(moduleRoot)`.
  3. Fails the test with the formatted Report if `Report.HasFailures()` is true.
- [ ] This test runs as part of `go test ./internal/projections/lint/...` which is part of `go test ./...`. No skip flag.
- [ ] `go test -race ./internal/projections/lint/...` on current `main` passes (all step-2 projections are correctly registered and their testrefs resolve).
- [ ] Commit: `feat(projections/lint): TestLintRepository runs against module at go test ./... time`.

### Commit 7 — Negative tests (deliberate failures)

- [ ] Add two tests that temporarily construct a synthetic module in `t.TempDir()`:
  1. `TestLint_FailsOnUnregisteredStore`: copies a tiny Go module with an unregistered suspect type → runs Check → asserts `Report.HasFailures()` and the diagnostic names the offending type.
  2. `TestLint_FailsOnMissingTestRef`: similar but with a broken IntegrationTestRef → asserts the missing-ref diagnostic fires.
- [ ] These tests run against a synthetic module, not the real one, so they can pass while the real `TestLintRepository` also passes.
- [ ] Commit: `test(projections/lint): negative tests for unregistered store and missing testref`.

### Commit 8 — Push and verify

- [ ] `go test -race ./... 2>&1 | grep -E 'ok |FAIL'` — all packages PASS.
- [ ] `go vet ./internal/projections/lint/...` — clean.
- [ ] `go build ./...` — clean.
- [ ] Push `feat/projections-registry-step-3` to origin.

---

## Verification plan

Per CLAUDE.md §4:

1. `go test -race ./...` passes with the lint check active — all 9 registered projections pass the check.
2. `TestLintRepository` against current `main` reports zero unregistered stores and zero missing testrefs.
3. `TestLint_FailsOnUnregisteredStore` confirms the check catches a deliberately unregistered suspect type.
4. `TestLint_FailsOnMissingTestRef` confirms the check catches a broken IntegrationTestRef.
5. `TestLint_PragmaSuppression_RequiresReason` confirms the pragma without a reason is rejected.
6. Plan document ↔ implementation match; any divergence noted before final commit.

No live-testnet verification required per the step prompt — this is build-time tooling.

---

## Explicit deferrals

| Item | Reason | Target |
|---|---|---|
| SSA taint analysis for interface-indirect persistence | Higher complexity; principle-level review covers | Step 3.5 or future hardening pass |
| `golangci-lint` plugin distribution | Not needed for CI; `go test` harness suffices | Optional future |
| Extending to Advisory test-refs | V11 permits empty; enforcing would contradict | N/A |
| Non-default build-tag enumeration | Protocol currently uses no such tags; warning path in place | Step 3.5 if tags are added |
| Automatic fix generation | Diagnostic tells the contributor exactly what to add | Optional future |
| Reputation store (deleted in Step 3 per plan §17, replaced in Step 4) | Different workstream boundary | Step 4 |

---

## Sign-off status — ACCEPTED with four refinements (2026-04-15)

Founder accepted D1–D7 with four small refinements folded into the plan above:
- **D1**: `golang.org/x/tools` lands as a first-class direct require in `go.mod`.
- **D2**: diagnostic includes the "dynamic registration defeats static analysis; requires code-review confirmation" sentence.
- **D3**: heuristic extended to catch embedding / composition paths in addition to direct `*badger.DB` fields; interface-indirect persistence deferred to Step 3.5 per the cost gate (< ~50 LOC / < 1 day threshold).
- **D4**: suppression-pragma reason must be ≥20 characters AND ≥3 words; insufficient reasons rejected with their own explicit diagnostic.
- **D6**: `TestLintRepository` failure diagnostic names type + file + line + remediation-path-(a)-or-(b); experience test is "a contributor whose PR fails the lint knows exactly what to do without reading the lint's source".

Proceeding to implementation. Historical sign-off prompt retained below.

---

## Historical sign-off request — deltas summary

This plan is lighter than Step 2's because the scope is narrower (build-time tooling, no runtime changes, no testnet deploy required). The founder-facing deltas to highlight:

**D1** — static-analysis framework: `go/packages` + custom AST walk, with `golang.org/x/tools` as one new module dep. Not a third-party linter framework.

**D2** — registered-set source of truth: the per-package `*Projection()` constructor functions shipped in Step 2. Extracted via AST parsing of the returned composite literal. Single source; no parallel truth list.

**D3** — heuristic tightening (per step-3 prompt on Grok attack 8): non-exported writers also count as suspect. The heuristic catches more than plan §9.3's version; the gap between the two is on the safer side (false positives are suppressible).

**D4** — suppression pragma: `// projections:lint ignore "<reason>"` with a mandatory non-empty reason. Makes bypass reviewable, not silent.

**D5** — build tags: default-only in Step 3. Non-default-tagged files warn, not fail. Module currently uses no such tags; extension deferred to Step 3.5 if that changes.

**D6** — tests: `testdata/` + `TestLintRepository` end-to-end harness running the check against the real module at `go test ./...` time. No vet flag, no skip flag.

**D7** — failure diagnostics explicitly frame themselves as an approximation of the principle, pointing the contributor at the pragma for legitimate false positives.

Eight commits planned (1 skeleton, 1 per analyzer component, 1 composer, 1 harness, 1 negative-tests, 1 push).

**No code will be written until sign-off on the design decisions D1–D7 or redirection.**
