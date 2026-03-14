package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/canary"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/platform"
)

// ---------------------------------------------------------------------------
// Stub all-signals stores
// ---------------------------------------------------------------------------

// allSignalsStub implements calibrationAgentsSource for testing.
type allSignalsStub struct {
	signals []*canary.CalibrationSignal
	err     error
}

func (s *allSignalsStub) AllSignals() ([]*canary.CalibrationSignal, error) {
	return s.signals, s.err
}

// errAllSignalsStub always returns an error from AllSignals.
type errAllSignalsStub struct{}

func (e *errAllSignalsStub) AllSignals() ([]*canary.CalibrationSignal, error) {
	return nil, errors.New("store error")
}

// calibrationAgentsSourceIface mirrors the private calibrationAgentsSource
// interface from the api package.
type calibrationAgentsSourceIface interface {
	AllSignals() ([]*canary.CalibrationSignal, error)
}

// ---------------------------------------------------------------------------
// Test server builders
// ---------------------------------------------------------------------------

func newAgentsTestServer(t *testing.T, cs calibrationAgentsSourceIface) *httptest.Server {
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
	srv.SetCalibrationAgentsStore(cs)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func newAgentsAuthTestServer(t *testing.T, cs calibrationAgentsSourceIface) (*httptest.Server, string) {
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

	km := platform.NewKeyManager()
	apiKey := km.GenerateKey("test", "test@example.com", platform.TierFree).Key

	srv := api.NewServer("", d, tl, gl, reg, eng, sm, nil, kp)
	srv.SetCalibrationAgentsStore(cs)
	srv.SetPlatformKeys(km)
	// requireAuth defaults to true.
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, apiKey
}

// ---------------------------------------------------------------------------
// Response types (mirrors private api structs for JSON decoding in tests)
// ---------------------------------------------------------------------------

type agentsTestResponse struct {
	Agents []struct {
		ActorID      string  `json:"actor_id"`
		TotalSignals int     `json:"total_signals"`
		Accuracy     float64 `json:"accuracy"`
		Bucket       string  `json:"bucket"`
	} `json:"agents"`
	Total int `json:"total"`
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAdminCalibrationAgents_NoStore_501 verifies that the endpoint returns
// 501 when no calibration agents store is configured.
func TestAdminCalibrationAgents_NoStore_501(t *testing.T) {
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
	// SetCalibrationAgentsStore NOT called.
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", resp.StatusCode)
	}
}

// TestAdminCalibrationAgents_ReturnsActorList verifies that when signals for
// multiple actors exist, the response contains one entry per actor with
// correct counts and bucket classifications.
func TestAdminCalibrationAgents_ReturnsActorList(t *testing.T) {
	signals := []*canary.CalibrationSignal{
		// actor-1: 2 correct → accuracy 1.0 → "strong" (but only 2 signals → "unknown")
		{ID: "s1", ActorID: "actor-1", Category: "code",
			Correctness: canary.CorrectnessCorrect},
		{ID: "s2", ActorID: "actor-1", Category: "code",
			Correctness: canary.CorrectnessCorrect},
		// actor-2: 1 incorrect → accuracy 0.0 → "weak" (but only 1 signal → "unknown")
		{ID: "s3", ActorID: "actor-2", Category: "research",
			Correctness: canary.CorrectnessIncorrect},
	}
	ts := newAgentsTestServer(t, &allSignalsStub{signals: signals})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result agentsTestResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if result.Total != 2 {
		t.Errorf("Total = %d; want 2", result.Total)
	}
	if len(result.Agents) != 2 {
		t.Fatalf("len(Agents) = %d; want 2", len(result.Agents))
	}

	// Response is sorted by actor_id — actor-1 first, actor-2 second.
	a1 := result.Agents[0]
	if a1.ActorID != "actor-1" {
		t.Errorf("Agents[0].ActorID = %q; want %q", a1.ActorID, "actor-1")
	}
	if a1.TotalSignals != 2 {
		t.Errorf("actor-1 TotalSignals = %d; want 2", a1.TotalSignals)
	}
	// 2 signals < MinCalibrationSamples (20) → bucket "unknown".
	if a1.Bucket != "unknown" {
		t.Errorf("actor-1 Bucket = %q; want %q", a1.Bucket, "unknown")
	}

	a2 := result.Agents[1]
	if a2.ActorID != "actor-2" {
		t.Errorf("Agents[1].ActorID = %q; want %q", a2.ActorID, "actor-2")
	}
	if a2.TotalSignals != 1 {
		t.Errorf("actor-2 TotalSignals = %d; want 1", a2.TotalSignals)
	}
	if a2.Bucket != "unknown" {
		t.Errorf("actor-2 Bucket = %q; want %q", a2.Bucket, "unknown")
	}
}

// TestAdminCalibrationAgents_EmptyStore_200 verifies that when no signals
// exist, the response is a 200 with an empty agents list.
func TestAdminCalibrationAgents_EmptyStore_200(t *testing.T) {
	ts := newAgentsTestServer(t, &allSignalsStub{signals: nil})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result agentsTestResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("Total = %d; want 0", result.Total)
	}
	if len(result.Agents) != 0 {
		t.Errorf("len(Agents) = %d; want 0", len(result.Agents))
	}
}

// TestAdminCalibrationAgents_StoreError_500 verifies that a store error
// produces a 500 response.
func TestAdminCalibrationAgents_StoreError_500(t *testing.T) {
	ts := newAgentsTestServer(t, &errAllSignalsStub{})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// TestAdminCalibrationAgents_RequiresAuth verifies that when requireAuth is
// enabled and platformKeys is configured, an unauthenticated request returns
// 401.
func TestAdminCalibrationAgents_RequiresAuth(t *testing.T) {
	ts, _ := newAgentsAuthTestServer(t, &allSignalsStub{signals: nil})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", resp.StatusCode)
	}
}

// TestAdminCalibrationAgents_Auth_Authenticated_200 verifies that a valid
// X-API-Key produces a 200 response when auth is required.
func TestAdminCalibrationAgents_Auth_Authenticated_200(t *testing.T) {
	ts, apiKey := newAgentsAuthTestServer(t, &allSignalsStub{signals: nil})

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/admin/calibration/agents", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-API-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for authenticated request, got %d", resp.StatusCode)
	}
}
