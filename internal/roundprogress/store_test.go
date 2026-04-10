package roundprogress

import (
	"os"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func TestMemoryStore_PutGet(t *testing.T) {
	s := NewMemorySnapshotStore()
	snap := &RoundProgressSnapshot{
		RoundID:     "round-1",
		ValidatorID: "val-1",
		AnalyzerFamily: "family-a",
		CurrentPhase:   ProgressPhaseAnalyzing,
		ProgressGeneration: 3,
	}
	if err := s.Put(snap); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("round-1", "val-1", "family-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.CurrentPhase != ProgressPhaseAnalyzing {
		t.Errorf("phase = %v, want Analyzing", got.CurrentPhase)
	}
	if got.ProgressGeneration != 3 {
		t.Errorf("generation = %d, want 3", got.ProgressGeneration)
	}
}

func TestMemoryStore_GetNonexistent(t *testing.T) {
	s := NewMemorySnapshotStore()
	got, err := s.Get("missing", "missing", "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent key, got %+v", got)
	}
}

func TestMemoryStore_GetAllForRound(t *testing.T) {
	s := NewMemorySnapshotStore()
	for _, fam := range []string{"a", "b", "c"} {
		_ = s.Put(&RoundProgressSnapshot{
			RoundID:        "round-1",
			ValidatorID:    "val-1",
			AnalyzerFamily: fam,
			CurrentPhase:   ProgressPhaseAnalyzing,
		})
	}
	_ = s.Put(&RoundProgressSnapshot{
		RoundID:        "round-2",
		ValidatorID:    "val-1",
		AnalyzerFamily: "a",
	})

	snaps, err := s.GetAllForRound("round-1")
	if err != nil {
		t.Fatalf("GetAllForRound: %v", err)
	}
	if len(snaps) != 3 {
		t.Errorf("got %d snapshots, want 3", len(snaps))
	}
}

func TestMemoryStore_PutReplaces(t *testing.T) {
	s := NewMemorySnapshotStore()
	_ = s.Put(&RoundProgressSnapshot{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f1",
		CurrentPhase: ProgressPhaseAcknowledged,
	})
	_ = s.Put(&RoundProgressSnapshot{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f1",
		CurrentPhase: ProgressPhaseAnalyzing,
	})
	got, _ := s.Get("r1", "v1", "f1")
	if got.CurrentPhase != ProgressPhaseAnalyzing {
		t.Errorf("phase = %v, want Analyzing (replaced)", got.CurrentPhase)
	}
}

func TestMemoryStore_DeleteRound(t *testing.T) {
	s := NewMemorySnapshotStore()
	_ = s.Put(&RoundProgressSnapshot{RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f1"})
	_ = s.Put(&RoundProgressSnapshot{RoundID: "r1", ValidatorID: "v2", AnalyzerFamily: "f1"})
	_ = s.Put(&RoundProgressSnapshot{RoundID: "r2", ValidatorID: "v1", AnalyzerFamily: "f1"})

	_ = s.DeleteRound("r1")

	snaps, _ := s.GetAllForRound("r1")
	if len(snaps) != 0 {
		t.Errorf("after DeleteRound: got %d snapshots, want 0", len(snaps))
	}
	// r2 should be untouched.
	snaps2, _ := s.GetAllForRound("r2")
	if len(snaps2) != 1 {
		t.Errorf("r2 should be untouched: got %d snapshots", len(snaps2))
	}
}

// ── BadgerDB Store Tests ────────────────────────────────────────────────────

func openTestBadger(t *testing.T) *badger.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "rp-badger-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	opts := badger.DefaultOptions(dir)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBadgerStore_PutGet(t *testing.T) {
	db := openTestBadger(t)
	s := NewBadgerSnapshotStore(db)

	snap := &RoundProgressSnapshot{
		RoundID:            "round-1",
		ValidatorID:        "val-1",
		AnalyzerFamily:     "family-a",
		CurrentPhase:       ProgressPhaseAnalyzing,
		ProgressGeneration: 5,
		ReasonCode:         ReasonCodeAnalyzerRunning,
	}
	if err := s.Put(snap); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("round-1", "val-1", "family-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.CurrentPhase != ProgressPhaseAnalyzing {
		t.Errorf("phase = %v, want Analyzing", got.CurrentPhase)
	}
	if got.ProgressGeneration != 5 {
		t.Errorf("generation = %d, want 5", got.ProgressGeneration)
	}
}

func TestBadgerStore_GetNonexistent(t *testing.T) {
	db := openTestBadger(t)
	s := NewBadgerSnapshotStore(db)
	got, err := s.Get("missing", "missing", "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent key")
	}
}

func TestBadgerStore_GetAllForRound(t *testing.T) {
	db := openTestBadger(t)
	s := NewBadgerSnapshotStore(db)

	for _, fam := range []string{"a", "b", "c"} {
		_ = s.Put(&RoundProgressSnapshot{
			RoundID: "round-1", ValidatorID: "val-1", AnalyzerFamily: fam,
			CurrentPhase: ProgressPhaseAnalyzing,
		})
	}
	_ = s.Put(&RoundProgressSnapshot{
		RoundID: "round-2", ValidatorID: "val-1", AnalyzerFamily: "a",
	})

	snaps, err := s.GetAllForRound("round-1")
	if err != nil {
		t.Fatalf("GetAllForRound: %v", err)
	}
	if len(snaps) != 3 {
		t.Errorf("got %d snapshots, want 3", len(snaps))
	}
}

func TestBadgerStore_PutReplaces(t *testing.T) {
	db := openTestBadger(t)
	s := NewBadgerSnapshotStore(db)

	_ = s.Put(&RoundProgressSnapshot{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f1",
		CurrentPhase: ProgressPhaseAcknowledged,
	})
	_ = s.Put(&RoundProgressSnapshot{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f1",
		CurrentPhase: ProgressPhaseAnalyzing,
	})
	got, _ := s.Get("r1", "v1", "f1")
	if got.CurrentPhase != ProgressPhaseAnalyzing {
		t.Errorf("phase = %v, want Analyzing", got.CurrentPhase)
	}
}

func TestBadgerStore_DeleteRound(t *testing.T) {
	db := openTestBadger(t)
	s := NewBadgerSnapshotStore(db)

	_ = s.Put(&RoundProgressSnapshot{RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f1"})
	_ = s.Put(&RoundProgressSnapshot{RoundID: "r1", ValidatorID: "v2", AnalyzerFamily: "f1"})
	_ = s.Put(&RoundProgressSnapshot{RoundID: "r2", ValidatorID: "v1", AnalyzerFamily: "f1"})

	_ = s.DeleteRound("r1")

	snaps, _ := s.GetAllForRound("r1")
	if len(snaps) != 0 {
		t.Errorf("after DeleteRound: got %d, want 0", len(snaps))
	}
	snaps2, _ := s.GetAllForRound("r2")
	if len(snaps2) != 1 {
		t.Errorf("r2 untouched: got %d, want 1", len(snaps2))
	}
}

func TestBadgerStore_SurvivesRestart(t *testing.T) {
	dir, err := os.MkdirTemp("", "rp-restart-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	// Write.
	opts := badger.DefaultOptions(dir)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := NewBadgerSnapshotStore(db)
	_ = s.Put(&RoundProgressSnapshot{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f1",
		CurrentPhase: ProgressPhaseAnalyzing, ProgressGeneration: 7,
	})
	db.Close()

	// Reopen.
	db2, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	s2 := NewBadgerSnapshotStore(db2)

	got, err := s2.Get("r1", "v1", "f1")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got == nil {
		t.Fatal("data lost after restart")
	}
	if got.ProgressGeneration != 7 {
		t.Errorf("generation = %d, want 7", got.ProgressGeneration)
	}
}
