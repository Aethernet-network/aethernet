package canary

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// Correctness values for CalibrationSignal.
const (
	CorrectnessCorrect   = "correct"
	CorrectnessPartial   = "partial"
	CorrectnessIncorrect = "incorrect"
)

// ActorRole values for CalibrationSignal.
const (
	RoleWorker          = "worker"
	RoleReplaySubmitter = "replay_submitter"
	RoleValidator       = "validator"
)

// ComputedBy values for CalibrationSignal.ComputedBy.
const (
	// ComputedByVerifierOnly means the signal is based solely on the evidence
	// verifier's pass/fail verdict vs. canary.ExpectedPass. No truth model was
	// applied — either ExpectedEvidence was nil or no observed output was provided.
	ComputedByVerifierOnly = "verifier_only"

	// ComputedByTruthModel means ExpectedEvidence was present and observed output
	// was provided. Keyword/length checks contributed to the severity score.
	// NOTE: this is heuristic approximation, not semantic-correctness proof.
	ComputedByTruthModel = "truth_model"

	// ComputedByTruthModelAndChecks means both ExpectedEvidence and per-check
	// comparison contributed (observed checks were non-nil and canary had
	// ExpectedCheckResults).
	ComputedByTruthModelAndChecks = "truth_model+checks"
)

// CalibrationSignal records the outcome of evaluating one canary task
// against a specific actor's response. It is the primary measurement unit
// for executor and validator quality.
type CalibrationSignal struct {
	// ID is deterministic: sha256("signal|<canaryID>|<actorID>|<role>").
	// The same actor evaluating the same canary always produces the same ID,
	// which prevents duplicate signal accumulation across retries.
	ID        string    `json:"id"`
	CanaryID  string    `json:"canary_id"`
	TaskID    string    `json:"task_id"`
	ActorID   string    `json:"actor_id"`
	ActorRole string    `json:"actor_role"` // worker | replay_submitter | validator
	Category  string    `json:"category"`
	CanaryType string   `json:"canary_type"`
	// ExpectedPass / ObservedPass: the primary correctness comparison.
	// ExpectedPass comes from the canary record; ObservedPass comes from the
	// evidence verifier verdict (worker path) or replay status (replay path).
	ExpectedPass bool `json:"expected_pass"`
	ObservedPass bool `json:"observed_pass"`
	// ExpectedCheckResults / ObservedCheckResults: per-check comparison when
	// the canary defines named gates. Nil means no per-check expectations.
	ExpectedCheckResults map[string]bool `json:"expected_check_results"`
	ObservedCheckResults map[string]bool `json:"observed_check_results"`
	// Correctness is the primary categorical outcome:
	//   "correct"   — overall pass matches AND all check results match
	//   "partial"   — overall pass matches but some checks differ, OR
	//                 verifier passed but truth model finds issues
	//   "incorrect" — overall pass doesn't match
	Correctness string  `json:"correctness"` // correct | partial | incorrect
	Severity    float64 `json:"severity"`    // 0.0 (correct) to 1.0 (approved bad work)

	// ── Truth model fields (populated when ComputedBy != "verifier_only") ──────
	// NOTE: these fields are computed from heuristic keyword/length checks.
	// They indicate approximate alignment with expected output characteristics,
	// NOT semantic correctness.

	// TruthModelScore is 0.0–1.0 representing how well the observed output
	// matched the canary's ExpectedEvidence. 0.0 when not computed.
	TruthModelScore float64 `json:"truth_model_score,omitempty"`

	// RequiredConceptsFound lists which required concepts were found in the
	// observed output (case-insensitive substring match).
	RequiredConceptsFound []string `json:"required_concepts_found,omitempty"`

	// ForbiddenConceptsFound lists which forbidden concepts were found.
	// Non-empty means the output contained content that should not appear.
	ForbiddenConceptsFound []string `json:"forbidden_concepts_found,omitempty"`

	// OutputLengthSatisfied indicates whether the output met the minimum
	// length expectation. Nil means length was not checked.
	OutputLengthSatisfied *bool `json:"output_length_satisfied,omitempty"`

	// ComputedBy identifies what data contributed to correctness and severity.
	// One of: "verifier_only", "truth_model", "truth_model+checks".
	ComputedBy string `json:"computed_by"`

	Timestamp time.Time `json:"timestamp"`
}

// signalID derives a deterministic, content-addressed ID for a calibration
// signal from the triple (canaryID, actorID, role). The first 8 bytes of
// sha256 give a 16-hex-char suffix — collision probability is negligible for
// the expected signal volume.
func signalID(canaryID, actorID, role string) string {
	raw := fmt.Sprintf("signal|%s|%s|%s", canaryID, actorID, role)
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("sig-%x", sum[:8])
}
