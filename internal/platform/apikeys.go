// Package platform implements the AetherNet developer platform layer.
//
// Third-party developers building applications on top of AetherNet use API
// keys to authenticate, track usage, and access tier-specific rate limits.
// Keys are persisted to the BadgerDB store via a keyStore interface so they
// survive node restarts.
package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// keyStore is the persistence interface used by KeyManager.
// *store.Store from the store package satisfies this.
type keyStore interface {
	PutAPIKey(key string, data []byte) error
	GetAPIKey(key string) ([]byte, error)
	AllAPIKeys() (map[string][]byte, error)
}

// Tier identifies a developer API key tier and its rate limit class.
type Tier string

const (
	TierFree       Tier = "free"       // 100 req/min
	TierDeveloper  Tier = "developer"  // 1000 req/min
	TierEnterprise Tier = "enterprise" // 10000 req/min
)

// APIKey holds metadata and usage statistics for a single developer API key.
type APIKey struct {
	Key          string `json:"key"`
	Name         string `json:"name"`          // application name
	Tier         Tier   `json:"tier"`
	OwnerEmail   string `json:"owner_email"`
	CreatedAt    int64  `json:"created_at"`
	LastUsed     int64  `json:"last_used"`
	RequestCount uint64 `json:"request_count"`
	Active       bool   `json:"active"`
}

// KeyManager manages developer API keys. It is safe for concurrent use.
// Keys are persisted to the store when SetStore is called.
type KeyManager struct {
	mu    sync.RWMutex
	keys  map[string]*APIKey // keyed by API key string
	store keyStore           // optional; nil = in-memory only
}

// NewKeyManager returns an empty, ready-to-use KeyManager.
func NewKeyManager() *KeyManager {
	return &KeyManager{
		keys: make(map[string]*APIKey),
	}
}

// SetStore attaches a persistence backend. After this call GenerateKey and
// Revoke write through to the store. Call LoadFromStore afterwards to
// restore previously-issued keys.
func (km *KeyManager) SetStore(s keyStore) {
	km.mu.Lock()
	defer km.mu.Unlock()
	km.store = s
}

// LoadFromStore restores API keys from the persistence store.
// Call after SetStore on node restart.
func (km *KeyManager) LoadFromStore(s keyStore) error {
	all, err := s.AllAPIKeys()
	if err != nil {
		return err
	}
	km.mu.Lock()
	defer km.mu.Unlock()
	for _, blob := range all {
		var k APIKey
		if err := json.Unmarshal(blob, &k); err == nil {
			km.keys[k.Key] = &k
		}
	}
	return nil
}

// GenerateKey creates and stores a new API key for a developer application.
// The key is prefixed with "aet_" followed by 64 hex-encoded random bytes.
// When a store is configured, the key is persisted immediately.
func (km *KeyManager) GenerateKey(name, email string, tier Tier) *APIKey {
	km.mu.Lock()
	defer km.mu.Unlock()

	keyBytes := make([]byte, 32)
	_, _ = rand.Read(keyBytes)
	keyStr := "aet_" + hex.EncodeToString(keyBytes)

	key := &APIKey{
		Key:        keyStr,
		Name:       name,
		Tier:       tier,
		OwnerEmail: email,
		CreatedAt:  time.Now().Unix(),
		Active:     true,
	}
	km.keys[keyStr] = key
	if km.store != nil {
		if data, err := json.Marshal(key); err == nil {
			_ = km.store.PutAPIKey(keyStr, data)
		}
	}
	return key
}

// Validate checks whether a key is known and active, and records usage.
// Returns the key and true on success; nil and false when unknown or revoked.
func (km *KeyManager) Validate(keyStr string) (*APIKey, bool) {
	km.mu.Lock()
	defer km.mu.Unlock()

	key, ok := km.keys[keyStr]
	if !ok || !key.Active {
		return nil, false
	}
	key.LastUsed = time.Now().Unix()
	key.RequestCount++
	return key, true
}

// GetKey returns the key metadata without recording usage. Returns false when
// the key is not known (regardless of active status).
func (km *KeyManager) GetKey(keyStr string) (*APIKey, bool) {
	km.mu.RLock()
	defer km.mu.RUnlock()
	key, ok := km.keys[keyStr]
	return key, ok
}

// Revoke deactivates an API key. Returns true if the key existed and was
// deactivated, false if the key was not found. When a store is configured,
// the updated key record is persisted.
func (km *KeyManager) Revoke(keyStr string) bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	key, ok := km.keys[keyStr]
	if !ok {
		return false
	}
	key.Active = false
	if km.store != nil {
		if data, err := json.Marshal(key); err == nil {
			_ = km.store.PutAPIKey(keyStr, data)
		}
	}
	return true
}

// RateLimit returns the requests-per-minute limit for a tier.
// Unknown tiers default to the free-tier limit.
func RateLimit(tier Tier) int {
	switch tier {
	case TierFree:
		return 100
	case TierDeveloper:
		return 1000
	case TierEnterprise:
		return 10000
	default:
		return 100
	}
}

// Stats returns aggregate usage statistics across all tracked keys.
func (km *KeyManager) Stats() map[string]any {
	km.mu.RLock()
	defer km.mu.RUnlock()

	var totalRequests uint64
	active := 0
	for _, key := range km.keys {
		if key.Active {
			active++
		}
		totalRequests += key.RequestCount
	}
	return map[string]any{
		"total_keys":     len(km.keys),
		"active_keys":    active,
		"total_requests": totalRequests,
	}
}
