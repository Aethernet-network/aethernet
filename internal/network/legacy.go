// legacy.go isolates legacy V1 sync compatibility behavior for the Fast Path.
//
// During rolling upgrade, V2 nodes must continue serving V1 peers via
// MsgRequestSync/MsgSyncBatch. This file documents the boundary between
// legacy and fast-path behavior and provides helpers for the transition.
//
// After all peers are upgraded to V2, EnableLegacySync can be set to false
// and this code path becomes dormant — MsgRequestSync is only sent to V1
// peers, and MsgSyncBatch responses are only generated for incoming requests.
package network

import (
	"log/slog"
)

// NegotiateV2 sends HelloV2 to a peer after the V1 handshake completes.
// If the peer supports V2, it will respond with its own HelloV2, which is
// handled by the MsgHelloV2 case in handleMessage. If the peer does not
// respond (V1-only), the connection remains in V1 mode.
//
// Called from Connect and handleIncomingConn after the V1 handshake succeeds.
func (n *Node) NegotiateV2(peer *Peer) {
	if !n.config.EnableV2 {
		return
	}

	hello := BuildHelloV2(n.dag.Tips())
	if err := SendHelloV2(peer, hello); err != nil {
		slog.Debug("legacy: failed to send HelloV2",
			"peer", peer.AgentID, "err", err)
		return
	}

	slog.Debug("legacy: sent HelloV2 to peer",
		"peer", peer.AgentID, "features", hello.Features)
}

// handleHelloV2Message processes an incoming MsgHelloV2 from a peer.
// On success, the peer is upgraded to V2 and bootstrap is initiated.
func (n *Node) handleHelloV2Message(peer *Peer, payload []byte) {
	if !HandleHelloV2(peer, payload) {
		return
	}

	// Initialize quota for the new V2 peer.
	peer.EnsureQuota(DefaultQuotaConfig())

	// Request checkpoint-based bootstrap from this peer if we have a gap.
	if n.config.EnableV2 {
		n.RequestBootstrap()
	}
}

// IsLegacyPeer returns true if the peer should use legacy sync.
// A peer is legacy if it has not completed V2 negotiation.
func IsLegacyPeer(p *Peer) bool {
	return p.IsLegacySyncOnly()
}

// LegacySyncTargets returns the subset of connected peers that should
// receive periodic MsgRequestSync messages (V1 peers only, unless
// EnableLegacySync forces all peers to use legacy sync).
func (n *Node) LegacySyncTargets() []*Peer {
	n.mu.RLock()
	defer n.mu.RUnlock()

	peers := make([]*Peer, 0, len(n.peers))
	for _, p := range n.peers {
		if n.config.EnableLegacySync || p.IsLegacySyncOnly() {
			peers = append(peers, p)
		}
	}
	return peers
}

// V2Peers returns the subset of connected peers that have completed
// V2 negotiation and should use the fast-path pipeline.
func (n *Node) V2Peers() []*Peer {
	n.mu.RLock()
	defer n.mu.RUnlock()

	peers := make([]*Peer, 0, len(n.peers))
	for _, p := range n.peers {
		if p.SupportsFastPath() {
			peers = append(peers, p)
		}
	}
	return peers
}

// PeerCounts returns the number of V1 and V2 connected peers.
func (n *Node) PeerCounts() (v1, v2 int) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, p := range n.peers {
		if p.SupportsFastPath() {
			v2++
		} else {
			v1++
		}
	}
	return
}

// ShouldFallbackToLegacy returns true if the node should fall back to
// legacy full DAG sync for V2 peers. This occurs when:
//   - LegacySyncFallbackAllowed is true AND
//   - The fast-path pipeline is severely degraded (all queues near capacity)
func (n *Node) ShouldFallbackToLegacy() bool {
	if !n.config.LegacySyncFallbackAllowed {
		return false
	}
	if n.ingest == nil {
		return false
	}
	// Fallback if the ingest pipeline is at >90% capacity.
	return n.ingest.MaxQueueDepth() > int(float64(n.ingest.config.MaxAnnounceQueueSize)*0.9)
}

