package validatorlifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// GenerateManifestJSON produces a pretty-printed JSON representation of the
// manifest suitable for writing to a file or displaying to an operator.
func GenerateManifestJSON(m *GenesisManifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(m, "", "  ")
}

// WriteManifestFile writes a validated manifest to a JSON file at the given
// path. The file is created with 0644 permissions.
func WriteManifestFile(m *GenesisManifest, path string) error {
	data, err := GenerateManifestJSON(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ComputeManifestDigest validates the manifest, seeds a Reducer, and returns
// the snapshot digest that all nodes should produce from this manifest.
func ComputeManifestDigest(m *GenesisManifest) (string, error) {
	r, err := SeedReducerFromManifest(m)
	if err != nil {
		return "", err
	}
	snap := r.Snapshot()
	return snap.ComputeDigest(), nil
}

// VerifyManifestConsistency loads a manifest file, computes its digest, and
// compares it to the expected digest. Returns nil if they match, or an error
// describing the mismatch.
func VerifyManifestConsistency(path, expectedDigest string) error {
	m, err := LoadManifestFromFile(path)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	actual, err := ComputeManifestDigest(m)
	if err != nil {
		return fmt.Errorf("compute digest: %w", err)
	}
	if actual != expectedDigest {
		return fmt.Errorf("digest mismatch: expected %s, got %s", expectedDigest, actual)
	}
	return nil
}

// FormatManifestSummary produces a human-readable summary of the manifest
// for operator verification.
func FormatManifestSummary(m *GenesisManifest) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Genesis Validator Manifest (%d seats)\n", len(m.Entries)))
	b.WriteString(strings.Repeat("─", 60) + "\n")

	var totalStake uint64
	for i, e := range m.Entries {
		b.WriteString(fmt.Sprintf("  [%d] ID: %s\n", i+1, e.ValidatorID))
		b.WriteString(fmt.Sprintf("      Operator: %s\n", e.OperatorAgentID))
		b.WriteString(fmt.Sprintf("      Key:      %s\n", e.ConsensusPublicKey))
		b.WriteString(fmt.Sprintf("      Stake:    %d µAET (%d AET)\n", e.BondedStake, e.BondedStake/1_000_000))
		b.WriteString(fmt.Sprintf("      Status:   %s\n", e.InitialStatus))
		totalStake += e.BondedStake
	}

	b.WriteString(strings.Repeat("─", 60) + "\n")
	b.WriteString(fmt.Sprintf("Total bonded stake: %d µAET (%d AET)\n", totalStake, totalStake/1_000_000))

	digest, err := ComputeManifestDigest(m)
	if err != nil {
		b.WriteString(fmt.Sprintf("Digest: ERROR (%v)\n", err))
	} else {
		b.WriteString(fmt.Sprintf("Snapshot digest: %s\n", digest))
	}

	return b.String()
}
