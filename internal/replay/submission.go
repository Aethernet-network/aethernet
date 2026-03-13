package replay

import "time"

// ReplaySubmission is the payload sent by an external replay executor to
// report the results of re-running the checks defined in a ReplayJob.
//
// The protocol compares SubmittedCheckResults against the original claimed
// results stored in the ReplayJob and produces a real match/mismatch outcome.
// The submitter supplies raw check results; the protocol decides the verdict.
type ReplaySubmission struct {
	// JobID is the ID of the ReplayJob this submission addresses.
	JobID string `json:"job_id"`

	// TaskID identifies the marketplace task. Must match the ReplayJob.
	TaskID string `json:"task_id"`

	// Category is the task category (e.g. "code", "research").
	// Must match the ReplayJob's Category.
	Category string `json:"category"`

	// PolicyVersion is the verification policy version (e.g. "v1").
	// Must match the ReplayJob's PolicyVersion (empty == "v1").
	PolicyVersion string `json:"policy_version"`

	// AcceptanceContractHash must match the ReplayJob's AcceptanceContractHash
	// when both are non-empty, proving the submitter ran against the same
	// acceptance criteria as the original worker.
	AcceptanceContractHash string `json:"acceptance_contract_hash,omitempty"`

	// SubmitterID is the identity of the agent or system that performed
	// the replay. Used as the ReplayerID in the resulting ReplayOutcome.
	SubmitterID string `json:"submitter_id"`

	// CheckResults holds one entry per check that was re-executed.
	// All checks listed in ReplayJob.ChecksToReplay must be present.
	CheckResults []SubmittedCheckResult `json:"check_results"`

	// SubmittedAt is the time the replay run completed.
	SubmittedAt time.Time `json:"submitted_at"`
}

// SubmittedCheckResult captures the output of re-executing a single named check.
// The protocol compares each field against the original claimed values in the
// ReplayJob: pass/fail, artifact hash, and machine-readable numeric fields.
type SubmittedCheckResult struct {
	// CheckType is the logical check name matching a value in
	// ReplayJob.ChecksToReplay (e.g. "go_test", "lint").
	CheckType string `json:"check_type"`

	// Pass indicates whether this check passed in the replay run.
	// The original claim for all checks in ChecksToReplay is always pass=true;
	// a replay reporting pass=false is a direct contradiction.
	Pass bool `json:"pass"`

	// ExitCode is the process exit code for the check, if applicable.
	// 0 = success; non-zero = failure. Included in mismatch detail when != 0.
	ExitCode int `json:"exit_code,omitempty"`

	// ArtifactHash is the content-addressed hash of the primary output
	// artifact produced by this check (e.g. "sha256:<hex>"). When present,
	// compared against ReplayJob.ArtifactRefs for the same check type.
	ArtifactHash string `json:"artifact_hash,omitempty"`

	// MachineReadableResult holds structured output from the check, keyed by
	// field name. Numeric values are compared conservatively against the
	// original MachineReadableResults fields in the ReplayJob.
	MachineReadableResult map[string]interface{} `json:"machine_readable_result,omitempty"`

	// Notes is optional human-readable context about this check result.
	Notes string `json:"notes,omitempty"`
}
