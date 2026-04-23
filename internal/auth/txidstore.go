package auth

import (
	"strconv"
	"sync"
	"time"
)

// txidTTL is how long a TxID is remembered in the hot cache.
const txidTTL = 10 * time.Minute

// TxIDStore tracks seen transaction IDs to prevent double-execution.
// Uses BadgerDB for persistence with in-memory hot cache.
type TxIDStore struct {
	store  nonceStoreBackend // reuse the same PutMeta/GetMeta interface
	mu     sync.Mutex
	local  map[string]int64
	stopCh chan struct{}
	once   sync.Once
}

// NewTxIDStore creates a TxID store backed by the given persistence backend.
func NewTxIDStore(store nonceStoreBackend) *TxIDStore {
	return &TxIDStore{
		store:  store,
		local:  make(map[string]int64),
		stopCh: make(chan struct{}),
	}
}

// MarkSeen records a TxID as seen. Returns true if it was already seen.
func (s *TxIDStore) MarkSeen(txID string) (alreadySeen bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := "txid:" + txID

	// Check in-memory cache.
	if _, exists := s.local[key]; exists {
		return true, nil
	}

	// Check persistent store.
	if s.store != nil {
		if data, err := s.store.GetMeta(key); err == nil && len(data) > 0 {
			s.local[key] = time.Now().Unix()
			return true, nil
		}
	}

	// Store the TxID.
	now := time.Now().Unix()
	s.local[key] = now
	if s.store != nil {
		_ = s.store.PutMeta(key, []byte(strconv.FormatInt(now, 10)))
	}
	return false, nil
}

// IsSeen checks if a TxID has been seen without marking it.
func (s *TxIDStore) IsSeen(txID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := "txid:" + txID
	if _, exists := s.local[key]; exists {
		return true, nil
	}
	if s.store != nil {
		if data, err := s.store.GetMeta(key); err == nil && len(data) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// Start launches a background goroutine that cleans up expired TxID entries.
func (s *TxIDStore) Start() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.cleanup()
			}
		}
	}()
}

// Stop signals the cleanup goroutine to exit.
func (s *TxIDStore) Stop() {
	s.once.Do(func() { close(s.stopCh) })
}

func (s *TxIDStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-txidTTL).Unix()
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for key, ts := range s.local {
		if ts < cutoff {
			delete(s.local, key)
		}
	}
}
