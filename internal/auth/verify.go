// Package auth provides Ed25519 request signature verification for the
// AetherNet protocol API.
//
// Clients sign a canonical string (AETHERNET-REQUEST-V1) constructed from
// the HTTP method, path, agent ID, nonce, timestamp, and body SHA-256.
// The server reconstructs the same string and verifies the signature
// against the agent's registered public key in the identity registry.
//
// Layer: Infrastructure — importable by any layer.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/identity"
)

// Clock skew tolerance: requests must have a timestamp within ±30 seconds
// of the server's clock.
const maxTimestampSkew = 30

// Sentinel errors returned by VerifyRequest.
var (
	ErrMissingHeader     = errors.New("auth: missing required header")
	ErrTimestampExpired  = errors.New("auth: timestamp outside ±30s window")
	ErrInvalidNonce      = errors.New("auth: invalid nonce format")
	ErrUnknownAgent      = errors.New("auth: agent not registered")
	ErrInvalidSignature  = errors.New("auth: invalid signature")
	ErrReplayedNonce     = errors.New("auth: nonce already used")
	ErrMalformedSig      = errors.New("auth: malformed signature encoding")
)

// RequestAuth holds the verified identity context extracted from a signed request.
type RequestAuth struct {
	AgentID   crypto.AgentID
	PublicKey []byte
	Timestamp int64
	Nonce     string
}

// agentRegistry looks up an agent's public key.
type agentRegistry interface {
	Get(crypto.AgentID) (*identity.CapabilityFingerprint, error)
}

// NonceStore tracks used nonces for replay protection.
// Implementations must be safe for concurrent use.
type NonceStore interface {
	// CheckAndStore atomically checks if a nonce has been used for this agent
	// and stores it if not. Returns true if the nonce was already used (replay).
	CheckAndStore(agentID crypto.AgentID, nonce string) (replayed bool, err error)
}

// VerifyRequest verifies the Ed25519 signature on a write request.
// Returns the verified auth context, or an error describing why verification failed.
func VerifyRequest(r *http.Request, body []byte, registry agentRegistry, nonceStore NonceStore) (*RequestAuth, error) {
	// 1. Extract headers.
	agentIDStr := r.Header.Get("X-Aethernet-Agent-ID")
	timestampStr := r.Header.Get("X-Aethernet-Timestamp")
	nonce := r.Header.Get("X-Aethernet-Nonce")
	sigHex := r.Header.Get("X-Aethernet-Signature")

	// 2. Validate all required headers present.
	if agentIDStr == "" {
		return nil, fmt.Errorf("%w: X-Aethernet-Agent-ID", ErrMissingHeader)
	}
	if timestampStr == "" {
		return nil, fmt.Errorf("%w: X-Aethernet-Timestamp", ErrMissingHeader)
	}
	if nonce == "" {
		return nil, fmt.Errorf("%w: X-Aethernet-Nonce", ErrMissingHeader)
	}
	if sigHex == "" {
		return nil, fmt.Errorf("%w: X-Aethernet-Signature", ErrMissingHeader)
	}

	// 3. Validate timestamp within ±30 seconds.
	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid timestamp", ErrTimestampExpired)
	}
	now := time.Now().Unix()
	if abs(now-timestamp) > maxTimestampSkew {
		return nil, fmt.Errorf("%w: skew %ds", ErrTimestampExpired, now-timestamp)
	}

	// 4. Validate nonce format (32 hex chars = 16 bytes).
	if len(nonce) != 32 {
		return nil, fmt.Errorf("%w: expected 32 hex chars, got %d", ErrInvalidNonce, len(nonce))
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return nil, fmt.Errorf("%w: not valid hex", ErrInvalidNonce)
	}

	// 5. Hash the request body.
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	// 6. Reconstruct canonical signing string.
	agentID := crypto.AgentID(agentIDStr)
	canonical := BuildCanonicalString(r.Method, r.URL.Path, agentIDStr, nonce, timestamp, bodyHashHex)

	// 7. Look up agent's public key.
	fp, err := registry.Get(agentID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAgent, agentID)
	}
	if len(fp.PublicKey) == 0 {
		return nil, fmt.Errorf("%w: no public key for %s", ErrUnknownAgent, agentID)
	}

	// 8. Decode and verify signature.
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedSig, err)
	}
	if !crypto.Verify(fp.PublicKey, canonical, sig) {
		return nil, ErrInvalidSignature
	}

	// 9. Check nonce replay.
	if nonceStore != nil {
		replayed, err := nonceStore.CheckAndStore(agentID, nonce)
		if err != nil {
			return nil, fmt.Errorf("auth: nonce store error: %w", err)
		}
		if replayed {
			return nil, ErrReplayedNonce
		}
	}

	return &RequestAuth{
		AgentID:   agentID,
		PublicKey: fp.PublicKey,
		Timestamp: timestamp,
		Nonce:     nonce,
	}, nil
}

// BuildCanonicalString constructs the canonical signing string from request
// components. This function must produce identical output in Go, JavaScript,
// and Python for the same inputs.
func BuildCanonicalString(method, path, agentID, nonce string, timestamp int64, bodyHash string) []byte {
	s := "AETHERNET-REQUEST-V1\n" +
		"method:" + method + "\n" +
		"path:" + path + "\n" +
		"agent_id:" + agentID + "\n" +
		"nonce:" + nonce + "\n" +
		"timestamp:" + strconv.FormatInt(timestamp, 10) + "\n" +
		"body_sha256:" + bodyHash
	return []byte(s)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
