package reputation

import (
	"github.com/Aethernet-network/aethernet/internal/projections"
)

// ProjectionName is the canonical registry name for the agent reputation
// manager.
const ProjectionName = "AgentReputation"

// Projection returns the Advisory projection entry for the agent
// reputation manager.
//
// Classification is Advisory per step-2 plan §D4: AgentReputation is
// populated by settlement (via identity.RecordTaskCompletion) but no
// canonical consumer reads it — readers are task discovery, the marketplace
// router, and the API, none of which gate consensus. The registry entry
// carries a standing reclassification warning via AllowIdleWithJustification
// so future reviews surface the question "has any canonical consumer
// started reading this since last review?".
//
// No StateProbe: Advisory projections are exempt from PR-5 per projections
// V11, and AllowIdleWithJustification is set so the entry is also exempt
// from the StateProbe-required guard in any future extension.
func Projection(_ *ReputationManager) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              ProjectionName,
		Package:           "internal/reputation",
		StoreType:         "ReputationManager",
		Classification:    projections.Advisory,
		SourceEvents:      []projections.EventType{"TaskSettlement", "Settlement"},
		LiveConsumerRef:   "internal/settlement.Applicator",
		ReplayConsumerRef: "internal/reputation.ReputationManager.LoadFromStore",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceNodeLocalHTTP,
			EndpointPath: "/v1/agents/{id}/reputation",
		},
		IntegrationTestRef:         "",
		Owner:                      "application",
		CreatedAt:                  "2026-04-14",
		AllowIdleWithJustification: true,
		IdleJustification:          "informational only; no canonical consumer; reclassify to Canonical if any settlement or consensus path reads it in the future",
	}
}
