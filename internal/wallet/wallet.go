// Package wallet manages the mapping between human-readable deposit addresses
// and internal agent IDs.
//
// An Address is derived deterministically from an agent's Ed25519 public key,
// allowing external parties (exchanges, other protocols, humans) to send AET
// to an agent without knowing the agent's internal ID.
package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// Address is a human-readable deposit address for an AetherNet agent.
// Format: "aet1" followed by the first 40 hex characters of SHA-256(publicKey).
type Address string

// DeriveAddress generates a deterministic deposit address from an Ed25519 public key.
// The same public key always produces the same address.
func DeriveAddress(pubKey []byte) Address {
	hash := sha256.Sum256(pubKey)
	return Address("aet1" + hex.EncodeToString(hash[:])[:40])
}

// Wallet maintains a bidirectional mapping between deposit addresses and agent IDs.
// It is safe for concurrent use.
type Wallet struct {
	mu        sync.RWMutex
	addresses map[Address]crypto.AgentID    // address → agent
	agents    map[crypto.AgentID]Address    // agent → address
}

// New returns an empty Wallet.
func New() *Wallet {
	return &Wallet{
		addresses: make(map[Address]crypto.AgentID),
		agents:    make(map[crypto.AgentID]Address),
	}
}

// Register creates (or refreshes) the deposit address for agentID derived from
// pubKey. Returns the resulting deposit address.
func (w *Wallet) Register(agentID crypto.AgentID, pubKey []byte) Address {
	addr := DeriveAddress(pubKey)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.addresses[addr] = agentID
	w.agents[agentID] = addr
	return addr
}

// Resolve returns the AgentID that owns the given deposit address.
// The second return value is false when the address is unknown.
func (w *Wallet) Resolve(addr Address) (crypto.AgentID, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	id, ok := w.addresses[addr]
	return id, ok
}

// AddressOf returns the deposit address registered for agentID.
// The second return value is false when the agent has not been registered.
func (w *Wallet) AddressOf(agentID crypto.AgentID) (Address, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	addr, ok := w.agents[agentID]
	return addr, ok
}
