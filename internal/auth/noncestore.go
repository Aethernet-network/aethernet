package auth

import (
	"strconv"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// nonceTTL is how long a nonce is remembered for replay protection.
const nonceTTL = 10 * time.Minute

// nonceStoreBackend is the persistence interface for nonce storage.
type nonceStoreBackend interface {
	PutMeta(key string, value []byte) error
	GetMeta(key string) ([]byte, error)
}

// BadgerNonceStore implements NonceStore backed by BadgerDB metadata keys.
// Nonces are stored with a timestamp and cleaned up periodically.
type BadgerNonceStore struct {
	store nonceStoreBackend
	mu    sync.Mutex
	// local tracks recently seen nonces in memory for fast lookup.
	// Entries are evicted by the cleanup goroutine.
	local  map[string]int64
	stopCh chan struct{}
	once   sync.Once
}

// NewBadgerNonceStore creates a nonce store backed by the given store.
// Call Start() to begin periodic cleanup of expired nonces.
func NewBadgerNonceStore(store nonceStoreBackend) *BadgerNonceStore {
	return &BadgerNonceStore{
		store:  store,
		local:  make(map[string]int64),
		stopCh: make(chan struct{}),
	}
}

// CheckAndStore atomically checks if a nonce has been used for this agent
// and stores it if not. Returns true if the nonce was already used (replay).
func (s *BadgerNonceStore) CheckAndStore(agentID crypto.AgentID, nonce string) (bool, error) {
	key := "nonce:" + string(agentID) + ":" + nonce

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check in-memory cache first.
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

	// Store the nonce.
	now := time.Now().Unix()
	s.local[key] = now
	if s.store != nil {
		_ = s.store.PutMeta(key, []byte(strconv.FormatInt(now, 10)))
	}
	return false, nil
}

// Start launches a background goroutine that periodically removes expired
// nonce entries from the in-memory cache. Expired entries in the store are
// harmless (they'll be ignored on next check since the timestamp window
// prevents reuse) but cleaned up to save memory.
func (s *BadgerNonceStore) Start() {
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
func (s *BadgerNonceStore) Stop() {
	s.once.Do(func() { close(s.stopCh) })
}

func (s *BadgerNonceStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-nonceTTL).Unix()
	for key, ts := range s.local {
		if ts < cutoff {
			delete(s.local, key)
		}
	}
}
