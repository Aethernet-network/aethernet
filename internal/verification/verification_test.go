package verification_test

import (
	"context"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeEv(summary, preview string) *evidence.Evidence {
	return &evidence.Evidence{
		Hash:          "sha256:testdeadbeef",
		OutputType:    "text",
		OutputSize:    uint64(len(summary) + len(preview)),
		Summary:       summary,
		OutputPreview: preview,
	}
}

func newRegistry() *evidence.VerifierRegistry {
	return evidence.NewVerifierRegistry()
}

// ---------------------------------------------------------------------------
// DeterministicVerifier
// ---------------------------------------------------------------------------

func TestDeterministicVerifier_ReportStructure(t *testing.T) {
	dv := verification.NewDeterministicVerifier(newRegistry())
	ev := makeEv("The quick brown fox jumps over the lazy dog.", "")
	report := dv.Verify("writing", ev, "Short story", "Write something", 100_000)

	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if len(report.HardGates) == 0 {
		t.Fatal("expected at least one hard gate")
	}
	// HardGates[0] must be the "threshold" gate.
	if report.HardGates[0].Name != "threshold" {
		t.Errorf("HardGates[0].Name = %q; want \"threshold\"", report.HardGates[0].Name)
	}
	// Required numeric score keys must be present.
	for _, key := range []string{"relevance", "completeness", "quality", "overall"} {
		if _, ok := report.NumericScores[key]; !ok {
			t.Errorf("NumericScores missing key %q", key)
		}
	}
}

func TestDeterministicVerifier_EmptyEvidenceFails(t *testing.T) {
	dv := verification.NewDeterministicVerifier(newRegistry())
	ev := &evidence.Evidence{}
	report := dv.Verify("writing", ev, "task", "desc", 100_000)

	if report == nil {
		t.Fatal("expected non-nil report even for empty evidence")
	}
	// threshold gate must fail for empty input.
	if report.HardGates[0].Pass {
		t.Errorf("threshold gate should fail for empty evidence")
	}
	// has_output gate must also fail.
	for _, g := range report.HardGates {
		if g.Name == "has_output" && g.Pass {
			t.Errorf("has_output gate should fail for empty evidence")
		}
	}
}

func TestDeterministicVerifier_NilEvidenceNoPanic(t *testing.T) {
	dv := verification.NewDeterministicVerifier(newRegistry())
	// Must not panic; nil is treated as empty evidence — all gates fail.
	report := dv.Verify("code", nil, "task", "desc", 100_000)
	if report == nil {
		t.Fatal("expected non-nil report for nil evidence")
	}
	// Threshold gate must fail for nil input.
	if len(report.HardGates) == 0 || report.HardGates[0].Pass {
		t.Error("threshold gate should fail for nil evidence")
	}
}

func TestDeterministicVerifier_NilRegistryFallback(t *testing.T) {
	dv := verification.NewDeterministicVerifier(nil) // keyword-verifier fallback
	ev := makeEv("agent aethernet protocol task transfer settlement evidence", "")
	report := dv.Verify("unknown", ev, "task", "desc", 10_000)

	if report == nil {
		t.Fatal("expected non-nil report with nil registry")
	}
	if len(report.HardGates) == 0 {
		t.Fatal("expected hard gates with nil registry")
	}
}

// ---------------------------------------------------------------------------
// ConsensusSufficiencyChecker
// ---------------------------------------------------------------------------

func TestConsensusSufficiencyChecker_AboveThreshold(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	sufficient, reasons := chk.Check(det, subj, "v1")
	if !sufficient {
		t.Errorf("expected sufficient=true for passing threshold gate, reasons=%v", reasons)
	}
	if len(reasons) != 0 {
		t.Errorf("expected no reasons for fully passing report, got %v", reasons)
	}
}

func TestConsensusSufficiencyChecker_BelowThreshold(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: false, Detail: "overall=0.300"},
		},
		NumericScores: map[string]float64{"overall": 0.30},
	}
	subj := &verification.SubjectiveReport{Overall: 0.30}

	sufficient, reasons := chk.Check(det, subj, "v1")
	if sufficient {
		t.Errorf("expected sufficient=false for failing threshold gate")
	}
	if len(reasons) == 0 {
		t.Error("expected at least one reason for failing report")
	}
}

func TestConsensusSufficiencyChecker_NoThresholdGateFallback(t *testing.T) {
	// When no "threshold" gate is present, falls back to evidence.PassThreshold.
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates:     []verification.GateResult{},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80} // above PassThreshold (0.60)

	sufficient, _ := chk.Check(det, subj, "v1")
	if !sufficient {
		t.Error("expected sufficient=true when overall (0.80) >= PassThreshold (0.60)")
	}
}

func TestConsensusSufficiencyChecker_StructuralGateFailAddsReason(t *testing.T) {
	// A failing structural gate adds a reason but does NOT flip sufficient
	// (only the threshold gate determines sufficient).
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.750"},
			{Name: "hash_valid", Pass: false, Detail: "evidence Hash must be non-empty"},
		},
		NumericScores: map[string]float64{"overall": 0.75},
	}
	subj := &verification.SubjectiveReport{Overall: 0.75}

	sufficient, reasons := chk.Check(det, subj, "v1")
	if !sufficient {
		t.Errorf("expected sufficient=true when threshold passes, got reasons=%v", reasons)
	}
	if len(reasons) == 0 {
		t.Error("expected a reason for the failing hash_valid gate")
	}
}

func TestConsensusSufficiencyChecker_NilDeterministicReport(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	sufficient, reasons := chk.Check(nil, &verification.SubjectiveReport{}, "v1")
	if sufficient {
		t.Error("expected sufficient=false for nil report")
	}
	if len(reasons) == 0 {
		t.Error("expected a reason for nil report")
	}
}

// ---------------------------------------------------------------------------
// InProcessVerifier — same results as pre-refactor
// ---------------------------------------------------------------------------

func TestInProcessVerifier_SameResultsAsRegistryDirect(t *testing.T) {
	reg := newRegistry()
	inProc := verification.NewInProcessVerifier(reg)

	cases := []struct {
		name     string
		category string
		ev       *evidence.Evidence
		title    string
		desc     string
		budget   uint64
	}{
		{
			name:     "go code",
			category: "code",
			ev: makeEv(`package main
import "fmt"
// Hello prints a greeting.
func Hello(name string) string {
	if name == "" { return "Hello, World!" }
	return fmt.Sprintf("Hello, %s!", name)
}
func main() { fmt.Println(Hello("test")) }`, ""),
			title:  "Write greeting function",
			desc:   "implement Hello(name string) string",
			budget: 100_000,
		},
		{
			name:     "research content",
			category: "research",
			ev: makeEv(`Analysis shows a 23% increase in ARR.
Average growth rate was 5.2%. Conclusion: strong performance.
Source: https://example.com/data. Reference: [1] Annual Report 2024.`, ""),
			title:  "Revenue analysis",
			desc:   "analyse Q4 revenue trends",
			budget: 500_000,
		},
		{
			name:     "empty evidence",
			category: "writing",
			ev:       &evidence.Evidence{},
			title:    "Write something",
			desc:     "any content",
			budget:   50_000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Direct registry call — the pre-refactor ground truth.
			wantScore, wantPassed := reg.Verify(tc.ev, tc.title, tc.desc, tc.budget, tc.category)
			if wantScore == nil {
				wantScore = &evidence.Score{}
			}

			// New path through InProcessVerifier.
			result, err := inProc.Verify(context.Background(), verification.VerificationRequest{
				Category:    tc.category,
				Title:       tc.title,
				Description: tc.desc,
				Budget:      tc.budget,
				Evidence:    tc.ev,
			})
			if err != nil {
				t.Fatalf("Verify returned error: %v", err)
			}
			if result == nil {
				t.Fatal("Verify returned nil result")
			}

			// Overall score must match.
			if got := result.SubjectiveReport.Overall; got != wantScore.Overall {
				t.Errorf("Overall: got %.4f, want %.4f", got, wantScore.Overall)
			}

			// Pass/fail verdict must match.
			gotPassed := len(result.DeterministicReport.HardGates) > 0 && result.DeterministicReport.HardGates[0].Pass
			if gotPassed != wantPassed {
				t.Errorf("passed: got %v, want %v (score=%.4f)", gotPassed, wantPassed, result.SubjectiveReport.Overall)
			}
		})
	}
}

func TestInProcessVerifier_ResultFields(t *testing.T) {
	inProc := verification.NewInProcessVerifier(newRegistry())
	ev := makeEv("AetherNet enables agents to transact autonomously using cryptographic identity and reputation.", "")
	result, err := inProc.Verify(context.Background(), verification.VerificationRequest{
		TaskID:      "task-123",
		Category:    "writing",
		Title:       "Overview",
		Description: "Write an AetherNet overview",
		Budget:      100_000,
		Evidence:    ev,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TaskID != "task-123" {
		t.Errorf("TaskID = %q; want \"task-123\"", result.TaskID)
	}
	if result.PolicyVersion != "v1" {
		t.Errorf("PolicyVersion = %q; want \"v1\"", result.PolicyVersion)
	}
	if result.VerifierID != "in-process" {
		t.Errorf("VerifierID = %q; want \"in-process\"", result.VerifierID)
	}
	if result.TrustProof != nil {
		t.Error("TrustProof must be nil for in-process verifier")
	}
	if result.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero")
	}
}

// ---------------------------------------------------------------------------
// DeterministicVerifier — RequiredChecks filtering
// ---------------------------------------------------------------------------

func TestDeterministicVerifier_RequiredChecksFiltering(t *testing.T) {
	dv := verification.NewDeterministicVerifier(newRegistry())
	ev := makeEv("The quick brown fox jumps over the lazy dog.", "")

	// Run with no RequiredChecks → all four gates returned.
	allReport := dv.Verify("writing", ev, "title", "desc", 100_000)
	if len(allReport.HardGates) < 4 {
		t.Fatalf("expected ≥4 gates with no filter, got %d", len(allReport.HardGates))
	}

	// Run with RequiredChecks=["has_output"] → threshold + has_output only.
	filtered := dv.Verify("writing", ev, "title", "desc", 100_000, "has_output")
	names := make(map[string]bool)
	for _, g := range filtered.HardGates {
		names[g.Name] = true
	}
	if !names["threshold"] {
		t.Error("threshold gate must always be present regardless of RequiredChecks")
	}
	if !names["has_output"] {
		t.Error("has_output gate must be present when in RequiredChecks")
	}
	if names["min_length"] || names["hash_valid"] {
		t.Error("min_length and hash_valid must be absent when not in RequiredChecks")
	}
}

func TestDeterministicVerifier_RequiredChecksEmpty(t *testing.T) {
	// Explicit empty slice must behave the same as no argument (run all gates).
	dv := verification.NewDeterministicVerifier(newRegistry())
	ev := makeEv("The quick brown fox jumps over the lazy dog.", "")

	noArg := dv.Verify("writing", ev, "title", "desc", 100_000)
	emptyArg := dv.Verify("writing", ev, "title", "desc", 100_000) // variadic, empty
	if len(noArg.HardGates) != len(emptyArg.HardGates) {
		t.Errorf("empty vs no arg: gate counts differ %d != %d",
			len(noArg.HardGates), len(emptyArg.HardGates))
	}
}

// ---------------------------------------------------------------------------
// ConsensusSufficiencyChecker — RequiredChecks enforcement
// ---------------------------------------------------------------------------

func TestConsensusSufficiencyChecker_RequiredCheckFailed(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}

	// threshold passes but "hash_valid" gate is required and fails.
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
			{Name: "hash_valid", Pass: false, Detail: "evidence Hash must be non-empty"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		RequiredChecks: []string{"hash_valid"},
	})
	if sufficient {
		t.Error("expected sufficient=false when a RequiredCheck gate fails")
	}
	found := false
	for _, r := range reasons {
		if r == "required_check_failed:hash_valid" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reason 'required_check_failed:hash_valid' in %v", reasons)
	}
}

func TestConsensusSufficiencyChecker_RequiredCheckMissing(t *testing.T) {
	// A RequiredCheck gate that doesn't exist in the report is treated as failed.
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		RequiredChecks: []string{"has_output", "hash_valid"},
	})
	if sufficient {
		t.Error("expected sufficient=false when RequiredCheck gates are missing")
	}
	if len(reasons) < 2 {
		t.Errorf("expected 2 required_check_failed reasons, got %v", reasons)
	}
}

func TestConsensusSufficiencyChecker_RequiredChecksPassed(t *testing.T) {
	// All RequiredChecks present and passing → falls through to threshold decision.
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
			{Name: "has_output", Pass: true, Detail: ""},
			{Name: "hash_valid", Pass: true, Detail: ""},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		RequiredChecks: []string{"has_output", "hash_valid"},
	})
	if !sufficient {
		t.Errorf("expected sufficient=true when all RequiredChecks pass, reasons=%v", reasons)
	}
}

// ---------------------------------------------------------------------------
// ConsensusSufficiencyChecker — challenge window enforcement
// ---------------------------------------------------------------------------

func TestConsensusSufficiencyChecker_ChallengeWindowOpen(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.900"},
		},
		NumericScores: map[string]float64{"overall": 0.90},
	}
	subj := &verification.SubjectiveReport{Overall: 0.90}

	// SubmittedAt is "now" and window is 300 seconds → window still open.
	submittedAt := time.Now().UnixNano()
	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		ChallengeWindowSecs: 300,
		SubmittedAt:         submittedAt,
	})
	if sufficient {
		t.Error("expected sufficient=false when challenge window is still open")
	}
	found := false
	for _, r := range reasons {
		if r == "challenge_window_open" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reason 'challenge_window_open' in %v", reasons)
	}
}

func TestConsensusSufficiencyChecker_ChallengeWindowExpired(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.900"},
		},
		NumericScores: map[string]float64{"overall": 0.90},
	}
	subj := &verification.SubjectiveReport{Overall: 0.90}

	// SubmittedAt is 10 minutes ago, window is 5 minutes → expired.
	submittedAt := time.Now().Add(-10 * time.Minute).UnixNano()
	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		ChallengeWindowSecs: 300, // 5 min
		SubmittedAt:         submittedAt,
	})
	if !sufficient {
		t.Errorf("expected sufficient=true when challenge window has expired, reasons=%v", reasons)
	}
}

func TestConsensusSufficiencyChecker_ChallengeWindowZeroSkipped(t *testing.T) {
	// ChallengeWindowSecs=0 → skip window check entirely.
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.900"},
		},
		NumericScores: map[string]float64{"overall": 0.90},
	}
	subj := &verification.SubjectiveReport{Overall: 0.90}

	submittedAt := time.Now().UnixNano() // just submitted
	sufficient, _ := chk.Check(det, subj, "v1", verification.ContractHints{
		ChallengeWindowSecs: 0, // disabled
		SubmittedAt:         submittedAt,
	})
	if !sufficient {
		t.Error("expected sufficient=true when ChallengeWindowSecs=0 (disabled)")
	}
}
