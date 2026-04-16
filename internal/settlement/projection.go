package settlement

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

// AppliedSetProjection returns the CanonicalProjection entry for the
// settlement applicator's applied-set. Registers in the single mandatory
// replay-state registry per C-9 with SubcategoryApplicatorApplied.
func AppliedSetProjection(a *Applicator) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:           "ApplicatorAppliedSet",
		Package:        "internal/settlement",
		StoreType:      "Applicator",
		Classification: projections.Canonical,
		SourceEvents:   []projections.EventType{"Settlement"},
		LiveConsumerRef:   "internal/recognition.SettlementConsumer",
		ReplayConsumerRef: "internal/settlement.Applicator",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (settlement.ApplicatorAppliedSet)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/settlement.TestApplicator_EscrowLockTransfer_RegistersEntry",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-16",
		Subcategory:        projections.SubcategoryApplicatorApplied,
		StateProbe: func(ctx context.Context) (bool, error) {
			return a.AppliedCount() == 0, nil
		},
		AllowIdleWithJustification: true,
		IdleJustification:          "applied-set is empty on fresh testnet wipe; populates after first settlement",
	}
}
