// Package assurance — replay_reserve.go
//
// ReplayReserve maintains a per-category pool of µAET that backs minimum
// replay-executor payouts. Each assured task that settles without triggering a
// replay accrues a fraction of its assurance fee (ReplayReserveShare) into the
// category's pool. When a replay is triggered the executor draws from this pool
// to top up its payout to at least MinReplayPayout.
//
// The circuit-breaker check (IsHealthy / CategoryHealthy) signals when the
// reserve has fallen below a safe operating level so the security floor can
// restrict new assured tasks until the reserve recovers.
package assurance

import (
	"fmt"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// reserveStore is the minimal persistence interface required by ReplayReserve.
// *store.Store satisfies this interface.
type reserveStore interface {
	PutReplayReserve(category string, balance uint64) error
	GetReplayReserve(category string) (uint64, error)
}

// ReplayReserve is a per-category escrow pool that ensures replay executors
// always receive at least MinReplayPayout, even when the assurance fee alone
// is insufficient.
//
// It is safe for concurrent use by multiple goroutines.
type ReplayReserve struct {
	mu       sync.RWMutex
	balances map[string]uint64 // µAET per category
	store    reserveStore
	cfg      *config.AssuranceConfig
}

// NewReplayReserve returns an empty ReplayReserve. store may be nil for
// in-memory-only operation (e.g. tests); persisted data is not loaded.
func NewReplayReserve(cfg *config.AssuranceConfig, s reserveStore) *ReplayReserve {
	return &ReplayReserve{
		balances: make(map[string]uint64),
		store:    s,
		cfg:      cfg,
	}
}

// LoadFromStore creates a ReplayReserve and restores every category balance
// that was previously persisted. Unlike NewReplayReserve it queries the store
// for each known category; unknown categories start at zero on first access.
//
// Because ReplayReserve only learns category names at runtime, LoadFromStore
// accepts a list of known categories to pre-warm. An empty slice is valid —
// balances for any unlisted category will be loaded lazily the first time
// Balance/Accrue/Draw is called, but this constructor cannot do that.
//
// Returns the store's error if any read fails.
func LoadFromStore(cfg *config.AssuranceConfig, s reserveStore, categories []string) (*ReplayReserve, error) {
	r := NewReplayReserve(cfg, s)
	if s == nil {
		return r, nil
	}
	for _, cat := range categories {
		bal, err := s.GetReplayReserve(cat)
		if err != nil {
			return nil, fmt.Errorf("assurance: load replay reserve for %q: %w", cat, err)
		}
		r.balances[cat] = bal
	}
	return r, nil
}

// lazyLoad fetches the stored balance for category if it has not yet been
// loaded into memory. Must be called with r.mu held (write-lock).
func (r *ReplayReserve) lazyLoad(category string) {
	if r.store == nil {
		return
	}
	if _, loaded := r.balances[category]; loaded {
		return
	}
	if bal, err := r.store.GetReplayReserve(category); err == nil {
		r.balances[category] = bal
	}
}

// Accrue adds amount to the category's reserve balance and persists to store.
// Called when a non-replayed assured task settles.
func (r *ReplayReserve) Accrue(category string, amount uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lazyLoad(category)
	r.balances[category] += amount
	return r.persistLocked(category)
}

// Draw deducts up to amount from the category's reserve and returns the
// actual amount drawn (which may be less than amount if the reserve is
// insufficient). Persists to store.
// Called when a replay executor needs its payout topped up.
func (r *ReplayReserve) Draw(category string, amount uint64) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lazyLoad(category)
	bal := r.balances[category]
	drawn := amount
	if drawn > bal {
		drawn = bal
	}
	r.balances[category] = bal - drawn
	if err := r.persistLocked(category); err != nil {
		// Undo in-memory change on persistence failure.
		r.balances[category] = bal
		return 0, err
	}
	return drawn, nil
}

// Balance returns the current reserve balance (µAET) for category.
func (r *ReplayReserve) Balance(category string) uint64 {
	r.mu.RLock()
	bal, loaded := r.balances[category]
	r.mu.RUnlock()
	if loaded {
		return bal
	}
	// Not in cache — try store.
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lazyLoad(category)
	return r.balances[category]
}

// IsHealthy returns true when the category's balance is at or above the
// circuit-breaker threshold (cfg.ReplayReserveCircuitBreaker × targetBalance).
// A false return means the protocol should restrict new assured tasks for
// this category.
func (r *ReplayReserve) IsHealthy(category string, targetBalance uint64) bool {
	bal := r.Balance(category)
	threshold := uint64(r.cfg.ReplayReserveCircuitBreaker * float64(targetBalance))
	return bal >= threshold
}

// TopUp adds amount to the category's reserve from an external funding source
// (e.g. network rewards during the bootstrap period). Persists to store.
func (r *ReplayReserve) TopUp(category string, amount uint64) error {
	return r.Accrue(category, amount) // identical mechanics, separate name for clarity
}

// CategoryHealthy reports whether the reserve is above the circuit-breaker
// threshold for category. The target balance is 10 × MinReplayPayout — the
// estimated reserve needed to fund ten replay executions at minimum payout.
func (r *ReplayReserve) CategoryHealthy(category string) bool {
	targetBalance := uint64(10) * r.cfg.MinReplayPayout
	return r.IsHealthy(category, targetBalance)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// persistLocked writes the in-memory balance for category to the backing
// store. Must be called with r.mu held (write-lock).
func (r *ReplayReserve) persistLocked(category string) error {
	if r.store == nil {
		return nil
	}
	return r.store.PutReplayReserve(category, r.balances[category])
}
