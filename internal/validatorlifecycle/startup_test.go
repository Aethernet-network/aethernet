package validatorlifecycle

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// ValidateReducer
// ---------------------------------------------------------------------------

func TestStartupCheck_ValidateReducer_Valid(t *testing.T) {
	m := DefaultTestnetManifest()
	r, _ := SeedReducerFromManifest(m)
	sc := DefaultStartupCheck()

	if err := sc.ValidateReducer(r); err != nil {
		t.Fatalf("valid reducer should pass: %v", err)
	}
}

func TestStartupCheck_ValidateReducer_Nil(t *testing.T) {
	sc := DefaultStartupCheck()
	err := sc.ValidateReducer(nil)
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("expected ErrNoSnapshot for nil reducer, got %v", err)
	}
}

func TestStartupCheck_ValidateReducer_Empty(t *testing.T) {
	sc := DefaultStartupCheck()
	r := NewReducer()
	err := sc.ValidateReducer(r)
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("expected ErrNoSnapshot for empty reducer, got %v", err)
	}
}

func TestStartupCheck_ValidateReducer_DigestMatch(t *testing.T) {
	m := DefaultTestnetManifest()
	r, _ := SeedReducerFromManifest(m)
	digest := r.Snapshot().ComputeDigest()

	sc := ProductionStartupCheck(digest)
	if err := sc.ValidateReducer(r); err != nil {
		t.Fatalf("matching digest should pass: %v", err)
	}
}

func TestStartupCheck_ValidateReducer_DigestMismatch(t *testing.T) {
	m := DefaultTestnetManifest()
	r, _ := SeedReducerFromManifest(m)

	sc := ProductionStartupCheck("wrong-digest-value")
	err := sc.ValidateReducer(r)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch, got %v", err)
	}
}

func TestStartupCheck_ValidateReducer_ConsistentAcrossNodes(t *testing.T) {
	m := DefaultTestnetManifest()

	// Simulate 3 nodes computing the digest independently.
	var digests [3]string
	for i := 0; i < 3; i++ {
		r, _ := SeedReducerFromManifest(m)
		snap := r.Snapshot()
		digests[i] = snap.ComputeDigest()
	}

	if digests[0] != digests[1] || digests[1] != digests[2] {
		t.Fatalf("digests diverge across nodes: %s, %s, %s", digests[0], digests[1], digests[2])
	}

	// All 3 pass the same startup check.
	sc := ProductionStartupCheck(digests[0])
	for i := 0; i < 3; i++ {
		r, _ := SeedReducerFromManifest(m)
		if err := sc.ValidateReducer(r); err != nil {
			t.Fatalf("node %d should pass with shared digest: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// ValidateManifest
// ---------------------------------------------------------------------------

func TestStartupCheck_ValidateManifest_Valid(t *testing.T) {
	sc := DefaultStartupCheck()
	m := DefaultTestnetManifest()
	if err := sc.ValidateManifest(m); err != nil {
		t.Fatalf("valid manifest should pass: %v", err)
	}
}

func TestStartupCheck_ValidateManifest_NilOptional(t *testing.T) {
	sc := DefaultStartupCheck() // RequireManifest = false
	if err := sc.ValidateManifest(nil); err != nil {
		t.Fatalf("nil manifest should be accepted when not required: %v", err)
	}
}

func TestStartupCheck_ValidateManifest_NilRequired(t *testing.T) {
	sc := ProductionStartupCheck("any-digest")
	err := sc.ValidateManifest(nil)
	if !errors.Is(err, ErrManifestRequired) {
		t.Fatalf("expected ErrManifestRequired, got %v", err)
	}
}

func TestStartupCheck_ValidateManifest_Invalid(t *testing.T) {
	sc := DefaultStartupCheck()
	m := &GenesisManifest{} // empty entries
	err := sc.ValidateManifest(m)
	if !errors.Is(err, ErrManifestEmpty) {
		t.Fatalf("expected ErrManifestEmpty, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration: invalid manifest prevents consensus start
// ---------------------------------------------------------------------------

func TestStartupCheck_InvalidManifestPreventsConsensus(t *testing.T) {
	// Simulate the startup sequence: load manifest → validate → seed → check.

	// Case 1: invalid manifest in production mode → fatal.
	sc := ProductionStartupCheck("any")
	if err := sc.ValidateManifest(nil); err == nil {
		t.Fatal("production mode should reject nil manifest")
	}

	// Case 2: valid manifest, wrong digest → fatal.
	m := DefaultTestnetManifest()
	r, _ := SeedReducerFromManifest(m)
	sc2 := ProductionStartupCheck("deliberately-wrong")
	if err := sc2.ValidateReducer(r); err == nil {
		t.Fatal("wrong digest should reject reducer")
	}

	// Case 3: valid manifest, correct digest → pass.
	digest := r.Snapshot().ComputeDigest()
	sc3 := ProductionStartupCheck(digest)
	if err := sc3.ValidateReducer(r); err != nil {
		t.Fatalf("correct digest should pass: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Consistent manifest => consistent validator set hash
// ---------------------------------------------------------------------------

func TestStartupCheck_ConsistentManifest_ConsistentHash(t *testing.T) {
	manifests := []*GenesisManifest{
		DefaultTestnetManifest(),
		{Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "k1", ConsensusPublicKey: "k1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
			{ValidatorID: "v2", OperatorAgentID: "k2", ConsensusPublicKey: "k2",
				KeyEpoch: 1, BondedStake: 200_000, InitialStatus: SeatActive},
		}},
	}

	for i, m := range manifests {
		d1, err1 := ComputeManifestDigest(m)
		d2, err2 := ComputeManifestDigest(m)
		if err1 != nil || err2 != nil {
			t.Fatalf("manifest %d: compute error: %v / %v", i, err1, err2)
		}
		if d1 != d2 {
			t.Fatalf("manifest %d: same manifest produced different digests: %s vs %s", i, d1, d2)
		}
	}
}
