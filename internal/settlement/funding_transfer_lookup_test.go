package settlement

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

type stubScanner struct {
	events []*event.Event
}

func (s *stubScanner) All() []*event.Event { return s.events }

func makeTransfer(t *testing.T, poster crypto.AgentID, taskID string, amount uint64, reason string) *event.Event {
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
			Reason:    reason,
			TaskID:    taskID,
		},
		string(poster),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	return e
}

func TestLookupEscrowLockTransfer_FindsMatchingTransfer(t *testing.T) {
	want := makeTransfer(t, "poster", "task1", 100, "escrow-lock")
	scanner := &stubScanner{events: []*event.Event{
		makeTransfer(t, "other", "task2", 50, "escrow-lock"),
		want,
		makeTransfer(t, "poster", "task1", 100, "transfer"),
	}}

	id, err := LookupEscrowLockTransfer(scanner, "task1", crypto.AgentID("poster"), 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != want.ID {
		t.Errorf("got %s want %s", id, want.ID)
	}
}

func TestLookupEscrowLockTransfer_ReturnsErrorOnMismatch(t *testing.T) {
	scanner := &stubScanner{events: []*event.Event{
		makeTransfer(t, "poster", "task1", 999, "escrow-lock"), // wrong amount
		makeTransfer(t, "poster", "task1", 100, "transfer"),    // wrong reason
	}}

	_, err := LookupEscrowLockTransfer(scanner, "task1", crypto.AgentID("poster"), 100)
	if !errors.Is(err, ErrFundingTransferNotFound) {
		t.Fatalf("want ErrFundingTransferNotFound, got %v", err)
	}
}

func TestLookupEscrowLockTransfer_EmptyDAG(t *testing.T) {
	scanner := &stubScanner{events: nil}
	_, err := LookupEscrowLockTransfer(scanner, "task1", crypto.AgentID("poster"), 100)
	if !errors.Is(err, ErrFundingTransferNotFound) {
		t.Fatalf("want ErrFundingTransferNotFound, got %v", err)
	}
}
