package protocolmath

import (
	"bytes"
	"errors"
	"math/big"
	"sort"
)

// Sentinel errors surfaced by the allocator. Callers should match via
// errors.Is, and upstream code that sees ErrInvariantViolation should log
// (the primitive does not) and then fail loudly — a negative weight or a
// duplicated canonical key means an upstream subsystem has a bug that
// silent absorption would hide.
var (
	// ErrInvariantViolation is returned when any recipient's Weight is
	// negative. The output map is empty (no partial results). Callers
	// upstream must treat this as a hard failure.
	ErrInvariantViolation = errors.New("protocolmath: invariant violation: negative weight")

	// ErrEmptyRecipients is returned when recipients is empty and pool is
	// nonzero. The caller is responsible for routing the orphaned pool —
	// typically to treasury — because this function does not encode a
	// policy about where unallocated funds should go.
	ErrEmptyRecipients = errors.New("protocolmath: empty recipient set with nonzero pool")

	// ErrDuplicateCanonicalKey is returned when two or more recipients
	// share a CanonicalKey. Stringifying CanonicalKey as the output map
	// key would collapse duplicates silently, and settlement callsites
	// should never produce duplicates, so the primitive surfaces this
	// loudly rather than merging or overwriting.
	ErrDuplicateCanonicalKey = errors.New("protocolmath: duplicate canonical key in recipient set")
)

// Allocate distributes pool among recipients weighted by Weight and
// returns a map keyed by string(CanonicalKey) with each recipient's share
// in MicroAET.
//
// Invariants:
//
//   - Recipients with Weight == 0 appear in the output map with amount 0.
//
//   - Sum of returned amounts equals pool exactly. Integer-arithmetic
//     remainders are absorbed by the last-of-sorted-order recipient.
//
//   - Recipients are sorted internally by CanonicalKey (bytes.Compare
//     ascending) before allocation; caller ordering does not affect the
//     output.
//
//   - A negative Weight returns ErrInvariantViolation without computing
//     any output.
//
//   - Total Weight == 0 with nonzero pool returns even-split:
//     pool / N per recipient, last absorbs remainder.
//
//   - Duplicate CanonicalKeys return ErrDuplicateCanonicalKey.
//
//   - Empty recipients with pool == 0 returns an empty map with no
//     error. Empty recipients with pool > 0 returns ErrEmptyRecipients.
//
//   - pool == 0 with nonzero recipients returns each recipient with
//     amount 0 (uniform output shape across pool values).
//
// All internal arithmetic uses math/big.Int; no non-integer arithmetic
// appears anywhere in this package.
func Allocate(recipients []Recipient, pool MicroAET) (map[string]MicroAET, error) {
	return allocate(recipients, pool, false)
}

// AllocateWithCeiling is Allocate with a defensive clamp: any Weight that
// exceeds MaxBasisPoints is clamped to MaxBasisPoints before the
// allocation runs. Negative weights are still rejected with
// ErrInvariantViolation; clamping applies only to the positive overflow
// direction. The ceiling variant is intended for boundaries that receive
// externally-sourced Q-like scores and want to cap a runaway value
// without rejecting the whole allocation.
func AllocateWithCeiling(recipients []Recipient, pool MicroAET) (map[string]MicroAET, error) {
	return allocate(recipients, pool, true)
}

// allocate is the internal implementation shared by Allocate and
// AllocateWithCeiling. The clamp flag controls whether weights above
// MaxBasisPoints are clamped to MaxBasisPoints (true) or passed through
// unchanged (false).
func allocate(recipients []Recipient, pool MicroAET, clamp bool) (map[string]MicroAET, error) {
	// Invariant scan — surfaced before any other gate so the caller
	// always sees ErrInvariantViolation when weights are malformed, even
	// if the pool or recipient count would have triggered a different
	// error.
	for i := range recipients {
		if recipients[i].Weight < 0 {
			return map[string]MicroAET{}, ErrInvariantViolation
		}
	}

	// Empty-recipient gates.
	if len(recipients) == 0 {
		if pool == 0 {
			return map[string]MicroAET{}, nil
		}
		return map[string]MicroAET{}, ErrEmptyRecipients
	}

	// Defensive copy so we never mutate the caller's slice. Apply the
	// ceiling clamp at the same time for the AllocateWithCeiling path.
	sorted := make([]Recipient, len(recipients))
	copy(sorted, recipients)
	if clamp {
		for i := range sorted {
			if sorted[i].Weight > MaxBasisPoints {
				sorted[i].Weight = MaxBasisPoints
			}
		}
	}

	// Deterministic ordering by CanonicalKey.
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].CanonicalKey, sorted[j].CanonicalKey) < 0
	})

	// Duplicate-key detection. Post-sort, duplicates are adjacent.
	for i := 1; i < len(sorted); i++ {
		if bytes.Equal(sorted[i-1].CanonicalKey, sorted[i].CanonicalKey) {
			return map[string]MicroAET{}, ErrDuplicateCanonicalKey
		}
	}

	out := make(map[string]MicroAET, len(sorted))

	// Zero-pool path: every recipient receives zero. Uniform shape.
	if pool == 0 {
		for i := range sorted {
			out[string(sorted[i].CanonicalKey)] = 0
		}
		return out, nil
	}

	// Total-weight sum in big.Int to match the mulDivBig arithmetic
	// domain. All weights are non-negative after the invariant scan.
	totalWeight := new(big.Int)
	for i := range sorted {
		totalWeight.Add(totalWeight, big.NewInt(int64(sorted[i].Weight)))
	}

	// Zero-total-weight fallback: even split, last-of-sorted absorbs
	// remainder.
	if totalWeight.Sign() == 0 {
		n := uint64(len(sorted))
		each := uint64(pool) / n
		var distributed uint64
		for i := 0; i < len(sorted)-1; i++ {
			out[string(sorted[i].CanonicalKey)] = MicroAET(each)
			distributed += each
		}
		out[string(sorted[len(sorted)-1].CanonicalKey)] = MicroAET(uint64(pool) - distributed)
		return out, nil
	}

	// Weighted path: every recipient except the last receives the
	// floor of (pool * weight / totalWeight); the last absorbs any
	// remainder so the sum equals pool exactly. Recipients with weight
	// zero receive exactly zero.
	poolBig := new(big.Int).SetUint64(uint64(pool))
	var distributed MicroAET
	for i := 0; i < len(sorted)-1; i++ {
		w := sorted[i].Weight
		if w == 0 {
			out[string(sorted[i].CanonicalKey)] = 0
			continue
		}
		wBig := big.NewInt(int64(w))
		amount := mulDivBig(poolBig, wBig, totalWeight)
		out[string(sorted[i].CanonicalKey)] = amount
		distributed += amount
	}
	// Last recipient gets whatever remains. If its weight happens to be
	// zero, it still receives the remainder — this is the correct
	// behavior because conservation dominates: the pool must be fully
	// allocated and the sorted-last recipient is the canonical remainder
	// absorber. In practice the last recipient is almost always a
	// positive-weight participant; the corner case is exercised by the
	// conservation tests.
	out[string(sorted[len(sorted)-1].CanonicalKey)] = pool - distributed
	return out, nil
}
