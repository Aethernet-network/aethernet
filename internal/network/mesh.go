// mesh.go implements bounded mesh management for Fast Path header dissemination.
//
// Instead of relaying headers to ALL V2 peers, the MeshManager selects a
// bounded fanout set informed by peer scoring and diversity. This prevents
// flooding while ensuring reliable propagation.
//
// Selection strategy:
//  1. Exclude the origin peer (no echo).
//  2. Exclude overloaded peers (PeerStatus.Healthy == false or score < MinUsableScore).
//  3. Sort remaining V2 peers by score descending.
//  4. Take up to TargetFanout peers from the top (high-quality fast propagation).
//  5. If fewer than MinFanout, include lower-scored peers to meet the minimum.
//  6. Inject diversity: with probability DiversityRatio, replace one deterministic
//     slot with a random usable peer not already selected. This prevents eclipse
//     attacks where only the top-scored peers receive all traffic.
//
// The MeshManager is stateless per-relay — it re-evaluates on every call.
// Periodic rebalance hooks are a no-op for now (the per-relay evaluation
// is the rebalance). Future prompts may add cached topology state.
package network

import (
	"math/rand"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// MeshConfig holds tunable parameters for relay mesh selection.
type MeshConfig struct {
	// TargetFanout is the ideal number of peers to relay each header to.
	TargetFanout int

	// MinFanout is the minimum peers to relay to. If fewer than MinFanout
	// usable peers are available, all usable peers are used.
	MinFanout int

	// MaxFanout is the hard cap on relay targets per event.
	MaxFanout int

	// DiversityRatio is the probability [0.0, 1.0] that one deterministic
	// slot is replaced with a random usable peer. 0.0 = pure score ranking,
	// 1.0 = always inject one random peer.
	DiversityRatio float64
}

// DefaultMeshConfig returns production-ready defaults.
func DefaultMeshConfig() MeshConfig {
	return MeshConfig{
		TargetFanout:   6,
		MinFanout:      2,
		MaxFanout:      12,
		DiversityRatio: 0.3,
	}
}

// MeshManager selects bounded relay targets from the V2 peer set.
// Safe for concurrent use.
type MeshManager struct {
	config MeshConfig
	rng    *rand.Rand
	mu     sync.Mutex
}

// NewMeshManager creates a MeshManager with the given configuration.
func NewMeshManager(config MeshConfig) *MeshManager {
	return &MeshManager{
		config: config,
		rng:    rand.New(rand.NewSource(rand.Int63())),
	}
}

// SelectRelayTargets returns a bounded set of V2 peers to relay a header to.
// Excludes the origin peer, prefers higher-scored peers, injects diversity,
// and respects overload signals.
func (mm *MeshManager) SelectRelayTargets(peers map[crypto.AgentID]*Peer, exclude crypto.AgentID) []*Peer {
	// Collect eligible V2 peers.
	candidates := make([]*Peer, 0, len(peers))
	for _, p := range peers {
		if p.AgentID == exclude {
			continue
		}
		if !p.SupportsFastPath() {
			continue
		}
		// Skip overloaded peers.
		if status := p.LastStatus(); status != nil && !status.Healthy {
			continue
		}
		// Skip peers with unusable scores.
		if ps := p.PeerScore(); ps != nil && !ps.IsUsable() {
			continue
		}
		candidates = append(candidates, p)
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort by score descending.
	sortedPeers := PeersByScore(candidates)
	if len(sortedPeers) == 0 {
		// All candidates have nil scores — use unsorted candidates.
		sortedPeers = candidates
	}

	// Determine target count.
	target := mm.config.TargetFanout
	if target < mm.config.MinFanout {
		target = mm.config.MinFanout
	}
	if target > mm.config.MaxFanout {
		target = mm.config.MaxFanout
	}
	if target > len(sortedPeers) {
		target = len(sortedPeers)
	}

	// Select top-scored peers.
	selected := make([]*Peer, target)
	copy(selected, sortedPeers[:target])

	// Diversity injection: with configured probability, replace one
	// deterministic slot with a random usable peer not already selected.
	mm.mu.Lock()
	shouldDiversify := mm.rng.Float64() < mm.config.DiversityRatio
	var diversityIdx int
	if shouldDiversify && len(sortedPeers) > target {
		// Pick a random peer from the unselected pool.
		poolStart := target
		poolSize := len(sortedPeers) - poolStart
		randomPeer := sortedPeers[poolStart+mm.rng.Intn(poolSize)]

		// Replace the last deterministic slot (lowest-scored in the selection).
		diversityIdx = target - 1
		selected[diversityIdx] = randomPeer
	}
	mm.mu.Unlock()

	return selected
}

// FanoutCount returns the current target fanout (for metrics/logging).
func (mm *MeshManager) FanoutCount() int {
	return mm.config.TargetFanout
}
