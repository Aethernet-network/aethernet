// Package crypto provides cryptographic identity and signing primitives for AetherNet.
//
// # Identity model
//
// An AetherNet agent's identity is its Ed25519 public key, hex-encoded as an AgentID.
// Identity is therefore:
//   - Deterministic: the same keypair always produces the same AgentID.
//   - Self-sovereign: no central authority mints or revokes identity.
//   - Forgery-resistant: creating an event that claims a given AgentID requires the
//     corresponding private key (enforced by VerifyEvent in signing.go).
//
// # Key storage
//
// Private keys are stored encrypted with AES-256-GCM. The AES key is derived from
// a user-supplied passphrase using scrypt, which is memory-hard and resistant to
// GPU/ASIC-accelerated brute-force attacks. Each Save call generates fresh random
// salt and nonce, so identical passphrases produce distinct ciphertexts.
//
// # Ed25519
//
// Ed25519 is chosen for AetherNet because:
//   - 64-byte signatures are compact for embedding in every DAG event.
//   - Verification is fast (~50 µs on modern hardware), critical for DAG ingestion.
//   - The signing API does not require a random nonce (unlike ECDSA), eliminating
//     a class of implementation vulnerabilities.
//   - Batch verification (useful for bulk DAG validation) is well-supported.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/scrypt"
)

// scrypt parameters for passphrase-based key derivation.
// Values follow OWASP recommendations for interactive authentication:
//   - N=32768 (2^15): memory cost in KiB, ~32 MB per derivation, ~50 ms on modern hardware.
//   - r=8, p=1: standard recommended block and parallelisation parameters.
//
// Increasing N significantly hardens against offline brute-force at the cost of
// slower key loading. These constants are intentionally not runtime-configurable
// to prevent accidental weakening in production deployments.
const (
	scryptN   = 1 << 15 // 32768
	scryptR   = 8
	scryptP   = 1
	aesKeyLen = 32 // 256-bit key for AES-256-GCM
	saltLen   = 32 // random salt, unique per Save call, input to scrypt
	nonceLen  = 12 // AES-GCM standard nonce size (96 bits)
)

// keyFileVersion is the format version written to disk.
// Increment if the on-disk format changes incompatibly; LoadKeyPair rejects
// files with an unrecognised version.
const keyFileVersion = 1

// AgentID is a hex-encoded Ed25519 public key (64 hex characters = 32 bytes).
//
// It serves as an agent's base identity on AetherNet: stable across sessions,
// derived deterministically from the keypair, and impossible to forge without
// the corresponding private key. It is also the value stored in event.Event.AgentID.
type AgentID string

// KeyPair holds an Ed25519 keypair for an AetherNet agent.
// The zero value is not usable; obtain via GenerateKeyPair or LoadKeyPair.
type KeyPair struct {
	// PrivateKey is the full 64-byte Ed25519 private key in Go's encoding:
	// [0:32] = 32-byte seed, [32:64] = 32-byte public key (redundantly stored).
	PrivateKey ed25519.PrivateKey

	// PublicKey is the 32-byte Ed25519 public key. It is the canonical agent identity.
	PublicKey ed25519.PublicKey
}

// GenerateKeyPair generates a fresh Ed25519 keypair from a cryptographically
// secure random source (crypto/rand). Returns an error only if the OS entropy
// pool is unavailable, which is exceedingly rare.
func GenerateKeyPair() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to generate Ed25519 keypair: %w", err)
	}
	return &KeyPair{PrivateKey: priv, PublicKey: pub}, nil
}

// AgentID returns the hex-encoded public key as the agent's base identity fingerprint.
// The result is deterministic: calling AgentID on the same keypair always returns the
// same value. It is the value that should be supplied to event.New as the agentID argument.
func (kp *KeyPair) AgentID() AgentID {
	return AgentID(hex.EncodeToString(kp.PublicKey))
}

// Sign produces a 64-byte Ed25519 signature over data using the keypair's private key.
// The signature is deterministic for a given private key and message (Ed25519 uses
// deterministic nonce derivation, eliminating the failure mode present in ECDSA).
func (kp *KeyPair) Sign(data []byte) ([]byte, error) {
	if len(kp.PrivateKey) == 0 {
		return nil, errors.New("crypto: keypair has no private key")
	}
	return ed25519.Sign(kp.PrivateKey, data), nil
}

// Verify checks that signature is a valid Ed25519 signature over data produced by
// the private key corresponding to publicKey.
//
// Returns false — rather than panicking — for all invalid inputs:
//   - publicKey not exactly 32 bytes
//   - signature not exactly 64 bytes
//   - cryptographically invalid signature
func Verify(publicKey, data, signature []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize {
		return false
	}
	if len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(publicKey), data, signature)
}

// keyFile is the JSON envelope written to disk by Save.
// All byte fields are hex-encoded so the file is human-readable.
type keyFile struct {
	// Version identifies the on-disk format. LoadKeyPair rejects unknown versions.
	Version int `json:"version"`

	// PublicKey is stored in plaintext — it is the agent's public identity.
	PublicKey string `json:"public_key"`

	// Salt is a 32-byte random value used as input to scrypt. Unique per Save call.
	// Storing the salt allows the same passphrase to produce different ciphertexts
	// (preventing correlation between key files with the same passphrase).
	Salt string `json:"salt"`

	// Nonce is a 12-byte random value for AES-GCM. Unique per Save call.
	// GCM nonce reuse with the same key is catastrophic; fresh salt per Save means
	// fresh AES keys per Save, but a fresh nonce is also generated for defence in depth.
	Nonce string `json:"nonce"`

	// Ciphertext is the AES-256-GCM encryption of the 64-byte Ed25519 private key.
	// The 16-byte GCM authentication tag is appended by gcm.Seal, making the total
	// stored length 80 bytes (160 hex characters). The tag provides both confidentiality
	// and integrity: a wrong passphrase causes authentication tag verification to fail,
	// giving a clear signal without leaking timing information.
	Ciphertext string `json:"ciphertext"`
}

// Save writes the keypair to path as a JSON file with the private key encrypted
// under AES-256-GCM. The AES key is derived from passphrase via scrypt.
//
// The file is written with mode 0600 (owner read/write only). Each call generates
// fresh random salt and nonce, so two Saves of the same keypair with the same
// passphrase produce distinct ciphertexts — safe to repeat.
func (kp *KeyPair) Save(path, passphrase string) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("crypto: failed to generate salt: %w", err)
	}

	aesKey, err := deriveKey(passphrase, salt)
	if err != nil {
		return err
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return fmt.Errorf("crypto: failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}

	// gcm.Seal appends the 16-byte authentication tag to the ciphertext.
	ciphertext := gcm.Seal(nil, nonce, kp.PrivateKey, nil)

	kf := keyFile{
		Version:    keyFileVersion,
		PublicKey:  hex.EncodeToString(kp.PublicKey),
		Salt:       hex.EncodeToString(salt),
		Nonce:      hex.EncodeToString(nonce),
		Ciphertext: hex.EncodeToString(ciphertext),
	}

	encoded, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return fmt.Errorf("crypto: failed to marshal key file: %w", err)
	}

	// 0600: private key files must not be world-readable.
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		return fmt.Errorf("crypto: failed to write key file %q: %w", path, err)
	}
	return nil
}

// LoadKeyPair reads a keypair from path and decrypts the private key with passphrase.
//
// Returns an error if:
//   - The file does not exist or cannot be read.
//   - The file is malformed JSON or has an unrecognised version.
//   - The passphrase is incorrect (AES-GCM authentication tag verification fails).
//   - The key file is corrupted (decrypted public key does not match stored public key).
func LoadKeyPair(path, passphrase string) (*KeyPair, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to read key file %q: %w", path, err)
	}

	var kf keyFile
	if err := json.Unmarshal(raw, &kf); err != nil {
		return nil, fmt.Errorf("crypto: failed to parse key file: %w", err)
	}
	if kf.Version != keyFileVersion {
		return nil, fmt.Errorf("crypto: unsupported key file version %d (want %d)", kf.Version, keyFileVersion)
	}

	pubBytes, err := hex.DecodeString(kf.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid public_key hex: %w", err)
	}
	salt, err := hex.DecodeString(kf.Salt)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid salt hex: %w", err)
	}
	nonce, err := hex.DecodeString(kf.Nonce)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid nonce hex: %w", err)
	}
	ciphertext, err := hex.DecodeString(kf.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid ciphertext hex: %w", err)
	}

	aesKey, err := deriveKey(passphrase, salt)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	// gcm.Open verifies the authentication tag. A wrong passphrase produces a
	// different AES key, which yields a different tag, causing this to fail.
	privBytes, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decryption failed — wrong passphrase or corrupted file: %w", err)
	}

	if len(privBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("crypto: decrypted key has wrong length %d (want %d)",
			len(privBytes), ed25519.PrivateKeySize)
	}

	privKey := ed25519.PrivateKey(privBytes)

	// Integrity check: the public key embedded in the private key (bytes 32–64 in
	// Go's ed25519 encoding) must match the public key stored in plaintext. A
	// mismatch indicates file corruption rather than a wrong passphrase.
	// subtle.ConstantTimeCompare is used to avoid timing side-channels.
	embeddedPub := privKey.Public().(ed25519.PublicKey)
	if subtle.ConstantTimeCompare([]byte(embeddedPub), pubBytes) != 1 {
		return nil, errors.New("crypto: public key mismatch — key file may be corrupted")
	}

	return &KeyPair{
		PrivateKey: privKey,
		PublicKey:  ed25519.PublicKey(pubBytes),
	}, nil
}

// deriveKey uses scrypt to derive a 256-bit AES key from a passphrase and salt.
// Parameters are taken from the package-level constants; they must not be weakened.
func deriveKey(passphrase string, salt []byte) ([]byte, error) {
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, aesKeyLen)
	if err != nil {
		return nil, fmt.Errorf("crypto: key derivation failed: %w", err)
	}
	return key, nil
}
