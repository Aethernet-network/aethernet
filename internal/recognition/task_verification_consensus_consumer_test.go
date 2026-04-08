package recognition_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

func newConsensusTestStore(t *testing.T) *taskverification.BadgerStore {
	t.Helper()
	opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return taskverification.NewBadgerStore(db)
}

func makeConsensusEvent(roundID, taskID, verdict string, scoreBP uint64) *event.Event {
	payload, _ := json.Marshal(event.TaskVerificationConsensusPayload{
		Version:              1,
		RoundID:              roundID,
		TaskID:               taskID,
		FinalVerdict:         verdict,
		FinalScoreBP:         scoreBP,
		FinalizationTimeUnix: time.Now().Unix(),
	})
	return &event.Event{
		ID:      event.EventID("evt-consensus-" + roundID),
		Type:    event.EventTypeTaskVerificationConsensus,
		Payload: payload,
	}
}

func TestConsensusConsumer_AppliesFinalization(t *testing.T) {
	store := newConsensusTestStore(t)
	ctx := context.Background()

	// Create an open round.
	r, _ := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID: "task-cc", SubmissionEventID: "evt-sub-cc",
		WorkerID: "w", PosterID: "p", Category: "research",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	_ = store.SaveRound(ctx, r)

	consumer := recognition.NewTaskVerificationConsensusConsumer(store, nil, nil)
	ev := makeConsensusEvent(string(r.RoundID), "task-cc", "pass", 7500)

	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.State != taskverification.RoundStateFinalizedAccept {
		t.Errorf("State = %s; want finalized_accept", updated.State)
	}
	if updated.FinalScoreBP != 7500 {
		t.Errorf("FinalScoreBP = %d; want 7500", updated.FinalScoreBP)
	}
}

func TestConsensusConsumer_Idempotent(t *testing.T) {
	store := newConsensusTestStore(t)
	ctx := context.Background()

	r, _ := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID: "task-idem-cc", SubmissionEventID: "evt-sub-idem-cc",
		WorkerID: "w", PosterID: "p", Category: "research",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	_ = store.SaveRound(ctx, r)

	consumer := recognition.NewTaskVerificationConsensusConsumer(store, nil, nil)
	ev := makeConsensusEvent(string(r.RoundID), "task-idem-cc", "fail", 0)

	_ = consumer.Consume(ctx, ev)
	// Second application — should be no-op.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("second Consume should not error: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.State != taskverification.RoundStateFinalizedReject {
		t.Errorf("State = %s; want finalized_reject", updated.State)
	}
}

func TestConsensusConsumer_ReplaySafety(t *testing.T) {
	store := newConsensusTestStore(t)
	ctx := context.Background()

	// Cold round in store (simulating post-restart state).
	r, _ := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID: "task-replay", SubmissionEventID: "evt-sub-replay",
		WorkerID: "w", PosterID: "p", Category: "code",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	_ = store.SaveRound(ctx, r)

	// Consensus event from replay.
	consumer := recognition.NewTaskVerificationConsensusConsumer(store, nil, nil)
	ev := makeConsensusEvent(string(r.RoundID), "task-replay", "pass", 8000)

	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.State != taskverification.RoundStateFinalizedAccept {
		t.Errorf("State = %s; want finalized_accept (replay applied)", updated.State)
	}
}
