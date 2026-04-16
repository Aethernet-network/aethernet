// Package escrow_testhelpers provides test-only combined fund-and-register
// primitives that production code cannot import.
//
// The module is structurally quarantined: its module path is declared in a
// separate go.mod, and the main module's go.mod does not depend on it. The
// production binary's dependency tree (go list -deps ./cmd/node) therefore
// excludes this package by construction. See
// docs/plans/2026-04-15-settlement-consensus-integrity-fix.md §6.1 and
// docs/plans/2026-04-15-f3b-part-e-escrow-hardening.md §1.
package escrow_testhelpers
