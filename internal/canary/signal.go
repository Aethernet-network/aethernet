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

// CalibrationSignal records the outcome of evaluating one canary task
// against a specific actor's response. It is the primary measurement unit
// for executor and validator quality.
type CalibrationSignal struct {
	// ID is deterministic: sha256("signal|<canaryID>|<actorID>|<role>").
	// The same actor evaluating the same canary always produces the same ID,
	// which prevents duplicate signal accumulation across retries.
	ID                   string          `json:"id"`
	CanaryID             string          `json:"canary_id"`
	TaskID               string          `json:"task_id"`
	ActorID              string          `json:"actor_id"`
	ActorRole            string          `json:"actor_role"`            // worker | replay_submitter | validator
	Category             string          `json:"category"`
	CanaryType           string          `json:"canary_type"`
	ExpectedPass         bool            `json:"expected_pass"`
	ObservedPass         bool            `json:"observed_pass"`
	ExpectedCheckResults map[string]bool `json:"expected_check_results"` // nil means no per-check expectation
	ObservedCheckResults map[string]bool `json:"observed_check_results"`
	Correctness          string          `json:"correctness"` // correct | partial | incorrect
	Severity             float64         `json:"severity"`    // 0.0 (correct) to 1.0 (approved bad work)
	Timestamp            time.Time       `json:"timestamp"`
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
