package replay

import (
	"fmt"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ---------------------------------------------------------------------------
// In-memory replayStore implementation for tests
// ---------------------------------------------------------------------------

type memStore struct {
	jobs     map[string][]byte
	outcomes map[string][]byte
}

func newMemStore() *memStore {
	return &memStore{
		jobs:     make(map[string][]byte),
		outcomes: make(map[string][]byte),
	}
}

func (m *memStore) PutReplayJob(id string, data []byte) error {
	m.jobs[id] = append([]byte{}, data...)
	return nil
}

func (m *memStore) GetReplayJob(id string) ([]byte, error) {
	data, ok := m.jobs[id]
	if !ok {
		return nil, fmt.Errorf("replay job not found: %s", id)
	}
	return append([]byte{}, data...), nil
}

func (m *memStore) AllReplayJobs() (map[string][]byte, error) {
	cp := make(map[string][]byte, len(m.jobs))
	for k, v := range m.jobs {
		cp[k] = append([]byte{}, v...)
	}
	return cp, nil
}

func (m *memStore) PutReplayOutcome(id string, data []byte) error {
	m.outcomes[id] = append([]byte{}, data...)
	return nil
}

func (m *memStore) GetReplayOutcome(id string) ([]byte, error) {
	data, ok := m.outcomes[id]
	if !ok {
		return nil, fmt.Errorf("replay outcome not found: %s", id)
	}
	return append([]byte{}, data...), nil
}

func (m *memStore) AllReplayOutcomes() (map[string][]byte, error) {
	cp := make(map[string][]byte, len(m.outcomes))
	for k, v := range m.outcomes {
		cp[k] = append([]byte{}, v...)
	}
	return cp, nil
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func fullReqs() *verification.ReplayRequirements {
	return &verification.ReplayRequirements{
		SourceSnapshotHash:      "sha256:src1234",
		AcceptanceContractHash:  "sha256:contract5678",
		RequiredChecks:          []string{"go_test", "lint"},
		EnvironmentManifestHash: "sha256:env9abc",
		ToolchainManifestHash:   "sha256:tool0def",
		CommandSpecs: []verification.CommandSpec{
			{CheckType: "go_test", Command: []string{"go", "test", "./..."}, WorkingDir: "."},
		},
		ArtifactRefs: []verification.ArtifactRef{
			{CheckType: "go_test", ArtifactType: "stdout", Hash: "sha256:out123", SizeBytes: 512},
		},
		MachineReadableResults: map[string]interface{}{"go_test": true},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNewReplayJob_DeterministicID(t *testing.T) {
	reqs := fullReqs()
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	j1 := NewReplayJob("task-1", "sha256:pkt1", "code", "v1", "agent-1", "spot-check", reqs, ts)
	j2 := NewReplayJob("task-1", "sha256:pkt1", "code", "v1", "agent-1", "spot-check", reqs, ts)

	if j1.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if j1.ID != j2.ID {
		t.Errorf("expected deterministic IDs to match: %q != %q", j1.ID, j2.ID)
	}
}

func TestNewReplayJob_FieldsCopiedFromRequirements(t *testing.T) {
	reqs := fullReqs()
	ts := time.Now()

	j := NewReplayJob("task-2", "sha256:pkt2", "code", "v1", "agent-2", "routine", reqs, ts)

	if j.TaskID != "task-2" {
		t.Errorf("TaskID: want %q, got %q", "task-2", j.TaskID)
	}
	if j.SourceSnapshotHash != reqs.SourceSnapshotHash {
		t.Errorf("SourceSnapshotHash: want %q, got %q", reqs.SourceSnapshotHash, j.SourceSnapshotHash)
	}
	if len(j.ChecksToReplay) != 2 || j.ChecksToReplay[0] != "go_test" || j.ChecksToReplay[1] != "lint" {
		t.Errorf("ChecksToReplay: want [go_test lint], got %v", j.ChecksToReplay)
	}
	if j.EnvironmentManifestHash != reqs.EnvironmentManifestHash {
		t.Errorf("EnvironmentManifestHash: want %q, got %q", reqs.EnvironmentManifestHash, j.EnvironmentManifestHash)
	}
	if j.ToolchainManifestHash != reqs.ToolchainManifestHash {
		t.Errorf("ToolchainManifestHash: want %q, got %q", reqs.ToolchainManifestHash, j.ToolchainManifestHash)
	}
	if len(j.CommandSpecs) != 1 {
		t.Errorf("CommandSpecs: want 1, got %d", len(j.CommandSpecs))
	}
	if len(j.ArtifactRefs) != 1 {
		t.Errorf("ArtifactRefs: want 1, got %d", len(j.ArtifactRefs))
	}
	if j.Status != "pending" {
		t.Errorf("Status: want %q, got %q", "pending", j.Status)
	}
}

func TestReplayOutcome_HasMismatch_TrueWhenOneMismatch(t *testing.T) {
	o := &ReplayOutcome{
		JobID:  "job-1",
		TaskID: "task-1",
		Comparisons: []CheckComparison{
			{CheckType: "go_test", Match: true},
			{CheckType: "lint", Match: false, Detail: "output differs"},
		},
	}

	if !o.HasMismatch() {
		t.Error("expected HasMismatch=true when one comparison has Match=false")
	}
	if o.MismatchCount() != 1 {
		t.Errorf("MismatchCount: want 1, got %d", o.MismatchCount())
	}
}

func TestReplayOutcome_HasMismatch_FalseWhenAllMatch(t *testing.T) {
	o := &ReplayOutcome{
		JobID:  "job-2",
		TaskID: "task-2",
		Comparisons: []CheckComparison{
			{CheckType: "go_test", Match: true},
			{CheckType: "lint", Match: true},
		},
	}

	if o.HasMismatch() {
		t.Error("expected HasMismatch=false when all comparisons match")
	}
	if o.MismatchCount() != 0 {
		t.Errorf("MismatchCount: want 0, got %d", o.MismatchCount())
	}
}

func TestManager_JobRoundTrip(t *testing.T) {
	mgr := NewManager(newMemStore())
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	original := NewReplayJob("task-rt", "sha256:pkthash", "code", "v1", "agent-rt", "audit", fullReqs(), ts)
	if err := mgr.PutJob(original); err != nil {
		t.Fatalf("PutJob: %v", err)
	}

	got, err := mgr.GetJob(original.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ID != original.ID {
		t.Errorf("ID: want %q, got %q", original.ID, got.ID)
	}
	if got.TaskID != original.TaskID {
		t.Errorf("TaskID: want %q, got %q", original.TaskID, got.TaskID)
	}
	if got.SourceSnapshotHash != original.SourceSnapshotHash {
		t.Errorf("SourceSnapshotHash: want %q, got %q", original.SourceSnapshotHash, got.SourceSnapshotHash)
	}
	if got.Status != "pending" {
		t.Errorf("Status: want %q, got %q", "pending", got.Status)
	}
}

func TestManager_OutcomeRoundTrip(t *testing.T) {
	mgr := NewManager(newMemStore())

	original := &ReplayOutcome{
		JobID:      "job-rt",
		TaskID:     "task-rt",
		ReplayerID: "replayer-1",
		ReplayedAt: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
		Comparisons: []CheckComparison{
			{CheckType: "go_test", OriginalHash: "sha256:abc", ReplayedHash: "sha256:abc", Match: true},
		},
	}
	if err := mgr.PutOutcome(original); err != nil {
		t.Fatalf("PutOutcome: %v", err)
	}

	got, err := mgr.GetOutcome(original.JobID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if got.JobID != original.JobID {
		t.Errorf("JobID: want %q, got %q", original.JobID, got.JobID)
	}
	if got.ReplayerID != original.ReplayerID {
		t.Errorf("ReplayerID: want %q, got %q", original.ReplayerID, got.ReplayerID)
	}
	if len(got.Comparisons) != 1 {
		t.Fatalf("Comparisons: want 1, got %d", len(got.Comparisons))
	}
	if !got.Comparisons[0].Match {
		t.Error("expected Comparisons[0].Match=true after round-trip")
	}
}
