package taskverification

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/projections"
)

// CalibrationProjectionName is the canonical registry name.
const CalibrationProjectionName = "CalibrationStore"

// CalibrationProjection returns the CanonicalProjection entry for the
// calibration store. Production wiring in cmd/node/main.go and the
// integration test in internal/integration both call this, giving a
// single source of truth for the entry's metadata (plan §D8).
//
// StateProbe is a closure wrapping store.Empty per §D7 so future
// probe-signature evolution absorbs at the entry site.
func CalibrationProjection(store *CalibrationStore) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              CalibrationProjectionName,
		Package:           "internal/taskverification",
		StoreType:         "CalibrationStore",
		Classification:    projections.Canonical,
		SourceEvents:      []projections.EventType{"TaskVerificationConsensus"},
		LiveConsumerRef:   "internal/recognition.TaskVerificationConsensusConsumer",
		ReplayConsumerRef: "internal/recognition.TaskVerificationConsensusConsumer",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (projections.CalibrationStore)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/integration.TestCalibration_AppliesOnRoundFinalization",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-14",
		StateProbe: func(ctx context.Context) (bool, error) {
			return store.Empty(ctx)
		},
	}
}

// RoundStoreProjectionName is the canonical registry name for the
// TaskVerificationRound BadgerStore.
const RoundStoreProjectionName = "TaskVerificationRound"

// RoundStoreProjection returns the CanonicalProjection entry for the
// TaskVerificationRound BadgerStore — the durable record of every
// multi-validator verification round's state and final verdict.
func RoundStoreProjection(store *BadgerStore) projections.CanonicalProjection {
	return projections.CanonicalProjection{
		Name:              RoundStoreProjectionName,
		Package:           "internal/taskverification",
		StoreType:         "BadgerStore",
		Classification:    projections.Canonical,
		SourceEvents:      []projections.EventType{"TaskSubmitted", "TaskVerificationVote", "TaskVerificationConsensus"},
		LiveConsumerRef:   "internal/recognition.TaskVerificationRoundConsumer",
		ReplayConsumerRef: "internal/recognition.TaskVerificationRoundConsumer",
		ObservabilitySurface: projections.Surface{
			Kind:         projections.SurfaceHealth,
			EndpointPath: "/v1/status (projections.TaskVerificationRound)",
		},
		IntegrationTestRef: "github.com/Aethernet-network/aethernet/internal/integration.TestTaskVerificationRound_PersistsOnTaskSubmitted",
		Owner:              "state-and-consensus",
		CreatedAt:          "2026-04-14",
		StateProbe: func(ctx context.Context) (bool, error) {
			return store.Empty(ctx)
		},
	}
}
