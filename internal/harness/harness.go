// Package harness provides a measurement tool for evaluating how accurately a
// VerificationService distinguishes quality work from bad work. It is NOT part
// of the production verification pipeline — it runs alongside it for testing
// and calibration purposes.
//
// Dependencies: only internal/verification and internal/evidence. Never imports
// autovalidator, tasks, escrow, or any other L3 package.
package harness

import "github.com/Aethernet-network/aethernet/internal/evidence"

// BenchmarkCase is a single labelled test case with known ground truth.
type BenchmarkCase struct {
	// ID uniquely identifies this case (e.g. "code-good-1").
	ID string

	// Title and Description are fed to the VerificationService exactly as a
	// real task would pass them.
	Title       string
	Description string

	// Category matches the evidence.VerifierRegistry routing keys
	// ("code", "research", "writing", etc.).
	Category string

	// Evidence is the work product submitted for evaluation.
	Evidence *evidence.Evidence

	// ExpectedPass is the human ground truth: should this submission pass
	// verification? The harness compares the verifier's verdict against this.
	ExpectedPass bool

	// ExpectedMinScore and ExpectedMaxScore define the calibration band.
	// A result's ScoreInRange is true when the verifier's overall score falls
	// within [ExpectedMinScore, ExpectedMaxScore].
	ExpectedMinScore float64
	ExpectedMaxScore float64

	// Tags classify the case for per-group reporting.
	// Typical values: "known-good", "known-bad", "adversarial", "edge-case".
	Tags []string
}

// VerifierResult holds the outcome of running one BenchmarkCase through a
// VerificationService, together with correctness metadata.
type VerifierResult struct {
	// CaseID is the ID of the BenchmarkCase that produced this result.
	CaseID string

	// VerifierID is taken from VerificationResult.VerifierID.
	VerifierID string

	// Passed is the verifier's pass/fail verdict (HardGates[0].Pass).
	Passed bool

	// Score is the verifier's overall numeric quality score (SubjectiveReport.Overall).
	Score float64

	// ReasonCodes are the failure reasons returned by the verifier (may be nil
	// when the verdict is pass).
	ReasonCodes []string

	// DurationMs is the wall-clock time taken by VerificationService.Verify in
	// milliseconds.
	DurationMs int64

	// Correct is true when Passed matches BenchmarkCase.ExpectedPass.
	Correct bool

	// ScoreInRange is true when Score is within
	// [BenchmarkCase.ExpectedMinScore, BenchmarkCase.ExpectedMaxScore].
	ScoreInRange bool
}

// TagReport is a per-tag accuracy breakdown within a HarnessReport.
type TagReport struct {
	Tag string

	// TotalCases is the number of BenchmarkCases carrying this tag.
	TotalCases int

	// Accuracy is CorrectCount / TotalCases for cases with this tag.
	Accuracy float64

	// FalsePositives is the count of cases where the verifier said pass but
	// ExpectedPass was false.
	FalsePositives int

	// FalseNegatives is the count of cases where the verifier said fail but
	// ExpectedPass was true.
	FalseNegatives int
}

// HarnessReport is the full evaluation result produced by Runner.Run.
type HarnessReport struct {
	// VerifierID identifies the verifier under test.
	VerifierID string

	TotalCases   int
	CorrectCount int

	// Accuracy is CorrectCount / TotalCases.
	Accuracy float64

	// FalsePositives counts cases where the verifier said pass but the ground
	// truth was fail.
	FalsePositives int

	// FalseNegatives counts cases where the verifier said fail but the ground
	// truth was pass.
	FalseNegatives int

	// AvgScore is the mean SubjectiveReport.Overall across all cases.
	AvgScore float64

	// AvgDurationMs is the mean per-case wall-clock time in milliseconds.
	AvgDurationMs int64

	// CalibrationScore is the fraction of cases where the verifier's numeric
	// score fell within [ExpectedMinScore, ExpectedMaxScore].
	CalibrationScore float64

	// ResultsByTag provides per-tag breakdowns. Keys are tag strings.
	ResultsByTag map[string]*TagReport

	// Results holds every individual VerifierResult for detailed inspection.
	Results []VerifierResult
}
