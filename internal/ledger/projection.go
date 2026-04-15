package ledger

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

// TransferLedgerProjectionName is the canonical registry name.
const TransferLedgerProjectionName = "TransferLedger"

// TransferLedgerProjection returns the CanonicalProjection entry for the
// transfer ledger. See docs/plans/2026-04-12-reputation-step-2-retrofit-projections.md §D8.
func TransferLedgerProjection(l *TransferLedger) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              TransferLedgerProjectionName,
		Package:           "internal/ledger",
		StoreType:         "TransferLedger",
		Classification:    projections.Canonical,
		SourceEvents:      []projections.EventType{"Transfer", "Generation", "Settlement"},
		LiveConsumerRef:   "internal/settlement.Applicator",
		ReplayConsumerRef: "internal/settlement.Applicator",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (projections.TransferLedger)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/integration.TestTransferLedger_AccumulatesOnSettlement",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-14",
		StateProbe: func(ctx context.Context) (bool, error) {
			return l.Empty(ctx)
		},
	}
}
