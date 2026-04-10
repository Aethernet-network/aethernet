package blobsync

import (
	"encoding/json"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// RegisterBootstrapExtractors registers extractors for all event types
// that carry blob references in the current protocol version.
func RegisterBootstrapExtractors(registry *BlobRefRegistry) {
	// TaskSubmitted carries the evidence blob hash.
	_ = registry.Register(string(event.EventTypeTaskSubmitted), "", extractTaskSubmittedBlobs)

	// TrajectoryCommit carries the checkpoint blob hash.
	// NOTE: not wired in prompt 01; noted for future registration.
	// _ = registry.Register(string(event.EventTypeTrajectoryCommit), "", extractTrajectoryCommitBlobs)
}

// extractTaskSubmittedBlobs extracts the evidence blob reference from a
// TaskSubmitted event payload.
func extractTaskSubmittedBlobs(payload []byte) ([]blobstore.BlobRef, error) {
	var p struct {
		EvidenceBodyHash string `json:"evidence_body_hash"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if p.EvidenceBodyHash == "" {
		return nil, nil // no blob reference in this payload
	}

	ref, err := blobstore.NewBlobRef(p.EvidenceBodyHash, blobstore.BlobKindEvidence, "")
	if err != nil {
		// Invalid hash format — defensive, return empty rather than error
		// since the evidence hash comes from a validated event payload.
		return nil, nil
	}
	// OriginNodeHint is not available from the payload alone; it will
	// be populated by the BlobDemandConsumer layer which has access to
	// the full event.Event including AgentID.
	return []blobstore.BlobRef{ref}, nil
}
