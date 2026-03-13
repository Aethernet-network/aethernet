package replay

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/canary"
)

// ---------------------------------------------------------------------------
// Stub calibration source
// ---------------------------------------------------------------------------

// stubCalibrationSource satisfies calibrationSource for testing. It returns a
// fixed result (or error) for any actorID/category combination.
type stubCalibrationSource struct {
	result *canary.CategoryCalibration
	err    error
}

func (s *stubCalibrationSource) CategoryCalibrationForActor(_, _ string) (*canary.CategoryCalibration, error) {
	return s.result, s.err
}

// ---------------------------------------------------------------------------
// Helpers — policies that reach the calibration-adjusted step
// ---------------------------------------------------------------------------

// calibrationOnlyPolicy returns a ReplayPolicy where only the baseline random
// sample (step h / g in ShouldReplay) can fire, so the calibration adjustment
// is the sole driver of the sampling decision. All deterministic triggers and
// probationary rates are disabled.
func calibrationOnlyPolicy(sampleRate float64) ReplayPolicy {
	return ReplayPolicy{
		SampleRate:             sampleRate,
		NewAgentSampleRate:     0.0,
		GenerationSampleRate:   0.0,
		AlwaysReplayChallenged: false,
		AlwaysReplayAnomalies:  false,
		LowConfidenceThreshold: 0.0, // disable; confidence is always above this
	}
}

// callShouldReplay calls ShouldReplay with established-agent / no-flag / high-
// confidence inputs so that only the calibration-adjusted step (or baseline
// sample) can fire.
func callShouldReplay(c *ReplayCoordinator, taskID, agentID, category string) (bool, string) {
	return c.ShouldReplay(
		taskID, agentID, category,
		0.90,  // confidence — above any threshold
		false, // generationEligible
		false, // challenged
		nil,   // anomalyFlags
		50,    // agentTaskCount — well above probation threshold
	)
}

// ---------------------------------------------------------------------------
// TestShouldReplay_NoCalibrationSource_ExistingBehaviorUnchanged
// ---------------------------------------------------------------------------

// TestShouldReplay_NoCalibrationSource_ExistingBehaviorUnchanged verifies that
// when no calibration source is set, ShouldReplay behaves identically to its
// pre-calibration behaviour. LastCalibrationDecision returns nil.
func TestShouldReplay_NoCalibrationSource_ExistingBehaviorUnchanged(t *testing.T) {
	// SampleRate=0 → never fires; no triggers enabled.
	coord := NewReplayCoordinator(calibrationOnlyPolicy(0.0), newMemStore())
	// SetCalibrationSource NOT called.

	ok, _ := callShouldReplay(coord, "task-nc", "agent-nc", "code")
	if ok {
		t.Error("expected ShouldReplay=false when SampleRate=0 and no calibration source")
	}
	if coord.LastCalibrationDecision() != nil {
		t.Error("LastCalibrationDecision should be nil when no calibration source is configured")
	}
}

// ---------------------------------------------------------------------------
// TestShouldReplay_UncertainActor_BelowThreshold_2x
// ---------------------------------------------------------------------------

// TestShouldReplay_UncertainActor_BelowThreshold_2x verifies that when the
// calibration source returns nil (no data for the actor/category), ShouldReplay
// doubles the effective sample rate and records a "below_threshold_2x" decision.
func TestShouldReplay_UncertainActor_BelowThreshold_2x(t *testing.T) {
	// base 0.5 × 2 = 1.0 → always fires.
	coord := NewReplayCoordinator(calibrationOnlyPolicy(0.5), newMemStore())
	coord.SetCalibrationSource(&stubCalibrationSource{result: nil})

	ok, _ := callShouldReplay(coord, "task-unc", "agent-unc", "code")
	if !ok {
		t.Error("expected ShouldReplay=true for uncertain actor with 2× adjusted rate (0.5×2=1.0)")
	}

	dec := coord.LastCalibrationDecision()
	if dec == nil {
		t.Fatal("LastCalibrationDecision must not be nil after calibration-aware call")
	}
	if dec.Reason != "below_threshold_2x" {
		t.Errorf("Reason = %q; want %q", dec.Reason, "below_threshold_2x")
	}
	if dec.AdjustedRate != 1.0 {
		t.Errorf("AdjustedRate = %v; want 1.0 (base 0.5 × 2)", dec.AdjustedRate)
	}
	if dec.BaseRate != 0.5 {
		t.Errorf("BaseRate = %v; want 0.5", dec.BaseRate)
	}
	if dec.ActorID != "agent-unc" {
		t.Errorf("ActorID = %q; want %q", dec.ActorID, "agent-unc")
	}
	if dec.Category != "code" {
		t.Errorf("Category = %q; want %q", dec.Category, "code")
	}
}

// TestShouldReplay_UncertainActor_BelowMinSamples_2x verifies that when the
// calibration source returns data but below MinCalibrationSamples, the same
// 2× adjustment applies.
func TestShouldReplay_UncertainActor_BelowMinSamples_2x(t *testing.T) {
	tooFew := &canary.CategoryCalibration{
		Category:     "code",
		TotalSignals: canary.MinCalibrationSamples - 1,
		Accuracy:     1.0, // perfect accuracy but ignored below threshold
	}
	coord := NewReplayCoordinator(calibrationOnlyPolicy(0.5), newMemStore())
	coord.SetCalibrationSource(&stubCalibrationSource{result: tooFew})

	callShouldReplay(coord, "task-few", "agent-few", "code")

	dec := coord.LastCalibrationDecision()
	if dec == nil {
		t.Fatal("expected non-nil CalibrationDecision")
	}
	if dec.Reason != "below_threshold_2x" {
		t.Errorf("Reason = %q; want %q", dec.Reason, "below_threshold_2x")
	}
	if dec.SampleCount != canary.MinCalibrationSamples-1 {
		t.Errorf("SampleCount = %d; want %d", dec.SampleCount, canary.MinCalibrationSamples-1)
	}
}

// ---------------------------------------------------------------------------
// TestShouldReplay_WeakActor_3x
// ---------------------------------------------------------------------------

// TestShouldReplay_WeakActor_3x verifies that when an actor has actionable
// calibration data with accuracy < 0.6, ShouldReplay applies a 3× multiplier.
func TestShouldReplay_WeakActor_3x(t *testing.T) {
	// base 0.4 × 3 = 1.2 → always fires.
	cal := &canary.CategoryCalibration{
		Category:     "code",
		TotalSignals: 10,
		CorrectCount: 2,
		Accuracy:     0.2, // clearly weak
		AvgSeverity:  0.8,
	}
	coord := NewReplayCoordinator(calibrationOnlyPolicy(0.4), newMemStore())
	coord.SetCalibrationSource(&stubCalibrationSource{result: cal})

	ok, _ := callShouldReplay(coord, "task-weak", "agent-weak", "code")
	if !ok {
		t.Error("expected ShouldReplay=true for weak actor with 3× adjusted rate (0.4×3=1.2)")
	}

	dec := coord.LastCalibrationDecision()
	if dec == nil {
		t.Fatal("expected non-nil CalibrationDecision")
	}
	if dec.Reason != "weak_calibration_3x" {
		t.Errorf("Reason = %q; want %q", dec.Reason, "weak_calibration_3x")
	}
	wantRate := 0.4 * 3
	const eps = 1e-9
	if dec.AdjustedRate < wantRate-eps || dec.AdjustedRate > wantRate+eps {
		t.Errorf("AdjustedRate = %v; want %v (±%v)", dec.AdjustedRate, wantRate, eps)
	}
	if dec.Accuracy != 0.2 {
		t.Errorf("Accuracy = %v; want 0.2", dec.Accuracy)
	}
	if dec.SampleCount != 10 {
		t.Errorf("SampleCount = %d; want 10", dec.SampleCount)
	}
	if dec.AvgSeverity != 0.8 {
		t.Errorf("AvgSeverity = %v; want 0.8", dec.AvgSeverity)
	}
}

// ---------------------------------------------------------------------------
// TestShouldReplay_StrongActor_0_5x
// ---------------------------------------------------------------------------

// TestShouldReplay_StrongActor_0_5x verifies that when an actor has actionable
// calibration data with accuracy > 0.9, ShouldReplay applies a 0.5× multiplier,
// halving the base sample rate.
func TestShouldReplay_StrongActor_0_5x(t *testing.T) {
	// base 1.0 × 0.5 = 0.5 — AdjustedRate is halved. Decision is recorded.
	cal := &canary.CategoryCalibration{
		Category:     "code",
		TotalSignals: 20,
		CorrectCount: 19,
		Accuracy:     0.95, // > 0.9 → strong
		AvgSeverity:  0.02,
	}
	coord := NewReplayCoordinator(calibrationOnlyPolicy(1.0), newMemStore())
	coord.SetCalibrationSource(&stubCalibrationSource{result: cal})

	// Call ShouldReplay; the main assertion is on the CalibrationDecision fields.
	callShouldReplay(coord, "task-strong", "agent-strong", "code")

	dec := coord.LastCalibrationDecision()
	if dec == nil {
		t.Fatal("expected non-nil CalibrationDecision")
	}
	if dec.Reason != "strong_calibration_0.5x" {
		t.Errorf("Reason = %q; want %q", dec.Reason, "strong_calibration_0.5x")
	}
	wantRate := 1.0 * 0.5
	if dec.AdjustedRate != wantRate {
		t.Errorf("AdjustedRate = %v; want %v (base 1.0 × 0.5)", dec.AdjustedRate, wantRate)
	}
	if dec.BaseRate != 1.0 {
		t.Errorf("BaseRate = %v; want 1.0", dec.BaseRate)
	}
	if dec.Accuracy != 0.95 {
		t.Errorf("Accuracy = %v; want 0.95", dec.Accuracy)
	}
	if dec.SampleCount != 20 {
		t.Errorf("SampleCount = %d; want 20", dec.SampleCount)
	}
}

// TestShouldReplay_StrongActor_ReducedRate_NeverFires verifies that with base
// SampleRate=0.0, a strong actor (0.5×) still never triggers random sampling.
func TestShouldReplay_StrongActor_ReducedRate_NeverFires(t *testing.T) {
	cal := &canary.CategoryCalibration{
		Category:     "research",
		TotalSignals: 15,
		Accuracy:     0.93,
	}
	coord := NewReplayCoordinator(calibrationOnlyPolicy(0.0), newMemStore())
	coord.SetCalibrationSource(&stubCalibrationSource{result: cal})

	ok, _ := callShouldReplay(coord, "task-snf", "agent-snf", "research")
	if ok {
		t.Error("expected ShouldReplay=false: 0.0×0.5=0.0 → never fires")
	}
	dec := coord.LastCalibrationDecision()
	if dec == nil {
		t.Fatal("expected non-nil CalibrationDecision even when not firing")
	}
	if dec.Reason != "strong_calibration_0.5x" {
		t.Errorf("Reason = %q; want %q", dec.Reason, "strong_calibration_0.5x")
	}
}

// ---------------------------------------------------------------------------
// TestCalibrationDecision_ContainsCorrectMetrics
// ---------------------------------------------------------------------------

// TestCalibrationDecision_ContainsCorrectMetrics verifies that all fields of
// the CalibrationDecision are populated with the right values from the calibration
// source and the policy.
func TestCalibrationDecision_ContainsCorrectMetrics(t *testing.T) {
	cal := &canary.CategoryCalibration{
		Category:     "writing",
		TotalSignals: 8,
		CorrectCount: 3,
		Accuracy:     0.375, // < 0.6 → weak
		AvgSeverity:  0.55,
	}
	baseRate := 0.1
	coord := NewReplayCoordinator(calibrationOnlyPolicy(baseRate), newMemStore())
	coord.SetCalibrationSource(&stubCalibrationSource{result: cal})

	callShouldReplay(coord, "task-dm", "agent-dm", "writing")

	dec := coord.LastCalibrationDecision()
	if dec == nil {
		t.Fatal("expected non-nil CalibrationDecision")
	}

	checks := []struct {
		field string
		got   interface{}
		want  interface{}
	}{
		{"ActorID", dec.ActorID, "agent-dm"},
		{"Category", dec.Category, "writing"},
		{"SampleCount", dec.SampleCount, 8},
		{"Accuracy", dec.Accuracy, 0.375},
		{"AvgSeverity", dec.AvgSeverity, 0.55},
		{"BaseRate", dec.BaseRate, baseRate},
		{"AdjustedRate", dec.AdjustedRate, baseRate * 3},
		{"Reason", dec.Reason, "weak_calibration_3x"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("CalibrationDecision.%s = %v; want %v", c.field, c.got, c.want)
		}
	}
	if dec.Timestamp.IsZero() {
		t.Error("CalibrationDecision.Timestamp must not be zero")
	}
}
