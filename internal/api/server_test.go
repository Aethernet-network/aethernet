package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aethernet/core/internal/api"
	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/dag"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/ocs"
)

// testSetup holds the components for a test API server.
type testSetup struct {
	kp       *crypto.KeyPair
	tl       *ledger.TransferLedger
	gl       *ledger.GenerationLedger
	reg      *identity.Registry
	eng      *ocs.Engine
	srv      *api.Server
	ts       *httptest.Server
}

// newTestSetup constructs a complete in-memory node stack and an httptest server.
func newTestSetup(t *testing.T) *testSetup {
	t.Helper()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	d := dag.New()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)
	sm := ledger.NewSupplyManager(tl, gl)

	srv := api.NewServer("", d, tl, gl, reg, eng, sm, nil, kp)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	return &testSetup{kp: kp, tl: tl, gl: gl, reg: reg, eng: eng, srv: srv, ts: ts}
}

// post is a convenience helper for JSON POST requests.
func post(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// get is a convenience helper for GET requests.
func get(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// decodeJSON decodes the response body into v.
func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHandleStatus(t *testing.T) {
	setup := newTestSetup(t)

	resp := get(t, setup.ts, "/v1/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var status struct {
		AgentID     string  `json:"agent_id"`
		DAGSize     int     `json:"dag_size"`
		SupplyRatio float64 `json:"supply_ratio"`
	}
	decodeJSON(t, resp, &status)

	if status.AgentID != string(setup.kp.AgentID()) {
		t.Errorf("agent_id mismatch: got %q", status.AgentID)
	}
	if status.DAGSize != 0 {
		t.Errorf("want dag_size=0, got %d", status.DAGSize)
	}
	if status.SupplyRatio < 1.0 {
		t.Errorf("supply_ratio should be >= 1.0, got %f", status.SupplyRatio)
	}
}

func TestHandleDAGTips_Empty(t *testing.T) {
	setup := newTestSetup(t)

	resp := get(t, setup.ts, "/v1/dag/tips")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var tips struct {
		Tips []string `json:"tips"`
	}
	decodeJSON(t, resp, &tips)

	if len(tips.Tips) != 0 {
		t.Errorf("want empty tips on fresh DAG, got %v", tips.Tips)
	}
}

func TestHandleRegisterAgent(t *testing.T) {
	setup := newTestSetup(t)

	resp := post(t, setup.ts, "/v1/agents", map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}

	var result struct {
		AgentID         string `json:"agent_id"`
		FingerprintHash string `json:"fingerprint_hash"`
	}
	decodeJSON(t, resp, &result)

	if result.AgentID != string(setup.kp.AgentID()) {
		t.Errorf("agent_id mismatch: got %q, want %q", result.AgentID, setup.kp.AgentID())
	}
	if result.FingerprintHash == "" {
		t.Error("fingerprint_hash must not be empty")
	}
}

func TestHandleRegisterAgent_AlreadyExists(t *testing.T) {
	setup := newTestSetup(t)

	// First registration returns 201.
	r1 := post(t, setup.ts, "/v1/agents", map[string]any{})
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("first registration: want 201, got %d", r1.StatusCode)
	}
	r1.Body.Close()

	// Second registration returns 200 (idempotent).
	r2 := post(t, setup.ts, "/v1/agents", map[string]any{})
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("second registration: want 200, got %d", r2.StatusCode)
	}

	var result struct {
		AgentID string `json:"agent_id"`
	}
	decodeJSON(t, r2, &result)
	if result.AgentID != string(setup.kp.AgentID()) {
		t.Errorf("agent_id mismatch on re-registration")
	}
}

func TestHandleGetAgent(t *testing.T) {
	setup := newTestSetup(t)

	// Register the agent first.
	r := post(t, setup.ts, "/v1/agents", map[string]any{})
	r.Body.Close()

	agentID := string(setup.kp.AgentID())
	resp := get(t, setup.ts, "/v1/agents/"+agentID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var fp struct {
		AgentID string `json:"agent_id"`
	}
	decodeJSON(t, resp, &fp)
	if fp.AgentID != agentID {
		t.Errorf("agent_id mismatch: got %q", fp.AgentID)
	}
}

func TestHandleGetAgent_NotFound(t *testing.T) {
	setup := newTestSetup(t)

	resp := get(t, setup.ts, "/v1/agents/doesnotexist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHandleGetBalance_ZeroBalance(t *testing.T) {
	setup := newTestSetup(t)

	agentID := string(setup.kp.AgentID())
	resp := get(t, setup.ts, "/v1/agents/"+agentID+"/balance")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var bal struct {
		Balance  uint64 `json:"balance"`
		Currency string `json:"currency"`
	}
	decodeJSON(t, resp, &bal)
	if bal.Balance != 0 {
		t.Errorf("want balance=0 for unfunded agent, got %d", bal.Balance)
	}
	if bal.Currency != "AET" {
		t.Errorf("want currency=AET, got %q", bal.Currency)
	}
}

func TestHandleTransfer_Funded(t *testing.T) {
	setup := newTestSetup(t)

	// Fund the agent directly via the ledger (no HTTP endpoint for funding).
	if err := setup.tl.FundAgent(setup.kp.AgentID(), 1_000_000); err != nil {
		t.Fatalf("fund agent: %v", err)
	}

	resp := post(t, setup.ts, "/v1/transfer", map[string]any{
		"to_agent":     string(setup.kp.AgentID()),
		"amount":       500,
		"currency":     "AET",
		"stake_amount": 1000,
	})
	if resp.StatusCode != http.StatusCreated {
		var e map[string]string
		decodeJSON(t, resp, &e)
		t.Fatalf("want 201, got %d: %v", resp.StatusCode, e)
	}

	var result struct {
		EventID string `json:"event_id"`
	}
	decodeJSON(t, resp, &result)
	if result.EventID == "" {
		t.Error("event_id must not be empty")
	}
}

func TestHandleTransfer_InsufficientFunds(t *testing.T) {
	setup := newTestSetup(t)

	// No FundAgent call — agent has zero balance.
	resp := post(t, setup.ts, "/v1/transfer", map[string]any{
		"to_agent":     string(setup.kp.AgentID()),
		"amount":       500,
		"stake_amount": 1000,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHandleGeneration_GetEvent(t *testing.T) {
	setup := newTestSetup(t)

	// Submit a generation event.
	resp := post(t, setup.ts, "/v1/generation", map[string]any{
		"claimed_value":    5000,
		"evidence_hash":    "sha256:deadbeef",
		"task_description": "unit-test inference run",
		"stake_amount":     1000,
	})
	if resp.StatusCode != http.StatusCreated {
		var e map[string]string
		decodeJSON(t, resp, &e)
		t.Fatalf("want 201, got %d: %v", resp.StatusCode, e)
	}

	var result struct {
		EventID string `json:"event_id"`
	}
	decodeJSON(t, resp, &result)
	if result.EventID == "" {
		t.Fatal("event_id must not be empty")
	}

	// Fetch the event by ID.
	evResp := get(t, setup.ts, "/v1/events/"+result.EventID)
	if evResp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", evResp.StatusCode)
	}

	var ev struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	decodeJSON(t, evResp, &ev)
	if ev.ID != result.EventID {
		t.Errorf("event id mismatch: got %q, want %q", ev.ID, result.EventID)
	}
	if ev.Type != "Generation" {
		t.Errorf("event type: got %q, want Generation", ev.Type)
	}
}

func TestHandleListAgents(t *testing.T) {
	setup := newTestSetup(t)

	// Register the agent.
	r := post(t, setup.ts, "/v1/agents", map[string]any{})
	r.Body.Close()

	resp := get(t, setup.ts, "/v1/agents")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var agents []struct {
		AgentID string `json:"agent_id"`
	}
	decodeJSON(t, resp, &agents)

	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	if agents[0].AgentID != string(setup.kp.AgentID()) {
		t.Errorf("agent_id mismatch")
	}
}

func TestPostVerify_Success(t *testing.T) {
	setup := newTestSetup(t)

	// Submit a generation event so there is something pending.
	genResp := post(t, setup.ts, "/v1/generation", map[string]any{
		"claimed_value": 5000,
		"evidence_hash": "sha256:verify-test",
		"stake_amount":  1000,
	})
	if genResp.StatusCode != http.StatusCreated {
		t.Fatalf("generation: want 201, got %d", genResp.StatusCode)
	}
	var genResult struct {
		EventID string `json:"event_id"`
	}
	decodeJSON(t, genResp, &genResult)

	// Verify the event with a positive verdict.
	resp := post(t, setup.ts, "/v1/verify", map[string]any{
		"event_id":       genResult.EventID,
		"verdict":        true,
		"verified_value": 5000,
	})
	if resp.StatusCode != http.StatusOK {
		var e map[string]string
		decodeJSON(t, resp, &e)
		t.Fatalf("verify: want 200, got %d: %v", resp.StatusCode, e)
	}

	var result struct {
		EventID string `json:"event_id"`
		Verdict bool   `json:"verdict"`
		Status  string `json:"status"`
	}
	decodeJSON(t, resp, &result)
	if result.Status != "settled" {
		t.Errorf("want status=settled, got %q", result.Status)
	}
	if !result.Verdict {
		t.Error("want verdict=true")
	}
}

func TestPostVerify_NotPending(t *testing.T) {
	setup := newTestSetup(t)

	resp := post(t, setup.ts, "/v1/verify", map[string]any{
		"event_id": "0000000000000000000000000000000000000000000000000000000000000000",
		"verdict":  true,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown event, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGetPending(t *testing.T) {
	setup := newTestSetup(t)

	// No pending items on a fresh node.
	resp := get(t, setup.ts, "/v1/pending")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var items []map[string]any
	decodeJSON(t, resp, &items)
	if len(items) != 0 {
		t.Errorf("want 0 pending items on fresh node, got %d", len(items))
	}

	// Submit a generation event — it should appear as pending.
	genResp := post(t, setup.ts, "/v1/generation", map[string]any{
		"claimed_value": 3000,
		"evidence_hash": "sha256:pending-test",
		"stake_amount":  1000,
	})
	if genResp.StatusCode != http.StatusCreated {
		t.Fatalf("generation: want 201, got %d", genResp.StatusCode)
	}
	genResp.Body.Close()

	resp2 := get(t, setup.ts, "/v1/pending")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp2.StatusCode)
	}
	var items2 []map[string]any
	decodeJSON(t, resp2, &items2)
	if len(items2) != 1 {
		t.Errorf("want 1 pending item after generation, got %d", len(items2))
	}
}
