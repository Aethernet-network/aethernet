package integration_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// TestEscrow_HoldsOnTransferOptimistic asserts that the escrow store's
// Empty probe flips from true to false when a hold is placed. Per plan
// §D8, also meta-asserts the projection entry's IntegrationTestRef.
//
// The test uses escrow.Hold directly rather than driving through the
// full settlement applicator because the Hold path is the canonical
// writer the live consumer (Applicator.applyTransferOptimistic) reaches
// — asserting on the probe here is equivalent and avoids the full
// multi-package wiring for a tight one-behavior test.
func TestEscrow_HoldsOnTransferOptimistic(t *testing.T) {
	tl := ledger.NewTransferLedger()
	if err := tl.FundAgent("poster-a", 10_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	e := escrow.New(tl)
	ctx := context.Background()

	empty, err := e.Empty(ctx)
	if err != nil || !empty {
		t.Fatalf("escrow must start empty (got empty=%v, err=%v)", empty, err)
	}

	if err := e.Hold("task-integration-escrow", "poster-a", 1_000); err != nil {
		t.Fatalf("Hold: %v", err)
	}

	empty, err = e.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty after Hold: %v", err)
	}
	if empty {
		t.Fatalf("escrow must not be empty after Hold")
	}

	// Meta-assertion (plan §D8).
	entry := escrow.Projection(e)
	want := "github.com/Aethernet-network/aethernet/internal/integration.TestEscrow_HoldsOnTransferOptimistic"
	if entry.IntegrationTestRef != want {
		t.Fatalf("IntegrationTestRef mismatch:\n  want: %s\n  got:  %s", want, entry.IntegrationTestRef)
	}
}
