// Command aet-harness runs the built-in benchmark corpus against the
// in-process verifier and prints a calibration report.
//
// Usage:
//
//	go run ./cmd/aet-harness
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/harness"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

func main() {
	registry := evidence.NewVerifierRegistry()
	svc := verification.NewInProcessVerifier(registry)

	corpus := harness.DefaultCorpus()
	runner := harness.NewRunner(svc, corpus)

	fmt.Fprintln(os.Stderr, "Running harness against in-process verifier…")
	report := runner.Run(context.Background())

	printReport(report, corpus)
}

// printReport renders the HarnessReport as a formatted table to stdout.
func printReport(report *harness.HarnessReport, corpus []harness.BenchmarkCase) {
	const sep = "─────────────────────────────────────────────────────────────────"

	fmt.Println(sep)
	fmt.Println("  Validator Evaluation Harness Report")
	fmt.Println(sep)
	fmt.Printf("  Verifier      : %s\n", report.VerifierID)
	fmt.Printf("  Total cases   : %d\n", report.TotalCases)
	fmt.Printf("  Accuracy      : %.1f%%  (%d / %d correct)\n",
		report.Accuracy*100, report.CorrectCount, report.TotalCases)
	fmt.Printf("  False pos     : %d  (verifier said pass; expected fail)\n", report.FalsePositives)
	fmt.Printf("  False neg     : %d  (verifier said fail; expected pass)\n", report.FalseNegatives)
	fmt.Printf("  Calibration   : %.1f%%  (score within expected range)\n", report.CalibrationScore*100)
	fmt.Printf("  Avg score     : %.3f\n", report.AvgScore)
	fmt.Printf("  Avg latency   : %dms\n", report.AvgDurationMs)
	fmt.Println()

	// ── Per-tag breakdown ─────────────────────────────────────────────────
	fmt.Println("  By Tag:")
	fmt.Printf("  %-18s %6s  %8s  %4s  %4s\n", "Tag", "Cases", "Accuracy", "FP", "FN")
	fmt.Printf("  %-18s %6s  %8s  %4s  %4s\n",
		strings.Repeat("-", 18), "------", "--------", "----", "----")

	// Stable sort order for deterministic output.
	tags := make([]string, 0, len(report.ResultsByTag))
	for tag := range report.ResultsByTag {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		tr := report.ResultsByTag[tag]
		fmt.Printf("  %-18s %6d  %7.1f%%  %4d  %4d\n",
			tr.Tag, tr.TotalCases, tr.Accuracy*100, tr.FalsePositives, tr.FalseNegatives)
	}
	fmt.Println()

	// ── Per-category breakdown ────────────────────────────────────────────
	fmt.Println("  By Category:")
	fmt.Printf("  %-12s %6s  %8s  %4s  %4s\n", "Category", "Cases", "Accuracy", "FP", "FN")
	fmt.Printf("  %-12s %6s  %8s  %4s  %4s\n",
		strings.Repeat("-", 12), "------", "--------", "----", "----")

	type catStat struct {
		total, correct, fp, fn int
	}
	cats := make(map[string]*catStat)
	// build a lookup from case ID to BenchmarkCase for expected fields.
	caseMap := make(map[string]harness.BenchmarkCase, len(corpus))
	for _, c := range corpus {
		caseMap[c.ID] = c
	}
	for _, res := range report.Results {
		c := caseMap[res.CaseID]
		cs := cats[c.Category]
		if cs == nil {
			cs = &catStat{}
			cats[c.Category] = cs
		}
		cs.total++
		if res.Correct {
			cs.correct++
		}
		if res.Passed && !c.ExpectedPass {
			cs.fp++
		}
		if !res.Passed && c.ExpectedPass {
			cs.fn++
		}
	}
	catNames := make([]string, 0, len(cats))
	for c := range cats {
		catNames = append(catNames, c)
	}
	sort.Strings(catNames)
	for _, cat := range catNames {
		cs := cats[cat]
		acc := 0.0
		if cs.total > 0 {
			acc = float64(cs.correct) / float64(cs.total) * 100
		}
		fmt.Printf("  %-12s %6d  %7.1f%%  %4d  %4d\n", cat, cs.total, acc, cs.fp, cs.fn)
	}
	fmt.Println()

	// ── Incorrect predictions ─────────────────────────────────────────────
	var incorrect []harness.VerifierResult
	for _, res := range report.Results {
		if !res.Correct {
			incorrect = append(incorrect, res)
		}
	}
	if len(incorrect) == 0 {
		fmt.Println("  All predictions correct.")
	} else {
		fmt.Println("  Incorrect predictions (calibration gaps):")
		fmt.Printf("  %-20s  %-8s  %-8s  %6s  %s\n", "Case", "Expected", "Got", "Score", "Tags")
		fmt.Printf("  %-20s  %-8s  %-8s  %6s  %s\n",
			strings.Repeat("-", 20), "--------", "--------", "------", "----")
		for _, res := range incorrect {
			c := caseMap[res.CaseID]
			exp := "pass"
			if !c.ExpectedPass {
				exp = "fail"
			}
			got := "fail"
			if res.Passed {
				got = "pass"
			}
			fmt.Printf("  %-20s  %-8s  %-8s  %6.3f  %s\n",
				res.CaseID, exp, got, res.Score, strings.Join(c.Tags, ", "))
		}
	}
	fmt.Println()
	fmt.Println(sep)
}
