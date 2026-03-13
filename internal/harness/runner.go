package harness

import (
	"context"
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// Runner executes a set of BenchmarkCases against a VerificationService and
// produces a HarnessReport.
type Runner struct {
	svc    verification.VerificationService
	cases  []BenchmarkCase
	budget uint64 // default task budget sent to the verifier
}

// NewRunner returns a Runner that will evaluate cases against svc.
// A default task budget of 500_000 µAET is used for every request; override
// with WithBudget if needed.
func NewRunner(svc verification.VerificationService, cases []BenchmarkCase) *Runner {
	return &Runner{svc: svc, cases: cases, budget: 500_000}
}

// WithBudget returns a copy of r with a different default task budget.
func (r *Runner) WithBudget(µAET uint64) *Runner {
	return &Runner{svc: r.svc, cases: r.cases, budget: µAET}
}

// Run evaluates every BenchmarkCase and returns a HarnessReport.
func (r *Runner) Run(ctx context.Context) *HarnessReport {
	report := &HarnessReport{
		ResultsByTag: make(map[string]*TagReport),
	}

	var totalScore float64
	var totalDurationMs int64
	var inRangeCount int

	for _, c := range r.cases {
		res := r.runCase(ctx, c)
		report.Results = append(report.Results, *res)
		report.TotalCases++

		if res.Correct {
			report.CorrectCount++
		}
		if res.Passed && !c.ExpectedPass {
			report.FalsePositives++
		}
		if !res.Passed && c.ExpectedPass {
			report.FalseNegatives++
		}
		totalScore += res.Score
		totalDurationMs += res.DurationMs
		if res.ScoreInRange {
			inRangeCount++
		}

		// Update VerifierID from the first successful result.
		if report.VerifierID == "" && res.VerifierID != "" {
			report.VerifierID = res.VerifierID
		}

		// Per-tag breakdown.
		for _, tag := range c.Tags {
			tr := report.ResultsByTag[tag]
			if tr == nil {
				tr = &TagReport{Tag: tag}
				report.ResultsByTag[tag] = tr
			}
			tr.TotalCases++
			if !res.Correct {
				if res.Passed && !c.ExpectedPass {
					tr.FalsePositives++
				}
				if !res.Passed && c.ExpectedPass {
					tr.FalseNegatives++
				}
			}
		}
	}

	if report.TotalCases > 0 {
		report.Accuracy = float64(report.CorrectCount) / float64(report.TotalCases)
		report.AvgScore = totalScore / float64(report.TotalCases)
		report.AvgDurationMs = totalDurationMs / int64(report.TotalCases)
		report.CalibrationScore = float64(inRangeCount) / float64(report.TotalCases)
	}

	// Compute per-tag accuracy.
	for _, tr := range report.ResultsByTag {
		correct := tr.TotalCases - tr.FalsePositives - tr.FalseNegatives
		if tr.TotalCases > 0 {
			tr.Accuracy = float64(correct) / float64(tr.TotalCases)
		}
	}

	return report
}

// RunSingle runs the benchmark case with the given ID and returns its result.
// Returns nil when no case with that ID exists.
func (r *Runner) RunSingle(ctx context.Context, caseID string) *VerifierResult {
	for _, c := range r.cases {
		if c.ID == caseID {
			return r.runCase(ctx, c)
		}
	}
	return nil
}

// runCase executes one BenchmarkCase and returns its VerifierResult.
func (r *Runner) runCase(ctx context.Context, c BenchmarkCase) *VerifierResult {
	req := verification.VerificationRequest{
		TaskID:        c.ID,
		Category:      c.Category,
		PolicyVersion: "v1",
		Title:         c.Title,
		Description:   c.Description,
		Budget:        r.budget,
		Evidence:      c.Evidence,
	}

	start := time.Now()
	result, err := r.svc.Verify(ctx, req)
	durationMs := time.Since(start).Milliseconds()

	res := &VerifierResult{
		CaseID:     c.ID,
		DurationMs: durationMs,
	}

	if err != nil || result == nil {
		// Verification error → treat as fail with zero score.
		res.VerifierID = "error"
		res.ReasonCodes = []string{fmt.Sprintf("verify error: %v", err)}
	} else {
		res.VerifierID = result.VerifierID
		res.ReasonCodes = result.SubjectiveReport.ReasonCodes

		// Primary verdict: HardGates[0] is always the "threshold" gate set by
		// InProcessVerifier. Fall back to Confidence > 0.5 if no gates present.
		if len(result.DeterministicReport.HardGates) > 0 {
			res.Passed = result.DeterministicReport.HardGates[0].Pass
		} else {
			res.Passed = result.Confidence >= 0.5
		}
		res.Score = result.SubjectiveReport.Overall
	}

	res.Correct = res.Passed == c.ExpectedPass
	res.ScoreInRange = res.Score >= c.ExpectedMinScore && res.Score <= c.ExpectedMaxScore

	return res
}
