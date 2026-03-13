// Package canary provides the canary task model, persistence layer, and
// injection mechanism for AetherNet's measurement infrastructure.
//
// Canaries are protocol-internal known-answer tasks injected into the live
// marketplace to measure executor and validator quality without the subjects
// knowing. Each canary has a ground-truth expected result; deviations surface
// calibration issues in the verification pipeline.
package canary

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// CanaryType values.
const (
	TypeKnownGood  = "known_good"
	TypeKnownBad   = "known_bad"
	TypeAdversarial = "adversarial"
	TypeEdgeCase   = "edge_case"
)

// CanaryStatus values.
const (
	StatusActive   = "active"
	StatusConsumed = "consumed"
	StatusExpired  = "expired"
)

// CanaryTask is a protocol-internal measurement unit. It wraps a known-answer
// task that has been (or will be) injected into the marketplace as a real task.
// When a verification result comes back, it is compared against the expected
// fields to compute quality metrics for the responding executor/validator.
type CanaryTask struct {
	ID                   string          `json:"id"`
	TaskID               string          `json:"task_id"`               // set after the protocol task is created
	Category             string          `json:"category"`
	PolicyVersion        string          `json:"policy_version"`
	CanaryType           string          `json:"canary_type"`           // known_good | known_bad | adversarial | edge_case
	ExpectedPass         bool            `json:"expected_pass"`         // should verification pass?
	ExpectedMinScore     float64         `json:"expected_min_score"`
	ExpectedMaxScore     float64         `json:"expected_max_score"`
	ExpectedCheckResults map[string]bool `json:"expected_check_results"` // per-check expected pass/fail
	GroundTruthHash      string          `json:"ground_truth_hash"`     // stable reference to known-correct output
	CreatedAt            time.Time       `json:"created_at"`
	Status               string          `json:"status"`               // active | consumed | expired
}

// NewCanaryTask creates a new CanaryTask from the given parameters.
// The ID is derived from sha256("canary|<category>|<canaryType>|<unix_nano>")
// so that IDs are stable within a nanosecond and practically unique across time.
// Status is set to "active", PolicyVersion to "v1".
func NewCanaryTask(
	category, canaryType string,
	expectedPass bool,
	expectedChecks map[string]bool,
	groundTruthHash string,
) *CanaryTask {
	now := time.Now()
	raw := fmt.Sprintf("canary|%s|%s|%d", category, canaryType, now.UnixNano())
	sum := sha256.Sum256([]byte(raw))
	id := fmt.Sprintf("cnr-%x", sum[:8]) // "cnr-" + 16 hex chars

	checks := expectedChecks
	if checks == nil {
		checks = map[string]bool{}
	}

	return &CanaryTask{
		ID:                   id,
		Category:             category,
		PolicyVersion:        "v1",
		CanaryType:           canaryType,
		ExpectedPass:         expectedPass,
		ExpectedCheckResults: checks,
		GroundTruthHash:      groundTruthHash,
		CreatedAt:            now,
		Status:               StatusActive,
	}
}
