// Package trajectory implements checkpoint body canonicalization and hashing
// for AetherNet trajectory commits.
//
// CheckpointBody is the large content-addressed artifact referenced by
// TrajectoryCommitPayload.CheckpointHash. It is stored in the blobstore,
// NOT in the DAG event payload or Fast Path event body.
//
// Layer: Application — may import Core Protocol packages.
package trajectory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// CheckpointBody is the structured content of a trajectory checkpoint.
// Stored externally in the blobstore, referenced by content hash in the
// lean TrajectoryCommitPayload within the DAG event.
type CheckpointBody struct {
	// ApproachDescription summarizes the current approach being explored.
	ApproachDescription string `json:"approach_description"`

	// Parameters captures the configuration/hyperparameters used.
	Parameters map[string]string `json:"parameters,omitempty"`

	// EvidenceSnippet contains a short sample of the work output.
	EvidenceSnippet string `json:"evidence_snippet,omitempty"`

	// ErrorDetail records any errors encountered during this checkpoint.
	ErrorDetail string `json:"error_detail,omitempty"`

	// IntermediateOutputHash is the content hash of any intermediate
	// artifacts produced (e.g., partial code, draft text).
	IntermediateOutputHash string `json:"intermediate_output_hash,omitempty"`
}

// checkpointCanonical is the canonical JSON representation for hashing.
// Fields are sorted alphabetically to ensure deterministic serialization.
type checkpointCanonical struct {
	ApproachDescription    string            `json:"approach_description"`
	ErrorDetail            string            `json:"error_detail"`
	EvidenceSnippet        string            `json:"evidence_snippet"`
	IntermediateOutputHash string            `json:"intermediate_output_hash"`
	Parameters             map[string]string `json:"parameters"`
}

// CanonicalCheckpointBytes produces the deterministic byte representation
// of a CheckpointBody for content-addressed hashing. The output is
// identical across Go, JavaScript, and Python for the same input:
//   - All fields included (even empty strings)
//   - Fields in alphabetical order (enforced by struct tag ordering)
//   - Parameters map keys sorted
//   - Compact JSON (no whitespace)
func CanonicalCheckpointBytes(body CheckpointBody) ([]byte, error) {
	// Sort parameter keys for determinism.
	params := body.Parameters
	if params == nil {
		params = make(map[string]string)
	}

	// Create a sorted-key copy for deterministic marshaling.
	sortedParams := make(map[string]string, len(params))
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sortedParams[k] = params[k]
	}

	canon := checkpointCanonical{
		ApproachDescription:    body.ApproachDescription,
		ErrorDetail:            body.ErrorDetail,
		EvidenceSnippet:        body.EvidenceSnippet,
		IntermediateOutputHash: body.IntermediateOutputHash,
		Parameters:             sortedParams,
	}

	data, err := json.Marshal(canon)
	if err != nil {
		return nil, fmt.Errorf("trajectory: marshal canonical checkpoint: %w", err)
	}
	return data, nil
}

// ComputeCheckpointHash computes the content-addressed hash and size of a
// CheckpointBody. Returns the hex-encoded SHA-256 hash and the byte size
// of the canonical representation.
func ComputeCheckpointHash(body CheckpointBody) (hash string, size int64, err error) {
	data, err := CanonicalCheckpointBytes(body)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}
