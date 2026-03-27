package trajectory_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/trajectory"
)

func TestComputeExplorationRoot_Deterministic(t *testing.T) {
	ids := []string{"commit-c", "commit-a", "commit-b"}
	r1, err := trajectory.ComputeExplorationRoot(ids)
	if err != nil {
		t.Fatalf("ComputeExplorationRoot: %v", err)
	}
	r2, err := trajectory.ComputeExplorationRoot(ids)
	if err != nil {
		t.Fatalf("ComputeExplorationRoot 2: %v", err)
	}
	if r1 != r2 {
		t.Error("root should be deterministic for same input")
	}
	if len(r1) != 64 { // SHA-256 hex
		t.Errorf("root length = %d; want 64", len(r1))
	}
}

func TestComputeExplorationRoot_OrderInsensitive(t *testing.T) {
	ids1 := []string{"z-commit", "a-commit", "m-commit"}
	ids2 := []string{"a-commit", "m-commit", "z-commit"}
	ids3 := []string{"m-commit", "z-commit", "a-commit"}

	r1, _ := trajectory.ComputeExplorationRoot(ids1)
	r2, _ := trajectory.ComputeExplorationRoot(ids2)
	r3, _ := trajectory.ComputeExplorationRoot(ids3)

	if r1 != r2 || r2 != r3 {
		t.Error("root should be the same regardless of input order")
	}
}

func TestComputeExplorationRoot_EmptySet(t *testing.T) {
	root, err := trajectory.ComputeExplorationRoot(nil)
	if err != nil {
		t.Fatalf("ComputeExplorationRoot(nil): %v", err)
	}
	if root != "" {
		t.Errorf("empty set root = %q; want empty string", root)
	}

	root2, _ := trajectory.ComputeExplorationRoot([]string{})
	if root2 != "" {
		t.Errorf("empty slice root = %q; want empty string", root2)
	}
}

func TestComputeExplorationRoot_SingleElement(t *testing.T) {
	root, err := trajectory.ComputeExplorationRoot([]string{"only-one"})
	if err != nil {
		t.Fatalf("ComputeExplorationRoot: %v", err)
	}
	if root == "" {
		t.Error("single element should produce non-empty root")
	}
	if len(root) != 64 {
		t.Errorf("root length = %d; want 64", len(root))
	}
}

func TestComputeExplorationRoot_DifferentSetsProduceDifferentRoots(t *testing.T) {
	r1, _ := trajectory.ComputeExplorationRoot([]string{"a", "b"})
	r2, _ := trajectory.ComputeExplorationRoot([]string{"a", "c"})
	if r1 == r2 {
		t.Error("different sets should produce different roots")
	}
}

func TestSampleExplorationCommits_Bounded(t *testing.T) {
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = string(rune('a' + i))
	}

	sample := trajectory.SampleExplorationCommits(ids, 10)
	if len(sample) > 10 {
		t.Errorf("sample = %d; want <= 10", len(sample))
	}
}

func TestSampleExplorationCommits_MaxEnforced(t *testing.T) {
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = string(rune('a' + i%26))
	}

	sample := trajectory.SampleExplorationCommits(ids, trajectory.MaxExplorationSample)
	if len(sample) != trajectory.MaxExplorationSample {
		t.Errorf("sample = %d; want %d", len(sample), trajectory.MaxExplorationSample)
	}
}

func TestSampleExplorationCommits_Deterministic(t *testing.T) {
	ids := []string{"z", "a", "m", "f", "c"}
	s1 := trajectory.SampleExplorationCommits(ids, 3)
	s2 := trajectory.SampleExplorationCommits(ids, 3)

	if len(s1) != len(s2) {
		t.Fatalf("sample sizes differ: %d vs %d", len(s1), len(s2))
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			t.Errorf("sample[%d] = %q vs %q", i, s1[i], s2[i])
		}
	}
}

func TestSampleExplorationCommits_SmallSet(t *testing.T) {
	ids := []string{"b", "a"}
	sample := trajectory.SampleExplorationCommits(ids, 10)
	if len(sample) != 2 {
		t.Errorf("sample = %d; want 2 (all elements)", len(sample))
	}
	// Should be sorted.
	if sample[0] != "a" || sample[1] != "b" {
		t.Errorf("sample not sorted: %v", sample)
	}
}

func TestSampleExplorationCommits_Empty(t *testing.T) {
	sample := trajectory.SampleExplorationCommits(nil, 10)
	if len(sample) != 0 {
		t.Errorf("sample = %d; want 0", len(sample))
	}
}
