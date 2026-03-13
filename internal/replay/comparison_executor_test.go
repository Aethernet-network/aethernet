package replay

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeJob builds a minimal ReplayJob for comparison tests.
func makeJob(id, taskID string, checks []string, artifactRefs []verification.ArtifactRef, mrr map[string]interface{}) *ReplayJob {
	return &ReplayJob{
		ID:             id,
		TaskID:         taskID,
		Category:       "code",
		PolicyVersion:  "v1",
		Status:         "pending",
		ChecksToReplay: checks,
		ArtifactRefs:   artifactRefs,
		MachineReadableResults: mrr,
	}
}

// makeSub builds a minimal ReplaySubmission for comparison tests.
func makeSub(jobID, taskID string, results []SubmittedCheckResult) *ReplaySubmission {
	return &ReplaySubmission{
		JobID:       jobID,
		TaskID:      taskID,
		Category:    "code",
		PolicyVersion: "v1",
		SubmitterID: "replayer-test",
		CheckResults: results,
		SubmittedAt: time.Now(),
	}
}

// ---------------------------------------------------------------------------
// ComparisonExecutor tests
// ---------------------------------------------------------------------------

// TestComparisonExecutor_ExactMatch verifies that when all checks pass and
// all artifact hashes match, the outcome status is "match" and all per-check
// comparisons have Match=true.
func TestComparisonExecutor_ExactMatch(t *testing.T) {
	job := makeJob("job-cm-1", "task-cm-1",
		[]string{"go_test", "lint"},
		[]verification.ArtifactRef{
			{CheckType: "go_test", Hash: "sha256:abc"},
			{CheckType: "lint", Hash: "sha256:def"},
		}, nil)

	sub := makeSub("job-cm-1", "task-cm-1", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: true, ArtifactHash: "sha256:abc"},
		{CheckType: "lint", Pass: true, ArtifactHash: "sha256:def"},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "match" {
		t.Errorf("Status = %q; want %q", outcome.Status, "match")
	}
	if len(outcome.Comparisons) != 2 {
		t.Fatalf("Comparisons len = %d; want 2", len(outcome.Comparisons))
	}
	for _, c := range outcome.Comparisons {
		if !c.Match {
			t.Errorf("check %q: Match = false; want true", c.CheckType)
		}
		if c.ScoreDelta != 0.0 {
			t.Errorf("check %q: ScoreDelta = %.2f; want 0.0", c.CheckType, c.ScoreDelta)
		}
	}
	if outcome.ReplayerID != "replayer-test" {
		t.Errorf("ReplayerID = %q; want %q", outcome.ReplayerID, "replayer-test")
	}
	if len(outcome.AnomalyFlags) != 0 {
		t.Errorf("AnomalyFlags = %v; want none", outcome.AnomalyFlags)
	}
}

// TestComparisonExecutor_PassFail_Mismatch verifies that a single check
// reporting pass=false produces Status="mismatch" with ScoreDelta=1.0 and
// a descriptive Detail string.
func TestComparisonExecutor_PassFail_Mismatch(t *testing.T) {
	job := makeJob("job-cm-2", "task-cm-2",
		[]string{"go_test"}, nil, nil)

	sub := makeSub("job-cm-2", "task-cm-2", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: false, ExitCode: 1},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "mismatch" {
		t.Errorf("Status = %q; want %q", outcome.Status, "mismatch")
	}
	if !outcome.HasMismatch() {
		t.Error("HasMismatch = false; want true")
	}
	if len(outcome.Comparisons) != 1 {
		t.Fatalf("Comparisons len = %d; want 1", len(outcome.Comparisons))
	}
	c := outcome.Comparisons[0]
	if c.Match {
		t.Error("Comparisons[0].Match = true; want false")
	}
	if c.ScoreDelta != 1.0 {
		t.Errorf("ScoreDelta = %.2f; want 1.0", c.ScoreDelta)
	}
	if c.Detail == "" {
		t.Error("Detail must not be empty for a pass/fail mismatch")
	}
}

// TestComparisonExecutor_OneOfThreeChecksFails_PartialMatch verifies that 1
// of 3 checks failing produces Status="partial_match".
func TestComparisonExecutor_OneOfThreeChecksFails_PartialMatch(t *testing.T) {
	job := makeJob("job-cm-3", "task-cm-3",
		[]string{"go_test", "lint", "vet"}, nil, nil)

	sub := makeSub("job-cm-3", "task-cm-3", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: false, ExitCode: 2}, // fail
		{CheckType: "lint", Pass: true},
		{CheckType: "vet", Pass: true},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "partial_match" {
		t.Errorf("Status = %q; want %q", outcome.Status, "partial_match")
	}
	if outcome.MismatchCount() != 1 {
		t.Errorf("MismatchCount = %d; want 1", outcome.MismatchCount())
	}
}

// TestComparisonExecutor_MajorityFail_Mismatch verifies that 2 of 3 checks
// failing (majority) produces Status="mismatch".
func TestComparisonExecutor_MajorityFail_Mismatch(t *testing.T) {
	job := makeJob("job-cm-4", "task-cm-4",
		[]string{"go_test", "lint", "vet"}, nil, nil)

	sub := makeSub("job-cm-4", "task-cm-4", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: false, ExitCode: 1},
		{CheckType: "lint", Pass: false, ExitCode: 1},
		{CheckType: "vet", Pass: true},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "mismatch" {
		t.Errorf("Status = %q; want %q", outcome.Status, "mismatch")
	}
	if outcome.MismatchCount() != 2 {
		t.Errorf("MismatchCount = %d; want 2", outcome.MismatchCount())
	}
}

// TestComparisonExecutor_MissingRequiredCheck_Error verifies that a submission
// omitting a check required by the job produces Status="error" with a note
// listing the missing check(s).
func TestComparisonExecutor_MissingRequiredCheck_Error(t *testing.T) {
	job := makeJob("job-cm-5", "task-cm-5",
		[]string{"go_test", "lint"}, nil, nil)

	// submission only has go_test; lint is missing
	sub := makeSub("job-cm-5", "task-cm-5", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: true},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "error" {
		t.Errorf("Status = %q; want %q", outcome.Status, "error")
	}
	if outcome.Notes == "" {
		t.Error("Notes must not be empty when required checks are missing")
	}
}

// TestComparisonExecutor_ArtifactHashMismatch verifies that pass=true but
// differing artifact hashes produce Match=false with ScoreDelta=0.5 and the
// "artifact_hash_divergence" anomaly flag.
func TestComparisonExecutor_ArtifactHashMismatch(t *testing.T) {
	job := makeJob("job-cm-6", "task-cm-6",
		[]string{"go_test"},
		[]verification.ArtifactRef{
			{CheckType: "go_test", Hash: "sha256:original"},
		}, nil)

	sub := makeSub("job-cm-6", "task-cm-6", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: true, ArtifactHash: "sha256:different"},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "mismatch" {
		t.Errorf("Status = %q; want %q", outcome.Status, "mismatch")
	}
	if len(outcome.Comparisons) != 1 {
		t.Fatalf("Comparisons len = %d; want 1", len(outcome.Comparisons))
	}
	c := outcome.Comparisons[0]
	if c.Match {
		t.Error("Match = true; want false for hash mismatch")
	}
	if c.ScoreDelta != 0.5 {
		t.Errorf("ScoreDelta = %.2f; want 0.5 for artifact hash mismatch", c.ScoreDelta)
	}

	// Artifact hash divergence while pass=true is an anomaly.
	found := false
	for _, f := range outcome.AnomalyFlags {
		if f == "artifact_hash_divergence" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("AnomalyFlags %v; want to contain %q", outcome.AnomalyFlags, "artifact_hash_divergence")
	}
}

// TestComparisonExecutor_ArtifactHashMatch_NoAnomalies verifies that matching
// artifact hashes produce no anomaly flags.
func TestComparisonExecutor_ArtifactHashMatch_NoAnomalies(t *testing.T) {
	job := makeJob("job-cm-7", "task-cm-7",
		[]string{"go_test"},
		[]verification.ArtifactRef{
			{CheckType: "go_test", Hash: "sha256:same"},
		}, nil)

	sub := makeSub("job-cm-7", "task-cm-7", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: true, ArtifactHash: "sha256:same"},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "match" {
		t.Errorf("Status = %q; want %q", outcome.Status, "match")
	}
	if len(outcome.AnomalyFlags) != 0 {
		t.Errorf("AnomalyFlags = %v; want none for matching hashes", outcome.AnomalyFlags)
	}
}

// TestComparisonExecutor_MachineReadableResults_NumericDivergenceAnomaly
// verifies that when the original MachineReadableResults map contains numeric
// fields and the replay-submitted values differ by >15%, a "numeric_divergence"
// anomaly flag is added (but the check is NOT failed — conservative comparison).
func TestComparisonExecutor_MachineReadableResults_NumericDivergenceAnomaly(t *testing.T) {
	// Original: go_test passed with 100 tests, 0 failures.
	mrr := map[string]interface{}{
		"go_test": map[string]interface{}{
			"passed":  float64(100),
			"failed":  float64(0),
		},
	}
	job := makeJob("job-cm-8", "task-cm-8",
		[]string{"go_test"}, nil, mrr)

	// Replay reports pass=true but only 60 tests passed (40% fewer — >15% delta).
	sub := makeSub("job-cm-8", "task-cm-8", []SubmittedCheckResult{
		{
			CheckType: "go_test",
			Pass:      true,
			MachineReadableResult: map[string]interface{}{
				"passed": float64(60),
				"failed": float64(0),
			},
		},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	// Check is not failed (conservative comparison) — status should be "match".
	if outcome.Status != "match" {
		t.Errorf("Status = %q; want %q (numeric divergence is anomaly, not mismatch)", outcome.Status, "match")
	}
	// Anomaly flag must be set.
	found := false
	for _, f := range outcome.AnomalyFlags {
		if f == "numeric_divergence" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("AnomalyFlags %v; want to contain %q for >15%% numeric delta", outcome.AnomalyFlags, "numeric_divergence")
	}
}

// TestComparisonExecutor_MachineReadableResults_SmallDelta_NoAnomaly verifies
// that a small numeric delta (within 15%) does not produce an anomaly flag.
func TestComparisonExecutor_MachineReadableResults_SmallDelta_NoAnomaly(t *testing.T) {
	mrr := map[string]interface{}{
		"go_test": map[string]interface{}{
			"passed": float64(100),
		},
	}
	job := makeJob("job-cm-9", "task-cm-9",
		[]string{"go_test"}, nil, mrr)

	sub := makeSub("job-cm-9", "task-cm-9", []SubmittedCheckResult{
		{
			CheckType: "go_test",
			Pass:      true,
			MachineReadableResult: map[string]interface{}{
				"passed": float64(95), // 5% delta — below 15% threshold
			},
		},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "match" {
		t.Errorf("Status = %q; want %q", outcome.Status, "match")
	}
	for _, f := range outcome.AnomalyFlags {
		if f == "numeric_divergence" {
			t.Errorf("unexpected numeric_divergence anomaly for small delta (5%%)")
		}
	}
}

// TestComparisonExecutor_MachineReadableResults_PersistedOnJob verifies that
// NewReplayJob correctly copies MachineReadableResults from ReplayRequirements
// and that the ComparisonExecutor can use them for numeric comparison.
func TestComparisonExecutor_MachineReadableResults_PersistedOnJob(t *testing.T) {
	mrr := map[string]interface{}{
		"lint": map[string]interface{}{
			"warnings": float64(0),
			"errors":   float64(0),
		},
	}
	reqs := &verification.ReplayRequirements{
		AcceptanceContractHash:  "sha256:contract",
		RequiredChecks:          []string{"lint"},
		MachineReadableResults:  mrr,
	}
	job := NewReplayJob("task-persist", "sha256:pkt", "writing", "v1", "agent-1", "spot", reqs, time.Now())

	// Verify field was copied.
	if job.MachineReadableResults == nil {
		t.Fatal("MachineReadableResults was not copied from ReplayRequirements")
	}
	lintVal, ok := job.MachineReadableResults["lint"]
	if !ok {
		t.Fatal("MachineReadableResults missing 'lint' key")
	}
	lintMap, ok := lintVal.(map[string]interface{})
	if !ok {
		t.Fatalf("MachineReadableResults['lint'] type = %T; want map[string]interface{}", lintVal)
	}
	if lintMap["errors"] != float64(0) {
		t.Errorf("errors = %v; want 0", lintMap["errors"])
	}

	// Run comparison with a large numeric delta → should produce numeric_divergence anomaly.
	sub := &ReplaySubmission{
		JobID:       job.ID,
		TaskID:      job.TaskID,
		Category:    job.Category,
		PolicyVersion: "v1",
		SubmitterID: "test-replayer",
		CheckResults: []SubmittedCheckResult{
			{
				CheckType: "lint",
				Pass:      true,
				MachineReadableResult: map[string]interface{}{
					"warnings": float64(50), // was 0 → large delta
					"errors":   float64(0),
				},
			},
		},
	}

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	found := false
	for _, f := range outcome.AnomalyFlags {
		if f == "numeric_divergence" {
			found = true
		}
	}
	if !found {
		t.Errorf("AnomalyFlags %v; want numeric_divergence for large warning delta", outcome.AnomalyFlags)
	}
}

// TestComparisonExecutor_EmptyChecksToReplay_Match verifies that a job with
// no required checks and an empty submission produces Status="match".
func TestComparisonExecutor_EmptyChecksToReplay_Match(t *testing.T) {
	job := makeJob("job-cm-10", "task-cm-10", nil, nil, nil)
	sub := makeSub("job-cm-10", "task-cm-10", nil)

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "match" {
		t.Errorf("Status = %q; want %q for empty check list", outcome.Status, "match")
	}
}

// TestComparisonExecutor_NoOriginalHash_HashNotCompared verifies that when
// the job has no original artifact hash for a check, the artifact hash field
// in the submission is not used for mismatch detection (benefit of doubt).
func TestComparisonExecutor_NoOriginalHash_HashNotCompared(t *testing.T) {
	// Job has no ArtifactRefs.
	job := makeJob("job-cm-11", "task-cm-11",
		[]string{"go_test"}, nil, nil)

	sub := makeSub("job-cm-11", "task-cm-11", []SubmittedCheckResult{
		{CheckType: "go_test", Pass: true, ArtifactHash: "sha256:anyhash"},
	})

	ex := &ComparisonExecutor{}
	outcome := ex.Compare(job, sub)

	if outcome.Status != "match" {
		t.Errorf("Status = %q; want %q when no original hash to compare", outcome.Status, "match")
	}
}
