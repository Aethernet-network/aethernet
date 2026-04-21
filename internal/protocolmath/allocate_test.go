package protocolmath

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// recipientsOf builds a []Recipient from (key, weight) pairs. Helper for
// table-driven tests; keys are short ASCII strings for readability.
func recipientsOf(pairs ...struct {
	key    string
	weight BasisPoints
}) []Recipient {
	out := make([]Recipient, len(pairs))
	for i, p := range pairs {
		out[i] = Recipient{CanonicalKey: []byte(p.key), Weight: p.weight}
	}
	return out
}

// sumAmounts returns the integer sum of an allocation map as uint64 so
// tests can assert conservation without overflow-risky arithmetic.
func sumAmounts(m map[string]MicroAET) uint64 {
	var s uint64
	for _, v := range m {
		s += uint64(v)
	}
	return s
}

func TestAllocate_SingleRecipient(t *testing.T) {
	got, err := Allocate(recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"alice", 10000},
	), 1_000_000)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if want := (map[string]MicroAET{"alice": 1_000_000}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestAllocate_TwoEqualRecipients(t *testing.T) {
	got, err := Allocate(recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"alice", 10000},
		struct {
			key    string
			weight BasisPoints
		}{"bob", 10000},
	), 1_000_000)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got["alice"] != 500_000 || got["bob"] != 500_000 {
		t.Errorf("expected 500_000 each; got %v", got)
	}
	if sumAmounts(got) != 1_000_000 {
		t.Errorf("sum = %d, want 1_000_000", sumAmounts(got))
	}
}

func TestAllocate_UnequalWeights_Basic(t *testing.T) {
	got, err := Allocate(recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"alice", 10000},
		struct {
			key    string
			weight BasisPoints
		}{"bob", 20000},
		struct {
			key    string
			weight BasisPoints
		}{"carol", 30000},
	), 600_000)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	// Recipients are sorted by CanonicalKey: alice < bob < carol.
	// With totalWeight = 60000: alice = 600_000 * 10000 / 60000 = 100_000,
	// bob = 200_000, carol = 600_000 - 300_000 = 300_000.
	wants := map[string]MicroAET{"alice": 100_000, "bob": 200_000, "carol": 300_000}
	if !reflect.DeepEqual(got, wants) {
		t.Errorf("got %v want %v", got, wants)
	}
}

func TestAllocate_Rounding_LastRecipientAbsorbs(t *testing.T) {
	// pool=100 divided equally across 3 recipients.
	// 100 * 10000 / 30000 = 33 (floor). First two get 33, last gets 34.
	got, err := Allocate(recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"a", 10000},
		struct {
			key    string
			weight BasisPoints
		}{"b", 10000},
		struct {
			key    string
			weight BasisPoints
		}{"c", 10000},
	), 100)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got["a"] != 33 || got["b"] != 33 || got["c"] != 34 {
		t.Errorf("expected 33/33/34; got %v", got)
	}
	if sumAmounts(got) != 100 {
		t.Errorf("sum = %d want 100", sumAmounts(got))
	}
}

func TestAllocate_ConservationOnManyRecipients(t *testing.T) {
	// 10 recipients with deterministic varied weights, pool is a
	// prime-ish value that guarantees irregular remainders.
	const pool MicroAET = 1_000_003
	weights := []BasisPoints{7, 13, 29, 31, 57, 101, 229, 541, 997, 1999}
	pairs := make([]struct {
		key    string
		weight BasisPoints
	}, 10)
	for i := 0; i < 10; i++ {
		pairs[i] = struct {
			key    string
			weight BasisPoints
		}{key: fmt.Sprintf("r%02d", i), weight: weights[i]}
	}
	got, err := Allocate(recipientsOf(pairs...), pool)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if sumAmounts(got) != uint64(pool) {
		t.Errorf("conservation broken: sum=%d pool=%d", sumAmounts(got), pool)
	}
	if len(got) != 10 {
		t.Errorf("expected 10 entries, got %d", len(got))
	}
}

func TestAllocate_PermutationInvariance(t *testing.T) {
	base := recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"alpha", 5000},
		struct {
			key    string
			weight BasisPoints
		}{"bravo", 12000},
		struct {
			key    string
			weight BasisPoints
		}{"charlie", 2345},
		struct {
			key    string
			weight BasisPoints
		}{"delta", 88888},
	)
	const pool MicroAET = 777_777

	// Three permutations of the same set.
	perms := [][]Recipient{
		{base[0], base[1], base[2], base[3]},
		{base[3], base[2], base[1], base[0]},
		{base[2], base[0], base[3], base[1]},
	}

	first, err := Allocate(perms[0], pool)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	for i := 1; i < len(perms); i++ {
		got, err := Allocate(perms[i], pool)
		if err != nil {
			t.Fatalf("Allocate perm %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, first) {
			t.Errorf("permutation %d produced different output: got %v want %v", i, got, first)
		}
	}
}

func TestAllocate_ThousandRuns_Identical(t *testing.T) {
	// Deterministic random input, run 1000 times, assert every output is
	// byte-identical to the first. Catches contamination from map
	// iteration order or any other nondeterministic source.
	r := rand.New(rand.NewSource(42))
	const n = 8
	pairs := make([]struct {
		key    string
		weight BasisPoints
	}, n)
	for i := 0; i < n; i++ {
		// Key is a 4-byte random sequence, hex-encoded for readability.
		k := make([]byte, 4)
		r.Read(k)
		pairs[i] = struct {
			key    string
			weight BasisPoints
		}{key: string(k), weight: BasisPoints(r.Intn(int(MaxBasisPoints)))}
	}

	recs := recipientsOf(pairs...)
	const pool MicroAET = 1_234_567

	first, err := Allocate(recs, pool)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	firstKeys := sortedKeys(first)
	firstVals := make([]MicroAET, len(firstKeys))
	for i, k := range firstKeys {
		firstVals[i] = first[k]
	}

	for i := 0; i < 1000; i++ {
		got, err := Allocate(recs, pool)
		if err != nil {
			t.Fatalf("Allocate iter %d: %v", i, err)
		}
		gotKeys := sortedKeys(got)
		if !reflect.DeepEqual(gotKeys, firstKeys) {
			t.Fatalf("iter %d: key set changed", i)
		}
		for j, k := range gotKeys {
			if got[k] != firstVals[j] {
				t.Fatalf("iter %d: value for %x changed %d → %d", i, k, firstVals[j], got[k])
			}
		}
	}
}

// sortedKeys returns the map's keys in bytes-ascending order for stable
// comparison across runs.
func sortedKeys(m map[string]MicroAET) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare([]byte(out[i]), []byte(out[j])) < 0
	})
	return out
}

func TestAllocate_ZeroTotalWeight_EvenSplit(t *testing.T) {
	// All weights zero, nonzero pool — fall back to even-split.
	got, err := Allocate(recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"alice", 0},
		struct {
			key    string
			weight BasisPoints
		}{"bob", 0},
		struct {
			key    string
			weight BasisPoints
		}{"carol", 0},
	), 1000)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	// 1000 / 3 = 333 (floor). First two get 333, last absorbs 334.
	if got["alice"] != 333 || got["bob"] != 333 || got["carol"] != 334 {
		t.Errorf("expected 333/333/334; got %v", got)
	}
	if sumAmounts(got) != 1000 {
		t.Errorf("sum = %d want 1000", sumAmounts(got))
	}
}

func TestAllocate_EmptyRecipientsZeroPool(t *testing.T) {
	got, err := Allocate(nil, 0)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
}

func TestAllocate_EmptyRecipientsNonzeroPool(t *testing.T) {
	got, err := Allocate(nil, 100)
	if err != ErrEmptyRecipients {
		t.Fatalf("expected ErrEmptyRecipients, got err=%v out=%v", err, got)
	}
	if len(got) != 0 {
		t.Errorf("expected empty output on error; got %v", got)
	}
}

func TestAllocate_SomeRecipientsZeroWeight(t *testing.T) {
	// Four recipients, only two have weight. Pool should be split
	// between the weighted pair 1:2 (alice:bob), and the zero-weighted
	// pair (carol, dave) should each receive zero.
	got, err := Allocate(recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"alice", 10000},
		struct {
			key    string
			weight BasisPoints
		}{"bob", 20000},
		struct {
			key    string
			weight BasisPoints
		}{"carol", 0},
		struct {
			key    string
			weight BasisPoints
		}{"dave", 0},
	), 900)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	// Sorted order: alice, bob, carol, dave.
	// totalWeight = 30000.
	// alice = floor(900 * 10000 / 30000) = 300
	// bob   = floor(900 * 20000 / 30000) = 600
	// carol = 0 (weight is zero, explicitly enumerated)
	// dave  = pool - distributed = 900 - 300 - 600 - 0 = 0
	wants := map[string]MicroAET{"alice": 300, "bob": 600, "carol": 0, "dave": 0}
	if !reflect.DeepEqual(got, wants) {
		t.Errorf("got %v want %v", got, wants)
	}
	if sumAmounts(got) != 900 {
		t.Errorf("sum = %d want 900", sumAmounts(got))
	}
}

func TestAllocate_SingleRecipientZeroPool(t *testing.T) {
	// Documented behavior: single recipient, pool == 0 → output is
	// {recipient: 0}. Uniform output shape regardless of pool value.
	got, err := Allocate(recipientsOf(
		struct {
			key    string
			weight BasisPoints
		}{"alice", 10000},
	), 0)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if want := (map[string]MicroAET{"alice": 0}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}
