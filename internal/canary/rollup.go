package canary

// MinCalibrationSamples is the minimum number of signals required before a
// CategoryCalibration is considered actionable. Below this threshold the
// sample is too small to drive routing or scrutiny adjustments reliably.
const MinCalibrationSamples = 5

// CategoryCalibration holds accuracy metrics for a single task category.
type CategoryCalibration struct {
	Category       string  `json:"category"`
	TotalSignals   int     `json:"total_signals"`
	CorrectCount   int     `json:"correct_count"`
	PartialCount   int     `json:"partial_count"`
	IncorrectCount int     `json:"incorrect_count"`
	Accuracy       float64 `json:"accuracy"`     // CorrectCount / TotalSignals
	AvgSeverity    float64 `json:"avg_severity"` // mean severity across signals in this category
}

// ActorCalibration summarises all calibration signals for a single actor,
// with per-category breakdowns.
type ActorCalibration struct {
	ActorID        string                        `json:"actor_id"`
	TotalSignals   int                           `json:"total_signals"`
	CorrectCount   int                           `json:"correct_count"`
	PartialCount   int                           `json:"partial_count"`
	IncorrectCount int                           `json:"incorrect_count"`
	Accuracy       float64                       `json:"accuracy"`     // CorrectCount / TotalSignals
	AvgSeverity    float64                       `json:"avg_severity"` // mean severity across all signals
	ByCategory     map[string]*CategoryCalibration `json:"by_category"`
}

// ComputeActorCalibration aggregates a slice of CalibrationSignals for one
// actor into an ActorCalibration. The signals slice may be empty — in that
// case all counts are zero and Accuracy/AvgSeverity are 0.0.
//
// All signals are assumed to belong to the same actor; ActorID is taken from
// the first signal (empty string when signals is empty).
func ComputeActorCalibration(signals []*CalibrationSignal) *ActorCalibration {
	ac := &ActorCalibration{
		ByCategory: make(map[string]*CategoryCalibration),
	}
	if len(signals) == 0 {
		return ac
	}

	ac.ActorID = signals[0].ActorID
	var totalSeverity float64

	for _, sig := range signals {
		ac.TotalSignals++
		totalSeverity += sig.Severity

		switch sig.Correctness {
		case CorrectnessCorrect:
			ac.CorrectCount++
		case CorrectnessPartial:
			ac.PartialCount++
		case CorrectnessIncorrect:
			ac.IncorrectCount++
		}

		// Per-category accumulation.
		cat, ok := ac.ByCategory[sig.Category]
		if !ok {
			cat = &CategoryCalibration{Category: sig.Category}
			ac.ByCategory[sig.Category] = cat
		}
		cat.TotalSignals++
		cat.AvgSeverity += sig.Severity // accumulated; divided below
		switch sig.Correctness {
		case CorrectnessCorrect:
			cat.CorrectCount++
		case CorrectnessPartial:
			cat.PartialCount++
		case CorrectnessIncorrect:
			cat.IncorrectCount++
		}
	}

	if ac.TotalSignals > 0 {
		ac.Accuracy = float64(ac.CorrectCount) / float64(ac.TotalSignals)
		ac.AvgSeverity = totalSeverity / float64(ac.TotalSignals)
	}

	for _, cat := range ac.ByCategory {
		if cat.TotalSignals > 0 {
			cat.Accuracy = float64(cat.CorrectCount) / float64(cat.TotalSignals)
			cat.AvgSeverity = cat.AvgSeverity / float64(cat.TotalSignals)
		}
	}

	return ac
}

// ComputeCategoryCalibration filters signals to the given category and
// aggregates them into a CategoryCalibration. Returns nil when no signals
// match the category — callers should treat nil as "no data" rather than
// an error.
func ComputeCategoryCalibration(signals []*CalibrationSignal, category string) *CategoryCalibration {
	cat := &CategoryCalibration{Category: category}
	var totalSeverity float64

	for _, sig := range signals {
		if sig.Category != category {
			continue
		}
		cat.TotalSignals++
		totalSeverity += sig.Severity
		switch sig.Correctness {
		case CorrectnessCorrect:
			cat.CorrectCount++
		case CorrectnessPartial:
			cat.PartialCount++
		case CorrectnessIncorrect:
			cat.IncorrectCount++
		}
	}

	if cat.TotalSignals == 0 {
		return nil
	}
	cat.Accuracy = float64(cat.CorrectCount) / float64(cat.TotalSignals)
	cat.AvgSeverity = totalSeverity / float64(cat.TotalSignals)
	return cat
}

// CalibrationActionable reports whether a CategoryCalibration has enough
// signals to make routing or scrutiny decisions. Returns false when cal is
// nil or cal.TotalSignals < MinCalibrationSamples.
func CalibrationActionable(cal *CategoryCalibration) bool {
	return cal != nil && cal.TotalSignals >= MinCalibrationSamples
}
