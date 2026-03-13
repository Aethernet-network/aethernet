package replay

import (
	"context"
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// TestInspectionExecutor_NoMaterial_ErrorStatus verifies that a job with no
// replay material produces a Status="error" outcome with a "not replayable"
// note listing missing fields.
func TestInspectionExecutor_NoMaterial_ErrorStatus(t *testing.T) {
	ex := NewInspectionExecutor()
	job := &ReplayJob{
		ID:       "job-ex-1",
		TaskID:   "task-ex-1",
		Category: "code",
		Status:   "pending",
	}

	outcome, err := ex.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome == nil {
		t.Fatal("outcome must not be nil")
	}
	if outcome.Status != "error" {
		t.Errorf("outcome.Status = %q; want %q", outcome.Status, "error")
	}
	if outcome.JobID != job.ID {
		t.Errorf("outcome.JobID = %q; want %q", outcome.JobID, job.ID)
	}
	if outcome.TaskID != job.TaskID {
		t.Errorf("outcome.TaskID = %q; want %q", outcome.TaskID, job.TaskID)
	}
	if !strings.HasPrefix(outcome.Notes, "not replayable") {
		t.Errorf("outcome.Notes = %q; want prefix %q", outcome.Notes, "not replayable")
	}
	if outcome.ReplayerID != "inspection-executor" {
		t.Errorf("outcome.ReplayerID = %q; want %q", outcome.ReplayerID, "inspection-executor")
	}
	if outcome.ReplayedAt.IsZero() {
		t.Error("outcome.ReplayedAt must not be zero")
	}
}

// TestInspectionExecutor_FullMaterial_ExecutionPending verifies that a
// non-code task with all structural replay material (AcceptanceContractHash +
// RequiredChecks) produces a Status="error" outcome with an "execution-pending"
// note (material is sufficient for replay, but no execution engine is available).
//
// Note: code/v1 tasks require MachineReadableResults which is not stored on
// ReplayJob, so they always report missing fields. Non-code categories use
// the structural assessment that only requires AcceptanceContractHash + checks.
func TestInspectionExecutor_FullMaterial_ExecutionPending(t *testing.T) {
	ex := NewInspectionExecutor()
	job := &ReplayJob{
		ID:                     "job-ex-2",
		TaskID:                 "task-ex-2",
		Category:               "writing",
		PolicyVersion:          "v1",
		Status:                 "pending",
		AcceptanceContractHash: "sha256:contract",
		ChecksToReplay:         []string{"content_check", "quality_check"},
	}

	outcome, err := ex.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Status != "error" {
		t.Errorf("outcome.Status = %q; want %q", outcome.Status, "error")
	}
	if !strings.Contains(outcome.Notes, "execution-pending") {
		t.Errorf("outcome.Notes = %q; want to contain %q", outcome.Notes, "execution-pending")
	}
}

// TestInspectionExecutor_NonCodeCategory_StructuralMatch verifies that a
// non-code task with the minimal structural requirements (acceptance contract
// hash + required checks) produces an "execution-pending" note.
func TestInspectionExecutor_NonCodeCategory_StructuralMatch(t *testing.T) {
	ex := NewInspectionExecutor()
	job := &ReplayJob{
		ID:                     "job-ex-3",
		TaskID:                 "task-ex-3",
		Category:               "research",
		Status:                 "pending",
		AcceptanceContractHash: "sha256:contract",
		ChecksToReplay:         []string{"content_check"},
	}

	outcome, err := ex.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Status != "error" {
		t.Errorf("outcome.Status = %q; want %q", outcome.Status, "error")
	}
	if !strings.Contains(outcome.Notes, "execution-pending") {
		t.Errorf("outcome.Notes = %q; want to contain %q", outcome.Notes, "execution-pending")
	}
}

// TestJobToReplayRequirements_CopiesAllFields verifies that jobToReplayRequirements
// correctly copies all stored fields from a ReplayJob.
func TestJobToReplayRequirements_CopiesAllFields(t *testing.T) {
	job := &ReplayJob{
		SourceSnapshotHash:      "src-hash",
		AcceptanceContractHash:  "contract-hash",
		ChecksToReplay:          []string{"check1", "check2"},
		EnvironmentManifestHash: "env-hash",
		ToolchainManifestHash:   "toolchain-hash",
		CommandSpecs: []verification.CommandSpec{
			{CheckType: "go_test"},
		},
		ArtifactRefs: []verification.ArtifactRef{
			{CheckType: "go_test", Hash: "artifact-hash"},
		},
	}

	reqs := jobToReplayRequirements(job)
	if reqs.SourceSnapshotHash != job.SourceSnapshotHash {
		t.Errorf("SourceSnapshotHash = %q; want %q", reqs.SourceSnapshotHash, job.SourceSnapshotHash)
	}
	if reqs.AcceptanceContractHash != job.AcceptanceContractHash {
		t.Errorf("AcceptanceContractHash = %q; want %q", reqs.AcceptanceContractHash, job.AcceptanceContractHash)
	}
	if len(reqs.RequiredChecks) != 2 {
		t.Errorf("RequiredChecks len = %d; want 2", len(reqs.RequiredChecks))
	}
	if reqs.EnvironmentManifestHash != job.EnvironmentManifestHash {
		t.Errorf("EnvironmentManifestHash = %q; want %q", reqs.EnvironmentManifestHash, job.EnvironmentManifestHash)
	}
	if reqs.ToolchainManifestHash != job.ToolchainManifestHash {
		t.Errorf("ToolchainManifestHash = %q; want %q", reqs.ToolchainManifestHash, job.ToolchainManifestHash)
	}
	if len(reqs.CommandSpecs) != 1 {
		t.Errorf("CommandSpecs len = %d; want 1", len(reqs.CommandSpecs))
	}
	if len(reqs.ArtifactRefs) != 1 {
		t.Errorf("ArtifactRefs len = %d; want 1", len(reqs.ArtifactRefs))
	}
}
