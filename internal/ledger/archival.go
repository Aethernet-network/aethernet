// archival.go — Background eviction of settled ledger entries from memory.
//
// Both the TransferLedger and GenerationLedger maintain in-memory maps that
// grow without bound on long-running nodes. Archival periodically evicts
// entries older than a configurable threshold from these maps. All data is
// retained in the persistent store (BadgerDB); the in-memory copy is purely
// a cache for hot-path lookups.
//
// Only Settled and Adjusted entries are eligible for eviction. Optimistic
// entries are never evicted because they may still be mutated by Settle /
// Verify / Reject.
//
// Balance correctness in the TransferLedger is maintained by a per-agent
// net-settled summary (archivedNetSettled) that records the cumulative balance
// contribution of every evicted Settled entry. Adjusted entries contribute
// nothing to the balance and require no accounting here.
package ledger

import (
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ArchiveConfig controls the ledger archival background goroutine.
// Set Threshold to zero or negative to disable archival entirely.
type ArchiveConfig struct {
	// Threshold is the minimum age of a Settled or Adjusted entry before it
	// may be evicted from the in-memory cache. Entries younger than Threshold
	// are kept in memory for fast access. Default: 7 days.
	// A zero or negative value disables archival.
	Threshold time.Duration

	// Interval is how often the archival goroutine wakes up to scan for
	// evictable entries. Default: 1 hour.
	Interval time.Duration
}

// DefaultArchiveThreshold is the age at which ledger entries are evicted from
// the in-memory cache. All data remains accessible via the persistent store.
const DefaultArchiveThreshold = 7 * 24 * time.Hour

const archiveDefaultInterval = time.Hour

// archiveCfgWithDefaults returns cfg with zero Interval replaced by the default.
func archiveCfgWithDefaults(cfg ArchiveConfig) ArchiveConfig {
	if cfg.Interval <= 0 {
		cfg.Interval = archiveDefaultInterval
	}
	return cfg
}

// ---------------------------------------------------------------------------
// TransferLedger archival
// ---------------------------------------------------------------------------

// EvictBefore removes all Settled and Adjusted TransferEntry records whose
// RecordedAt timestamp is strictly before cutoff from the in-memory map.
// Optimistic entries are never evicted. archivedNetSettled is updated so that
// Balance() remains correct after eviction.
//
// Returns the number of entries removed.
func (l *TransferLedger) EvictBefore(cutoff time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	var removed int
	for id, e := range l.entries {
		if e.Settlement == event.SettlementOptimistic {
			continue // never evict pending entries
		}
		if !e.RecordedAt.Before(cutoff) {
			continue // entry is too recent
		}
		// Track the balance contribution of evicted Settled entries.
		// Adjusted entries are excluded from the balance by definition and
		// need no accounting.
		if e.Settlement == event.SettlementSettled {
			l.archivedNetSettled[e.ToAgent] += int64(e.Amount)
			l.archivedNetSettled[e.FromAgent] -= int64(e.Amount)
		}
		delete(l.entries, id)
		removed++
	}
	return removed
}

// LoadTransferEntry returns the TransferEntry for id. It checks the in-memory
// map first; if the entry has been evicted it falls back to the persistent
// store. Returns ErrEntryNotFound if neither source contains the entry.
func (l *TransferLedger) LoadTransferEntry(id event.EventID) (*TransferEntry, error) {
	l.mu.RLock()
	entry, ok := l.entries[id]
	l.mu.RUnlock()
	if ok {
		return entry, nil
	}
	if l.store != nil {
		if stored, err := l.store.GetTransfer(id); err == nil {
			return stored, nil
		}
	}
	return nil, ErrEntryNotFound
}

// Start launches the background archival goroutine for the TransferLedger.
// Eviction runs every cfg.Interval, removing entries older than cfg.Threshold.
// If cfg.Threshold is zero or negative, Start is a no-op and archival is
// disabled. Calling Start more than once without an intervening Stop is a
// no-op.
func (l *TransferLedger) Start(cfg ArchiveConfig) {
	if cfg.Threshold <= 0 {
		return
	}
	cfg = archiveCfgWithDefaults(cfg)

	l.mu.Lock()
	if l.archiveDone != nil {
		l.mu.Unlock()
		return // already running
	}
	done := make(chan struct{})
	l.archiveDone = done
	l.mu.Unlock()

	go func() {
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				l.EvictBefore(time.Now().Add(-cfg.Threshold))
			}
		}
	}()
}

// Stop signals the archival goroutine to exit. If Start was not called, Stop
// is a no-op. Stop does not wait for the goroutine to finish its current
// eviction pass; the goroutine exits on its next select iteration.
func (l *TransferLedger) Stop() {
	l.mu.Lock()
	done := l.archiveDone
	l.archiveDone = nil
	l.mu.Unlock()
	if done != nil {
		close(done)
	}
}

// ---------------------------------------------------------------------------
// GenerationLedger archival
// ---------------------------------------------------------------------------

// EvictBefore removes all Settled and Adjusted GenerationEntry records whose
// RecordedAt timestamp is strictly before cutoff from the in-memory map.
// Optimistic entries are never evicted. Returns the number of entries removed.
func (l *GenerationLedger) EvictBefore(cutoff time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	var removed int
	for id, e := range l.entries {
		if e.Settlement == event.SettlementOptimistic {
			continue
		}
		if !e.RecordedAt.Before(cutoff) {
			continue
		}
		delete(l.entries, id)
		removed++
	}
	return removed
}

// LoadGenerationEntry returns the GenerationEntry for id. It checks the
// in-memory map first; if the entry has been evicted it falls back to the
// persistent store. Returns ErrEntryNotFound if neither source has the entry.
func (l *GenerationLedger) LoadGenerationEntry(id event.EventID) (*GenerationEntry, error) {
	l.mu.RLock()
	entry, ok := l.entries[id]
	l.mu.RUnlock()
	if ok {
		return entry, nil
	}
	if l.store != nil {
		if stored, err := l.store.GetGeneration(id); err == nil {
			return stored, nil
		}
	}
	return nil, ErrEntryNotFound
}

// Start launches the background archival goroutine for the GenerationLedger.
// Eviction runs every cfg.Interval, removing entries older than cfg.Threshold.
// If cfg.Threshold is zero or negative, Start is a no-op and archival is
// disabled. Calling Start more than once without an intervening Stop is a
// no-op.
func (l *GenerationLedger) Start(cfg ArchiveConfig) {
	if cfg.Threshold <= 0 {
		return
	}
	cfg = archiveCfgWithDefaults(cfg)

	l.mu.Lock()
	if l.archiveDone != nil {
		l.mu.Unlock()
		return // already running
	}
	done := make(chan struct{})
	l.archiveDone = done
	l.mu.Unlock()

	go func() {
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				l.EvictBefore(time.Now().Add(-cfg.Threshold))
			}
		}
	}()
}

// Stop signals the archival goroutine to exit. If Start was not called, Stop
// is a no-op.
func (l *GenerationLedger) Stop() {
	l.mu.Lock()
	done := l.archiveDone
	l.archiveDone = nil
	l.mu.Unlock()
	if done != nil {
		close(done)
	}
}
