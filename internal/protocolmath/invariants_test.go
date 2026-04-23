package protocolmath

import (
	"errors"
	"reflect"
	"testing"
)

func TestAllocate_NegativeWeight_ReturnsError(t *testing.T) {
	// One negative weight — even if the other recipients are valid and
	// the pool is nonzero, the allocator must refuse to produce output.
	got, err := Allocate([]Recipient{
		{CanonicalKey: []byte("alice"), Weight: 10000},
		{CanonicalKey: []byte("bob"), Weight: -1},
	}, 1_000_000)
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("expected ErrInvariantViolation, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on invariant violation; got %v", got)
	}
}

func TestAllocateWithCeiling_ClampsHighWeights(t *testing.T) {
	// One weight far above MaxBasisPoints; the ceiling variant should
	// clamp it down and produce the same allocation as if the caller had
	// passed Weight = MaxBasisPoints explicitly.
	recsClamp := []Recipient{
		{CanonicalKey: []byte("alice"), Weight: 999_999}, // will clamp
		{CanonicalKey: []byte("bob"), Weight: MaxBasisPoints},
	}
	recsEqual := []Recipient{
		{CanonicalKey: []byte("alice"), Weight: MaxBasisPoints},
		{CanonicalKey: []byte("bob"), Weight: MaxBasisPoints},
	}

	gotClamp, err := AllocateWithCeiling(recsClamp, 1_000_000)
	if err != nil {
		t.Fatalf("AllocateWithCeiling: %v", err)
	}
	gotEqual, err := Allocate(recsEqual, 1_000_000)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if !reflect.DeepEqual(gotClamp, gotEqual) {
		t.Errorf("clamped allocation differs from MaxBasisPoints allocation:\nclamp=%v\nequal=%v",
			gotClamp, gotEqual)
	}
}

func TestAllocateWithCeiling_NegativesStillError(t *testing.T) {
	// Clamping does not convert a negative weight to zero. Invariant
	// still fires because the upstream producer has a bug that must be
	// surfaced.
	got, err := AllocateWithCeiling([]Recipient{
		{CanonicalKey: []byte("alice"), Weight: 10000},
		{CanonicalKey: []byte("bob"), Weight: -5},
	}, 100_000)
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("expected ErrInvariantViolation even with clamp; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
}

func TestAllocate_DuplicateCanonicalKeys_ReturnsError(t *testing.T) {
	// Documented behavior: two recipients with equal CanonicalKey is an
	// upstream bug; the allocator surfaces ErrDuplicateCanonicalKey
	// rather than silently merging or overwriting.
	got, err := Allocate([]Recipient{
		{CanonicalKey: []byte("alice"), Weight: 10000},
		{CanonicalKey: []byte("bob"), Weight: 5000},
		{CanonicalKey: []byte("alice"), Weight: 3000}, // dup
	}, 100)
	if !errors.Is(err, ErrDuplicateCanonicalKey) {
		t.Fatalf("expected ErrDuplicateCanonicalKey, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on duplicate; got %v", got)
	}
}
