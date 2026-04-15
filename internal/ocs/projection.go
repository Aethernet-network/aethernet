package ocs

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

// PendingProjectionName is the canonical registry name for the OCS engine's
// pending-item store.
const PendingProjectionName = "OCSPending"

// PendingProjection returns the CanonicalProjection entry for the OCS
// engine's pending-items state. See plan §D8.
func PendingProjection(eng *Engine) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              PendingProjectionName,
		Package:           "internal/ocs",
		StoreType:         "Engine.pending",
		Classification:    projections.Canonical,
		SourceEvents:      []projections.EventType{"Transfer", "Generation", "Settlement"},
		LiveConsumerRef:   "internal/recognition.OCSSubmitConsumer",
		ReplayConsumerRef: "internal/recognition.OCSSubmitConsumer",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (projections.OCSPending)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/integration.TestOCSPending_AccumulatesOnOptimistic",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-14",
		StateProbe: func(ctx context.Context) (bool, error) {
			return eng.PendingEmpty(ctx)
		},
	}
}
