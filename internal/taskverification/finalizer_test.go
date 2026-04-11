package taskverification

import (
	"testing"
)

func makeRoundForFinalizer(passWeight, failWeight, abstainWeight, totalActive uint64, families map[string]uint64, votes []TaskVerificationVoteRecord) *TaskVerificationRound {
	// Build AllParticipatingFamilies from votes.
	allFamilies := make(map[string]bool)
	for _, v := range votes {
		if v.AnalyzerFamily != "" {
			allFamilies[v.AnalyzerFamily] = true
		}
	}
	return &TaskVerificationRound{
		RoundID:                  "round-fin-test",
		State:                    RoundStateOpen,
		PassWeight:               passWeight,
		FailWeight:               failWeight,
		AbstainWeight:            abstainWeight,
		ParticipatingFamilies:    families,
		AllParticipatingFamilies: allFamilies,
		Votes:                    votes,
		DeadlineUnix:             2000,
	}
}

func TestFinalizer_AcceptSupermajority(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// 700/1000 pass weight (70% > 66.67%), 2 pass families, 3 participating families
	// Median score is observability only — no longer gates acceptance.
	round := makeRoundForFinalizer(700, 100, 0, 1000,
		map[string]uint64{"llm_semantic": 400, "heuristic": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 7000, AnalyzerFamily: "llm_semantic"},
			{Verdict: VerdictPass, ScoreBP: 8000, AnalyzerFamily: "heuristic"},
			{Verdict: VerdictFail, ScoreBP: 5000, AnalyzerFamily: "statistical"},
		})
	d := f.Evaluate(round, 1000, 1500)
	if !d.ShouldFinalize {
		t.Fatal("should finalize")
	}
	if d.Verdict != VerdictPass {
		t.Errorf("Verdict = %s; want pass", d.Verdict)
	}
	if d.Reason != ReasonAcceptSupermajority {
		t.Errorf("Reason = %s; want accept_supermajority", d.Reason)
	}
}

func TestFinalizer_AcceptInsufficientWeight(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// 600/1000 = 60% < 66.67%
	round := makeRoundForFinalizer(600, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 300, "heuristic": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 8000},
		})
	d := f.Evaluate(round, 1000, 1500)
	if d.ShouldFinalize {
		t.Error("should NOT finalize with insufficient pass weight")
	}
}

func TestFinalizer_AcceptInsufficientDiversity(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// 700/1000 pass weight OK, but only 1 family
	round := makeRoundForFinalizer(700, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 700},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 8000},
		})
	d := f.Evaluate(round, 1000, 1500)
	if d.ShouldFinalize {
		t.Error("should NOT finalize with insufficient diversity")
	}
}

func TestFinalizer_AcceptInsufficientParticipation(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// Supermajority + diversity floor met, but only 2 participating families
	// (participation floor = 3). Should NOT finalize.
	round := makeRoundForFinalizer(700, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 400, "heuristic": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 3000, AnalyzerFamily: "llm_semantic"},
			{Verdict: VerdictPass, ScoreBP: 5000, AnalyzerFamily: "heuristic"},
		})
	d := f.Evaluate(round, 1000, 1500)
	if d.ShouldFinalize {
		t.Error("should NOT finalize with insufficient participation (2 < 3)")
	}
}

func TestFinalizer_RejectSupermajority(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	round := makeRoundForFinalizer(0, 700, 0, 1000,
		map[string]uint64{},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictFail, ScoreBP: 2000},
		})
	d := f.Evaluate(round, 1000, 1500)
	if !d.ShouldFinalize {
		t.Fatal("should finalize as reject")
	}
	if d.Verdict != VerdictFail {
		t.Errorf("Verdict = %s; want fail", d.Verdict)
	}
	if d.Reason != ReasonRejectSupermajority {
		t.Errorf("Reason = %s; want reject_supermajority", d.Reason)
	}
}

func TestFinalizer_DeadlineExpiredDispute(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// Partial votes, deadline passed, no convergence
	round := makeRoundForFinalizer(300, 200, 0, 1000,
		map[string]uint64{"llm_semantic": 300},
		nil)
	round.DeadlineUnix = 1000
	d := f.Evaluate(round, 1000, 1500) // now=1500 > deadline=1000
	if !d.ShouldFinalize {
		t.Fatal("should finalize as dispute (deadline expired)")
	}
	if d.Verdict != VerdictAbstain {
		t.Errorf("Verdict = %s; want abstain (disputed)", d.Verdict)
	}
	if d.Reason != ReasonDeadlineExpiredDispute {
		t.Errorf("Reason = %s; want deadline_expired_dispute", d.Reason)
	}
}

func TestFinalizer_DeadlineExpiredAfterExtension(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	round := makeRoundForFinalizer(500, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 500},
		nil)
	round.DeadlineUnix = 1000
	round.ExtendedUntilUnix = 1500
	// Now past extended deadline
	d := f.Evaluate(round, 1000, 1600)
	if !d.ShouldFinalize {
		t.Fatal("should finalize after extended deadline")
	}
	if d.Verdict != VerdictAbstain {
		t.Errorf("Verdict = %s; want abstain", d.Verdict)
	}
}

func TestFinalizer_DeadlineExpiredConvergent_NoFinalize(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// 600/1000 pass weight (≥50%) — convergence plausible, let checker extend
	round := makeRoundForFinalizer(600, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 600},
		nil)
	round.DeadlineUnix = 1000
	d := f.Evaluate(round, 1000, 1500)
	if d.ShouldFinalize {
		t.Error("should NOT finalize when convergence is plausible (let checker extend)")
	}
}

func TestFinalizer_OpenWithinDeadline(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	round := makeRoundForFinalizer(300, 100, 0, 1000,
		map[string]uint64{"llm_semantic": 300},
		nil)
	d := f.Evaluate(round, 1000, 1500) // deadline 2000, now 1500
	if d.ShouldFinalize {
		t.Error("should NOT finalize within deadline with partial votes")
	}
}

func TestFinalizer_AlreadyFinalized(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	round := makeRoundForFinalizer(700, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 400, "heuristic": 300},
		nil)
	round.State = RoundStateFinalizedAccept
	d := f.Evaluate(round, 1000, 1500)
	if d.ShouldFinalize {
		t.Error("should NOT finalize already-finalized round")
	}
}

func TestMedianScore_OddCount(t *testing.T) {
	votes := []TaskVerificationVoteRecord{
		{Verdict: VerdictPass, ScoreBP: 5000},
		{Verdict: VerdictPass, ScoreBP: 7000},
		{Verdict: VerdictPass, ScoreBP: 9000},
	}
	if m := MedianScore(votes, VerdictPass); m != 7000 {
		t.Errorf("median = %d; want 7000", m)
	}
}

func TestMedianScore_EvenCount(t *testing.T) {
	votes := []TaskVerificationVoteRecord{
		{Verdict: VerdictPass, ScoreBP: 5000},
		{Verdict: VerdictPass, ScoreBP: 7000},
		{Verdict: VerdictPass, ScoreBP: 8000},
		{Verdict: VerdictPass, ScoreBP: 9000},
	}
	// (7000 + 8000) / 2 = 7500
	if m := MedianScore(votes, VerdictPass); m != 7500 {
		t.Errorf("median = %d; want 7500", m)
	}
}

func TestMedianScore_Empty(t *testing.T) {
	if m := MedianScore(nil, VerdictPass); m != 0 {
		t.Errorf("median = %d; want 0", m)
	}
}

func TestMedianScore_FiltersVerdict(t *testing.T) {
	votes := []TaskVerificationVoteRecord{
		{Verdict: VerdictPass, ScoreBP: 8000},
		{Verdict: VerdictFail, ScoreBP: 2000},
		{Verdict: VerdictPass, ScoreBP: 6000},
	}
	// Only pass votes: 6000, 8000 → median = 7000
	if m := MedianScore(votes, VerdictPass); m != 7000 {
		t.Errorf("median = %d; want 7000", m)
	}
}

// ── New tests for median-removal and participation floor ──────────────────

func TestFinalizer_AcceptWithLowMedianScore(t *testing.T) {
	// Reproduces the reference test failure: BFT supermajority + diversity met,
	// median score 5386 (below old 6000 threshold). Should now pass.
	f := NewFinalizer(DefaultFinalizerConfig())
	round := makeRoundForFinalizer(350000000000, 150000000000, 0, 250000000000,
		map[string]uint64{"statistical_structural": 150000000000, "embedding_similarity": 200000000000},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictFail, ScoreBP: 5800, AnalyzerFamily: "deterministic_heuristic"},
			{Verdict: VerdictPass, ScoreBP: 5386, AnalyzerFamily: "embedding_similarity"},
			{Verdict: VerdictPass, ScoreBP: 5386, AnalyzerFamily: "embedding_similarity"},
			{Verdict: VerdictPass, ScoreBP: 6081, AnalyzerFamily: "statistical_structural"},
			{Verdict: VerdictFail, ScoreBP: 5800, AnalyzerFamily: "deterministic_heuristic"},
			{Verdict: VerdictPass, ScoreBP: 6081, AnalyzerFamily: "statistical_structural"},
			{Verdict: VerdictFail, ScoreBP: 5800, AnalyzerFamily: "deterministic_heuristic"},
			{Verdict: VerdictPass, ScoreBP: 5386, AnalyzerFamily: "embedding_similarity"},
			{Verdict: VerdictPass, ScoreBP: 6081, AnalyzerFamily: "statistical_structural"},
			{Verdict: VerdictPass, ScoreBP: 5386, AnalyzerFamily: "embedding_similarity"},
		})
	d := f.Evaluate(round, 250000000000, 1500)
	if !d.ShouldFinalize {
		t.Fatal("should finalize: BFT pass + diversity + 3 participating families")
	}
	if d.Verdict != VerdictPass {
		t.Errorf("Verdict = %s; want pass", d.Verdict)
	}
}

func TestFinalizer_ParticipationFloorBlocks(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// Pass weight + diversity met, but only 2 families participated (< 3 floor).
	round := makeRoundForFinalizer(700, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 400, "heuristic": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 8000, AnalyzerFamily: "llm_semantic"},
			{Verdict: VerdictPass, ScoreBP: 7000, AnalyzerFamily: "heuristic"},
		})
	d := f.Evaluate(round, 1000, 1500)
	if d.ShouldFinalize {
		t.Error("should NOT finalize: only 2 participating families < floor of 3")
	}
}

func TestFinalizer_ParticipationFloor_Exactly3(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// 3 families participated (pass, fail, pass). Should finalize.
	round := makeRoundForFinalizer(700, 100, 0, 1000,
		map[string]uint64{"llm_semantic": 400, "heuristic": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 8000, AnalyzerFamily: "llm_semantic"},
			{Verdict: VerdictPass, ScoreBP: 7000, AnalyzerFamily: "heuristic"},
			{Verdict: VerdictFail, ScoreBP: 4000, AnalyzerFamily: "statistical"},
		})
	d := f.Evaluate(round, 1000, 1500)
	if !d.ShouldFinalize {
		t.Fatal("should finalize: 3 participating families meets floor")
	}
	if d.Verdict != VerdictPass {
		t.Errorf("Verdict = %s; want pass", d.Verdict)
	}
}

func TestEffectiveParticipationFloor_SmallNetwork(t *testing.T) {
	if f := EffectiveParticipationFloor(2); f != 2 {
		t.Errorf("EffectiveParticipationFloor(2) = %d; want 2", f)
	}
	if f := EffectiveParticipationFloor(3); f != 3 {
		t.Errorf("EffectiveParticipationFloor(3) = %d; want 3", f)
	}
	if f := EffectiveParticipationFloor(5); f != 3 {
		t.Errorf("EffectiveParticipationFloor(5) = %d; want 3", f)
	}
	if f := EffectiveParticipationFloor(1); f != 2 {
		t.Errorf("EffectiveParticipationFloor(1) = %d; want 2 (hard minimum)", f)
	}
}

func TestFinalizer_ScoresStillInPayload(t *testing.T) {
	// After accept finalization, FinalScoreBP should still contain the median.
	f := NewFinalizer(DefaultFinalizerConfig())
	round := makeRoundForFinalizer(700, 100, 0, 1000,
		map[string]uint64{"a": 400, "b": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 5000, AnalyzerFamily: "a"},
			{Verdict: VerdictPass, ScoreBP: 7000, AnalyzerFamily: "b"},
			{Verdict: VerdictFail, ScoreBP: 3000, AnalyzerFamily: "c"},
		})
	d := f.Evaluate(round, 1000, 1500)
	if !d.ShouldFinalize {
		t.Fatal("should finalize")
	}
	// Median of pass votes: [5000, 7000] → 6000
	if d.FinalScoreBP != 6000 {
		t.Errorf("FinalScoreBP = %d; want 6000 (observability median)", d.FinalScoreBP)
	}
}
