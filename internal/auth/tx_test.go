package auth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/auth"
)

// ── JCS Canonicalization ─────────────────────────────────────────────────────

func TestCanonicalizeJSON_KeyOrdering(t *testing.T) {
	a, _ := auth.CanonicalizeJSON([]byte(`{"b":2,"a":1}`))
	b, _ := auth.CanonicalizeJSON([]byte(`{"a":1,"b":2}`))
	if string(a) != string(b) {
		t.Errorf("key ordering: %q != %q", a, b)
	}
	if string(a) != `{"a":1,"b":2}` {
		t.Errorf("expected sorted output, got %q", a)
	}
}

func TestCanonicalizeJSON_NestedObjects(t *testing.T) {
	input := `{"z":{"b":2,"a":1},"a":true}`
	out, err := auth.CanonicalizeJSON([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":true,"z":{"a":1,"b":2}}` {
		t.Errorf("nested: %q", out)
	}
}

func TestCanonicalizeJSON_Numbers(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{`{"n":0}`, `{"n":0}`},
		{`{"n":-0}`, `{"n":0}`},
		{`{"n":1.0}`, `{"n":1}`},
		{`{"n":1.5}`, `{"n":1.5}`},
		{`{"n":100}`, `{"n":100}`},
		{`{"n":-42}`, `{"n":-42}`},
	}
	for _, tt := range tests {
		out, err := auth.CanonicalizeJSON([]byte(tt.input))
		if err != nil {
			t.Fatalf("input %s: %v", tt.input, err)
		}
		if string(out) != tt.want {
			t.Errorf("input %s: got %q, want %q", tt.input, out, tt.want)
		}
	}
}

func TestCanonicalizeJSON_Strings(t *testing.T) {
	input := `{"s":"hello\nworld"}`
	out, _ := auth.CanonicalizeJSON([]byte(input))
	if string(out) != `{"s":"hello\nworld"}` {
		t.Errorf("string escaping: %q", out)
	}
}

func TestCanonicalizeJSON_Arrays(t *testing.T) {
	out, _ := auth.CanonicalizeJSON([]byte(`[3,1,2]`))
	if string(out) != `[3,1,2]` {
		t.Errorf("arrays should preserve order: %q", out)
	}
}

func TestCanonicalizeJSON_NullBooleans(t *testing.T) {
	out, _ := auth.CanonicalizeJSON([]byte(`{"a":null,"b":true,"c":false}`))
	if string(out) != `{"a":null,"b":true,"c":false}` {
		t.Errorf("literals: %q", out)
	}
}

func TestCanonicalizeJSON_Deterministic(t *testing.T) {
	input := `{"version":"AETHERNET-TX-V1","nonce":"abc","actor":"def","method":"POST"}`
	a, _ := auth.CanonicalizeJSON([]byte(input))
	b, _ := auth.CanonicalizeJSON([]byte(input))
	if string(a) != string(b) {
		t.Error("not deterministic")
	}
}

// ── Transaction Sign/Verify ──────────────────────────────────────────────────

func testTx() *auth.Transaction {
	return &auth.Transaction{
		Version:    auth.TxVersion,
		ChainID:    "aethernet-testnet-1",
		Actor:      "deadbeef",
		Method:     "POST",
		Path:       "/v1/transfer",
		BodySHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		CreatedAt:  1700000000,
		ExpiresAt:  1700000060,
		Nonce:      "0123456789abcdef0123456789abcdef",
	}
}

func TestTransaction_SignVerify_Roundtrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := priv.Public().(ed25519.PublicKey)

	tx := testTx()
	tx.Actor = hex.EncodeToString(pub)

	sig, err := tx.Sign(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := tx.Verify(pub, sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestTransaction_TxID_Deterministic(t *testing.T) {
	tx := testTx()
	id1, _ := tx.TxID()
	id2, _ := tx.TxID()
	if id1 != id2 {
		t.Error("TxID not deterministic")
	}
	if len(id1) != 64 {
		t.Errorf("TxID length = %d; want 64", len(id1))
	}
}

func TestTransaction_TxID_ChangesWithField(t *testing.T) {
	tx1 := testTx()
	id1, _ := tx1.TxID()

	tx2 := testTx()
	tx2.Nonce = "ffffffffffffffffffffffffffffffff"
	id2, _ := tx2.TxID()
	if id1 == id2 {
		t.Error("TxID should change when nonce changes")
	}

	tx3 := testTx()
	tx3.Path = "/v1/stake"
	id3, _ := tx3.TxID()
	if id1 == id3 {
		t.Error("TxID should change when path changes")
	}

	tx4 := testTx()
	tx4.BodySHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	id4, _ := tx4.TxID()
	if id1 == id4 {
		t.Error("TxID should change when body hash changes")
	}
}

func TestTransaction_Verify_WrongKey(t *testing.T) {
	_, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)

	tx := testTx()
	sig, _ := tx.Sign(priv1)

	if err := tx.Verify(pub2, sig); err == nil {
		t.Error("verify should fail with wrong key")
	}
}

func TestTransaction_Verify_TamperedBody(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := priv.Public().(ed25519.PublicKey)

	tx := testTx()
	sig, _ := tx.Sign(priv)

	tx.BodySHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := tx.Verify(pub, sig); err == nil {
		t.Error("verify should fail after body hash tamper")
	}
}

// ── Transaction Verification Middleware ───────────────────────────────────────

func TestVerifyTransaction_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	actor := hex.EncodeToString(pub)
	now := time.Now().Unix()

	body := []byte(`{"amount":1000}`)
	canon, _ := auth.CanonicalizeJSON(body)
	bodyHash := sha256.Sum256(canon)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	tx := &auth.Transaction{
		Version:    auth.TxVersion,
		ChainID:    "aethernet-testnet-1",
		Actor:      actor,
		Method:     "POST",
		Path:       "/v1/transfer",
		BodySHA256: bodyHashHex,
		CreatedAt:  now,
		ExpiresAt:  now + 60,
		Nonce:      "0123456789abcdef0123456789abcdef",
	}
	sig, _ := tx.Sign(priv)

	r := httptest.NewRequest("POST", "/v1/transfer", strings.NewReader(string(body)))
	r.Header.Set("X-AetherNet-Version", auth.TxVersion)
	r.Header.Set("X-AetherNet-Chain-ID", "aethernet-testnet-1")
	r.Header.Set("X-AetherNet-Actor", actor)
	r.Header.Set("X-AetherNet-Created", strconv.FormatInt(now, 10))
	r.Header.Set("X-AetherNet-Expires", strconv.FormatInt(now+60, 10))
	r.Header.Set("X-AetherNet-Nonce", "0123456789abcdef0123456789abcdef")
	r.Header.Set("X-AetherNet-Signature", sig)

	lookup := func(a string) (ed25519.PublicKey, error) { return pub, nil }

	vtx, err := auth.VerifyTransaction(r, body, "aethernet-testnet-1", lookup)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if vtx.Actor != actor {
		t.Errorf("actor = %q; want %q", vtx.Actor, actor)
	}
	if vtx.TxID == "" {
		t.Error("TxID should not be empty")
	}
}

func TestVerifyTransaction_Expired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	actor := hex.EncodeToString(pub)
	past := time.Now().Unix() - 300

	tx := &auth.Transaction{
		Version:    auth.TxVersion,
		ChainID:    "aethernet-testnet-1",
		Actor:      actor,
		Method:     "POST",
		Path:       "/v1/test",
		BodySHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		CreatedAt:  past,
		ExpiresAt:  past + 60,
		Nonce:      "0123456789abcdef0123456789abcdef",
	}
	sig, _ := tx.Sign(priv)

	r := httptest.NewRequest("POST", "/v1/test", nil)
	r.Header.Set("X-AetherNet-Version", auth.TxVersion)
	r.Header.Set("X-AetherNet-Chain-ID", "aethernet-testnet-1")
	r.Header.Set("X-AetherNet-Actor", actor)
	r.Header.Set("X-AetherNet-Created", strconv.FormatInt(past, 10))
	r.Header.Set("X-AetherNet-Expires", strconv.FormatInt(past+60, 10))
	r.Header.Set("X-AetherNet-Nonce", "0123456789abcdef0123456789abcdef")
	r.Header.Set("X-AetherNet-Signature", sig)

	lookup := func(a string) (ed25519.PublicKey, error) { return pub, nil }

	_, err := auth.VerifyTransaction(r, []byte(`{}`), "aethernet-testnet-1", lookup)
	if err == nil {
		t.Fatal("expected expired error")
	}
}

func TestVerifyTransaction_ChainMismatch(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/test", nil)
	r.Header.Set("X-AetherNet-Version", auth.TxVersion)
	r.Header.Set("X-AetherNet-Chain-ID", "wrong-chain")
	r.Header.Set("X-AetherNet-Actor", "abc")
	r.Header.Set("X-AetherNet-Created", strconv.FormatInt(time.Now().Unix(), 10))
	r.Header.Set("X-AetherNet-Expires", strconv.FormatInt(time.Now().Unix()+60, 10))
	r.Header.Set("X-AetherNet-Nonce", "0123456789abcdef0123456789abcdef")
	r.Header.Set("X-AetherNet-Signature", "deadbeef")

	lookup := func(a string) (ed25519.PublicKey, error) { return nil, nil }

	_, err := auth.VerifyTransaction(r, []byte(`{}`), "aethernet-testnet-1", lookup)
	if err == nil {
		t.Fatal("expected chain mismatch error")
	}
	if !strings.Contains(err.Error(), "chain_id") {
		t.Errorf("error = %q; want chain_id mention", err)
	}
}

func TestVerifyTransaction_MissingHeaders(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/test", nil)
	// No headers set.

	lookup := func(a string) (ed25519.PublicKey, error) { return nil, nil }

	_, err := auth.VerifyTransaction(r, []byte(`{}`), "aethernet-testnet-1", lookup)
	if err == nil {
		t.Fatal("expected missing header error")
	}
}

// ── TxID Store ───────────────────────────────────────────────────────────────

func TestTxIDStore_MarkSeen(t *testing.T) {
	store := auth.NewTxIDStore(nil)

	seen, _ := store.MarkSeen("tx-1")
	if seen {
		t.Error("first call should return false")
	}

	seen, _ = store.MarkSeen("tx-1")
	if !seen {
		t.Error("second call should return true")
	}
}

func TestTxIDStore_IsSeen(t *testing.T) {
	store := auth.NewTxIDStore(nil)

	seen, _ := store.IsSeen("tx-2")
	if seen {
		t.Error("should not be seen before marking")
	}

	store.MarkSeen("tx-2")

	seen, _ = store.IsSeen("tx-2")
	if !seen {
		t.Error("should be seen after marking")
	}
}

func TestTxIDStore_Concurrent(t *testing.T) {
	store := auth.NewTxIDStore(nil)
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id string) {
			store.MarkSeen(id)
			store.IsSeen(id)
			done <- true
		}(strconv.Itoa(i))
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
