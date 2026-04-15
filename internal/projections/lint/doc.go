// Package lint implements the static-analysis check that closes plan §9.7
// defense item 1 ("CI static check for writer-without-caller pattern") and
// the second half of PR-3 ("IntegrationTestRef must resolve to a real test
// symbol").
//
// The check runs as a Go test (TestLintRepository) so `go test ./...`
// invokes it by default. There is no vet flag to omit and no skip flag;
// suppression of individual findings is only via an explicit
// `// projections:lint ignore "<reason>"` pragma with a sufficient
// justification.
//
// Binding spec:
//   - docs/plans/2026-04-12-reputation-and-consensus-integrity.md §9.3
//     (structural definition) and §9.7 (defense stack).
//   - docs/plans/2026-04-12-reputation-step-3-projection-lint.md (this
//     step's implementation plan; resolved founder refinements on D1–D7).
//
// Design decisions recorded in the plan:
//   - D1: golang.org/x/tools/go/packages + custom AST walk (first-class dep).
//   - D2: registered set extracted by AST-parsing per-package *Projection()
//     constructors returning CanonicalProjection literals.
//   - D3: heuristic matches direct *badger.DB fields AND embedding-based
//     persistence; interface-indirect persistence deferred to Step 3.5.
//   - D4: suppression pragma requires ≥20 chars, ≥3 words.
//   - D5: default build tags only; tagged files warn, do not fail.
//   - D6: failure diagnostics name type + file + line + remediation path.
//   - D7: TestLintRepository runs the real check; unsuppressable at CI.
package lint
