package wallet_test

import (
	"strings"
	"testing"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/wallet"
)

// TestDeriveAddress_Deterministic verifies that the same public key always
// produces the same deposit address.
func TestDeriveAddress_Deterministic(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	pubKey := []byte(kp.PublicKey)

	addr1 := wallet.DeriveAddress(pubKey)
	addr2 := wallet.DeriveAddress(pubKey)
	if addr1 != addr2 {
		t.Errorf("DeriveAddress is not deterministic: %q != %q", addr1, addr2)
	}
}

// TestDeriveAddress_Prefix verifies that every derived address starts with "aet1".
func TestDeriveAddress_Prefix(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	addr := wallet.DeriveAddress([]byte(kp.PublicKey))
	if !strings.HasPrefix(string(addr), "aet1") {
		t.Errorf("address %q does not start with aet1", addr)
	}
}

// TestDeriveAddress_Unique verifies that two different public keys produce
// different deposit addresses.
func TestDeriveAddress_Unique(t *testing.T) {
	kp1, _ := crypto.GenerateKeyPair()
	kp2, _ := crypto.GenerateKeyPair()
	addr1 := wallet.DeriveAddress([]byte(kp1.PublicKey))
	addr2 := wallet.DeriveAddress([]byte(kp2.PublicKey))
	if addr1 == addr2 {
		t.Errorf("two different keys produced the same address: %q", addr1)
	}
}

// TestRegisterAndResolve verifies the full register → resolve-by-address →
// resolve-by-agent-ID round trip.
func TestRegisterAndResolve(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	agentID := kp.AgentID()
	pubKey := []byte(kp.PublicKey)

	w := wallet.New()
	addr := w.Register(agentID, pubKey)

	// Resolve by address.
	gotID, ok := w.Resolve(addr)
	if !ok {
		t.Fatalf("Resolve(%q) returned false, want true", addr)
	}
	if gotID != agentID {
		t.Errorf("Resolve returned %q, want %q", gotID, agentID)
	}

	// Resolve by agent ID.
	gotAddr, ok := w.AddressOf(agentID)
	if !ok {
		t.Fatalf("AddressOf(%q) returned false, want true", agentID)
	}
	if gotAddr != addr {
		t.Errorf("AddressOf returned %q, want %q", gotAddr, addr)
	}
}

// TestResolve_Unknown verifies that Resolve returns false for an unregistered address.
func TestResolve_Unknown(t *testing.T) {
	w := wallet.New()
	_, ok := w.Resolve(wallet.Address("aet1deadbeef"))
	if ok {
		t.Error("Resolve of unknown address returned true, want false")
	}
}
