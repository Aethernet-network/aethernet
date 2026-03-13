package verification

// ReplayRequirements is the replay material that must accompany a
// verification packet for code-category tasks to be generation-eligible.
// Nil on a VerificationRequest means no replay material was submitted.
type ReplayRequirements struct {
	// SourceSnapshotHash is a content-addressed hash of the exact source tree
	// that was executed (e.g. a git tree-sha or tarball hash).
	SourceSnapshotHash string

	// AcceptanceContractHash is the hash of the AcceptanceContract that
	// governed what "pass" means for this task.
	AcceptanceContractHash string

	// RequiredChecks lists the gate names that must all pass for the task to
	// settle under the acceptance contract.
	RequiredChecks []string

	// EnvironmentManifestHash is a hash of the execution environment
	// specification (e.g. Docker image digest, Nix flake lock).
	EnvironmentManifestHash string

	// ToolchainManifestHash is a hash of the exact toolchain used
	// (e.g. go.sum, package-lock.json, Cargo.lock).
	ToolchainManifestHash string

	// CommandSpecs describes the individual check commands that were run.
	CommandSpecs []CommandSpec

	// ArtifactRefs lists the output artifacts produced by each check command.
	ArtifactRefs []ArtifactRef

	// MachineReadableResults holds structured check output keyed by check name.
	// Values must be JSON-serialisable.
	MachineReadableResults map[string]interface{}
}

// CommandSpec describes a single verifiable check command.
type CommandSpec struct {
	// CheckType is the logical name of the check (e.g. "go_test", "lint").
	CheckType string

	// Command is the argv of the check invocation.
	Command []string

	// WorkingDir is the working directory relative to the source root.
	WorkingDir string

	// ArgsHash is a hash of Command + WorkingDir for compact verification.
	ArgsHash string
}

// ArtifactRef references a verifiable artifact produced by a check.
type ArtifactRef struct {
	// CheckType identifies which check produced this artifact.
	CheckType string

	// ArtifactType classifies the artifact: "stdout", "stderr", "report", "binary".
	ArtifactType string

	// Hash is the content-addressed hash of the artifact bytes.
	Hash string

	// SizeBytes is the byte length of the artifact.
	SizeBytes int64
}

// ReplayabilityAssessment is the result of AssessReplayability.
type ReplayabilityAssessment struct {
	// Replayable is true when the submitted replay material satisfies the
	// minimum requirements for the given category and policy version.
	Replayable bool

	// MissingFields lists the field names (snake_case) that were absent or
	// empty. Nil when Replayable is true.
	MissingFields []string

	// ReplayLevel classifies the strength of the replay material:
	//   "none"                — no replay material; cannot be replayed
	//   "structural"          — minimal fields present (non-code categories, v1)
	//   "replayable"          — full replay pack present; execution reproducible
	//   "attested_replayable" — replayable + hardware attestation (future)
	ReplayLevel string
}

// AssessReplayability evaluates whether req meets the minimum replay
// requirements for the given category and policyVersion.
//
// For category "code" with policyVersion "v1" (or empty), ALL of the
// following fields must be non-empty / non-nil:
//
//   - SourceSnapshotHash
//   - AcceptanceContractHash
//   - RequiredChecks (non-empty slice)
//   - EnvironmentManifestHash
//   - ToolchainManifestHash
//   - CommandSpecs (at least one)
//   - ArtifactRefs (at least one)
//   - MachineReadableResults (at least one entry)
//
// For other categories the bar is lower ("structural"): only
// AcceptanceContractHash and RequiredChecks are required.
//
// A nil req is treated as if all fields are missing.
func AssessReplayability(req *ReplayRequirements, category, policyVersion string) *ReplayabilityAssessment {
	if req == nil {
		return &ReplayabilityAssessment{
			Replayable: false,
			ReplayLevel: "none",
			MissingFields: []string{
				"source_snapshot_hash",
				"acceptance_contract_hash",
				"required_checks",
				"environment_manifest_hash",
				"toolchain_manifest_hash",
				"command_specs",
				"artifact_refs",
				"machine_readable_results",
			},
		}
	}

	pv := policyVersion
	if pv == "" {
		pv = "v1"
	}

	if category == "code" && pv == "v1" {
		return assessCodeV1(req)
	}
	return assessStructural(req)
}

// assessCodeV1 enforces the full replay-material requirement for code/v1.
func assessCodeV1(req *ReplayRequirements) *ReplayabilityAssessment {
	var missing []string

	if req.SourceSnapshotHash == "" {
		missing = append(missing, "source_snapshot_hash")
	}
	if req.AcceptanceContractHash == "" {
		missing = append(missing, "acceptance_contract_hash")
	}
	if len(req.RequiredChecks) == 0 {
		missing = append(missing, "required_checks")
	}
	if req.EnvironmentManifestHash == "" {
		missing = append(missing, "environment_manifest_hash")
	}
	if req.ToolchainManifestHash == "" {
		missing = append(missing, "toolchain_manifest_hash")
	}
	if len(req.CommandSpecs) == 0 {
		missing = append(missing, "command_specs")
	}
	if len(req.ArtifactRefs) == 0 {
		missing = append(missing, "artifact_refs")
	}
	if len(req.MachineReadableResults) == 0 {
		missing = append(missing, "machine_readable_results")
	}

	if len(missing) > 0 {
		return &ReplayabilityAssessment{
			Replayable:    false,
			MissingFields: missing,
			ReplayLevel:   "none",
		}
	}
	return &ReplayabilityAssessment{
		Replayable:    true,
		MissingFields: nil,
		ReplayLevel:   "replayable",
	}
}

// assessStructural enforces the minimal structural replay requirement for
// non-code categories: AcceptanceContractHash and RequiredChecks must be present.
func assessStructural(req *ReplayRequirements) *ReplayabilityAssessment {
	var missing []string

	if req.AcceptanceContractHash == "" {
		missing = append(missing, "acceptance_contract_hash")
	}
	if len(req.RequiredChecks) == 0 {
		missing = append(missing, "required_checks")
	}

	if len(missing) > 0 {
		return &ReplayabilityAssessment{
			Replayable:    false,
			MissingFields: missing,
			ReplayLevel:   "none",
		}
	}
	return &ReplayabilityAssessment{
		Replayable:    true,
		MissingFields: nil,
		ReplayLevel:   "structural",
	}
}
