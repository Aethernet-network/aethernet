package canary

import (
	"log/slog"
	"strings"
	"time"
)

// Evaluator compares an actor's observed result against a canary's ground truth
// and emits a CalibrationSignal that captures the accuracy and severity of any
// deviation.
//
// Evaluation runs in two phases:
//  1. Pass/check comparison (primary): observed verifier pass/fail vs. expected,
//     plus per-check results when available.
//  2. Truth model (supplementary, heuristic): keyword/length checks against
//     ExpectedEvidence when the canary has it populated and observed output is
//     provided. This phase can elevate or reduce severity relative to the
//     pass/check comparison, but is clearly marked as approximate.
type Evaluator struct {
	store calibrationStore
}

// NewEvaluator returns an Evaluator that persists signals via store.
func NewEvaluator(store calibrationStore) *Evaluator {
	return &Evaluator{store: store}
}

// Evaluate compares the observed result against the canary ground truth for
// the given actor and emits a CalibrationSignal.
//
// Parameters:
//   - canary:         the canary record with ExpectedPass and optionally ExpectedEvidence
//   - actorID:        the actor being evaluated (worker ID, replayer ID, etc.)
//   - role:           RoleWorker | RoleReplaySubmitter | RoleValidator
//   - observedPass:   the evidence verifier's verdict (true = passed threshold)
//   - observedChecks: per-check results from the verifier; nil = not available
//   - observedOutput: the raw output text submitted by the actor; "" = not available
//
// Phase 1 — Pass/check comparison (always runs):
//   - "correct"   — overall pass matches AND all checks match (or no checks expected)
//   - "partial"   — overall pass matches but some per-check results differ
//   - "incorrect" — overall pass doesn't match (severity 0.7 = missed, 1.0 = approved bad)
//
// Phase 2 — Truth model adjustment (runs when ExpectedEvidence is set AND observedOutput != ""):
//   NOTE: this is heuristic keyword/length matching, not semantic proof.
//   - If verifier passed (correct) but truth model score < 0.4:
//     correctness → partial, severity → 0.5 (verifier may be susceptible to surface patterns)
//   - If verifier failed (severity 0.7 = missed good work) but truth model score > 0.7:
//     severity → 0.4 (output may actually be good; verifier may be miscalibrated)
func (ev *Evaluator) Evaluate(
	canary *CanaryTask,
	actorID, role string,
	observedPass bool,
	observedChecks map[string]bool,
	observedOutput string,
) *CalibrationSignal {
	// ── Phase 1: pass/check comparison ──────────────────────────────────────

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

	// ── Phase 2: truth model (heuristic) ────────────────────────────────────

	computedBy := ComputedByVerifierOnly
	var truthScore float64 = -1 // sentinel: not computed
	var reqFound, forbidFound []string
	var outputLenOK *bool

	if canary.ExpectedEvidence != nil && observedOutput != "" {
		computedBy = ComputedByTruthModel
		if len(observedChecks) > 0 && len(canary.ExpectedCheckResults) > 0 {
			computedBy = ComputedByTruthModelAndChecks
		}

		ev2 := canary.ExpectedEvidence
		lower := strings.ToLower(observedOutput)

		// Required concepts: each missing concept penalises score by an equal share.
		reqMissing := 0
		for _, kw := range ev2.RequiredConcepts {
			if strings.Contains(lower, strings.ToLower(kw)) {
				reqFound = append(reqFound, kw)
			} else {
				reqMissing++
			}
		}

		// Forbidden concepts: each found concept penalises score.
		for _, kw := range ev2.ForbiddenConcepts {
			if strings.Contains(lower, strings.ToLower(kw)) {
				forbidFound = append(forbidFound, kw)
			}
		}

		// Output length check.
		if ev2.MinOutputLength > 0 {
			ok := len(observedOutput) >= ev2.MinOutputLength
			outputLenOK = &ok
		}

		// Compute truth model score: start at 1.0, subtract penalties.
		score := 1.0
		totalReq := len(ev2.RequiredConcepts)
		if totalReq > 0 {
			// Missing required concepts contribute up to 70% penalty.
			// This ensures that an output missing all required concepts scores
			// well below the 0.4 elevation threshold, triggering a partial signal.
			score -= 0.7 * float64(reqMissing) / float64(totalReq)
		}
		totalForbid := len(ev2.ForbiddenConcepts)
		if totalForbid > 0 {
			// Forbidden concepts found contribute up to 50% penalty.
			// Each found forbidden concept contributes an equal share of the penalty.
			score -= 0.5 * float64(len(forbidFound)) / float64(totalForbid)
		}
		if outputLenOK != nil && !*outputLenOK {
			score -= 0.1 // small penalty for being too short
		}
		if score < 0 {
			score = 0
		}
		truthScore = score

		// Adjust correctness/severity based on truth model disagreement.
		// All adjustments are documented as heuristic (see package comment).
		switch correctness {
		case CorrectnessCorrect:
			// Verifier approved AND truth model is weak: the submission may be
			// exploiting surface patterns rather than producing correct content.
			// Elevate to partial with increased severity.
			if truthScore < 0.4 {
				correctness = CorrectnessPartial
				severity = 0.5
			}
		case CorrectnessIncorrect:
			// Verifier rejected (severity=0.7, missed good work) BUT truth model
			// suggests the output actually looks correct. Reduce severity to
			// reflect possible verifier miscalibration.
			if canary.ExpectedPass && !observedPass && truthScore > 0.7 {
				severity = 0.4
			}
		}
	}

	// ── Build and persist signal ─────────────────────────────────────────────

	sig := &CalibrationSignal{
		ID:                     signalID(canary.ID, actorID, role),
		CanaryID:               canary.ID,
		TaskID:                 canary.TaskID,
		ActorID:                actorID,
		ActorRole:              role,
		Category:               canary.Category,
		CanaryType:             canary.CanaryType,
		ExpectedPass:           canary.ExpectedPass,
		ObservedPass:           observedPass,
		ExpectedCheckResults:   canary.ExpectedCheckResults,
		ObservedCheckResults:   observedChecks,
		Correctness:            correctness,
		Severity:               severity,
		ForbiddenConceptsFound: forbidFound,
		ComputedBy:             computedBy,
		Timestamp:              time.Now(),
	}

	// Only populate optional truth-model fields when the phase ran.
	if truthScore >= 0 {
		sig.TruthModelScore = truthScore
		if len(reqFound) > 0 {
			sig.RequiredConceptsFound = reqFound
		}
		sig.OutputLengthSatisfied = outputLenOK
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
