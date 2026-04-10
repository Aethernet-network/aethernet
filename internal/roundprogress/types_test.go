package roundprogress

import "testing"

func TestValidTransition_ForwardTransitions(t *testing.T) {
	valid := []struct {
		from, to ProgressPhase
	}{
		{ProgressPhaseAcknowledged, ProgressPhaseFetchingBlob},
		{ProgressPhaseAcknowledged, ProgressPhaseAnalyzing},
		{ProgressPhaseAcknowledged, ProgressPhaseScorePending},
		{ProgressPhaseAcknowledged, ProgressPhaseVoteEmitted},
		{ProgressPhaseAcknowledged, ProgressPhaseAbstained},
		{ProgressPhaseAcknowledged, ProgressPhaseFailed},
		{ProgressPhaseFetchingBlob, ProgressPhaseAnalyzing},
		{ProgressPhaseFetchingBlob, ProgressPhaseScorePending},
		{ProgressPhaseFetchingBlob, ProgressPhaseVoteEmitted},
		{ProgressPhaseFetchingBlob, ProgressPhaseAbstained},
		{ProgressPhaseFetchingBlob, ProgressPhaseFailed},
		{ProgressPhaseAnalyzing, ProgressPhaseScorePending},
		{ProgressPhaseAnalyzing, ProgressPhaseVoteEmitted},
		{ProgressPhaseAnalyzing, ProgressPhaseAbstained},
		{ProgressPhaseAnalyzing, ProgressPhaseFailed},
		{ProgressPhaseScorePending, ProgressPhaseVoteEmitted},
		{ProgressPhaseScorePending, ProgressPhaseAbstained},
		{ProgressPhaseScorePending, ProgressPhaseFailed},
	}
	for _, tc := range valid {
		if !ValidTransition(tc.from, tc.to) {
			t.Errorf("ValidTransition(%s, %s) = false, want true", tc.from, tc.to)
		}
	}
}

func TestValidTransition_BackwardTransitions(t *testing.T) {
	invalid := []struct {
		from, to ProgressPhase
	}{
		{ProgressPhaseFetchingBlob, ProgressPhaseAcknowledged},
		{ProgressPhaseAnalyzing, ProgressPhaseFetchingBlob},
		{ProgressPhaseAnalyzing, ProgressPhaseAcknowledged},
		{ProgressPhaseScorePending, ProgressPhaseAnalyzing},
		{ProgressPhaseScorePending, ProgressPhaseFetchingBlob},
	}
	for _, tc := range invalid {
		if ValidTransition(tc.from, tc.to) {
			t.Errorf("ValidTransition(%s, %s) = true, want false (backward)", tc.from, tc.to)
		}
	}
}

func TestValidTransition_SelfTransitions(t *testing.T) {
	phases := []ProgressPhase{
		ProgressPhaseAcknowledged,
		ProgressPhaseFetchingBlob,
		ProgressPhaseAnalyzing,
		ProgressPhaseScorePending,
	}
	for _, p := range phases {
		if ValidTransition(p, p) {
			t.Errorf("ValidTransition(%s, %s) = true, want false (self-transition)", p, p)
		}
	}
}

func TestValidTransition_TerminalPhasesRejectAll(t *testing.T) {
	terminals := []ProgressPhase{
		ProgressPhaseVoteEmitted,
		ProgressPhaseAbstained,
		ProgressPhaseFailed,
	}
	allPhases := []ProgressPhase{
		ProgressPhaseAcknowledged,
		ProgressPhaseFetchingBlob,
		ProgressPhaseAnalyzing,
		ProgressPhaseScorePending,
		ProgressPhaseVoteEmitted,
		ProgressPhaseAbstained,
		ProgressPhaseFailed,
	}
	for _, from := range terminals {
		for _, to := range allPhases {
			if ValidTransition(from, to) {
				t.Errorf("ValidTransition(%s, %s) = true, want false (terminal source)", from, to)
			}
		}
	}
}

func TestProgressPhase_IsTerminal(t *testing.T) {
	if ProgressPhaseAcknowledged.IsTerminal() {
		t.Error("Acknowledged should not be terminal")
	}
	if ProgressPhaseFetchingBlob.IsTerminal() {
		t.Error("FetchingBlob should not be terminal")
	}
	if !ProgressPhaseVoteEmitted.IsTerminal() {
		t.Error("VoteEmitted should be terminal")
	}
	if !ProgressPhaseAbstained.IsTerminal() {
		t.Error("Abstained should be terminal")
	}
	if !ProgressPhaseFailed.IsTerminal() {
		t.Error("Failed should be terminal")
	}
}

func TestProgressPhase_String(t *testing.T) {
	if s := ProgressPhaseAnalyzing.String(); s != "Analyzing" {
		t.Errorf("String() = %q, want %q", s, "Analyzing")
	}
}
