// Package analytics provides protocol-level metrics and causal chain analysis.
//
// Layer: Infrastructure — importable by any layer.
package analytics

import (
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// EvidenceLookup resolves an evidence hash to its evidence packet.
type EvidenceLookup interface {
	GetEvidenceByHash(hash string) (*evidence.Evidence, error)
}

// ComputeVerificationDepth calculates the number of independent verification
// events in an evidence packet's causal ancestry.
//
// depth=1 means the output itself was verified (leaf task, no genesis chain).
// depth>1 means it consumed verified inputs, compounding trust.
//
// Uses a visited set for cycle protection and a cache for performance on
// deep/wide chains.
func ComputeVerificationDepth(evidenceHash string, lookup EvidenceLookup) (int, error) {
	cache := make(map[string]int)
	visited := make(map[string]bool)
	return computeDepth(evidenceHash, lookup, cache, visited)
}

func computeDepth(hash string, lookup EvidenceLookup, cache map[string]int, visited map[string]bool) (int, error) {
	if d, ok := cache[hash]; ok {
		return d, nil
	}
	if visited[hash] {
		return 0, fmt.Errorf("analytics: cycle detected in genesis chain at %s", hash)
	}
	visited[hash] = true

	ev, err := lookup.GetEvidenceByHash(hash)
	if err != nil {
		// Unknown evidence hash — treat as depth 1 (verified leaf).
		cache[hash] = 1
		return 1, nil
	}

	if len(ev.GenesisChain) == 0 {
		cache[hash] = 1
		return 1, nil
	}

	total := 0
	for _, parentHash := range ev.GenesisChain {
		parentDepth, err := computeDepth(parentHash, lookup, cache, visited)
		if err != nil {
			return 0, err
		}
		total += parentDepth
	}
	// +1 for this output's own verification.
	depth := total + 1
	cache[hash] = depth
	delete(visited, hash) // allow revisits from other branches
	return depth, nil
}
