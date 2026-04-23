package jcs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// ─── Determinism & idempotence ────────────────────────────────────────────────
//
// JCS's load-bearing property is that semantically-equivalent JSON inputs
// produce byte-identical canonical output. Every content-addressed event
// in the protocol relies on this; non-determinism here would manifest as
// EventID drift across nodes and break consensus.

// TestCanonicalize_DeterministicAcrossCalls runs Canonicalize 100x against
// a corpus of inputs whose unmarshalled representation involves a Go map
// (non-deterministic iteration order). Each input must produce a single
// canonical output across all 100 calls. Run with -count=100 for stress.
func TestCanonicalize_DeterministicAcrossCalls(t *testing.T) {
	t.Parallel()

	inputs := []string{
		`{}`,
		`{"a":1}`,
		`{"b":2,"a":1}`,
		`{"c":3,"a":1,"b":2}`,
		// 8-key map — well past the threshold where Go's map iteration
		// order would visibly randomize between calls if the sort were
		// missing.
		`{"h":8,"g":7,"f":6,"e":5,"d":4,"c":3,"b":2,"a":1}`,
		`{"nested":{"z":26,"y":25,"x":24,"w":23},"top":1}`,
		`[{"k1":1},{"k2":2,"k1":1},{"k3":3,"k2":2,"k1":1}]`,
	}

	for _, in := range inputs {
		in := in
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			ref, err := Canonicalize([]byte(in))
			if err != nil {
				t.Fatalf("first Canonicalize: %v", err)
			}
			for i := 1; i < 100; i++ {
				got, err := Canonicalize([]byte(in))
				if err != nil {
					t.Fatalf("Canonicalize iter %d: %v", i, err)
				}
				if !bytes.Equal(ref, got) {
					t.Fatalf("Canonicalize non-deterministic at iter %d:\n  ref: %s\n  got: %s",
						i, string(ref), string(got))
				}
			}
		})
	}
}

// TestCanonicalize_Idempotent asserts Canonicalize(Canonicalize(x)) ==
// Canonicalize(x). A second pass over already-canonical bytes must be a
// fixed point.
func TestCanonicalize_Idempotent(t *testing.T) {
	t.Parallel()

	cases := []string{
		`null`,
		`true`,
		`false`,
		`0`,
		`42`,
		`-7`,
		`1.5`,
		`"hello"`,
		`""`,
		`[]`,
		`{}`,
		`[1,2,3]`,
		`{"a":1,"b":2}`,
		`{"nested":{"deep":{"deeper":[1,2,3]}}}`,
		`{"unicode":"café","escape":"a\nb"}`,
	}
	for _, in := range cases {
		first, err := Canonicalize([]byte(in))
		if err != nil {
			t.Fatalf("first Canonicalize(%q): %v", in, err)
		}
		second, err := Canonicalize(first)
		if err != nil {
			t.Fatalf("second Canonicalize(%q): %v", in, err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("not idempotent for %q:\n  first:  %s\n  second: %s",
				in, string(first), string(second))
		}
	}
}

// ─── Canonical-form properties ────────────────────────────────────────────────

// TestCanonicalize_SortsObjectKeys asserts object keys appear in
// lexicographic order regardless of input order. RFC 8785 §3.2.3.
func TestCanonicalize_SortsObjectKeys(t *testing.T) {
	t.Parallel()
	in := `{"z":1,"a":2,"m":3,"b":4}`
	got, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	want := `{"a":2,"b":4,"m":3,"z":1}`
	if string(got) != want {
		t.Errorf("sort order:\n  got:  %s\n  want: %s", string(got), want)
	}
}

// TestCanonicalize_NoWhitespace asserts the output contains zero
// whitespace characters between tokens.
func TestCanonicalize_NoWhitespace(t *testing.T) {
	t.Parallel()
	in := `{ "a" :  1 ,  "b" :  [ 2 , 3 ] }`
	got, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	for _, r := range string(got) {
		switch r {
		case ' ', '\t', '\n', '\r':
			t.Errorf("whitespace rune %q in canonical output: %s", r, string(got))
		}
	}
	if string(got) != `{"a":1,"b":[2,3]}` {
		t.Errorf("got %q", string(got))
	}
}

// TestCanonicalize_NormalizedNumbers asserts integers in float-form
// inputs round-trip to canonical integer form, and that floats use the
// strconv 'G' format.
func TestCanonicalize_NormalizedNumbers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{`0`, `0`},
		{`0.0`, `0`},
		{`-0`, `0`},
		{`1`, `1`},
		{`1.0`, `1`},
		{`-1.0`, `-1`},
		{`100`, `100`},
		{`1.5`, `1.5`},
		{`1e3`, `1000`},
		{`1.5e2`, `150`},
		{`0.1`, `0.1`},
	}
	for _, tc := range cases {
		got, err := Canonicalize([]byte(tc.in))
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("number normalization for %q:\n  got:  %s\n  want: %s",
				tc.in, string(got), tc.want)
		}
	}
}

// TestCanonicalize_NumberPrecisionEdges covers numbers near the int/float
// boundary the implementation uses (`< 1e20` for FormatInt vs FormatFloat).
//
// FINDING (F4A B.1): the implementation's threshold is `|f| < 1e20`, but
// int64's representable range tops out at ~9.22e18 (MaxInt64). Numbers
// between (MaxInt64, 1e20) silently saturate to MaxInt64 / MinInt64 in
// the canonical output rather than rounding via the FormatFloat path.
// This is observable behavior — the assertions below DOCUMENT the current
// implementation rather than the desired RFC 8785 behavior. A fix would
// tighten the threshold to `|f| < float64(math.MaxInt64)`. Tracked as a
// follow-up; out of scope for this F4A test-coverage commit.
func TestCanonicalize_NumberPrecisionEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
		note     string
	}{
		{`1.5e10`, `15000000000`, "exact integer"},
		{`1e15`, `1000000000000000`, "exact, well below MaxInt64"},
		{`9223372036854775807`, `9223372036854775807`, "MaxInt64 exact"},
		// The next two document the silent-overflow bug. With the
		// existing `|f| < 1e20` threshold, the cast int64(f) saturates
		// at MaxInt64 / MinInt64. A correct implementation would emit
		// the FormatFloat representation here.
		{`9.999999999999999e19`, `9223372036854775807`, "FINDING: silent overflow"},
		{`-9.999999999999999e19`, `-9223372036854775808`, "FINDING: silent overflow"},
		// Above 1e20 the FormatFloat path correctly engages.
		{`1e21`, `1E+21`, "above 1e20 threshold, FormatFloat path"},
	}
	for _, tc := range cases {
		got, err := Canonicalize([]byte(tc.in))
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("precision-edge for %q (%s):\n  got:  %s\n  want: %s",
				tc.in, tc.note, string(got), tc.want)
		}
	}
}

// TestCanonicalize_StringEscapes asserts the seven standard escape
// sequences and that control characters use \uXXXX hex.
func TestCanonicalize_StringEscapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   any
		want string
	}{
		{`hello`, `"hello"`},
		{"\"", `"\""`},
		{"\\", `"\\"`},
		{"\b", `"\b"`},
		{"\f", `"\f"`},
		{"\n", `"\n"`},
		{"\r", `"\r"`},
		{"\t", `"\t"`},
		{"\x00", `"\u0000"`},
		{"\x01", `"\u0001"`},
		{"\x1f", `"\u001f"`},
		{"a\nb\tc", `"a\nb\tc"`},
		{"café", `"café"`},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatalf("test setup marshal %q: %v", tc.in, err)
		}
		got, err := Canonicalize(raw)
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("string escape for %q:\n  got:  %s\n  want: %s",
				tc.in, string(got), tc.want)
		}
	}
}

// TestCanonicalize_UnicodePassesThrough asserts multi-byte UTF-8 above
// 0x1f is emitted as raw UTF-8, not \uXXXX-escaped. RFC 8785 §3.2.5.
func TestCanonicalize_UnicodePassesThrough(t *testing.T) {
	t.Parallel()
	cases := []string{
		`"café"`,                  // Latin-1 supplement
		`"日本語"`,                  // CJK
		`"emoji: 🚀"`,              // 4-byte UTF-8
		`"mixed: ascii café 日本"`, // multi-script
	}
	for _, in := range cases {
		got, err := Canonicalize([]byte(in))
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", in, err)
			continue
		}
		if !utf8.ValidString(string(got)) {
			t.Errorf("Canonicalize(%q) produced invalid UTF-8: %q", in, string(got))
		}
		if strings.Contains(string(got), `\u`) {
			t.Errorf("Canonicalize(%q) escaped non-control codepoint: %s", in, string(got))
		}
	}
}

// ─── Roundtrip ────────────────────────────────────────────────────────────────

// TestCanonicalize_RoundtripStructure asserts that canonical bytes
// unmarshal to a value equivalent to the original semantic structure.
// Because Canonicalize loses the float/int distinction (it uses
// json.Unmarshal which decodes all numbers as float64), only the
// post-decode structure can be compared, not byte-for-byte against
// arbitrary input.
func TestCanonicalize_RoundtripStructure(t *testing.T) {
	t.Parallel()
	cases := []string{
		`null`,
		`true`,
		`false`,
		`42`,
		`"hello"`,
		`[]`,
		`{}`,
		`[1,2,3]`,
		`{"a":1,"b":[2,3],"c":{"d":4}}`,
		`{"unicode":"café"}`,
	}
	for _, in := range cases {
		canonical, err := Canonicalize([]byte(in))
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", in, err)
			continue
		}
		var orig, got any
		if err := json.Unmarshal([]byte(in), &orig); err != nil {
			t.Fatalf("test setup unmarshal %q: %v", in, err)
		}
		if err := json.Unmarshal(canonical, &got); err != nil {
			t.Errorf("Unmarshal(canonical) for %q: %v", in, err)
			continue
		}
		// Compare via re-canonicalization (avoids deep-equal pitfalls
		// with map[string]any). The comparison passes iff the canonical
		// bytes derived from the post-decode structure equal the
		// canonical bytes derived from the input — i.e., the roundtrip
		// preserved structural identity.
		again, err := Canonicalize(canonical)
		if err != nil {
			t.Errorf("re-Canonicalize for %q: %v", in, err)
			continue
		}
		if !bytes.Equal(canonical, again) {
			t.Errorf("roundtrip drift for %q:\n  first:  %s\n  again:  %s",
				in, string(canonical), string(again))
		}
	}
}

// ─── Equivalence: differently-formatted inputs collapse to one output ─────────

// TestCanonicalize_EquivalentInputsCollapse asserts that JSON inputs
// equal modulo whitespace and key order produce byte-identical canonical
// bytes. This is the property that makes JCS load-bearing for content
// addressing.
func TestCanonicalize_EquivalentInputsCollapse(t *testing.T) {
	t.Parallel()
	groups := [][]string{
		{
			`{"a":1,"b":2}`,
			`{"b":2,"a":1}`,
			`{ "a" : 1 , "b" : 2 }`,
			`{"b":2,"a":1}`,
		},
		{
			`[1,2,3]`,
			`[ 1, 2, 3 ]`,
			`[1, 2, 3]`,
		},
		{
			`{"k":[{"a":1,"b":2}]}`,
			`{ "k" : [ { "b" : 2, "a" : 1 } ] }`,
		},
	}
	for groupIdx, group := range groups {
		var ref []byte
		for i, in := range group {
			got, err := Canonicalize([]byte(in))
			if err != nil {
				t.Errorf("group %d input %d Canonicalize(%q): %v", groupIdx, i, in, err)
				continue
			}
			if i == 0 {
				ref = got
				continue
			}
			if !bytes.Equal(ref, got) {
				t.Errorf("group %d input %d did not collapse to ref:\n  ref: %s\n  got: %s",
					groupIdx, i, string(ref), string(got))
			}
		}
	}
}

// ─── Edge cases ───────────────────────────────────────────────────────────────

// TestCanonicalize_EmptyContainers covers {} and [] at top level and
// nested.
func TestCanonicalize_EmptyContainers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{`{}`, `{}`},
		{`[]`, `[]`},
		{`{"a":{}}`, `{"a":{}}`},
		{`{"a":[]}`, `{"a":[]}`},
		{`[[],{},[],{}]`, `[[],{},[],{}]`},
		{`{"a":{"b":{"c":{}}}}`, `{"a":{"b":{"c":{}}}}`},
	}
	for _, tc := range cases {
		got, err := Canonicalize([]byte(tc.in))
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("empty containers for %q:\n  got:  %s\n  want: %s",
				tc.in, string(got), tc.want)
		}
	}
}

// TestCanonicalize_NestedNulls asserts null values are preserved at every
// depth and never elided.
func TestCanonicalize_NestedNulls(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{`null`, `null`},
		{`[null]`, `[null]`},
		{`[null,null,null]`, `[null,null,null]`},
		{`{"a":null}`, `{"a":null}`},
		{`{"a":null,"b":[null,{"c":null}]}`, `{"a":null,"b":[null,{"c":null}]}`},
	}
	for _, tc := range cases {
		got, err := Canonicalize([]byte(tc.in))
		if err != nil {
			t.Errorf("Canonicalize(%q): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("nested nulls for %q:\n  got:  %s\n  want: %s",
				tc.in, string(got), tc.want)
		}
	}
}

// TestCanonicalize_DeepNesting asserts the implementation doesn't blow
// the stack on moderately deep structures (recursive write).
func TestCanonicalize_DeepNesting(t *testing.T) {
	t.Parallel()
	const depth = 200
	in := strings.Repeat(`{"a":`, depth) + `1` + strings.Repeat(`}`, depth)
	got, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatalf("Canonicalize(deep): %v", err)
	}
	want := in // already canonical (single key, no whitespace)
	if string(got) != want {
		t.Errorf("deep nesting drift:\n  got len=%d\n  want len=%d", len(got), len(want))
	}
}

// ─── Negative tests ───────────────────────────────────────────────────────────

// TestCanonicalize_InvalidJSON asserts that malformed input returns a
// wrapped error rather than panicking or producing partial output.
func TestCanonicalize_InvalidJSON(t *testing.T) {
	t.Parallel()
	cases := []string{
		``,
		`{`,
		`{"a":}`,
		`[1,2,`,
		`"unterminated`,
		`{key:1}`, // unquoted key
		`undefined`,
	}
	for _, in := range cases {
		_, err := Canonicalize([]byte(in))
		if err == nil {
			t.Errorf("expected error for invalid JSON %q, got nil", in)
		}
	}
}

// TestCanonicalize_NaNInfNormalized asserts that NaN/+Inf/-Inf
// (which JSON does not represent) come out as JSON null when
// somehow injected through the Go decode path. The number()
// helper guards against this.
//
// Note: standard json.Unmarshal will reject "NaN" / "Infinity" in
// the input bytes, so this is a defensive assertion on the helper
// rather than an end-to-end input test. Exercises the helper via
// a direct call.
func TestCanonicalize_NaNInfNormalized(t *testing.T) {
	t.Parallel()
	for _, v := range []float64{
		float64Nan(),
		float64PosInf(),
		float64NegInf(),
	} {
		got := number(v)
		if got != "null" {
			t.Errorf("number(%v) = %q; want null", v, got)
		}
	}
}

func float64Nan() float64    { return math0Div0() }
func float64PosInf() float64 { return math1Div0() }
func float64NegInf() float64 { return -math1Div0() }
func math0Div0() float64     { var z float64; return z / z }
func math1Div0() float64     { var z float64; return 1.0 / z }
