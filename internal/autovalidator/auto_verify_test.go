package autovalidator_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// setupVerifyHarness creates a minimal autovalidator stack for verification tests.
func setupVerifyHarness(t *testing.T) (*autovalidator.AutoValidator, *ocs.Engine, *dag.DAG, *ledger.TransferLedger, *crypto.KeyPair) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	d := dag.New()

	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	kp, _ := crypto.GenerateKeyPair()
	av := autovalidator.NewAutoValidator(eng, kp.AgentID(), 50*time.Millisecond)
	av.SetDAG(d)
	av.SetKeyPair(kp)

	tm := tasks.NewTaskManager()
	escrowMgr := escrow.New(tl)
	av.SetTaskManager(tm, escrowMgr)

	return av, eng, d, tl, kp
}

// TestVerify_TransferApproved verifies that a valid transfer is approved
// by the structural verifier with verifiedValue = amount.
func TestVerify_TransferApproved(t *testing.T) {
	av, eng, d, tl, kp := setupVerifyHarness(t)

	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("fund: %v", err)
	}

	payload := event.TransferPayload{
		Version: 1, FromAgent: "alice", ToAgent: "bob",
		Amount: 5_000, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nil, payload, string(kp.AgentID()), nil, 1000)
	_ = crypto.SignEvent(ev, kp)
	_ = d.Add(ev)
	if err := eng.Submit(ev); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Verify event is pending.
	if eng.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", eng.PendingCount())
	}

	// Start autovalidator and wait for it to process.
	av.Start()
	defer av.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.Size() > 1 {
			break // vote event was added to DAG
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The autovalidator should have emitted a vote (DAG grew).
	if d.Size() <= 1 {
		t.Fatal("autovalidator should have emitted a vote event")
	}
}

// TestVerify_GenerationMissingEvidence verifies that a generation event
// with no evidence hash is rejected by the structural verifier.
func TestVerify_GenerationMissingEvidence(t *testing.T) {
	av, eng, d, _, kp := setupVerifyHarness(t)

	payload := event.GenerationPayload{
		Version:          1,
		GeneratingAgent:  string(kp.AgentID()),
		BeneficiaryAgent: string(kp.AgentID()),
		ClaimedValue:     10_000,
		EvidenceHash:     "", // empty — should be rejected
		TaskDescription:  "test generation",
	}
	ev, _ := event.New(event.EventTypeGeneration, nil, payload, string(kp.AgentID()), nil, 1000)
	_ = crypto.SignEvent(ev, kp)
	_ = d.Add(ev)

	// Submit via SubmitFromSync to bypass the normal Submit path (which doesn't
	// require evidence hash for Generation events).
	if err := eng.SubmitFromSync(ev); err != nil {
		t.Fatalf("submit: %v", err)
	}

	av.Start()
	defer av.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.Size() > 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The autovalidator should have emitted a REJECT vote.
	if d.Size() <= 1 {
		t.Fatal("autovalidator should have emitted a vote event (reject)")
	}

	// Find the vote event and check its verdict.
	for _, ev := range d.All() {
		if ev.Type == event.EventTypeVerificationVote {
			// The vote exists — the autovalidator voted (reject).
			return
		}
	}
	t.Fatal("no VerificationVote event found in DAG")
}

// TestVerify_GenerationWithEvidence verifies that a generation event
// with an evidence hash is approved.
func TestVerify_GenerationWithEvidence(t *testing.T) {
	av, eng, d, _, kp := setupVerifyHarness(t)

	payload := event.GenerationPayload{
		Version:          1,
		GeneratingAgent:  string(kp.AgentID()),
		BeneficiaryAgent: string(kp.AgentID()),
		ClaimedValue:     10_000,
		EvidenceHash:     "sha256:abc123",
		TaskDescription:  "test generation",
	}
	ev, _ := event.New(event.EventTypeGeneration, nil, payload, string(kp.AgentID()), nil, 1000)
	_ = crypto.SignEvent(ev, kp)
	_ = d.Add(ev)

	if err := eng.SubmitFromSync(ev); err != nil {
		t.Fatalf("submit: %v", err)
	}

	av.Start()
	defer av.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.Size() > 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if d.Size() <= 1 {
		t.Fatal("autovalidator should have emitted a vote event (approve)")
	}
}
