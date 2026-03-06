// Package platform implements the AetherNet developer platform layer.
//
// Third-party developers building applications on top of AetherNet use API
// keys to authenticate, track usage, and access tier-specific rate limits.
// Keys are in-memory only (no persistence); they are recreated from config or
// env on restart — appropriate for a testnet developer platform.
package platform

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

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

// KeyManager manages developer API keys in memory. It is safe for concurrent use.
type KeyManager struct {
	mu   sync.RWMutex
	keys map[string]*APIKey // keyed by API key string
}

// NewKeyManager returns an empty, ready-to-use KeyManager.
func NewKeyManager() *KeyManager {
	return &KeyManager{
		keys: make(map[string]*APIKey),
	}
}

// GenerateKey creates and stores a new API key for a developer application.
// The key is prefixed with "aet_" followed by 64 hex-encoded random bytes.
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
// deactivated, false if the key was not found.
func (km *KeyManager) Revoke(keyStr string) bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	key, ok := km.keys[keyStr]
	if !ok {
		return false
	}
	key.Active = false
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
