// Package jcs implements RFC 8785 JSON Canonicalization Scheme.
//
// This is the shared JCS implementation used by both event.ComputeID and
// crypto.CanonicalBytes. It has zero dependencies on other internal packages
// to break the potential import cycle between event, crypto, and auth.
//
// The auth package has its own copy (internal/auth/canonical.go) that
// predates this shared version. Both implementations produce identical
// output for the same input.
package jcs

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Canonicalize takes arbitrary JSON bytes and returns RFC 8785 JSON
// Canonicalization Scheme (JCS) output: deterministic key ordering, strict
// number serialization, no whitespace.
func Canonicalize(input []byte) ([]byte, error) {
	var raw any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, fmt.Errorf("jcs: unmarshal: %w", err)
	}
	var b strings.Builder
	if err := write(&b, raw); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func write(b *strings.Builder, v any) error {
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
		b.WriteString(number(val))
	case string:
		b.WriteString(str(val))
	case []any:
		b.WriteByte('[')
		for i, elem := range val {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := write(b, elem); err != nil {
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
			b.WriteString(str(k))
			b.WriteByte(':')
			if err := write(b, val[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported type %T", v)
	}
	return nil
}

func number(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e20 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'G', -1, 64)
}

func str(s string) string {
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
