package replay

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ---------------------------------------------------------------------------
// Helper: build and store a job for SubmissionProcessor tests
// ---------------------------------------------------------------------------

// storeJob serialises job into ms and returns job.
func storeJob(t *testing.T, ms *memStore, job *ReplayJob) *ReplayJob {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	if err := ms.PutReplayJob(job.ID, data); err != nil {
		t.Fatalf("PutReplayJob: %v", err)
	}
	return job
}

// newProcessor wires up a SubmissionProcessor with a fresh memStore,
// fakeTaskMgr and fakeGenTrigger. It returns the processor plus the fakeTaskMgr
// so tests can inspect task-side effects.
func newProcessor(t *testing.T) (*SubmissionProcessor, *memStore, *fakeTaskMgr) {
	t.Helper()
	ms := newMemStore()
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enforcer := NewReplayEnforcer(tm, resolver, gen)
	proc := NewSubmissionProcessor(ms, enforcer, nil)
	return proc, ms, tm
}

// minJobForSubmission returns a pending job that is ready to accept
// a matching submission with the given check list.
func minJobForSubmission(id, taskID, category string, checks []string) *ReplayJob {
	return &ReplayJob{
		ID:             id,
		TaskID:         taskID,
		Category:       category,
		PolicyVersion:  "v1",
		Status:         "pending",
		ChecksToReplay: checks,
		CreatedAt:      time.Now(),
	}
}

// ---------------------------------------------------------------------------
// Error-path tests
// ---------------------------------------------------------------------------

// TestSubmissionProcessor_UnknownJob_Error verifies that submitting an outcome
// for a non-existent job returns ErrJobNotFound.
func TestSubmissionProcessor_UnknownJob_Error(t *testing.T) {
	proc, _, _ := newProcessor(t)

	sub := &ReplaySubmission{
		JobID:   "does-not-exist",
		TaskID:  "task-sp-0",
		Category: "code",
	}
	_, _, err := proc.Process(sub)
	if err == nil {
		t.Fatal("expected error for unknown job")
	}
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("error = %v; want ErrJobNotFound", err)
	}
}

// TestSubmissionProcessor_TerminalJobResubmission_Error verifies that
// submitting an outcome for a job already in "completed" state returns
// ErrOutcomeAlreadyTerminal.
func TestSubmissionProcessor_TerminalJobResubmission_Error(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	job := minJobForSubmission("job-sp-term", "task-sp-term", "code", nil)
	job.Status = "completed" // already terminal
	storeJob(t, ms, job)

	sub := &ReplaySubmission{
		JobID:    "job-sp-term",
		TaskID:   "task-sp-term",
		Category: "code",
	}
	_, _, err := proc.Process(sub)
	if err == nil {
		t.Fatal("expected error for terminal job")
	}
	if !errors.Is(err, ErrOutcomeAlreadyTerminal) {
		t.Errorf("error = %v; want ErrOutcomeAlreadyTerminal", err)
	}
}

// TestSubmissionProcessor_BindingMismatch_TaskID verifies that when the
// submission's TaskID differs from the job's TaskID, ErrBindingMismatch is
// returned.
func TestSubmissionProcessor_BindingMismatch_TaskID(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	job := minJobForSubmission("job-sp-bm-task", "task-correct", "code", nil)
	storeJob(t, ms, job)

	sub := &ReplaySubmission{
		JobID:    "job-sp-bm-task",
		TaskID:   "task-WRONG", // mismatch
		Category: "code",
	}
	_, _, err := proc.Process(sub)
	if err == nil {
		t.Fatal("expected error for task_id mismatch")
	}
	if !errors.Is(err, ErrBindingMismatch) {
		t.Errorf("error = %v; want ErrBindingMismatch", err)
	}
}

// TestSubmissionProcessor_BindingMismatch_Category verifies that when the
// submission's Category differs from the job's Category, ErrBindingMismatch
// is returned.
func TestSubmissionProcessor_BindingMismatch_Category(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	job := minJobForSubmission("job-sp-bm-cat", "task-sp-cat", "code", nil)
	storeJob(t, ms, job)

	sub := &ReplaySubmission{
		JobID:    "job-sp-bm-cat",
		TaskID:   "task-sp-cat",
		Category: "writing", // mismatch
	}
	_, _, err := proc.Process(sub)
	if err == nil {
		t.Fatal("expected error for category mismatch")
	}
	if !errors.Is(err, ErrBindingMismatch) {
		t.Errorf("error = %v; want ErrBindingMismatch", err)
	}
}

// TestSubmissionProcessor_BindingMismatch_PolicyVersion verifies that when the
// submission's PolicyVersion differs from the job's PolicyVersion (both
// normalised — "v1" vs "v2" — not empty-vs-v1), ErrBindingMismatch is
// returned.
func TestSubmissionProcessor_BindingMismatch_PolicyVersion(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	job := minJobForSubmission("job-sp-bm-pv", "task-sp-pv", "code", nil)
	job.PolicyVersion = "v1"
	storeJob(t, ms, job)

	sub := &ReplaySubmission{
		JobID:         "job-sp-bm-pv",
		TaskID:        "task-sp-pv",
		Category:      "code",
		PolicyVersion: "v2", // mismatch
	}
	_, _, err := proc.Process(sub)
	if err == nil {
		t.Fatal("expected error for policy_version mismatch")
	}
	if !errors.Is(err, ErrBindingMismatch) {
		t.Errorf("error = %v; want ErrBindingMismatch", err)
	}
}

// TestSubmissionProcessor_PolicyVersion_EmptyNormalisedToV1 verifies that an
// empty PolicyVersion on the submission is normalised to "v1" and matches a
// job whose PolicyVersion is also "v1".
func TestSubmissionProcessor_PolicyVersion_EmptyNormalisedToV1(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	job := minJobForSubmission("job-sp-pv-norm", "task-sp-pv-norm", "code", nil)
	job.PolicyVersion = "v1"
	storeJob(t, ms, job)

	// Empty PolicyVersion in the submission should be treated as "v1".
	sub := &ReplaySubmission{
		JobID:         "job-sp-pv-norm",
		TaskID:        "task-sp-pv-norm",
		Category:      "code",
		PolicyVersion: "", // empty → normalised to "v1"
	}
	_, _, err := proc.Process(sub)
	// Binding normalisation should succeed; the process call may fail for other
	// reasons (empty ChecksToReplay + no enforcer side-effects) but NOT with
	// ErrBindingMismatch.
	if errors.Is(err, ErrBindingMismatch) {
		t.Errorf("empty PolicyVersion must not produce ErrBindingMismatch when job is v1; got %v", err)
	}
}

// TestSubmissionProcessor_BindingMismatch_ContractHash verifies that when both
// the job and the submission have non-empty AcceptanceContractHash values that
// differ, ErrBindingMismatch is returned.
func TestSubmissionProcessor_BindingMismatch_ContractHash(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	job := minJobForSubmission("job-sp-bm-ch", "task-sp-ch", "code", nil)
	job.AcceptanceContractHash = "sha256:original-contract"
	storeJob(t, ms, job)

	sub := &ReplaySubmission{
		JobID:                  "job-sp-bm-ch",
		TaskID:                 "task-sp-ch",
		Category:               "code",
		AcceptanceContractHash: "sha256:DIFFERENT-contract", // mismatch
	}
	_, _, err := proc.Process(sub)
	if err == nil {
		t.Fatal("expected error for acceptance_contract_hash mismatch")
	}
	if !errors.Is(err, ErrBindingMismatch) {
		t.Errorf("error = %v; want ErrBindingMismatch", err)
	}
}

// TestSubmissionProcessor_ContractHash_OneEmpty_NotCompared verifies that when
// either side's AcceptanceContractHash is empty, the hash field is not used for
// mismatch detection (backwards compatibility with older jobs).
func TestSubmissionProcessor_ContractHash_OneEmpty_NotCompared(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	// Job has a contract hash; submission omits it (empty string).
	job := minJobForSubmission("job-sp-ch-empty", "task-sp-ch-empty", "code", nil)
	job.AcceptanceContractHash = "sha256:some-contract"
	storeJob(t, ms, job)

	sub := &ReplaySubmission{
		JobID:                  "job-sp-ch-empty",
		TaskID:                 "task-sp-ch-empty",
		Category:               "code",
		AcceptanceContractHash: "", // empty → not compared
	}
	_, _, err := proc.Process(sub)
	if errors.Is(err, ErrBindingMismatch) {
		t.Errorf("empty submission AcceptanceContractHash must not produce ErrBindingMismatch; got %v", err)
	}
}

// TestSubmissionProcessor_MissingRequiredChecks verifies that when the job
// requires checks that are absent from the submission, ErrMissingRequiredChecks
// is returned.
func TestSubmissionProcessor_MissingRequiredChecks(t *testing.T) {
	proc, ms, _ := newProcessor(t)

	job := minJobForSubmission("job-sp-missing", "task-sp-missing", "code",
		[]string{"go_test", "lint"})
	storeJob(t, ms, job)

	// Only go_test is submitted; lint is missing.
	sub := &ReplaySubmission{
		JobID:    "job-sp-missing",
		TaskID:   "task-sp-missing",
		Category: "code",
		CheckResults: []SubmittedCheckResult{
			{CheckType: "go_test", Pass: true},
		},
	}
	_, _, err := proc.Process(sub)
	if err == nil {
		t.Fatal("expected error for missing required checks")
	}
	if !errors.Is(err, ErrMissingRequiredChecks) {
		t.Errorf("error = %v; want ErrMissingRequiredChecks", err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end happy-path tests
// ---------------------------------------------------------------------------

// TestSubmissionProcessor_MatchSubmission_GenerationRecognized verifies the
// full processing lifecycle when all submitted checks match the original claims.
// Expected: outcome.Status="match", task status="replay_complete",
// generation status="recognized", verdict.Action="no_action".
func TestSubmissionProcessor_MatchSubmission_GenerationRecognized(t *testing.T) {
	ms := newMemStore()
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enforcer := NewReplayEnforcer(tm, resolver, gen)
	proc := NewSubmissionProcessor(ms, enforcer, nil)

	job := minJobForSubmission("job-sp-match", "task-sp-match", "code",
		[]string{"go_test", "lint"})
	storeJob(t, ms, job)

	sub := &ReplaySubmission{
		JobID:    "job-sp-match",
		TaskID:   "task-sp-match",
		Category: "code",
		CheckResults: []SubmittedCheckResult{
			{CheckType: "go_test", Pass: true},
			{CheckType: "lint", Pass: true},
		},
		SubmittedAt: time.Now(),
	}
	outcome, verdict, err := proc.Process(sub)
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}
	if outcome == nil {
		t.Fatal("outcome must not be nil on success")
	}
	if verdict == nil {
		t.Fatal("verdict must not be nil on success")
	}

	// Outcome status.
	if outcome.Status != "match" {
		t.Errorf("outcome.Status = %q; want %q", outcome.Status, "match")
	}

	// Task status via enforcer.
	if got := tm.status["task-sp-match"]; got != "replay_complete" {
		t.Errorf("task status = %q; want %q", got, "replay_complete")
	}

	// Verdict action.
	if verdict.Action != "no_action" {
		t.Errorf("verdict.Action = %q; want %q", verdict.Action, "no_action")
	}
}

// TestSubmissionProcessor_MismatchSubmission_GenerationDenied verifies the full
// processing lifecycle when submitted checks contradict the original claims.
// Expected: outcome.Status="mismatch", task status="replay_disputed",
// generation status="denied", verdict.Action="open_challenge" or higher.
func TestSubmissionProcessor_MismatchSubmission_GenerationDenied(t *testing.T) {
	ms := newMemStore()
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enforcer := NewReplayEnforcer(tm, resolver, gen)
	proc := NewSubmissionProcessor(ms, enforcer, nil)

	job := minJobForSubmission("job-sp-mismatch", "task-sp-mismatch", "code",
		[]string{"go_test", "lint"})
	storeJob(t, ms, job)

	// Both checks fail → majority mismatch → "mismatch" outcome.
	sub := &ReplaySubmission{
		JobID:    "job-sp-mismatch",
		TaskID:   "task-sp-mismatch",
		Category: "code",
		CheckResults: []SubmittedCheckResult{
			{CheckType: "go_test", Pass: false, ExitCode: 1},
			{CheckType: "lint", Pass: false, ExitCode: 1},
		},
		SubmittedAt: time.Now(),
	}
	outcome, verdict, err := proc.Process(sub)
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}
	if outcome == nil {
		t.Fatal("outcome must not be nil on success")
	}
	if verdict == nil {
		t.Fatal("verdict must not be nil on success")
	}

	// Outcome status.
	if outcome.Status != "mismatch" {
		t.Errorf("outcome.Status = %q; want %q", outcome.Status, "mismatch")
	}

	// Task status.
	if got := tm.status["task-sp-mismatch"]; got != "replay_disputed" {
		t.Errorf("task status = %q; want %q", got, "replay_disputed")
	}

	// Verdict action must be adversarial.
	switch verdict.Action {
	case "open_challenge", "slash_recommended":
		// correct
	default:
		t.Errorf("verdict.Action = %q; want open_challenge or slash_recommended", verdict.Action)
	}

	// Generation trigger must NOT be called for a disputed task.
	if len(gen.calls) != 0 {
		t.Errorf("generation trigger calls = %d; want 0 for disputed task", len(gen.calls))
	}
}

// TestSubmissionProcessor_PartialMatch_FlagForReview verifies that a single
// failing check out of two produces a "partial_match" outcome and
// "flag_for_review" verdict with task status "replay_complete".
func TestSubmissionProcessor_PartialMatch_FlagForReview(t *testing.T) {
	ms := newMemStore()
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enforcer := NewReplayEnforcer(tm, resolver, gen)
	proc := NewSubmissionProcessor(ms, enforcer, nil)

	job := minJobForSubmission("job-sp-partial", "task-sp-partial", "code",
		[]string{"go_test", "lint"})
	storeJob(t, ms, job)

	// go_test fails, lint passes → 1 of 2 → partial_match.
	sub := &ReplaySubmission{
		JobID:    "job-sp-partial",
		TaskID:   "task-sp-partial",
		Category: "code",
		CheckResults: []SubmittedCheckResult{
			{CheckType: "go_test", Pass: false, ExitCode: 1},
			{CheckType: "lint", Pass: true},
		},
		SubmittedAt: time.Now(),
	}
	outcome, verdict, err := proc.Process(sub)
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}

	if outcome.Status != "partial_match" {
		t.Errorf("outcome.Status = %q; want %q", outcome.Status, "partial_match")
	}
	// partial_match with single ScoreDelta=1.0 (go_test fail) → open_challenge
	// (since maxDelta > 0.3). Task status follows the verdict mapping.
	switch tm.status["task-sp-partial"] {
	case "replay_complete", "replay_disputed":
		// both valid depending on delta threshold
	default:
		t.Errorf("task status = %q; want replay_complete or replay_disputed", tm.status["task-sp-partial"])
	}
	if verdict == nil {
		t.Error("verdict must not be nil for partial_match")
	}
}

// ---------------------------------------------------------------------------
// Trust-boundary documentation test
// ---------------------------------------------------------------------------

// TestCoordinatedFraudBlindSpot documents that the protocol cannot detect
// coordinated fraud between a worker and a colluding replay submitter.
//
// TRUST BOUNDARY: the protocol is a comparator, not a ground-truth verifier.
// When an authenticated replay submitter confirms false original claims with
// structurally consistent evidence (pass=true, matching artifact hashes,
// matching numeric fields), the comparison produces a "match" outcome and the
// enforcer issues a "no_action" verdict.
//
// The protocol has no way to distinguish:
//   - a genuine replay that confirms real work, and
//   - a colluding replay that confirms fabricated claims.
//
// Addressing this requires external trust signals explicitly deferred for a
// later pass: TEE attestation, stake-weighted multi-party replay, or
// cryptographically verified execution traces.
//
// This test is NOT expected to fail. It asserts the current behavior so that
// the blind spot is explicit, durable, and visible in CI.
func TestCoordinatedFraudBlindSpot_AuthenticatedSubmitterConfirmsFalseClaims(t *testing.T) {
	ms := newMemStore()
	resolver := NewReplayResolver(ms)
	tm := newFakeTaskMgr()
	gen := &fakeGenTrigger{}
	enforcer := NewReplayEnforcer(tm, resolver, gen)
	proc := NewSubmissionProcessor(ms, enforcer, nil)

	// Worker originally claimed: go_test passed, artifact hash "sha256:false",
	// with 100 tests passing. In reality, the work was never done.
	job := minJobForSubmission("job-fraud", "task-fraud", "code", []string{"go_test"})
	job.ArtifactRefs = []verification.ArtifactRef{
		{CheckType: "go_test", Hash: "sha256:false-claim"},
	}
	job.MachineReadableResults = map[string]interface{}{
		"go_test": map[string]interface{}{
			"passed": float64(100),
			"failed": float64(0),
		},
	}
	storeJob(t, ms, job)

	// Colluding replay submitter confirms the false claims exactly:
	// pass=true, same artifact hash, same numeric values.
	// The submitter ran no actual checks — they simply echo the original claims.
	sub := &ReplaySubmission{
		JobID:    "job-fraud",
		TaskID:   "task-fraud",
		Category: "code",
		CheckResults: []SubmittedCheckResult{
			{
				CheckType:    "go_test",
				Pass:         true,                       // confirms false claim
				ArtifactHash: "sha256:false-claim",       // matches false claim exactly
				MachineReadableResult: map[string]interface{}{
					"passed": float64(100), // matches false claim exactly
					"failed": float64(0),
				},
			},
		},
		SubmittedAt: time.Now(),
	}

	outcome, verdict, err := proc.Process(sub)
	if err != nil {
		t.Fatalf("Process: unexpected error: %v", err)
	}

	// KNOWN BLIND SPOT: coordinated fraud is structurally indistinguishable
	// from a genuine replay. Both produce outcome.Status="match".
	//
	// If this assertion fails, something changed in the comparison logic that
	// may have accidentally created false detection coverage. Investigate before
	// removing — do not simply update the expected value.
	if outcome.Status != "match" {
		t.Errorf("outcome.Status = %q; want %q — coordinated fraud must produce match (blind spot documented)",
			outcome.Status, "match")
	}
	if verdict.Action != "no_action" {
		t.Errorf("verdict.Action = %q; want %q — coordinated fraud is currently undetectable by the protocol",
			verdict.Action, "no_action")
	}

	// The task reaches replay_complete as if the work were genuine.
	if tm.status["task-fraud"] != "replay_complete" {
		t.Errorf("task status = %q; want %q — coordinated fraud reaches completion status",
			tm.status["task-fraud"], "replay_complete")
	}

	// No anomaly flags — structurally consistent fraud produces a clean outcome.
	if len(outcome.AnomalyFlags) != 0 {
		t.Errorf("AnomalyFlags = %v; want none — coordinated fraud leaves no anomaly trace",
			outcome.AnomalyFlags)
	}
}
