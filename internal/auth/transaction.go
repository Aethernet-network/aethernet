package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// TxVersion is the version string for the AETHERNET-TX-V1 signing scheme.
const TxVersion = "AETHERNET-TX-V1"

// DefaultChainID returns the chain_id based on the AETHERNET_TESTNET env var.
func DefaultChainID() string {
	if os.Getenv("AETHERNET_TESTNET") == "true" {
		return "aethernet-testnet-1"
	}
	return "aethernet-mainnet-1"
}

// TxMaxLifetimeSecs is the maximum allowed lifetime of a transaction (expires_at - created_at).
const TxMaxLifetimeSecs = 120

// TxClockSkewSecs is the maximum allowed clock skew between client and server.
const TxClockSkewSecs = 60

// Transaction is the AETHERNET-TX-V1 signing envelope.
type Transaction struct {
	Version    string `json:"version"`
	ChainID    string `json:"chain_id"`
	Actor      string `json:"actor"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	BodySHA256 string `json:"body_sha256"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Nonce      string `json:"nonce"`
}

// SignBytes returns the canonical bytes to be signed.
// This is the JCS-canonicalized JSON of the Transaction struct.
func (tx *Transaction) SignBytes() ([]byte, error) {
	raw, err := json.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("tx: marshal: %w", err)
	}
	return CanonicalizeJSON(raw)
}

// TxID returns SHA-256(SignBytes) as a hex string.
// Deterministic: same transaction always produces the same TxID.
func (tx *Transaction) TxID() (string, error) {
	sb, err := tx.SignBytes()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(sb)
	return hex.EncodeToString(h[:]), nil
}

// Sign signs the transaction with an Ed25519 private key.
// Returns the signature as hex-encoded bytes.
func (tx *Transaction) Sign(privateKey ed25519.PrivateKey) (string, error) {
	sb, err := tx.SignBytes()
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(privateKey, sb)
	return hex.EncodeToString(sig), nil
}

// Verify checks the Ed25519 signature against the provided public key.
func (tx *Transaction) Verify(publicKey ed25519.PublicKey, signatureHex string) error {
	sig, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("tx: decode signature: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("tx: invalid signature length: %d", len(sig))
	}
	sb, err := tx.SignBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, sb, sig) {
		return fmt.Errorf("tx: signature verification failed")
	}
	return nil
}
