package settlement

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
)

// escrowDerivationLookup adapts *escrow.Escrow to the
// derivation.EscrowLookup interface. The adapter is read-only on its
// underlying *escrow.Escrow — every method delegates to existing
// canonical-frozen reads (Get for FundingTransferRef + Amount + PosterID).
//
// Per F5 5B Plan v3 §3 + canonical-epoch sub-spec §8.2: the adapter
// keeps the derivation package isolated from the concrete escrow
// implementation while preserving the canonical-state-only read
// discipline.
type escrowDerivationLookup struct {
	escrow *escrow.Escrow
}

func (a escrowDerivationLookup) FundingRef(taskID string) (event.EventID, error) {
	entry, err := a.escrow.Get(taskID)
	if err != nil {
		return "", err
	}
	return entry.FundingTransferRef, nil
}

func (a escrowDerivationLookup) EscrowAmount(taskID string) (uint64, error) {
	entry, err := a.escrow.Get(taskID)
	if err != nil {
		return 0, err
	}
	return entry.Amount, nil
}

func (a escrowDerivationLookup) PosterID(taskID string) (crypto.AgentID, error) {
	entry, err := a.escrow.Get(taskID)
	if err != nil {
		return "", err
	}
	return entry.PosterID, nil
}

// (taskDerivationLookup removed at multi-AI ChatGPT review 2026-04-25
// alongside derivation.TaskLookup interface. DeriveSettlement does not
// consume task-metadata from inputs; the settler retains its own
// *tasks.TaskManager reference for the Gate 5A.1 §9.2 option-b
// short-circuit. See internal/settlement/derivation/lookups.go.)

// qScoreFnAsCanonicalW adapts a ValidatorQScoreFn closure to the
// CanonicalWProjection interface. Used to wire the existing per-node W
// implementation (currently NeutralBP for new validators; reads the
// reputation store for known validators) into the derivation package's
// stub-W slot.
//
// The Lookup signature collapses CanonicalWProjection's six parameters
// down to ValidatorQScoreFn's three (validator, family, category) —
// the contributor / escrowBudget / epoch arguments are absorbed by the
// closure but not consulted by today's implementation. When the locked
// Reputation-and-Consensus-Integrity workstream ships a real W, it
// will be wired via WProjections.Real and this adapter falls out of
// the stub slot naturally.
type qScoreFnAsCanonicalW struct {
	fn ValidatorQScoreFn
}

func (a qScoreFnAsCanonicalW) Lookup(
	validator crypto.AgentID,
	family string,
	category string,
	_ crypto.AgentID,
	_ uint64,
	_ uint64,
) (protocolmath.BasisPoints, error) {
	if a.fn == nil {
		return protocolmath.NeutralBP, nil
	}
	return a.fn(validator, family, category), nil
}

// dagAnchorReaderAdapter narrows *dag.DAG to the
// derivation.AnchorReader surface DeriveSettlement consumes. Although
// passing *dag.DAG directly through the AnchorReader interface already
// narrows at the type level (the interface upcast hides every method
// not in AnchorReader), this explicit struct adapter provides:
//
//   - **Refactoring resistance**: a future change that broadens the
//     settler's DAGReader surface back to *dag.DAG cannot accidentally
//     leak the wide type into NewDerivationInputs without first
//     refactoring through this adapter.
//   - **Pattern parity**: matches the precedent set by
//     escrowDerivationLookup and qScoreFnAsCanonicalW (multi-AI Item 1
//     composite, 2026-04-25 — "where DerivationInputs surfaces a
//     service interface, wrap the real service in a narrow adapter
//     exposing only canonical-frozen reads").
//
// Only the three AnchorReader methods are exposed; the underlying
// *dag.DAG's mutating Add/Remove/Reorganize surface is structurally
// unreachable from derivation code.
type dagAnchorReaderAdapter struct {
	dag *dag.DAG
}

func (a dagAnchorReaderAdapter) IsAncestor(ancestor, descendant event.EventID) (bool, error) {
	return a.dag.IsAncestor(ancestor, descendant)
}

func (a dagAnchorReaderAdapter) Get(id event.EventID) (*event.Event, error) {
	return a.dag.Get(id)
}

func (a dagAnchorReaderAdapter) CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error) {
	return a.dag.CountAncestorsByType(descendant, eventType)
}

// Compile-time assertions: each adapter satisfies its target interface.
var (
	_ derivation.EscrowLookup         = escrowDerivationLookup{}
	_ derivation.CanonicalWProjection = qScoreFnAsCanonicalW{}
	_ derivation.AnchorReader         = dagAnchorReaderAdapter{}
)
