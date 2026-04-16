package escrow

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// stubDAGReader is an in-test DAGReader used by RegisterEscrow tests.
// Returns ErrEventNotFound for any ID not explicitly seeded.
type stubDAGReader struct {
	events map[event.EventID]*event.Event
}

func (s *stubDAGReader) Get(id event.EventID) (*event.Event, error) {
	if e, ok := s.events[id]; ok {
		return e, nil
	}
	return nil, errors.New("stub-dag: not found")
}

func newStubDAG() *stubDAGReader {
	return &stubDAGReader{events: make(map[event.EventID]*event.Event)}
}

func makeEscrowLockTransfer(t *testing.T, poster crypto.AgentID, taskID string, amount uint64) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeTransfer,
		nil,
		event.TransferPayload{
			Version:   1,
			FromAgent: string(poster),
			ToAgent:   "escrow:" + taskID,
			Amount:    amount,
			Currency:  "AET",
			Reason:    "escrow-lock",
			TaskID:    taskID,
		},
		string(poster),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct transfer event: %v", err)
	}
	return e
}

func newEscrowWithDAG(tl *ledger.TransferLedger, r DAGReader) *Escrow {
	e := New(tl)
	e.SetDAGReader(r)
	return e
}

func TestRegisterEscrow_WithoutDAGReader(t *testing.T) {
	tl := ledger.NewTransferLedger()
	_ = tl.FundAgent("poster", 1000)
	e := New(tl)
	err := e.RegisterEscrow("task1", "poster", 100, event.EventID("evt-1"))
	if !errors.Is(err, ErrDAGReaderNotConfigured) {
		t.Fatalf("want ErrDAGReaderNotConfigured, got %v", err)
	}
}

func TestRegisterEscrow_RejectsMissingFundingRef(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	e := newEscrowWithDAG(tl, dag)

	err := e.RegisterEscrow("task1", "poster", 100, event.EventID("missing"))
	if !errors.Is(err, ErrFundingTransferNotProjected) {
		t.Fatalf("want ErrFundingTransferNotProjected, got %v", err)
	}
}

func TestRegisterEscrow_RejectsWrongEventType(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	// Seed a non-Transfer event.
	wrong, err := event.New(event.EventTypeRegistration, nil, nil, "agent", nil, 0)
	if err != nil {
		t.Fatalf("make event: %v", err)
	}
	dag.events[wrong.ID] = wrong

	e := newEscrowWithDAG(tl, dag)
	err = e.RegisterEscrow("task1", "poster", 100, wrong.ID)
	if !errors.Is(err, ErrFundingTransferWrongType) {
		t.Fatalf("want ErrFundingTransferWrongType, got %v", err)
	}
}

func TestRegisterEscrow_RejectsMismatchedAmount(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	evt := makeEscrowLockTransfer(t, "poster", "task1", 100)
	dag.events[evt.ID] = evt

	e := newEscrowWithDAG(tl, dag)
	err := e.RegisterEscrow("task1", "poster", 999, evt.ID)
	if !errors.Is(err, ErrFundingTransferMismatch) {
		t.Fatalf("want ErrFundingTransferMismatch, got %v", err)
	}
}

func TestRegisterEscrow_RejectsMismatchedPoster(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	evt := makeEscrowLockTransfer(t, "other-poster", "task1", 100)
	dag.events[evt.ID] = evt

	e := newEscrowWithDAG(tl, dag)
	err := e.RegisterEscrow("task1", "poster", 100, evt.ID)
	if !errors.Is(err, ErrFundingTransferMismatch) {
		t.Fatalf("want ErrFundingTransferMismatch, got %v", err)
	}
}

func TestRegisterEscrow_RejectsMismatchedTaskID(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	evt := makeEscrowLockTransfer(t, "poster", "wrong-task", 100)
	dag.events[evt.ID] = evt

	e := newEscrowWithDAG(tl, dag)
	err := e.RegisterEscrow("task1", "poster", 100, evt.ID)
	if !errors.Is(err, ErrFundingTransferMismatch) {
		t.Fatalf("want ErrFundingTransferMismatch, got %v", err)
	}
}

func TestRegisterEscrow_RejectsMismatchedReason(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	// Build a Transfer with the WRONG Reason.
	evt, err := event.New(
		event.EventTypeTransfer,
		nil,
		event.TransferPayload{
			Version:   1,
			FromAgent: "poster",
			ToAgent:   "escrow:task1",
			Amount:    100,
			Currency:  "AET",
			Reason:    "stake-lock",
			TaskID:    "task1",
		},
		"poster",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct event: %v", err)
	}
	dag.events[evt.ID] = evt

	e := newEscrowWithDAG(tl, dag)
	err = e.RegisterEscrow("task1", "poster", 100, evt.ID)
	if !errors.Is(err, ErrFundingTransferMismatch) {
		t.Fatalf("want ErrFundingTransferMismatch, got %v", err)
	}
}

func TestRegisterEscrow_HappyPath(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	evt := makeEscrowLockTransfer(t, "poster", "task1", 100)
	dag.events[evt.ID] = evt

	e := newEscrowWithDAG(tl, dag)
	if err := e.RegisterEscrow("task1", "poster", 100, evt.ID); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got, err := e.Get("task1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Amount != 100 {
		t.Errorf("amount: got %d want 100", got.Amount)
	}
	if got.FundingTransferRef != evt.ID {
		t.Errorf("FundingTransferRef: got %s want %s", got.FundingTransferRef, evt.ID)
	}
}

func TestRegisterEscrow_Idempotent(t *testing.T) {
	tl := ledger.NewTransferLedger()
	dag := newStubDAG()
	evt := makeEscrowLockTransfer(t, "poster", "task1", 100)
	dag.events[evt.ID] = evt

	e := newEscrowWithDAG(tl, dag)
	if err := e.RegisterEscrow("task1", "poster", 100, evt.ID); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := e.RegisterEscrow("task1", "poster", 100, evt.ID); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if e.TotalEscrowed() != 100 {
		t.Errorf("TotalEscrowed after idempotent: got %d want 100", e.TotalEscrowed())
	}
}

func TestRegisterEscrow_DoesNotMoveFunds(t *testing.T) {
	tl := ledger.NewTransferLedger()
	_ = tl.FundAgent("poster", 1000)
	dag := newStubDAG()
	evt := makeEscrowLockTransfer(t, "poster", "task1", 100)
	dag.events[evt.ID] = evt

	e := newEscrowWithDAG(tl, dag)
	before, _ := tl.Balance("poster")
	if err := e.RegisterEscrow("task1", "poster", 100, evt.ID); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	after, _ := tl.Balance("poster")
	if before != after {
		t.Errorf("RegisterEscrow moved funds: before=%d after=%d", before, after)
	}
	if bal, _ := tl.Balance(crypto.AgentID("escrow:task1")); bal != 0 {
		t.Errorf("escrow bucket has funds: %d", bal)
	}
}
