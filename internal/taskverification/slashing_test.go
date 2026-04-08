package taskverification

import (
	"context"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

func newTestSlashingEvaluator(t *testing.T) (*SlashingEvaluator, *ValidatorReputationStore, *CalibrationStore) {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rep := NewValidatorReputationStore(db)

	db2, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	cal := NewCalibrationStore(db2, CalibrationConfig{DefaultThreshold: 5})

	eval := NewSlashingEvaluator(DefaultSlashingPolicy(), rep, cal)
	return eval, rep, cal
}

func TestSlashing_SoftSlashOnDeviation(t *testing.T) {
	eval, _, cal := newTestSlashingEvaluator(t)
	ctx := context.Background()

	// Calibrate so slashing is active.
	for i := 0; i < 5; i++ {
		cal.Increment(ctx, "research", verification.FamilyDeterministicHeuristic)
	}

	round := &TaskVerificationRound{
		RoundID:      "round-slash-1",
		Category:     "research",
		State:        RoundStateFinalizedAccept,
		FinalVerdict: VerdictPass,
		Votes: []TaskVerificationVoteRecord{
			{ValidatorID: "v1", Verdict: VerdictPass, AnalyzerFamily: "deterministic_heuristic"},
			{ValidatorID: "v2", Verdict: VerdictFail, AnalyzerFamily: "deterministic_heuristic"}, // deviated
		},
	}

	actions := eval.EvaluateRound(ctx, round)

	// v2 should get soft slash for deviation.
	var softCount int
	for _, a := range actions {
		if a.Type == SlashingTypeSoft && a.ValidatorID == "v2" {
			softCount++
		}
	}
	if softCount != 1 {
		t.Errorf("soft slashes for v2 = %d; want 1", softCount)
	}
}

func TestSlashing_SkipsDuringCalibration(t *testing.T) {
	eval, _, _ := newTestSlashingEvaluator(t)
	ctx := context.Background()
	// Do NOT calibrate — counter stays at 0.

	round := &TaskVerificationRound{
		RoundID:      "round-cal",
		Category:     "research",
		State:        RoundStateFinalizedAccept,
		FinalVerdict: VerdictPass,
		Votes: []TaskVerificationVoteRecord{
			{ValidatorID: "v1", Verdict: VerdictFail, AnalyzerFamily: "deterministic_heuristic"},
		},
	}

	actions := eval.EvaluateRound(ctx, round)
	if len(actions) != 0 {
		t.Errorf("expected 0 actions during calibration; got %d", len(actions))
	}
}

func TestSlashing_HardSlashOnSystematicDivergence(t *testing.T) {
	eval, rep, cal := newTestSlashingEvaluator(t)
	ctx := context.Background()

	// Calibrate.
	for i := 0; i < 5; i++ {
		cal.Increment(ctx, "code", verification.FamilyStatisticalStructural)
	}

	// Build a validator with terrible agreement rate (20% over 50 votes).
	for i := 0; i < 10; i++ {
		_ = rep.RecordVote(ctx, "bad-validator", verification.FamilyStatisticalStructural, "code", true, false, int64(i))
	}
	for i := 0; i < 40; i++ {
		_ = rep.RecordVote(ctx, "bad-validator", verification.FamilyStatisticalStructural, "code", false, false, int64(10+i))
	}

	round := &TaskVerificationRound{
		RoundID:      "round-diverge",
		Category:     "code",
		State:        RoundStateFinalizedAccept,
		FinalVerdict: VerdictPass,
		Votes: []TaskVerificationVoteRecord{
			{ValidatorID: "bad-validator", Verdict: VerdictFail, AnalyzerFamily: "statistical_structural"},
		},
	}

	actions := eval.EvaluateRound(ctx, round)
	var hardCount int
	for _, a := range actions {
		if a.Type == SlashingTypeHard && a.ValidatorID == "bad-validator" {
			hardCount++
		}
	}
	if hardCount != 1 {
		t.Errorf("hard slashes for bad-validator = %d; want 1", hardCount)
	}
}

func TestSlashing_EquivocationDetection(t *testing.T) {
	eval, _, cal := newTestSlashingEvaluator(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		cal.Increment(ctx, "research", verification.FamilyLLMSemantic)
	}

	// Round with equivocating validator (same family, different verdicts).
	round := &TaskVerificationRound{
		RoundID:      "round-equi",
		Category:     "research",
		State:        RoundStateFinalizedAccept,
		FinalVerdict: VerdictPass,
		Votes: []TaskVerificationVoteRecord{
			{ValidatorID: "equi-v", Verdict: VerdictPass, ScoreBP: 7000, AnalyzerFamily: "llm_semantic"},
			{ValidatorID: "equi-v", Verdict: VerdictFail, ScoreBP: 3000, AnalyzerFamily: "llm_semantic"}, // equivocation
		},
	}

	actions := eval.EvaluateRound(ctx, round)
	var hardEqui int
	for _, a := range actions {
		if a.Type == SlashingTypeHard && a.Reason == "equivocation: conflicting votes for same family" {
			hardEqui++
		}
	}
	if hardEqui != 1 {
		t.Errorf("equivocation hard slashes = %d; want 1", hardEqui)
	}
}

func TestSlashing_NoActionForAgreeingValidator(t *testing.T) {
	eval, _, cal := newTestSlashingEvaluator(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		cal.Increment(ctx, "research", verification.FamilyDeterministicHeuristic)
	}

	round := &TaskVerificationRound{
		RoundID:      "round-agree",
		Category:     "research",
		State:        RoundStateFinalizedAccept,
		FinalVerdict: VerdictPass,
		Votes: []TaskVerificationVoteRecord{
			{ValidatorID: "good-v", Verdict: VerdictPass, AnalyzerFamily: "deterministic_heuristic"},
		},
	}

	actions := eval.EvaluateRound(ctx, round)
	if len(actions) != 0 {
		t.Errorf("expected 0 actions for agreeing validator; got %d", len(actions))
	}
}
