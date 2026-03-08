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
	"strings"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// Address is a human-readable deposit address for an AetherNet agent.
// Format: "aet1" followed by 40 hex chars of SHA-256(publicKey) and a
// 4-hex-char checksum (first 2 bytes of SHA-256 of the 40-char body).
// Total length: 4 + 40 + 4 = 48 characters.
type Address string

// DeriveAddress generates a deterministic deposit address from an Ed25519 public key.
// The address includes a 4-hex-char checksum so typos can be detected at input.
func DeriveAddress(pubKey []byte) Address {
	hash := sha256.Sum256(pubKey)
	body := hex.EncodeToString(hash[:])[:40]
	// Compute checksum: first 2 bytes of SHA-256 of the body string.
	checksumHash := sha256.Sum256([]byte(body))
	checksum := hex.EncodeToString(checksumHash[:])[:4]
	return Address("aet1" + body + checksum)
}

// ValidateAddress reports whether addr is a well-formed AetherNet deposit address
// with a valid embedded checksum. Returns false for legacy 44-char addresses
// (without checksum) or addresses with incorrect checksums.
func ValidateAddress(addr Address) bool {
	s := string(addr)
	if !strings.HasPrefix(s, "aet1") {
		return false
	}
	rest := s[4:] // strip "aet1"
	// New format: 40-char body + 4-char checksum = 44 chars total after prefix.
	if len(rest) != 44 {
		return false
	}
	body := rest[:40]
	checksum := rest[40:]
	checksumHash := sha256.Sum256([]byte(body))
	expected := hex.EncodeToString(checksumHash[:])[:4]
	return checksum == expected
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
