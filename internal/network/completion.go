// completion.go implements receiver-driven body fetch for the Fast Path.
//
// After a node receives an EventHeader (Announced state), it decides whether
// to fetch the body. Bodies are requested from the source peer and verified
// against the BodyCommitment in the header before advancing to Completed.
//
// This separates header awareness (fast relay) from body availability
// (needed for validation and materialization). Bodies are no longer
// mandatory on the initial hot-path relay.
package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// Body completion errors.
var (
	ErrBodyCommitmentMismatch = errors.New("completion: body commitment mismatch")
	ErrEventNotTracked        = errors.New("completion: event not tracked")
	ErrBodyAlreadyAvailable   = errors.New("completion: body already available")
)

// NeedBody returns true if the event needs a body fetch: it is tracked,
// at Announced stage, and the body has not yet been requested or received.
func (im *IngestManager) NeedBody(id event.EventID) bool {
	im.mu.RLock()
	defer im.mu.RUnlock()
	t, ok := im.tracked[id]
	if !ok {
		return false
	}
	return t.Stage == StageAnnounced && !t.BodyAvailable && !t.BodyRequested
}

// MarkBodyRequested records that a body fetch has been initiated for this
// event. Prevents duplicate requests. Returns false if the event is not
// tracked or the body is already available/requested.
func (im *IngestManager) MarkBodyRequested(id event.EventID) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok || t.BodyRequested || t.BodyAvailable {
		return false
	}
	t.BodyRequested = true
	return true
}

// CompleteBody verifies the body payload against the header's BodyCommitment,
// stores it on the tracking entry, and advances the event to Completed.
// Returns an error if the commitment does not match or the event is not tracked.
func (im *IngestManager) CompleteBody(id event.EventID, payload json.RawMessage) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	t, ok := im.tracked[id]
	if !ok {
		return ErrEventNotTracked
	}
	if t.BodyAvailable {
		return ErrBodyAlreadyAvailable
	}

	// Verify body commitment.
	data := payload
	if data == nil {
		data = json.RawMessage("null")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != t.Header.BodyCommitment {
		slog.Warn("completion: body commitment mismatch",
			"event_id", id, "expected", t.Header.BodyCommitment, "got", got)
		return ErrBodyCommitmentMismatch
	}

	t.Body = payload
	t.BodyAvailable = true
	t.AdvanceTo(StageCompleted)

	// Non-blocking enqueue to completeQ for the validation stage.
	select {
	case im.completeQ <- id:
	default:
	}

	return nil
}

// SetBodyAvailable marks the body as locally available without verification.
// Used for locally-created events where the body is available by construction.
func (im *IngestManager) SetBodyAvailable(id event.EventID, payload json.RawMessage) {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok {
		return
	}
	t.Body = payload
	t.BodyAvailable = true
}

// completionWorker drains announceQ and initiates body fetches for events
// that need bodies. Runs as a goroutine started by Node.Start.
func (n *Node) completionWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-n.ingest.AnnounceQ():
			if !ok {
				return
			}
			n.maybeRequestBody(id)
		}
	}
}

// maybeRequestBody checks if an event needs a body and sends a request to
// the source peer if so.
func (n *Node) maybeRequestBody(id event.EventID) {
	if !n.ingest.NeedBody(id) {
		return
	}
	if !n.ingest.MarkBodyRequested(id) {
		return
	}

	tracking := n.ingest.GetTracking(id)
	if tracking == nil {
		return
	}

	// Request body from the source peer.
	n.mu.RLock()
	source, ok := n.peers[tracking.SourcePeer]
	n.mu.RUnlock()
	if !ok {
		slog.Debug("completion: source peer not connected", "event_id", id, "source", tracking.SourcePeer)
		return
	}

	ref := BodyRef{
		EventID:        id,
		BodyCommitment: tracking.Header.BodyCommitment,
	}
	payload, err := json.Marshal(ref)
	if err != nil {
		return
	}
	SafeSend(source, Message{Type: MsgBodyRequest, Payload: payload})
}

// handleBodyRequest processes an incoming MsgBodyRequest from a peer.
// Looks up the event in the local DAG and responds with the body payload.
func (n *Node) handleBodyRequest(peer *Peer, payload []byte) {
	var ref BodyRef
	if err := json.Unmarshal(payload, &ref); err != nil {
		return
	}

	// Look up in the local DAG (materialized events have full bodies).
	ev, err := n.dag.Get(ref.EventID)
	if err != nil {
		// Not in DAG — check ingest tracking for locally available bodies.
		if n.ingest != nil {
			tr := n.ingest.GetTracking(ref.EventID)
			if tr != nil && tr.BodyAvailable && tr.Body != nil {
				body := EventBody{EventID: ref.EventID, Payload: tr.Body}
				data, _ := json.Marshal(body)
				SafeSend(peer, Message{Type: MsgEventBody, Payload: data})
				return
			}
		}
		return
	}

	body := EventBody{EventID: ref.EventID, Payload: ev.Payload}
	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	SafeSend(peer, Message{Type: MsgEventBody, Payload: data})
}

// handleBodyResponse processes an incoming MsgEventBody from a peer.
// Verifies the body commitment and advances the event to Completed.
func (n *Node) handleBodyResponse(peer *Peer, payload []byte) {
	var body EventBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return
	}

	if n.ingest == nil {
		return
	}

	if err := n.ingest.CompleteBody(body.EventID, body.Payload); err != nil {
		if errors.Is(err, ErrBodyCommitmentMismatch) {
			slog.Warn("completion: body from peer failed commitment check",
				"event_id", body.EventID, "peer", peer.AgentID)
		}
	}
}
