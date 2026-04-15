package escrow

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

// ProjectionName is the canonical registry name.
const ProjectionName = "Escrow"

// Projection returns the CanonicalProjection entry for the escrow store.
// See docs/plans/2026-04-12-reputation-step-2-retrofit-projections.md §D8.
func Projection(e *Escrow) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              ProjectionName,
		Package:           "internal/escrow",
		StoreType:         "Escrow",
		Classification:    projections.Canonical,
		SourceEvents:      []projections.EventType{"Transfer", "Settlement"},
		LiveConsumerRef:   "internal/settlement.Applicator",
		ReplayConsumerRef: "internal/settlement.Applicator",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (projections.Escrow)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/integration.TestEscrow_HoldsOnTransferOptimistic",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-14",
		StateProbe: func(ctx context.Context) (bool, error) {
			return e.Empty(ctx)
		},
	}
}
