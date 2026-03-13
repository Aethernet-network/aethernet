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
// Test-only calibration store implementations
// ---------------------------------------------------------------------------

// staticCalibrationStore returns a fixed slice of signals for any actorID.
type staticCalibrationStore struct {
	signals []*canary.CalibrationSignal
	err     error
}

func (s *staticCalibrationStore) SignalsByActor(_ string) ([]*canary.CalibrationSignal, error) {
	return s.signals, s.err
}

// emptyCalibrationStore returns no signals.
type emptyCalibrationStore struct{}

func (e *emptyCalibrationStore) SignalsByActor(_ string) ([]*canary.CalibrationSignal, error) {
	return nil, nil
}

// errCalibrationStore always returns an error.
type errCalibrationStore struct{}

func (e *errCalibrationStore) SignalsByActor(_ string) ([]*canary.CalibrationSignal, error) {
	return nil, errors.New("db error")
}

// ---------------------------------------------------------------------------
// Test server builder
// ---------------------------------------------------------------------------

// calibrationStoreIface mirrors the private calibrationSource interface from the api package.
type calibrationStoreIface interface {
	SignalsByActor(actorID string) ([]*canary.CalibrationSignal, error)
}

func newCalibrationTestServer(t *testing.T, cs calibrationStoreIface) *httptest.Server {
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
	srv.SetCalibrationStore(cs)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAdminCalibration_NoStore_501 verifies that the endpoint returns 501 when
// no calibration store is wired in.
func TestAdminCalibration_NoStore_501(t *testing.T) {
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
	// SetCalibrationStore NOT called.
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agent-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", resp.StatusCode)
	}
}

// TestAdminCalibration_EmptySignals_200 verifies that an actor with no signals
// returns a 200 with an empty ActorCalibration (zero counts, zero accuracy).
func TestAdminCalibration_EmptySignals_200(t *testing.T) {
	ts := newCalibrationTestServer(t, &emptyCalibrationStore{})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agent-empty")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rollup canary.ActorCalibration
	if err := json.NewDecoder(resp.Body).Decode(&rollup); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rollup.TotalSignals != 0 {
		t.Errorf("expected TotalSignals=0, got %d", rollup.TotalSignals)
	}
	if rollup.Accuracy != 0.0 {
		t.Errorf("expected Accuracy=0.0, got %f", rollup.Accuracy)
	}
}

// TestAdminCalibration_WithSignals_200 verifies that when signals exist the
// rollup reflects the correct counts and accuracy.
func TestAdminCalibration_WithSignals_200(t *testing.T) {
	signals := []*canary.CalibrationSignal{
		{
			ID:           "sig-1",
			CanaryID:     "cnr-1",
			ActorID:      "agent-1",
			ActorRole:    canary.RoleWorker,
			Category:     "code",
			Correctness:  canary.CorrectnessCorrect,
			Severity:     0.0,
			ExpectedPass: true,
			ObservedPass: true,
		},
		{
			ID:           "sig-2",
			CanaryID:     "cnr-2",
			ActorID:      "agent-1",
			ActorRole:    canary.RoleWorker,
			Category:     "code",
			Correctness:  canary.CorrectnessIncorrect,
			Severity:     1.0,
			ExpectedPass: false,
			ObservedPass: true,
		},
	}
	ts := newCalibrationTestServer(t, &staticCalibrationStore{signals: signals})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agent-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rollup canary.ActorCalibration
	if err := json.NewDecoder(resp.Body).Decode(&rollup); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rollup.TotalSignals != 2 {
		t.Errorf("expected TotalSignals=2, got %d", rollup.TotalSignals)
	}
	if rollup.CorrectCount != 1 {
		t.Errorf("expected CorrectCount=1, got %d", rollup.CorrectCount)
	}
	if rollup.IncorrectCount != 1 {
		t.Errorf("expected IncorrectCount=1, got %d", rollup.IncorrectCount)
	}
	// Accuracy = 1/2 = 0.5
	if rollup.Accuracy != 0.5 {
		t.Errorf("expected Accuracy=0.5, got %f", rollup.Accuracy)
	}
}

// TestAdminCalibration_StoreError_500 verifies that a store error returns 500.
func TestAdminCalibration_StoreError_500(t *testing.T) {
	ts := newCalibrationTestServer(t, &errCalibrationStore{})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agent-x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// newCalibrationAuthServer returns a test server with requireAuth=true and a
// wired platform.KeyManager. It also returns a valid API key for use in
// authenticated requests.
func newCalibrationAuthServer(t *testing.T, cs calibrationStoreIface) (*httptest.Server, string) {
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
	srv.SetCalibrationStore(cs)
	srv.SetPlatformKeys(km)
	// requireAuth defaults to true — no need to call SetRequireAuth.
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, apiKey
}

// TestAdminCalibration_Auth_Unauthenticated_401 verifies that when requireAuth
// is enabled and platformKeys is configured, an unauthenticated request to
// GET /v1/admin/calibration/{actor_id} returns 401.
func TestAdminCalibration_Auth_Unauthenticated_401(t *testing.T) {
	ts, _ := newCalibrationAuthServer(t, &emptyCalibrationStore{})

	resp, err := http.Get(ts.URL + "/v1/admin/calibration/agent-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", resp.StatusCode)
	}
}

// TestAdminCalibration_Auth_Authenticated_200 verifies that when requireAuth
// is enabled and a valid X-API-Key is provided, the endpoint returns 200.
func TestAdminCalibration_Auth_Authenticated_200(t *testing.T) {
	ts, apiKey := newCalibrationAuthServer(t, &emptyCalibrationStore{})

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/admin/calibration/agent-1", nil)
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
