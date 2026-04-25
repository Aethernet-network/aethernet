package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// EscrowLookup is the narrow read surface DeriveSettlement needs from
// the escrow manager. Satisfies DerivationInputs contract clause (b):
// every method reads canonical-frozen fields populated at
// RegisterEscrow time.
//
// This is an interface rather than a direct *escrow.Escrow dependency
// so the derivation package:
//   - Avoids importing escrow (which imports ledger; keeps the
//     derivation package's import graph narrow and testable).
//   - Can be exercised in unit tests with a fake that does not need
//     the full escrow state machine.
//   - Enforces at the type level that derivation does not reach beyond
//     the declared reads (non-canonical fields like the paid-flag
//     projection are not on this surface).
type EscrowLookup interface {
	// FundingRef returns the canonical Transfer event ID that funded
	// the escrow for taskID. Canonical-frozen at RegisterEscrow.
	// Returns a non-nil error (e.g., ErrEscrowNotFound) if no entry
	// exists for taskID — caller treats as a derivation-time
	// precondition violation (settler guarantees entry presence for
	// every round it settles).
	FundingRef(taskID string) (event.EventID, error)

	// EscrowAmount returns the canonical escrow budget for taskID.
	// Sum of canonical Transfer inputs to the escrow bucket for the
	// task; canonical-frozen once the escrow round opens. Used by the
	// pool-share computation in deriveAccept / deriveReject.
	EscrowAmount(taskID string) (uint64, error)

	// PosterID returns the canonical poster agent for taskID,
	// canonical-frozen at TaskPosted. Exposed on this interface as a
	// fallback when the round does not carry PosterID directly (in
	// current code paths the round does); keeping it in the contract
	// lets the derivation function read the escrow-side value when
	// needed without reaching into the escrow package for it.
	PosterID(taskID string) (crypto.AgentID, error)
}

// TaskLookup was removed at multi-AI ChatGPT review (2026-04-25) as
// drift cleanup. DeriveSettlement does not read task metadata directly
// — the task.Status early-exit short-circuit per Gate 5A.1 §9.2
// option-b lives at the SETTLER layer, not in the derivation function;
// Category/Family arguments to W.Lookup come from round.Category +
// empty-string family per the pre-5B settler convention.
//
// If a future workstream adds a derivation-time task-metadata read
// (e.g., a canonical-frozen Category lookup that doesn't go through
// the round struct), define a narrow TaskCanonicalReader interface at
// that point — but only when the read is genuinely needed. Premature
// surface = drift.
