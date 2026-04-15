package blobsync

import (
	"github.com/Aethernet-network/aethernet/internal/projections"
)

// ServingReputationProjectionName is the canonical registry name for the
// per-peer blob-serving reputation tracker.
const ServingReputationProjectionName = "BlobServingReputation"

// ServingReputationProjection returns the Advisory projection entry for
// BlobServingReputation. Classification is Advisory per plan §9.6 and
// §D3: the store feeds BlobSync peer selection, not consensus. Upgrade
// to Canonical when it feeds consensus weighting in a later workstream.
//
// No StateProbe: Advisory projections are exempt from PR-5.
func ServingReputationProjection(_ *BlobServingReputation) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              ServingReputationProjectionName,
		Package:           "internal/blobsync",
		StoreType:         "BlobServingReputation",
		Classification:    projections.Advisory,
		SourceEvents:      []projections.EventType{"BlobFetchComplete", "BlobFetchFailed"},
		LiveConsumerRef:   "internal/blobsync.BlobTransport",
		ReplayConsumerRef: "internal/blobsync.BlobTransport",
		ObservabilitySurface: projections.Surface{
			Kind:          projections.SurfaceNone,
			Justification: "Advisory only; peer reputation is internal transport routing logic, not externally observable",
		},
		IntegrationTestRef: "",
		Owner:              "transport",
		CreatedAt:          "2026-04-14",
	}
}
