package trajectory_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/trajectory"
)

func testCheckpointBody() trajectory.CheckpointBody {
	return trajectory.CheckpointBody{
		ApproachDescription:    "Using recursive decomposition to solve the optimization problem.",
		Parameters:             map[string]string{"learning_rate": "0.001", "batch_size": "32", "epochs": "100"},
		EvidenceSnippet:        "Loss decreased from 2.3 to 0.8 over 50 epochs.",
		ErrorDetail:            "",
		IntermediateOutputHash: "abc123def456",
	}
}

func TestCanonicalCheckpointBytes_Deterministic(t *testing.T) {
	body := testCheckpointBody()
	a, err := trajectory.CanonicalCheckpointBytes(body)
	if err != nil {
		t.Fatalf("CanonicalCheckpointBytes: %v", err)
	}
	b, err := trajectory.CanonicalCheckpointBytes(body)
	if err != nil {
		t.Fatalf("CanonicalCheckpointBytes 2: %v", err)
	}
	if string(a) != string(b) {
		t.Error("canonical bytes should be deterministic")
	}
}

func TestCanonicalCheckpointBytes_ParameterOrderIndependent(t *testing.T) {
	body1 := trajectory.CheckpointBody{
		ApproachDescription: "test",
		Parameters:          map[string]string{"z": "1", "a": "2", "m": "3"},
	}
	body2 := trajectory.CheckpointBody{
		ApproachDescription: "test",
		Parameters:          map[string]string{"a": "2", "m": "3", "z": "1"},
	}
	a, _ := trajectory.CanonicalCheckpointBytes(body1)
	b, _ := trajectory.CanonicalCheckpointBytes(body2)
	if string(a) != string(b) {
		t.Error("parameter order should not affect canonical bytes")
	}
}

func TestCanonicalCheckpointBytes_NilParameters(t *testing.T) {
	body := trajectory.CheckpointBody{
		ApproachDescription: "test with no params",
	}
	data, err := trajectory.CanonicalCheckpointBytes(body)
	if err != nil {
		t.Fatalf("should handle nil parameters: %v", err)
	}
	if len(data) == 0 {
		t.Error("canonical bytes should not be empty")
	}
}

func TestComputeCheckpointHash_Deterministic(t *testing.T) {
	body := testCheckpointBody()
	h1, s1, err := trajectory.ComputeCheckpointHash(body)
	if err != nil {
		t.Fatalf("ComputeCheckpointHash: %v", err)
	}
	h2, s2, err := trajectory.ComputeCheckpointHash(body)
	if err != nil {
		t.Fatalf("ComputeCheckpointHash 2: %v", err)
	}
	if h1 != h2 {
		t.Error("hash should be deterministic")
	}
	if s1 != s2 {
		t.Error("size should be deterministic")
	}
}

func TestComputeCheckpointHash_SizeMatchesCanonical(t *testing.T) {
	body := testCheckpointBody()
	hash, size, err := trajectory.ComputeCheckpointHash(body)
	if err != nil {
		t.Fatalf("ComputeCheckpointHash: %v", err)
	}
	canonical, _ := trajectory.CanonicalCheckpointBytes(body)
	if size != int64(len(canonical)) {
		t.Errorf("size = %d; want %d (len of canonical bytes)", size, len(canonical))
	}
	if len(hash) != 64 { // SHA-256 hex
		t.Errorf("hash length = %d; want 64", len(hash))
	}
}

func TestComputeCheckpointHash_DiffersForDiffBodies(t *testing.T) {
	body1 := testCheckpointBody()
	body2 := testCheckpointBody()
	body2.ApproachDescription = "Different approach"

	h1, _, _ := trajectory.ComputeCheckpointHash(body1)
	h2, _, _ := trajectory.ComputeCheckpointHash(body2)
	if h1 == h2 {
		t.Error("different bodies should produce different hashes")
	}
}
