package settlement

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// ComputeValidatorPayoutsIntegerForTest is a test-only entry point that
// invokes the exact integer distribution logic the settler uses on its
// canonical path when shadowMode is false. Not for production use; exposed
// so the cross-architecture determinism binary at cmd/cross-arch-corpus/
// can exercise the code that ships, not a mirror of it. Any change to
// computeValidatorPayoutsInteger is reflected here automatically — this
// is a call, not a copy.
//
// The returned map keys are the AgentIDs of the input recipients with
// their allocated amounts in µAET. On-error paths (negative Q clamped,
// protocolmath error fallback to even-split) are reached identically to
// the production settler path.
func ComputeValidatorPayoutsIntegerForTest(
	qFn ValidatorQScoreFn,
	recipients []crypto.AgentID,
	pool uint64,
	category string,
) map[crypto.AgentID]uint64 {
	s := &VerificationConsensusSettler{
		qScoreFn:   qFn,
		shadowMode: false,
	}
	return s.computeValidatorPayoutsInteger(recipients, pool, category)
}
