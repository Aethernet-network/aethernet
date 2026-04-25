package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
)

// NeutralBPStubW is the F5 5B CanonicalWProjection stub. Returns
// NeutralBP (10000) for every input, satisfying the contract that the
// protocol behaves as if every validator has Q = 1.0 until the real
// reputation implementation activates via V-1 canonical-position
// selection.
//
// Specification: docs/architecture/q-score-canonicalization-design.md
// §0.8.6 (NeutralBPStubW).
//
// Purity: no fields, no internal state, pure function of inputs.
// Satisfies DerivationInputs contract clause (b) trivially: the return
// value is deterministic per arguments (and insensitive to all of
// them today). Never returns an error; never returns ErrEventNotFound.
type NeutralBPStubW struct{}

// Lookup always returns NeutralBP, nil. See type doc.
func (NeutralBPStubW) Lookup(
	_ crypto.AgentID,
	_ string,
	_ string,
	_ crypto.AgentID,
	_ uint64,
	_ uint64,
) (protocolmath.BasisPoints, error) {
	return protocolmath.NeutralBP, nil
}

// NeutralQualityStub is the F5 5B CanonicalQualityProjection stub.
// Returns NeutralBP for every ancestor at every epoch. Satisfies the
// pre-real-quality convention that every gen-ledger ancestor weighs
// equally before per-event quality canonicalization ships.
//
// Specification: docs/architecture/generation-ledger-canonical-
// derivation.md §6.4 (NeutralQualityStub pattern).
//
// Purity: same structure as NeutralBPStubW.
type NeutralQualityStub struct{}

// Lookup always returns NeutralBP, nil.
func (NeutralQualityStub) Lookup(
	_ event.EventID,
	_ uint64,
) (protocolmath.BasisPoints, error) {
	return protocolmath.NeutralBP, nil
}
