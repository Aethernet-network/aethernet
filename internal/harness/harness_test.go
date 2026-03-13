package harness

import (
	"context"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// mockVerifier is a test double for verification.VerificationService that
// returns a pre-configured verdict for every call.
type mockVerifier struct {
	pass  bool
	score float64
	id    string
}

func (m *mockVerifier) Verify(_ context.Context, req verification.VerificationRequest) (*verification.VerificationResult, error) {
	return &verification.VerificationResult{
		TaskID:     req.TaskID,
		VerifierID: m.id,
		SubjectiveReport: verification.SubjectiveReport{
			Overall: m.score,
		},
		DeterministicReport: verification.DeterministicReport{
			HardGates: []verification.GateResult{
				{Name: "threshold", Pass: m.pass},
			},
		},
		Confidence:    m.score,
		PolicyVersion: "v1",
		Timestamp:     time.Now(),
	}, nil
}

// TestRunnerAccuracy verifies the math for CorrectCount, FalsePositives,
// FalseNegatives, Accuracy, and CalibrationScore.
func TestRunnerAccuracy(t *testing.T) {
	// Build a small corpus with known ground truth.
	cases := []BenchmarkCase{
		{
			ID: "pass-expect-pass", ExpectedPass: true,
			ExpectedMinScore: 0.5, ExpectedMaxScore: 1.0,
			Evidence: &evidence.Evidence{Hash: "sha256:aa", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
		{
			ID: "pass-expect-fail", ExpectedPass: false,
			ExpectedMinScore: 0.0, ExpectedMaxScore: 0.4,
			Evidence: &evidence.Evidence{Hash: "sha256:bb", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
		{
			ID: "pass-expect-pass-2", ExpectedPass: true,
			ExpectedMinScore: 0.5, ExpectedMaxScore: 1.0,
			Evidence: &evidence.Evidence{Hash: "sha256:cc", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
	}

	// Mock always says pass with score 0.8.
	mock := &mockVerifier{pass: true, score: 0.8, id: "mock"}
	r := NewRunner(mock, cases)
	report := r.Run(context.Background())

	if report.TotalCases != 3 {
		t.Fatalf("TotalCases: want 3, got %d", report.TotalCases)
	}
	// "pass-expect-pass" and "pass-expect-pass-2" are correct; "pass-expect-fail" is wrong.
	if report.CorrectCount != 2 {
		t.Errorf("CorrectCount: want 2, got %d", report.CorrectCount)
	}
	if report.FalsePositives != 1 {
		t.Errorf("FalsePositives: want 1, got %d", report.FalsePositives)
	}
	if report.FalseNegatives != 0 {
		t.Errorf("FalseNegatives: want 0, got %d", report.FalseNegatives)
	}
	wantAccuracy := 2.0 / 3.0
	if report.Accuracy < wantAccuracy-0.001 || report.Accuracy > wantAccuracy+0.001 {
		t.Errorf("Accuracy: want %.4f, got %.4f", wantAccuracy, report.Accuracy)
	}
	// Score 0.8 is within [0.5,1.0] for case 1 and 3 → in range.
	// Score 0.8 is NOT within [0.0,0.4] for case 2 → out of range.
	wantCalib := 2.0 / 3.0
	if report.CalibrationScore < wantCalib-0.001 || report.CalibrationScore > wantCalib+0.001 {
		t.Errorf("CalibrationScore: want %.4f, got %.4f", wantCalib, report.CalibrationScore)
	}
}

// TestRunnerFalseNegatives verifies false-negative counting when the mock always fails.
func TestRunnerFalseNegatives(t *testing.T) {
	cases := []BenchmarkCase{
		{
			ID: "fail-expect-pass", ExpectedPass: true,
			ExpectedMinScore: 0.0, ExpectedMaxScore: 1.0,
			Evidence: &evidence.Evidence{Hash: "sha256:dd", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
		{
			ID: "fail-expect-fail", ExpectedPass: false,
			ExpectedMinScore: 0.0, ExpectedMaxScore: 0.5,
			Evidence: &evidence.Evidence{Hash: "sha256:ee", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
	}

	mock := &mockVerifier{pass: false, score: 0.1, id: "mock"}
	r := NewRunner(mock, cases)
	report := r.Run(context.Background())

	if report.FalseNegatives != 1 {
		t.Errorf("FalseNegatives: want 1, got %d", report.FalseNegatives)
	}
	if report.FalsePositives != 0 {
		t.Errorf("FalsePositives: want 0, got %d", report.FalsePositives)
	}
}

// TestRunnerTagBreakdown verifies that per-tag counts are correct.
func TestRunnerTagBreakdown(t *testing.T) {
	cases := []BenchmarkCase{
		{
			ID: "c1", ExpectedPass: true, Tags: []string{"known-good"},
			ExpectedMinScore: 0.0, ExpectedMaxScore: 1.0,
			Evidence: &evidence.Evidence{Hash: "sha256:11", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
		{
			ID: "c2", ExpectedPass: false, Tags: []string{"known-bad"},
			ExpectedMinScore: 0.0, ExpectedMaxScore: 1.0,
			Evidence: &evidence.Evidence{Hash: "sha256:22", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
		{
			ID: "c3", ExpectedPass: true, Tags: []string{"known-good", "edge-case"},
			ExpectedMinScore: 0.0, ExpectedMaxScore: 1.0,
			Evidence: &evidence.Evidence{Hash: "sha256:33", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
	}

	// Mock always passes.
	mock := &mockVerifier{pass: true, score: 0.9, id: "mock"}
	r := NewRunner(mock, cases)
	report := r.Run(context.Background())

	kg := report.ResultsByTag["known-good"]
	if kg == nil {
		t.Fatal("known-good tag missing from ResultsByTag")
	}
	if kg.TotalCases != 2 {
		t.Errorf("known-good TotalCases: want 2, got %d", kg.TotalCases)
	}
	// c1 and c3 both expect pass, mock says pass → both correct.
	if kg.FalsePositives != 0 {
		t.Errorf("known-good FalsePositives: want 0, got %d", kg.FalsePositives)
	}

	kb := report.ResultsByTag["known-bad"]
	if kb == nil {
		t.Fatal("known-bad tag missing from ResultsByTag")
	}
	// c2 expects fail but mock passes → 1 false positive.
	if kb.FalsePositives != 1 {
		t.Errorf("known-bad FalsePositives: want 1, got %d", kb.FalsePositives)
	}

	ec := report.ResultsByTag["edge-case"]
	if ec == nil {
		t.Fatal("edge-case tag missing from ResultsByTag")
	}
	if ec.TotalCases != 1 {
		t.Errorf("edge-case TotalCases: want 1, got %d", ec.TotalCases)
	}
}

// TestRunSingle returns the correct result for a given case ID.
func TestRunSingle(t *testing.T) {
	cases := []BenchmarkCase{
		{
			ID: "alpha", ExpectedPass: true,
			ExpectedMinScore: 0.6, ExpectedMaxScore: 1.0,
			Evidence: &evidence.Evidence{Hash: "sha256:aa", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
		{
			ID: "beta", ExpectedPass: false,
			ExpectedMinScore: 0.0, ExpectedMaxScore: 0.4,
			Evidence: &evidence.Evidence{Hash: "sha256:bb", OutputType: "text", OutputSize: 10, Summary: "x"},
		},
	}
	mock := &mockVerifier{pass: true, score: 0.8, id: "mock"}
	r := NewRunner(mock, cases)

	res := r.RunSingle(context.Background(), "beta")
	if res == nil {
		t.Fatal("RunSingle returned nil for case 'beta'")
	}
	if res.CaseID != "beta" {
		t.Errorf("CaseID: want beta, got %s", res.CaseID)
	}
	// mock passes but beta expects fail → not correct
	if res.Correct {
		t.Error("expected Correct=false for false-positive case")
	}

	// Unknown ID returns nil.
	if r.RunSingle(context.Background(), "nonexistent") != nil {
		t.Error("expected nil for unknown case ID")
	}
}

// TestCorpusMinSize verifies the default corpus has at least 20 cases.
func TestCorpusMinSize(t *testing.T) {
	corpus := DefaultCorpus()
	if len(corpus) < 20 {
		t.Errorf("DefaultCorpus: want at least 20 cases, got %d", len(corpus))
	}
}

// TestCorpusValidEvidence checks that every case has a non-empty Hash and
// non-zero OutputSize.
func TestCorpusValidEvidence(t *testing.T) {
	for _, c := range DefaultCorpus() {
		if c.Evidence == nil {
			t.Errorf("case %s: Evidence is nil", c.ID)
			continue
		}
		if c.Evidence.Hash == "" {
			t.Errorf("case %s: Evidence.Hash is empty", c.ID)
		}
		if c.Evidence.OutputSize == 0 {
			t.Errorf("case %s: Evidence.OutputSize is 0", c.ID)
		}
	}
}

// TestCorpusUniqueIDs verifies that every BenchmarkCase has a unique ID.
func TestCorpusUniqueIDs(t *testing.T) {
	seen := make(map[string]bool)
	for _, c := range DefaultCorpus() {
		if c.ID == "" {
			t.Error("found a BenchmarkCase with empty ID")
			continue
		}
		if seen[c.ID] {
			t.Errorf("duplicate BenchmarkCase ID: %s", c.ID)
		}
		seen[c.ID] = true
	}
}

// TestCorpusScoreRangeValid verifies that ExpectedMinScore <= ExpectedMaxScore
// for every case.
func TestCorpusScoreRangeValid(t *testing.T) {
	for _, c := range DefaultCorpus() {
		if c.ExpectedMinScore > c.ExpectedMaxScore {
			t.Errorf("case %s: ExpectedMinScore (%.2f) > ExpectedMaxScore (%.2f)",
				c.ID, c.ExpectedMinScore, c.ExpectedMaxScore)
		}
	}
}

// TestRunnerWithRealVerifier exercises the Runner against the real InProcessVerifier
// to confirm integration compiles and executes without panics.
func TestRunnerWithRealVerifier(t *testing.T) {
	registry := evidence.NewVerifierRegistry()
	svc := verification.NewInProcessVerifier(registry)
	r := NewRunner(svc, DefaultCorpus())
	report := r.Run(context.Background())
	if report.TotalCases != len(DefaultCorpus()) {
		t.Errorf("TotalCases: want %d, got %d", len(DefaultCorpus()), report.TotalCases)
	}
	if report.VerifierID == "" {
		t.Error("VerifierID should be set from real verifier")
	}
}
