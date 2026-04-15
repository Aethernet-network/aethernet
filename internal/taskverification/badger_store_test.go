package taskverification

import (
	"context"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// Empty probe contract for the projection registry.
// See docs/plans/2026-04-12-reputation-step-2-retrofit-projections.md §D7.
func TestBadgerStore_Empty(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewBadgerStore(db)
	ctx := context.Background()

	empty, err := s.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty on fresh store: %v", err)
	}
	if !empty {
		t.Fatalf("fresh BadgerStore must be empty")
	}

	round := &TaskVerificationRound{
		RoundID:           RoundID("r-empty-test"),
		TaskID:            "task-x",
		SubmissionEventID: event.EventID("sub-x"),
		Category:          "research",
		State:             RoundStateOpen,
		DiversityFloor:    2,
	}
	if err := s.SaveRound(ctx, round); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	empty, err = s.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty after SaveRound: %v", err)
	}
	if empty {
		t.Fatalf("BadgerStore must not be empty after SaveRound")
	}
}
