package sdk_test

import (
	"net/http/httptest"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/api"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/pkg/sdk"
)

// newTestClient spins up an in-memory node stack and returns an SDK client
// pointed at its httptest server, plus the TransferLedger for test funding.
func newTestClient(t *testing.T) (*sdk.Client, *ledger.TransferLedger, crypto.AgentID) {
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

	return sdk.New(ts.URL, nil), tl, kp.AgentID()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestClient_Register(t *testing.T) {
	client, _, agentID := newTestClient(t)

	id, err := client.Register(nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if id != string(agentID) {
		t.Errorf("want agent_id %q, got %q", agentID, id)
	}
}

func TestClient_Register_Idempotent(t *testing.T) {
	client, _, _ := newTestClient(t)

	id1, err := client.Register(nil)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	id2, err := client.Register(nil)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if id1 != id2 {
		t.Errorf("idempotent register should return same id: %q vs %q", id1, id2)
	}
}

func TestClient_Status(t *testing.T) {
	client, _, agentID := newTestClient(t)

	status, err := client.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.AgentID != string(agentID) {
		t.Errorf("agent_id mismatch: got %q", status.AgentID)
	}
	if status.SupplyRatio < 1.0 {
		t.Errorf("supply_ratio should be >= 1.0, got %f", status.SupplyRatio)
	}
}

func TestClient_Tips_Empty(t *testing.T) {
	client, _, _ := newTestClient(t)

	tips, err := client.Tips()
	if err != nil {
		t.Fatalf("Tips: %v", err)
	}
	if len(tips.Tips) != 0 {
		t.Errorf("want empty tips, got %v", tips.Tips)
	}
}

func TestClient_Generate(t *testing.T) {
	client, _, _ := newTestClient(t)

	eventID, err := client.Generate(sdk.GenerationRequest{
		ClaimedValue:    5000,
		EvidenceHash:    "sha256:abc123",
		TaskDescription: "SDK test run",
		StakeAmount:     1000,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if eventID == "" {
		t.Error("event_id must not be empty")
	}
}

func TestClient_GetEvent(t *testing.T) {
	client, _, _ := newTestClient(t)

	eventID, err := client.Generate(sdk.GenerationRequest{
		ClaimedValue: 1000,
		EvidenceHash: "sha256:test",
		StakeAmount:  1000,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	ev, err := client.GetEvent(eventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if ev.ID != eventID {
		t.Errorf("id mismatch: got %q, want %q", ev.ID, eventID)
	}
	if ev.Type != "Generation" {
		t.Errorf("type: got %q, want Generation", ev.Type)
	}
}

func TestClient_Balance(t *testing.T) {
	client, _, agentID := newTestClient(t)

	bal, err := client.Balance(string(agentID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.Balance != 0 {
		t.Errorf("want 0 balance, got %d", bal.Balance)
	}
	if bal.Currency != "AET" {
		t.Errorf("want AET, got %q", bal.Currency)
	}
}

func TestClient_Transfer(t *testing.T) {
	client, tl, agentID := newTestClient(t)

	if err := tl.FundAgent(agentID, 1_000_000); err != nil {
		t.Fatalf("fund agent: %v", err)
	}

	eventID, err := client.Transfer(sdk.TransferRequest{
		ToAgent:     string(agentID),
		Amount:      500,
		Currency:    "AET",
		Memo:        "sdk test transfer",
		StakeAmount: 1000,
	})
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if eventID == "" {
		t.Error("event_id must not be empty")
	}
}

func TestClient_Transfer_InsufficientFunds(t *testing.T) {
	client, _, agentID := newTestClient(t)

	_, err := client.Transfer(sdk.TransferRequest{
		ToAgent:     string(agentID),
		Amount:      1000,
		StakeAmount: 1000,
	})
	if err == nil {
		t.Error("expected error for insufficient funds, got nil")
	}
}

func TestClient_Profile(t *testing.T) {
	client, _, agentID := newTestClient(t)

	// Register the agent first.
	if _, err := client.Register(nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	profile, err := client.Profile(string(agentID))
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if profile.AgentID != string(agentID) {
		t.Errorf("agent_id mismatch: got %q", profile.AgentID)
	}
}
