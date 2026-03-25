package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// buildCanonicalString constructs the AETHERNET-REQUEST-V1 signing string.
// Must produce identical output as internal/auth.BuildCanonicalString and
// the Arena's JavaScript signing code.
func buildCanonicalString(method, path, agentID, nonce string, timestamp int64, bodyHash string) []byte {
	s := "AETHERNET-REQUEST-V1\n" +
		"method:" + method + "\n" +
		"path:" + path + "\n" +
		"agent_id:" + agentID + "\n" +
		"nonce:" + nonce + "\n" +
		"timestamp:" + strconv.FormatInt(timestamp, 10) + "\n" +
		"body_sha256:" + bodyHash
	return []byte(s)
}

// signRequest produces the X-Aethernet-* headers for an authenticated request.
func signRequest(method, path string, body []byte, agentID string, privateKey ed25519.PrivateKey) map[string]string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	nonceHex := hex.EncodeToString(nonce)

	timestamp := time.Now().Unix()
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	canonical := buildCanonicalString(method, path, agentID, nonceHex, timestamp, bodyHashHex)
	signature := ed25519.Sign(privateKey, canonical)

	return map[string]string{
		"X-Aethernet-Agent-ID":  agentID,
		"X-Aethernet-Timestamp": strconv.FormatInt(timestamp, 10),
		"X-Aethernet-Nonce":     nonceHex,
		"X-Aethernet-Signature": hex.EncodeToString(signature),
		"Content-Type":          "application/json",
	}
}

// ed25519SeedToPrivateKey converts a 32-byte seed to a 64-byte ed25519 private key.
func ed25519SeedToPrivateKey(seed []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(seed)
}

// ed25519AgentID returns the hex-encoded public key (agent ID) for a private key.
func ed25519AgentID(pk ed25519.PrivateKey) string {
	pub := pk.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub)
}

// randomHex returns n random bytes as a hex string.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}
