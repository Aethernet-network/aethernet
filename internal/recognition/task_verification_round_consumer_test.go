package recognition_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

func newTestVerificationStore(t *testing.T) *taskverification.BadgerStore {
	t.Helper()
	opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return taskverification.NewBadgerStore(db)
}

func makeSubmittedEventForRound(taskID, claimerID string) *event.Event {
	payload, _ := json.Marshal(map[string]string{
		"task_id":    taskID,
		"claimer_id": claimerID,
	})
	return &event.Event{
		ID:      event.EventID("evt-submitted-" + taskID),
		Type:    event.EventTypeTaskSubmitted,
		Payload: payload,
	}
}

func stubTaskMeta(posterID, category string) recognition.TaskMetadataFunc {
	return func(taskID string) (string, string, error) {
		if posterID == "" {
			return "", "", errors.New("task not found")
		}
		return posterID, category, nil
	}
}

func fixedClock(t int64) func() int64 {
	return func() int64 { return t }
}

func fixedValidatorVer(v uint64) func() uint64 {
	return func() uint64 { return v }
}

func TestRoundConsumer_OpensRoundOnTaskSubmitted(t *testing.T) {
	store := newTestVerificationStore(t)
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("poster-1", "research"),
		fixedValidatorVer(3),
		fixedClock(1000),
	)

	ev := makeSubmittedEventForRound("task-1", "worker-1")
	ctx := context.Background()

	// Verify interested.
	if !consumer.Interested(ev) {
		t.Fatal("should be interested in TaskSubmitted")
	}

	// Ready check.
	ready, _, err := consumer.Ready(ctx, ev, nil)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if !ready {
		t.Fatal("should be ready when task metadata is available")
	}

	// Consume.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	// Verify round was created.
	r, err := store.LoadRoundBySubmissionEvent(ctx, ev.ID)
	if err != nil {
		t.Fatalf("LoadRoundBySubmissionEvent: %v", err)
	}
	if r.TaskID != "task-1" {
		t.Errorf("TaskID = %s; want task-1", r.TaskID)
	}
	if string(r.WorkerID) != "worker-1" {
		t.Errorf("WorkerID = %s; want worker-1", r.WorkerID)
	}
	if string(r.PosterID) != "poster-1" {
		t.Errorf("PosterID = %s; want poster-1", r.PosterID)
	}
	if r.Category != "research" {
		t.Errorf("Category = %s; want research", r.Category)
	}
	if r.State != taskverification.RoundStateOpen {
		t.Errorf("State = %s; want open", r.State)
	}
	if r.DeadlineUnix != 1060 {
		t.Errorf("DeadlineUnix = %d; want 1060", r.DeadlineUnix)
	}
}

func TestRoundConsumer_Idempotent(t *testing.T) {
	store := newTestVerificationStore(t)
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("poster-1", "research"),
		fixedValidatorVer(1),
		fixedClock(1000),
	)

	ev := makeSubmittedEventForRound("task-idem", "worker-1")
	ctx := context.Background()

	// First consume.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume 1: %v", err)
	}

	// Second consume — same event.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume 2: %v", err)
	}

	// Should still be exactly one round.
	rounds, err := store.LoadRoundsByTaskID(ctx, "task-idem")
	if err != nil {
		t.Fatalf("LoadRoundsByTaskID: %v", err)
	}
	if len(rounds) != 1 {
		t.Errorf("got %d rounds; want 1", len(rounds))
	}
}

func TestRoundConsumer_BindsValidatorSetVersion(t *testing.T) {
	store := newTestVerificationStore(t)
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("poster-1", "code"),
		fixedValidatorVer(42),
		fixedClock(2000),
	)

	ev := makeSubmittedEventForRound("task-vsv", "worker-1")
	ctx := context.Background()
	_ = consumer.Consume(ctx, ev)

	r, _ := store.LoadRoundBySubmissionEvent(ctx, ev.ID)
	if r.ValidatorSetVersion != 42 {
		t.Errorf("ValidatorSetVersion = %d; want 42", r.ValidatorSetVersion)
	}
}

func TestRoundConsumer_DeterministicRoundID(t *testing.T) {
	store := newTestVerificationStore(t)
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("poster-1", "research"),
		fixedValidatorVer(1),
		fixedClock(1000),
	)

	// Two events with the same submission event ID (simulating local + remote).
	ev1 := makeSubmittedEventForRound("task-det", "worker-1")
	ev2 := makeSubmittedEventForRound("task-det", "worker-1")
	// Force same event ID.
	ev2.ID = ev1.ID

	ctx := context.Background()
	_ = consumer.Consume(ctx, ev1)
	_ = consumer.Consume(ctx, ev2) // idempotent

	r, _ := store.LoadRoundBySubmissionEvent(ctx, ev1.ID)
	expected := taskverification.NewRoundID(ev1.ID)
	if r.RoundID != expected {
		t.Errorf("RoundID = %s; want %s", r.RoundID, expected)
	}
}

func TestRoundConsumer_IgnoresNonTaskSubmitted(t *testing.T) {
	store := newTestVerificationStore(t)
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("poster-1", "research"),
		fixedValidatorVer(1),
		fixedClock(1000),
	)

	// TaskClaimed event.
	payload, _ := json.Marshal(map[string]string{"task_id": "task-x", "claimer_id": "c"})
	ev := &event.Event{
		ID:      "evt-claimed-1",
		Type:    event.EventTypeTaskClaimed,
		Payload: payload,
	}

	if consumer.Interested(ev) {
		t.Error("should not be interested in TaskClaimed")
	}
}

func TestRoundConsumer_DefersIfTaskMissing(t *testing.T) {
	store := newTestVerificationStore(t)
	// Task metadata not available (returns error).
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("", ""),
		fixedValidatorVer(1),
		fixedClock(1000),
	)

	ev := makeSubmittedEventForRound("task-missing", "worker-1")
	ctx := context.Background()

	// Ready should return false with prerequisite key.
	ready, prereq, err := consumer.Ready(ctx, ev, nil)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ready {
		t.Error("should NOT be ready when task metadata is missing")
	}
	expectedPrereq := recognition.PrerequisiteKeyTaskMetadata("task-missing")
	if prereq != expectedPrereq {
		t.Errorf("prereq = %s; want %s", prereq, expectedPrereq)
	}
}

func TestRoundConsumer_BootstrapModeCommitteeNil(t *testing.T) {
	store := newTestVerificationStore(t)
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("poster-1", "research"),
		fixedValidatorVer(1),
		fixedClock(1000),
	)

	ev := makeSubmittedEventForRound("task-boot", "worker-1")
	ctx := context.Background()
	_ = consumer.Consume(ctx, ev)

	r, _ := store.LoadRoundBySubmissionEvent(ctx, ev.ID)
	if r.Committee != nil {
		t.Errorf("Committee should be nil in bootstrap mode; got %v", r.Committee)
	}
}

func TestRoundConsumer_RaceConditionTwoConcurrentCommits(t *testing.T) {
	store := newTestVerificationStore(t)
	consumer := recognition.NewTaskVerificationRoundConsumer(
		store,
		stubTaskMeta("poster-1", "research"),
		fixedValidatorVer(1),
		fixedClock(1000),
	)

	ev := makeSubmittedEventForRound("task-race", "worker-1")
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = consumer.Consume(ctx, ev)
		}(i)
	}
	wg.Wait()

	// Both should succeed (idempotent — second is a no-op or upsert).
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	rounds, _ := store.LoadRoundsByTaskID(ctx, "task-race")
	if len(rounds) != 1 {
		t.Errorf("got %d rounds; want 1", len(rounds))
	}
}
