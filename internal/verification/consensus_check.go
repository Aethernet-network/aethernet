package verification

import (
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

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

	return sufficient, reasons
}
