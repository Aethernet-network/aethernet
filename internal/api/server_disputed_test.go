package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/genesis"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// newDisputedServer returns a test server WITHOUT a task manager, used to test
// the 501 case.
func newNoTaskServer(t *testing.T) *httptest.Server {
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
	// NOTE: no SetTaskManager call — task manager intentionally absent.
	srv := api.NewServer("", d, tl, gl, reg, eng, sm, nil, kp)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// TestHandleDisputedTasks_NoTaskManager verifies that the endpoint returns 501
// when no task manager has been wired.
func TestHandleDisputedTasks_NoTaskManager(t *testing.T) {
	ts := newNoTaskServer(t)

	resp, err := http.Get(ts.URL + "/v1/tasks/disputed")
	if err != nil {
		t.Fatalf("GET /v1/tasks/disputed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d; want 501", resp.StatusCode)
	}
}

// TestHandleDisputedTasks_EmptyWhenNone verifies that an empty JSON array is
// returned when there are no disputed tasks.
func TestHandleDisputedTasks_EmptyWhenNone(t *testing.T) {
	setup := newTestSetup(t)
	// Post a non-disputed task.
	if _, err := setup.taskMgr.PostTask("poster-d", "Normal task", "desc", "code", 1_000); err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	resp, err := http.Get(setup.ts.URL + "/v1/tasks/disputed")
	if err != nil {
		t.Fatalf("GET /v1/tasks/disputed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}

	var result []*tasks.Task
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("disputed tasks = %d; want 0", len(result))
	}
}

// TestHandleDisputedTasks_ReturnsDisputedTasks verifies that tasks with
// ReplayStatus "replay_disputed" are returned and non-disputed tasks are not.
func TestHandleDisputedTasks_ReturnsDisputedTasks(t *testing.T) {
	setup := newTestSetup(t)

	// Create two tasks; dispute one.
	td, err := setup.taskMgr.PostTask("poster-d2", "Disputed task", "desc", "code", 2_000)
	if err != nil {
		t.Fatalf("PostTask (disputed): %v", err)
	}
	tc, err := setup.taskMgr.PostTask("poster-d3", "Clean task", "desc", "data", 3_000)
	if err != nil {
		t.Fatalf("PostTask (clean): %v", err)
	}
	_ = tc // deliberately unused — should NOT appear in disputed results

	if err := setup.taskMgr.SetReplayStatus(td.ID, "replay_disputed", "job-d-api"); err != nil {
		t.Fatalf("SetReplayStatus: %v", err)
	}

	resp, err := http.Get(setup.ts.URL + "/v1/tasks/disputed")
	if err != nil {
		t.Fatalf("GET /v1/tasks/disputed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}

	var result []*tasks.Task
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("disputed tasks = %d; want 1", len(result))
	}
	if result[0].ID != td.ID {
		t.Errorf("result[0].ID = %q; want %q", result[0].ID, td.ID)
	}
	if result[0].ReplayStatus != "replay_disputed" {
		t.Errorf("result[0].ReplayStatus = %q; want %q", result[0].ReplayStatus, "replay_disputed")
	}
}

// TestHandleDisputedTasks_MultipleDisputed verifies that multiple disputed tasks
// are all returned.
func TestHandleDisputedTasks_MultipleDisputed(t *testing.T) {
	setup := newTestSetup(t)

	for i := 0; i < 3; i++ {
		task, err := setup.taskMgr.PostTask("poster-md", "Task", "desc", "code", 1_000)
		if err != nil {
			t.Fatalf("PostTask %d: %v", i, err)
		}
		if err := setup.taskMgr.SetReplayStatus(task.ID, "replay_disputed", "job-md"); err != nil {
			t.Fatalf("SetReplayStatus %d: %v", i, err)
		}
	}
	// One non-disputed task — should not appear.
	if _, err := setup.taskMgr.PostTask("poster-md", "Clean", "desc", "data", 500); err != nil {
		t.Fatalf("PostTask (clean): %v", err)
	}

	resp, err := http.Get(setup.ts.URL + "/v1/tasks/disputed")
	if err != nil {
		t.Fatalf("GET /v1/tasks/disputed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}

	var result []*tasks.Task
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("disputed tasks = %d; want 3", len(result))
	}
}
