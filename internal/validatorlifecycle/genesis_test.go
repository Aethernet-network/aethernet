package validatorlifecycle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Manifest Validation
// ---------------------------------------------------------------------------

func TestGenesisManifest_Validate_Valid(t *testing.T) {
	m := DefaultTestnetManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("default testnet manifest should be valid: %v", err)
	}
}

func TestGenesisManifest_Validate_Empty(t *testing.T) {
	m := &GenesisManifest{}
	if err := m.Validate(); !errors.Is(err, ErrManifestEmpty) {
		t.Fatalf("expected ErrManifestEmpty, got %v", err)
	}
}

func TestGenesisManifest_Validate_DuplicateID(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1", KeyEpoch: 1, BondedStake: 100, InitialStatus: SeatActive},
			{ValidatorID: "v1", OperatorAgentID: "op2", ConsensusPublicKey: "cpk2", KeyEpoch: 1, BondedStake: 200, InitialStatus: SeatActive},
		},
	}
	if err := m.Validate(); !errors.Is(err, ErrManifestDuplicateID) {
		t.Fatalf("expected ErrManifestDuplicateID, got %v", err)
	}
}

func TestGenesisManifest_Validate_DuplicateKey(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk-same", KeyEpoch: 1, BondedStake: 100, InitialStatus: SeatActive},
			{ValidatorID: "v2", OperatorAgentID: "op2", ConsensusPublicKey: "cpk-same", KeyEpoch: 1, BondedStake: 200, InitialStatus: SeatActive},
		},
	}
	if err := m.Validate(); !errors.Is(err, ErrManifestDuplicateKey) {
		t.Fatalf("expected ErrManifestDuplicateKey, got %v", err)
	}
}

func TestGenesisManifest_Validate_MissingFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GenesisManifestEntry)
	}{
		{"missing_validator_id", func(e *GenesisManifestEntry) { e.ValidatorID = "" }},
		{"missing_operator", func(e *GenesisManifestEntry) { e.OperatorAgentID = "" }},
		{"missing_consensus_key", func(e *GenesisManifestEntry) { e.ConsensusPublicKey = "" }},
		{"missing_initial_status", func(e *GenesisManifestEntry) { e.InitialStatus = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := GenesisManifestEntry{
				ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
				KeyEpoch: 1, BondedStake: 100, InitialStatus: SeatActive,
			}
			tt.mutate(&entry)
			m := &GenesisManifest{Entries: []GenesisManifestEntry{entry}}
			if err := m.Validate(); !errors.Is(err, ErrManifestMissingField) {
				t.Fatalf("expected ErrManifestMissingField, got %v", err)
			}
		})
	}
}

func TestGenesisManifest_Validate_ZeroStake(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
				KeyEpoch: 1, BondedStake: 0, InitialStatus: SeatActive},
		},
	}
	if err := m.Validate(); !errors.Is(err, ErrManifestInvalidStake) {
		t.Fatalf("expected ErrManifestInvalidStake, got %v", err)
	}
}

func TestGenesisManifest_Validate_ZeroKeyEpoch(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
				KeyEpoch: 0, BondedStake: 100, InitialStatus: SeatActive},
		},
	}
	if err := m.Validate(); !errors.Is(err, ErrManifestInvalidKeyEpoch) {
		t.Fatalf("expected ErrManifestInvalidKeyEpoch, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Deterministic Loading
// ---------------------------------------------------------------------------

func TestSeedReducerFromManifest_Deterministic(t *testing.T) {
	m := DefaultTestnetManifest()

	r1, err := SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	r2, err := SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("seed 2: %v", err)
	}

	snap1 := r1.Snapshot()
	snap2 := r2.Snapshot()

	d1 := snap1.ComputeDigest()
	d2 := snap2.ComputeDigest()

	if d1 != d2 {
		t.Fatalf("same manifest must produce same digest: %s != %s", d1, d2)
	}
	if snap1.Version != snap2.Version {
		t.Fatalf("same manifest must produce same version: %d != %d", snap1.Version, snap2.Version)
	}
}

func TestSeedReducerFromManifest_SameDigest_MultipleSeats(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
			{ValidatorID: "v2", OperatorAgentID: "op2", ConsensusPublicKey: "cpk2",
				KeyEpoch: 1, BondedStake: 200_000, InitialStatus: SeatActive},
			{ValidatorID: "v3", OperatorAgentID: "op3", ConsensusPublicKey: "cpk3",
				KeyEpoch: 1, BondedStake: 300_000, InitialStatus: SeatProbationary},
		},
	}

	r1, _ := SeedReducerFromManifest(m)
	r2, _ := SeedReducerFromManifest(m)

	d1 := r1.Snapshot().ComputeDigest()
	d2 := r2.Snapshot().ComputeDigest()

	if d1 != d2 {
		t.Fatalf("multi-seat manifest not deterministic: %s != %s", d1, d2)
	}
}

func TestSeedReducerFromManifest_DifferentManifests_DifferentDigests(t *testing.T) {
	m1 := DefaultTestnetManifest()

	m2 := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "different-id", OperatorAgentID: "op-diff", ConsensusPublicKey: "cpk-diff",
				KeyEpoch: 1, BondedStake: 999_999, InitialStatus: SeatActive},
		},
	}

	r1, _ := SeedReducerFromManifest(m1)
	r2, _ := SeedReducerFromManifest(m2)

	d1 := r1.Snapshot().ComputeDigest()
	d2 := r2.Snapshot().ComputeDigest()

	if d1 == d2 {
		t.Fatal("different manifests should produce different digests")
	}
}

// ---------------------------------------------------------------------------
// Reducer State After Seeding
// ---------------------------------------------------------------------------

func TestSeedReducerFromManifest_SeatState(t *testing.T) {
	m := DefaultTestnetManifest()
	r, err := SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	entry := m.Entries[0]
	seat, err := r.Seat(entry.ValidatorID)
	if err != nil {
		t.Fatalf("seat lookup: %v", err)
	}
	if seat.Status != SeatActive {
		t.Fatalf("expected Active, got %s", seat.Status)
	}
	if seat.IsGenesis != true {
		t.Fatal("expected IsGenesis=true")
	}
	if seat.StakeAmount != entry.BondedStake {
		t.Fatalf("expected stake %d, got %d", entry.BondedStake, seat.StakeAmount)
	}
	if seat.OperatorKey != entry.ConsensusPublicKey {
		t.Fatalf("expected key %s, got %s", entry.ConsensusPublicKey, seat.OperatorKey)
	}
	if seat.Weight != entry.BondedStake {
		t.Fatalf("active genesis seat weight should equal stake: got %d", seat.Weight)
	}
	if len(seat.KeyHistory) != 1 || seat.KeyHistory[0].Epoch != 1 {
		t.Fatalf("expected 1 key epoch at epoch 1, got %v", seat.KeyHistory)
	}
}

func TestSeedReducerFromManifest_Probationary(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-prob", OperatorAgentID: "op-p", ConsensusPublicKey: "cpk-p",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatProbationary},
		},
	}
	r, err := SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	seat, _ := r.Seat("v-prob")
	if seat.Status != SeatProbationary {
		t.Fatalf("expected Probationary, got %s", seat.Status)
	}
	if seat.Weight != 50_000 {
		t.Fatalf("probationary weight should be half stake: got %d", seat.Weight)
	}
}

func TestSeedReducerFromManifest_PendingJoin(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-pj", OperatorAgentID: "op-pj", ConsensusPublicKey: "cpk-pj",
				KeyEpoch: 1, BondedStake: 50_000, InitialStatus: SeatPendingJoin},
		},
	}
	r, err := SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	seat, _ := r.Seat("v-pj")
	if seat.Status != SeatPendingJoin {
		t.Fatalf("expected PendingJoin, got %s", seat.Status)
	}
}

func TestSeedReducerFromManifest_UnsupportedInitialStatus(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v-bad", OperatorAgentID: "op-bad", ConsensusPublicKey: "cpk-bad",
				KeyEpoch: 1, BondedStake: 50_000, InitialStatus: SeatExcluded},
		},
	}
	_, err := SeedReducerFromManifest(m)
	if err == nil {
		t.Fatal("expected error for unsupported initial status")
	}
}

func TestSeedReducerFromManifest_InvalidManifest(t *testing.T) {
	m := &GenesisManifest{} // empty
	_, err := SeedReducerFromManifest(m)
	if !errors.Is(err, ErrManifestEmpty) {
		t.Fatalf("expected ErrManifestEmpty, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Snapshot Properties
// ---------------------------------------------------------------------------

func TestSeedReducerFromManifest_SnapshotCountsCorrect(t *testing.T) {
	m := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "v1", OperatorAgentID: "op1", ConsensusPublicKey: "cpk1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: SeatActive},
			{ValidatorID: "v2", OperatorAgentID: "op2", ConsensusPublicKey: "cpk2",
				KeyEpoch: 1, BondedStake: 200_000, InitialStatus: SeatActive},
			{ValidatorID: "v3", OperatorAgentID: "op3", ConsensusPublicKey: "cpk3",
				KeyEpoch: 1, BondedStake: 50_000, InitialStatus: SeatPendingJoin},
		},
	}
	r, err := SeedReducerFromManifest(m)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	snap := r.Snapshot()
	if len(snap.Seats) != 3 {
		t.Fatalf("expected 3 seats, got %d", len(snap.Seats))
	}
	if snap.ActiveSeatCount != 2 {
		t.Fatalf("expected 2 active seats, got %d", snap.ActiveSeatCount)
	}
	if snap.TotalActiveWeight != 300_000 {
		t.Fatalf("expected total active weight 300000, got %d", snap.TotalActiveWeight)
	}
}

// ---------------------------------------------------------------------------
// File Loading
// ---------------------------------------------------------------------------

func TestLoadManifestFromFile_Valid(t *testing.T) {
	m := DefaultTestnetManifest()
	data, _ := json.MarshalIndent(m, "", "  ")

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadManifestFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(loaded.Entries))
	}
	if loaded.Entries[0].ValidatorID != m.Entries[0].ValidatorID {
		t.Fatal("loaded manifest does not match written manifest")
	}
}

func TestLoadManifestFromFile_RoundTrip_SameDigest(t *testing.T) {
	m := DefaultTestnetManifest()
	data, _ := json.MarshalIndent(m, "", "  ")

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	_ = os.WriteFile(path, data, 0o644)

	loaded, _ := LoadManifestFromFile(path)

	r1, _ := SeedReducerFromManifest(m)
	r2, _ := SeedReducerFromManifest(loaded)

	d1 := r1.Snapshot().ComputeDigest()
	d2 := r2.Snapshot().ComputeDigest()
	if d1 != d2 {
		t.Fatalf("file round-trip changed digest: %s != %s", d1, d2)
	}
}

func TestLoadManifestFromFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o644)

	_, err := LoadManifestFromFile(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadManifestFromFile_MissingFile(t *testing.T) {
	_, err := LoadManifestFromFile("/nonexistent/path/manifest.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadManifestFromFile_EmptyManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	_ = os.WriteFile(path, []byte(`{"entries":[]}`), 0o644)

	_, err := LoadManifestFromFile(path)
	if !errors.Is(err, ErrManifestEmpty) {
		t.Fatalf("expected ErrManifestEmpty, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LogSnapshotDigest
// ---------------------------------------------------------------------------

func TestLogSnapshotDigest_Returns64HexChars(t *testing.T) {
	m := DefaultTestnetManifest()
	r, _ := SeedReducerFromManifest(m)
	digest := LogSnapshotDigest(r)
	if len(digest) != 64 {
		t.Fatalf("expected 64-char hex digest, got %d chars", len(digest))
	}
}
