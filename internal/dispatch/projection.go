package dispatch

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

const projectionName = "DispatchAdmission"

// Projection returns the CanonicalProjection entry for the dispatcher's
// admission state. Registers in the single mandatory replay-state
// registry per C-9 with SubcategoryDispatchAdmission.
func Projection(d *Dispatcher) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:           projectionName,
		Package:        "internal/dispatch",
		StoreType:      "Dispatcher",
		Classification: projections.Canonical,
		SourceEvents:   []projections.EventType{"Transfer", "Generation", "TaskSettlement", "TaskVerificationConsensus"},
		LiveConsumerRef:   "internal/dispatch.CanonicalEventDispatcher",
		ReplayConsumerRef: "internal/dispatch.CanonicalEventDispatcher",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (dispatch.DispatchAdmission)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/dispatch.TestDispatcher_FullAdmissionFlow_TypeA",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-15",
		Subcategory:        projections.SubcategoryDispatchAdmission,
		StateProbe: func(ctx context.Context) (bool, error) {
			records, err := d.store.AllAdmissions()
			if err != nil {
				return false, err
			}
			return len(records) == 0, nil
		},
		AllowIdleWithJustification: true,
		IdleJustification: "Part C ships with zero consumers; admission records " +
			"only appear after commit 9 of §9 wires the first real consumer",
	}
}
