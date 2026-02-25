package crypto_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/event"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mustGenerateKeyPair generates a keypair or fails the test immediately.
func mustGenerateKeyPair(t *testing.T) *crypto.KeyPair {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	return kp
}

// makeEvent creates an unsigned event attributed to kp.
func makeEvent(t *testing.T, kp *crypto.KeyPair) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeTransfer,
		nil,
		event.TransferPayload{
			FromAgent: string(kp.AgentID()),
			ToAgent:   "sink",
			Amount:    1,
			Currency:  "AET",
		},
		string(kp.AgentID()),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	return e
}

// makeSignedEvent creates an event that has been signed by kp.
func makeSignedEvent(t *testing.T, kp *crypto.KeyPair) *event.Event {
	t.Helper()
	e := makeEvent(t, kp)
	if err := crypto.SignEvent(e, kp); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}
	return e
}

// ---------------------------------------------------------------------------
// GenerateKeyPair
// ---------------------------------------------------------------------------

func TestGenerateKeyPair_ReturnsValidKeyPair(t *testing.T) {
	kp := mustGenerateKeyPair(t)

	if len(kp.PrivateKey) != 64 {
		t.Errorf("PrivateKey len = %d, want 64 (Ed25519 private key size)", len(kp.PrivateKey))
	}
	if len(kp.PublicKey) != 32 {
		t.Errorf("PublicKey len = %d, want 32 (Ed25519 public key size)", len(kp.PublicKey))
	}
}

func TestGenerateKeyPair_ProducesDistinctPairs(t *testing.T) {
	// Two sequential calls must produce different keypairs — the entropy source
	// must not be degenerate or predictably seeded.
	kp1 := mustGenerateKeyPair(t)
	kp2 := mustGenerateKeyPair(t)

	if kp1.AgentID() == kp2.AgentID() {
		t.Error("two GenerateKeyPair calls produced the same AgentID — entropy source may be broken")
	}
}

// ---------------------------------------------------------------------------
// AgentID
// ---------------------------------------------------------------------------

func TestAgentID_Is64HexChars(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	id := string(kp.AgentID())

	if len(id) != 64 {
		t.Errorf("AgentID len = %d, want 64 (hex-encoded 32-byte Ed25519 public key)", len(id))
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("AgentID %q contains non-lowercase-hex character %q", id, c)
			break
		}
	}
}

func TestAgentID_Deterministic(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	// Multiple calls on the same keypair must return the same value.
	if kp.AgentID() != kp.AgentID() {
		t.Error("AgentID returned different values on consecutive calls for the same keypair")
	}
}

func TestAgentID_DifferentForDifferentKeyPairs(t *testing.T) {
	kp1, kp2 := mustGenerateKeyPair(t), mustGenerateKeyPair(t)
	if kp1.AgentID() == kp2.AgentID() {
		t.Error("different keypairs produced the same AgentID")
	}
}

// ---------------------------------------------------------------------------
// Sign / Verify (raw bytes)
// ---------------------------------------------------------------------------

func TestSign_Verify_Roundtrip(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	data := []byte("canonical event bytes for signing")

	sig, err := kp.Sign(data)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) != 64 {
		t.Errorf("signature len = %d, want 64 (Ed25519 signature size)", len(sig))
	}
	if !crypto.Verify(kp.PublicKey, data, sig) {
		t.Error("Verify returned false for a signature just produced by Sign")
	}
}

func TestVerify_TamperedData(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	original := []byte("original payload")
	sig, _ := kp.Sign(original)

	tampered := []byte("tampered payload")
	if crypto.Verify(kp.PublicKey, tampered, sig) {
		t.Error("Verify returned true for signature checked against tampered data")
	}
}

func TestVerify_WrongKey(t *testing.T) {
	kp1 := mustGenerateKeyPair(t)
	kp2 := mustGenerateKeyPair(t)
	data := []byte("a message")
	sig, _ := kp1.Sign(data)

	if crypto.Verify(kp2.PublicKey, data, sig) {
		t.Error("Verify returned true when checked against a different keypair's public key")
	}
}

func TestVerify_TamperedSignature(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	data := []byte("data")
	sig, _ := kp.Sign(data)

	// Flip one bit in the middle of the signature.
	sig[32] ^= 0xFF
	if crypto.Verify(kp.PublicKey, data, sig) {
		t.Error("Verify returned true for a bit-flipped signature")
	}
}

func TestVerify_ShortSignature(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	data := []byte("data")
	if crypto.Verify(kp.PublicKey, data, []byte("tooshort")) {
		t.Error("Verify returned true for an undersized signature")
	}
}

func TestVerify_NilSignature(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	if crypto.Verify(kp.PublicKey, []byte("data"), nil) {
		t.Error("Verify returned true for a nil signature")
	}
}

func TestVerify_ShortPublicKey(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	data := []byte("data")
	sig, _ := kp.Sign(data)
	if crypto.Verify([]byte("shortkey"), data, sig) {
		t.Error("Verify returned true for an undersized public key")
	}
}

func TestVerify_NilPublicKey(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	data := []byte("data")
	sig, _ := kp.Sign(data)
	if crypto.Verify(nil, data, sig) {
		t.Error("Verify returned true for a nil public key")
	}
}

// ---------------------------------------------------------------------------
// CanonicalBytes
// ---------------------------------------------------------------------------

func TestCanonicalBytes_Deterministic(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeEvent(t, kp)

	b1, err := crypto.CanonicalBytes(e)
	if err != nil {
		t.Fatalf("CanonicalBytes first call: %v", err)
	}
	b2, err := crypto.CanonicalBytes(e)
	if err != nil {
		t.Fatalf("CanonicalBytes second call: %v", err)
	}
	if string(b1) != string(b2) {
		t.Error("CanonicalBytes produced different output on consecutive calls for the same event")
	}
}

func TestCanonicalBytes_DifferentForDifferentEvents(t *testing.T) {
	kp := mustGenerateKeyPair(t)

	e1, _ := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		string(kp.AgentID()), nil, 0,
	)
	e2, _ := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 2, Currency: "AET"},
		string(kp.AgentID()), nil, 0,
	)

	b1, _ := crypto.CanonicalBytes(e1)
	b2, _ := crypto.CanonicalBytes(e2)
	if string(b1) == string(b2) {
		t.Error("CanonicalBytes produced identical output for two events with different payloads")
	}
}

func TestCanonicalBytes_MatchesEventComputeID(t *testing.T) {
	// Critical invariant: sha256(CanonicalBytes(e)) must equal e.ID.
	//
	// This proves that SignEvent and event.ComputeID commit to the exact same
	// content. If a signed event is in the DAG, the signature covers precisely
	// the content that was hashed to produce the event's address. There is no
	// gap between "what was signed" and "what is identified".
	kp := mustGenerateKeyPair(t)
	e, err := event.New(
		event.EventTypeGeneration,
		nil,
		event.GenerationPayload{
			GeneratingAgent:  string(kp.AgentID()),
			BeneficiaryAgent: "client",
			ClaimedValue:     1000,
			EvidenceHash:     "sha256:deadbeef",
			TaskDescription:  "canonical bytes test",
		},
		string(kp.AgentID()),
		nil,
		250,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	canonical, err := crypto.CanonicalBytes(e)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}

	sum := sha256.Sum256(canonical)
	derived := hex.EncodeToString(sum[:])

	if string(e.ID) != derived {
		t.Errorf(
			"sha256(CanonicalBytes(e)) = %q\n"+
				"                  e.ID  = %q\n"+
				"These must be equal — signing and ID-computation must cover the same content.",
			derived, e.ID,
		)
	}
}

// ---------------------------------------------------------------------------
// SignEvent
// ---------------------------------------------------------------------------

func TestSignEvent_PopulatesSignature(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	if len(e.Signature) != 64 {
		t.Errorf("e.Signature len = %d after SignEvent, want 64", len(e.Signature))
	}
}

func TestSignEvent_DoesNotChangeEventID(t *testing.T) {
	// Signature is excluded from the content hash, so signing must not change e.ID.
	// This allows the event to be signed after creation without invalidating the
	// content-addressed identity it was given at construction time.
	kp := mustGenerateKeyPair(t)
	e := makeEvent(t, kp)
	idBefore := e.ID

	if err := crypto.SignEvent(e, kp); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}
	if e.ID != idBefore {
		t.Errorf("event ID changed after SignEvent: %q → %q", idBefore, e.ID)
	}
}

func TestSignEvent_AgentIDMismatch_ReturnsError(t *testing.T) {
	kp1 := mustGenerateKeyPair(t)
	kp2 := mustGenerateKeyPair(t)

	// Event claims kp1's identity.
	e, _ := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		string(kp1.AgentID()),
		nil, 0,
	)

	// Attempt to sign with kp2 — should be rejected.
	err := crypto.SignEvent(e, kp2)
	if err == nil {
		t.Error("SignEvent should return an error when e.AgentID does not match kp.AgentID(), got nil")
	}
	// The event must remain unsigned.
	if len(e.Signature) != 0 {
		t.Error("SignEvent populated e.Signature despite AgentID mismatch — event is now inconsistent")
	}
}

// ---------------------------------------------------------------------------
// VerifyEvent
// ---------------------------------------------------------------------------

func TestVerifyEvent_ValidSignedEvent(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	if !crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned false for a correctly signed event")
	}
}

func TestVerifyEvent_UnsignedEvent_ReturnsFalse(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeEvent(t, kp) // not signed

	if crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned true for an unsigned event")
	}
}

func TestVerifyEvent_TamperedType_ReturnsFalse(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	e.Type = event.EventTypeGeneration // change after signing
	if crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned true after Type was modified post-signing")
	}
}

func TestVerifyEvent_TamperedStakeAmount_ReturnsFalse(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	e.StakeAmount = 99999 // change canonical field after signing
	if crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned true after StakeAmount was modified post-signing")
	}
}

func TestVerifyEvent_TamperedAgentID_ReturnsFalse(t *testing.T) {
	// Replacing AgentID with another valid key's ID fails verification in two ways:
	// (1) the canonical bytes now include the wrong AgentID so the content is different,
	// (2) we try to verify with the wrong public key.
	kp1 := mustGenerateKeyPair(t)
	kp2 := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp1)

	e.AgentID = string(kp2.AgentID())
	if crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned true after AgentID was replaced with a different key's ID")
	}
}

func TestVerifyEvent_MalformedAgentID_ReturnsFalse(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	e.AgentID = "not-valid-hex!!"
	if crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned true for an event with a non-hex AgentID")
	}
}

func TestVerifyEvent_TamperedSignatureBytes_ReturnsFalse(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	// Flip a byte in the middle of the signature — a 1-bit change is sufficient.
	e.Signature[32] ^= 0xFF
	if crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned true after signature bytes were tampered")
	}
}

func TestVerifyEvent_SettlementStateChange_StillValid(t *testing.T) {
	// SettlementState is intentionally excluded from canonical content so that
	// the OCS engine can advance events through the Optimistic → Settled → Adjusted
	// lifecycle without requiring re-signing at each step.
	// Changing SettlementState must not invalidate the signature.
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	if err := event.Transition(e, event.SettlementSettled); err != nil {
		t.Fatalf("Transition to Settled: %v", err)
	}
	if !crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned false after SettlementState was advanced to Settled — should still be valid")
	}

	if err := event.Transition(e, event.SettlementAdjusted); err != nil {
		t.Fatalf("Transition to Adjusted: %v", err)
	}
	if !crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned false after SettlementState was advanced to Adjusted — should still be valid")
	}
}

func TestVerifyEvent_IDChange_StillValid(t *testing.T) {
	// ID is the hash of the signed content — it is not itself part of the signed
	// content (that would be circular). ID tampering is detectable via event.ComputeID,
	// not via VerifyEvent. Changing e.ID must not affect signature validity.
	kp := mustGenerateKeyPair(t)
	e := makeSignedEvent(t, kp)

	e.ID = event.EventID(strings.Repeat("f", 64)) // overwrite with a fake ID
	if !crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned false after e.ID was changed — ID is not part of signed content")
	}
}

func TestVerifyEvent_SignedByDifferentKey_ReturnsFalse(t *testing.T) {
	// An event whose Signature was produced by a different key than its AgentID claims
	// must fail verification. This is the core security guarantee of the signature scheme.
	kp1 := mustGenerateKeyPair(t)
	kp2 := mustGenerateKeyPair(t)

	// Create event for kp1, sign with kp2's key directly (bypassing SignEvent's guard).
	e := makeEvent(t, kp1)
	wrongSig, _ := kp2.Sign(func() []byte {
		b, _ := crypto.CanonicalBytes(e)
		return b
	}())
	e.Signature = wrongSig

	if crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned true for an event signed by the wrong key")
	}
}

// ---------------------------------------------------------------------------
// Save / LoadKeyPair
// ---------------------------------------------------------------------------

func TestSave_LoadKeyPair_Roundtrip(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	path := filepath.Join(t.TempDir(), "agent.key")
	const passphrase = "correct-horse-battery-staple"

	if err := kp.Save(path, passphrase); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := crypto.LoadKeyPair(path, passphrase)
	if err != nil {
		t.Fatalf("LoadKeyPair: %v", err)
	}

	if loaded.AgentID() != kp.AgentID() {
		t.Errorf("loaded AgentID = %q, want %q", loaded.AgentID(), kp.AgentID())
	}
	if string(loaded.PublicKey) != string(kp.PublicKey) {
		t.Error("loaded PublicKey does not match original")
	}
	if string(loaded.PrivateKey) != string(kp.PrivateKey) {
		t.Error("loaded PrivateKey does not match original")
	}
}

func TestSave_LoadKeyPair_LoadedKeyIsFunctional(t *testing.T) {
	// The loaded keypair must work end-to-end: sign an event and verify it.
	kp := mustGenerateKeyPair(t)
	path := filepath.Join(t.TempDir(), "agent.key")
	if err := kp.Save(path, "pass"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := crypto.LoadKeyPair(path, "pass")
	if err != nil {
		t.Fatalf("LoadKeyPair: %v", err)
	}

	e := makeEvent(t, loaded)
	if err := crypto.SignEvent(e, loaded); err != nil {
		t.Fatalf("SignEvent with loaded key: %v", err)
	}
	if !crypto.VerifyEvent(e) {
		t.Error("VerifyEvent returned false for event signed with the loaded keypair")
	}
}

func TestLoadKeyPair_WrongPassphrase_ReturnsError(t *testing.T) {
	kp := mustGenerateKeyPair(t)
	path := filepath.Join(t.TempDir(), "agent.key")
	if err := kp.Save(path, "correct"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := crypto.LoadKeyPair(path, "wrong")
	if err == nil {
		t.Fatal("LoadKeyPair should return an error for the wrong passphrase, got nil")
	}
}

func TestLoadKeyPair_FileNotFound_ReturnsError(t *testing.T) {
	_, err := crypto.LoadKeyPair("/nonexistent/path/key.json", "pass")
	if err == nil {
		t.Fatal("LoadKeyPair should return an error for a non-existent path, got nil")
	}
}

func TestSave_FilePermissions(t *testing.T) {
	// Key files must be written with mode 0600 — readable only by the owner.
	kp := mustGenerateKeyPair(t)
	path := filepath.Join(t.TempDir(), "agent.key")
	if err := kp.Save(path, "pass"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("key file permissions = %04o, want 0600", perm)
	}
}

func TestSave_EachCallProducesDistinctCiphertext(t *testing.T) {
	// Each Save generates fresh random salt and nonce. Two Saves of the same
	// keypair with the same passphrase must produce different files — otherwise
	// an attacker who sees two key files can confirm they belong to the same
	// agent without breaking the encryption.
	kp := mustGenerateKeyPair(t)
	dir := t.TempDir()
	path1 := filepath.Join(dir, "key1.json")
	path2 := filepath.Join(dir, "key2.json")
	const passphrase = "same-passphrase-both-times"

	if err := kp.Save(path1, passphrase); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	if err := kp.Save(path2, passphrase); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	b1, _ := os.ReadFile(path1)
	b2, _ := os.ReadFile(path2)
	if string(b1) == string(b2) {
		t.Error("two Save calls with identical inputs produced identical files — salt/nonce are not random")
	}
}

func TestSave_LoadKeyPair_EmptyPassphrase(t *testing.T) {
	// An empty passphrase is valid (weak, but the API must not panic or error).
	kp := mustGenerateKeyPair(t)
	path := filepath.Join(t.TempDir(), "agent.key")
	if err := kp.Save(path, ""); err != nil {
		t.Fatalf("Save with empty passphrase: %v", err)
	}
	loaded, err := crypto.LoadKeyPair(path, "")
	if err != nil {
		t.Fatalf("LoadKeyPair with empty passphrase: %v", err)
	}
	if loaded.AgentID() != kp.AgentID() {
		t.Errorf("roundtrip with empty passphrase: AgentID mismatch")
	}
}
