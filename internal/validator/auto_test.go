package validator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/validator"
)

// TestAutoValidator_ProcessesPending verifies that AutoValidator polls the OCS
// engine and settles pending items within one tick interval.
func TestAutoValidator_ProcessesPending(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer eng.Stop()

	// Fund the sender so the OCS balance check passes.
	senderID := crypto.AgentID("alice")
	if err := tl.FundAgent(senderID, 100_000); err != nil {
		t.Fatalf("fund sender: %v", err)
	}

	// Build and submit a Transfer event. The OCS engine does not require a
	// cryptographic signature — that is enforced by the API server layer.
	payload := event.TransferPayload{
		FromAgent: "alice",
		ToAgent:   "bob",
		Amount:    1_000,
		Currency:  "AET",
	}
	ev, err := event.New(event.EventTypeTransfer, nil, payload, "alice", nil, 1_000)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := eng.Submit(ev); err != nil {
		t.Fatalf("submit event: %v", err)
	}

	if count := eng.PendingCount(); count != 1 {
		t.Fatalf("expected 1 pending item before auto-validation, got %d", count)
	}

	// The auto-validator's ID must be different from alice and bob to pass the
	// OCS anti-self-dealing guard.
	validatorID := crypto.AgentID("testnet-validator")
	av := validator.NewAutoValidator(eng, validatorID, 50*time.Millisecond)
	av.Start()
	defer av.Stop()

	// Wait up to 2 s for the pending count to reach 0.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eng.PendingCount() == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if count := eng.PendingCount(); count != 0 {
		t.Fatalf("expected 0 pending items after auto-validation, got %d", count)
	}
}

// TestAutoValidator_StopIsIdempotent verifies that calling Stop multiple times
// does not panic (uses sync.Once internally).
func TestAutoValidator_StopIsIdempotent(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer eng.Stop()

	av := validator.NewAutoValidator(eng, "testnet-validator", time.Second)
	av.Start()
	av.Stop()
	av.Stop() // must not panic
}
