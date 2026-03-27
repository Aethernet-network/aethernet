// merkle.go implements deterministic Merkle root computation over trajectory
// commit EventIDs for evidence anchoring.
//
// The exploration root is a compact cryptographic summary of all trajectory
// commits associated with a task. It is included in evidence packets to
// anchor the exploration tree that led to the submitted work.
package trajectory

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// MaxExplorationSample is the maximum number of commit IDs included in
// the ExplorationSample field of an evidence packet.
const MaxExplorationSample = 10

// ComputeExplorationRoot computes a deterministic Merkle root over a set
// of trajectory commit EventIDs.
//
// The algorithm:
//  1. Sort event IDs lexicographically (stable, order-insensitive input).
//  2. Hash each ID to produce leaf nodes: SHA-256(id_bytes).
//  3. Iteratively pair and hash until a single root remains.
//  4. Return the hex-encoded root hash.
//
// Empty set produces an empty string (no root).
// Single element produces the hash of that element.
func ComputeExplorationRoot(eventIDs []string) (string, error) {
	if len(eventIDs) == 0 {
		return "", nil
	}

	// Stable sort for determinism regardless of input order.
	sorted := make([]string, len(eventIDs))
	copy(sorted, eventIDs)
	sort.Strings(sorted)

	// Compute leaf hashes.
	hashes := make([][]byte, len(sorted))
	for i, id := range sorted {
		h := sha256.Sum256([]byte(id))
		hashes[i] = h[:]
	}

	// Iterative Merkle tree construction.
	for len(hashes) > 1 {
		var next [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				// Pair: hash(left || right)
				combined := append(hashes[i], hashes[i+1]...)
				h := sha256.Sum256(combined)
				next = append(next, h[:])
			} else {
				// Odd element: promote unchanged.
				next = append(next, hashes[i])
			}
		}
		hashes = next
	}

	return hex.EncodeToString(hashes[0]), nil
}

// SampleExplorationCommits returns a deterministic bounded sample of
// trajectory commit EventIDs for inclusion in evidence packets.
//
// The sample is taken from the sorted list: first MaxExplorationSample
// elements. This is deterministic and reproducible.
func SampleExplorationCommits(eventIDs []string, max int) []string {
	if max <= 0 || max > MaxExplorationSample {
		max = MaxExplorationSample
	}

	sorted := make([]string, len(eventIDs))
	copy(sorted, eventIDs)
	sort.Strings(sorted)

	if len(sorted) <= max {
		return sorted
	}
	return sorted[:max]
}
