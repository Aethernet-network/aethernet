package autovalidator

import (
	"context"
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// --- Mocks ---

type mockAnalyzer struct {
	id     verification.AnalyzerID
	family verification.FamilyID
	output *verification.AnalysisOutput
	err    error
}

func (m *mockAnalyzer) ID() verification.AnalyzerID   { return m.id }
func (m *mockAnalyzer) Family() verification.FamilyID  { return m.family }
func (m *mockAnalyzer) Version() string                { return "v1" }
func (m *mockAnalyzer) Calibration(_ string) bool      { return false }
func (m *mockAnalyzer) Analyze(_ context.Context, _ verification.AnalysisInput) (*verification.AnalysisOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.output, nil
}

type recordingPublisher struct {
	events []*event.Event
}

func (p *recordingPublisher) Publish(ev *event.Event) error {
	p.events = append(p.events, ev)
	return nil
}

func makeTestRoundForVoter(roundID taskverification.RoundID, subEventID event.EventID) *taskverification.TaskVerificationRound {
	return &taskverification.TaskVerificationRound{
		RoundID:               roundID,
		TaskID:                "task-mv",
		SubmissionEventID:     subEventID,
		WorkerID:              "worker-1",
		PosterID:              "poster-1",
		Category:              "research",
		State:                 taskverification.RoundStateOpen,
		ParticipatingFamilies: make(map[string]uint64),
		Votes:                 []taskverification.TaskVerificationVoteRecord{},
	}
}

func newMultiVoterForTest(analyzers []verification.Analyzer, pub eventPublisher) *MultiVoter {
	kp, _ := crypto.GenerateKeyPair()
	return &MultiVoter{
		analyzers:   analyzers,
		rounds:      nil, // not used in ScoreAndVote directly
		publisher:   pub,
		kp:          kp,
		validatorID: kp.AgentID(),
		clock:       func() int64 { return 1000 },
	}
}

// --- Tests ---

func TestMultiVoter_SingleFamily(t *testing.T) {
	pub := &recordingPublisher{}
	analyzer := &mockAnalyzer{
		id: "heuristic/h:v1", family: verification.FamilyDeterministicHeuristic,
		output: &verification.AnalysisOutput{
			ScoreBP: 7500, Verdict: "pass", Version: "v1",
			ArtifactHash: "abc123",
			ScoreBreakdown: map[string]uint64{"quality": 8000},
		},
	}
	mv := newMultiVoterForTest([]verification.Analyzer{analyzer}, pub)
	round := makeTestRoundForVoter("round-1", "evt-sub-1")

	result, err := mv.ScoreAndVote(context.Background(), round, verification.AnalysisInput{
		TaskID: "task-mv", Category: "research",
	})
	if err != nil {
		t.Fatalf("ScoreAndVote: %v", err)
	}
	if result.AnalyzersRun != 1 {
		t.Errorf("AnalyzersRun = %d; want 1", result.AnalyzersRun)
	}
	if result.VotesEmitted != 1 {
		t.Errorf("VotesEmitted = %d; want 1", result.VotesEmitted)
	}
	if len(pub.events) != 1 {
		t.Fatalf("published events = %d; want 1", len(pub.events))
	}
	if pub.events[0].Type != event.EventTypeTaskVerificationVote {
		t.Errorf("event type = %s; want TaskVerificationVote", pub.events[0].Type)
	}
}

func TestMultiVoter_MultipleFamilies(t *testing.T) {
	pub := &recordingPublisher{}
	analyzers := []verification.Analyzer{
		&mockAnalyzer{
			id: "heuristic/h:v1", family: verification.FamilyDeterministicHeuristic,
			output: &verification.AnalysisOutput{ScoreBP: 7500, Verdict: "pass", Version: "v1", ArtifactHash: "a1"},
		},
		&mockAnalyzer{
			id: "statistical/s:v1", family: verification.FamilyStatisticalStructural,
			output: &verification.AnalysisOutput{ScoreBP: 6500, Verdict: "pass", Version: "v1", ArtifactHash: "a2"},
		},
		&mockAnalyzer{
			id: "embedding/e:v1", family: verification.FamilyEmbeddingSimilarity,
			output: &verification.AnalysisOutput{ScoreBP: 8000, Verdict: "pass", Version: "v1", ArtifactHash: "a3"},
		},
	}
	mv := newMultiVoterForTest(analyzers, pub)
	round := makeTestRoundForVoter("round-multi", "evt-sub-multi")

	result, err := mv.ScoreAndVote(context.Background(), round, verification.AnalysisInput{
		TaskID: "task-mv", Category: "research",
	})
	if err != nil {
		t.Fatalf("ScoreAndVote: %v", err)
	}
	if result.VotesEmitted != 3 {
		t.Errorf("VotesEmitted = %d; want 3", result.VotesEmitted)
	}
	if len(pub.events) != 3 {
		t.Errorf("published events = %d; want 3", len(pub.events))
	}
}

func TestMultiVoter_SkipsAlreadyVoted(t *testing.T) {
	pub := &recordingPublisher{}
	kp, _ := crypto.GenerateKeyPair()
	mv := &MultiVoter{
		analyzers: []verification.Analyzer{
			&mockAnalyzer{
				id: "heuristic/h:v1", family: verification.FamilyDeterministicHeuristic,
				output: &verification.AnalysisOutput{ScoreBP: 7500, Verdict: "pass", Version: "v1"},
			},
		},
		publisher:   pub,
		kp:          kp,
		validatorID: kp.AgentID(),
		clock:       func() int64 { return 1000 },
	}

	round := makeTestRoundForVoter("round-skip", "evt-sub-skip")
	// Pre-populate a vote from this validator for this family.
	round.Votes = []taskverification.TaskVerificationVoteRecord{
		{
			ValidatorID:    kp.AgentID(),
			AnalyzerFamily: string(verification.FamilyDeterministicHeuristic),
			Verdict:        taskverification.VerdictPass,
			ScoreBP:        7500,
			Stake:          300,
		},
	}

	result, _ := mv.ScoreAndVote(context.Background(), round, verification.AnalysisInput{})
	if result.VotesSkipped != 1 {
		t.Errorf("VotesSkipped = %d; want 1", result.VotesSkipped)
	}
	if result.VotesEmitted != 0 {
		t.Errorf("VotesEmitted = %d; want 0", result.VotesEmitted)
	}
	if len(pub.events) != 0 {
		t.Errorf("should not have published any events")
	}
}

func TestMultiVoter_AnalyzerFailureContinues(t *testing.T) {
	pub := &recordingPublisher{}
	analyzers := []verification.Analyzer{
		&mockAnalyzer{
			id: "heuristic/h:v1", family: verification.FamilyDeterministicHeuristic,
			err: fmt.Errorf("API timeout"),
		},
		&mockAnalyzer{
			id: "statistical/s:v1", family: verification.FamilyStatisticalStructural,
			output: &verification.AnalysisOutput{ScoreBP: 6500, Verdict: "pass", Version: "v1", ArtifactHash: "ok"},
		},
	}
	mv := newMultiVoterForTest(analyzers, pub)
	round := makeTestRoundForVoter("round-fail", "evt-sub-fail")

	result, err := mv.ScoreAndVote(context.Background(), round, verification.AnalysisInput{})
	if err != nil {
		t.Fatalf("ScoreAndVote should not error: %v", err)
	}
	if result.AnalyzersFailed != 1 {
		t.Errorf("AnalyzersFailed = %d; want 1", result.AnalyzersFailed)
	}
	if result.VotesEmitted != 1 {
		t.Errorf("VotesEmitted = %d; want 1 (surviving analyzer)", result.VotesEmitted)
	}
}

func TestMultiVoter_RoundNotOpen(t *testing.T) {
	pub := &recordingPublisher{}
	mv := newMultiVoterForTest([]verification.Analyzer{
		&mockAnalyzer{
			id: "h/h:v1", family: verification.FamilyDeterministicHeuristic,
			output: &verification.AnalysisOutput{ScoreBP: 7500, Verdict: "pass", Version: "v1"},
		},
	}, pub)

	round := makeTestRoundForVoter("round-closed", "evt-sub-closed")
	round.State = taskverification.RoundStateFinalizedAccept

	result, _ := mv.ScoreAndVote(context.Background(), round, verification.AnalysisInput{})
	if result.AnalyzersRun != 0 {
		t.Errorf("AnalyzersRun = %d; want 0 (round not open)", result.AnalyzersRun)
	}
	if len(pub.events) != 0 {
		t.Errorf("should not publish for closed round")
	}
}

func TestMultiVoter_VotePayloadCorrect(t *testing.T) {
	pub := &recordingPublisher{}
	analyzer := &mockAnalyzer{
		id: "heuristic/h:v1", family: verification.FamilyDeterministicHeuristic,
		output: &verification.AnalysisOutput{
			ScoreBP: 7500, Verdict: "pass", Version: "v1",
			ArtifactHash:  "artifact123",
			ScoreBreakdown: map[string]uint64{"quality": 8000, "relevance": 7000},
		},
	}
	mv := newMultiVoterForTest([]verification.Analyzer{analyzer}, pub)
	round := makeTestRoundForVoter("round-payload", "evt-sub-payload")
	round.TaskID = "task-payload-test"

	_, _ = mv.ScoreAndVote(context.Background(), round, verification.AnalysisInput{
		TaskID: "task-payload-test",
	})

	if len(pub.events) != 1 {
		t.Fatalf("expected 1 published event")
	}

	ev := pub.events[0]
	// Verify semantic parent.
	if len(ev.CausalRefs) != 1 || ev.CausalRefs[0] != "evt-sub-payload" {
		t.Errorf("CausalRefs = %v; want [evt-sub-payload]", ev.CausalRefs)
	}

	vp, err := event.GetPayload[event.TaskVerificationVotePayload](ev)
	if err != nil {
		t.Fatalf("GetPayload: %v", err)
	}
	if vp.RoundID != "round-payload" {
		t.Errorf("RoundID = %s; want round-payload", vp.RoundID)
	}
	if vp.TaskID != "task-payload-test" {
		t.Errorf("TaskID = %s; want task-payload-test", vp.TaskID)
	}
	if vp.Verdict != "pass" {
		t.Errorf("Verdict = %s; want pass", vp.Verdict)
	}
	if vp.ScoreBP != 7500 {
		t.Errorf("ScoreBP = %d; want 7500", vp.ScoreBP)
	}
	if vp.AnalyzerFamily != "deterministic_heuristic" {
		t.Errorf("AnalyzerFamily = %s; want deterministic_heuristic", vp.AnalyzerFamily)
	}
	if vp.AnalysisArtifactHash != "artifact123" {
		t.Errorf("ArtifactHash = %s; want artifact123", vp.AnalysisArtifactHash)
	}
}
