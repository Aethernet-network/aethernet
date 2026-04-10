package blobsync

import (
	"sync"
	"time"
)

// BlobServingReputation tracks per-peer blob serving statistics.
// Used to prefer reliable peers for future fetches and to detect
// peers that consistently fail or timeout.
type BlobServingReputation struct {
	mu    sync.RWMutex
	peers map[string]*peerServingStats
}

type peerServingStats struct {
	Successes    uint32
	Failures     uint32
	Timeouts     uint32
	TotalLatency time.Duration // cumulative for average calculation
	LastUpdated  time.Time
}

// NewBlobServingReputation creates a new reputation tracker.
func NewBlobServingReputation() *BlobServingReputation {
	return &BlobServingReputation{
		peers: make(map[string]*peerServingStats),
	}
}

func (r *BlobServingReputation) getOrCreate(peerID string) *peerServingStats {
	stats, ok := r.peers[peerID]
	if !ok {
		stats = &peerServingStats{}
		r.peers[peerID] = stats
	}
	return stats
}

// RecordSuccess records a successful blob fetch from a peer.
func (r *BlobServingReputation) RecordSuccess(peerID string, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreate(peerID)
	s.Successes++
	s.TotalLatency += latency
	s.LastUpdated = time.Now()
}

// RecordFailure records a failed blob fetch from a peer.
func (r *BlobServingReputation) RecordFailure(peerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreate(peerID)
	s.Failures++
	s.LastUpdated = time.Now()
}

// RecordTimeout records a timed-out blob fetch from a peer.
func (r *BlobServingReputation) RecordTimeout(peerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreate(peerID)
	s.Timeouts++
	s.LastUpdated = time.Now()
}

// SuccessRate returns the fraction of successful fetches for a peer (0.0–1.0).
// Returns 1.0 for unknown peers (benefit of the doubt).
func (r *BlobServingReputation) SuccessRate(peerID string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.peers[peerID]
	if !ok {
		return 1.0
	}
	total := s.Successes + s.Failures + s.Timeouts
	if total == 0 {
		return 1.0
	}
	return float64(s.Successes) / float64(total)
}

// AverageLatency returns the average fetch latency for a peer.
// Returns 0 for unknown peers or peers with no successful fetches.
func (r *BlobServingReputation) AverageLatency(peerID string) time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.peers[peerID]
	if !ok || s.Successes == 0 {
		return 0
	}
	return s.TotalLatency / time.Duration(s.Successes)
}

// Stats returns a snapshot of a peer's serving statistics.
func (r *BlobServingReputation) Stats(peerID string) (successes, failures, timeouts uint32) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.peers[peerID]
	if !ok {
		return 0, 0, 0
	}
	return s.Successes, s.Failures, s.Timeouts
}
