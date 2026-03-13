package canary

import (
	"testing"
)

// makeCanaryWithEvidence creates a CanaryTask with an ExpectedEvidence block
// for use in truth-model evaluator tests.
func makeCanaryWithEvidence(expectedPass bool, ev *ExpectedEvidence) *CanaryTask {
	c := NewCanaryTask("code", TypeKnownGood, expectedPass, nil, "sha256:test")
	c.ExpectedEvidence = ev
	return c
}

// TestEvaluate_TruthModel_MatchingEvidence verifies that when the observed
// output contains all required concepts and no forbidden ones, the truth model
// score is high and the signal remains correct/low-severity.
func TestEvaluate_TruthModel_MatchingEvidence(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := makeCanaryWithEvidence(true, &ExpectedEvidence{
		RequiredConcepts: []string{"func ", "return", "error"},
		MinOutputLength:  30,
	})

	output := "func processItem(id string) error {\n    return nil\n}\n"
	sig := ev.Evaluate(c, "worker-good", RoleWorker, true, nil, output)

	if sig.Correctness != CorrectnessCorrect {
		t.Errorf("Correctness = %q; want %q", sig.Correctness, CorrectnessCorrect)
	}
	if sig.Severity != 0.0 {
		t.Errorf("Severity = %v; want 0.0 (correct + truth model agrees)", sig.Severity)
	}
	if sig.TruthModelScore < 0.9 {
		t.Errorf("TruthModelScore = %v; want >= 0.9 for fully matching output", sig.TruthModelScore)
	}
	if sig.ComputedBy != ComputedByTruthModel {
		t.Errorf("ComputedBy = %q; want %q", sig.ComputedBy, ComputedByTruthModel)
	}
	if len(sig.RequiredConceptsFound) != 3 {
		t.Errorf("RequiredConceptsFound = %v; want 3 entries", sig.RequiredConceptsFound)
	}
}

// TestEvaluate_TruthModel_ForbiddenContent verifies that when the observed
// output contains forbidden concepts, the truth model score drops and severity
// is elevated from 0.0 (even though the verifier passed).
func TestEvaluate_TruthModel_ForbiddenContent(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := makeCanaryWithEvidence(true, &ExpectedEvidence{
		RequiredConcepts:  []string{"func "},
		ForbiddenConcepts: []string{"panic(", "os.Exit("},
	})

	// Output contains a required concept but also a forbidden one.
	output := "func doSomething() {\n    panic(\"not implemented\")\n}\n"
	sig := ev.Evaluate(c, "worker-bad", RoleWorker, true, nil, output)

	// Verifier passed (observedPass=true, expectedPass=true) so base is correct.
	// But truth model finds forbidden content → should elevate severity.
	if sig.TruthModelScore >= 1.0 {
		t.Errorf("TruthModelScore = %v; expected < 1.0 due to forbidden content", sig.TruthModelScore)
	}
	if len(sig.ForbiddenConceptsFound) == 0 {
		t.Error("ForbiddenConceptsFound should be non-empty when forbidden content is present")
	}
	// With one of two forbidden concepts found (panic( matched), score should drop.
	if sig.TruthModelScore >= 0.8 {
		t.Errorf("TruthModelScore = %v; expected < 0.8 with forbidden content", sig.TruthModelScore)
	}
}

// TestEvaluate_TruthModel_VerifierPassTruthModelFail verifies that when the
// verifier says pass but the truth model score is very low (< 0.4), severity
// is elevated above 0.0 — the signal indicates the verifier may be susceptible
// to surface patterns.
func TestEvaluate_TruthModel_VerifierPassTruthModelFail(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := makeCanaryWithEvidence(true, &ExpectedEvidence{
		RequiredConcepts: []string{"func ", "return", "error", "validate", "parse"},
		// Strict: many required concepts, none present in the output below.
	})

	// Output has none of the required concepts — very low truth model score.
	output := "This is a completely off-topic response that somehow passes keyword scoring."
	sig := ev.Evaluate(c, "worker-gaming", RoleWorker, true, nil, output)

	// Verifier passed, truth model fails hard → correctness elevated to partial,
	// severity elevated.
	if sig.Correctness != CorrectnessPartial {
		t.Errorf("Correctness = %q; want %q (truth model elevation)", sig.Correctness, CorrectnessPartial)
	}
	if sig.Severity < 0.4 {
		t.Errorf("Severity = %v; want >= 0.4 when verifier passes but truth model fails", sig.Severity)
	}
	if sig.TruthModelScore >= 0.4 {
		t.Errorf("TruthModelScore = %v; want < 0.4 for output missing all required concepts", sig.TruthModelScore)
	}
}

// TestEvaluate_TruthModel_VerifierFailTruthModelPass verifies that when the
// verifier says fail (expectedPass=true, observedPass=false) but the truth
// model score is high, severity is reduced from 0.7 to approximately 0.4 —
// indicating possible verifier miscalibration.
func TestEvaluate_TruthModel_VerifierFailTruthModelPass(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := makeCanaryWithEvidence(true, &ExpectedEvidence{
		RequiredConcepts: []string{"func ", "return"},
		// Few required concepts so it's easy for truth model to pass.
	})

	// Output has required concepts → truth model score should be high.
	output := "func compute(x int) int {\n    return x * 2\n}\n"
	sig := ev.Evaluate(c, "worker-penalised", RoleWorker, false, nil, output)

	// Verifier failed (observedPass=false, expectedPass=true) → base severity=0.7.
	// Truth model strong (>0.7) → reduced to 0.4.
	if sig.Severity != 0.4 {
		t.Errorf("Severity = %v; want 0.4 (verifier fail + truth model pass)", sig.Severity)
	}
	if sig.TruthModelScore <= 0.7 {
		t.Errorf("TruthModelScore = %v; want > 0.7 for output with all required concepts", sig.TruthModelScore)
	}
}

// TestEvaluate_TruthModel_OutputLengthCheck verifies that the MinOutputLength
// constraint is evaluated and OutputLengthSatisfied is populated correctly.
func TestEvaluate_TruthModel_OutputLengthCheck(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := makeCanaryWithEvidence(true, &ExpectedEvidence{
		MinOutputLength: 200,
	})

	// Short output (< 200 chars).
	short := "func f() {}"
	sig := ev.Evaluate(c, "w-short", RoleWorker, true, nil, short)

	if sig.OutputLengthSatisfied == nil {
		t.Fatal("OutputLengthSatisfied should not be nil when MinOutputLength is set")
	}
	if *sig.OutputLengthSatisfied {
		t.Error("OutputLengthSatisfied should be false for output shorter than MinOutputLength")
	}

	// Long output (>= 200 chars).
	long := "func processItems(items []string) error {\n" +
		"    for _, item := range items {\n" +
		"        if err := validate(item); err != nil {\n" +
		"            return fmt.Errorf(\"invalid item %q: %w\", item, err)\n" +
		"        }\n" +
		"    }\n" +
		"    return nil\n}\n"
	sig2 := ev.Evaluate(c, "w-long", RoleWorker, true, nil, long)

	if sig2.OutputLengthSatisfied == nil {
		t.Fatal("OutputLengthSatisfied should not be nil")
	}
	if !*sig2.OutputLengthSatisfied {
		t.Errorf("OutputLengthSatisfied should be true for output len=%d >= 200", len(long))
	}
}

// TestEvaluate_TruthModel_NoOutput_FallsBackToVerifierOnly verifies that when
// observedOutput is empty, the evaluator falls back to verifier-only scoring
// and ComputedBy = "verifier_only".
func TestEvaluate_TruthModel_NoOutput_FallsBackToVerifierOnly(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := makeCanaryWithEvidence(true, &ExpectedEvidence{
		RequiredConcepts: []string{"func ", "return"},
	})

	// Empty output: truth model skipped.
	sig := ev.Evaluate(c, "worker-noout", RoleWorker, true, nil, "")

	if sig.ComputedBy != ComputedByVerifierOnly {
		t.Errorf("ComputedBy = %q; want %q when output is empty", sig.ComputedBy, ComputedByVerifierOnly)
	}
	if sig.TruthModelScore != 0 {
		t.Errorf("TruthModelScore = %v; want 0 when truth model did not run", sig.TruthModelScore)
	}
}

// TestEvaluate_ObservedChecks_PassedThrough verifies that observed checks passed
// to Evaluate are preserved in the emitted signal.
func TestEvaluate_ObservedChecks_PassedThrough(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := NewCanaryTask("code", TypeKnownGood, true, map[string]bool{
		"has_output": true, "min_length": true,
	}, "sha256:checks-test")

	observed := map[string]bool{"has_output": true, "min_length": false}
	sig := ev.Evaluate(c, "w-checks", RoleWorker, true, observed, "some output")

	if sig.ObservedCheckResults == nil {
		t.Fatal("ObservedCheckResults should be non-nil when observedChecks is provided")
	}
	if sig.ObservedCheckResults["has_output"] != true {
		t.Errorf("has_output = %v; want true", sig.ObservedCheckResults["has_output"])
	}
	if sig.ObservedCheckResults["min_length"] != false {
		t.Errorf("min_length = %v; want false", sig.ObservedCheckResults["min_length"])
	}
	// 1 mismatch (min_length: expected true, observed false) → partial.
	if sig.Correctness != CorrectnessPartial {
		t.Errorf("Correctness = %q; want partial for 1 check mismatch", sig.Correctness)
	}
}

// TestEvaluate_TruthModelAndChecks_ComputedBy verifies that ComputedBy is set
// to "truth_model+checks" when both ExpectedEvidence and non-nil observedChecks
// with matching ExpectedCheckResults are present.
func TestEvaluate_TruthModelAndChecks_ComputedBy(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	c := NewCanaryTask("code", TypeKnownGood, true, map[string]bool{
		"has_output": true,
	}, "sha256:combo")
	c.ExpectedEvidence = &ExpectedEvidence{
		RequiredConcepts: []string{"func "},
	}

	sig := ev.Evaluate(c, "w-combo", RoleWorker, true,
		map[string]bool{"has_output": true}, "func f() {}")

	if sig.ComputedBy != ComputedByTruthModelAndChecks {
		t.Errorf("ComputedBy = %q; want %q", sig.ComputedBy, ComputedByTruthModelAndChecks)
	}
}
