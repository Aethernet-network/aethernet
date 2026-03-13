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
	TypeKnownGood   = "known_good"
	TypeKnownBad    = "known_bad"
	TypeAdversarial = "adversarial"
	TypeEdgeCase    = "edge_case"
)

// CanaryStatus values.
const (
	StatusActive   = "active"
	StatusConsumed = "consumed"
	StatusExpired  = "expired"
)

// ExpectedEvidence is the structured truth representation for a canary task.
// It is the primary field used by the Evaluator to assess whether a submitted
// output is semantically correct, beyond the raw pass/fail comparison.
//
// Fields are intentionally generic — the schema supports code, research, and
// writing categories without overfitting to any single tool or language.
//
// IMPORTANT: evaluation using this struct is heuristic (keyword/length checks).
// It is NOT a semantic-correctness proof. The Evaluator explicitly marks signals
// computed from this struct as "truth_model" to distinguish them from purely
// verifier-based assessment.
type ExpectedEvidence struct {
	// Summary describes what a correct output looks like. Informational only —
	// not used in automated evaluation but helpful for corpus review and
	// understanding what the canary is measuring.
	Summary string `json:"summary,omitempty"`

	// RequiredConcepts lists keywords or short phrases that a correct output
	// must contain (case-insensitive substring match). Missing required concepts
	// reduce the truth model score proportionally.
	RequiredConcepts []string `json:"required_concepts,omitempty"`

	// ForbiddenConcepts lists keywords or short phrases that must NOT appear
	// in a correct output (case-insensitive substring match). Finding forbidden
	// content increases severity — it suggests the worker produced incorrect or
	// off-topic output.
	ForbiddenConcepts []string `json:"forbidden_concepts,omitempty"`

	// MinOutputLength is the minimum character count expected for a complete
	// response. Zero means no minimum is enforced.
	MinOutputLength int `json:"min_output_length,omitempty"`

	// ExpectedChecks maps check names to expected pass/fail outcomes.
	// These are the same check names used by DeterministicVerifier.
	// When populated, the Evaluator compares observed check results against
	// these expectations to produce per-check accuracy.
	ExpectedChecks map[string]bool `json:"expected_checks,omitempty"`
}

// CanaryTask is a protocol-internal measurement unit. It wraps a known-answer
// task that has been (or will be) injected into the marketplace as a real task.
// When a verification result comes back, it is compared against the expected
// fields to compute quality metrics for the responding executor/validator.
type CanaryTask struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`        // set after the protocol task is created
	Category      string `json:"category"`
	PolicyVersion string `json:"policy_version"`
	CanaryType    string `json:"canary_type"`    // known_good | known_bad | adversarial | edge_case
	// ExpectedPass is whether the evidence verifier should pass a correct output.
	// For known_good canaries this is true; for known_bad/adversarial it is false.
	// The Evaluator compares the observed verifier verdict against this field.
	ExpectedPass         bool            `json:"expected_pass"`
	ExpectedMinScore     float64         `json:"expected_min_score"`
	ExpectedMaxScore     float64         `json:"expected_max_score"`
	ExpectedCheckResults map[string]bool `json:"expected_check_results"` // per-check expected pass/fail
	// GroundTruthHash is a stable SHA-256 reference to the known-correct output
	// artifact (or reference document). It is stored and transported as a durable
	// identity anchor. The Evaluator does not retrieve or compare the artifact
	// directly in the current implementation — ExpectedEvidence carries the
	// evaluation-usable truth representation.
	GroundTruthHash string `json:"ground_truth_hash"`
	// ExpectedEvidence is the structured truth representation used by the
	// Evaluator to produce richer calibration signals. When nil, the Evaluator
	// falls back to pure verifier pass/fail comparison.
	ExpectedEvidence *ExpectedEvidence `json:"expected_evidence,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	Status           string            `json:"status"` // active | consumed | expired
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
