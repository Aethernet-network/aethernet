package validatorlifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateManifestJSON_Deterministic(t *testing.T) {
	m := DefaultTestnetManifest()
	data1, err := GenerateManifestJSON(m)
	if err != nil {
		t.Fatalf("generate 1: %v", err)
	}
	data2, err := GenerateManifestJSON(m)
	if err != nil {
		t.Fatalf("generate 2: %v", err)
	}
	if string(data1) != string(data2) {
		t.Fatal("manifest JSON should be deterministic")
	}
}

func TestGenerateManifestJSON_InvalidManifest(t *testing.T) {
	m := &GenesisManifest{} // empty
	_, err := GenerateManifestJSON(m)
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
}

func TestWriteManifestFile_RoundTrip(t *testing.T) {
	m := DefaultTestnetManifest()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	if err := WriteManifestFile(m, path); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := LoadManifestFromFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	d1, _ := ComputeManifestDigest(m)
	d2, _ := ComputeManifestDigest(loaded)
	if d1 != d2 {
		t.Fatalf("round-trip changed digest: %s vs %s", d1, d2)
	}
}

func TestComputeManifestDigest_Deterministic(t *testing.T) {
	m := DefaultTestnetManifest()
	d1, _ := ComputeManifestDigest(m)
	d2, _ := ComputeManifestDigest(m)
	if d1 != d2 {
		t.Fatalf("digest should be deterministic: %s vs %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("expected 64-char hex digest, got %d chars", len(d1))
	}
}

func TestComputeManifestDigest_DifferentManifests(t *testing.T) {
	m1 := DefaultTestnetManifest()
	m2 := &GenesisManifest{
		Entries: []GenesisManifestEntry{
			{ValidatorID: "different", OperatorAgentID: "diff-key", ConsensusPublicKey: "diff-key",
				KeyEpoch: 1, BondedStake: 999_999, InitialStatus: SeatActive},
		},
	}
	d1, _ := ComputeManifestDigest(m1)
	d2, _ := ComputeManifestDigest(m2)
	if d1 == d2 {
		t.Fatal("different manifests should produce different digests")
	}
}

func TestVerifyManifestConsistency_Match(t *testing.T) {
	m := DefaultTestnetManifest()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	_ = WriteManifestFile(m, path)

	expected, _ := ComputeManifestDigest(m)
	if err := VerifyManifestConsistency(path, expected); err != nil {
		t.Fatalf("consistency check should pass: %v", err)
	}
}

func TestVerifyManifestConsistency_Mismatch(t *testing.T) {
	m := DefaultTestnetManifest()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	_ = WriteManifestFile(m, path)

	if err := VerifyManifestConsistency(path, "wrong-digest"); err == nil {
		t.Fatal("consistency check should fail with wrong digest")
	}
}

func TestVerifyManifestConsistency_MissingFile(t *testing.T) {
	if err := VerifyManifestConsistency("/nonexistent/path", "any"); err == nil {
		t.Fatal("should fail for missing file")
	}
}

func TestFormatManifestSummary(t *testing.T) {
	m := DefaultTestnetManifest()
	summary := FormatManifestSummary(m)
	if summary == "" {
		t.Fatal("summary should not be empty")
	}
	// Should contain key information.
	if !containsAll(summary, "Genesis Validator Manifest", "Snapshot digest") {
		t.Fatalf("summary missing expected content:\n%s", summary)
	}
}

func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestManifestJSON_ParseableByExternal(t *testing.T) {
	m := DefaultTestnetManifest()
	data, _ := GenerateManifestJSON(m)

	// Verify it's valid JSON that can be parsed by the standard library.
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("manifest JSON should be parseable: %v", err)
	}

	entries, ok := parsed["entries"].([]interface{})
	if !ok || len(entries) == 0 {
		t.Fatal("parsed manifest should have entries array")
	}
}

func TestWriteManifestFile_Permissions(t *testing.T) {
	m := DefaultTestnetManifest()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	_ = WriteManifestFile(m, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm&0o200 == 0 {
		t.Fatalf("manifest file should be writable by owner, got %o", perm)
	}
}
