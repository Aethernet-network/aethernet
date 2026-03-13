package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/platform"
	"github.com/Aethernet-network/aethernet/internal/replay"
	"github.com/Aethernet-network/aethernet/internal/store"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// newReplayServer sets up a minimal API server with a ReplayEnforcer wired in.
// It returns the server, the httptest.Server, the task manager, the replay store,
// and the replay resolver so tests can pre-populate jobs.
func newReplayServer(t *testing.T) (*api.Server, *httptest.Server, *tasks.TaskManager, *store.Store, *replay.ReplayResolver) {
	t.Helper()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	d := dag.New()
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketEcosystem), genesis.EcosystemAllocation); err != nil {
		t.Fatalf("seed ecosystem: %v", err)
	}
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)
	sm := ledger.NewSupplyManager(tl, gl)

	tm := tasks.NewTaskManager()
	escrowMgr := escrow.New(tl)

	st, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resolver := replay.NewReplayResolver(st)
	// genTrigger is nil: generation ledger recording is not tested here.
	enforcer := replay.NewReplayEnforcer(tm, resolver, nil)

	srv := api.NewServer("", d, tl, gl, reg, eng, sm, nil, kp)
	srv.SetTaskManager(tm, escrowMgr)
	srv.SetReplayEnforcer(enforcer)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	return srv, ts, tm, st, resolver
}

// postReplayOutcome sends a POST /v1/replay/outcome request with the given
// outcome and returns the HTTP response.
func postReplayOutcome(t *testing.T, ts *httptest.Server, outcome *replay.ReplayOutcome) *http.Response {
	t.Helper()
	body, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	resp, err := http.Post(ts.URL+"/v1/replay/outcome", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/replay/outcome: %v", err)
	}
	return resp
}

// storeReplayJob persists a minimal pending ReplayJob to st so outcome submission
// can succeed.
func storeReplayJob(t *testing.T, st *store.Store, jobID, taskID string) *replay.ReplayJob {
	t.Helper()
	job := &replay.ReplayJob{
		ID:        jobID,
		TaskID:    taskID,
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	if err := st.PutReplayJob(jobID, data); err != nil {
		t.Fatalf("PutReplayJob: %v", err)
	}
	return job
}

// --- Tests ----------------------------------------------------------------

// TestHandleReplayOutcome_NoEnforcer verifies that the endpoint returns 501
// when no ReplayEnforcer has been wired.
func TestHandleReplayOutcome_NoEnforcer(t *testing.T) {
	setup := newTestSetup(t)
	// setup.srv has NO replayEnforcer wired.

	outcome := &replay.ReplayOutcome{
		JobID:  "job-x",
		TaskID: "task-x",
		Status: "match",
	}
	resp := postReplayOutcome(t, setup.ts, outcome)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d; want 501", resp.StatusCode)
	}
}

// TestHandleReplayOutcome_InvalidJSON verifies that a malformed body returns 400.
func TestHandleReplayOutcome_InvalidJSON(t *testing.T) {
	_, ts, _, _, _ := newReplayServer(t)

	resp, err := http.Post(ts.URL+"/v1/replay/outcome", "application/json",
		bytes.NewBufferString("not-json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// TestHandleReplayOutcome_MissingJobID verifies that an outcome with empty
// job_id returns 400.
func TestHandleReplayOutcome_MissingJobID(t *testing.T) {
	_, ts, _, _, _ := newReplayServer(t)

	outcome := &replay.ReplayOutcome{
		JobID:  "", // empty
		TaskID: "task-y",
		Status: "match",
	}
	resp := postReplayOutcome(t, ts, outcome)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// TestHandleReplayOutcome_UnknownJob verifies that an outcome referencing a
// non-existent job returns 400 (ErrJobNotFound propagated from validator).
func TestHandleReplayOutcome_UnknownJob(t *testing.T) {
	_, ts, _, _, _ := newReplayServer(t)

	outcome := &replay.ReplayOutcome{
		JobID:  "this-job-does-not-exist",
		TaskID: "task-z",
		Status: "match",
	}
	resp := postReplayOutcome(t, ts, outcome)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (unknown job)", resp.StatusCode)
	}
}

// TestHandleReplayOutcome_Success verifies that a valid outcome for an existing
// job returns 200 and a ReplayVerdict in the body.
func TestHandleReplayOutcome_Success(t *testing.T) {
	_, ts, tm, st, _ := newReplayServer(t)

	// Create a task so the enforcer can look up claimer details.
	task, err := tm.PostTask("poster-1", "Run tests", "go test ./...", "code", 100_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	// Pre-populate a pending ReplayJob referencing this task.
	job := storeReplayJob(t, st, "job-success-1", task.ID)

	outcome := &replay.ReplayOutcome{
		JobID:      job.ID,
		TaskID:     task.ID,
		Status:     "match",
		ReplayedAt: time.Now(),
		ReplayerID: "test-replayer",
		Comparisons: []replay.CheckComparison{
			{CheckType: "go_test", Match: true},
		},
	}
	resp := postReplayOutcome(t, ts, outcome)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}

	var verdict verification.ReplayVerdict
	if err := json.NewDecoder(resp.Body).Decode(&verdict); err != nil {
		t.Fatalf("decode verdict: %v", err)
	}
	if verdict.Action == "" {
		t.Error("verdict.Action must be non-empty")
	}

	// After processing, the task's ReplayStatus should be updated.
	updated, err := tm.Get(task.ID)
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if updated.ReplayStatus != "replay_complete" {
		t.Errorf("ReplayStatus = %q; want %q", updated.ReplayStatus, "replay_complete")
	}
}

// newReplayServerWithAuth sets up a server that has both a ReplayEnforcer and
// a platform.KeyManager wired. requireAuth defaults to true, so requests must
// carry a valid X-API-Key to reach the enforcer. Returns the server, the
// httptest.Server, the store, and the valid API key string.
func newReplayServerWithAuth(t *testing.T) (*api.Server, *httptest.Server, *store.Store, string) {
	t.Helper()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	d := dag.New()
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketEcosystem), genesis.EcosystemAllocation); err != nil {
		t.Fatalf("seed ecosystem: %v", err)
	}
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)
	sm := ledger.NewSupplyManager(tl, gl)

	tm := tasks.NewTaskManager()
	escrowMgr := escrow.New(tl)

	st, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resolver := replay.NewReplayResolver(st)
	enforcer := replay.NewReplayEnforcer(tm, resolver, nil)

	km := platform.NewKeyManager()
	apiKey := km.GenerateKey("test-app", "test@example.com", platform.TierDeveloper)

	srv := api.NewServer("", d, tl, gl, reg, eng, sm, nil, kp)
	srv.SetTaskManager(tm, escrowMgr)
	srv.SetReplayEnforcer(enforcer)
	srv.SetPlatformKeys(km)
	// requireAuth=true by default; do NOT call SetRequireAuth(false)

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts, st, apiKey.Key
}

// postReplayOutcomeWithKey sends a POST /v1/replay/outcome request optionally
// carrying an X-API-Key header.
func postReplayOutcomeWithKey(t *testing.T, ts *httptest.Server, outcome *replay.ReplayOutcome, apiKey string) *http.Response {
	t.Helper()
	body, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/replay/outcome", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/replay/outcome: %v", err)
	}
	return resp
}

// TestHandleReplayOutcome_RequiresAuth verifies that when requireAuth=true and
// platformKeys are wired, an unauthenticated request returns 401.
func TestHandleReplayOutcome_RequiresAuth(t *testing.T) {
	_, ts, _, _ := newReplayServerWithAuth(t)

	outcome := &replay.ReplayOutcome{
		JobID:  "job-auth-1",
		TaskID: "task-auth-1",
		Status: "match",
	}
	// No X-API-Key header → 401.
	resp := postReplayOutcomeWithKey(t, ts, outcome, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 (unauthenticated)", resp.StatusCode)
	}
}

// TestHandleReplayOutcome_AuthenticatedPasses verifies that a request with a
// valid X-API-Key passes the auth gate (and then fails for business reasons
// since the job doesn't exist — returning 400, not 401).
func TestHandleReplayOutcome_AuthenticatedPasses(t *testing.T) {
	_, ts, _, validKey := newReplayServerWithAuth(t)

	outcome := &replay.ReplayOutcome{
		JobID:  "job-auth-2",
		TaskID: "task-auth-2",
		Status: "match",
	}
	// Valid key → passes auth gate → fails on unknown job → 400 (not 401).
	resp := postReplayOutcomeWithKey(t, ts, outcome, validKey)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("status = 401; request with valid key must not be rejected by auth gate")
	}
	// Expect 400: job doesn't exist in store.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (unknown job, valid auth)", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// POST /v1/replay/submit helpers and tests
// ---------------------------------------------------------------------------

// newReplayServerWithSubmission extends newReplayServer by also wiring a
// SubmissionProcessor. It returns the server, httptest.Server, store, and
// the task manager so tests can pre-populate tasks the enforcer will look up.
func newReplayServerWithSubmission(t *testing.T) (*api.Server, *httptest.Server, *store.Store, *tasks.TaskManager) {
	t.Helper()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	d := dag.New()
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent(crypto.AgentID(genesis.BucketEcosystem), genesis.EcosystemAllocation); err != nil {
		t.Fatalf("seed ecosystem: %v", err)
	}
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)
	sm := ledger.NewSupplyManager(tl, gl)

	tm := tasks.NewTaskManager()
	escrowMgr := escrow.New(tl)

	st, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resolver := replay.NewReplayResolver(st)
	enforcer := replay.NewReplayEnforcer(tm, resolver, nil)
	proc := replay.NewSubmissionProcessor(st, enforcer, nil)

	srv := api.NewServer("", d, tl, gl, reg, eng, sm, nil, kp)
	srv.SetTaskManager(tm, escrowMgr)
	srv.SetReplayEnforcer(enforcer)
	srv.SetSubmissionProcessor(proc)
	srv.SetRequireAuth(false)

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts, st, tm
}

// storeReplayJobFull stores a pending job with Category, ChecksToReplay, and
// AcceptanceContractHash populated — suitable for submit-path tests.
func storeReplayJobFull(t *testing.T, st *store.Store, jobID, taskID, category string, checks []string, contractHash string) *replay.ReplayJob {
	t.Helper()
	job := &replay.ReplayJob{
		ID:                     jobID,
		TaskID:                 taskID,
		Category:               category,
		PolicyVersion:          "v1",
		Status:                 "pending",
		ChecksToReplay:         checks,
		AcceptanceContractHash: contractHash,
		CreatedAt:              time.Now(),
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	if err := st.PutReplayJob(jobID, data); err != nil {
		t.Fatalf("PutReplayJob: %v", err)
	}
	return job
}

// postReplaySubmit sends POST /v1/replay/submit with the given submission.
func postReplaySubmit(t *testing.T, ts *httptest.Server, sub *replay.ReplaySubmission) *http.Response {
	t.Helper()
	body, err := json.Marshal(sub)
	if err != nil {
		t.Fatalf("marshal submission: %v", err)
	}
	resp, err := http.Post(ts.URL+"/v1/replay/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/replay/submit: %v", err)
	}
	return resp
}

// TestHandleReplaySubmit_NoProcessor_501 verifies that when no SubmissionProcessor
// is wired the endpoint returns 501.
func TestHandleReplaySubmit_NoProcessor_501(t *testing.T) {
	setup := newTestSetup(t) // no SubmissionProcessor wired
	sub := &replay.ReplaySubmission{
		JobID:  "job-sub-501",
		TaskID: "task-sub-501",
	}
	resp := postReplaySubmit(t, setup.ts, sub)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d; want 501", resp.StatusCode)
	}
}

// TestHandleReplaySubmit_InvalidJSON_400 verifies that a malformed body returns 400.
func TestHandleReplaySubmit_InvalidJSON_400(t *testing.T) {
	_, ts, _, _ := newReplayServerWithSubmission(t)
	resp, err := http.Post(ts.URL+"/v1/replay/submit", "application/json",
		bytes.NewBufferString("not-valid-json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// TestHandleReplaySubmit_MissingJobID_400 verifies that a submission with an
// empty job_id returns 400.
func TestHandleReplaySubmit_MissingJobID_400(t *testing.T) {
	_, ts, _, _ := newReplayServerWithSubmission(t)
	sub := &replay.ReplaySubmission{
		JobID:  "", // empty
		TaskID: "task-sub-missing",
	}
	resp := postReplaySubmit(t, ts, sub)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// TestHandleReplaySubmit_MissingTaskID_400 verifies that a submission with an
// empty task_id returns 400.
func TestHandleReplaySubmit_MissingTaskID_400(t *testing.T) {
	_, ts, _, _ := newReplayServerWithSubmission(t)
	sub := &replay.ReplaySubmission{
		JobID:  "job-sub-notask",
		TaskID: "", // empty
	}
	resp := postReplaySubmit(t, ts, sub)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// TestHandleReplaySubmit_UnknownJob_400 verifies that a submission referencing
// a non-existent job returns 400.
func TestHandleReplaySubmit_UnknownJob_400(t *testing.T) {
	_, ts, _, _ := newReplayServerWithSubmission(t)
	sub := &replay.ReplaySubmission{
		JobID:    "job-sub-ghost",
		TaskID:   "task-sub-ghost",
		Category: "code",
	}
	resp := postReplaySubmit(t, ts, sub)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (unknown job)", resp.StatusCode)
	}
}

// TestHandleReplaySubmit_TerminalJob_400 verifies that re-submitting for a job
// in a terminal state returns 400.
func TestHandleReplaySubmit_TerminalJob_400(t *testing.T) {
	_, ts, st, _ := newReplayServerWithSubmission(t)

	// Store a completed job.
	job := storeReplayJobFull(t, st, "job-sub-term", "task-sub-term", "code", nil, "")
	// Mark it as completed.
	job.Status = "completed"
	data, _ := json.Marshal(job)
	_ = st.PutReplayJob(job.ID, data)

	sub := &replay.ReplaySubmission{
		JobID:    "job-sub-term",
		TaskID:   "task-sub-term",
		Category: "code",
	}
	resp := postReplaySubmit(t, ts, sub)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (terminal job)", resp.StatusCode)
	}
}

// TestHandleReplaySubmit_ValidMatch_200 verifies the happy path: a valid
// submission that produces a "match" outcome returns 200 with an outcome+verdict
// body. No checks are required, so an empty submission matches trivially.
func TestHandleReplaySubmit_ValidMatch_200(t *testing.T) {
	_, ts, st, tm := newReplayServerWithSubmission(t)

	// Pre-create the task so the enforcer can call SetReplayStatus.
	task, err := tm.PostTask("poster-sub-1", "Run replay match", "check outputs", "code", 100_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	// Job with no required checks — an empty submission matches trivially.
	storeReplayJobFull(t, st, "job-sub-match", task.ID, "code", nil, "")

	sub := &replay.ReplaySubmission{
		JobID:       "job-sub-match",
		TaskID:      task.ID,
		Category:    "code",
		SubmitterID: "test-replayer",
		SubmittedAt: time.Now(),
	}
	resp := postReplaySubmit(t, ts, sub)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}

	var result struct {
		Outcome *replay.ReplayOutcome        `json:"outcome"`
		Verdict *verification.ReplayVerdict  `json:"verdict"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Outcome == nil {
		t.Fatal("outcome must not be nil in 200 response")
	}
	if result.Verdict == nil {
		t.Fatal("verdict must not be nil in 200 response")
	}
	if result.Outcome.Status != "match" {
		t.Errorf("outcome.Status = %q; want %q", result.Outcome.Status, "match")
	}
	if result.Verdict.Action == "" {
		t.Error("verdict.Action must be non-empty")
	}
}

// TestHandleReplaySubmit_ValidMismatch_200 verifies that a submission that
// produces a "mismatch" outcome (checks fail) still returns 200 (the protocol
// successfully processed the submission and derived a verdict).
func TestHandleReplaySubmit_ValidMismatch_200(t *testing.T) {
	_, ts, st, tm := newReplayServerWithSubmission(t)

	// Pre-create the task so the enforcer can call SetReplayStatus.
	task, err := tm.PostTask("poster-sub-2", "Run replay mismatch", "check outputs", "code", 100_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	storeReplayJobFull(t, st, "job-sub-mm", task.ID, "code",
		[]string{"go_test"}, "")

	sub := &replay.ReplaySubmission{
		JobID:    "job-sub-mm",
		TaskID:   task.ID,
		Category: "code",
		CheckResults: []replay.SubmittedCheckResult{
			{CheckType: "go_test", Pass: false, ExitCode: 1},
		},
		SubmittedAt: time.Now(),
	}
	resp := postReplaySubmit(t, ts, sub)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200 (valid submission even when mismatch)", resp.StatusCode)
	}

	var result struct {
		Outcome *replay.ReplayOutcome       `json:"outcome"`
		Verdict *verification.ReplayVerdict `json:"verdict"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Outcome.Status != "mismatch" {
		t.Errorf("outcome.Status = %q; want %q", result.Outcome.Status, "mismatch")
	}
	switch result.Verdict.Action {
	case "open_challenge", "slash_recommended":
		// expected
	default:
		t.Errorf("verdict.Action = %q; want open_challenge or slash_recommended", result.Verdict.Action)
	}
}

// TestHandleReplayOutcome_DisabledWhenSubmissionProcessorActive verifies that
// POST /v1/replay/outcome returns 410 Gone when the SubmissionProcessor is
// active. Callers must use POST /v1/replay/submit instead.
func TestHandleReplayOutcome_DisabledWhenSubmissionProcessorActive(t *testing.T) {
	_, ts, _, _ := newReplayServerWithSubmission(t) // wires both enforcer + submissionProcessor

	outcome := &replay.ReplayOutcome{
		JobID:  "job-bypass-test",
		TaskID: "task-bypass-test",
		Status: "match",
	}
	resp := postReplayOutcome(t, ts, outcome)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d; want 410 Gone — /v1/replay/outcome must be disabled when SubmissionProcessor is active",
			resp.StatusCode)
	}
}

// TestHandleReplayOutcome_DuplicateTerminal verifies that re-submitting an
// outcome for a completed job returns 400.
func TestHandleReplayOutcome_DuplicateTerminal(t *testing.T) {
	_, ts, tm, st, _ := newReplayServer(t)

	task, err := tm.PostTask("poster-2", "Write docs", "doc generation", "writing", 100_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	job := storeReplayJob(t, st, "job-dedup-api", task.ID)

	// First submission — succeeds.
	first := &replay.ReplayOutcome{
		JobID:      job.ID,
		TaskID:     task.ID,
		Status:     "match",
		ReplayedAt: time.Now(),
	}
	resp1 := postReplayOutcome(t, ts, first)
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first submission status = %d; want 200", resp1.StatusCode)
	}

	// Second submission — job is already completed → 400.
	second := &replay.ReplayOutcome{
		JobID:      job.ID,
		TaskID:     task.ID,
		Status:     "mismatch",
		ReplayedAt: time.Now(),
	}
	resp2 := postReplayOutcome(t, ts, second)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("second submission status = %d; want 400 (duplicate terminal)", resp2.StatusCode)
	}
}
