package roundprogress

import "testing"

func TestClampETA_FetchingBlob_Within(t *testing.T) {
	now := int64(1000)
	eta := now + 20 // within 30s
	result := ClampETA(ProgressPhaseFetchingBlob, eta, now)
	if result != eta {
		t.Errorf("ClampETA = %d, want %d (pass-through)", result, eta)
	}
}

func TestClampETA_FetchingBlob_Beyond(t *testing.T) {
	now := int64(1000)
	eta := now + 100 // beyond 30s
	result := ClampETA(ProgressPhaseFetchingBlob, eta, now)
	want := now + 30
	if result != want {
		t.Errorf("ClampETA = %d, want %d (clamped)", result, want)
	}
}

func TestClampETA_Analyzing_Within(t *testing.T) {
	now := int64(1000)
	eta := now + 45 // within 60s
	result := ClampETA(ProgressPhaseAnalyzing, eta, now)
	if result != eta {
		t.Errorf("ClampETA = %d, want %d (pass-through)", result, eta)
	}
}

func TestClampETA_Analyzing_Beyond(t *testing.T) {
	now := int64(1000)
	eta := now + 200 // beyond 60s
	result := ClampETA(ProgressPhaseAnalyzing, eta, now)
	want := now + 60
	if result != want {
		t.Errorf("ClampETA = %d, want %d (clamped)", result, want)
	}
}

func TestClampETA_ZeroETA(t *testing.T) {
	now := int64(1000)
	result := ClampETA(ProgressPhaseFetchingBlob, 0, now)
	if result != 0 {
		t.Errorf("ClampETA(0) = %d, want 0 (unknown passes through)", result)
	}
}

func TestClampETA_TerminalPhase(t *testing.T) {
	now := int64(1000)
	result := ClampETA(ProgressPhaseVoteEmitted, now+10, now)
	if result != 0 {
		t.Errorf("ClampETA(terminal) = %d, want 0", result)
	}
}

func TestClampETA_ScorePending(t *testing.T) {
	now := int64(1000)
	result := ClampETA(ProgressPhaseScorePending, now+5, now)
	if result != 0 {
		t.Errorf("ClampETA(ScorePending) = %d, want 0 (not a clampable phase)", result)
	}
}
