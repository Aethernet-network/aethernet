package roundprogress

import "testing"

func TestComputeLeaseExpiry_Acknowledged(t *testing.T) {
	now := int64(1000)
	expiry := ComputeLeaseExpiry(ProgressPhaseAcknowledged, 0, 0, now)
	if expiry != now+30 {
		t.Errorf("Acknowledged expiry = %d, want %d", expiry, now+30)
	}
}

func TestComputeLeaseExpiry_FetchingBlob_WithETA(t *testing.T) {
	now := int64(1000)
	eta := now + 20 // within 30s max
	expiry := ComputeLeaseExpiry(ProgressPhaseFetchingBlob, 0, eta, now)
	if expiry != eta {
		t.Errorf("FetchingBlob with ETA %d: expiry = %d, want %d", eta, expiry, eta)
	}
}

func TestComputeLeaseExpiry_FetchingBlob_ETABeyondMax(t *testing.T) {
	now := int64(1000)
	eta := now + 100 // beyond 30s max
	expiry := ComputeLeaseExpiry(ProgressPhaseFetchingBlob, 0, eta, now)
	if expiry != now+30 {
		t.Errorf("FetchingBlob with high ETA: expiry = %d, want %d", expiry, now+30)
	}
}

func TestComputeLeaseExpiry_Analyzing_WithETA(t *testing.T) {
	now := int64(1000)
	eta := now + 45 // within 60s max
	expiry := ComputeLeaseExpiry(ProgressPhaseAnalyzing, 0, eta, now)
	if expiry != eta {
		t.Errorf("Analyzing with ETA %d: expiry = %d, want %d", eta, expiry, eta)
	}
}

func TestComputeLeaseExpiry_Analyzing_ETABeyondMax(t *testing.T) {
	now := int64(1000)
	eta := now + 200
	expiry := ComputeLeaseExpiry(ProgressPhaseAnalyzing, 0, eta, now)
	if expiry != now+60 {
		t.Errorf("Analyzing with high ETA: expiry = %d, want %d", expiry, now+60)
	}
}

func TestComputeLeaseExpiry_ScorePending_Fixed(t *testing.T) {
	now := int64(1000)
	expiry := ComputeLeaseExpiry(ProgressPhaseScorePending, 0, 0, now)
	if expiry != now+10 {
		t.Errorf("ScorePending expiry = %d, want %d", expiry, now+10)
	}
}

func TestComputeLeaseExpiry_Terminal_Zero(t *testing.T) {
	now := int64(1000)
	for _, phase := range []ProgressPhase{ProgressPhaseVoteEmitted, ProgressPhaseAbstained, ProgressPhaseFailed} {
		expiry := ComputeLeaseExpiry(phase, 0, 0, now)
		if expiry != 0 {
			t.Errorf("%s expiry = %d, want 0", phase, expiry)
		}
	}
}

func TestValidateLeaseExtension_ValidForwardTransition(t *testing.T) {
	current := &RoundProgressSnapshot{
		CurrentPhase:       ProgressPhaseFetchingBlob,
		ProgressGeneration: 1,
	}
	update := &ProgressUpdate{
		Phase:              ProgressPhaseAnalyzing,
		ProgressGeneration: 2,
		ProgressEvidence:   [32]byte{1, 2, 3},
	}
	if err := ValidateLeaseExtension(current, update); err != nil {
		t.Errorf("expected valid transition, got: %v", err)
	}
}

func TestValidateLeaseExtension_SameEvidence_StaleGeneration(t *testing.T) {
	current := &RoundProgressSnapshot{
		CurrentPhase:       ProgressPhaseFetchingBlob,
		ProgressGeneration: 3,
	}
	update := &ProgressUpdate{
		Phase:              ProgressPhaseFetchingBlob,
		ProgressGeneration: 3, // same generation — invalid
	}
	// Same phase with same generation is a self-transition (rejected by ValidTransition).
	err := ValidateLeaseExtension(current, update)
	if err == nil {
		t.Error("expected error for self-transition")
	}
}

func TestValidateLeaseExtension_BackwardsPhase(t *testing.T) {
	current := &RoundProgressSnapshot{
		CurrentPhase:       ProgressPhaseAnalyzing,
		ProgressGeneration: 2,
	}
	update := &ProgressUpdate{
		Phase:              ProgressPhaseFetchingBlob, // backwards
		ProgressGeneration: 3,
	}
	err := ValidateLeaseExtension(current, update)
	if err == nil {
		t.Error("expected error for backwards phase transition")
	}
}

func TestValidateLeaseExtension_StaleGeneration(t *testing.T) {
	current := &RoundProgressSnapshot{
		CurrentPhase:       ProgressPhaseFetchingBlob,
		ProgressGeneration: 5,
	}
	update := &ProgressUpdate{
		Phase:              ProgressPhaseAnalyzing,
		ProgressGeneration: 3, // stale — less than current
	}
	err := ValidateLeaseExtension(current, update)
	if err == nil {
		t.Error("expected error for stale generation")
	}
}

func TestValidateLeaseExtension_ScorePendingNoRepeat(t *testing.T) {
	current := &RoundProgressSnapshot{
		CurrentPhase:       ProgressPhaseScorePending,
		ProgressGeneration: 5,
	}
	update := &ProgressUpdate{
		Phase:              ProgressPhaseScorePending, // same phase — self-transition
		ProgressGeneration: 6,
	}
	err := ValidateLeaseExtension(current, update)
	if err == nil {
		t.Error("expected error for ScorePending repeated extension")
	}
}

func TestValidateLeaseExtension_TerminalPhaseAccepted(t *testing.T) {
	current := &RoundProgressSnapshot{
		CurrentPhase:       ProgressPhaseAnalyzing,
		ProgressGeneration: 2,
	}
	update := &ProgressUpdate{
		Phase:              ProgressPhaseVoteEmitted,
		ProgressGeneration: 3,
	}
	if err := ValidateLeaseExtension(current, update); err != nil {
		t.Errorf("terminal transition should be accepted: %v", err)
	}
}
