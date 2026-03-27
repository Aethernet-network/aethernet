// backpressure.go implements explicit overload detection and per-peer quotas
// for the Fast Path.
//
// Overload is detected from queue depths in the IngestManager. When the node
// is overloaded, it sends MsgOverloaded to peers requesting them to reduce
// traffic. Peers respect received Overloaded signals by deprioritizing the
// sender in mesh selection (already handled via PeerStatus.Healthy in mesh.go).
//
// Per-peer quotas enforce bounded send rates for headers, bodies, and repair
// requests. Under overload, low-priority work (relay to low-scored peers) is
// shed first while safety-critical work (repair responses, body responses to
// high-scored peers) is preserved.
package network

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Overload thresholds — if any queue exceeds its threshold, the node
// is considered overloaded.
const (
	OverloadQueueThreshold = 3000 // > 75% of default 4096 queue capacity
	OverloadRelayThreshold = 3000
	StatusBroadcastInterval = 10 * time.Second
)

// PeerQuota enforces bounded send rates per peer per plane.
// All fields are accessed atomically — no mutex needed.
type PeerQuota struct {
	// Headers sent to this peer in the current window.
	headersSent atomic.Int64
	// Bodies sent to this peer in the current window.
	bodiesSent atomic.Int64
	// Repair requests sent to this peer in the current window.
	repairsSent atomic.Int64
	// Concurrent body requests outstanding to this peer.
	pendingBodies atomic.Int64
	// Concurrent repair requests outstanding to this peer.
	pendingRepairs atomic.Int64
	// Window start time.
	windowStart atomic.Int64 // Unix seconds

	// Per-window limits.
	maxHeadersPerWindow int64
	maxBodiesPerWindow  int64
	maxRepairsPerWindow int64
	maxPendingBodies    int64
	maxPendingRepairs   int64
}

// QuotaConfig holds per-peer rate limits.
type QuotaConfig struct {
	HeadersPerWindow int64 // max headers sent per window
	BodiesPerWindow  int64 // max bodies sent per window
	RepairsPerWindow int64 // max repair requests per window
	MaxPendingBodies int64 // max concurrent body requests
	MaxPendingRepairs int64 // max concurrent repair requests
	WindowSeconds    int64 // quota window duration
}

// DefaultQuotaConfig returns production defaults.
func DefaultQuotaConfig() QuotaConfig {
	return QuotaConfig{
		HeadersPerWindow:  500,
		BodiesPerWindow:   100,
		RepairsPerWindow:  50,
		MaxPendingBodies:  10,
		MaxPendingRepairs: 5,
		WindowSeconds:     60,
	}
}

// NewPeerQuota creates a PeerQuota with the given limits.
func NewPeerQuota(cfg QuotaConfig) *PeerQuota {
	pq := &PeerQuota{
		maxHeadersPerWindow: cfg.HeadersPerWindow,
		maxBodiesPerWindow:  cfg.BodiesPerWindow,
		maxRepairsPerWindow: cfg.RepairsPerWindow,
		maxPendingBodies:    cfg.MaxPendingBodies,
		maxPendingRepairs:   cfg.MaxPendingRepairs,
	}
	pq.windowStart.Store(time.Now().Unix())
	return pq
}

func (pq *PeerQuota) resetIfNeeded(windowSecs int64) {
	now := time.Now().Unix()
	start := pq.windowStart.Load()
	if now-start >= windowSecs {
		pq.headersSent.Store(0)
		pq.bodiesSent.Store(0)
		pq.repairsSent.Store(0)
		pq.windowStart.Store(now)
	}
}

// AllowHeader returns true if the header quota is not exhausted.
func (pq *PeerQuota) AllowHeader(windowSecs int64) bool {
	pq.resetIfNeeded(windowSecs)
	return pq.headersSent.Add(1) <= pq.maxHeadersPerWindow
}

// AllowBody returns true if the body quota is not exhausted.
func (pq *PeerQuota) AllowBody(windowSecs int64) bool {
	pq.resetIfNeeded(windowSecs)
	return pq.bodiesSent.Add(1) <= pq.maxBodiesPerWindow
}

// AllowRepair returns true if the repair quota is not exhausted.
func (pq *PeerQuota) AllowRepair(windowSecs int64) bool {
	pq.resetIfNeeded(windowSecs)
	return pq.repairsSent.Add(1) <= pq.maxRepairsPerWindow
}

// AllowPendingBody returns true if concurrent body requests are under limit.
// Uses compare-and-swap to avoid over-counting on denial.
func (pq *PeerQuota) AllowPendingBody() bool {
	for {
		cur := pq.pendingBodies.Load()
		if cur >= pq.maxPendingBodies {
			return false
		}
		if pq.pendingBodies.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// ReleasePendingBody decrements the concurrent body request counter.
func (pq *PeerQuota) ReleasePendingBody() {
	pq.pendingBodies.Add(-1)
}

// AllowPendingRepair returns true if concurrent repair requests are under limit.
func (pq *PeerQuota) AllowPendingRepair() bool {
	for {
		cur := pq.pendingRepairs.Load()
		if cur >= pq.maxPendingRepairs {
			return false
		}
		if pq.pendingRepairs.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// ReleasePendingRepair decrements the concurrent repair request counter.
func (pq *PeerQuota) ReleasePendingRepair() {
	pq.pendingRepairs.Add(-1)
}

// HeadersRemaining returns how many headers can still be sent in this window.
func (pq *PeerQuota) HeadersRemaining(windowSecs int64) int64 {
	pq.resetIfNeeded(windowSecs)
	rem := pq.maxHeadersPerWindow - pq.headersSent.Load()
	if rem < 0 {
		return 0
	}
	return rem
}

// ── Node Overload Detection ──────────────────────────────────────────────────

// OverloadState tracks the node's current load level.
type OverloadState struct {
	mu          sync.RWMutex
	overloaded  bool
	since       time.Time
	lastSignal  time.Time
}

// IsOverloaded returns true if the node is currently overloaded.
func (os *OverloadState) IsOverloaded() bool {
	os.mu.RLock()
	defer os.mu.RUnlock()
	return os.overloaded
}

// SetOverloaded updates the overload state.
func (os *OverloadState) SetOverloaded(v bool) {
	os.mu.Lock()
	defer os.mu.Unlock()
	if v && !os.overloaded {
		os.since = time.Now()
	}
	os.overloaded = v
}

// CanSignal returns true if enough time has passed since the last signal
// to avoid flooding peers with overload messages.
func (os *OverloadState) CanSignal() bool {
	os.mu.Lock()
	defer os.mu.Unlock()
	if time.Since(os.lastSignal) < 5*time.Second {
		return false
	}
	os.lastSignal = time.Now()
	return true
}

// ── Queue Depth Metrics ──────────────────────────────────────────────────────

// QueueDepths returns the current depth of each ingest queue.
func (im *IngestManager) QueueDepths() map[string]int {
	return map[string]int{
		"announce":    len(im.announceQ),
		"relay":       len(im.relayQ),
		"complete":    len(im.completeQ),
		"validate":    len(im.validateQ),
		"materialize": len(im.materializeQ),
		"repair":      len(im.repairCh),
	}
}

// MaxQueueDepth returns the highest queue depth across all ingest queues.
func (im *IngestManager) MaxQueueDepth() int {
	depths := im.QueueDepths()
	max := 0
	for _, d := range depths {
		if d > max {
			max = d
		}
	}
	return max
}

// ── Backpressure Worker ──────────────────────────────────────────────────────

// backpressureWorker periodically checks queue depths, updates overload state,
// and broadcasts PeerStatus to all V2 peers. Runs as a goroutine.
func (n *Node) backpressureWorker(ctx context.Context) {
	ticker := time.NewTicker(StatusBroadcastInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.evaluateOverload()
			n.broadcastStatus()
		}
	}
}

// evaluateOverload checks queue depths and updates the node's overload state.
func (n *Node) evaluateOverload() {
	if n.ingest == nil {
		return
	}

	maxDepth := n.ingest.MaxQueueDepth()
	wasOverloaded := n.overload.IsOverloaded()
	isOverloaded := maxDepth >= OverloadQueueThreshold

	n.overload.SetOverloaded(isOverloaded)

	if isOverloaded && !wasOverloaded {
		slog.Warn("backpressure: node entering overloaded state",
			"max_queue_depth", maxDepth, "threshold", OverloadQueueThreshold)
		n.sendOverloadSignal()
	} else if !isOverloaded && wasOverloaded {
		slog.Info("backpressure: node recovered from overload",
			"max_queue_depth", maxDepth)
	}
}

// sendOverloadSignal sends MsgOverloaded to all V2 peers.
func (n *Node) sendOverloadSignal() {
	if !n.overload.CanSignal() {
		return
	}

	ol := Overloaded{RetryAfterMs: 5000}
	payload, err := json.Marshal(ol)
	if err != nil {
		return
	}
	msg := Message{Type: MsgOverloaded, Payload: payload}

	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, p := range n.peers {
		SafeSend(p, msg)
	}
}

// broadcastStatus sends PeerStatus to all V2 peers.
func (n *Node) broadcastStatus() {
	if n.ingest == nil {
		return
	}

	status := PeerStatus{
		QueueDepth:    n.ingest.MaxQueueDepth(),
		DAGEventCount: uint64(n.dag.Size()),
		Healthy:       !n.overload.IsOverloaded(),
	}

	payload, err := json.Marshal(status)
	if err != nil {
		return
	}
	msg := Message{Type: MsgPeerStatus, Payload: payload}

	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, p := range n.peers {
		SafeSend(p, msg)
	}
}

// handleOverloaded processes an incoming MsgOverloaded from a peer.
func (n *Node) handleOverloaded(peer *Peer, payload []byte) {
	var ol Overloaded
	if err := json.Unmarshal(payload, &ol); err != nil {
		return
	}
	// Mark the peer as unhealthy — mesh.SelectRelayTargets will exclude it.
	peer.UpdateStatus(PeerStatus{Healthy: false, QueueDepth: 9999})
	slog.Info("backpressure: peer reported overload",
		"peer", peer.AgentID, "retry_after_ms", ol.RetryAfterMs)
}

// handlePeerStatus processes an incoming MsgPeerStatus from a peer.
func (n *Node) handlePeerStatus(peer *Peer, payload []byte) {
	var status PeerStatus
	if err := json.Unmarshal(payload, &status); err != nil {
		return
	}
	peer.UpdateStatus(status)
}
