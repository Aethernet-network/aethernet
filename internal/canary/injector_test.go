package canary

import (
	"testing"
)

// TestNextCanary_CopiesExpectedEvidence verifies that NextCanary propagates
// ExpectedEvidence from the corpus template to the returned canary. Without
// this copy the Evaluator's truth model phase never runs on live canaries —
// ExpectedEvidence is nil for every injected canary and Phase 2 is unreachable.
func TestNextCanary_CopiesExpectedEvidence(t *testing.T) {
	cfg := InjectorConfig{
		Enabled:       true,
		InjectionRate: 1.0,
		Categories:    []string{"code"},
	}
	inj := NewInjector(cfg, newMemCanaryStore())

	got := inj.NextCanary("code")
	if got == nil {
		t.Fatal("NextCanary returned nil")
	}
	if got.ExpectedEvidence == nil {
		t.Fatal("ExpectedEvidence is nil on injected canary — truth model will never run")
	}
	if len(got.ExpectedEvidence.RequiredConcepts) == 0 {
		t.Error("ExpectedEvidence.RequiredConcepts is empty; expected corpus content to be copied")
	}
}

// TestNextCanary_CopiesExpectedEvidence_AllCategories verifies that the
// ExpectedEvidence copy holds for all corpus categories (code, research,
// writing), not just the requested category.
func TestNextCanary_CopiesExpectedEvidence_AllCategories(t *testing.T) {
	for _, cat := range []string{"code", "research", "writing"} {
		t.Run(cat, func(t *testing.T) {
			cfg := InjectorConfig{
				Enabled:       true,
				InjectionRate: 1.0,
				Categories:    []string{cat},
			}
			inj := NewInjector(cfg, newMemCanaryStore())
			got := inj.NextCanary(cat)
			if got == nil {
				t.Fatalf("NextCanary(%q) returned nil", cat)
			}
			if got.ExpectedEvidence == nil {
				t.Errorf("category %q: ExpectedEvidence is nil on injected canary", cat)
			}
		})
	}
}

// TestNextCanary_TruthModelReachable verifies the full injection → evaluation
// → truth model path. NextCanary must produce a canary with ExpectedEvidence
// populated so that Evaluator.Evaluate runs Phase 2 when observedOutput is set.
func TestNextCanary_TruthModelReachable(t *testing.T) {
	cfg := InjectorConfig{
		Enabled:       true,
		InjectionRate: 1.0,
		Categories:    []string{"code"},
	}
	inj := NewInjector(cfg, newMemCanaryStore())

	c := inj.NextCanary("code")
	if c == nil {
		t.Fatal("NextCanary returned nil")
	}
	if c.ExpectedEvidence == nil {
		t.Skip("corpus has no code canary with ExpectedEvidence — fix corpus before this test")
	}

	evalStore := newMemCalibrationStore()
	ev := NewEvaluator(evalStore)

	// Provide a non-empty observed output so Phase 2 (truth model) can run.
	// The exact content does not matter — we only verify that Phase 2 executed.
	sig := ev.Evaluate(c, "worker-tm", RoleWorker, true, nil, "func Add(a, b int) int { return a + b }")

	if sig.ComputedBy == ComputedByVerifierOnly {
		t.Errorf("ComputedBy=%q; want truth_model or truth_model+checks — "+
			"likely ExpectedEvidence was not copied from corpus template", sig.ComputedBy)
	}
}
