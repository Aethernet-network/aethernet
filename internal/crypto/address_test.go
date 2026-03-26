package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestAddressFromPublicKey_Roundtrip(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	addr, err := AddressFromPublicKey(pub, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(addr, "taet1") {
		t.Errorf("testnet address should start with taet1, got %q", addr)
	}

	decoded, hrp, err := PublicKeyFromAddress(addr)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hrp != TestnetHRP {
		t.Errorf("hrp = %q; want %q", hrp, TestnetHRP)
	}
	if !decoded.Equal(pub) {
		t.Error("decoded public key does not match original")
	}
}

func TestAddressFromPublicKey_MainnetPrefix(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	addr, err := AddressFromPublicKey(pub, false)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(addr, "aet1") {
		t.Errorf("mainnet address should start with aet1, got %q", addr)
	}

	decoded, hrp, err := PublicKeyFromAddress(addr)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hrp != MainnetHRP {
		t.Errorf("hrp = %q; want %q", hrp, MainnetHRP)
	}
	if !decoded.Equal(pub) {
		t.Error("decoded public key does not match original")
	}
}

func TestAddressFromPublicKey_DifferentPrefixes(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	testAddr, _ := AddressFromPublicKey(pub, true)
	mainAddr, _ := AddressFromPublicKey(pub, false)

	if testAddr == mainAddr {
		t.Error("testnet and mainnet addresses should differ for the same key")
	}
}

func TestValidateAddress_Valid(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	addr, _ := AddressFromPublicKey(pub, true)

	if !ValidateAddress(addr) {
		t.Errorf("valid address %q rejected", addr)
	}
}

func TestValidateAddress_Invalid(t *testing.T) {
	cases := []string{
		"",
		"taet1invalid",
		"not-an-address",
		"taet1" + strings.Repeat("q", 100), // too long data
	}
	for _, c := range cases {
		if ValidateAddress(c) {
			t.Errorf("invalid address %q accepted", c)
		}
	}
}

func TestValidateAddress_BadChecksum(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	addr, _ := AddressFromPublicKey(pub, true)

	// Tamper with last character.
	tampered := addr[:len(addr)-1] + "q"
	if addr[len(addr)-1] == 'q' {
		tampered = addr[:len(addr)-1] + "p"
	}
	if ValidateAddress(tampered) {
		t.Error("address with bad checksum should be invalid")
	}
}

func TestResolveAgentID_Bech32(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	addr, _ := AddressFromPublicKey(pub, true)
	hexID := AgentID(pubKeyHex(pub))

	resolved := ResolveAgentID(addr)
	if resolved != hexID {
		t.Errorf("resolved = %q; want %q", resolved, hexID)
	}
}

func TestResolveAgentID_HexPassthrough(t *testing.T) {
	input := "deadbeef01234567deadbeef01234567deadbeef01234567deadbeef01234567"
	resolved := ResolveAgentID(input)
	if string(resolved) != input {
		t.Errorf("hex input should pass through unchanged")
	}
}

func TestResolveAgentID_LegacyString(t *testing.T) {
	input := "my-research-agent"
	resolved := ResolveAgentID(input)
	if string(resolved) != input {
		t.Errorf("legacy string should pass through unchanged")
	}
}

func TestKeyPair_Address(t *testing.T) {
	kp, _ := GenerateKeyPair()
	addr := kp.Address(true)
	if !strings.HasPrefix(addr, "taet1") {
		t.Errorf("address = %q; want taet1 prefix", addr)
	}
	// Roundtrip.
	resolved := ResolveAgentID(addr)
	if resolved != kp.AgentID() {
		t.Errorf("resolved %q != AgentID %q", resolved, kp.AgentID())
	}
}

func pubKeyHex(pub ed25519.PublicKey) string {
	h := make([]byte, len(pub)*2)
	for i, b := range pub {
		const hextable = "0123456789abcdef"
		h[i*2] = hextable[b>>4]
		h[i*2+1] = hextable[b&0x0f]
	}
	return string(h)
}
