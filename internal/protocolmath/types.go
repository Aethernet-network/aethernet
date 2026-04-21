package protocolmath

// BasisPoints is the integer representation of a ratio where 10000 equals
// 1.0. The protocol uses basis points in place of non-integer fractions so
// every weight, share, and score has a byte-stable canonical form.
//
// Valid range is [0, MaxBasisPoints]. Negative values are invariant
// violations surfaced via ErrInvariantViolation from the allocator. Values
// above MaxBasisPoints are rejected by the non-clamping Allocate path and
// silently clamped to MaxBasisPoints by AllocateWithCeiling.
type BasisPoints int64

// MicroAET is the protocol's canonical unit of value. One AET equals 10^6
// µAET. All on-ledger amounts — balances, escrow pools, transfer amounts —
// are expressed as MicroAET. A uint64 comfortably holds the total supply
// (10^15 µAET) with many orders of magnitude to spare.
type MicroAET uint64

// Recipient represents one payee in a proportional allocation.
//
// CanonicalKey is the byte sequence used for deterministic ordering; in
// settlement callsites it is typically the AgentID bytes, but any stable
// byte identity works. Two recipients with the same CanonicalKey within a
// single allocation call is treated as an upstream bug and surfaced via
// ErrDuplicateCanonicalKey.
//
// Weight is the recipient's relative share. A zero Weight means the
// recipient is enumerated in the output map with amount 0. A negative
// Weight is an invariant violation.
type Recipient struct {
	CanonicalKey []byte
	Weight       BasisPoints
}

// NeutralBP is the "Q = 1.0" convention in basis points. Subsystems that
// score validators on a reputational axis use NeutralBP as the default
// score for a participant with no history.
const NeutralBP BasisPoints = 10000

// MaxBasisPoints is the protocol-enforced ceiling for any Q-like score.
// Enforced at the producer boundary when scores are emitted and at every
// allocator callsite so a runaway weight cannot distort a settlement.
const MaxBasisPoints BasisPoints = 100000
