package verification_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fullCodeReplay returns a ReplayRequirements that satisfies every field
// required for category "code", policy "v1".
func fullCodeReplay() *verification.ReplayRequirements {
	return &verification.ReplayRequirements{
		SourceSnapshotHash:      "sha256:source1234",
		AcceptanceContractHash:  "sha256:contract5678",
		RequiredChecks:          []string{"go_test", "lint"},
		EnvironmentManifestHash: "sha256:env9abc",
		ToolchainManifestHash:   "sha256:tool0def",
		CommandSpecs: []verification.CommandSpec{
			{CheckType: "go_test", Command: []string{"go", "test", "./..."}, WorkingDir: ".", ArgsHash: "sha256:argshash1"},
		},
		ArtifactRefs: []verification.ArtifactRef{
			{CheckType: "go_test", ArtifactType: "stdout", Hash: "sha256:outabc", SizeBytes: 1024},
		},
		MachineReadableResults: map[string]interface{}{
			"go_test": map[string]interface{}{"passed": true, "tests": 42},
		},
	}
}

// ---------------------------------------------------------------------------
// AssessReplayability — code/v1 (full requirements)
// ---------------------------------------------------------------------------

func TestAssessReplayability_CodeFullRequirements(t *testing.T) {
	req := fullCodeReplay()
	a := verification.AssessReplayability(req, "code", "v1")

	if !a.Replayable {
		t.Errorf("expected Replayable=true for full code/v1 requirements, got false; missing=%v", a.MissingFields)
	}
	if a.ReplayLevel != "replayable" {
		t.Errorf("ReplayLevel = %q; want \"replayable\"", a.ReplayLevel)
	}
	if len(a.MissingFields) != 0 {
		t.Errorf("expected no MissingFields, got %v", a.MissingFields)
	}
}

func TestAssessReplayability_CodeEmptyPolicyDefaultsToV1(t *testing.T) {
	// Empty policyVersion should default to "v1", applying code/v1 rules.
	req := fullCodeReplay()
	a := verification.AssessReplayability(req, "code", "")

	if !a.Replayable {
		t.Errorf("empty policyVersion should default to v1 and pass: %v", a.MissingFields)
	}
	if a.ReplayLevel != "replayable" {
		t.Errorf("ReplayLevel = %q; want \"replayable\"", a.ReplayLevel)
	}
}

// ---------------------------------------------------------------------------
// AssessReplayability — missing single fields
// ---------------------------------------------------------------------------

func TestAssessReplayability_MissingSourceSnapshotHash(t *testing.T) {
	req := fullCodeReplay()
	req.SourceSnapshotHash = ""

	a := verification.AssessReplayability(req, "code", "v1")
	if a.Replayable {
		t.Error("expected Replayable=false when SourceSnapshotHash is empty")
	}
	if !containsField(a.MissingFields, "source_snapshot_hash") {
		t.Errorf("expected \"source_snapshot_hash\" in MissingFields, got %v", a.MissingFields)
	}
}

func TestAssessReplayability_MissingAcceptanceContractHash(t *testing.T) {
	req := fullCodeReplay()
	req.AcceptanceContractHash = ""

	a := verification.AssessReplayability(req, "code", "v1")
	if a.Replayable {
		t.Error("expected Replayable=false when AcceptanceContractHash is empty")
	}
	if !containsField(a.MissingFields, "acceptance_contract_hash") {
		t.Errorf("expected \"acceptance_contract_hash\" in MissingFields, got %v", a.MissingFields)
	}
}

func TestAssessReplayability_MissingRequiredChecks(t *testing.T) {
	req := fullCodeReplay()
	req.RequiredChecks = nil

	a := verification.AssessReplayability(req, "code", "v1")
	if a.Replayable {
		t.Error("expected Replayable=false when RequiredChecks is empty")
	}
	if !containsField(a.MissingFields, "required_checks") {
		t.Errorf("expected \"required_checks\" in MissingFields, got %v", a.MissingFields)
	}
}

// ---------------------------------------------------------------------------
// AssessReplayability — multiple missing fields
// ---------------------------------------------------------------------------

func TestAssessReplayability_MultipleMissingFields(t *testing.T) {
	// Empty struct: all eight code/v1 fields are missing.
	req := &verification.ReplayRequirements{}
	a := verification.AssessReplayability(req, "code", "v1")

	if a.Replayable {
		t.Error("expected Replayable=false when all fields are empty")
	}

	wantMissing := []string{
		"source_snapshot_hash",
		"acceptance_contract_hash",
		"required_checks",
		"environment_manifest_hash",
		"toolchain_manifest_hash",
		"command_specs",
		"artifact_refs",
		"machine_readable_results",
	}
	for _, f := range wantMissing {
		if !containsField(a.MissingFields, f) {
			t.Errorf("expected %q in MissingFields, got %v", f, a.MissingFields)
		}
	}
	if len(a.MissingFields) != len(wantMissing) {
		t.Errorf("MissingFields length: want %d, got %d (%v)", len(wantMissing), len(a.MissingFields), a.MissingFields)
	}
}

func TestAssessReplayability_NilRequest(t *testing.T) {
	a := verification.AssessReplayability(nil, "code", "v1")

	if a.Replayable {
		t.Error("expected Replayable=false for nil request")
	}
	if a.ReplayLevel != "none" {
		t.Errorf("ReplayLevel = %q; want \"none\"", a.ReplayLevel)
	}
	if len(a.MissingFields) == 0 {
		t.Error("expected non-empty MissingFields for nil request")
	}
}

// ---------------------------------------------------------------------------
// AssessReplayability — non-code category (structural level)
// ---------------------------------------------------------------------------

func TestAssessReplayability_NonCodeMinimalFields(t *testing.T) {
	// For non-code categories, only AcceptanceContractHash + RequiredChecks required.
	req := &verification.ReplayRequirements{
		AcceptanceContractHash: "sha256:contract5678",
		RequiredChecks:         []string{"coherence"},
	}
	for _, cat := range []string{"research", "writing", "data"} {
		a := verification.AssessReplayability(req, cat, "v1")
		if !a.Replayable {
			t.Errorf("category %q: expected Replayable=true with minimal structural fields, missing=%v", cat, a.MissingFields)
		}
		if a.ReplayLevel != "structural" {
			t.Errorf("category %q: ReplayLevel = %q; want \"structural\"", cat, a.ReplayLevel)
		}
	}
}

func TestAssessReplayability_NonCodeMissingContractHash(t *testing.T) {
	req := &verification.ReplayRequirements{
		RequiredChecks: []string{"coherence"},
		// AcceptanceContractHash deliberately empty
	}
	a := verification.AssessReplayability(req, "writing", "v1")
	if a.Replayable {
		t.Error("expected Replayable=false for writing with missing AcceptanceContractHash")
	}
	if !containsField(a.MissingFields, "acceptance_contract_hash") {
		t.Errorf("expected \"acceptance_contract_hash\" in MissingFields, got %v", a.MissingFields)
	}
}

// ---------------------------------------------------------------------------
// Generation-eligible gate — settlement blocked when evidence not replayable
// ---------------------------------------------------------------------------

func TestGenerationEligible_BlockedWithoutReplayableMaterial(t *testing.T) {
	reg := evidence.NewVerifierRegistry()
	inProc := verification.NewInProcessVerifier(reg)

	// A piece of Go code that would ordinarily pass the code verifier.
	goCode := `package main

import "fmt"

// Run prints the result.
func Run() string { return fmt.Sprintf("result=%d", 42) }

func main() { fmt.Println(Run()) }`

	ev := &evidence.Evidence{
		Hash:       "sha256:codehashdeadbeef",
		OutputType: "code",
		OutputSize: uint64(len(goCode)),
		Summary:    goCode,
	}

	// Request with GenerationEligible=true but no replay material.
	result, err := inProc.Verify(context.Background(), verification.VerificationRequest{
		TaskID:             "task-gen-1",
		Category:           "code",
		Title:              "Implement Run function",
		Description:        "implement Run() string",
		Budget:             200_000,
		Evidence:           ev,
		GenerationEligible: true,
		// ReplayRequirements deliberately nil
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Verify returned nil result")
	}

	// The threshold gate may pass (good code) but the generation gate must block.
	passed := len(result.DeterministicReport.HardGates) > 0 && result.DeterministicReport.HardGates[0].Pass
	if passed {
		t.Error("expected settlement blocked for generation-eligible task without replay material")
	}

	// The reason must be present.
	found := false
	for _, r := range result.SubjectiveReport.ReasonCodes {
		if strings.Contains(r, "generation_requires_replayable_evidence") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reason 'generation_requires_replayable_evidence' in %v",
			result.SubjectiveReport.ReasonCodes)
	}
}

func TestGenerationEligible_PassesWithFullReplayMaterial(t *testing.T) {
	reg := evidence.NewVerifierRegistry()
	inProc := verification.NewInProcessVerifier(reg)

	goCode := `package main

import "fmt"

// Compute returns the sum.
func Compute(a, b int) int { return a + b }

func main() { fmt.Println(Compute(1, 2)) }`

	ev := &evidence.Evidence{
		Hash:       "sha256:codehashfull1234",
		OutputType: "code",
		OutputSize: uint64(len(goCode)),
		Summary:    goCode,
	}

	result, err := inProc.Verify(context.Background(), verification.VerificationRequest{
		TaskID:             "task-gen-2",
		Category:           "code",
		Title:              "Implement Compute function",
		Description:        "implement Compute(a, b int) int",
		Budget:             200_000,
		Evidence:           ev,
		GenerationEligible: true,
		ReplayRequirements: fullCodeReplay(),
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Verify returned nil result")
	}

	// ReplayabilityAssessment must be attached and Replayable.
	if result.ReplayabilityAssessment == nil {
		t.Fatal("expected non-nil ReplayabilityAssessment on result")
	}
	if !result.ReplayabilityAssessment.Replayable {
		t.Errorf("expected Replayable=true, got false; missing=%v",
			result.ReplayabilityAssessment.MissingFields)
	}
	if result.ReplayabilityAssessment.ReplayLevel != "replayable" {
		t.Errorf("ReplayLevel = %q; want \"replayable\"", result.ReplayabilityAssessment.ReplayLevel)
	}
}

func TestGenerationEligible_NonGenerationTaskSettlesWithoutReplay(t *testing.T) {
	// Backward compat: non-generation tasks must still settle without replay material.
	reg := evidence.NewVerifierRegistry()
	inProc := verification.NewInProcessVerifier(reg)

	goCode := `package main

import "fmt"

// Greet returns a greeting.
func Greet(name string) string {
	if name == "" {
		return "Hello, World!"
	}
	return fmt.Sprintf("Hello, %s!", name)
}

func main() { fmt.Println(Greet("test")) }`

	ev := &evidence.Evidence{
		Hash:       "sha256:greetcodehash",
		OutputType: "code",
		OutputSize: uint64(len(goCode)),
		Summary:    goCode,
	}

	result, err := inProc.Verify(context.Background(), verification.VerificationRequest{
		TaskID:             "task-compat-1",
		Category:           "code",
		Title:              "Write greeting function",
		Description:        "implement Greet(name string) string",
		Budget:             100_000,
		Evidence:           ev,
		GenerationEligible: false, // not generation-eligible
		// ReplayRequirements deliberately nil
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Verify returned nil result")
	}

	// ReplayabilityAssessment must be nil (no replay material submitted).
	if result.ReplayabilityAssessment != nil {
		t.Error("expected nil ReplayabilityAssessment when no ReplayRequirements provided")
	}

	// generation_requires_replayable_evidence must NOT appear in reasons.
	for _, r := range result.SubjectiveReport.ReasonCodes {
		if strings.Contains(r, "generation_requires_replayable_evidence") {
			t.Errorf("non-generation task should not be blocked by replay gate, reasons=%v",
				result.SubjectiveReport.ReasonCodes)
		}
	}
}

// ---------------------------------------------------------------------------
// ConsensusSufficiencyChecker — generation gate directly
// ---------------------------------------------------------------------------

func TestConsensusSufficiencyChecker_GenerationGateBlocks(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	// Non-replayable assessment.
	assessment := &verification.ReplayabilityAssessment{
		Replayable:    false,
		ReplayLevel:   "none",
		MissingFields: []string{"source_snapshot_hash"},
	}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		GenerationEligible:      true,
		ReplayabilityAssessment: assessment,
	})
	if sufficient {
		t.Error("expected sufficient=false when generation-eligible and not replayable")
	}
	if !containsField(reasons, "generation_requires_replayable_evidence") {
		t.Errorf("expected reason 'generation_requires_replayable_evidence' in %v", reasons)
	}
}

func TestConsensusSufficiencyChecker_GenerationGatePassesWithReplayable(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	// Fully replayable assessment.
	assessment := &verification.ReplayabilityAssessment{
		Replayable:    true,
		ReplayLevel:   "replayable",
		MissingFields: nil,
	}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		GenerationEligible:      true,
		ReplayabilityAssessment: assessment,
	})
	if !sufficient {
		t.Errorf("expected sufficient=true when generation-eligible AND replayable, reasons=%v", reasons)
	}
}

func TestConsensusSufficiencyChecker_GenerationGateSkippedWhenNotEligible(t *testing.T) {
	// When GenerationEligible=false, even non-replayable evidence must not
	// trigger the gate (backward compatibility).
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	assessment := &verification.ReplayabilityAssessment{
		Replayable:    false,
		ReplayLevel:   "none",
		MissingFields: []string{"source_snapshot_hash"},
	}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		GenerationEligible:      false,
		ReplayabilityAssessment: assessment,
	})
	if !sufficient {
		t.Errorf("expected sufficient=true when not generation-eligible, reasons=%v", reasons)
	}
	for _, r := range reasons {
		if strings.Contains(r, "generation_requires_replayable_evidence") {
			t.Errorf("non-generation task should not see replay gate reason, got %v", reasons)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func containsField(fields []string, target string) bool {
	for _, f := range fields {
		if f == target {
			return true
		}
	}
	return false
}
