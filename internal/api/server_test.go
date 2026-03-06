package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/registry"
	"github.com/Aethernet-network/aethernet/internal/staking"
	"github.com/Aethernet-network/aethernet/internal/wallet"
)

// testSetup holds the components for a test API server.
type testSetup struct {
	kp       *crypto.KeyPair
	d        *dag.DAG
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
	srv.SetServiceRegistry(registry.New())
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	return &testSetup{kp: kp, d: d, tl: tl, gl: gl, reg: reg, eng: eng, srv: srv, ts: ts}
}

// newVerifierServer creates a second API server sharing the same internal
// components (dag, ledgers, registry, engine) as setup but with a fresh keypair.
// This simulates an independent validator node on the same network.
func newVerifierServer(t *testing.T, setup *testSetup) *httptest.Server {
	t.Helper()
	kp2, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate verifier keypair: %v", err)
	}
	sm2 := ledger.NewSupplyManager(setup.tl, setup.gl)
	srv2 := api.NewServer("", setup.d, setup.tl, setup.gl, setup.reg, setup.eng, sm2, nil, kp2)
	ts2 := httptest.NewServer(srv2)
	t.Cleanup(ts2.Close)
	return ts2
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

	// Verify using an independent validator node (different keypair, shared engine).
	// The submitter cannot verify their own event (anti-self-dealing rule).
	verifier := newVerifierServer(t, setup)
	resp := post(t, verifier, "/v1/verify", map[string]any{
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

func TestGetRecentEvents(t *testing.T) {
	setup := newTestSetup(t)

	// Empty DAG — should return an empty array.
	resp := get(t, setup.ts, "/v1/events/recent")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var items []map[string]any
	decodeJSON(t, resp, &items)
	if len(items) != 0 {
		t.Errorf("want 0 events on empty DAG, got %d", len(items))
	}

	// Submit two generation events.
	for i := 0; i < 2; i++ {
		r := post(t, setup.ts, "/v1/generation", map[string]any{
			"claimed_value": 1000,
			"evidence_hash": "sha256:recent-test",
			"stake_amount":  1000,
		})
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("generation %d: want 201, got %d", i, r.StatusCode)
		}
		r.Body.Close()
	}

	resp2 := get(t, setup.ts, "/v1/events/recent?limit=10")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp2.StatusCode)
	}
	var items2 []map[string]any
	decodeJSON(t, resp2, &items2)
	if len(items2) != 2 {
		t.Errorf("want 2 recent events, got %d", len(items2))
	}
	// Verify required fields are present.
	for _, item := range items2 {
		if item["id"] == nil {
			t.Error("event missing id field")
		}
		if item["type"] == nil {
			t.Error("event missing type field")
		}
	}
}

func TestGetLeaderboard(t *testing.T) {
	setup := newTestSetup(t)

	// Register the node's agent.
	r := post(t, setup.ts, "/v1/agents", map[string]any{})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", r.StatusCode)
	}
	r.Body.Close()

	resp := get(t, setup.ts, "/v1/agents/leaderboard?sort=reputation&limit=10")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var entries []map[string]any
	decodeJSON(t, resp, &entries)
	if len(entries) != 1 {
		t.Fatalf("want 1 leaderboard entry, got %d", len(entries))
	}
	if entries[0]["agent_id"] == nil {
		t.Error("leaderboard entry missing agent_id")
	}
	if entries[0]["rank"] == nil {
		t.Error("leaderboard entry missing rank")
	}
}

func TestGetDagStats(t *testing.T) {
	setup := newTestSetup(t)

	resp := get(t, setup.ts, "/v1/dag/stats")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var stats map[string]any
	decodeJSON(t, resp, &stats)
	if stats["total_events"] == nil {
		t.Error("dag stats missing total_events")
	}
	if stats["tips_count"] == nil {
		t.Error("dag stats missing tips_count")
	}

	// Submit an event and verify the counts increase.
	genResp := post(t, setup.ts, "/v1/generation", map[string]any{
		"claimed_value": 1000,
		"evidence_hash": "sha256:stats-test",
		"stake_amount":  1000,
	})
	if genResp.StatusCode != http.StatusCreated {
		t.Fatalf("generation: want 201, got %d", genResp.StatusCode)
	}
	genResp.Body.Close()

	resp2 := get(t, setup.ts, "/v1/dag/stats")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("want 200 after event, got %d", resp2.StatusCode)
	}
	var stats2 map[string]any
	decodeJSON(t, resp2, &stats2)
	if int(stats2["total_events"].(float64)) != 1 {
		t.Errorf("want total_events=1, got %v", stats2["total_events"])
	}
}

func TestGetNetworkActivity(t *testing.T) {
	setup := newTestSetup(t)

	resp := get(t, setup.ts, "/v1/network/activity?hours=24")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result map[string]any
	decodeJSON(t, resp, &result)
	if result["hours"] == nil {
		t.Error("network activity missing hours field")
	}
	if result["buckets"] == nil {
		t.Error("network activity missing buckets field")
	}
	buckets, ok := result["buckets"].([]any)
	if !ok {
		t.Fatal("buckets is not an array")
	}
	if len(buckets) != 24 {
		t.Errorf("want 24 buckets, got %d", len(buckets))
	}
}

// ---------------------------------------------------------------------------
// Registry endpoint tests
// ---------------------------------------------------------------------------

func TestPostRegistry(t *testing.T) {
	setup := newTestSetup(t)

	resp := post(t, setup.ts, "/v1/registry", map[string]any{
		"agent_id":    string(setup.kp.AgentID()),
		"name":        "Document Summarizer",
		"description": "Summarizes documents up to 50 pages",
		"category":    "research",
		"price_aet":   5000,
		"tags":        []string{"summarization", "research"},
		"active":      true,
	})
	if resp.StatusCode != http.StatusCreated {
		var e map[string]string
		decodeJSON(t, resp, &e)
		t.Fatalf("want 201, got %d: %v", resp.StatusCode, e)
	}

	var result map[string]any
	decodeJSON(t, resp, &result)
	if result["name"] != "Document Summarizer" {
		t.Errorf("name mismatch: got %v", result["name"])
	}
	if result["category"] != "research" {
		t.Errorf("category mismatch: got %v", result["category"])
	}
}

func TestGetRegistrySearch(t *testing.T) {
	setup := newTestSetup(t)

	// Register a listing.
	r := post(t, setup.ts, "/v1/registry", map[string]any{
		"name":     "Code Reviewer",
		"category": "code-review",
		"active":   true,
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", r.StatusCode)
	}
	r.Body.Close()

	resp := get(t, setup.ts, "/v1/registry/search?q=code")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var results []map[string]any
	decodeJSON(t, resp, &results)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0]["name"] != "Code Reviewer" {
		t.Errorf("wrong result name: %v", results[0]["name"])
	}
}

func TestGetRegistrySearch_Category(t *testing.T) {
	setup := newTestSetup(t)

	r := post(t, setup.ts, "/v1/registry", map[string]any{
		"name":     "Research Assistant",
		"category": "research",
		"active":   true,
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", r.StatusCode)
	}
	r.Body.Close()

	resp := get(t, setup.ts, "/v1/registry/search?category=research")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var results []map[string]any
	decodeJSON(t, resp, &results)
	if len(results) != 1 {
		t.Fatalf("want 1 result for category=research, got %d", len(results))
	}
}

func TestGetRegistryListing(t *testing.T) {
	setup := newTestSetup(t)

	r := post(t, setup.ts, "/v1/registry", map[string]any{
		"name":     "ML Classifier",
		"category": "research",
		"active":   true,
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", r.StatusCode)
	}
	r.Body.Close()

	agentID := string(setup.kp.AgentID())
	resp := get(t, setup.ts, "/v1/registry/"+agentID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var listing map[string]any
	decodeJSON(t, resp, &listing)
	if listing["name"] != "ML Classifier" {
		t.Errorf("name: got %v", listing["name"])
	}
	if listing["agent_id"] == nil {
		t.Error("agent_id must be present")
	}
}

func TestGetCategories(t *testing.T) {
	setup := newTestSetup(t)

	r := post(t, setup.ts, "/v1/registry", map[string]any{
		"name":     "Writer",
		"category": "writing",
		"active":   true,
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", r.StatusCode)
	}
	r.Body.Close()

	resp := get(t, setup.ts, "/v1/registry/categories")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var cats map[string]any
	decodeJSON(t, resp, &cats)
	if cats["writing"] == nil {
		t.Error("categories must include 'writing'")
	}
	if int(cats["writing"].(float64)) != 1 {
		t.Errorf("writing count: want 1, got %v", cats["writing"])
	}
}

// ---------------------------------------------------------------------------
// CORS tests
// ---------------------------------------------------------------------------

func TestCORS_Headers(t *testing.T) {
	setup := newTestSetup(t)

	resp := get(t, setup.ts, "/v1/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin: got %q, want *", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Access-Control-Allow-Methods header missing")
	}
}

func TestCORS_Preflight(t *testing.T) {
	setup := newTestSetup(t)

	req, err := http.NewRequest(http.MethodOptions, setup.ts.URL+"/v1/transfer", nil)
	if err != nil {
		t.Fatalf("build OPTIONS request: %v", err)
	}
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight: want 204, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin: got %q, want *", got)
	}
}

// ---------------------------------------------------------------------------
// Structured error response tests
// ---------------------------------------------------------------------------

func TestErrorResponse_Format(t *testing.T) {
	setup := newTestSetup(t)

	// Any 404 should return an APIError struct with at minimum an "error" field.
	resp := get(t, setup.ts, "/v1/agents/nonexistent")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}

	var apiErr struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Details string `json:"details"`
	}
	decodeJSON(t, resp, &apiErr)
	if apiErr.Error == "" {
		t.Error("error field must not be empty")
	}
	if apiErr.Code == "" {
		t.Error("code field must not be empty for agent_not_found")
	}
}

func TestErrorResponse_AgentNotFound(t *testing.T) {
	setup := newTestSetup(t)

	resp := get(t, setup.ts, "/v1/agents/doesnotexist2")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}

	var apiErr struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decodeJSON(t, resp, &apiErr)
	if apiErr.Code != "agent_not_found" {
		t.Errorf("code: want agent_not_found, got %q", apiErr.Code)
	}
}

func TestErrorResponse_InvalidJSON(t *testing.T) {
	setup := newTestSetup(t)

	// Send malformed JSON to a POST endpoint.
	resp, err := http.Post(setup.ts.URL+"/v1/transfer", "application/json",
		bytes.NewReader([]byte(`{invalid json`)))
	if err != nil {
		t.Fatalf("POST /v1/transfer: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}

	var apiErr struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decodeJSON(t, resp, &apiErr)
	if apiErr.Code != "invalid_request" {
		t.Errorf("code: want invalid_request, got %q", apiErr.Code)
	}
}

func TestDeleteRegistry(t *testing.T) {
	setup := newTestSetup(t)

	// Register a listing under the node's own agentID.
	r := post(t, setup.ts, "/v1/registry", map[string]any{
		"agent_id": string(setup.kp.AgentID()),
		"name":     "To Be Deleted",
		"category": "writing",
		"active":   true,
	})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", r.StatusCode)
	}
	r.Body.Close()

	// DELETE the listing.
	agentID := string(setup.kp.AgentID())
	req, err := http.NewRequest(http.MethodDelete, setup.ts.URL+"/v1/registry/"+agentID, nil)
	if err != nil {
		t.Fatalf("build DELETE request: %v", err)
	}
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE request: %v", err)
	}
	if delResp.StatusCode != http.StatusOK {
		var e map[string]string
		decodeJSON(t, delResp, &e)
		t.Fatalf("want 200, got %d: %v", delResp.StatusCode, e)
	}

	var result map[string]any
	decodeJSON(t, delResp, &result)
	if result["active"] != false {
		t.Errorf("want active=false, got %v", result["active"])
	}

	// Search should no longer return the listing.
	resp := get(t, setup.ts, "/v1/registry/search?category=writing")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search: want 200, got %d", resp.StatusCode)
	}
	var results []map[string]any
	decodeJSON(t, resp, &results)
	for _, l := range results {
		if l["name"] == "To Be Deleted" {
			t.Error("deactivated listing still appears in search")
		}
	}
}

// TestHandleRegisterAgent_OnboardingFunded verifies that a newly registered agent
// receives a non-zero onboarding allocation that is immediately spendable and
// auto-staked, without requiring prior genesis seeding.
func TestHandleRegisterAgent_OnboardingFunded(t *testing.T) {
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
	srv.SetServiceRegistry(registry.New())

	// Wire in staking, fees, and wallet so the full onboarding path runs.
	stakeMgr := staking.NewStakeManager()
	feeCollector := fees.NewCollector(tl)
	walletMgr := wallet.New()
	srv.SetEconomics(walletMgr, stakeMgr, feeCollector)

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	// Register the agent — no prior genesis seeding required.
	r := post(t, ts, "/v1/agents", map[string]any{})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", r.StatusCode)
	}

	var regResp struct {
		AgentID              string `json:"agent_id"`
		OnboardingAllocation uint64 `json:"onboarding_allocation"`
		TrustLimit           uint64 `json:"trust_limit"`
	}
	decodeJSON(t, r, &regResp)

	if regResp.OnboardingAllocation == 0 {
		t.Error("onboarding_allocation must be non-zero after registration")
	}
	if regResp.TrustLimit == 0 {
		t.Error("trust_limit must be non-zero when staking is wired in")
	}

	// Verify the agent has a spendable balance in the transfer ledger.
	agentID := crypto.AgentID(kp.AgentID())
	bal, err := tl.Balance(agentID)
	if err != nil {
		t.Fatalf("balance check: %v", err)
	}
	if bal == 0 {
		t.Error("agent balance must be non-zero after onboarding")
	}

	// Verify the agent has a staked amount.
	staked := stakeMgr.StakedAmount(agentID)
	if staked == 0 {
		t.Error("agent staked_amount must be non-zero after auto-stake")
	}
}

// TestHandleRegisterAgent_TwoDistinctAgents verifies that two different external
// agents can register through the same node using their own keypairs, receive
// separate identities, and accumulate independent balances and staked amounts.
func TestHandleRegisterAgent_TwoDistinctAgents(t *testing.T) {
	setup := newTestSetup(t)

	// Wire staking and economics so onboarding grants are auto-staked.
	stakeMgr := staking.NewStakeManager()
	feeCollector := fees.NewCollector(setup.tl)
	walletMgr := wallet.New()
	setup.srv.SetEconomics(walletMgr, stakeMgr, feeCollector)

	// Generate two independent keypairs representing distinct external agents.
	kp1, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate kp1: %v", err)
	}
	kp2, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate kp2: %v", err)
	}

	pubKeyB64_1 := base64.StdEncoding.EncodeToString(kp1.PublicKey)
	pubKeyB64_2 := base64.StdEncoding.EncodeToString(kp2.PublicKey)

	// Register agent 1.
	r1 := post(t, setup.ts, "/v1/agents", map[string]any{
		"agent_id":       string(kp1.AgentID()),
		"public_key_b64": pubKeyB64_1,
	})
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("agent1 registration: want 201, got %d", r1.StatusCode)
	}
	var resp1 struct {
		AgentID              string `json:"agent_id"`
		OnboardingAllocation uint64 `json:"onboarding_allocation"`
	}
	decodeJSON(t, r1, &resp1)

	// Register agent 2.
	r2 := post(t, setup.ts, "/v1/agents", map[string]any{
		"agent_id":       string(kp2.AgentID()),
		"public_key_b64": pubKeyB64_2,
	})
	if r2.StatusCode != http.StatusCreated {
		t.Fatalf("agent2 registration: want 201, got %d", r2.StatusCode)
	}
	var resp2 struct {
		AgentID              string `json:"agent_id"`
		OnboardingAllocation uint64 `json:"onboarding_allocation"`
	}
	decodeJSON(t, r2, &resp2)

	// The two agents must have distinct identities.
	if resp1.AgentID == resp2.AgentID {
		t.Errorf("agents must have different agent_ids, both got %q", resp1.AgentID)
	}
	if resp1.AgentID != string(kp1.AgentID()) {
		t.Errorf("agent1 id: want %q, got %q", kp1.AgentID(), resp1.AgentID)
	}
	if resp2.AgentID != string(kp2.AgentID()) {
		t.Errorf("agent2 id: want %q, got %q", kp2.AgentID(), resp2.AgentID)
	}

	// Both agents received an onboarding allocation.
	if resp1.OnboardingAllocation == 0 {
		t.Error("agent1 onboarding_allocation must be non-zero")
	}
	if resp2.OnboardingAllocation == 0 {
		t.Error("agent2 onboarding_allocation must be non-zero")
	}

	// Balances are separate — each agent has its own spendable balance.
	bal1, _ := setup.tl.Balance(kp1.AgentID())
	bal2, _ := setup.tl.Balance(kp2.AgentID())
	if bal1 == 0 {
		t.Error("agent1 balance must be non-zero after onboarding")
	}
	if bal2 == 0 {
		t.Error("agent2 balance must be non-zero after onboarding")
	}

	// Staked amounts are separate.
	staked1 := stakeMgr.StakedAmount(kp1.AgentID())
	staked2 := stakeMgr.StakedAmount(kp2.AgentID())
	if staked1 == 0 {
		t.Error("agent1 staked_amount must be non-zero after auto-stake")
	}
	if staked2 == 0 {
		t.Error("agent2 staked_amount must be non-zero after auto-stake")
	}

	// Registering the same agent again returns 200 (idempotent), not 201.
	r1b := post(t, setup.ts, "/v1/agents", map[string]any{
		"agent_id":       string(kp1.AgentID()),
		"public_key_b64": pubKeyB64_1,
	})
	if r1b.StatusCode != http.StatusOK {
		t.Fatalf("re-registration of agent1: want 200, got %d", r1b.StatusCode)
	}
	r1b.Body.Close()
}

// TestHandleRegisterAgent_HumanReadableIDs verifies that agents can be registered
// with arbitrary human-readable IDs (e.g. "researcher-agent") paired with an
// Ed25519 public key, and that both appear correctly in the agent listing.
func TestHandleRegisterAgent_HumanReadableIDs(t *testing.T) {
	setup := newTestSetup(t)

	// Generate keypairs for the two agents.
	kp1, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate kp1: %v", err)
	}
	kp2, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate kp2: %v", err)
	}

	pub1B64 := base64.StdEncoding.EncodeToString(kp1.PublicKey)
	pub2B64 := base64.StdEncoding.EncodeToString(kp2.PublicKey)

	// Register "researcher-agent" with kp1's public key.
	r1 := post(t, setup.ts, "/v1/agents", map[string]any{
		"agent_id":       "researcher-agent",
		"public_key_b64": pub1B64,
	})
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("researcher registration: want 201, got %d", r1.StatusCode)
	}
	var resp1 struct {
		AgentID              string `json:"agent_id"`
		OnboardingAllocation uint64 `json:"onboarding_allocation"`
	}
	decodeJSON(t, r1, &resp1)
	if resp1.AgentID != "researcher-agent" {
		t.Errorf("want agent_id %q, got %q", "researcher-agent", resp1.AgentID)
	}
	if resp1.OnboardingAllocation == 0 {
		t.Error("researcher-agent: onboarding_allocation must be non-zero")
	}

	// Register "writer-agent" with kp2's public key.
	r2 := post(t, setup.ts, "/v1/agents", map[string]any{
		"agent_id":       "writer-agent",
		"public_key_b64": pub2B64,
	})
	if r2.StatusCode != http.StatusCreated {
		t.Fatalf("writer registration: want 201, got %d", r2.StatusCode)
	}
	var resp2 struct {
		AgentID              string `json:"agent_id"`
		OnboardingAllocation uint64 `json:"onboarding_allocation"`
	}
	decodeJSON(t, r2, &resp2)
	if resp2.AgentID != "writer-agent" {
		t.Errorf("want agent_id %q, got %q", "writer-agent", resp2.AgentID)
	}
	if resp2.OnboardingAllocation == 0 {
		t.Error("writer-agent: onboarding_allocation must be non-zero")
	}

	// List all agents and confirm both appear with the correct IDs.
	listResp := get(t, setup.ts, "/v1/agents")
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list agents: want 200, got %d", listResp.StatusCode)
	}
	var agents []struct {
		AgentID string `json:"agent_id"`
	}
	decodeJSON(t, listResp, &agents)

	found := map[string]bool{}
	for _, a := range agents {
		found[a.AgentID] = true
	}
	if !found["researcher-agent"] {
		t.Error("researcher-agent not found in agent listing")
	}
	if !found["writer-agent"] {
		t.Error("writer-agent not found in agent listing")
	}

	// Re-registering the same human-readable ID is idempotent (200, not 201).
	r1b := post(t, setup.ts, "/v1/agents", map[string]any{
		"agent_id":       "researcher-agent",
		"public_key_b64": pub1B64,
	})
	if r1b.StatusCode != http.StatusOK {
		t.Fatalf("re-registration: want 200, got %d", r1b.StatusCode)
	}
	r1b.Body.Close()
}
