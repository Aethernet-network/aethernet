package integration_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
)

// TestOCSPending_AccumulatesOnOptimistic asserts that the OCS engine's
// PendingEmpty probe flips from true to false when an Optimistic Transfer
// event is submitted — the canonical live-consumer path for OCS pending.
// Per plan §D8, also meta-asserts the projection entry's IntegrationTestRef.
func TestOCSPending_AccumulatesOnOptimistic(t *testing.T) {
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	const fromAgent = "poster-ocs"
	const toAgent = "worker-ocs"
	if err := tl.FundAgent(fromAgent, 10_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	cfg := ocs.DefaultConfig()
	cfg.MinStakeRequired = 0 // no stake gate for the test
	eng := ocs.NewEngine(cfg, tl, gl, reg)

	ctx := context.Background()
	empty, err := eng.PendingEmpty(ctx)
	if err != nil || !empty {
		t.Fatalf("engine must start with empty pending (got empty=%v, err=%v)", empty, err)
	}

	// Build a minimal Transfer event and submit it. The submit path is the
	// live consumer for OCSPending.
	ev, err := event.New(
		event.EventTypeTransfer,
		nil, // causalRefs
		event.TransferPayload{
			Version:   1,
			FromAgent: fromAgent,
			ToAgent:   toAgent,
			Amount:    100,
		},
		fromAgent, // agentID (string form)
		nil,       // priorTimestamps
		0,         // stakeAmount (MinStakeRequired=0)
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	_ = crypto.AgentID(fromAgent) // keep crypto import live for clarity

	if err := eng.Submit(ev); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	empty, err = eng.PendingEmpty(ctx)
	if err != nil {
		t.Fatalf("PendingEmpty after Submit: %v", err)
	}
	if empty {
		t.Fatalf("engine must have non-empty pending after Submit")
	}

	// Meta-assertion (plan §D8).
	entry := ocs.PendingProjection(eng)
	want := "github.com/Aethernet-network/aethernet/internal/integration.TestOCSPending_AccumulatesOnOptimistic"
	if entry.IntegrationTestRef != want {
		t.Fatalf("IntegrationTestRef mismatch:\n  want: %s\n  got:  %s", want, entry.IntegrationTestRef)
	}
}
