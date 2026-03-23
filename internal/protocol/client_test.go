package protocol_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/protocol"
)

func newTestClient(t *testing.T) (*protocol.Client, *dag.DAG, *ocs.Engine, *ledger.TransferLedger) {
	t.Helper()
	d := dag.New()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	kp, _ := crypto.GenerateKeyPair()
	agentID := kp.AgentID()

	// Fund the agent so balance checks pass.
	if err := tl.FundAgent(agentID, 1_000_000); err != nil {
		t.Fatalf("fund agent: %v", err)
	}

	return protocol.NewClient(d, kp, eng, agentID), d, eng, tl
}

func TestSubmitTransfer_CreatesDAGEvent(t *testing.T) {
	client, d, _, tl := newTestClient(t)
	tl.FundAgent("alice", 1_000_000)

	id, err := client.SubmitTransfer("alice", "bob", 500, "transfer", "")
	if err != nil {
		t.Fatalf("SubmitTransfer: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty event ID")
	}

	ev, err := d.Get(id)
	if err != nil {
		t.Fatalf("DAG Get: %v", err)
	}
	if ev.Type != event.EventTypeTransfer {
		t.Errorf("event type = %q; want Transfer", ev.Type)
	}

	tp, err := event.GetPayload[event.TransferPayload](ev)
	if err != nil {
		t.Fatalf("GetPayload: %v", err)
	}
	if tp.FromAgent != "alice" {
		t.Errorf("FromAgent = %q; want alice", tp.FromAgent)
	}
	if tp.ToAgent != "bob" {
		t.Errorf("ToAgent = %q; want bob", tp.ToAgent)
	}
	if tp.Amount != 500 {
		t.Errorf("Amount = %d; want 500", tp.Amount)
	}
	if tp.Reason != "transfer" {
		t.Errorf("Reason = %q; want transfer", tp.Reason)
	}
}

func TestSubmitTransfer_SetsReasonAndTaskID(t *testing.T) {
	client, d, _, tl := newTestClient(t)
	tl.FundAgent("escrow:task-1", 1_000_000)

	id, err := client.SubmitTransfer("escrow:task-1", "worker", 1000, "escrow-release", "task-1")
	if err != nil {
		t.Fatalf("SubmitTransfer: %v", err)
	}

	ev, err := d.Get(id)
	if err != nil {
		t.Fatalf("DAG Get: %v", err)
	}

	tp, err := event.GetPayload[event.TransferPayload](ev)
	if err != nil {
		t.Fatalf("GetPayload: %v", err)
	}
	if tp.Reason != "escrow-release" {
		t.Errorf("Reason = %q; want escrow-release", tp.Reason)
	}
	if tp.TaskID != "task-1" {
		t.Errorf("TaskID = %q; want task-1", tp.TaskID)
	}
}

func TestSubmitEscrowLock(t *testing.T) {
	client, d, eng, tl := newTestClient(t)
	tl.FundAgent("poster-1", 1_000_000)

	id, err := client.SubmitEscrowLock("poster-1", "task-42", 5000)
	if err != nil {
		t.Fatalf("SubmitEscrowLock: %v", err)
	}

	ev, _ := d.Get(id)
	tp, _ := event.GetPayload[event.TransferPayload](ev)

	if tp.FromAgent != "poster-1" {
		t.Errorf("FromAgent = %q; want poster-1", tp.FromAgent)
	}
	if tp.ToAgent != "escrow:task-42" {
		t.Errorf("ToAgent = %q; want escrow:task-42", tp.ToAgent)
	}
	if tp.Reason != "escrow-lock" {
		t.Errorf("Reason = %q; want escrow-lock", tp.Reason)
	}
	if tp.TaskID != "task-42" {
		t.Errorf("TaskID = %q; want task-42", tp.TaskID)
	}

	// Event should be in OCS pending.
	if !eng.IsPending(id) {
		t.Error("event should be in OCS pending after SubmitEscrowLock")
	}
}

func TestSubmitRefund(t *testing.T) {
	client, d, _, tl := newTestClient(t)
	tl.FundAgent("escrow:task-99", 1_000_000)

	id, err := client.SubmitRefund("task-99", "poster-x", 3000)
	if err != nil {
		t.Fatalf("SubmitRefund: %v", err)
	}

	ev, _ := d.Get(id)
	tp, _ := event.GetPayload[event.TransferPayload](ev)

	if tp.FromAgent != "escrow:task-99" {
		t.Errorf("FromAgent = %q; want escrow:task-99", tp.FromAgent)
	}
	if tp.Reason != "task-refund" {
		t.Errorf("Reason = %q; want task-refund", tp.Reason)
	}
}

func TestSubmitGrant(t *testing.T) {
	client, d, _, tl := newTestClient(t)
	tl.FundAgent("genesis:ecosystem", 1_000_000)

	id, err := client.SubmitGrant("genesis:ecosystem", "new-agent", 10000, "onboarding-grant")
	if err != nil {
		t.Fatalf("SubmitGrant: %v", err)
	}

	ev, _ := d.Get(id)
	tp, _ := event.GetPayload[event.TransferPayload](ev)

	if tp.FromAgent != "genesis:ecosystem" {
		t.Errorf("FromAgent = %q; want genesis:ecosystem", tp.FromAgent)
	}
	if tp.Reason != "onboarding-grant" {
		t.Errorf("Reason = %q; want onboarding-grant", tp.Reason)
	}
	if tp.TaskID != "" {
		t.Errorf("TaskID = %q; want empty (no task)", tp.TaskID)
	}
}
