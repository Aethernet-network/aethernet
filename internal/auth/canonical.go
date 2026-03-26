package auth

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// CanonicalizeJSON takes arbitrary JSON bytes and returns RFC 8785 JSON
// Canonicalization Scheme (JCS) output: deterministic key ordering, strict
// number serialization, no whitespace. Deterministic across Go, JavaScript,
// and Python for the same input.
func CanonicalizeJSON(input []byte) ([]byte, error) {
	var raw any
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, fmt.Errorf("jcs: unmarshal: %w", err)
	}
	var b strings.Builder
	if err := jcsWrite(&b, raw); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

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
		// RFC 8785: sort keys by Unicode code point (Go string comparison).
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

// jcsNumber serializes a float64 per RFC 8785 / ECMAScript number-to-string.
func jcsNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null" // RFC 8785: NaN and Infinity serialize as null
	}
	// -0 → "0" per RFC 8785
	if f == 0 {
		return "0"
	}
	// If it's an integer value, emit without decimal point.
	if f == math.Trunc(f) && math.Abs(f) < 1e20 {
		return strconv.FormatInt(int64(f), 10)
	}
	// Use shortest representation that round-trips.
	return strconv.FormatFloat(f, 'G', -1, 64)
}

// jcsString serializes a string per RFC 8785 with minimal escapes.
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
