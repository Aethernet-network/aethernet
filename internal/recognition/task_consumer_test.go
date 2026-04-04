package recognition_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// mockTaskApplier records ApplyDAGEvent calls.
type mockTaskApplier struct {
	mu     sync.Mutex
	events []*event.Event
}

func (m *mockTaskApplier) ApplyDAGEvent(ev *event.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
}

func (m *mockTaskApplier) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func (m *mockTaskApplier) lastType() event.EventType {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return ""
	}
	return m.events[len(m.events)-1].Type
}

func makeTaskEvent(id string, typ event.EventType) *event.Event {
	return &event.Event{
		ID:   event.EventID(id),
		Type: typ,
	}
}

// TestTaskConsumer_LocalTaskPostedRecognized verifies that a locally posted
// task is recognized through the fabric.
func TestTaskConsumer_LocalTaskPostedRecognized(t *testing.T) {
	applier := &mockTaskApplier{}
	consumer := recognition.NewTaskLifecycleConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeTaskEvent("task-posted-local", event.EventTypeTaskPosted)
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.callCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if applier.callCount() != 1 {
		t.Fatalf("ApplyDAGEvent calls = %d; want 1", applier.callCount())
	}
	if applier.lastType() != event.EventTypeTaskPosted {
		t.Errorf("last type = %q; want TaskPosted", applier.lastType())
	}
}

// TestTaskConsumer_RemoteTaskPostedRecognized verifies that a remote task
// posted event is recognized.
func TestTaskConsumer_RemoteTaskPostedRecognized(t *testing.T) {
	applier := &mockTaskApplier{}
	consumer := recognition.NewTaskLifecycleConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeTaskEvent("task-posted-remote", event.EventTypeTaskPosted)
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceRemoteFastPath, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.callCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if applier.callCount() != 1 {
		t.Fatalf("ApplyDAGEvent calls = %d; want 1", applier.callCount())
	}
}

// TestTaskConsumer_ReplayRestoresState verifies that replayed task events
// are recognized consistently.
func TestTaskConsumer_ReplayRestoresState(t *testing.T) {
	applier := &mockTaskApplier{}
	consumer := recognition.NewTaskLifecycleConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	// Replay 3 task lifecycle events.
	types := []event.EventType{
		event.EventTypeTaskPosted,
		event.EventTypeTaskClaimed,
		event.EventTypeTaskSubmitted,
	}
	for i, typ := range types {
		ev := makeTaskEvent("task-replay-"+string(typ), typ)
		rm.events[ev.ID] = ev
		recognition.EmitCommit(bus, ev, recognition.SourceReplay, true)
		_ = i
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.callCount() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if applier.callCount() != 3 {
		t.Fatalf("ApplyDAGEvent calls = %d; want 3", applier.callCount())
	}
}

// TestTaskConsumer_DuplicateRecognitionIdempotent verifies that duplicate
// recognition from fabric + existing syncHandler is safe.
func TestTaskConsumer_DuplicateRecognitionIdempotent(t *testing.T) {
	applier := &mockTaskApplier{}
	consumer := recognition.NewTaskLifecycleConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeTaskEvent("task-dup", event.EventTypeTaskPosted)
	rm.events[ev.ID] = ev

	// Emit twice — simulates syncHandler + fabric both seeing the event.
	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)
	recognition.EmitCommit(bus, ev, recognition.SourceRemoteLegacy, false)

	time.Sleep(200 * time.Millisecond)

	// Index idempotency: only one ApplyDAGEvent call.
	if applier.callCount() != 1 {
		t.Errorf("ApplyDAGEvent calls = %d; want 1 (idempotent)", applier.callCount())
	}
}

// TestTaskConsumer_AllFiveTypes verifies all task lifecycle types are handled.
func TestTaskConsumer_AllFiveTypes(t *testing.T) {
	applier := &mockTaskApplier{}
	consumer := recognition.NewTaskLifecycleConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	types := []event.EventType{
		event.EventTypeTaskPosted,
		event.EventTypeTaskClaimed,
		event.EventTypeTaskSubmitted,
		event.EventTypeTaskApproved,
		event.EventTypeTaskDisputed,
	}
	for _, typ := range types {
		ev := makeTaskEvent("task-all-"+string(typ), typ)
		rm.events[ev.ID] = ev
		recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.callCount() >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if applier.callCount() != 5 {
		t.Fatalf("ApplyDAGEvent calls = %d; want 5", applier.callCount())
	}
}

// TestTaskConsumer_IgnoresNonTaskTypes verifies the filter works.
func TestTaskConsumer_IgnoresNonTaskTypes(t *testing.T) {
	consumer := recognition.NewTaskLifecycleConsumer(&mockTaskApplier{})

	nonTaskTypes := []event.EventType{
		event.EventTypeTransfer,
		event.EventTypeGeneration,
		event.EventTypeVerificationVote,
		event.EventTypeSettlement,
		event.EventTypeRegistration,
	}
	for _, typ := range nonTaskTypes {
		ev := &event.Event{ID: event.EventID("evt-" + string(typ)), Type: typ}
		if consumer.Interested(ev) {
			t.Errorf("should not be interested in %q", typ)
		}
	}
}

// --- Evidence readiness consumer tests ---

type mockEvidenceMarker struct {
	mu      sync.Mutex
	marked  []string
}

func (m *mockEvidenceMarker) MarkEvidenceReady(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marked = append(m.marked, taskID)
}

func (m *mockEvidenceMarker) markCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.marked)
}

type mockBlobChecker struct {
	mu   sync.Mutex
	blobs map[string]bool
}

func newMockBlobChecker() *mockBlobChecker {
	return &mockBlobChecker{blobs: make(map[string]bool)}
}

func (m *mockBlobChecker) Has(_ context.Context, hash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blobs[hash], nil
}

func (m *mockBlobChecker) addBlob(hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[hash] = true
}

func makeTaskSubmittedEvent(id, taskID, evidenceHash string) *event.Event {
	payload := map[string]interface{}{
		"version":            1,
		"task_id":            taskID,
		"claimer_id":         "worker",
		"result_hash":        "sha256:abc",
		"evidence_body_hash": evidenceHash,
	}
	data, _ := json.Marshal(payload)
	return &event.Event{
		ID:              event.EventID(id),
		Type:            event.EventTypeTaskSubmitted,
		Payload:         data,
		SettlementState: event.SettlementOptimistic,
	}
}

// TestEvidenceConsumer_NoHashImmediatelyReady verifies that a TaskSubmitted
// without an evidence hash is immediately evidence-ready.
func TestEvidenceConsumer_NoHashImmediatelyReady(t *testing.T) {
	marker := &mockEvidenceMarker{}
	consumer := recognition.NewEvidenceReadinessConsumer(marker, nil)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeTaskSubmittedEvent("evt-no-hash", "task-1", "")
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if marker.markCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if marker.markCount() != 1 {
		t.Fatalf("MarkEvidenceReady calls = %d; want 1", marker.markCount())
	}
}

// TestEvidenceConsumer_BlobMissingDeferred verifies that a TaskSubmitted with
// a blob hash defers when the blob is not yet available.
func TestEvidenceConsumer_BlobMissingDeferred(t *testing.T) {
	marker := &mockEvidenceMarker{}
	checker := newMockBlobChecker()
	consumer := recognition.NewEvidenceReadinessConsumer(marker, checker)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeTaskSubmittedEvent("evt-blob-missing", "task-2", "hash123")
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceRemoteFastPath, false)

	time.Sleep(200 * time.Millisecond)

	if marker.markCount() != 0 {
		t.Errorf("MarkEvidenceReady should not be called when blob is missing")
	}

	state, err := idx.Get("evidence_readiness", ev.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if state.Ready {
		t.Error("should be deferred, not ready")
	}
	expectedKey := recognition.PrerequisiteKeyEvidenceBlob("hash123")
	if state.PrerequisiteKey != expectedKey {
		t.Errorf("PrerequisiteKey = %q; want %q", state.PrerequisiteKey, expectedKey)
	}
}

// TestEvidenceConsumer_BlobArrivalActivatesReadiness verifies that when the
// blob becomes available, the evidence readiness consumer is activated.
func TestEvidenceConsumer_BlobArrivalActivatesReadiness(t *testing.T) {
	marker := &mockEvidenceMarker{}
	checker := newMockBlobChecker()
	consumer := recognition.NewEvidenceReadinessConsumer(marker, checker)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeTaskSubmittedEvent("evt-blob-arrives", "task-3", "hash456")
	rm.events[ev.ID] = ev

	// Step 1: Emit — blob not available, deferred.
	recognition.EmitCommit(bus, ev, recognition.SourceRemoteFastPath, false)

	// Wait for deferral.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := idx.Get("evidence_readiness", ev.ID)
		if s != nil && s.Recognized && !s.Ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if marker.markCount() != 0 {
		t.Fatal("should be deferred before blob arrives")
	}

	// Step 2: Blob arrives — signal activation.
	checker.addBlob("hash456")
	activator := recognition.NewActivator(idx, bus)
	activator.Signal(context.Background(), recognition.PrerequisiteKeyEvidenceBlob("hash456"))

	time.Sleep(200 * time.Millisecond)

	if marker.markCount() != 1 {
		t.Fatalf("MarkEvidenceReady calls = %d; want 1 after blob arrival", marker.markCount())
	}

	state, _ := idx.Get("evidence_readiness", ev.ID)
	if !state.Ready {
		t.Error("should be ready after activation")
	}
}
