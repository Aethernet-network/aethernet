package verification

import (
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

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
// policyVersion is accepted for future policy-dispatch; currently ignored.
func (c *ConsensusSufficiencyChecker) Check(det *DeterministicReport, subj *SubjectiveReport, _ string) (sufficient bool, reasons []string) {
	if det == nil {
		return false, []string{"deterministic report is nil"}
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
