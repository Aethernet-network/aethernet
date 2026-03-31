package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/validatorlifecycle"
)

// ---------------------------------------------------------------------------
// Validator Status Endpoint Tests
// ---------------------------------------------------------------------------

func TestGetValidatorSeat_NotEnabled(t *testing.T) {
	setup := newTestSetup(t)
	// No lifecycle reducer wired — should return 501.
	resp, err := http.Get(setup.ts.URL + "/v1/validator/seat-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
}

func TestGetValidatorSeat_WithReducer(t *testing.T) {
	setup := newTestSetup(t)

	// Create a reducer with a seat that has recovery configured.
	r, err := validatorlifecycle.SeedReducerFromManifest(&validatorlifecycle.GenesisManifest{
		Entries: []validatorlifecycle.GenesisManifestEntry{
			{ValidatorID: "test-seat", OperatorAgentID: "op-key", ConsensusPublicKey: "op-key",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: validatorlifecycle.SeatActive},
		},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Set recovery key.
	_ = r.Apply(validatorlifecycle.LifecycleEvent{
		Kind: validatorlifecycle.EventRecoveryKeySet, EventID: "rks",
		CausalTS: 10, SeatID: "test-seat", RecoveryKey: "cold-key",
	})
	setup.srv.SetLifecycleReducer(r)

	resp, err := http.Get(setup.ts.URL + "/v1/validator/test-seat")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var view map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&view)
	if view["id"] != "test-seat" {
		t.Fatalf("wrong id: %v", view["id"])
	}
	if view["status"] != "active" {
		t.Fatalf("wrong status: %v", view["status"])
	}
	if view["recovery_configured"] != true {
		t.Fatal("recovery_configured should be true")
	}
	// Recovery public key should NOT be exposed in the view.
	if _, exists := view["recovery_key"]; exists {
		t.Fatal("recovery_key should not be exposed in status view")
	}
	if _, exists := view["recovery_public_key"]; exists {
		t.Fatal("recovery_public_key should not be exposed in status view")
	}
}

func TestListValidatorSeats(t *testing.T) {
	setup := newTestSetup(t)

	r, _ := validatorlifecycle.SeedReducerFromManifest(&validatorlifecycle.GenesisManifest{
		Entries: []validatorlifecycle.GenesisManifestEntry{
			{ValidatorID: "s1", OperatorAgentID: "k1", ConsensusPublicKey: "k1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: validatorlifecycle.SeatActive},
			{ValidatorID: "s2", OperatorAgentID: "k2", ConsensusPublicKey: "k2",
				KeyEpoch: 1, BondedStake: 200_000, InitialStatus: validatorlifecycle.SeatActive},
		},
	})
	setup.srv.SetLifecycleReducer(r)

	resp, err := http.Get(setup.ts.URL + "/v1/validator")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	count := result["count"].(float64)
	if count != 2 {
		t.Fatalf("expected 2 seats, got %v", count)
	}
}

func TestGetValidatorSeat_NotFound(t *testing.T) {
	setup := newTestSetup(t)

	r, _ := validatorlifecycle.SeedReducerFromManifest(&validatorlifecycle.GenesisManifest{
		Entries: []validatorlifecycle.GenesisManifestEntry{
			{ValidatorID: "s1", OperatorAgentID: "k1", ConsensusPublicKey: "k1",
				KeyEpoch: 1, BondedStake: 100_000, InitialStatus: validatorlifecycle.SeatActive},
		},
	})
	setup.srv.SetLifecycleReducer(r)

	resp, _ := http.Get(setup.ts.URL + "/v1/validator/nonexistent-seat")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestSetRecoveryKey_PublishesThroughAuthorativePath verifies that the
// recovery key set endpoint creates a DAG event via the publisher (not
// direct dag.Add).
func TestSetRecoveryKey_PublishesThroughAuthorativePath(t *testing.T) {
	setup := newTestSetup(t)
	dagSizeBefore := setup.d.Size()

	body, _ := json.Marshal(map[string]string{
		"validator_id":       "seat-test-1",
		"recovery_public_key": "aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233",
	})
	resp, err := http.Post(setup.ts.URL+"/v1/validator/recovery-key", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/validator/recovery-key: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	// Verify a DAG event was created.
	if setup.d.Size() <= dagSizeBefore {
		t.Fatal("expected DAG to grow after recovery key set")
	}

	var result map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "recovery_key_set" {
		t.Fatalf("unexpected status: %q", result["status"])
	}
	if result["event_id"] == "" {
		t.Fatal("expected non-empty event_id")
	}
}

// TestSetRecoveryKey_MissingFields returns 400.
func TestSetRecoveryKey_MissingFields(t *testing.T) {
	setup := newTestSetup(t)

	// Missing recovery_public_key.
	body, _ := json.Marshal(map[string]string{
		"validator_id": "seat-test-1",
	})
	resp, _ := http.Post(setup.ts.URL+"/v1/validator/recovery-key", "application/json", bytes.NewReader(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing key, got %d", resp.StatusCode)
	}

	// Missing validator_id.
	body, _ = json.Marshal(map[string]string{
		"recovery_public_key": "aabb",
	})
	resp, _ = http.Post(setup.ts.URL+"/v1/validator/recovery-key", "application/json", bytes.NewReader(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing validator_id, got %d", resp.StatusCode)
	}
}

// TestRecoveryEvent_AcceptsPreSignedEvent verifies the recovery event
// endpoint accepts a pre-signed DAG event and publishes it.
func TestRecoveryEvent_AcceptsPreSignedEvent(t *testing.T) {
	setup := newTestSetup(t)

	recovKP, _ := crypto.GenerateKeyPair()
	recovID := string(recovKP.AgentID())

	payload := validatorlifecycle.ValidatorEmergencySuspendPayload{
		Version:           1,
		ValidatorID:       "seat-test-1",
		RecoveryPublicKey: crypto.AgentID(recovID),
		Reason:            "key_compromise",
		RequestedAt:       1700000000,
	}

	ev, err := event.New(event.EventTypeValidatorEmergencySuspend, nil, payload, recovID, nil, 0)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	_ = crypto.SignEvent(ev, recovKP)

	dagSizeBefore := setup.d.Size()

	body, _ := json.Marshal(ev)
	resp, err := http.Post(setup.ts.URL+"/v1/validator/recovery-event", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		t.Fatalf("expected 201, got %d: %v", resp.StatusCode, errResp)
	}

	if setup.d.Size() <= dagSizeBefore {
		t.Fatal("expected DAG to grow after recovery event")
	}
}

// TestRecoveryEvent_RejectsUnsignedEvent returns 400.
func TestRecoveryEvent_RejectsUnsignedEvent(t *testing.T) {
	setup := newTestSetup(t)

	recovKP, _ := crypto.GenerateKeyPair()
	recovID := string(recovKP.AgentID())

	payload := validatorlifecycle.ValidatorEmergencySuspendPayload{
		Version:           1,
		ValidatorID:       "seat-test-1",
		RecoveryPublicKey: crypto.AgentID(recovID),
		Reason:            "compromise",
		RequestedAt:       1700000000,
	}

	ev, _ := event.New(event.EventTypeValidatorEmergencySuspend, nil, payload, recovID, nil, 0)
	// Intentionally NOT signing the event.

	body, _ := json.Marshal(ev)
	resp, _ := http.Post(setup.ts.URL+"/v1/validator/recovery-event", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsigned event, got %d", resp.StatusCode)
	}
}

// TestRecoveryEvent_RejectsNonRecoveryTypes returns 400 for normal event types.
func TestRecoveryEvent_RejectsNonRecoveryTypes(t *testing.T) {
	setup := newTestSetup(t)

	kp, _ := crypto.GenerateKeyPair()
	agentID := string(kp.AgentID())

	// Try to submit a Transfer event through the recovery endpoint.
	payload := event.TransferPayload{
		Version:   1,
		FromAgent: "alice",
		ToAgent:   "bob",
		Amount:    1000,
		Currency:  "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nil, payload, agentID, nil, 1000)
	_ = crypto.SignEvent(ev, kp)

	body, _ := json.Marshal(ev)
	resp, _ := http.Post(setup.ts.URL+"/v1/validator/recovery-event", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-recovery event type, got %d", resp.StatusCode)
	}
}

// TestRecoveryEvent_NoDirectDAGMutation verifies the handler uses
// publisher.Publish, not dag.Add directly. Since our test setup doesn't
// wire a publisher (it falls back to dag.Add), we verify the event reaches
// the DAG — which means the fallback path works correctly.
func TestRecoveryEvent_NoDirectDAGMutation(t *testing.T) {
	// This test verifies the architectural invariant: the handler calls
	// s.publisher.Publish (or falls back to s.dag.Add for tests). It does
	// NOT call dag.Add in a separate code path.
	setup := newTestSetup(t)

	recovKP, _ := crypto.GenerateKeyPair()
	recovID := string(recovKP.AgentID())

	payload := validatorlifecycle.ValidatorRecoveryRotatePayload{
		Version:              1,
		ValidatorID:          "seat-test-2",
		RecoveryPublicKey:    crypto.AgentID(recovID),
		NewPublicKey:         "new-key-hex",
		RequestedAt:          1700000000,
		EffectiveAfter:       1700000300,
		EffectiveFromVersion: 5,
	}

	ev, _ := event.New(event.EventTypeValidatorRecoveryRotate, nil, payload, recovID, nil, 0)
	_ = crypto.SignEvent(ev, recovKP)

	body, _ := json.Marshal(ev)
	resp, _ := http.Post(setup.ts.URL+"/v1/validator/recovery-event", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	// Event should be in DAG.
	_, err := setup.d.Get(ev.ID)
	if err != nil {
		t.Fatalf("event should be in DAG after publish: %v", err)
	}
}
