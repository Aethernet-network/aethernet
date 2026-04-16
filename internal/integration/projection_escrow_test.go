package integration_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/escrow_testhelpers"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// TestEscrow_HoldsOnTransferOptimistic asserts that the escrow store's
// Empty probe flips from true to false when an entry is registered.
// Per plan §D8, also meta-asserts the projection entry's IntegrationTestRef.
//
// Test name and import path are preserved verbatim because escrow.Projection()
// declares this symbol as its IntegrationTestRef; the projection-registry lint
// would fail the build otherwise. See
// docs/plans/2026-04-15-f3b-part-e-escrow-hardening.md §6.
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

	if err := escrow_testhelpers.FundAndRegisterEscrowForTest(
		tl, e, "task-integration-escrow", crypto.AgentID("poster-a"), 1_000,
	); err != nil {
		t.Fatalf("FundAndRegisterEscrowForTest: %v", err)
	}

	empty, err = e.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty after register: %v", err)
	}
	if empty {
		t.Fatalf("escrow must not be empty after registration")
	}

	// Meta-assertion (plan §D8).
	entry := escrow.Projection(e)
	want := "github.com/Aethernet-network/aethernet/internal/integration.TestEscrow_HoldsOnTransferOptimistic"
	if entry.IntegrationTestRef != want {
		t.Fatalf("IntegrationTestRef mismatch:\n  want: %s\n  got:  %s", want, entry.IntegrationTestRef)
	}
}
