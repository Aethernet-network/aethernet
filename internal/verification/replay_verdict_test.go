package verification_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// TestConsensusSufficiencyChecker_ReplayVerdictBlocksSettlement verifies that
// a passing threshold gate is overridden to false when the ReplayVerdict
// recommends opening a challenge.
func TestConsensusSufficiencyChecker_ReplayVerdictBlocksSettlement(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	verdict := &verification.ReplayVerdict{
		Action:        "open_challenge",
		Reason:        "multiple check mismatches",
		MismatchCount: 2,
		SeverityScore: 0.6,
	}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		ReplayVerdict: verdict,
	})
	if sufficient {
		t.Error("expected sufficient=false when ReplayVerdict.Action is open_challenge")
	}
	found := false
	for _, r := range reasons {
		if r == "replay_mismatch_detected" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reason %q in %v", "replay_mismatch_detected", reasons)
	}
}

// TestConsensusSufficiencyChecker_SlashRecommendedBlocksSettlement verifies
// that "slash_recommended" also blocks settlement.
func TestConsensusSufficiencyChecker_SlashRecommendedBlocksSettlement(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.850"},
		},
		NumericScores: map[string]float64{"overall": 0.85},
	}
	subj := &verification.SubjectiveReport{Overall: 0.85}

	verdict := &verification.ReplayVerdict{
		Action:        "slash_recommended",
		Reason:        "severe fraud signal",
		MismatchCount: 3,
		SeverityScore: 1.0,
	}

	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		ReplayVerdict: verdict,
	})
	if sufficient {
		t.Error("expected sufficient=false when ReplayVerdict.Action is slash_recommended")
	}
	found := false
	for _, r := range reasons {
		if r == "replay_mismatch_detected" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reason %q in %v", "replay_mismatch_detected", reasons)
	}
}

// TestConsensusSufficiencyChecker_NilReplayVerdictBackwardCompat verifies that
// callers that do not provide a ReplayVerdict are unaffected (nil = no replay done).
func TestConsensusSufficiencyChecker_NilReplayVerdictBackwardCompat(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	// ContractHints with nil ReplayVerdict — existing callers are unaffected.
	sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
		ReplayVerdict: nil,
	})
	if !sufficient {
		t.Errorf("expected sufficient=true when ReplayVerdict is nil, reasons=%v", reasons)
	}
	for _, r := range reasons {
		if r == "replay_mismatch_detected" {
			t.Errorf("unexpected reason %q when ReplayVerdict is nil", r)
		}
	}
}

// TestConsensusSufficiencyChecker_NoActionReplayVerdictDoesNotBlock verifies
// that "no_action" and "flag_for_review" verdicts do not block settlement.
func TestConsensusSufficiencyChecker_NoActionReplayVerdictDoesNotBlock(t *testing.T) {
	chk := verification.ConsensusSufficiencyChecker{}
	det := &verification.DeterministicReport{
		HardGates: []verification.GateResult{
			{Name: "threshold", Pass: true, Detail: "overall=0.800"},
		},
		NumericScores: map[string]float64{"overall": 0.80},
	}
	subj := &verification.SubjectiveReport{Overall: 0.80}

	for _, action := range []string{"no_action", "flag_for_review"} {
		verdict := &verification.ReplayVerdict{
			Action:        action,
			SeverityScore: 0.3,
		}
		sufficient, reasons := chk.Check(det, subj, "v1", verification.ContractHints{
			ReplayVerdict: verdict,
		})
		if !sufficient {
			t.Errorf("action=%q: expected sufficient=true, reasons=%v", action, reasons)
		}
	}
}
