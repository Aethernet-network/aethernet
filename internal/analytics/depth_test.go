package analytics_test

import (
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/analytics"
	"github.com/Aethernet-network/aethernet/internal/evidence"
)

type mockLookup struct {
	packets map[string]*evidence.Evidence
}

func (m *mockLookup) GetEvidenceByHash(hash string) (*evidence.Evidence, error) {
	ev, ok := m.packets[hash]
	if !ok {
		return nil, fmt.Errorf("not found: %s", hash)
	}
	return ev, nil
}

func TestVerificationDepth_Leaf(t *testing.T) {
	lookup := &mockLookup{packets: map[string]*evidence.Evidence{
		"hash-A": {Hash: "hash-A"},
	}}
	depth, err := analytics.ComputeVerificationDepth("hash-A", lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if depth != 1 {
		t.Errorf("depth = %d; want 1", depth)
	}
}

func TestVerificationDepth_Chain(t *testing.T) {
	// A (depth 1) → B references A (depth 2) → C references B (depth 3)
	lookup := &mockLookup{packets: map[string]*evidence.Evidence{
		"hash-A": {Hash: "hash-A"},
		"hash-B": {Hash: "hash-B", GenesisChain: []string{"hash-A"}},
		"hash-C": {Hash: "hash-C", GenesisChain: []string{"hash-B"}},
	}}

	d, _ := analytics.ComputeVerificationDepth("hash-A", lookup)
	if d != 1 {
		t.Errorf("A depth = %d; want 1", d)
	}
	d, _ = analytics.ComputeVerificationDepth("hash-B", lookup)
	if d != 2 {
		t.Errorf("B depth = %d; want 2", d)
	}
	d, _ = analytics.ComputeVerificationDepth("hash-C", lookup)
	if d != 3 {
		t.Errorf("C depth = %d; want 3", d)
	}
}

func TestVerificationDepth_Diamond(t *testing.T) {
	// D references A (depth 1) and C (depth 3) → depth = 1 + 3 + 1 = 5
	lookup := &mockLookup{packets: map[string]*evidence.Evidence{
		"hash-A": {Hash: "hash-A"},
		"hash-B": {Hash: "hash-B", GenesisChain: []string{"hash-A"}},
		"hash-C": {Hash: "hash-C", GenesisChain: []string{"hash-B"}},
		"hash-D": {Hash: "hash-D", GenesisChain: []string{"hash-A", "hash-C"}},
	}}

	d, _ := analytics.ComputeVerificationDepth("hash-D", lookup)
	if d != 5 {
		t.Errorf("D depth = %d; want 5", d)
	}
}

func TestVerificationDepth_CycleDetection(t *testing.T) {
	lookup := &mockLookup{packets: map[string]*evidence.Evidence{
		"hash-X": {Hash: "hash-X", GenesisChain: []string{"hash-Y"}},
		"hash-Y": {Hash: "hash-Y", GenesisChain: []string{"hash-X"}},
	}}

	_, err := analytics.ComputeVerificationDepth("hash-X", lookup)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

func TestVerificationDepth_UnknownHash(t *testing.T) {
	lookup := &mockLookup{packets: map[string]*evidence.Evidence{}}
	depth, err := analytics.ComputeVerificationDepth("hash-unknown", lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if depth != 1 {
		t.Errorf("depth = %d; want 1 (unknown treated as leaf)", depth)
	}
}
