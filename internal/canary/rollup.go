package canary

// CategoryCalibration holds accuracy metrics for a single task category.
type CategoryCalibration struct {
	Category       string  `json:"category"`
	TotalSignals   int     `json:"total_signals"`
	CorrectCount   int     `json:"correct_count"`
	PartialCount   int     `json:"partial_count"`
	IncorrectCount int     `json:"incorrect_count"`
	Accuracy       float64 `json:"accuracy"` // CorrectCount / TotalSignals
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
		}
	}

	return ac
}
