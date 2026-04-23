// Package protocolmath provides the canonical deterministic allocation
// primitive used wherever the protocol distributes a µAET pool across
// weighted recipients — settlement, generation-ledger royalties, validator
// fee splits.
//
// The package exists to satisfy two standing principles:
//
//   - Principle 6 (generalize the primitive, not the fix): every callsite
//     that performs proportional allocation funnels through one
//     implementation, so per-callsite bespoke arithmetic cannot drift
//     apart across nodes or across time.
//
//   - Principle 11 (integer canonical state, no exceptions): all
//     multiply-divide is via math/big.Int and the returned amounts are
//     integer µAET. Non-integer arithmetic is non-deterministic across
//     hardware and is forbidden in any code path whose output contributes
//     to what nodes must agree on.
//
// Determinism guarantees:
//
//   - Permutation invariance: recipients are sorted internally by
//     CanonicalKey (bytes.Compare ascending) before allocation. Caller
//     ordering does not affect output.
//
//   - Conservation: sum of returned amounts equals the input pool exactly.
//     Integer-arithmetic remainders are routed to the last-of-sorted
//     recipient so no µAET is created or destroyed.
//
//   - Byte-stable across runs, machines, and Go versions: no maps
//     iterated for ordering, no time-based randomness, no hardware-
//     dependent arithmetic.
//
// Callers must not perform their own proportional math; they construct a
// []Recipient and call Allocate (or AllocateWithCeiling for boundaries that
// receive externally-sourced weights and want defensive clamping to
// MaxBasisPoints).
//
// Invariant violations — notably a negative Weight — are surfaced as
// errors, never silently absorbed. The caller upstream has a bug in that
// case and must be told.
//
// See docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md
// §4.1 for the architectural rationale and the cross-cutting migration
// that replaces per-callsite non-integer arithmetic with this primitive.
package protocolmath
