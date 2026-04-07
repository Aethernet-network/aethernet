package taskverification

import (
	"testing"
	"time"
)

func TestOpenRound_ValidParams(t *testing.T) {
	r, err := OpenRound(OpenRoundParams{
		TaskID:                "task-1",
		SubmissionEventID:     "evt-sub-1",
		WorkerID:              "worker-1",
		PosterID:              "poster-1",
		Category:              "research",
		ValidatorSetVersion:   5,
		AnalyzerPolicyID:      "bootstrap_v1",
		DiversityFloor:        2,
		AcceptanceThresholdBP: 6000,
		DeadlineSeconds:       60,
		Now:                   1000,
	})
	if err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	if r.RoundID != NewRoundID("evt-sub-1") {
		t.Errorf("RoundID mismatch")
	}
	if r.State != RoundStateOpen {
		t.Errorf("State = %s; want open", r.State)
	}
	if r.OpenedAtUnix != 1000 {
		t.Errorf("OpenedAtUnix = %d; want 1000", r.OpenedAtUnix)
	}
	if r.DeadlineUnix != 1060 {
		t.Errorf("DeadlineUnix = %d; want 1060", r.DeadlineUnix)
	}
	if r.ExtendedUntilUnix != 0 {
		t.Errorf("ExtendedUntilUnix = %d; want 0", r.ExtendedUntilUnix)
	}
	if r.ValidatorSetVersion != 5 {
		t.Errorf("ValidatorSetVersion = %d; want 5", r.ValidatorSetVersion)
	}
	if r.Committee != nil {
		t.Errorf("Committee should be nil for bootstrap mode")
	}
}

func TestOpenRound_DeterministicID(t *testing.T) {
	p := OpenRoundParams{
		TaskID: "t", SubmissionEventID: "evt-1", WorkerID: "w",
		PosterID: "p", Category: "c", DiversityFloor: 1,
		AcceptanceThresholdBP: 5000, DeadlineSeconds: 60, Now: 1000,
	}
	r1, _ := OpenRound(p)
	r2, _ := OpenRound(p)
	if r1.RoundID != r2.RoundID {
		t.Errorf("same params produced different round IDs")
	}
}

func TestOpenRound_ValidationErrors(t *testing.T) {
	base := OpenRoundParams{
		TaskID: "t", SubmissionEventID: "e", WorkerID: "w",
		PosterID: "p", Category: "c", DiversityFloor: 2,
		AcceptanceThresholdBP: 6000, DeadlineSeconds: 60, Now: 1000,
	}

	cases := []struct {
		name   string
		mutate func(*OpenRoundParams)
	}{
		{"empty TaskID", func(p *OpenRoundParams) { p.TaskID = "" }},
		{"empty SubmissionEventID", func(p *OpenRoundParams) { p.SubmissionEventID = "" }},
		{"empty WorkerID", func(p *OpenRoundParams) { p.WorkerID = "" }},
		{"empty PosterID", func(p *OpenRoundParams) { p.PosterID = "" }},
		{"empty Category", func(p *OpenRoundParams) { p.Category = "" }},
		{"zero DeadlineSeconds", func(p *OpenRoundParams) { p.DeadlineSeconds = 0 }},
		{"negative DeadlineSeconds", func(p *OpenRoundParams) { p.DeadlineSeconds = -1 }},
		{"zero DiversityFloor", func(p *OpenRoundParams) { p.DiversityFloor = 0 }},
		{"threshold > 10000", func(p *OpenRoundParams) { p.AcceptanceThresholdBP = 10001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			if _, err := OpenRound(p); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestRound_StateTransitions_Valid(t *testing.T) {
	cases := []struct {
		name string
		to   RoundState
	}{
		{"Open → FinalizedAccept", RoundStateFinalizedAccept},
		{"Open → FinalizedReject", RoundStateFinalizedReject},
		{"Open → Disputed", RoundStateDisputed},
		{"Open → Expired", RoundStateExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &TaskVerificationRound{State: RoundStateOpen}
			now := time.Now().Unix()
			if err := r.Transition(tc.to, now); err != nil {
				t.Fatalf("Transition to %s: %v", tc.to, err)
			}
			if r.State != tc.to {
				t.Errorf("State = %s; want %s", r.State, tc.to)
			}
			if r.FinalizationTime != now {
				t.Errorf("FinalizationTime = %d; want %d", r.FinalizationTime, now)
			}
		})
	}
}

func TestRound_StateTransitions_Invalid(t *testing.T) {
	terminalStates := []RoundState{
		RoundStateFinalizedAccept,
		RoundStateFinalizedReject,
		RoundStateDisputed,
		RoundStateExpired,
	}
	allStates := append([]RoundState{RoundStateOpen}, terminalStates...)

	for _, from := range terminalStates {
		for _, to := range allStates {
			t.Run(from.String()+"→"+to.String(), func(t *testing.T) {
				r := &TaskVerificationRound{State: from}
				if err := r.Transition(to, time.Now().Unix()); err == nil {
					t.Errorf("Transition from terminal state %s to %s should fail", from, to)
				}
			})
		}
	}

	// Open → Open should also fail.
	t.Run("Open→Open", func(t *testing.T) {
		r := &TaskVerificationRound{State: RoundStateOpen}
		if err := r.Transition(RoundStateOpen, time.Now().Unix()); err == nil {
			t.Error("Open → Open should fail")
		}
	})
}

func TestRound_IsTerminal(t *testing.T) {
	if (&TaskVerificationRound{State: RoundStateOpen}).IsTerminal() {
		t.Error("Open should not be terminal")
	}
	for _, s := range []RoundState{RoundStateFinalizedAccept, RoundStateFinalizedReject, RoundStateDisputed, RoundStateExpired} {
		if !(&TaskVerificationRound{State: s}).IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
}

func TestRound_DeadlineForCurrentPhase_Original(t *testing.T) {
	r := &TaskVerificationRound{DeadlineUnix: 1000, ExtendedUntilUnix: 0}
	if got := r.DeadlineForCurrentPhase(); got != 1000 {
		t.Errorf("DeadlineForCurrentPhase() = %d; want 1000", got)
	}
}

func TestRound_DeadlineForCurrentPhase_Extended(t *testing.T) {
	r := &TaskVerificationRound{DeadlineUnix: 1000, ExtendedUntilUnix: 2000}
	if got := r.DeadlineForCurrentPhase(); got != 2000 {
		t.Errorf("DeadlineForCurrentPhase() = %d; want 2000", got)
	}
}

func TestRound_DistinctPassFamilies(t *testing.T) {
	cases := []struct {
		name     string
		families map[string]uint64
		want     int
	}{
		{"nil", nil, 0},
		{"empty", map[string]uint64{}, 0},
		{"one", map[string]uint64{"llm_semantic": 100}, 1},
		{"two", map[string]uint64{"llm_semantic": 100, "heuristic": 200}, 2},
		{"zero weight excluded", map[string]uint64{"llm_semantic": 100, "heuristic": 0}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &TaskVerificationRound{ParticipatingFamilies: tc.families}
			if got := r.DistinctPassFamilies(); got != tc.want {
				t.Errorf("DistinctPassFamilies() = %d; want %d", got, tc.want)
			}
		})
	}
}

func TestRound_Canonical_Roundtrip(t *testing.T) {
	r := makeTestRound()
	data1, err := r.Canonical()
	if err != nil {
		t.Fatalf("Canonical(): %v", err)
	}
	decoded, err := RoundFromCanonical(data1)
	if err != nil {
		t.Fatalf("RoundFromCanonical(): %v", err)
	}
	data2, err := decoded.Canonical()
	if err != nil {
		t.Fatalf("Canonical() on decoded: %v", err)
	}
	if string(data1) != string(data2) {
		t.Error("re-encoded canonical bytes differ from original")
	}
}

func TestRound_Canonical_DeterministicMapOrder(t *testing.T) {
	// Build same round with maps populated in different insertion orders.
	r1 := makeTestRound()
	r1.ParticipatingFamilies = map[string]uint64{}
	r1.ParticipatingFamilies["zzz_last"] = 300
	r1.ParticipatingFamilies["aaa_first"] = 100
	r1.ParticipatingFamilies["mmm_middle"] = 200

	r2 := makeTestRound()
	r2.ParticipatingFamilies = map[string]uint64{}
	r2.ParticipatingFamilies["aaa_first"] = 100
	r2.ParticipatingFamilies["mmm_middle"] = 200
	r2.ParticipatingFamilies["zzz_last"] = 300

	data1, err := r1.Canonical()
	if err != nil {
		t.Fatalf("Canonical(r1): %v", err)
	}
	data2, err := r2.Canonical()
	if err != nil {
		t.Fatalf("Canonical(r2): %v", err)
	}
	if string(data1) != string(data2) {
		t.Errorf("different map insertion order produced different canonical bytes:\n  r1: %s\n  r2: %s", data1, data2)
	}
}

// makeTestRound creates a fully-populated round for testing.
func makeTestRound() *TaskVerificationRound {
	return &TaskVerificationRound{
		RoundID:               "round-test-123",
		TaskID:                "task-abc",
		SubmissionEventID:     "evt-submit-1",
		WorkerID:              "worker-1",
		PosterID:              "poster-1",
		Category:              "research",
		ValidatorSetVersion:   3,
		Committee:             nil,
		AnalyzerPolicyID:      "default",
		DiversityFloor:        DefaultDiversityFloor,
		AcceptanceThresholdBP: DefaultAcceptanceThresholdBP,
		OpenedAtUnix:          1000,
		DeadlineUnix:          1060,
		State:                 RoundStateOpen,
		PassWeight:            500,
		FailWeight:            100,
		AbstainWeight:         50,
		ParticipatingFamilies: map[string]uint64{
			"llm_semantic":           300,
			"deterministic_heuristic": 200,
		},
		Votes: []TaskVerificationVoteRecord{
			{
				ValidatorID:     "validator-b",
				Verdict:         VerdictPass,
				ScoreBP:         7500,
				ScoreBreakdown:  map[string]uint64{"quality": 8000, "completeness": 7000},
				AnalyzerFamily:  "llm_semantic",
				AnalyzerVersion: "claude_semantic_v1",
				PolicyVersion:   "v1",
				Stake:           300,
				TimestampUnix:   1005,
			},
			{
				ValidatorID:     "validator-a",
				Verdict:         VerdictPass,
				ScoreBP:         6500,
				ScoreBreakdown:  map[string]uint64{"quality": 7000, "completeness": 6000},
				AnalyzerFamily:  "deterministic_heuristic",
				AnalyzerVersion: "heuristic_v1",
				PolicyVersion:   "v1",
				Stake:           200,
				TimestampUnix:   1010,
			},
		},
		FinalVerdict: VerdictPass,
		FinalScoreBP: 7000,
	}
}
