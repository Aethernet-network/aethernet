package taskverification

import (
	"context"
	"sync"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/event"
)

func newTestDB(t *testing.T) *badger.DB {
	t.Helper()
	opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestStore(t *testing.T) *BadgerStore {
	t.Helper()
	return NewBadgerStore(newTestDB(t))
}

func makeStorableRound(taskID string, subEventID event.EventID) *TaskVerificationRound {
	roundID := NewRoundID(subEventID)
	return &TaskVerificationRound{
		RoundID:               roundID,
		TaskID:                taskID,
		SubmissionEventID:     subEventID,
		WorkerID:              "worker-1",
		PosterID:              "poster-1",
		Category:              "research",
		ValidatorSetVersion:   1,
		AnalyzerPolicyID:      "default",
		DiversityFloor:        DefaultDiversityFloor,
		AcceptanceThresholdBP: DefaultAcceptanceThresholdBP,
		OpenedAtUnix:          time.Now().Unix(),
		DeadlineUnix:          time.Now().Unix() + DefaultRoundDeadlineSeconds,
		State:                 RoundStateOpen,
		ParticipatingFamilies: map[string]uint64{},
		Votes:                 []TaskVerificationVoteRecord{},
	}
}

func TestBadgerStore_SaveAndLoadRound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := makeStorableRound("task-1", "evt-sub-1")
	r.PassWeight = 500
	r.FailWeight = 100
	r.ParticipatingFamilies = map[string]uint64{"llm_semantic": 300}
	r.Votes = []TaskVerificationVoteRecord{
		{ValidatorID: "v1", Verdict: VerdictPass, ScoreBP: 7500, AnalyzerFamily: "llm_semantic", Stake: 300, TimestampUnix: 1000},
	}

	if err := s.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	loaded, err := s.LoadRound(ctx, r.RoundID)
	if err != nil {
		t.Fatalf("LoadRound: %v", err)
	}

	if loaded.RoundID != r.RoundID {
		t.Errorf("RoundID = %s; want %s", loaded.RoundID, r.RoundID)
	}
	if loaded.TaskID != r.TaskID {
		t.Errorf("TaskID = %s; want %s", loaded.TaskID, r.TaskID)
	}
	if loaded.PassWeight != r.PassWeight {
		t.Errorf("PassWeight = %d; want %d", loaded.PassWeight, r.PassWeight)
	}
	if loaded.State != r.State {
		t.Errorf("State = %s; want %s", loaded.State, r.State)
	}
	if len(loaded.Votes) != 1 {
		t.Fatalf("Votes len = %d; want 1", len(loaded.Votes))
	}
	if loaded.Votes[0].ScoreBP != 7500 {
		t.Errorf("Votes[0].ScoreBP = %d; want 7500", loaded.Votes[0].ScoreBP)
	}
	if loaded.ParticipatingFamilies["llm_semantic"] != 300 {
		t.Errorf("ParticipatingFamilies[llm_semantic] = %d; want 300", loaded.ParticipatingFamilies["llm_semantic"])
	}
}

func TestBadgerStore_LoadRoundBySubmissionEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	subID := event.EventID("evt-lookup-test")
	r := makeStorableRound("task-2", subID)
	if err := s.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	loaded, err := s.LoadRoundBySubmissionEvent(ctx, subID)
	if err != nil {
		t.Fatalf("LoadRoundBySubmissionEvent: %v", err)
	}
	if loaded.RoundID != r.RoundID {
		t.Errorf("RoundID = %s; want %s", loaded.RoundID, r.RoundID)
	}

	// Non-existent submission event.
	_, err = s.LoadRoundBySubmissionEvent(ctx, "nonexistent")
	if err != ErrRoundNotFound {
		t.Errorf("want ErrRoundNotFound; got %v", err)
	}
}

func TestBadgerStore_LoadRoundsByTaskID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two rounds for same task (e.g., re-verification after rejection).
	r1 := makeStorableRound("task-multi", "evt-sub-a")
	r2 := makeStorableRound("task-multi", "evt-sub-b")
	if err := s.SaveRound(ctx, r1); err != nil {
		t.Fatalf("SaveRound r1: %v", err)
	}
	if err := s.SaveRound(ctx, r2); err != nil {
		t.Fatalf("SaveRound r2: %v", err)
	}

	rounds, err := s.LoadRoundsByTaskID(ctx, "task-multi")
	if err != nil {
		t.Fatalf("LoadRoundsByTaskID: %v", err)
	}
	if len(rounds) != 2 {
		t.Fatalf("got %d rounds; want 2", len(rounds))
	}

	// Empty result for unknown task.
	rounds, err = s.LoadRoundsByTaskID(ctx, "unknown-task")
	if err != nil {
		t.Fatalf("LoadRoundsByTaskID(unknown): %v", err)
	}
	if len(rounds) != 0 {
		t.Errorf("got %d rounds; want 0", len(rounds))
	}
}

func TestBadgerStore_StateTransitionPersistence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := makeStorableRound("task-transition", "evt-trans")

	// Save in Open state.
	if err := s.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound (open): %v", err)
	}

	openRounds, _ := s.ListRoundsByState(ctx, RoundStateOpen)
	if len(openRounds) != 1 {
		t.Fatalf("ListRoundsByState(Open) = %d; want 1", len(openRounds))
	}

	// Transition to FinalizedAccept and re-save.
	now := time.Now().Unix()
	if err := r.Transition(RoundStateFinalizedAccept, now); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := s.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound (finalized): %v", err)
	}

	// Open list should now be empty.
	openRounds, _ = s.ListRoundsByState(ctx, RoundStateOpen)
	if len(openRounds) != 0 {
		t.Errorf("ListRoundsByState(Open) after transition = %d; want 0", len(openRounds))
	}

	// FinalizedAccept list should have 1.
	acceptRounds, _ := s.ListRoundsByState(ctx, RoundStateFinalizedAccept)
	if len(acceptRounds) != 1 {
		t.Errorf("ListRoundsByState(FinalizedAccept) = %d; want 1", len(acceptRounds))
	}
}

func TestBadgerStore_DeleteRound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	subID := event.EventID("evt-delete-test")
	r := makeStorableRound("task-del", subID)
	if err := s.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	// Verify it exists via all indexes.
	if _, err := s.LoadRound(ctx, r.RoundID); err != nil {
		t.Fatalf("LoadRound before delete: %v", err)
	}
	if _, err := s.LoadRoundBySubmissionEvent(ctx, subID); err != nil {
		t.Fatalf("LoadRoundBySubmissionEvent before delete: %v", err)
	}

	// Delete.
	if err := s.DeleteRound(ctx, r.RoundID); err != nil {
		t.Fatalf("DeleteRound: %v", err)
	}

	// All lookups should fail.
	if _, err := s.LoadRound(ctx, r.RoundID); err != ErrRoundNotFound {
		t.Errorf("LoadRound after delete: want ErrRoundNotFound, got %v", err)
	}
	if _, err := s.LoadRoundBySubmissionEvent(ctx, subID); err != ErrRoundNotFound {
		t.Errorf("LoadRoundBySubmissionEvent after delete: want ErrRoundNotFound, got %v", err)
	}
	rounds, _ := s.LoadRoundsByTaskID(ctx, "task-del")
	if len(rounds) != 0 {
		t.Errorf("LoadRoundsByTaskID after delete: got %d; want 0", len(rounds))
	}
	openRounds, _ := s.ListOpenRounds(ctx)
	if len(openRounds) != 0 {
		t.Errorf("ListOpenRounds after delete: got %d; want 0", len(openRounds))
	}

	// Delete non-existent.
	if err := s.DeleteRound(ctx, "nonexistent"); err != ErrRoundNotFound {
		t.Errorf("DeleteRound(nonexistent): want ErrRoundNotFound, got %v", err)
	}
}

func TestBadgerStore_ListOpenRounds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r1 := makeStorableRound("task-a", "evt-a")
	r2 := makeStorableRound("task-b", "evt-b")
	r3 := makeStorableRound("task-c", "evt-c")
	_ = s.SaveRound(ctx, r1)
	_ = s.SaveRound(ctx, r2)
	_ = s.SaveRound(ctx, r3)

	// Finalize one.
	_ = r2.Transition(RoundStateFinalizedReject, time.Now().Unix())
	_ = s.SaveRound(ctx, r2)

	open, _ := s.ListOpenRounds(ctx)
	if len(open) != 2 {
		t.Errorf("ListOpenRounds = %d; want 2", len(open))
	}
}

func TestBadgerStore_ConcurrentWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	n := 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			r := makeStorableRound("task-concurrent", event.EventID("evt-concurrent-"+string(rune('A'+i))))
			_ = s.SaveRound(ctx, r)
		}(i)
	}
	wg.Wait()

	// All rounds should be loadable.
	rounds, err := s.LoadRoundsByTaskID(ctx, "task-concurrent")
	if err != nil {
		t.Fatalf("LoadRoundsByTaskID: %v", err)
	}
	if len(rounds) != n {
		t.Errorf("got %d rounds; want %d", len(rounds), n)
	}
}

func TestBadgerStore_DuplicateRoundID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := makeStorableRound("task-dup", "evt-dup")
	r.PassWeight = 0

	if err := s.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound (first): %v", err)
	}

	// Update and re-save.
	r.PassWeight = 999
	if err := s.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound (update): %v", err)
	}

	loaded, err := s.LoadRound(ctx, r.RoundID)
	if err != nil {
		t.Fatalf("LoadRound after update: %v", err)
	}
	if loaded.PassWeight != 999 {
		t.Errorf("PassWeight = %d; want 999 (update should have persisted)", loaded.PassWeight)
	}
}
