// Package lint implements the F5 Phase 5A.4.c CI check that validates
// settlement-code reads against the settlement-input-manifest.yaml.
//
// The settlement input manifest enumerates every value that flows into
// the canonical payout derivation (and the control-flow / observability
// reads adjacent to it). Each manifest entry carries a `source_locations`
// list of file:line tuples naming the read sites that consume that value.
// This lint walks the in-scope source packages, extracts every read-shape
// AST node (CallExpr, SelectorExpr, IndexExpr), and reports any read
// whose file:line is NOT declared in the manifest.
//
// Scope (per F5 Phase 5A.4.c): walks `internal/settlement/`,
// `internal/escrow/`, `internal/ledger/`, and `internal/reputation/`
// only. Out-of-scope-but-adjacent reads in `internal/staking/`,
// `internal/fees/`, and `internal/identity/` are explicitly NOT scanned.
// Future workstreams (F8 stake-manager defense-in-depth, post-F5 broader
// transfer workstream, Reputation Step 4) own those domains.
//
// The lint runs as a Go test (TestLintRepository) so `go test ./...`
// invokes it by default — no separate CI hook is required. This mirrors
// the projections/lint precedent.
//
// Suppression of individual findings is via a per-line pragma:
//
//	// settlement:lint ignore "<reason>"
//
// Reasons must be at least 20 characters AND at least 3 whitespace-
// separated words; insufficient justifications fail the lint.
//
// Manifest path: docs/architecture/settlement-input-manifest.yaml
//
// # Warnings vs Failures (5A.4.c-vs-5D enforcement posture)
//
// As of 5A.4.c ship, the lint surfaces undeclared in-scope reads as
// informational warnings rather than failures. This is deliberate: the
// settlement input manifest was authored at "unique input" granularity
// (~97 source_locations) during 5A.1 audit, while the repo contains
// ~2363 AST-read-shaped sites in scope. Most of those 2363 are
// mechanical reads (local variables, function parameters) not semantic
// derivation inputs. The capability to fail on undeclared reads is
// proven (TestLint_FailsOnUndeclaredRead); active enforcement gate
// flips at 5D when the manifest is expanded for comprehensive
// regression-detection coverage. The threshold change is a one-line
// edit.
//
// Architect-confirmed at Gate 5A.4.c closure: this scoping is correct
// discipline. Flipping the gate to FAIL today would either require
// expanding the manifest from ~97 to ~2363 source_locations
// (substantial 5A scope expansion, much spurious because most reads
// are mechanical local-variable reads, not semantic derivation
// inputs), or require the lint to become smarter about read
// classification (research-project scope). Neither is right for
// 5A.4.c.
//
// Future maintainers: "lint warns but doesn't fail" is NOT a bug. It
// is the deliberate 5A.4.c-vs-5D scoping. The mechanism ships now;
// 5D's verification matrix takes responsibility for the comprehensive
// regression-detection gate.
//
// Failure-causing finding categories TODAY (active gates):
//   - Manifest-integrity issues (file unreadable, line out of range)
//   - Stale manifest entries (declared file:line with no AST activity at all)
//   - Insufficient pragmas (reason fails ≥20-char / ≥3-word validation)
//
// Warning-only finding categories TODAY (5D will flip these to failures):
//   - Undeclared in-scope reads
//
// Binding spec:
//   - F5 Phase 5A.1 manifest: `docs/architecture/settlement-input-manifest.yaml`
//   - F5 Phase 5A Plan v3 §3.1 (Consumer contract between phases)
//   - Gate 5A.1 Finding 5 (deliberate-undeclared-read negative test)
//   - Gate 5A.1 Finding 10 (do not "just update the test"; investigate
//     whether a failing existing test encodes a legitimate invariant)
package lint
