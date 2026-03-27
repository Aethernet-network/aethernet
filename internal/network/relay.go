// relay.go implements the Fast Path header relay worker.
//
// The relay worker drains the IngestManager's relayQ and sends EventHeader
// messages to eligible V2 peers. Relay happens from Announced or Completed
// state — BEFORE validation and materialization. This is the core performance
// path: new event awareness propagates across the V2 mesh without waiting for
// dag.Add or legacy sync ticks.
//
// Peer selection: all V2 peers except the origin peer of the event.
// V1 peers never receive V2 header messages (enforced by SafeSend).
// Full mesh topology and scoring come in a later prompt.
package network

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// relayWorker drains the ingest manager's relayQ and sends EventHeader
// messages to eligible V2 peers. Runs as a goroutine started by Node.Start.
func (n *Node) relayWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-n.ingest.RelayQ():
			if !ok {
				return
			}
			n.relayHeader(id)
		}
	}
}

// relayHeader sends the EventHeader for the given event to all eligible peers.
func (n *Node) relayHeader(id event.EventID) {
	tracking := n.ingest.GetTracking(id)
	if tracking == nil {
		return // evicted or removed between enqueue and relay
	}

	payload, err := json.Marshal(tracking.Header)
	if err != nil {
		slog.Warn("network: relay marshal failed", "event_id", id, "err", err)
		return
	}
	msg := Message{Type: MsgEventHeader, Payload: payload}

	peers := n.v2PeersExcept(tracking.SourcePeer)
	relayed := 0
	for _, p := range peers {
		if SafeSend(p, msg) {
			relayed++
		}
	}

	tracking.RelayCount = relayed
	if relayed > 0 {
		slog.Debug("network: relayed header",
			"event_id", id, "peers", relayed, "source", tracking.SourcePeer)
	}
}

// v2PeersExcept returns all connected V2 peers except the one with the given
// AgentID. Used to avoid relaying a header back to its source.
func (n *Node) v2PeersExcept(exclude crypto.AgentID) []*Peer {
	n.mu.RLock()
	defer n.mu.RUnlock()

	peers := make([]*Peer, 0, len(n.peers))
	for _, p := range n.peers {
		if p.AgentID == exclude {
			continue
		}
		if p.SupportsFastPath() {
			peers = append(peers, p)
		}
	}
	return peers
}
