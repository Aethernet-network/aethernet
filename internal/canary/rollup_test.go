package canary

import (
	"testing"
)

// ---------------------------------------------------------------------------
// ComputeCategoryCalibration
// ---------------------------------------------------------------------------

// TestComputeCategoryCalibration_FiltersCorrectly verifies that only signals
// matching the requested category are included in the result.
func TestComputeCategoryCalibration_FiltersCorrectly(t *testing.T) {
	signals := []*CalibrationSignal{
		{ActorID: "a1", Category: "code", Correctness: CorrectnessCorrect, Severity: 0.0},
		{ActorID: "a1", Category: "code", Correctness: CorrectnessIncorrect, Severity: 1.0},
		{ActorID: "a1", Category: "research", Correctness: CorrectnessCorrect, Severity: 0.0},
		{ActorID: "a1", Category: "writing", Correctness: CorrectnessPartial, Severity: 0.3},
	}

	got := ComputeCategoryCalibration(signals, "code")
	if got == nil {
		t.Fatal("expected non-nil result for category with matching signals")
	}
	if got.TotalSignals != 2 {
		t.Errorf("TotalSignals = %d; want 2", got.TotalSignals)
	}
	if got.CorrectCount != 1 {
		t.Errorf("CorrectCount = %d; want 1", got.CorrectCount)
	}
	if got.IncorrectCount != 1 {
		t.Errorf("IncorrectCount = %d; want 1", got.IncorrectCount)
	}
	if got.Accuracy != 0.5 {
		t.Errorf("Accuracy = %v; want 0.5", got.Accuracy)
	}
	wantAvgSeverity := (0.0 + 1.0) / 2.0
	if got.AvgSeverity != wantAvgSeverity {
		t.Errorf("AvgSeverity = %v; want %v", got.AvgSeverity, wantAvgSeverity)
	}
	if got.Category != "code" {
		t.Errorf("Category = %q; want %q", got.Category, "code")
	}
}

// TestComputeCategoryCalibration_ReturnsNilForNoMatch verifies that nil is
// returned when no signals match the requested category.
func TestComputeCategoryCalibration_ReturnsNilForNoMatch(t *testing.T) {
	signals := []*CalibrationSignal{
		{ActorID: "a1", Category: "code", Correctness: CorrectnessCorrect, Severity: 0.0},
	}
	got := ComputeCategoryCalibration(signals, "research")
	if got != nil {
		t.Errorf("expected nil for non-matching category, got %+v", got)
	}
}

// TestComputeCategoryCalibration_ReturnsNilForEmptySlice verifies that nil is
// returned when the signals slice is empty.
func TestComputeCategoryCalibration_ReturnsNilForEmptySlice(t *testing.T) {
	got := ComputeCategoryCalibration(nil, "code")
	if got != nil {
		t.Errorf("expected nil for empty signals, got %+v", got)
	}
}

// TestComputeCategoryCalibration_AllThreeCorrectness verifies correct
// counting for all three correctness values within a single category.
func TestComputeCategoryCalibration_AllThreeCorrectness(t *testing.T) {
	signals := []*CalibrationSignal{
		{Category: "writing", Correctness: CorrectnessCorrect, Severity: 0.0},
		{Category: "writing", Correctness: CorrectnessPartial, Severity: 0.3},
		{Category: "writing", Correctness: CorrectnessIncorrect, Severity: 1.0},
		{Category: "writing", Correctness: CorrectnessCorrect, Severity: 0.0},
	}
	got := ComputeCategoryCalibration(signals, "writing")
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.TotalSignals != 4 {
		t.Errorf("TotalSignals = %d; want 4", got.TotalSignals)
	}
	if got.CorrectCount != 2 {
		t.Errorf("CorrectCount = %d; want 2", got.CorrectCount)
	}
	if got.PartialCount != 1 {
		t.Errorf("PartialCount = %d; want 1", got.PartialCount)
	}
	if got.IncorrectCount != 1 {
		t.Errorf("IncorrectCount = %d; want 1", got.IncorrectCount)
	}
	// Smoothed accuracy: (2 correct + 0.5 × 1 partial) / 4 = 2.5/4 = 0.625
	wantAccuracy := (2.0 + 0.5*1.0) / 4.0
	if got.Accuracy != wantAccuracy {
		t.Errorf("Accuracy = %v; want %v (smoothed: correct+0.5×partial / total)", got.Accuracy, wantAccuracy)
	}
}

// ---------------------------------------------------------------------------
// CalibrationActionable
// ---------------------------------------------------------------------------

// TestCalibrationActionable_ReturnsFalseForNil verifies nil → false.
func TestCalibrationActionable_ReturnsFalseForNil(t *testing.T) {
	if CalibrationActionable(nil) {
		t.Error("CalibrationActionable(nil) = true; want false")
	}
}

// TestCalibrationActionable_ReturnsFalseBelow verifies that a count below
// MinCalibrationSamples is not actionable.
func TestCalibrationActionable_ReturnsFalseBelow(t *testing.T) {
	cal := &CategoryCalibration{TotalSignals: MinCalibrationSamples - 1}
	if CalibrationActionable(cal) {
		t.Errorf("CalibrationActionable with %d signals = true; want false (threshold %d)",
			cal.TotalSignals, MinCalibrationSamples)
	}
}

// TestCalibrationActionable_ReturnsTrueAtThreshold verifies that exactly
// MinCalibrationSamples signals is actionable.
func TestCalibrationActionable_ReturnsTrueAtThreshold(t *testing.T) {
	cal := &CategoryCalibration{TotalSignals: MinCalibrationSamples}
	if !CalibrationActionable(cal) {
		t.Errorf("CalibrationActionable with %d signals = false; want true (threshold %d)",
			cal.TotalSignals, MinCalibrationSamples)
	}
}

// TestCalibrationActionable_ReturnsTrueAboveThreshold verifies that more than
// MinCalibrationSamples signals is actionable.
func TestCalibrationActionable_ReturnsTrueAboveThreshold(t *testing.T) {
	cal := &CategoryCalibration{TotalSignals: MinCalibrationSamples + 10}
	if !CalibrationActionable(cal) {
		t.Errorf("CalibrationActionable with %d signals = false; want true", cal.TotalSignals)
	}
}

// ---------------------------------------------------------------------------
// Smoothed accuracy — partial signals contribute 0.5
// ---------------------------------------------------------------------------

// TestSmoothedAccuracy_Partials verifies that partial signals contribute 0.5
// to the smoothed accuracy formula rather than 0 (as incorrect would).
func TestSmoothedAccuracy_Partials(t *testing.T) {
	// 5 correct + 5 partial + 0 incorrect = 10 total
	// smoothed accuracy = (5 + 0.5×5) / 10 = 7.5/10 = 0.75
	var signals []*CalibrationSignal
	for i := 0; i < 5; i++ {
		signals = append(signals, &CalibrationSignal{
			ActorID:     "a1",
			Category:    "code",
			Correctness: CorrectnessCorrect,
			Severity:    0.0,
		})
	}
	for i := 0; i < 5; i++ {
		signals = append(signals, &CalibrationSignal{
			ActorID:     "a1",
			Category:    "code",
			Correctness: CorrectnessPartial,
			Severity:    0.3,
		})
	}

	got := ComputeCategoryCalibration(signals, "code")
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	wantAccuracy := (5.0 + 0.5*5.0) / 10.0 // 0.75
	if got.Accuracy != wantAccuracy {
		t.Errorf("Accuracy = %v; want %v (5 correct + 5 partial, smoothed)",
			got.Accuracy, wantAccuracy)
	}
}

// TestMinCalibrationSamples_Is20 is a regression guard verifying that
// MinCalibrationSamples was raised from 5 to 20. This constant drives both
// the canary and the router thresholds for "actionable" calibration data.
func TestMinCalibrationSamples_Is20(t *testing.T) {
	if MinCalibrationSamples != 20 {
		t.Errorf("MinCalibrationSamples = %d; want 20 (regression: was previously 5)",
			MinCalibrationSamples)
	}
	// 19 signals → not actionable; 20 signals → actionable.
	below := &CategoryCalibration{TotalSignals: 19}
	if CalibrationActionable(below) {
		t.Errorf("CalibrationActionable({TotalSignals:19}) = true; want false (threshold 20)")
	}
	at := &CategoryCalibration{TotalSignals: 20}
	if !CalibrationActionable(at) {
		t.Errorf("CalibrationActionable({TotalSignals:20}) = false; want true (threshold 20)")
	}
}

// ---------------------------------------------------------------------------
// AvgSeverity propagation in ComputeActorCalibration (regression guard)
// ---------------------------------------------------------------------------

// TestComputeActorCalibration_CategoryAvgSeverity verifies that the AvgSeverity
// field is correctly computed on per-category breakdown entries.
func TestComputeActorCalibration_CategoryAvgSeverity(t *testing.T) {
	signals := []*CalibrationSignal{
		{ActorID: "a1", Category: "code", Correctness: CorrectnessCorrect, Severity: 0.0},
		{ActorID: "a1", Category: "code", Correctness: CorrectnessIncorrect, Severity: 1.0},
	}
	ac := ComputeActorCalibration(signals)
	code, ok := ac.ByCategory["code"]
	if !ok {
		t.Fatal("ByCategory missing 'code'")
	}
	wantAvg := (0.0 + 1.0) / 2.0
	if code.AvgSeverity != wantAvg {
		t.Errorf("code.AvgSeverity = %v; want %v", code.AvgSeverity, wantAvg)
	}
}
