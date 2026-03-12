// Package evidence provides structured proof-of-work models and quality scoring
// for AetherNet task verification.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Evidence captures the structured output of AI work submitted for a task.
type Evidence struct {
	Hash          string            `json:"hash"`
	OutputType    string            `json:"output_type"`
	OutputSize    uint64            `json:"output_size"`
	Summary       string            `json:"summary"`
	InputHash     string            `json:"input_hash,omitempty"`
	Metrics       map[string]string `json:"metrics,omitempty"`
	OutputPreview string            `json:"output_preview,omitempty"`
	OutputURL     string            `json:"output_url,omitempty"`
}

// Validate checks that required fields are present.
func (e *Evidence) Validate() error {
	if e.Hash == "" {
		return errors.New("evidence hash is required")
	}
	if e.OutputType == "" {
		return errors.New("output_type is required")
	}
	if e.OutputSize == 0 {
		return errors.New("output_size must be > 0")
	}
	if e.Summary == "" {
		return errors.New("summary is required")
	}
	return nil
}

// ComputeHash returns a sha256 hex hash prefixed with "sha256:" for raw output bytes.
func ComputeHash(output []byte) string {
	h := sha256.Sum256(output)
	return "sha256:" + hex.EncodeToString(h[:])
}

// Score holds the three assessment dimensions and the weighted overall.
type Score struct {
	Relevance    float64 `json:"relevance"`
	Completeness float64 `json:"completeness"`
	Quality      float64 `json:"quality"`
	Overall      float64 `json:"overall"`
}

// ComputeOverall recalculates Overall from the three sub-scores using fixed weights:
// Relevance×0.3 + Completeness×0.4 + Quality×0.3.
func (s *Score) ComputeOverall() float64 {
	s.Overall = s.Relevance*0.3 + s.Completeness*0.4 + s.Quality*0.3
	return s.Overall
}

// PassThreshold is the minimum Overall score required for automatic task approval.
const PassThreshold = 0.60
