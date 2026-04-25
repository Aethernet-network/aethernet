package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ReputationActivationEventID is the canonical DAG event ID at which
// real-W replaces NeutralBPStubW per the V-1 invariant. Its definition
// is owned by the locked Reputation-and-Consensus-Integrity workstream
// (see docs/plans/2026-04-12-reputation-and-consensus-integrity.md),
// which has not yet shipped.
//
// Until that workstream defines the concrete activation event, this
// constant is the empty string — which `isActivated` short-circuits to
// `(false, nil)` (select stub) without surfacing an ErrEventNotFound
// deferral signal. Once the real workstream ships and this constant is
// set to the real activation event's EventID, DeriveSettlement
// automatically selects real-W for rounds canonically AFTER the
// activation event — no derivation-package change required.
//
// Must be a compile-time constant (not var) to preserve V-1: a mutable
// variable could be overwritten at runtime to swap selection behavior,
// exactly the non-canonical-state leakage the §2.1 contract forbids.
const ReputationActivationEventID event.EventID = ""

// QualityActivationEventID is the analogous symbol for quality-activation.
// Same discipline as ReputationActivationEventID: empty pre-workstream;
// concrete event ID post-workstream; const-bound, not var-bound.
const QualityActivationEventID event.EventID = ""

// isActivated is the V-1 canonical-ancestor check used by
// DeriveSettlement to decide stub vs real for each activation-gated
// primitive (W today; quality later).
//
// Semantic: returns true iff `activationID` is a canonical ancestor of
// `sealCtx` in the DAG. Plan v3 §2.3 step 3 pseudocode:
//
//	useRealW, err := isActivated(dagReader, ReputationActivationEventID, R.CanonicalSealContext)
//	if errors.Is(err, ErrEventNotFound) { defer with Cause=DeferredCauseV1AncestorCheck }
//
// Empty-activation-ID short-circuit: when `activationID == ""`, returns
// (false, nil) directly — matches the pre-locked-workstream universal-
// stub-W behavior per Plan v3 §0 V-1 discipline. Without this short-
// circuit, dag.IsAncestor("", X) returns ErrEventNotFound (per
// dag.go:665-667) which DeriveSettlement would convert to
// Cause=DeferredCauseV1AncestorCheck, deferring every round forever.
//
// Boundary semantic (Grok-predicted discovery-tax item per Plan v3 §4.6):
// at the EXACT canonical position of the activation event, the check is
// irreflexive — IsAncestor(A, A) returns false per dag.IsAncestor's
// strict-ancestor semantic. Consistent interpretation: activation takes
// effect for rounds canonically AFTER the activation event, not AT it.
//
// Purity (multi-AI Item 1 composite, 2026-04-25): isActivated reads ONLY
// the canonical AnchorReader.IsAncestor primitive plus a canonical-frozen
// activationID value. The function-field surface that previously allowed
// closure-captured runtime state is GONE — the §2.1 DerivationInputs
// contract is now enforced by the type system: there is no syntactic
// place a runtime flag could hide.
//
// Errors: returns the underlying dag.ErrEventNotFound (or an error
// wrapping it) when the sealCtx is not yet locally materialized.
// DeriveSettlement converts that signal to Status=StatusDeferred,
// Cause=DeferredCauseV1AncestorCheck.
func isActivated(reader AnchorReader, activationID event.EventID, sealCtx event.EventID) (bool, error) {
	if activationID == "" {
		return false, nil
	}
	return reader.IsAncestor(activationID, sealCtx)
}
