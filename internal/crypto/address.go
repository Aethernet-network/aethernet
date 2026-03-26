package crypto

import (
	"crypto/ed25519"
	"fmt"
	"strings"
)

// Address prefixes for bech32-encoded agent addresses.
const (
	TestnetHRP = "taet"
	MainnetHRP = "aet"
)

// AddressFromPublicKey derives a bech32 address from an Ed25519 public key.
// Uses testnet prefix "taet" or mainnet prefix "aet".
func AddressFromPublicKey(pubKey ed25519.PublicKey, testnet bool) (string, error) {
	if len(pubKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("crypto: invalid public key length: %d", len(pubKey))
	}
	hrp := MainnetHRP
	if testnet {
		hrp = TestnetHRP
	}
	data, err := convertBits(pubKey, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("crypto: convert bits: %w", err)
	}
	return bech32Encode(hrp, data)
}

// PublicKeyFromAddress extracts the Ed25519 public key from a bech32 address.
// Returns the public key and the HRP (human-readable part).
func PublicKeyFromAddress(address string) (ed25519.PublicKey, string, error) {
	hrp, data, err := bech32Decode(address)
	if err != nil {
		return nil, "", fmt.Errorf("crypto: decode address: %w", err)
	}
	if hrp != TestnetHRP && hrp != MainnetHRP {
		return nil, "", fmt.Errorf("crypto: unknown address prefix %q", hrp)
	}
	converted, err := convertBits(data, 5, 8, false)
	if err != nil {
		return nil, "", fmt.Errorf("crypto: convert bits: %w", err)
	}
	if len(converted) != ed25519.PublicKeySize {
		return nil, "", fmt.Errorf("crypto: decoded key length %d, want %d", len(converted), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(converted), hrp, nil
}

// ValidateAddress checks if a string is a valid AetherNet bech32 address.
func ValidateAddress(address string) bool {
	_, _, err := PublicKeyFromAddress(address)
	return err == nil
}

// Address returns the bech32 address for this keypair.
func (kp *KeyPair) Address(testnet bool) string {
	addr, _ := AddressFromPublicKey(kp.PublicKey, testnet)
	return addr
}

// ResolveAgentID accepts either a bech32 address or a hex-encoded public key
// and returns the canonical hex-encoded AgentID. This allows the API to accept
// both formats at the boundary.
func ResolveAgentID(input string) AgentID {
	// Try bech32 first.
	if strings.HasPrefix(input, TestnetHRP+"1") || strings.HasPrefix(input, MainnetHRP+"1") {
		pk, _, err := PublicKeyFromAddress(input)
		if err == nil {
			return AgentID(fmt.Sprintf("%x", pk))
		}
	}
	// Already hex or legacy string ID — return as-is.
	return AgentID(input)
}

// ── BIP-173 Bech32 Implementation ────────────────────────────────────────────

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32CharsetRev [128]int8

func init() {
	for i := range bech32CharsetRev {
		bech32CharsetRev[i] = -1
	}
	for i, c := range bech32Charset {
		bech32CharsetRev[c] = int8(i)
	}
}

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32HRPExpand(hrp string) []byte {
	result := make([]byte, 0, len(hrp)*2+1)
	for _, c := range hrp {
		result = append(result, byte(c>>5))
	}
	result = append(result, 0)
	for _, c := range hrp {
		result = append(result, byte(c&31))
	}
	return result
}

func bech32CreateChecksum(hrp string, data []byte) []byte {
	values := append(bech32HRPExpand(hrp), data...)
	values = append(values, 0, 0, 0, 0, 0, 0)
	polymod := bech32Polymod(values) ^ 1
	result := make([]byte, 6)
	for i := 0; i < 6; i++ {
		result[i] = byte((polymod >> uint(5*(5-i))) & 31)
	}
	return result
}

func bech32VerifyChecksum(hrp string, data []byte) bool {
	return bech32Polymod(append(bech32HRPExpand(hrp), data...)) == 1
}

func bech32Encode(hrp string, data []byte) (string, error) {
	chk := bech32CreateChecksum(hrp, data)
	combined := append(data, chk...)
	var b strings.Builder
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, d := range combined {
		if int(d) >= len(bech32Charset) {
			return "", fmt.Errorf("bech32: invalid data byte %d", d)
		}
		b.WriteByte(bech32Charset[d])
	}
	return b.String(), nil
}

func bech32Decode(s string) (string, []byte, error) {
	if len(s) > 90 {
		return "", nil, fmt.Errorf("bech32: string too long")
	}
	s = strings.ToLower(s)
	pos := strings.LastIndexByte(s, '1')
	if pos < 1 || pos+7 > len(s) {
		return "", nil, fmt.Errorf("bech32: invalid separator position")
	}
	hrp := s[:pos]
	dataStr := s[pos+1:]
	data := make([]byte, len(dataStr))
	for i, c := range dataStr {
		if c >= 128 || bech32CharsetRev[c] == -1 {
			return "", nil, fmt.Errorf("bech32: invalid character %c", c)
		}
		data[i] = byte(bech32CharsetRev[c])
	}
	if !bech32VerifyChecksum(hrp, data) {
		return "", nil, fmt.Errorf("bech32: invalid checksum")
	}
	return hrp, data[:len(data)-6], nil
}

// convertBits converts a byte slice from one bit-group size to another.
// pad determines whether to zero-pad the last group if incomplete.
func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	acc := uint32(0)
	bits := uint(0)
	maxv := uint32((1 << toBits) - 1)
	var result []byte
	for _, b := range data {
		acc = (acc << fromBits) | uint32(b)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte((acc>>bits)&maxv))
		}
	}
	if pad {
		if bits > 0 {
			result = append(result, byte((acc<<(toBits-bits))&maxv))
		}
	} else {
		if bits >= fromBits {
			return nil, fmt.Errorf("bech32: non-zero padding")
		}
		if (acc<<(toBits-bits))&maxv != 0 {
			return nil, fmt.Errorf("bech32: non-zero padding bits")
		}
	}
	return result, nil
}
