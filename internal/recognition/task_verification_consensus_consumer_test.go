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
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// TestConsensusConsumer_CalibrationAppliedOnce exercises the step-2 §D2
// wiring: the consumer increments each participating family's calibration
// counter exactly once per round, guarded by round.CalibrationApplied so
// a replay does not double-count.
func TestConsensusConsumer_CalibrationAppliedOnce(t *testing.T) {
	store := newConsensusTestStore(t)
	calDB, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatalf("open cal db: %v", err)
	}
	t.Cleanup(func() { calDB.Close() })
	calibration := taskverification.NewCalibrationStore(calDB, taskverification.CalibrationConfig{DefaultThreshold: 100})

	ctx := context.Background()

	// Create an open round and populate it with two votes from distinct
	// families (det_heuristic, stat_structural) plus a duplicate from
	// det_heuristic to exercise the distinct-family guard.
	r, _ := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID: "task-cal", SubmissionEventID: "evt-sub-cal",
		WorkerID: "w", PosterID: "p", Category: "research",
		DiversityFloor: 2, AcceptanceThresholdBP: 6000,
		DeadlineSeconds: 60, Now: 1000,
	})
	r.Votes = []taskverification.TaskVerificationVoteRecord{
		{ValidatorID: "v1", AnalyzerFamily: string(verification.FamilyDeterministicHeuristic), Verdict: taskverification.VerdictPass},
		{ValidatorID: "v2", AnalyzerFamily: string(verification.FamilyStatisticalStructural), Verdict: taskverification.VerdictPass},
		{ValidatorID: "v3", AnalyzerFamily: string(verification.FamilyDeterministicHeuristic), Verdict: taskverification.VerdictPass},
	}
	_ = store.SaveRound(ctx, r)

	consumer := recognition.NewTaskVerificationConsensusConsumer(store, nil, calibration, nil)
	ev := makeConsensusEvent(string(r.RoundID), "task-cal", "pass", 7500)

	// First Consume — should increment each distinct family once.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("first Consume: %v", err)
	}

	gotDH, _ := calibration.Get(ctx, "research", verification.FamilyDeterministicHeuristic)
	gotSS, _ := calibration.Get(ctx, "research", verification.FamilyStatisticalStructural)
	if gotDH != 1 {
		t.Fatalf("det_heuristic count after first Consume: want 1, got %d", gotDH)
	}
	if gotSS != 1 {
		t.Fatalf("stat_structural count after first Consume: want 1, got %d", gotSS)
	}

	// Verify CalibrationApplied was persisted.
	reloaded, err := store.LoadRound(ctx, r.RoundID)
	if err != nil {
		t.Fatalf("LoadRound: %v", err)
	}
	if !reloaded.CalibrationApplied {
		t.Fatalf("CalibrationApplied must be true after first apply")
	}

	// Replay — same event, same round. Must be a no-op for calibration.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("second Consume (replay): %v", err)
	}

	gotDH2, _ := calibration.Get(ctx, "research", verification.FamilyDeterministicHeuristic)
	gotSS2, _ := calibration.Get(ctx, "research", verification.FamilyStatisticalStructural)
	if gotDH2 != 1 {
		t.Fatalf("det_heuristic count after replay: want 1 (idempotent), got %d", gotDH2)
	}
	if gotSS2 != 1 {
		t.Fatalf("stat_structural count after replay: want 1 (idempotent), got %d", gotSS2)
	}
}

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

	consumer := recognition.NewTaskVerificationConsensusConsumer(store, nil, nil, nil)
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

	consumer := recognition.NewTaskVerificationConsensusConsumer(store, nil, nil, nil)
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
	consumer := recognition.NewTaskVerificationConsensusConsumer(store, nil, nil, nil)
	ev := makeConsensusEvent(string(r.RoundID), "task-replay", "pass", 8000)

	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	updated, _ := store.LoadRound(ctx, r.RoundID)
	if updated.State != taskverification.RoundStateFinalizedAccept {
		t.Errorf("State = %s; want finalized_accept (replay applied)", updated.State)
	}
}
