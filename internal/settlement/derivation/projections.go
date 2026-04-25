package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
)

// CanonicalWProjection is the canonical-state lookup for a validator's
// reputation weight W at a given cutoff. Specification source of truth:
// docs/architecture/q-score-canonicalization-design.md §0.8.1.
//
// Satisfies DerivationInputs contract clause (b): deterministic
// replayable lookup at cutoff. Two nodes calling Lookup with identical
// arguments against the same canonical state at the same cutoff epoch
// produce byte-identical return values.
//
// Returns ErrEventNotFound on materialization lag (F5 5A.2 §7.2).
// Caller (DeriveSettlement) converts that signal to
// Status=StatusDeferred, Cause=DeferredCauseWLookup per plan §2.3
// step 6.
//
// Return value is a BasisPoints weight in the locked tier set per the
// reputation workstream (7000/8500/10000/11500/13000). The stub
// implementation (NeutralBPStubW) returns NeutralBP (10000) for all
// inputs; the real implementation ships with the Reputation-and-
// Consensus-Integrity workstream and is selected at derivation time via
// the V-1 canonical-ancestor check (ActivationCheck).
type CanonicalWProjection interface {
	Lookup(
		validator crypto.AgentID,
		family string,
		category string,
		contributor crypto.AgentID,
		escrowBudget uint64,
		epoch uint64,
	) (protocolmath.BasisPoints, error)
}

// CanonicalQualityProjection is the canonical-state lookup for a
// generation-ledger ancestor event's quality at a given cutoff.
// Specification reference: docs/architecture/generation-ledger-canonical-
// derivation.md §6.4.
//
// Satisfies DerivationInputs contract clause (b). Returns
// ErrEventNotFound on materialization lag; caller converts to
// Status=StatusDeferred, Cause=DeferredCauseQualityLookup.
//
// F5 ships with the stub (NeutralQualityStub) only. Real per-event
// quality is deferred to a future quality-canonicalization workstream;
// when it ships, selection is V-1-bound (canonical-position, not
// runtime-flag), analogous to CanonicalWProjection.
type CanonicalQualityProjection interface {
	Lookup(
		ancestorID event.EventID,
		epoch uint64,
	) (protocolmath.BasisPoints, error)
}

// WProjections bundles the stub and real CanonicalWProjection
// implementations. DeriveSettlement receives the bundle and selects
// Stub vs Real per the V-1 canonical-ancestor check (plan §2.3 step 4:
// `wImpl := useRealW ? W.Real : W.Stub`).
//
// V-1 discipline: the selection happens INSIDE DeriveSettlement against
// the round's canonical position, never at wiring/call-site time
// against runtime flags. The caller passes the bundle once per settler;
// DeriveSettlement picks the implementation per round.
//
// Real may be nil pre-activation (the ReputationActivation event has
// not been defined in the codebase as of F5 5B landing). When nil,
// ActivationCheck must always return false for every round the settler
// sees, so DeriveSettlement never reaches the `wImpl := W.Real` branch.
// A future commit that wires the real implementation drops the nil and
// the same DeriveSettlement selects it automatically at the first round
// whose canonical position is AT-OR-AFTER ReputationActivation.
type WProjections struct {
	Stub CanonicalWProjection
	Real CanonicalWProjection
}

// QualityProjections bundles the stub and real CanonicalQualityProjection
// implementations. Selection pattern mirrors WProjections: Stub vs Real
// is canonical-position-bound per V-1.
//
// F5 5B ships with Real == nil (no real quality implementation
// scheduled until a future workstream). ActivationCheck for
// quality-activation always returns false under the shipped config.
type QualityProjections struct {
	Stub CanonicalQualityProjection
	Real CanonicalQualityProjection
}
