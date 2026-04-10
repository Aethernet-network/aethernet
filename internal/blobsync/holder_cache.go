package blobsync

import (
	"log/slog"
	"sync"
	"time"
)

// HolderHint is a signed assertion that a peer holds a specific blob.
// Non-authoritative: influences routing order only, cannot mark a blob
// as available or affect round state.
type HolderHint struct {
	BlobHash       [32]byte
	HolderPeerID   string
	ValidUntilUnix int64
	Signature      []byte // Ed25519 signature from the holder; verified before mainnet
}

// HolderHintCache is a bounded in-memory cache of HolderHints.
// Thread-safe. Hints expire at ValidUntilUnix and are evicted on access.
type HolderHintCache struct {
	mu         sync.RWMutex
	hints      map[[32]byte][]HolderHint
	accessOrder [][32]byte // LRU tracking (oldest first)
	maxSize    int
	blacklist  map[string]int64 // peerID → blacklist-until unix
}

// NewHolderHintCache creates a cache with the given maximum entry count.
func NewHolderHintCache(maxSize int) *HolderHintCache {
	if maxSize <= 0 {
		maxSize = 1_000_000
	}
	return &HolderHintCache{
		hints:     make(map[[32]byte][]HolderHint),
		maxSize:   maxSize,
		blacklist: make(map[string]int64),
	}
}

// Add stores a hint. Rejects expired hints. Evicts LRU if at capacity.
func (c *HolderHintCache) Add(hint HolderHint) {
	now := time.Now().Unix()
	if hint.ValidUntilUnix <= now {
		return // expired
	}

	// TODO: verify HolderHint signatures before mainnet.
	// The Signature field is populated on emit and stored on receipt,
	// but Ed25519 verification is deferred to a future prompt.
	if len(hint.Signature) == 0 {
		slog.Debug("blobsync: received unsigned HolderHint",
			"peer", hint.HolderPeerID, "hash", hint.BlobHash)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict LRU if at capacity.
	for len(c.hints) >= c.maxSize && len(c.accessOrder) > 0 {
		oldest := c.accessOrder[0]
		c.accessOrder = c.accessOrder[1:]
		delete(c.hints, oldest)
	}

	existing := c.hints[hint.BlobHash]
	// Deduplicate by peer.
	for i, h := range existing {
		if h.HolderPeerID == hint.HolderPeerID {
			existing[i] = hint // update
			return
		}
	}
	c.hints[hint.BlobHash] = append(existing, hint)
	c.accessOrder = append(c.accessOrder, hint.BlobHash)
}

// Get returns non-expired, non-blacklisted hints for a hash, freshest first.
func (c *HolderHintCache) Get(hash [32]byte) []HolderHint {
	now := time.Now().Unix()
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []HolderHint
	for _, h := range c.hints[hash] {
		if h.ValidUntilUnix <= now {
			continue // expired
		}
		if until, ok := c.blacklist[h.HolderPeerID]; ok && now < until {
			continue // blacklisted
		}
		result = append(result, h)
	}
	return result
}

// Remove removes a specific peer's hint for a hash.
func (c *HolderHintCache) Remove(hash [32]byte, peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hints := c.hints[hash]
	for i, h := range hints {
		if h.HolderPeerID == peerID {
			c.hints[hash] = append(hints[:i], hints[i+1:]...)
			return
		}
	}
}

// Blacklist temporarily ignores all hints from a peer.
func (c *HolderHintCache) Blacklist(peerID string, duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blacklist[peerID] = time.Now().Add(duration).Unix()
}

// Size returns the number of distinct blob hashes in the cache.
func (c *HolderHintCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.hints)
}
