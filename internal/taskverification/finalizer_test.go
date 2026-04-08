package taskverification

import (
	"testing"
)

func makeRoundForFinalizer(passWeight, failWeight, abstainWeight, totalActive uint64, families map[string]uint64, votes []TaskVerificationVoteRecord) *TaskVerificationRound {
	return &TaskVerificationRound{
		RoundID:               "round-fin-test",
		State:                 RoundStateOpen,
		PassWeight:            passWeight,
		FailWeight:            failWeight,
		AbstainWeight:         abstainWeight,
		ParticipatingFamilies: families,
		Votes:                 votes,
		DeadlineUnix:          2000,
	}
}

func TestFinalizer_AcceptSupermajority(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// 700/1000 pass weight (70% > 66.67%), 2 families, median score 7500 (> 6000)
	round := makeRoundForFinalizer(700, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 400, "heuristic": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 7000, AnalyzerFamily: "llm_semantic"},
			{Verdict: VerdictPass, ScoreBP: 8000, AnalyzerFamily: "heuristic"},
		})
	d := f.Evaluate(round, 1000, 1500)
	if !d.ShouldFinalize {
		t.Fatal("should finalize")
	}
	if d.Verdict != VerdictPass {
		t.Errorf("Verdict = %s; want pass", d.Verdict)
	}
	if d.FinalScoreBP != 7500 {
		t.Errorf("FinalScoreBP = %d; want 7500", d.FinalScoreBP)
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

func TestFinalizer_AcceptInsufficientScore(t *testing.T) {
	f := NewFinalizer(DefaultFinalizerConfig())
	// Supermajority + diversity, but median score 4000 < 6000
	round := makeRoundForFinalizer(700, 0, 0, 1000,
		map[string]uint64{"llm_semantic": 400, "heuristic": 300},
		[]TaskVerificationVoteRecord{
			{Verdict: VerdictPass, ScoreBP: 3000, AnalyzerFamily: "llm_semantic"},
			{Verdict: VerdictPass, ScoreBP: 5000, AnalyzerFamily: "heuristic"},
		})
	d := f.Evaluate(round, 1000, 1500)
	if d.ShouldFinalize {
		t.Error("should NOT finalize with median score below threshold")
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
