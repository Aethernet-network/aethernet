package derivation

// Pool-share constants — basis points out of 10000. LOCKED at the v4.1
// economic model. Mirror `internal/settlement/verification_consensus_settler.go`
// constants exactly (workerShareBP / validatorShareBP / generationShareBP /
// treasuryShareBP) so the F5 5B derivation function produces byte-identical
// pool amounts to the pre-5B settler for the same canonical inputs.
//
// Any change to these constants would change the canonical settlement
// output across the protocol; not in F5 scope.
const (
	WorkerShareBP     uint64 = 7300 // 73%
	ValidatorShareBP  uint64 = 2300 // 23%
	GenerationShareBP uint64 = 200  // 2%
	TreasuryShareBP   uint64 = 200  // 2%
)

// SharesDenominator is the basis-points denominator. Pool share math is
// `budget * shareBP / SharesDenominator` with integer truncation;
// remainders flow to treasury per the canonical conservation rule.
const SharesDenominator uint64 = 10000
