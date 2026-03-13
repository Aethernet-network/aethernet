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
