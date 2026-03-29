package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// CanonicalBytes returns the RFC 8785 (JCS) canonical JSON encoding of data.
//
// This is the same canonicalization used for AETHERNET-TX-V1 signing, evidence
// hashes, and TxID computation. External verifiers should use this function to
// produce bytes compatible with the protocol.
func CanonicalBytes(data interface{}) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("sdk: marshal: %w", err)
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("sdk: unmarshal: %w", err)
	}
	var b strings.Builder
	if err := jcsWrite(&b, parsed); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// CanonicalHash returns the hex-encoded SHA-256 hash of the JCS-canonicalized
// JSON representation of data.
//
// This is the same hash computation used for TxID and evidence hashes in the
// AetherNet protocol.
func CanonicalHash(data interface{}) (string, error) {
	cb, err := CanonicalBytes(data)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(cb)
	return hex.EncodeToString(h[:]), nil
}

// jcsWrite serializes a JSON value per RFC 8785.
// This is a standalone copy of internal/auth.jcsWrite so that the SDK has no
// dependency on internal packages.
func jcsWrite(b *strings.Builder, v any) error {
	switch val := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if val {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		b.WriteString(jcsNumber(val))
	case string:
		b.WriteString(jcsString(val))
	case []any:
		b.WriteByte('[')
		for i, elem := range val {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := jcsWrite(b, elem); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		first := true
		for _, k := range keys {
			if first {
				first = false
			} else {
				b.WriteByte(',')
			}
			b.WriteString(jcsString(k))
			b.WriteByte(':')
			if err := jcsWrite(b, val[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported type %T", v)
	}
	return nil
}

func jcsNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e20 {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%G", f)
}

func jcsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
