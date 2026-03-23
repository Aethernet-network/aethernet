package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/auth"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/identity"
)

// mockRegistry satisfies auth's agentRegistry interface.
type mockRegistry struct {
	fps map[crypto.AgentID]*identity.CapabilityFingerprint
}

func (m *mockRegistry) Get(id crypto.AgentID) (*identity.CapabilityFingerprint, error) {
	fp, ok := m.fps[id]
	if !ok {
		return nil, identity.ErrAgentNotFound
	}
	return fp, nil
}

// mockNonceStore is an in-memory nonce store for testing.
type mockNonceStore struct {
	seen map[string]bool
}

func newMockNonceStore() *mockNonceStore {
	return &mockNonceStore{seen: make(map[string]bool)}
}

func (s *mockNonceStore) CheckAndStore(agentID crypto.AgentID, nonce string) (bool, error) {
	key := string(agentID) + ":" + nonce
	if s.seen[key] {
		return true, nil
	}
	s.seen[key] = true
	return false, nil
}

// signedRequest creates a properly signed HTTP request for testing.
func signedRequest(t *testing.T, kp *crypto.KeyPair, method, path, body string, ts int64, nonce string) *http.Request {
	t.Helper()
	agentID := string(kp.AgentID())

	bodyBytes := []byte(body)
	bodyHash := sha256.Sum256(bodyBytes)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	canonical := auth.BuildCanonicalString(method, path, agentID, nonce, ts, bodyHashHex)
	sig, err := kp.Sign(canonical)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Aethernet-Agent-ID", agentID)
	r.Header.Set("X-Aethernet-Timestamp", strconv.FormatInt(ts, 10))
	r.Header.Set("X-Aethernet-Nonce", nonce)
	r.Header.Set("X-Aethernet-Signature", hex.EncodeToString(sig))
	return r
}

func makeRegistry(kp *crypto.KeyPair) *mockRegistry {
	agentID := kp.AgentID()
	fp, _ := identity.NewFingerprint(agentID, kp.PublicKey, nil)
	return &mockRegistry{fps: map[crypto.AgentID]*identity.CapabilityFingerprint{agentID: fp}}
}

const testNonce = "0123456789abcdef0123456789abcdef"

func TestVerifyRequest_Valid(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	reg := makeRegistry(kp)
	ns := newMockNonceStore()

	body := `{"amount": 1000}`
	now := time.Now().Unix()
	r := signedRequest(t, kp, "POST", "/v1/transfer", body, now, testNonce)

	result, err := auth.VerifyRequest(r, []byte(body), reg, ns)
	if err != nil {
		t.Fatalf("VerifyRequest: %v", err)
	}
	if result.AgentID != kp.AgentID() {
		t.Errorf("AgentID = %q; want %q", result.AgentID, kp.AgentID())
	}
	if result.Nonce != testNonce {
		t.Errorf("Nonce = %q; want %q", result.Nonce, testNonce)
	}
}

func TestVerifyRequest_TamperedBody(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	reg := makeRegistry(kp)
	ns := newMockNonceStore()

	body := `{"amount": 1000}`
	now := time.Now().Unix()
	r := signedRequest(t, kp, "POST", "/v1/transfer", body, now, testNonce)

	// Pass a different body for verification.
	tampered := []byte(`{"amount": 9999}`)
	_, err := auth.VerifyRequest(r, tampered, reg, ns)
	if err == nil {
		t.Fatal("expected error for tampered body")
	}
	if !strings.Contains(err.Error(), "invalid signature") {
		t.Errorf("error = %q; want invalid signature", err)
	}
}

func TestVerifyRequest_StaleTimestamp(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	reg := makeRegistry(kp)
	ns := newMockNonceStore()

	body := `{}`
	stale := time.Now().Unix() - 60 // 60 seconds old
	r := signedRequest(t, kp, "POST", "/v1/stake", body, stale, testNonce)

	_, err := auth.VerifyRequest(r, []byte(body), reg, ns)
	if err == nil {
		t.Fatal("expected error for stale timestamp")
	}
	if !strings.Contains(err.Error(), "timestamp") {
		t.Errorf("error = %q; want timestamp-related error", err)
	}
}

func TestVerifyRequest_ReplayedNonce(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	reg := makeRegistry(kp)
	ns := newMockNonceStore()

	body := `{}`
	now := time.Now().Unix()
	nonce1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	nonce2 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1" // same

	r1 := signedRequest(t, kp, "POST", "/v1/stake", body, now, nonce1)
	_, err := auth.VerifyRequest(r1, []byte(body), reg, ns)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	r2 := signedRequest(t, kp, "POST", "/v1/stake", body, now, nonce2)
	_, err = auth.VerifyRequest(r2, []byte(body), reg, ns)
	if err == nil {
		t.Fatal("expected error for replayed nonce")
	}
	if !strings.Contains(err.Error(), "nonce already used") {
		t.Errorf("error = %q; want nonce-related error", err)
	}
}

func TestVerifyRequest_UnknownAgent(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	reg := &mockRegistry{fps: map[crypto.AgentID]*identity.CapabilityFingerprint{}} // empty
	ns := newMockNonceStore()

	body := `{}`
	now := time.Now().Unix()
	r := signedRequest(t, kp, "POST", "/v1/transfer", body, now, testNonce)

	_, err := auth.VerifyRequest(r, []byte(body), reg, ns)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("error = %q; want agent-not-registered error", err)
	}
}

func TestVerifyRequest_MissingHeaders(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	reg := makeRegistry(kp)

	r := httptest.NewRequest("POST", "/v1/transfer", strings.NewReader("{}"))
	// No headers set.

	_, err := auth.VerifyRequest(r, []byte("{}"), reg, nil)
	if err == nil {
		t.Fatal("expected error for missing headers")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error = %q; want missing-header error", err)
	}
}

func TestVerifyRequest_MalformedSignature(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	reg := makeRegistry(kp)

	body := `{}`
	now := time.Now().Unix()
	r := httptest.NewRequest("POST", "/v1/transfer", strings.NewReader(body))
	r.Header.Set("X-Aethernet-Agent-ID", string(kp.AgentID()))
	r.Header.Set("X-Aethernet-Timestamp", strconv.FormatInt(now, 10))
	r.Header.Set("X-Aethernet-Nonce", testNonce)
	r.Header.Set("X-Aethernet-Signature", "not-valid-hex!!")

	_, err := auth.VerifyRequest(r, []byte(body), reg, nil)
	if err == nil {
		t.Fatal("expected error for malformed signature")
	}
}

func TestBuildCanonicalString_Deterministic(t *testing.T) {
	a := auth.BuildCanonicalString("POST", "/v1/transfer", "agent123", "abcd1234abcd1234abcd1234abcd1234", 1700000000, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	b := auth.BuildCanonicalString("POST", "/v1/transfer", "agent123", "abcd1234abcd1234abcd1234abcd1234", 1700000000, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if string(a) != string(b) {
		t.Error("BuildCanonicalString is not deterministic")
	}
	if !strings.HasPrefix(string(a), "AETHERNET-REQUEST-V1\n") {
		t.Errorf("missing version prefix: %q", string(a)[:30])
	}
}

func TestAgentRateLimiter(t *testing.T) {
	rl := auth.NewAgentRateLimiter(3) // 3 per hour
	agent := crypto.AgentID("test-agent")

	for i := 0; i < 3; i++ {
		if !rl.Allow(agent) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.Allow(agent) {
		t.Error("4th request should be rejected")
	}
}
