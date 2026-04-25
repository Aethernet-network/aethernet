// Package derivation is the F5 Phase 5B pure-derivation layer for
// canonical settlement. Given a finalized TaskVerificationRound and a
// bundle of canonical-state primitives, DeriveSettlement produces the
// ordered slice of PayoutRecord values that fully settle the round.
//
// The function is pure: every correct node computing DeriveSettlement
// on the same canonical state produces a byte-identical DerivationResult
// (property D-1). Settlement effects become a function of canonical
// state, not of local execution ordering — closing the concurrent-Apply
// race that motivated F5.
//
// The package is split from internal/settlement to:
//   - Prevent accidental coupling with imperative settlement code.
//   - Enable clean CI lint scoping (F5 5A.4.c) against new derivation
//     reads without false-positives on existing imperative code.
//   - Make the canonical-derivation surface its own review target for
//     future workstreams (quality-canonicalization, real-W activation).
//
// Package layout:
//
//	doc.go          this file — scope and pointers.
//	types.go        DerivationStatus, DeferredCause, DerivationResult,
//	                DerivationSummary, TerminalStatus, DerivationVersion.
//	record.go       PayoutRecord + composite types per
//	                docs/architecture/payout-artifact-schema.yaml.
//	projections.go  CanonicalWProjection, CanonicalQualityProjection,
//	                and the stub/real pair bundles.
//	stubs.go        NeutralBPStubW, NeutralQualityStub.
//	lookups.go      EscrowLookup, TaskLookup — the minimal canonical
//	                reads the derivation function needs.
//	inputs.go       DerivationInputs + §2.1 contract documentation.
//	activation.go   ActivationCheck function type + canonical activation
//	                event-id symbol placeholders.
//	canonical_id.go CanonicalID computation (SHA-256 + RFC 8785 JCS).
//	derive.go       DeriveSettlement entry point.
//
// This package is the 5B load-bearing surface. The §2.1 DerivationInputs
// contract — every field is canonical-frozen OR deterministic replayable
// lookup at cutoff — is the structural defense against non-canonical
// state leaking into derivation. Do not add a field to DerivationInputs
// that violates the contract.
package derivation
