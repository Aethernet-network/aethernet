package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
)

// ---------------------------------------------------------------------------
// Test double: stubChallengeManager satisfies api.challengeSource
// ---------------------------------------------------------------------------

type stubChallengeManager struct {
	openID        string
	openCreatedAt string
	openErr       error

	resolveRefund  uint64
	resolveForfit  uint64
	resolveErr     error

	challenges []api.ChallengeRecord
}

func (s *stubChallengeManager) OpenChallenge(taskID, challengerID, targetID string, bond uint64) (string, string, error) {
	return s.openID, s.openCreatedAt, s.openErr
}

func (s *stubChallengeManager) ResolveChallenge(challengeID string, outcome string, fraudBounty uint64) (uint64, uint64, error) {
	return s.resolveRefund, s.resolveForfit, s.resolveErr
}

func (s *stubChallengeManager) ChallengesForTask(taskID string) []api.ChallengeRecord {
	return s.challenges
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newChallengeTestServer(t *testing.T, cm *stubChallengeManager) *httptest.Server {
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
	if cm != nil {
		srv.SetChallengeManager(cm)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// ---------------------------------------------------------------------------
// TestHandleOpenChallenge_501_WhenNotConfigured
// ---------------------------------------------------------------------------

// TestHandleOpenChallenge_501_WhenNotConfigured verifies that when no challenge
// manager is wired, POST /v1/challenges returns 501.
func TestHandleOpenChallenge_501_WhenNotConfigured(t *testing.T) {
	ts := newChallengeTestServer(t, nil)

	body := `{"task_id":"t1","challenger_id":"c1","target_id":"v1","bond":1000000}`
	resp, err := http.Post(ts.URL+"/v1/challenges", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /v1/challenges: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d; want 501", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestHandleOpenChallenge_Success
// ---------------------------------------------------------------------------

// TestHandleOpenChallenge_Success verifies that POST /v1/challenges returns
// the opened challenge record.
func TestHandleOpenChallenge_Success(t *testing.T) {
	cm := &stubChallengeManager{
		openID:        "chal-abc123",
		openCreatedAt: "2026-01-01T00:00:00Z",
	}
	ts := newChallengeTestServer(t, cm)

	body := `{"task_id":"t1","challenger_id":"c1","target_id":"v1","bond":1000000}`
	resp, err := http.Post(ts.URL+"/v1/challenges", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /v1/challenges: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	var rec api.ChallengeRecord
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rec.ID != "chal-abc123" {
		t.Errorf("ID = %q; want %q", rec.ID, "chal-abc123")
	}
	if rec.Status != "open" {
		t.Errorf("Status = %q; want %q", rec.Status, "open")
	}
	if rec.Bond != 1_000_000 {
		t.Errorf("Bond = %d; want 1000000", rec.Bond)
	}
}

// ---------------------------------------------------------------------------
// TestHandleOpenChallenge_MissingFields
// ---------------------------------------------------------------------------

// TestHandleOpenChallenge_MissingFields verifies that POST /v1/challenges with
// a missing required field returns 400.
func TestHandleOpenChallenge_MissingFields(t *testing.T) {
	cm := &stubChallengeManager{openID: "x", openCreatedAt: "t"}
	ts := newChallengeTestServer(t, cm)

	// target_id is missing
	body := `{"task_id":"t1","challenger_id":"c1","bond":1000000}`
	resp, err := http.Post(ts.URL+"/v1/challenges", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /v1/challenges: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestHandleResolveChallenge_Success
// ---------------------------------------------------------------------------

// TestHandleResolveChallenge_Success verifies that POST /v1/challenges/{id}/resolve
// returns the economic distribution.
func TestHandleResolveChallenge_Success(t *testing.T) {
	cm := &stubChallengeManager{
		resolveRefund: 1_000_000,
		resolveForfit: 0,
	}
	ts := newChallengeTestServer(t, cm)

	body := `{"outcome":"succeeded","fraud_bounty":500000}`
	resp, err := http.Post(ts.URL+"/v1/challenges/chal-abc/resolve", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /v1/challenges/{id}/resolve: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["challenge_id"] != "chal-abc" {
		t.Errorf("challenge_id = %v; want chal-abc", result["challenge_id"])
	}
	if result["outcome"] != "succeeded" {
		t.Errorf("outcome = %v; want succeeded", result["outcome"])
	}
}

// ---------------------------------------------------------------------------
// TestHandleResolveChallenge_ManagerError
// ---------------------------------------------------------------------------

// TestHandleResolveChallenge_ManagerError verifies that manager errors are
// returned as 400.
func TestHandleResolveChallenge_ManagerError(t *testing.T) {
	cm := &stubChallengeManager{
		resolveErr: errors.New("challenge: not found"),
	}
	ts := newChallengeTestServer(t, cm)

	body := `{"outcome":"succeeded"}`
	resp, err := http.Post(ts.URL+"/v1/challenges/does-not-exist/resolve", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST resolve: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TestHandleListChallenges_Success
// ---------------------------------------------------------------------------

// TestHandleListChallenges_Success verifies that GET /v1/challenges/{task_id}
// returns the challenges for the task.
func TestHandleListChallenges_Success(t *testing.T) {
	cm := &stubChallengeManager{
		challenges: []api.ChallengeRecord{
			{
				ID:           "chal-1",
				TaskID:       "task-abc",
				ChallengerID: "challenger-1",
				TargetID:     "validator-1",
				Bond:         500_000,
				Status:       "open",
				CreatedAt:    "2026-01-01T00:00:00Z",
			},
		},
	}
	ts := newChallengeTestServer(t, cm)

	resp, err := http.Get(ts.URL + "/v1/challenges/task-abc")
	if err != nil {
		t.Fatalf("GET /v1/challenges/task-abc: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	var result struct {
		Challenges []api.ChallengeRecord `json:"challenges"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Challenges) != 1 {
		t.Fatalf("challenges count = %d; want 1", len(result.Challenges))
	}
	if result.Challenges[0].ID != "chal-1" {
		t.Errorf("challenge ID = %q; want chal-1", result.Challenges[0].ID)
	}
}

// ---------------------------------------------------------------------------
// TestHandleListChallenges_501_WhenNotConfigured
// ---------------------------------------------------------------------------

// TestHandleListChallenges_501_WhenNotConfigured verifies that GET
// /v1/challenges/{task_id} returns 501 when no manager is wired.
func TestHandleListChallenges_501_WhenNotConfigured(t *testing.T) {
	ts := newChallengeTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/v1/challenges/task-abc")
	if err != nil {
		t.Fatalf("GET /v1/challenges/task-abc: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d; want 501", resp.StatusCode)
	}
}
