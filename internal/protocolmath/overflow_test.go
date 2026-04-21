package protocolmath

import (
	"math/big"
	"testing"
)

func TestAllocate_NearMaxPool(t *testing.T) {
	// Pool near 10^18 (within uint64 but large enough that naive
	// int64-only multiplication of pool * weight would overflow). The
	// big.Int implementation must handle it cleanly, produce the
	// correct weighted split, and conserve exactly.
	const pool MicroAET = 1_000_000_000_000_000_000 // 10^18
	recs := []Recipient{
		{CanonicalKey: []byte("alice"), Weight: MaxBasisPoints},
		{CanonicalKey: []byte("bob"), Weight: MaxBasisPoints},
		{CanonicalKey: []byte("carol"), Weight: MaxBasisPoints},
	}
	got, err := Allocate(recs, pool)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	// Equal weights, equal split. pool / 3 each, last absorbs remainder.
	var sum uint64
	for _, v := range got {
		sum += uint64(v)
	}
	if sum != uint64(pool) {
		t.Errorf("conservation broken at near-max pool: sum=%d pool=%d", sum, pool)
	}
	// Expected per-share: floor(10^18 / 3) = 333...3 (18 digits)
	expected := uint64(pool) / 3
	if got["alice"] != MicroAET(expected) || got["bob"] != MicroAET(expected) {
		t.Errorf("unexpected split: alice=%d bob=%d want each=%d",
			got["alice"], got["bob"], expected)
	}
	// carol absorbs remainder: pool - 2*expected
	if got["carol"] != MicroAET(uint64(pool)-2*expected) {
		t.Errorf("remainder mismatch: carol=%d want %d", got["carol"], uint64(pool)-2*expected)
	}
}

func TestMulDivBig_PanicOnOverflow(t *testing.T) {
	// Construct a quotient that does not fit in uint64: a huge `a`, a
	// huge `b`, and a small `c`. The result is the full product divided
	// by c, which for these inputs is well above 2^64. mulDivBig must
	// panic rather than silently truncate.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on overflow; got no panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic value; got %T (%v)", r, r)
		}
		if msg == "" || msg == "protocolmath: mulDivBig divide by zero" {
			t.Fatalf("unexpected panic message: %q", msg)
		}
	}()

	// a = 2^100 (way above uint64.Max), b = 2^100, c = 1 → quotient = 2^200
	a := new(big.Int).Lsh(big.NewInt(1), 100)
	b := new(big.Int).Lsh(big.NewInt(1), 100)
	c := big.NewInt(1)
	_ = mulDivBig(a, b, c) // must panic
}

func TestMulDivBig_PanicOnDivideByZero(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on divide-by-zero; got no panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic value; got %T (%v)", r, r)
		}
		if msg != "protocolmath: mulDivBig divide by zero" {
			t.Fatalf("unexpected panic message: %q", msg)
		}
	}()
	_ = mulDivBig(big.NewInt(10), big.NewInt(20), big.NewInt(0)) // must panic
}
