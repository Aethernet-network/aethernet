package recognition_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

func newVoteTestStore(t *testing.T) *taskverification.BadgerStore {
	t.Helper()
	opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return taskverification.NewBadgerStore(db)
}

func makeTVVoteEvent(roundID, taskID, submissionEventID, validatorID, verdict, family string, scoreBP uint64) *event.Event {
	payload, _ := json.Marshal(event.TaskVerificationVotePayload{
		Version:           1,
		RoundID:           roundID,
		TaskID:            taskID,
		SubmissionEventID: submissionEventID,
		ValidatorID:       validatorID,
		Verdict:           verdict,
		ScoreBP:           scoreBP,
		AnalyzerFamily:    family,
		AnalyzerVersion:   "v1",
		PolicyVersion:     "bootstrap_v1",
		TimestampUnix:     time.Now().Unix(),
	})
	return &event.Event{
		ID:      event.EventID("evt-vote-" + validatorID + "-" + roundID),
		Type:    event.EventTypeTaskVerificationVote,
		Payload: payload,
	}
}

func saveOpenRound(t *testing.T, store *taskverification.BadgerStore, taskID string, subEventID event.EventID) *taskverification.TaskVerificationRound {
	t.Helper()
	r, err := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID:                taskID,
		SubmissionEventID:     subEventID,
		WorkerID:              "worker-1",
		PosterID:              "poster-1",
		Category:              "research",
		ValidatorSetVersion:   1,
		AnalyzerPolicyID:      "bootstrap_v1",
		DiversityFloor:        2,
		AcceptanceThresholdBP: 6000,
		DeadlineSeconds:       60,
		Now:                   time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	if err := store.SaveRound(context.Background(), r); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}
	return r
}

func allEligible(stake uint64) recognition.ValidatorWeightFunc {
	return func(id crypto.AgentID) (uint64, bool) { return stake, true }
}

func TestVoteConsumer_AppliesValidVote(t *testing.T) {
	store := newVoteTestStore(t)
	r := saveOpenRound(t, store, "task-1", "evt-sub-1")

	consumer := recognition.NewTaskVerificationVoteConsumer(store, allEligible(300))
	ev := makeTVVoteEvent(string(r.RoundID), "task-1", "evt-sub-1", "validator-a", "pass", "llm_semantic", 7500)
	ctx := context.Background()

	if !consumer.Interested(ev) {
		t.Fatal("should be interested")
	}
	ready, _, _ := consumer.Ready(ctx, ev, nil)
	if !ready {
		t.Fatal("should be ready when round exists")
	}

	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.PassWeight != 300 {
		t.Errorf("PassWeight = %d; want 300", updated.PassWeight)
	}
	if len(updated.Votes) != 1 {
		t.Fatalf("Votes len = %d; want 1", len(updated.Votes))
	}
	if updated.Votes[0].ScoreBP != 7500 {
		t.Errorf("ScoreBP = %d; want 7500", updated.Votes[0].ScoreBP)
	}
}

func TestVoteConsumer_IdempotentDuplicate(t *testing.T) {
	store := newVoteTestStore(t)
	r := saveOpenRound(t, store, "task-idem", "evt-sub-idem")

	consumer := recognition.NewTaskVerificationVoteConsumer(store, allEligible(300))
	ev := makeTVVoteEvent(string(r.RoundID), "task-idem", "evt-sub-idem", "validator-a", "pass", "llm_semantic", 7500)
	ctx := context.Background()

	_ = consumer.Consume(ctx, ev)
	_ = consumer.Consume(ctx, ev) // duplicate

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.PassWeight != 300 {
		t.Errorf("PassWeight = %d; want 300 (duplicate should not double-count)", updated.PassWeight)
	}
	if len(updated.Votes) != 1 {
		t.Errorf("Votes len = %d; want 1", len(updated.Votes))
	}
}

func TestVoteConsumer_DetectsEquivocation(t *testing.T) {
	store := newVoteTestStore(t)
	r := saveOpenRound(t, store, "task-equi", "evt-sub-equi")

	consumer := recognition.NewTaskVerificationVoteConsumer(store, allEligible(300))
	ctx := context.Background()

	ev1 := makeTVVoteEvent(string(r.RoundID), "task-equi", "evt-sub-equi", "validator-a", "pass", "llm_semantic", 7500)
	ev2 := makeTVVoteEvent(string(r.RoundID), "task-equi", "evt-sub-equi", "validator-a", "fail", "llm_semantic", 3000)

	_ = consumer.Consume(ctx, ev1)
	// Second consume with different verdict — equivocation is logged but returns nil.
	err := consumer.Consume(ctx, ev2)
	if err != nil {
		t.Fatalf("equivocation should not return error to caller: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.PassWeight != 300 {
		t.Errorf("PassWeight = %d; want 300 (equivocating vote should not be applied)", updated.PassWeight)
	}
}

func TestVoteConsumer_IneligibleValidator(t *testing.T) {
	store := newVoteTestStore(t)
	r := saveOpenRound(t, store, "task-inelig", "evt-sub-inelig")

	// Weight function returns ineligible.
	noOne := recognition.ValidatorWeightFunc(func(id crypto.AgentID) (uint64, bool) { return 0, false })
	consumer := recognition.NewTaskVerificationVoteConsumer(store, noOne)

	ev := makeTVVoteEvent(string(r.RoundID), "task-inelig", "evt-sub-inelig", "bad-validator", "pass", "llm_semantic", 7500)
	ctx := context.Background()

	// Should succeed (not error) but vote should not be applied.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if len(updated.Votes) != 0 {
		t.Errorf("Votes len = %d; want 0 (ineligible validator)", len(updated.Votes))
	}
}

func TestVoteConsumer_RoundNotYetOpen(t *testing.T) {
	store := newVoteTestStore(t)
	// Do NOT save a round — simulate vote arriving before round is opened.
	consumer := recognition.NewTaskVerificationVoteConsumer(store, allEligible(300))

	roundID := string(taskverification.NewRoundID("evt-sub-notyet"))
	ev := makeTVVoteEvent(roundID, "task-notyet", "evt-sub-notyet", "validator-a", "pass", "llm_semantic", 7500)
	ctx := context.Background()

	ready, prereq, err := consumer.Ready(ctx, ev, nil)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ready {
		t.Error("should NOT be ready when round doesn't exist")
	}
	expected := recognition.PrerequisiteKeyTVRound(roundID)
	if prereq != expected {
		t.Errorf("prereq = %s; want %s", prereq, expected)
	}
}

func TestVoteConsumer_PostFinalizationVote(t *testing.T) {
	store := newVoteTestStore(t)
	r := saveOpenRound(t, store, "task-finalized", "evt-sub-fin")
	// Finalize the round.
	_ = r.Transition(taskverification.RoundStateFinalizedAccept, time.Now().Unix())
	_ = store.SaveRound(context.Background(), r)

	consumer := recognition.NewTaskVerificationVoteConsumer(store, allEligible(300))
	ev := makeTVVoteEvent(string(r.RoundID), "task-finalized", "evt-sub-fin", "late-validator", "pass", "llm_semantic", 7000)
	ctx := context.Background()

	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	// Vote should be recorded for audit but NOT change aggregation.
	if len(updated.Votes) != 1 {
		t.Fatalf("Votes len = %d; want 1 (audit record)", len(updated.Votes))
	}
	if updated.PassWeight != 0 {
		t.Errorf("PassWeight = %d; want 0 (post-finalization should not change weights)", updated.PassWeight)
	}
}

func TestVoteConsumer_MultipleFamilies(t *testing.T) {
	store := newVoteTestStore(t)
	r := saveOpenRound(t, store, "task-families", "evt-sub-fam")

	consumer := recognition.NewTaskVerificationVoteConsumer(store, allEligible(200))
	ctx := context.Background()

	_ = consumer.Consume(ctx, makeTVVoteEvent(string(r.RoundID), "task-families", "evt-sub-fam", "v1", "pass", "llm_semantic", 7500))
	_ = consumer.Consume(ctx, makeTVVoteEvent(string(r.RoundID), "task-families", "evt-sub-fam", "v2", "pass", "heuristic", 6500))

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.DistinctPassFamilies() != 2 {
		t.Errorf("DistinctPassFamilies = %d; want 2", updated.DistinctPassFamilies())
	}
	if updated.PassWeight != 400 {
		t.Errorf("PassWeight = %d; want 400", updated.PassWeight)
	}
}
