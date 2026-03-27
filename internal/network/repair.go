// repair.go implements bounded missing-parent repair for the Fast Path.
//
// When materialization fails because a parent event is not yet in the DAG,
// the repair system requests only the specific missing events rather than
// falling back to full DAG sync. Requests are bounded by MaxRepairIDs to
// prevent a malicious peer from triggering unbounded state transfer.
//
// Flow:
//  1. materializeEvent detects ErrMissingCausalRef → identifies missing parents
//  2. Tracking entry records MissingParents set
//  3. repairWorker sends targeted RepairRequest to the source peer
//  4. Source peer responds with RepairResponse (full events, bounded)
//  5. Received events are fed through the standard fast-path or legacy path
//  6. Once parents materialize, the blocked child is retried
package network

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// repairQSize is the capacity of the repair request queue.
const repairQSize = 1024

// repairQ returns the repair queue channel. Created lazily if needed.
func (im *IngestManager) repairQ() chan event.EventID {
	return im.repairCh
}

// EnqueueRepair adds an event to the repair queue. Non-blocking.
func (im *IngestManager) EnqueueRepair(id event.EventID) bool {
	select {
	case im.repairCh <- id:
		return true
	default:
		return false
	}
}

// SetMissingParents records which causal refs are missing for an event.
func (im *IngestManager) SetMissingParents(id event.EventID, missing map[event.EventID]struct{}) {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok {
		return
	}
	t.MissingParents = missing
}

// MarkRepairRequested prevents duplicate repair requests for an event.
func (im *IngestManager) MarkRepairRequested(id event.EventID) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok || t.RepairRequested {
		return false
	}
	t.RepairRequested = true
	return true
}

// GetMissingParents returns the set of missing parent IDs for an event.
func (im *IngestManager) GetMissingParents(id event.EventID) []event.EventID {
	im.mu.RLock()
	defer im.mu.RUnlock()
	t, ok := im.tracked[id]
	if !ok || t.MissingParents == nil {
		return nil
	}
	ids := make([]event.EventID, 0, len(t.MissingParents))
	for pid := range t.MissingParents {
		ids = append(ids, pid)
	}
	return ids
}

// PendingRepairChildren returns event IDs that are blocked waiting for a
// specific parent to materialize. Used to retry children after parent arrives.
func (im *IngestManager) PendingRepairChildren(parentID event.EventID) []event.EventID {
	im.mu.RLock()
	defer im.mu.RUnlock()
	var children []event.EventID
	for id, t := range im.tracked {
		if t.MissingParents == nil {
			continue
		}
		if _, needs := t.MissingParents[parentID]; needs {
			children = append(children, id)
		}
	}
	return children
}

// ClearMissingParent removes a parent from an event's MissingParents set.
// Returns true if the set is now empty (all parents resolved).
func (im *IngestManager) ClearMissingParent(id, parentID event.EventID) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok || t.MissingParents == nil {
		return false
	}
	delete(t.MissingParents, parentID)
	return len(t.MissingParents) == 0
}

// ── Repair Worker ────────────────────────────────────────────────────────────

// repairWorker drains the repair queue and sends targeted RepairRequests
// to the source peer of each blocked event. Runs as a goroutine.
func (n *Node) repairWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-n.ingest.repairQ():
			if !ok {
				return
			}
			n.requestRepair(id)
		}
	}
}

// requestRepair sends a targeted RepairRequest for the missing parents of
// an event to the source peer.
func (n *Node) requestRepair(id event.EventID) {
	if !n.ingest.MarkRepairRequested(id) {
		return // already requested
	}

	missing := n.ingest.GetMissingParents(id)
	if len(missing) == 0 {
		return
	}

	// Bound the request size.
	if len(missing) > MaxRepairIDs {
		missing = missing[:MaxRepairIDs]
	}

	tracking := n.ingest.GetTracking(id)
	if tracking == nil {
		return
	}

	// Send to source peer first.
	n.mu.RLock()
	source, ok := n.peers[tracking.SourcePeer]
	n.mu.RUnlock()

	if ok && source.SupportsFastPath() {
		req := RepairRequest{MissingIDs: missing}
		payload, err := json.Marshal(req)
		if err != nil {
			return
		}
		SafeSend(source, Message{Type: MsgRepairRequest, Payload: payload})
		slog.Debug("repair: sent targeted request",
			"event_id", id, "missing", len(missing), "peer", tracking.SourcePeer)
		return
	}

	// Fallback: try any V2 peer.
	n.mu.RLock()
	var fallback *Peer
	for _, p := range n.peers {
		if p.SupportsFastPath() && p.AgentID != tracking.SourcePeer {
			fallback = p
			break
		}
	}
	n.mu.RUnlock()

	if fallback != nil {
		req := RepairRequest{MissingIDs: missing}
		payload, _ := json.Marshal(req)
		SafeSend(fallback, Message{Type: MsgRepairRequest, Payload: payload})
		slog.Debug("repair: sent targeted request to fallback peer",
			"event_id", id, "missing", len(missing), "peer", fallback.AgentID)
	}
}

// ── Request/Response Handlers ────────────────────────────────────────────────

// handleRepairRequest processes an incoming MsgRepairRequest from a peer.
// Responds with the requested events from the local DAG, bounded by MaxRepairIDs.
func (n *Node) handleRepairRequest(peer *Peer, payload []byte) {
	var req RepairRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// Bound the response.
	if len(req.MissingIDs) > MaxRepairIDs {
		req.MissingIDs = req.MissingIDs[:MaxRepairIDs]
	}

	var events []*event.Event
	truncated := false

	if len(req.MissingIDs) > 0 {
		for _, id := range req.MissingIDs {
			if len(events) >= MaxRepairIDs {
				truncated = true
				break
			}
			if ev, err := n.dag.Get(id); err == nil {
				events = append(events, ev)
			}
		}
	} else if req.FromTimestamp > 0 && req.ToTimestamp > 0 {
		// Window-based repair: collect events in the timestamp range.
		all := n.dag.All()
		sort.Slice(all, func(i, j int) bool {
			return all[i].CausalTimestamp < all[j].CausalTimestamp
		})
		for _, ev := range all {
			if ev.CausalTimestamp >= req.FromTimestamp && ev.CausalTimestamp <= req.ToTimestamp {
				events = append(events, ev)
				if len(events) >= MaxRepairIDs {
					truncated = true
					break
				}
			}
		}
	}

	if len(events) == 0 {
		return
	}

	resp := RepairResponse{Events: events, Truncated: truncated}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	SafeSend(peer, Message{Type: MsgRepairResponse, Payload: data})
}

// handleRepairResponse processes an incoming MsgRepairResponse from a peer.
// Feeds received events through the standard dag.Add path (with signature
// verification and causal ref checks) and retries any children that were
// blocked waiting for these parents.
func (n *Node) handleRepairResponse(peer *Peer, payload []byte) {
	var resp RepairResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return
	}

	// Sort by CausalTimestamp so parents are added before children.
	sort.Slice(resp.Events, func(i, j int) bool {
		return resp.Events[i].CausalTimestamp < resp.Events[j].CausalTimestamp
	})

	n.mu.RLock()
	sh := n.syncHandler
	n.mu.RUnlock()

	for _, ev := range resp.Events {
		if !crypto.VerifyEvent(ev) {
			slog.Warn("repair: dropping event with invalid signature",
				"event_id", ev.ID, "peer", peer.AgentID)
			continue
		}

		if err := n.dag.Add(ev); err != nil {
			if !errors.Is(err, dag.ErrDuplicateEvent) {
				slog.Debug("repair: dag.Add failed",
					"event_id", ev.ID, "err", err)
			}
			continue
		}

		if sh != nil {
			sh(ev)
		}

		// Check if any tracked events were waiting for this parent.
		if n.ingest != nil {
			n.retryBlockedChildren(ev.ID)
		}
	}
}

// retryBlockedChildren re-enqueues events that were blocked waiting for
// parentID to materialize. Called after a repair response adds the parent.
func (n *Node) retryBlockedChildren(parentID event.EventID) {
	children := n.ingest.PendingRepairChildren(parentID)
	for _, childID := range children {
		allResolved := n.ingest.ClearMissingParent(childID, parentID)
		if allResolved {
			// All parents are now in the DAG — retry materialization.
			tr := n.ingest.GetTracking(childID)
			if tr != nil && tr.Stage == StageValidated && tr.Reconstructed != nil {
				select {
				case n.ingest.materializeQ <- childID:
				default:
				}
			}
		}
	}
}

// ── Frontier Summary ─────────────────────────────────────────────────────────

// BuildFrontierSummary generates a FrontierSummary from the current DAG state.
func (n *Node) BuildFrontierSummary() FrontierSummary {
	tips := n.dag.Tips()
	var maxTS uint64
	for _, tip := range tips {
		if ev, err := n.dag.Get(tip); err == nil {
			if ev.CausalTimestamp > maxTS {
				maxTS = ev.CausalTimestamp
			}
		}
	}
	return FrontierSummary{
		Tips:         tips,
		MaxTimestamp: maxTS,
		EventCount:   uint64(n.dag.Size()),
	}
}
