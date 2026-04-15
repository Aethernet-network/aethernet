package epoch

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

// RoundCounterProjectionName is the canonical registry name. Exposed as a
// constant so integration tests can match on it without hardcoding strings
// in multiple places.
const RoundCounterProjectionName = "RoundCounter"

// RoundCounterProjection returns the CanonicalProjection entry for the
// round counter. Production wiring in cmd/node/main.go calls this and
// passes the result to ProjectionRegistry.MustRegister; integration tests
// call it to read the entry's IntegrationTestRef for the meta-assertion
// required by plan §D8.
//
// Per plan §D7 (closure indirection), the StateProbe wraps counter.Empty
// rather than assigning the method value directly — this absorbs future
// probe-signature changes at the registry-entry site.
func RoundCounterProjection(counter *RoundCounter) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              RoundCounterProjectionName,
		Package:           "internal/epoch",
		StoreType:         "RoundCounter",
		Classification:    projections.Canonical,
		SourceEvents:      []projections.EventType{"TaskVerificationConsensus"},
		LiveConsumerRef:   "internal/epoch.RoundCountConsumer",
		ReplayConsumerRef: "internal/epoch.RoundCountConsumer",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (projections.RoundCounter)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/integration.TestRoundCounter_IncrementsOnConsensus",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-14",
		StateProbe: func(ctx context.Context) (bool, error) {
			return counter.Empty(ctx)
		},
	}
}
