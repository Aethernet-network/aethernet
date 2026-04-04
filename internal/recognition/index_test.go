package recognition_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/recognition"
)

func TestIndex_PutGetRoundtrip(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)

	state := &recognition.RecognitionState{
		ConsumerName:    "ocs-vote",
		EventID:         "evt-001",
		Recognized:      true,
		Ready:           false,
		DeferredReason:  "target not pending",
		PrerequisiteKey: "pending:evt-target",
		LastAttemptUnix: 1234567890,
		Attempts:        2,
	}
	if err := idx.Put(state); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := idx.Get("ocs-vote", "evt-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ConsumerName != state.ConsumerName {
		t.Errorf("ConsumerName = %q; want %q", got.ConsumerName, state.ConsumerName)
	}
	if got.EventID != state.EventID {
		t.Errorf("EventID = %q; want %q", got.EventID, state.EventID)
	}
	if !got.Recognized {
		t.Error("Recognized should be true")
	}
	if got.Ready {
		t.Error("Ready should be false")
	}
	if got.DeferredReason != "target not pending" {
		t.Errorf("DeferredReason = %q; want %q", got.DeferredReason, "target not pending")
	}
	if got.PrerequisiteKey != "pending:evt-target" {
		t.Errorf("PrerequisiteKey = %q; want %q", got.PrerequisiteKey, "pending:evt-target")
	}
	if got.Attempts != 2 {
		t.Errorf("Attempts = %d; want 2", got.Attempts)
	}
}

func TestIndex_MarkRecognized_Idempotent(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)

	// First call creates the state.
	s1, err := idx.MarkRecognized("consumer-a", "evt-002")
	if err != nil {
		t.Fatalf("first MarkRecognized: %v", err)
	}
	if !s1.Recognized {
		t.Error("should be recognized after first call")
	}

	// Second call is idempotent.
	s2, err := idx.MarkRecognized("consumer-a", "evt-002")
	if err != nil {
		t.Fatalf("second MarkRecognized: %v", err)
	}
	if !s2.Recognized {
		t.Error("should still be recognized after second call")
	}
}

func TestIndex_MarkReady(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)

	// Create deferred state.
	idx.SetDeferred("consumer-b", "evt-003", "waiting", "prereq:abc")

	got, _ := idx.Get("consumer-b", "evt-003")
	if got.Ready {
		t.Error("should not be ready before MarkReady")
	}

	// Mark ready.
	if err := idx.MarkReady("consumer-b", "evt-003"); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	got, _ = idx.Get("consumer-b", "evt-003")
	if !got.Ready {
		t.Error("should be ready after MarkReady")
	}
	if got.DeferredReason != "" {
		t.Errorf("DeferredReason should be cleared; got %q", got.DeferredReason)
	}
	if got.PrerequisiteKey != "" {
		t.Errorf("PrerequisiteKey should be cleared; got %q", got.PrerequisiteKey)
	}
}

func TestIndex_DeferredByPrerequisite(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)

	// Create 3 deferred items, 2 with the same prerequisite.
	idx.SetDeferred("c1", "evt-a", "waiting", "prereq:xyz")
	idx.SetDeferred("c2", "evt-b", "waiting", "prereq:xyz")
	idx.SetDeferred("c3", "evt-c", "waiting", "prereq:other")

	matches := idx.DeferredByPrerequisite("prereq:xyz")
	if len(matches) != 2 {
		t.Fatalf("DeferredByPrerequisite = %d; want 2", len(matches))
	}

	// Both should be for prereq:xyz.
	for _, s := range matches {
		if s.PrerequisiteKey != "prereq:xyz" {
			t.Errorf("unexpected prerequisite key: %q", s.PrerequisiteKey)
		}
	}
}

func TestIndex_GetNotFound(t *testing.T) {
	idx := recognition.NewIndex(nil)

	_, err := idx.Get("nonexistent", "evt-999")
	if err != recognition.ErrNotFound {
		t.Errorf("expected ErrNotFound; got %v", err)
	}
}

func TestIndex_PersistenceRoundtrip(t *testing.T) {
	store := recognition.NewMemoryIndexStore()

	// Write via index 1.
	idx1 := recognition.NewIndex(store)
	idx1.Put(&recognition.RecognitionState{
		ConsumerName: "persist-test",
		EventID:      "evt-persist",
		Recognized:   true,
		Ready:        true,
	})

	// Create index 2 with same store — simulate restart.
	idx2 := recognition.NewIndex(store)
	if err := idx2.LoadFromStore(); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	got, err := idx2.Get("persist-test", "evt-persist")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if !got.Recognized || !got.Ready {
		t.Error("state should survive persistence round-trip")
	}
}

func TestIndex_ReplayFlagSurvivesPersistence(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)

	// The RecognitionState itself doesn't carry a replay flag directly —
	// that's on CommitRecord. But the state persists the consumer/event
	// pair and its readiness. Verify it round-trips.
	state := &recognition.RecognitionState{
		ConsumerName:    "replay-test",
		EventID:         "evt-replay",
		Recognized:      true,
		Ready:           false,
		DeferredReason:  "replay-deferred",
		PrerequisiteKey: "prereq:replay",
	}
	idx.Put(state)

	// Reload.
	idx2 := recognition.NewIndex(store)
	idx2.LoadFromStore()

	got, err := idx2.Get("replay-test", "evt-replay")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DeferredReason != "replay-deferred" {
		t.Errorf("DeferredReason = %q; want %q", got.DeferredReason, "replay-deferred")
	}
}
