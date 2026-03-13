package canary

import (
	"log/slog"
	"time"
)

// Evaluator compares an actor's observed result against a canary's ground truth
// and emits a CalibrationSignal that captures the accuracy and severity of any
// deviation.
type Evaluator struct {
	store calibrationStore
}

// NewEvaluator returns an Evaluator that persists signals via store.
func NewEvaluator(store calibrationStore) *Evaluator {
	return &Evaluator{store: store}
}

// Evaluate compares the observed pass/fail and per-check results against the
// canary ground truth for the given actor. It creates, persists, and returns a
// CalibrationSignal.
//
// Correctness classification:
//   - "correct"   — overall pass matches AND all check results match
//   - "partial"   — overall pass matches but some checks differ
//   - "incorrect" — overall pass doesn't match
//
// Severity:
//   - "correct"   → 0.0
//   - "partial"   → 0.3 + 0.1 × (number of mismatched checks)
//   - "incorrect" → 0.7 if expected pass=true but observed false (missed good work)
//   - "incorrect" → 1.0 if expected pass=false but observed true  (approved bad work — most dangerous)
func (ev *Evaluator) Evaluate(
	canary *CanaryTask,
	actorID, role string,
	observedPass bool,
	observedChecks map[string]bool,
) *CalibrationSignal {
	passMatch := canary.ExpectedPass == observedPass

	// Count check-level mismatches (only when the canary has check expectations).
	mismatches := 0
	if len(canary.ExpectedCheckResults) > 0 {
		for checkName, expected := range canary.ExpectedCheckResults {
			observed, ok := observedChecks[checkName]
			if !ok || observed != expected {
				mismatches++
			}
		}
	}
	allChecksMatch := mismatches == 0

	var correctness string
	var severity float64

	switch {
	case passMatch && allChecksMatch:
		correctness = CorrectnessCorrect
		severity = 0.0
	case passMatch && !allChecksMatch:
		correctness = CorrectnessPartial
		severity = 0.3 + 0.1*float64(mismatches)
	default: // overall pass mismatch
		correctness = CorrectnessIncorrect
		if canary.ExpectedPass && !observedPass {
			severity = 0.7 // missed good work
		} else {
			severity = 1.0 // approved bad work — most dangerous
		}
	}

	sig := &CalibrationSignal{
		ID:                   signalID(canary.ID, actorID, role),
		CanaryID:             canary.ID,
		TaskID:               canary.TaskID,
		ActorID:              actorID,
		ActorRole:            role,
		Category:             canary.Category,
		CanaryType:           canary.CanaryType,
		ExpectedPass:         canary.ExpectedPass,
		ObservedPass:         observedPass,
		ExpectedCheckResults: canary.ExpectedCheckResults,
		ObservedCheckResults: observedChecks,
		Correctness:          correctness,
		Severity:             severity,
		Timestamp:            time.Now(),
	}

	if err := ev.store.PutSignal(sig); err != nil {
		slog.Error("canary: evaluator failed to persist signal",
			"id", sig.ID,
			"canary_id", canary.ID,
			"actor_id", actorID,
			"err", err,
		)
	}
	return sig
}
