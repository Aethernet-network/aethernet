// checkpoint.go implements checkpoint-based bootstrap for the Fast Path.
//
// New or long-disconnected V2 nodes bootstrap from checkpoint summaries
// plus bounded recent backfill, instead of relying on full DAG sync.
//
// Bootstrap flow:
//  1. New V2 peer connects and completes HelloV2 negotiation.
//  2. Peer requests a checkpoint summary from a high-scored peer.
//  3. Checkpoint contains: tips, max timestamp, event count, state hash.
//  4. Peer uses the checkpoint to identify its gap: events with
//     CausalTimestamp > local max timestamp.
//  5. Peer sends a bounded RepairRequest for the gap (window-based).
//  6. Peer joins the live header stream for ongoing events.
//
// The checkpoint is a signed snapshot of the DAG state at a point in time.
// It does NOT transfer events — it provides metadata for targeted repair.
//
// V1 peers continue using MsgRequestSync for full DAG sync.
package network

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// MaxBootstrapRepairEvents bounds the number of events in a single bootstrap
// repair response. Prevents unbounded state transfer during bootstrap.
const MaxBootstrapRepairEvents = 1024

// BuildCheckpoint generates a CheckpointSummary from the current DAG state.
// The state hash is the SHA-256 of sorted event IDs, providing a compact
// deterministic digest for state comparison.
func (n *Node) BuildCheckpoint() CheckpointSummary {
	tips := n.dag.Tips()
	eventIDs := n.dag.EventIDs()

	// Sort IDs for deterministic hash.
	sort.Slice(eventIDs, func(i, j int) bool {
		return eventIDs[i] < eventIDs[j]
	})

	h := sha256.New()
	for _, id := range eventIDs {
		h.Write([]byte(id))
	}
	stateHash := hex.EncodeToString(h.Sum(nil))

	var maxTS uint64
	for _, tip := range tips {
		if ev, err := n.dag.Get(tip); err == nil {
			if ev.CausalTimestamp > maxTS {
				maxTS = ev.CausalTimestamp
			}
		}
	}

	return CheckpointSummary{
		Timestamp:  maxTS,
		Tips:       tips,
		EventCount: uint64(n.dag.Size()),
		StateHash:  stateHash,
		NodeID:     string(n.config.AgentID),
	}
}

// SignCheckpoint signs a checkpoint summary with the node's keypair.
func (n *Node) SignCheckpoint(cp *CheckpointSummary) {
	if n.config.KeyPair == nil {
		return
	}
	data, err := json.Marshal(struct {
		Timestamp  uint64 `json:"timestamp"`
		StateHash  string `json:"state_hash"`
		EventCount uint64 `json:"event_count"`
		NodeID     string `json:"node_id"`
	}{
		Timestamp:  cp.Timestamp,
		StateHash:  cp.StateHash,
		EventCount: cp.EventCount,
		NodeID:     cp.NodeID,
	})
	if err != nil {
		return
	}
	sig, err := n.config.KeyPair.Sign(data)
	if err != nil {
		return
	}
	cp.Signature = sig
}

// handleCheckpointRequest processes an incoming MsgCheckpoint request.
// Responds with the node's current checkpoint summary.
func (n *Node) handleCheckpointRequest(peer *Peer, _ []byte) {
	cp := n.BuildCheckpoint()
	n.SignCheckpoint(&cp)

	payload, err := json.Marshal(cp)
	if err != nil {
		return
	}
	SafeSend(peer, Message{Type: MsgCheckpoint, Payload: payload})
}

// handleCheckpointResponse processes an incoming checkpoint from a peer.
// Computes the gap between local state and the checkpoint, then sends
// a bounded repair request to backfill missing events.
func (n *Node) handleCheckpointResponse(peer *Peer, payload []byte) {
	var cp CheckpointSummary
	if err := json.Unmarshal(payload, &cp); err != nil {
		return
	}

	localMaxTS := n.dag.MaxTimestamp()
	localCount := uint64(n.dag.Size())

	// If the remote has more events and a higher timestamp, we need backfill.
	if cp.EventCount <= localCount && cp.Timestamp <= localMaxTS {
		slog.Debug("checkpoint: local state is current, no backfill needed",
			"local_count", localCount, "remote_count", cp.EventCount)
		return
	}

	// Compute the gap: events with timestamp > our max.
	gap := cp.Timestamp - localMaxTS
	if gap == 0 {
		return
	}

	// Send a bounded window-based repair request for the gap.
	req := RepairRequest{
		FromTimestamp: localMaxTS + 1,
		ToTimestamp:   cp.Timestamp,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return
	}
	SafeSend(peer, Message{Type: MsgRepairRequest, Payload: data})

	slog.Info("checkpoint: bootstrap backfill requested",
		"peer", peer.AgentID,
		"local_ts", localMaxTS, "remote_ts", cp.Timestamp,
		"local_count", localCount, "remote_count", cp.EventCount,
		"gap_events", cp.EventCount-localCount)
}

// RequestBootstrap sends a checkpoint request to the best-scored V2 peer
// to initiate the bootstrap flow. Called after a V2 peer connection is
// established and HelloV2 is negotiated.
func (n *Node) RequestBootstrap() {
	n.mu.RLock()
	best := BestPeerFor(n.peers, n.config.AgentID)
	n.mu.RUnlock()

	if best == nil {
		slog.Debug("checkpoint: no V2 peer available for bootstrap")
		return
	}

	// Request checkpoint from the best peer.
	SafeSend(best, Message{Type: MsgCheckpoint})

	slog.Info("checkpoint: bootstrap checkpoint requested",
		"peer", best.AgentID)
}

// BootstrapGap computes the gap between local DAG state and a checkpoint.
// Returns the number of events and the timestamp range needed.
func BootstrapGap(localMaxTS, localCount uint64, cp CheckpointSummary) (gapEvents uint64, fromTS, toTS uint64) {
	if cp.EventCount <= localCount && cp.Timestamp <= localMaxTS {
		return 0, 0, 0
	}
	gapEvents = cp.EventCount - localCount
	fromTS = localMaxTS + 1
	toTS = cp.Timestamp
	return
}

// ── DAG Helper: Bounded Frontier Digest ──────────────────────────────────────

// BuildWindowDigest generates a WindowDigest covering events in the given
// timestamp range. Used for checkpoint-based state comparison.
func (n *Node) BuildWindowDigest(fromTS, toTS uint64) WindowDigest {
	recent := n.dag.RecentEvents(fromTS, MaxRepairIDs)
	ids := make([]event.EventID, 0, len(recent))
	for _, ev := range recent {
		if ev.CausalTimestamp <= toTS {
			ids = append(ids, ev.ID)
		}
	}
	return WindowDigest{
		FromTimestamp: fromTS,
		ToTimestamp:   toTS,
		EventIDs:     ids,
	}
}

// ── Checkpoint Verification ──────────────────────────────────────────────────

// VerifyCheckpoint validates a checkpoint's signature using the sender's
// public key. Returns true if the signature is valid or if no signature
// is present (unsigned checkpoints are accepted from trusted peers).
func VerifyCheckpoint(cp *CheckpointSummary) bool {
	if len(cp.Signature) == 0 {
		return true // unsigned — accepted for backward compat
	}

	pubBytes, err := hex.DecodeString(cp.NodeID)
	if err != nil || len(pubBytes) != 32 {
		return false
	}

	data, err := json.Marshal(struct {
		Timestamp  uint64 `json:"timestamp"`
		StateHash  string `json:"state_hash"`
		EventCount uint64 `json:"event_count"`
		NodeID     string `json:"node_id"`
	}{
		Timestamp:  cp.Timestamp,
		StateHash:  cp.StateHash,
		EventCount: cp.EventCount,
		NodeID:     cp.NodeID,
	})
	if err != nil {
		return false
	}

	return crypto.Verify(pubBytes, data, cp.Signature)
}
