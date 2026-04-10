package blobsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// PeerSender abstracts the network layer for sending blob-related messages
// to specific peers or broadcasting to all peers. Implemented by a thin
// adapter over the network.Node to avoid circular imports.
type PeerSender interface {
	// SendToPeer sends a typed message to a specific peer by ID.
	// Returns error if peer is not connected or send buffer is full.
	SendToPeer(peerID string, msgType string, payload []byte) error

	// BroadcastMessage sends a typed message to up to `fanout` random peers.
	// Returns the peer IDs that were sent to.
	BroadcastMessage(msgType string, payload []byte, fanout int) ([]string, error)

	// LocalPeerID returns this node's peer ID.
	LocalPeerID() string
}

// Wire message types matching network.protocol constants.
const (
	wireMsgBlobRequest      = "v2_blob_request"
	wireMsgBlobResponse     = "v2_blob_response"
	wireMsgBlobQuery        = "v2_blob_query"
	wireMsgBlobQueryResp    = "v2_blob_query_response"
)

// blobRequestWire is the JSON structure for MsgBlobRequest (reuses existing protocol struct).
type blobRequestWire struct {
	Hash string `json:"hash"`
}

// blobResponseWire is the JSON structure for MsgBlobResponse.
type blobResponseWire struct {
	Hash string `json:"hash"`
	Data []byte `json:"data"`
}

// blobQueryWire is the JSON structure for MsgBlobQuery.
type blobQueryWire struct {
	Hash string `json:"hash"` // hex-encoded for wire compatibility
}

// blobQueryResponseWire is the JSON structure for MsgBlobQueryResponse.
type blobQueryResponseWire struct {
	Hash    string `json:"hash"`
	HasBlob bool   `json:"has_blob"`
}

// pendingFetch tracks an outstanding FetchFromPeer request awaiting a response.
type pendingFetch struct {
	dataCh chan []byte
	errCh  chan error
}

// pendingQuery tracks an outstanding BroadcastQuery awaiting responses.
type pendingQuery struct {
	mu       sync.Mutex
	peerIDs  []string
	expected int
	doneCh   chan struct{}
}

// BlobTransport implements the BlobFetcher interface using the peer network.
// It sends blob fetch and discovery messages over the existing peer connections
// and routes incoming responses to pending request channels.
type BlobTransport struct {
	sender PeerSender
	reputation *BlobServingReputation

	mu             sync.Mutex
	pendingFetches map[string]*pendingFetch  // hex-hash+peerID → pending
	pendingQueries map[string]*pendingQuery  // hex-hash → pending query
}

// NewBlobTransport creates a transport wired to the given peer network.
func NewBlobTransport(sender PeerSender, reputation *BlobServingReputation) *BlobTransport {
	return &BlobTransport{
		sender:         sender,
		reputation:     reputation,
		pendingFetches: make(map[string]*pendingFetch),
		pendingQueries: make(map[string]*pendingQuery),
	}
}

// FetchFromPeer requests a blob from a specific peer. Blocks until the
// response arrives or the context expires.
func (t *BlobTransport) FetchFromPeer(ctx context.Context, peerID string, hash [32]byte) ([]byte, error) {
	hexHash := fmt.Sprintf("%x", hash)
	key := hexHash + ":" + peerID

	pf := &pendingFetch{
		dataCh: make(chan []byte, 1),
		errCh:  make(chan error, 1),
	}

	t.mu.Lock()
	t.pendingFetches[key] = pf
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pendingFetches, key)
		t.mu.Unlock()
	}()

	// Send the request.
	req := blobRequestWire{Hash: hexHash}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("blobsync: marshal fetch request: %w", err)
	}

	start := time.Now()
	if err := t.sender.SendToPeer(peerID, wireMsgBlobRequest, payload); err != nil {
		return nil, fmt.Errorf("blobsync: send fetch to %s: %w", peerID, err)
	}

	// Wait for response.
	select {
	case data := <-pf.dataCh:
		elapsed := time.Since(start)
		if t.reputation != nil {
			t.reputation.RecordSuccess(peerID, elapsed)
		}
		return data, nil
	case err := <-pf.errCh:
		if t.reputation != nil {
			t.reputation.RecordFailure(peerID)
		}
		return nil, err
	case <-ctx.Done():
		if t.reputation != nil {
			t.reputation.RecordTimeout(peerID)
		}
		return nil, ctx.Err()
	}
}

// BroadcastQuery sends a "do you have this?" query to up to fanout peers
// and returns the peer IDs that responded affirmatively.
func (t *BlobTransport) BroadcastQuery(ctx context.Context, hash [32]byte, fanout int) ([]string, error) {
	hexHash := fmt.Sprintf("%x", hash)

	query := blobQueryWire{Hash: hexHash}
	payload, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("blobsync: marshal query: %w", err)
	}

	pq := &pendingQuery{
		doneCh: make(chan struct{}),
	}

	t.mu.Lock()
	t.pendingQueries[hexHash] = pq
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pendingQueries, hexHash)
		t.mu.Unlock()
	}()

	sentTo, err := t.sender.BroadcastMessage(wireMsgBlobQuery, payload, fanout)
	if err != nil {
		return nil, fmt.Errorf("blobsync: broadcast query: %w", err)
	}

	pq.mu.Lock()
	pq.expected = len(sentTo)
	pq.mu.Unlock()

	if len(sentTo) == 0 {
		return nil, nil
	}

	// Wait for responses with a bounded timeout (don't wait for stragglers).
	queryTimeout := 2 * time.Second
	timer := time.NewTimer(queryTimeout)
	defer timer.Stop()

	select {
	case <-pq.doneCh:
	case <-timer.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	pq.mu.Lock()
	result := make([]string, len(pq.peerIDs))
	copy(result, pq.peerIDs)
	pq.mu.Unlock()

	return result, nil
}

// HandleBlobResponse is called by the network layer when a MsgBlobResponse
// arrives from a peer. Routes the data to the pending fetch channel.
func (t *BlobTransport) HandleBlobResponse(peerID string, payload []byte) {
	var resp blobResponseWire
	if err := json.Unmarshal(payload, &resp); err != nil {
		slog.Debug("blobsync: invalid blob response", "peer", peerID, "err", err)
		return
	}
	if resp.Hash == "" || len(resp.Data) == 0 {
		return
	}

	key := resp.Hash + ":" + peerID

	t.mu.Lock()
	pf, ok := t.pendingFetches[key]
	t.mu.Unlock()

	if ok {
		select {
		case pf.dataCh <- resp.Data:
		default:
		}
	}
}

// HandleBlobQueryResponse is called by the network layer when a
// MsgBlobQueryResponse arrives from a peer.
func (t *BlobTransport) HandleBlobQueryResponse(peerID string, payload []byte) {
	var resp blobQueryResponseWire
	if err := json.Unmarshal(payload, &resp); err != nil {
		slog.Debug("blobsync: invalid query response", "peer", peerID, "err", err)
		return
	}

	t.mu.Lock()
	pq, ok := t.pendingQueries[resp.Hash]
	t.mu.Unlock()

	if !ok {
		return
	}

	pq.mu.Lock()
	if resp.HasBlob {
		pq.peerIDs = append(pq.peerIDs, peerID)
	}
	received := len(pq.peerIDs)
	expected := pq.expected
	pq.mu.Unlock()

	// If all expected responses arrived (or enough affirmative ones), signal done.
	if received >= expected || received >= 1 {
		select {
		case <-pq.doneCh:
			// Already closed.
		default:
			close(pq.doneCh)
		}
	}
}

// HandleBlobQuery is called by the network layer when a MsgBlobQuery arrives.
// If we have the blob, respond affirmatively.
func (t *BlobTransport) HandleBlobQuery(peerID string, payload []byte, hasBlob func(hash string) bool) {
	var query blobQueryWire
	if err := json.Unmarshal(payload, &query); err != nil {
		slog.Debug("blobsync: invalid blob query", "peer", peerID, "err", err)
		return
	}
	if query.Hash == "" {
		return
	}

	has := hasBlob(query.Hash)
	resp := blobQueryResponseWire{
		Hash:    query.Hash,
		HasBlob: has,
	}
	respPayload, err := json.Marshal(resp)
	if err != nil {
		return
	}

	if err := t.sender.SendToPeer(peerID, wireMsgBlobQueryResp, respPayload); err != nil {
		slog.Debug("blobsync: failed to send query response", "peer", peerID, "err", err)
	}
}
