package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// VerifiedTx holds the result of a successful AETHERNET-TX-V1 verification.
type VerifiedTx struct {
	TxID      string       // SHA-256(sign bytes), hex
	Actor     string       // verified signer identity
	Tx        *Transaction // the validated transaction envelope
	Signature string       // hex-encoded Ed25519 signature
}

// Sentinel errors for transaction verification.
var (
	ErrTxMissingHeader  = fmt.Errorf("tx: missing required header")
	ErrTxChainMismatch  = fmt.Errorf("tx: chain_id mismatch")
	ErrTxExpired        = fmt.Errorf("tx: transaction expired")
	ErrTxFuture         = fmt.Errorf("tx: created_at is in the future")
	ErrTxLifetimeExceed = fmt.Errorf("tx: lifetime exceeds maximum")
	ErrTxBodyMismatch   = fmt.Errorf("tx: body_sha256 mismatch")
	ErrTxBadSignature   = fmt.Errorf("tx: signature verification failed")
	ErrTxBadNonce       = fmt.Errorf("tx: invalid nonce format")
)

// VerifyTransaction validates an AETHERNET-TX-V1 request.
// lookupPubKey resolves an actor identifier to an Ed25519 public key.
// body is the raw request body bytes (will be JCS-canonicalized for hash comparison).
func VerifyTransaction(
	r *http.Request,
	body []byte,
	chainID string,
	lookupPubKey func(actor string) (ed25519.PublicKey, error),
) (*VerifiedTx, error) {
	// 1. Extract headers.
	version := r.Header.Get("X-AetherNet-Version")
	hChainID := r.Header.Get("X-AetherNet-Chain-ID")
	actor := r.Header.Get("X-AetherNet-Actor")
	createdStr := r.Header.Get("X-AetherNet-Created")
	expiresStr := r.Header.Get("X-AetherNet-Expires")
	nonce := r.Header.Get("X-AetherNet-Nonce")
	sigHex := r.Header.Get("X-AetherNet-Signature")

	if version == "" {
		return nil, fmt.Errorf("%w: X-AetherNet-Version", ErrTxMissingHeader)
	}
	if actor == "" {
		return nil, fmt.Errorf("%w: X-AetherNet-Actor", ErrTxMissingHeader)
	}
	if createdStr == "" {
		return nil, fmt.Errorf("%w: X-AetherNet-Created", ErrTxMissingHeader)
	}
	if expiresStr == "" {
		return nil, fmt.Errorf("%w: X-AetherNet-Expires", ErrTxMissingHeader)
	}
	if nonce == "" {
		return nil, fmt.Errorf("%w: X-AetherNet-Nonce", ErrTxMissingHeader)
	}
	if sigHex == "" {
		return nil, fmt.Errorf("%w: X-AetherNet-Signature", ErrTxMissingHeader)
	}

	// 2. Verify chain_id.
	if hChainID != chainID {
		return nil, fmt.Errorf("%w: got %q, want %q", ErrTxChainMismatch, hChainID, chainID)
	}

	// 3. Parse timestamps.
	createdAt, err := strconv.ParseInt(createdStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("tx: invalid created_at: %w", err)
	}
	expiresAt, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("tx: invalid expires_at: %w", err)
	}

	now := time.Now().Unix()

	// Check lifetime.
	if expiresAt-createdAt > TxMaxLifetimeSecs {
		return nil, fmt.Errorf("%w: %ds > %ds", ErrTxLifetimeExceed, expiresAt-createdAt, TxMaxLifetimeSecs)
	}

	// Check not expired (with clock skew tolerance).
	if now > expiresAt+TxClockSkewSecs {
		return nil, fmt.Errorf("%w: expired %ds ago", ErrTxExpired, now-expiresAt)
	}

	// Check not too far in the future.
	if createdAt > now+TxClockSkewSecs {
		return nil, fmt.Errorf("%w: %ds in the future", ErrTxFuture, createdAt-now)
	}

	// 4. Validate nonce format (32 hex chars = 128 bits).
	if len(nonce) != 32 {
		return nil, fmt.Errorf("%w: expected 32 hex chars, got %d", ErrTxBadNonce, len(nonce))
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return nil, fmt.Errorf("%w: not valid hex", ErrTxBadNonce)
	}

	// 5. Compute body_sha256 from JCS-canonicalized body.
	canonBody, err := CanonicalizeJSON(body)
	if err != nil {
		// Body may not be JSON (empty or malformed) — hash raw bytes.
		canonBody = body
	}
	bodyHash := sha256.Sum256(canonBody)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	// 6. Reconstruct the transaction.
	tx := &Transaction{
		Version:    version,
		ChainID:    hChainID,
		Actor:      actor,
		Method:     r.Method,
		Path:       r.URL.Path,
		BodySHA256: bodyHashHex,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
		Nonce:      nonce,
	}

	// 7. Look up public key.
	pubKey, err := lookupPubKey(actor)
	if err != nil {
		return nil, fmt.Errorf("tx: unknown actor %q: %w", actor, err)
	}

	// 8. Verify signature.
	if err := tx.Verify(pubKey, sigHex); err != nil {
		return nil, ErrTxBadSignature
	}

	// 9. Compute TxID.
	txID, err := tx.TxID()
	if err != nil {
		return nil, fmt.Errorf("tx: compute txid: %w", err)
	}

	return &VerifiedTx{
		TxID:      txID,
		Actor:     actor,
		Tx:        tx,
		Signature: sigHex,
	}, nil
}
