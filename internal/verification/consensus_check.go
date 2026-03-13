package verification

import (
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// ReplayVerdict is the outcome of evaluating a completed ReplayJob against the
// original verification packet. It is defined here (rather than in
// internal/replay) to keep the type in the stable verification seam and avoid
// import cycles — internal/replay already imports internal/verification.
type ReplayVerdict struct {
	// Action is the recommended response:
	//   "no_action"          — replay agrees with original; nothing to do
	//   "flag_for_review"    — minor discrepancy; human review warranted
	//   "open_challenge"     — significant disagreement; challenge should be opened
	//   "slash_recommended"  — severe fraud signal; slash should be considered
	Action string

	// Reason is a human-readable explanation of the verdict.
	Reason string

	// MismatchCount is the number of checks that disagreed with the original.
	MismatchCount int

	// AnomalyFlags lists any anomaly indicators detected during replay.
	AnomalyFlags []string

	// SeverityScore ranges from 0.0 (clean) to 1.0 (severe fraud signal).
	SeverityScore float64
}

// ContractHints carries acceptance-contract fields needed by
// ConsensusSufficiencyChecker. All fields are optional; zero values apply
// backward-compatible defaults.
type ContractHints struct {
	// RequiredChecks lists gate names that must all appear as passing gates
	// in the DeterministicReport. Empty means no per-gate enforcement (only
	// the threshold gate governs the sufficiency decision).
	RequiredChecks []string

	// ChallengeWindowSecs is the number of seconds after result submission
	// before settlement is considered final. 0 means no window is enforced.
	ChallengeWindowSecs int64

	// SubmittedAt is the unix nanosecond timestamp of result submission.
	// Used with ChallengeWindowSecs to enforce the challenge window.
	// 0 means skip challenge-window enforcement even if ChallengeWindowSecs > 0.
	SubmittedAt int64

	// GenerationEligible indicates that the task is requesting
	// generation-eligible settlement. When true and ReplayabilityAssessment
	// is present and not replayable, sufficient is overridden to false.
	GenerationEligible bool

	// ReplayabilityAssessment carries the replay-material evaluation for this
	// request. Nil when no replay material was submitted.
	ReplayabilityAssessment *ReplayabilityAssessment

	// ReplayVerdict carries the result of a completed ReplayJob for this task.
	// Nil when no replay has been performed. When non-nil and Action is
	// "open_challenge" or "slash_recommended", sufficient is overridden to false.
	ReplayVerdict *ReplayVerdict
}

// ConsensusSufficiencyChecker decides whether combined deterministic and
// subjective reports meet the bar required for task settlement.
//
// sufficient is determined by the "threshold" hard gate in det.HardGates —
// this gate is set by DeterministicVerifier and captures the per-category
// threshold comparison that lives inside evidence.VerifierRegistry. Using the
// gate (rather than re-computing the threshold here) keeps the threshold table
// in a single place and avoids divergence.
//
// All failing hard gates contribute to the returned reason codes so callers
// can explain exactly why a task was held or rejected.
type ConsensusSufficiencyChecker struct{}

// Check evaluates det and subj and returns whether the evidence is sufficient
// for settlement together with human-readable reason codes for any failures.
//
// policyVersion is accepted for future policy-dispatch; currently ignored.
//
// An optional ContractHints may be passed as the last argument to enable
// acceptance-contract enforcement:
//   - RequiredChecks: all named gates must be present and passing in det.
//   - ChallengeWindowSecs + SubmittedAt: if the submission window is still
//     open, returns sufficient=false with reason "challenge_window_open".
//
// Existing callers that omit ContractHints are unaffected.
func (c *ConsensusSufficiencyChecker) Check(det *DeterministicReport, subj *SubjectiveReport, _ string, hints ...ContractHints) (sufficient bool, reasons []string) {
	if det == nil {
		return false, []string{"deterministic report is nil"}
	}

	var h ContractHints
	if len(hints) > 0 {
		h = hints[0]
	}

	// Challenge-window enforcement: if the task was submitted recently, hold
	// settlement until the window expires. This gives the poster time to
	// dispute before the auto-validator finalises.
	if h.ChallengeWindowSecs > 0 && h.SubmittedAt > 0 {
		submittedAt := time.Unix(0, h.SubmittedAt)
		windowEnd := submittedAt.Add(time.Duration(h.ChallengeWindowSecs) * time.Second)
		if time.Now().Before(windowEnd) {
			return false, []string{"challenge_window_open"}
		}
	}

	// Build a lookup of gate results from the deterministic report.
	gateResults := make(map[string]bool, len(det.HardGates))
	for _, gate := range det.HardGates {
		gateResults[gate.Name] = gate.Pass
	}

	// Enforce RequiredChecks: every named check must be present and passing.
	for _, check := range h.RequiredChecks {
		pass, found := gateResults[check]
		if !found || !pass {
			reasons = append(reasons, fmt.Sprintf("required_check_failed:%s", check))
		}
	}
	if len(reasons) > 0 {
		return false, reasons
	}

	// Collect reason codes from every failing gate.
	thresholdFound := false
	thresholdPass := false
	for _, gate := range det.HardGates {
		if !gate.Pass {
			msg := gate.Name
			if gate.Detail != "" {
				msg += ": " + gate.Detail
			}
			reasons = append(reasons, msg)
		}
		if gate.Name == "threshold" {
			thresholdFound = true
			thresholdPass = gate.Pass
		}
	}

	if thresholdFound {
		// Primary decision: the registry's per-category threshold comparison.
		sufficient = thresholdPass
	} else {
		// Fallback when no threshold gate is present: compare against the
		// global PassThreshold constant.
		overall := 0.0
		if subj != nil {
			overall = subj.Overall
		}
		sufficient = overall >= evidence.PassThreshold
		if !sufficient {
			reasons = append(reasons, fmt.Sprintf("overall %.3f below global threshold %.2f", overall, evidence.PassThreshold))
		}
	}

	// Generation-eligible tasks require replayable evidence. This override
	// fires even when the quality threshold passes, because generation-eligible
	// settlement demands a stronger evidence standard than ordinary settlement.
	// Non-generation-eligible tasks are unaffected (backward compatible).
	if h.GenerationEligible && h.ReplayabilityAssessment != nil && !h.ReplayabilityAssessment.Replayable {
		sufficient = false
		reasons = append(reasons, "generation_requires_replayable_evidence")
	}

	// Replay-mismatch enforcement: if a completed replay strongly disagrees with
	// the original verification, block settlement. Tasks without a ReplayVerdict
	// (nil) are unaffected — this is purely additive and backward compatible.
	if h.ReplayVerdict != nil {
		switch h.ReplayVerdict.Action {
		case "open_challenge", "slash_recommended":
			sufficient = false
			reasons = append(reasons, "replay_mismatch_detected")
		}
	}

	return sufficient, reasons
}
