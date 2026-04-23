// completion.go implements receiver-driven body fetch for the Fast Path.
//
// After a node receives an EventHeader (Announced state), it decides whether
// to fetch the body. Bodies are requested from the source peer and verified
// against the BodyCommitment in the header before advancing to Completed.
//
// This separates header awareness (fast relay) from body availability
// (needed for validation and materialization). Bodies are no longer
// mandatory on the initial hot-path relay.
//
// TRAJECTORY COMMIT POLICY: For EventTypeTrajectoryCommit events, the Fast
// Path EventBody is the lean TrajectoryCommitPayload JSON — NOT the large
// CheckpointBody blob. Checkpoint bodies are external content-addressed
// artifacts stored in the blobstore and retrieved by the trajectory service
// layer, not by the Fast Path completion pipeline. This separation is
// architectural: the Fast Path handles event payload propagation; blob
// retrieval is an application-layer concern.
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
		slog.Warn("fastpath: body commitment mismatch — rejecting body",
			"event_id", id,
			"expected", t.Header.BodyCommitment,
			"got", got,
			"source_peer", t.SourcePeer,
			"reason", "commitment_mismatch",
		)
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

	// Request body from the source peer, or the best-scored fallback.
	n.mu.RLock()
	source, ok := n.peers[tracking.SourcePeer]
	if !ok || (source.PeerScore() != nil && !source.PeerScore().IsUsable()) {
		source = BestPeerFor(n.peers, tracking.SourcePeer)
		ok = source != nil
	}
	n.mu.RUnlock()
	if !ok {
		slog.Warn("fastpath: body fetch failed — no usable peer",
			"event_id", id,
			"source_peer", tracking.SourcePeer,
			"reason", "no_usable_peer",
		)
		return
	}

	slog.Info("fastpath: body requested",
		"event_id", id,
		"fetch_peer", source.AgentID,
		"source_peer", tracking.SourcePeer,
	)

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
// For TaskSubmitted events with an EvidenceBodyHash, attaches the blob as
// a sidecar if it is locally available and within MaxInlineFastPathBlobBytes.
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

	// Attach evidence blob sidecar for TaskSubmitted events.
	body.AttachedBlobs = n.collectAttachedBlobs(ev)

	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	SafeSend(peer, Message{Type: MsgEventBody, Payload: data})
}

// collectAttachedBlobs returns blobs referenced by the event that are locally
// available and within MaxInlineFastPathBlobBytes. Currently supports
// TaskSubmitted events with an EvidenceBodyHash.
//
// Must NOT be called while holding n.mu — blobstore reads must not occur
// under broad locks.
func (n *Node) collectAttachedBlobs(ev *event.Event) map[string][]byte {
	n.mu.RLock()
	bs := n.blobStore
	n.mu.RUnlock()
	if bs == nil {
		return nil
	}

	if ev.Type != event.EventTypeTaskSubmitted {
		return nil
	}

	tp, err := event.GetPayload[event.TaskSubmittedPayload](ev)
	if err != nil || tp.EvidenceBodyHash == "" {
		return nil
	}

	data, err := bs.Get(context.Background(), tp.EvidenceBodyHash)
	if err != nil || len(data) == 0 {
		return nil
	}

	if len(data) > MaxInlineFastPathBlobBytes {
		slog.Debug("fastpath: blob too large for inline sidecar",
			"event_id", ev.ID,
			"hash", tp.EvidenceBodyHash,
			"size", len(data),
			"max", MaxInlineFastPathBlobBytes,
		)
		return nil
	}

	return map[string][]byte{tp.EvidenceBodyHash: data}
}

// handleBodyResponse processes an incoming MsgEventBody from a peer.
// Verifies the body commitment and advances the event to Completed.
// If attached blobs are present, verifies each against its content hash
// and stores valid ones in the blobstore before completing the body.
// If a referenced blob is missing after completion, triggers a fallback
// blob fetch from the source peer.
func (n *Node) handleBodyResponse(peer *Peer, payload []byte) {
	var body EventBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return
	}

	if n.ingest == nil {
		return
	}

	// Verify and store attached blobs BEFORE completing the body, so the
	// blobstore has the evidence blob available when downstream processing
	// (autovalidator) runs.
	if len(body.AttachedBlobs) > 0 {
		n.processAttachedBlobs(peer, body.EventID, body.AttachedBlobs)
	}

	if err := n.ingest.CompleteBody(body.EventID, body.Payload); err != nil {
		if errors.Is(err, ErrBodyCommitmentMismatch) {
			slog.Warn("completion: body from peer failed commitment check",
				"event_id", body.EventID, "peer", peer.AgentID)
			if ps := peer.PeerScore(); ps != nil {
				ps.RecordInvalidBody()
			}
		}
	} else {
		if ps := peer.EnsureScore(); ps != nil {
			ps.RecordValidBody()
		}

		// After successful body completion, check if this event references
		// a blob that wasn't included in the sidecar. If so, trigger a
		// fallback blob fetch.
		n.maybeRequestBlob(body.EventID, body.Payload, peer)
	}
}

// processAttachedBlobs verifies each attached blob against its content hash
// and stores valid ones in the local blobstore. Corrupt blobs are logged and
// rejected without affecting event completion.
func (n *Node) processAttachedBlobs(peer *Peer, eventID event.EventID, blobs map[string][]byte) {
	n.mu.RLock()
	bs := n.blobStore
	n.mu.RUnlock()
	if bs == nil {
		return
	}

	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for hash, data := range blobs {
		// Verify content hash. Evidence body hashes are bare hex SHA-256
		// (no "sha256:" prefix), matching blobstore.FSStore.Put output.
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != hash {
			slog.Warn("fastpath: attached blob hash mismatch — rejecting",
				"event_id", eventID,
				"expected_hash", hash,
				"computed_hash", got,
				"peer", peer.AgentID,
				"size", len(data),
			)
			if ps := peer.PeerScore(); ps != nil {
				ps.RecordInvalidBody()
			}
			continue
		}

		if _, _, err := bs.Put(context.Background(), data); err != nil {
			slog.Warn("fastpath: failed to store attached blob",
				"event_id", eventID,
				"hash", hash,
				"err", err,
			)
			continue
		}

		slog.Info("fastpath: stored attached evidence blob",
			"event_id", eventID,
			"hash", hash,
			"size", len(data),
			"peer", peer.AgentID,
		)
	}
}

// ── Blob Fallback Fetch ─────────────────────────────────────────────────────

// maybeRequestBlob checks whether the event body references a blob (e.g.
// EvidenceBodyHash for TaskSubmitted) that is not yet in the local blobstore.
// If so, records the need on the tracking entry and sends a MsgBlobRequest
// to the peer that provided the body.
//
// This is the fallback path for blobs that were:
//   - too large for inline sidecar (> MaxInlineFastPathBlobBytes)
//   - missing on the sender during body request handling
//   - failed content-hash verification as an inline attachment
func (n *Node) maybeRequestBlob(eventID event.EventID, bodyPayload json.RawMessage, sourcePeer *Peer) {
	// Only TaskSubmitted events reference blobs currently.
	var raw struct {
		Type string `json:"type"`
	}
	// The body is the event payload. We need to check if the event type is
	// TaskSubmitted — get it from the tracking header.
	if n.ingest == nil {
		return
	}
	tr := n.ingest.GetTracking(eventID)
	if tr == nil {
		return
	}
	if tr.Header.Type != event.EventTypeTaskSubmitted {
		return
	}
	_ = raw // suppress unused

	// Parse the body to extract EvidenceBodyHash.
	var tp event.TaskSubmittedPayload
	if err := json.Unmarshal(bodyPayload, &tp); err != nil || tp.EvidenceBodyHash == "" {
		return
	}

	// Check if we already have the blob locally.
	n.mu.RLock()
	bs := n.blobStore
	n.mu.RUnlock()
	if bs != nil {
		if existing, err := bs.Get(context.Background(), tp.EvidenceBodyHash); err == nil && len(existing) > 0 {
			return // blob already available
		}
	}

	// Mark blob as requested on the tracking entry (dedup).
	n.ingest.MarkBlobRequested(eventID, tp.EvidenceBodyHash)

	// Send MsgBlobRequest to the source peer.
	req := BlobRequest{
		Hash:    tp.EvidenceBodyHash,
		EventID: eventID,
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return
	}
	SafeSend(sourcePeer, Message{Type: MsgBlobRequest, Payload: payload})
	slog.Info("fastpath: blob fallback request sent",
		"event_id", eventID,
		"hash", tp.EvidenceBodyHash,
		"peer", sourcePeer.AgentID,
	)
}

// handleBlobRequest processes an incoming MsgBlobRequest from a peer.
// Looks up the blob in the local blobstore and responds with MsgBlobResponse.
// Bounded by MaxBlobRequestsPerPeer — excess requests are silently dropped.
func (n *Node) handleBlobRequest(peer *Peer, payload []byte) {
	var req BlobRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	if req.Hash == "" {
		return
	}

	n.mu.RLock()
	bs := n.blobStore
	n.mu.RUnlock()
	if bs == nil {
		return
	}

	data, err := bs.Get(context.Background(), req.Hash)
	if err != nil || len(data) == 0 {
		slog.Debug("fastpath: blob request — blob not found locally",
			"hash", req.Hash,
			"event_id", req.EventID,
			"peer", peer.AgentID,
		)
		return
	}

	resp := BlobResponse{Hash: req.Hash, Data: data}
	respPayload, err := json.Marshal(resp)
	if err != nil {
		return
	}
	SafeSend(peer, Message{Type: MsgBlobResponse, Payload: respPayload})
	slog.Info("fastpath: blob response sent",
		"hash", req.Hash,
		"size", len(data),
		"peer", peer.AgentID,
	)
}

// handleBlobResponse processes an incoming MsgBlobResponse from a peer.
// Verifies the content hash and stores the blob in the local blobstore.
func (n *Node) handleBlobResponse(peer *Peer, payload []byte) {
	var resp BlobResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return
	}

	if resp.Hash == "" || len(resp.Data) == 0 {
		return
	}

	// Verify content hash before storing.
	sum := sha256.Sum256(resp.Data)
	got := hex.EncodeToString(sum[:])
	if got != resp.Hash {
		slog.Warn("fastpath: blob fallback response hash mismatch — rejecting",
			"expected_hash", resp.Hash,
			"computed_hash", got,
			"peer", peer.AgentID,
			"size", len(resp.Data),
		)
		if ps := peer.PeerScore(); ps != nil {
			ps.RecordInvalidBody()
		}
		return
	}

	n.mu.RLock()
	bs := n.blobStore
	n.mu.RUnlock()
	if bs == nil {
		return
	}

	if _, _, err := bs.Put(context.Background(), resp.Data); err != nil {
		slog.Warn("fastpath: failed to store fallback blob",
			"hash", resp.Hash, "err", err)
		return
	}

	if ps := peer.EnsureScore(); ps != nil {
		ps.RecordValidBody()
	}

	slog.Info("fastpath: stored fallback evidence blob",
		"hash", resp.Hash,
		"size", len(resp.Data),
		"peer", peer.AgentID,
	)
}
