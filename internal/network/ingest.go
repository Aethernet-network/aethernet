// ingest.go implements the Fast Path event ingest manager.
//
// The IngestManager tracks events through the pre-materialization pipeline:
// Announced → Completed → Validated → Materialized. It provides deduplication,
// admission control, stage-based queueing, and TTL-based garbage collection.
//
// The manager does NOT call dag.Add or perform validation. Those are driven
// by the relay and validation components wired in subsequent prompts.
package network

import (
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// FastPathConfig holds tunable parameters for the fast-path pipeline.
type FastPathConfig struct {
	// AnnounceTTL is how long an event can remain in Announced state before
	// being garbage-collected. A header that stays announced without body
	// completion for this duration is considered stale.
	AnnounceTTL time.Duration

	// CompleteTTL is how long an event can remain in Completed state waiting
	// for validation before being garbage-collected.
	CompleteTTL time.Duration

	// GCInterval is how often the garbage collector runs.
	GCInterval time.Duration

	// MaxTracked is the maximum number of events tracked simultaneously.
	// When exceeded, the oldest Announced entries are evicted.
	MaxTracked int

	// MaxAnnounceQueueSize is the capacity of the announce queue.
	MaxAnnounceQueueSize int

	// MaxRelayQueueSize is the capacity of the relay queue.
	MaxRelayQueueSize int
}

// DefaultFastPathConfig returns production-ready defaults.
func DefaultFastPathConfig() FastPathConfig {
	return FastPathConfig{
		AnnounceTTL:          60 * time.Second,
		CompleteTTL:          30 * time.Second,
		GCInterval:           10 * time.Second,
		MaxTracked:           10000,
		MaxAnnounceQueueSize: 4096,
		MaxRelayQueueSize:    4096,
	}
}

// IngestManager tracks events through the fast-path pre-materialization
// pipeline. It is safe for concurrent use by multiple goroutines.
type IngestManager struct {
	config FastPathConfig

	// tracked maps EventID → EventTracking for all events in the pipeline.
	tracked map[event.EventID]*EventTracking

	// Stage-based queues. Each queue holds EventIDs ready for the next
	// processing step. Consumers drain these queues asynchronously.
	announceQ    chan event.EventID // headers admitted, ready for relay decision
	relayQ       chan event.EventID // headers approved for relay to peers
	completeQ    chan event.EventID // bodies received, ready for validation
	validateQ    chan event.EventID // validation passed, ready for materialization
	materializeQ chan event.EventID // materialization ready (dag.Add)

	mu     sync.RWMutex
	stopCh chan struct{}
	once   sync.Once
}

// NewIngestManager creates an IngestManager with the given configuration.
// Call Start() to begin the GC goroutine.
func NewIngestManager(config FastPathConfig) *IngestManager {
	return &IngestManager{
		config:       config,
		tracked:      make(map[event.EventID]*EventTracking, config.MaxTracked),
		announceQ:    make(chan event.EventID, config.MaxAnnounceQueueSize),
		relayQ:       make(chan event.EventID, config.MaxRelayQueueSize),
		completeQ:    make(chan event.EventID, config.MaxAnnounceQueueSize),
		validateQ:    make(chan event.EventID, config.MaxAnnounceQueueSize),
		materializeQ: make(chan event.EventID, config.MaxAnnounceQueueSize),
		stopCh:       make(chan struct{}),
	}
}

// Start launches the background GC goroutine.
func (im *IngestManager) Start() {
	go im.gcLoop()
}

// Stop signals the GC goroutine to exit.
func (im *IngestManager) Stop() {
	im.once.Do(func() { close(im.stopCh) })
}

// AdmitHeader performs cheap admission of an event header and creates an
// Announced tracking entry. Returns true if the header was newly admitted,
// false if it was a duplicate (already tracked or already in the DAG).
//
// This is the entry point for the fast-path pipeline. It does NOT perform
// validation, body fetch, or dag.Add — those are driven by later stages.
func (im *IngestManager) AdmitHeader(from crypto.AgentID, hdr EventHeader) bool {
	id := hdr.EventID

	im.mu.Lock()
	defer im.mu.Unlock()

	// Deduplicate: already tracked.
	if _, exists := im.tracked[id]; exists {
		return false
	}

	// Capacity check: evict oldest Announced entries if at limit.
	if len(im.tracked) >= im.config.MaxTracked {
		im.evictOldestAnnouncedLocked()
		if len(im.tracked) >= im.config.MaxTracked {
			slog.Warn("ingest: tracking capacity exceeded, dropping header",
				"event_id", id, "tracked", len(im.tracked))
			return false
		}
	}

	tracking := &EventTracking{
		Header:     hdr,
		Stage:      StageAnnounced,
		ReceivedAt: time.Now(),
		SourcePeer: from,
	}
	im.tracked[id] = tracking

	// Non-blocking enqueue. If the queue is full, log and continue —
	// the event is still tracked and can be picked up on the next pass.
	select {
	case im.announceQ <- id:
	default:
		slog.Debug("ingest: announceQ full, event tracked but not queued",
			"event_id", id)
	}

	return true
}

// GetTracking returns the tracking entry for an event, or nil if not tracked.
func (im *IngestManager) GetTracking(id event.EventID) *EventTracking {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return im.tracked[id]
}

// IsTracked returns true if the event ID is in the tracking map.
func (im *IngestManager) IsTracked(id event.EventID) bool {
	im.mu.RLock()
	defer im.mu.RUnlock()
	_, ok := im.tracked[id]
	return ok
}

// MarkCompleted transitions an event from Announced to Completed.
// Returns false if the event is not tracked or cannot advance.
func (im *IngestManager) MarkCompleted(id event.EventID) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok {
		return false
	}
	if !t.AdvanceTo(StageCompleted) {
		return false
	}
	select {
	case im.completeQ <- id:
	default:
	}
	return true
}

// MarkValidated transitions an event from Completed to Validated.
func (im *IngestManager) MarkValidated(id event.EventID) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok {
		return false
	}
	if !t.AdvanceTo(StageValidated) {
		return false
	}
	select {
	case im.validateQ <- id:
	default:
	}
	return true
}

// MarkMaterialized transitions an event to Materialized and enqueues it.
// After materialization the tracking entry is retained briefly for dedup
// but will be GC'd on the next cycle.
func (im *IngestManager) MarkMaterialized(id event.EventID) bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	t, ok := im.tracked[id]
	if !ok {
		return false
	}
	if !t.AdvanceTo(StageMaterialized) {
		return false
	}
	select {
	case im.materializeQ <- id:
	default:
	}
	return true
}

// Remove deletes a tracking entry. Used after materialization or on error.
func (im *IngestManager) Remove(id event.EventID) {
	im.mu.Lock()
	defer im.mu.Unlock()
	delete(im.tracked, id)
}

// TrackedCount returns the number of events currently in the pipeline.
func (im *IngestManager) TrackedCount() int {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return len(im.tracked)
}

// StageCount returns the number of events at each stage.
func (im *IngestManager) StageCount() map[EventStage]int {
	im.mu.RLock()
	defer im.mu.RUnlock()
	counts := make(map[EventStage]int, 4)
	for _, t := range im.tracked {
		counts[t.Stage]++
	}
	return counts
}

// AnnounceQ returns the announce queue for external consumers.
func (im *IngestManager) AnnounceQ() <-chan event.EventID { return im.announceQ }

// RelayQ returns the relay queue for external consumers.
func (im *IngestManager) RelayQ() <-chan event.EventID { return im.relayQ }

// CompleteQ returns the complete queue for external consumers.
func (im *IngestManager) CompleteQ() <-chan event.EventID { return im.completeQ }

// ValidateQ returns the validate queue for external consumers.
func (im *IngestManager) ValidateQ() <-chan event.EventID { return im.validateQ }

// MaterializeQ returns the materialize queue for external consumers.
func (im *IngestManager) MaterializeQ() <-chan event.EventID { return im.materializeQ }

// ── GC ───────────────────────────────────────────────────────────────────────

func (im *IngestManager) gcLoop() {
	ticker := time.NewTicker(im.config.GCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-im.stopCh:
			return
		case <-ticker.C:
			im.gc()
		}
	}
}

func (im *IngestManager) gc() {
	im.mu.Lock()
	defer im.mu.Unlock()

	now := time.Now()
	var evicted int
	for id, t := range im.tracked {
		switch t.Stage {
		case StageAnnounced:
			if now.Sub(t.ReceivedAt) > im.config.AnnounceTTL {
				delete(im.tracked, id)
				evicted++
			}
		case StageCompleted:
			if now.Sub(t.CompletedAt) > im.config.CompleteTTL {
				delete(im.tracked, id)
				evicted++
			}
		case StageMaterialized:
			// Materialized entries are retained for dedup but GC'd after
			// the announce TTL to prevent unbounded growth.
			if now.Sub(t.ReceivedAt) > im.config.AnnounceTTL {
				delete(im.tracked, id)
				evicted++
			}
		}
	}
	if evicted > 0 {
		slog.Debug("ingest: gc evicted stale entries", "evicted", evicted, "remaining", len(im.tracked))
	}
}

// evictOldestAnnouncedLocked removes the oldest Announced entry to make room.
// Must be called under im.mu write lock.
func (im *IngestManager) evictOldestAnnouncedLocked() {
	var oldestID event.EventID
	var oldestTime time.Time
	for id, t := range im.tracked {
		if t.Stage == StageAnnounced {
			if oldestTime.IsZero() || t.ReceivedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = t.ReceivedAt
			}
		}
	}
	if oldestID != "" {
		delete(im.tracked, oldestID)
	}
}
