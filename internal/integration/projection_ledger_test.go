package integration_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// TestTransferLedger_AccumulatesOnSettlement asserts that the transfer
// ledger's Empty probe flips from true to false on the first recorded
// transfer. Per plan §D8, also meta-asserts the projection entry's
// IntegrationTestRef.
func TestTransferLedger_AccumulatesOnSettlement(t *testing.T) {
	tl := ledger.NewTransferLedger()
	ctx := context.Background()

	empty, err := tl.Empty(ctx)
	if err != nil || !empty {
		t.Fatalf("ledger must start empty (got empty=%v, err=%v)", empty, err)
	}

	// FundAgent is the canonical minting path and the simplest way to
	// drive a recorded entry into the ledger without the full event /
	// applicator pipeline. The live consumer (Applicator) ultimately
	// calls Record / Settle, which mutate the same entries map.
	if err := tl.FundAgent("agent-a", 1_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	empty, err = tl.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty after FundAgent: %v", err)
	}
	if empty {
		t.Fatalf("ledger must not be empty after FundAgent")
	}

	// Meta-assertion (plan §D8).
	entry := ledger.TransferLedgerProjection(tl)
	want := "github.com/Aethernet-network/aethernet/internal/integration.TestTransferLedger_AccumulatesOnSettlement"
	if entry.IntegrationTestRef != want {
		t.Fatalf("IntegrationTestRef mismatch:\n  want: %s\n  got:  %s", want, entry.IntegrationTestRef)
	}
}
