package roundprogress

import (
	"github.com/Aethernet-network/aethernet/internal/projections"
)

// ProjectionName is the canonical registry name for the round-progress
// snapshot store.
const ProjectionName = "RoundProgress"

// Projection returns the Advisory projection entry for the round-progress
// snapshot store. Classification is Advisory per plan §D3 and the BlobSync
// design §6.4: RoundProgressSnapshot is a liveness input only — it cannot
// by itself create an accept or reject verdict.
//
// No StateProbe: Advisory projections are exempt from PR-5.
func Projection(_ *BadgerSnapshotStore) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              ProjectionName,
		Package:           "internal/roundprogress",
		StoreType:         "BadgerSnapshotStore",
		Classification:    projections.Advisory,
		SourceEvents:      []projections.EventType{"ProgressUpdate"},
		LiveConsumerRef:   "internal/roundprogress.Aggregator",
		ReplayConsumerRef: "internal/roundprogress.Aggregator",
		ObservabilitySurface: projections.Surface{
			Kind:          projections.SurfaceNone,
			Justification: "Advisory only; control-plane liveness input per blobsync-design §6.4; not a consensus ledger",
		},
		IntegrationTestRef: "",
		Owner:              "transport",
		CreatedAt:          "2026-04-14",
	}
}
