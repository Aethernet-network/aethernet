package derivation

import (
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/genesis"
)

// AnchorReader is the narrow subset of DAG read methods DeriveSettlement
// depends on. Satisfies DerivationInputs contract clause (b): every
// method is a deterministic function of canonical DAG state.
//
// Locally-defined here (not imported from internal/dispatch or
// internal/dag) to keep the derivation package's import graph narrow
// and to let the F5 5A.3 §2.1 consolidation (move to
// internal/dag/anchor_reader.go) land independently. *dag.DAG satisfies
// this interface structurally; once the consolidated type exists, a
// single-line import switch points this package at the canonical
// definition without caller changes.
//
// Returns dag.ErrEventNotFound on reads that cannot be served from
// locally-materialized state; DeriveSettlement converts the signal to
// Status=StatusDeferred with the appropriate DeferredCause.
type AnchorReader interface {
	// IsAncestor reports whether `ancestor` is a strict canonical
	// ancestor of `descendant`. Strict means irreflexive:
	// IsAncestor(X, X) == false. Used by the V-1 `isActivated` helper
	// and by ReadAtAnchor's anchor-scoping test.
	IsAncestor(ancestor, descendant event.EventID) (bool, error)

	// Get retrieves an event by ID. Used by ReadAtAnchor for BFS.
	// Returns ErrEventNotFound if the event is not locally
	// materialized.
	Get(id event.EventID) (*event.Event, error)

	// CountAncestorsByType counts the canonical strict ancestors of
	// descendant whose event type equals eventType (does NOT include
	// descendant itself; matches IsAncestor's irreflexive semantic).
	// Per F5 5B canonical-epoch sub-spec §3.
	//
	// All-or-defer per sub-spec §3.1: returns ErrEventNotFound if
	// descendant or any traversed ancestor is not locally materialized.
	// Caller (DeriveSettlement at the cutoff-epoch derivation site, or
	// the finalizing consumer at round.EpochAtFinalization population)
	// converts the signal to Status=StatusDeferred or to F3-B
	// causal-prerequisite-gating respectively.
	//
	// Returns (0, nil) if descendant exists but has no ancestors of
	// the requested type. Not an error.
	CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error)
}

// DerivationInputs bundles every canonical-state primitive
// DeriveSettlement reads. Constructed once per settler and passed into
// every DeriveSettlement invocation unchanged.
//
// **Multi-AI Item 1 composite (2026-04-25)**: ALL fields are unexported.
// External callers MUST construct via NewDerivationInputs, which
// performs §2.1 contract validation at the boundary (treasury identity,
// non-nil required services, activation EventIDs). The unexported
// fields prevent `derivation.DerivationInputs{...field-set...}`
// composite literals at the type level; the
// internal/settlement/lint/derivation_inputs_construction lint rule
// catches the residual zero-value `derivation.DerivationInputs{}`
// pattern outside the constructor allow-list.
//
// In-package construction (derivation/*_test.go) uses the unexported
// field names directly — same-package access is a deliberate design
// allowance so tests can exercise edge cases that the constructor's
// validation would reject (e.g., non-canonical TreasuryID values used
// purely to make assertion outputs human-readable).
//
// §2.1 CONTRACT (LOAD-BEARING — from Gate 5B Plan v1 multi-AI review):
// every field MUST satisfy at least one of:
//
//	(a) Canonical-frozen value — fixed at DerivationInputs construction
//	    time; does not change during DeriveSettlement. Example: a locked
//	    enum or an interface handle to a stub that only returns
//	    constants.
//
//	(b) Deterministic replayable lookup at cutoff — exposes a query
//	    interface whose return values are pure functions of canonical
//	    state at the cutoff anchor. Example: CanonicalWProjection.Lookup,
//	    AnchorReader.IsAncestor.
//
// No field may expose mutable state through alternative paths. Adding
// a field that violates the contract is a halt-and-surface trigger per
// plan §5: "Derivation function impurity detected".
//
// This contract is the 5B analogue of V-1's "no runtime flag" rule:
// V-1 forbade a reputationActivated bool anywhere in the derivation
// package; the DerivationInputs contract generalizes to forbid any
// state-leaking path through the input bundle's field set. Failure
// modes the contract rules out:
//
//   - A mutable wrapper around CanonicalWProjection that caches results
//     in a process-local map and falls back to live-read on miss.
//   - An AnchorReader wrapper that falls back to local-tip on
//     ErrEventNotFound instead of propagating the deferral signal.
//   - A "convenience" EscrowLookup.GetWithFallback that synthesizes
//     data when the entry is absent instead of returning an error.
//
// **Multi-AI Item 1 composite (2026-04-25)**: the prior failure mode
// "a flag-closing ActivationCheck whose closure captures a runtime
// reputationActivated bool" is no longer syntactically expressible.
// The function-field surface was removed; activation is now performed
// inside DeriveSettlement via the canonical primitives directly
// (`isActivated(inputs.dagReader, inputs.reputationActivationEventID,
// round.CanonicalSealContext)`). The activation EventID fields satisfy
// clause (a) (canonical-frozen at construction); the IsAncestor primitive
// satisfies clause (b) (deterministic canonical-DAG read). The contract
// is now mechanically enforced by the type system at the activation
// boundary — no review discipline required.
type DerivationInputs struct {
	// w is the CanonicalWProjection pair (stub + real). DeriveSettlement
	// selects Stub vs Real per the V-1 canonical-ancestor check against
	// reputationActivationEventID. Satisfies clause (b): both Stub and
	// Real expose deterministic Lookup returning canonical values;
	// selection is canonical-position-bound, never runtime-flag-bound.
	w WProjections

	// quality is the CanonicalQualityProjection pair (stub + real).
	// Same V-1 pattern as w. F5 ships with Real==nil; quality-activation
	// check always returns false until the future quality-
	// canonicalization workstream wires the real implementation.
	// Satisfies clause (b).
	quality QualityProjections

	// dagReader is the narrow canonical-DAG read surface. Satisfies
	// clause (b): IsAncestor and Get are deterministic functions of
	// canonical DAG state (materialization lag surfaces as
	// ErrEventNotFound, not as a wrong answer).
	dagReader AnchorReader

	// escrowMgr is the canonical-frozen escrow-entry read surface.
	// Satisfies clause (b): every method reads fields set at
	// RegisterEscrow and never mutated thereafter.
	escrowMgr EscrowLookup

	// **Drift cleanup note (multi-AI ChatGPT review, 2026-04-25)**:
	// taskMgr was removed from DerivationInputs at this point. The
	// early-exit short-circuit on task.Status (Gate 5A.1 §9.2 option-b)
	// lives at the SETTLER layer (verification_consensus_settler.go),
	// NOT in DeriveSettlement. The derivation function itself does not
	// read task.Status — that's a deliberate design invariant per Plan
	// v3 §0 decision. DerivationInputs only contains what DeriveSettlement
	// actually uses; settler retains its own *tasks.TaskManager
	// reference for the short-circuit.

	// reputationActivationEventID is the V-1 canonical-ancestor check
	// target for the W projection: real-W is selected for round R iff
	// IsAncestor(reputationActivationEventID, R.CanonicalSealContext).
	//
	// Satisfies §2.1 contract clause (a) — fixed at DerivationInputs
	// construction; does not change during DeriveSettlement. Pre-locked-
	// workstream placeholder is the empty string (constant
	// derivation.ReputationActivationEventID). The empty-string short-
	// circuit lives in `isActivated` per multi-AI Item 1 composite
	// (2026-04-25): the function-field ActivationCheck surface that
	// previously hid closure-captured runtime state is GONE; activation
	// is now performed via canonical primitives directly.
	reputationActivationEventID event.EventID

	// qualityActivationEventID is the analogous V-1 check target for the
	// Quality projection. Same discipline as reputationActivationEventID:
	// pre-locked-workstream placeholder is the empty string (constant
	// derivation.QualityActivationEventID). Satisfies clause (a).
	qualityActivationEventID event.EventID

	// treasuryID is the canonical treasury agent. Per F5 5A.1 manifest
	// treasury_id row.
	//
	// Source today (architect-confirmed at breakpoint-2 closure): a Go
	// compile-time constant `genesis.BucketTreasury = "genesis:treasury"`
	// at internal/genesis/genesis.go:39, wired in cmd/node/main.go:1665
	// as `crypto.AgentID(genesis.BucketTreasury)`. Identical on every
	// node by build artifact; canonical-frozen at compile time.
	//
	// Satisfies §2.1 contract clause (a) — fixed at DerivationInputs
	// construction; does not change during DeriveSettlement.
	// NewDerivationInputs validates this field == genesis.BucketTreasury
	// at construction (multi-AI Item 1 composite, 2026-04-25); the
	// construction-time check makes any later swap to a canonical-DAG-
	// derived TreasuryID a strictly stronger source without changing the
	// derivation package itself.
	treasuryID crypto.AgentID
}

// TreasuryID exposes the canonical treasury agent ID for read-only use
// inside the derivation package. Adapters and tests may need to surface
// the field externally; production code reads inputs.treasuryID directly
// in same-package routes.
func (i DerivationInputs) TreasuryID() crypto.AgentID { return i.treasuryID }

// NewDerivationInputs constructs a DerivationInputs bundle with §2.1
// contract validation per multi-AI Item 1 composite (2026-04-25).
//
// This is the ONLY supported external construction path. The unexported
// fields prevent composite-literal field assignment from outside the
// derivation package; the
// internal/settlement/lint/derivation_inputs_construction lint rule
// catches the residual zero-value `derivation.DerivationInputs{}`
// pattern outside this constructor.
//
// Validation rules:
//
//	(1) treasuryID MUST equal genesis.BucketTreasury. The canonical
//	    treasury identity is locked at the genesis bucket constant; no
//	    other value is admissible until the canonical-snapshot
//	    infrastructure provides a stronger source (FORWARD_NOTES.md §1).
//
//	(2) w.Stub, quality.Stub, dagReader, escrowMgr MUST be non-nil.
//	    A nil here would surface as a panic on the first
//	    DeriveSettlement call; the construction-time check catches
//	    wiring bugs at build/start time instead of at first settlement.
//
//	(3) Activation EventIDs (reputationActivationEventID,
//	    qualityActivationEventID) are accepted as-is. Empty string is
//	    the valid pre-locked-workstream placeholder per
//	    derivation.{Reputation,Quality}ActivationEventID constants;
//	    isActivated short-circuits the empty case to (false, nil).
//
// The `w.Real` and `quality.Real` slots are NOT validated as non-nil:
// they remain nil pre-activation per V-1 (the canonical-ancestor check
// always returns false for empty activation EventIDs, so the Real slot
// is never selected at runtime).
func NewDerivationInputs(
	w WProjections,
	quality QualityProjections,
	dagReader AnchorReader,
	escrowMgr EscrowLookup,
	reputationActivationEventID event.EventID,
	qualityActivationEventID event.EventID,
	treasuryID crypto.AgentID,
) (DerivationInputs, error) {
	if w.Stub == nil {
		return DerivationInputs{}, errors.New("derivation: NewDerivationInputs: W.Stub is nil")
	}
	if quality.Stub == nil {
		return DerivationInputs{}, errors.New("derivation: NewDerivationInputs: Quality.Stub is nil")
	}
	if dagReader == nil {
		return DerivationInputs{}, errors.New("derivation: NewDerivationInputs: dagReader is nil")
	}
	if escrowMgr == nil {
		return DerivationInputs{}, errors.New("derivation: NewDerivationInputs: escrowMgr is nil")
	}
	if treasuryID != crypto.AgentID(genesis.BucketTreasury) {
		return DerivationInputs{}, fmt.Errorf("derivation: NewDerivationInputs: treasuryID=%q must equal genesis.BucketTreasury=%q",
			treasuryID, genesis.BucketTreasury)
	}
	return DerivationInputs{
		w:                           w,
		quality:                     quality,
		dagReader:                   dagReader,
		escrowMgr:                   escrowMgr,
		reputationActivationEventID: reputationActivationEventID,
		qualityActivationEventID:    qualityActivationEventID,
		treasuryID:                  treasuryID,
	}, nil
}
