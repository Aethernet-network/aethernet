package taskverification

import (
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

func makeVote(validatorID string, verdict Verdict, scoreBP, stake uint64, family string) TaskVerificationVoteRecord {
	return TaskVerificationVoteRecord{
		ValidatorID:    crypto.AgentID(validatorID),
		Verdict:        verdict,
		ScoreBP:        scoreBP,
		Stake:          stake,
		AnalyzerFamily: family,
		AnalyzerVersion: "v1",
		PolicyVersion:  "bootstrap_v1",
		TimestampUnix:  1000,
	}
}

func makeOpenRound() *TaskVerificationRound {
	return &TaskVerificationRound{
		RoundID:               "round-agg-test",
		State:                 RoundStateOpen,
		ParticipatingFamilies: make(map[string]uint64),
		Votes:                 []TaskVerificationVoteRecord{},
	}
}

func TestApplyVote_NewVotePass(t *testing.T) {
	r := makeOpenRound()
	vote := makeVote("v1", VerdictPass, 7500, 300, "llm_semantic")
	res, err := ApplyVoteToRound(r, vote)
	if err != nil {
		t.Fatalf("ApplyVoteToRound: %v", err)
	}
	if !res.Applied {
		t.Error("should be applied")
	}
	if r.PassWeight != 300 {
		t.Errorf("PassWeight = %d; want 300", r.PassWeight)
	}
	if r.ParticipatingFamilies["llm_semantic"] != 300 {
		t.Errorf("ParticipatingFamilies[llm_semantic] = %d; want 300", r.ParticipatingFamilies["llm_semantic"])
	}
	if len(r.Votes) != 1 {
		t.Errorf("Votes len = %d; want 1", len(r.Votes))
	}
}

func TestApplyVote_NewVoteFail(t *testing.T) {
	r := makeOpenRound()
	vote := makeVote("v1", VerdictFail, 3000, 200, "heuristic")
	res, err := ApplyVoteToRound(r, vote)
	if err != nil {
		t.Fatalf("ApplyVoteToRound: %v", err)
	}
	if !res.Applied {
		t.Error("should be applied")
	}
	if r.FailWeight != 200 {
		t.Errorf("FailWeight = %d; want 200", r.FailWeight)
	}
	// Fail votes should NOT add to ParticipatingFamilies.
	if len(r.ParticipatingFamilies) != 0 {
		t.Errorf("ParticipatingFamilies should be empty for fail votes; got %v", r.ParticipatingFamilies)
	}
}

func TestApplyVote_NewVoteAbstain(t *testing.T) {
	r := makeOpenRound()
	vote := makeVote("v1", VerdictAbstain, 0, 100, "")
	res, err := ApplyVoteToRound(r, vote)
	if err != nil {
		t.Fatalf("ApplyVoteToRound: %v", err)
	}
	if !res.Applied {
		t.Error("should be applied")
	}
	if r.AbstainWeight != 100 {
		t.Errorf("AbstainWeight = %d; want 100", r.AbstainWeight)
	}
}

func TestApplyVote_DuplicateIdentical(t *testing.T) {
	r := makeOpenRound()
	vote := makeVote("v1", VerdictPass, 7500, 300, "llm_semantic")
	_, _ = ApplyVoteToRound(r, vote)
	res, err := ApplyVoteToRound(r, vote)
	if err != nil {
		t.Fatalf("duplicate should not error: %v", err)
	}
	if !res.DuplicateVote {
		t.Error("should be marked as duplicate")
	}
	if res.Applied {
		t.Error("duplicate should not be applied")
	}
	if r.PassWeight != 300 {
		t.Errorf("PassWeight should still be 300; got %d", r.PassWeight)
	}
	if len(r.Votes) != 1 {
		t.Errorf("Votes len should still be 1; got %d", len(r.Votes))
	}
}

func TestApplyVote_Equivocation_DifferentVerdict(t *testing.T) {
	r := makeOpenRound()
	vote1 := makeVote("v1", VerdictPass, 7500, 300, "llm_semantic")
	_, _ = ApplyVoteToRound(r, vote1)

	vote2 := makeVote("v1", VerdictFail, 3000, 300, "llm_semantic")
	res, err := ApplyVoteToRound(r, vote2)
	if !errors.Is(err, ErrEquivocation) {
		t.Fatalf("expected ErrEquivocation; got %v", err)
	}
	if !res.EquivocationDetected {
		t.Error("should flag equivocation")
	}
}

func TestApplyVote_Equivocation_DifferentScore(t *testing.T) {
	r := makeOpenRound()
	vote1 := makeVote("v1", VerdictPass, 7500, 300, "llm_semantic")
	_, _ = ApplyVoteToRound(r, vote1)

	vote2 := makeVote("v1", VerdictPass, 8000, 300, "llm_semantic")
	res, err := ApplyVoteToRound(r, vote2)
	if !errors.Is(err, ErrEquivocation) {
		t.Fatalf("expected ErrEquivocation; got %v", err)
	}
	if !res.EquivocationDetected {
		t.Error("should flag equivocation")
	}
}

func TestApplyVote_RoundNotOpen(t *testing.T) {
	r := makeOpenRound()
	r.State = RoundStateFinalizedAccept
	vote := makeVote("v1", VerdictPass, 7500, 300, "llm_semantic")
	_, err := ApplyVoteToRound(r, vote)
	if !errors.Is(err, ErrRoundNotOpen) {
		t.Fatalf("expected ErrRoundNotOpen; got %v", err)
	}
}

func TestApplyVote_MultipleFamilies(t *testing.T) {
	r := makeOpenRound()
	_, _ = ApplyVoteToRound(r, makeVote("v1", VerdictPass, 7500, 300, "llm_semantic"))
	_, _ = ApplyVoteToRound(r, makeVote("v2", VerdictPass, 6500, 200, "heuristic"))
	_, _ = ApplyVoteToRound(r, makeVote("v3", VerdictPass, 7000, 250, "llm_semantic"))

	if r.PassWeight != 750 {
		t.Errorf("PassWeight = %d; want 750", r.PassWeight)
	}
	if r.ParticipatingFamilies["llm_semantic"] != 550 {
		t.Errorf("llm_semantic weight = %d; want 550", r.ParticipatingFamilies["llm_semantic"])
	}
	if r.ParticipatingFamilies["heuristic"] != 200 {
		t.Errorf("heuristic weight = %d; want 200", r.ParticipatingFamilies["heuristic"])
	}
	if r.DistinctPassFamilies() != 2 {
		t.Errorf("DistinctPassFamilies = %d; want 2", r.DistinctPassFamilies())
	}
}

func TestApplyVote_StakeWeighting(t *testing.T) {
	r := makeOpenRound()
	_, _ = ApplyVoteToRound(r, makeVote("big-validator", VerdictPass, 8000, 50000, "llm_semantic"))
	_, _ = ApplyVoteToRound(r, makeVote("small-validator", VerdictFail, 2000, 100, "heuristic"))

	if r.PassWeight != 50000 {
		t.Errorf("PassWeight = %d; want 50000", r.PassWeight)
	}
	if r.FailWeight != 100 {
		t.Errorf("FailWeight = %d; want 100", r.FailWeight)
	}
}

func TestRecordPostFinalizationVote(t *testing.T) {
	r := makeOpenRound()
	r.State = RoundStateFinalizedAccept

	vote := makeVote("late-validator", VerdictPass, 7000, 200, "llm_semantic")
	RecordPostFinalizationVote(r, vote)

	if len(r.Votes) != 1 {
		t.Fatalf("Votes len = %d; want 1", len(r.Votes))
	}
	// Aggregation state should NOT change.
	if r.PassWeight != 0 {
		t.Errorf("PassWeight should remain 0; got %d", r.PassWeight)
	}
}
