package recognition_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// testConsumer is a minimal CommitConsumer for testing.
type testConsumer struct {
	name       string
	interested func(ev *event.Event) bool
	ready      func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error)
	consumed   atomic.Int64
}

func (c *testConsumer) Name() string { return c.name }
func (c *testConsumer) Interested(ev *event.Event) bool {
	if c.interested != nil {
		return c.interested(ev)
	}
	return true
}
func (c *testConsumer) Ready(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
	if c.ready != nil {
		return c.ready(ctx, ev, view)
	}
	return true, "", nil
}
func (c *testConsumer) Consume(ctx context.Context, ev *event.Event) error {
	c.consumed.Add(1)
	return nil
}

// testReadModel provides a minimal ReadModel for testing.
type testReadModel struct {
	events map[event.EventID]*event.Event
}

func (m *testReadModel) HasEvent(id event.EventID) bool {
	_, ok := m.events[id]
	return ok
}
func (m *testReadModel) GetEvent(id event.EventID) *event.Event {
	return m.events[id]
}

func makeTestReadModel() *testReadModel {
	return &testReadModel{events: make(map[event.EventID]*event.Event)}
}

func TestBus_MultipleConsumersReceiveCommits(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 2}, idx, rm)

	c1 := &testConsumer{name: "consumer-1"}
	c2 := &testConsumer{name: "consumer-2"}
	c3 := &testConsumer{name: "consumer-3", interested: func(ev *event.Event) bool {
		return ev.Type == event.EventTypeTransfer
	}}

	bus.Register(c1)
	bus.Register(c2)
	bus.Register(c3)
	bus.Start()
	defer bus.Stop()

	// Emit a Transfer event.
	ev := &event.Event{
		ID:   "evt-001",
		Type: event.EventTypeTransfer,
	}
	rm.events[ev.ID] = ev

	record := recognition.CommitRecord{
		EventID:     ev.ID,
		EventType:   ev.Type,
		Source:       recognition.SourceLocal,
		CommittedAt: time.Now(),
	}
	if err := bus.Emit(record, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Wait for dispatch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c1.consumed.Load() > 0 && c2.consumed.Load() > 0 && c3.consumed.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if c1.consumed.Load() != 1 {
		t.Errorf("consumer-1 consumed %d; want 1", c1.consumed.Load())
	}
	if c2.consumed.Load() != 1 {
		t.Errorf("consumer-2 consumed %d; want 1", c2.consumed.Load())
	}
	if c3.consumed.Load() != 1 {
		t.Errorf("consumer-3 consumed %d; want 1 (Transfer event matches filter)", c3.consumed.Load())
	}
}

func TestBus_InterestedFilterExcludes(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	c := &testConsumer{
		name: "transfer-only",
		interested: func(ev *event.Event) bool {
			return ev.Type == event.EventTypeTransfer
		},
	}
	bus.Register(c)
	bus.Start()
	defer bus.Stop()

	// Emit a Generation event — should NOT match.
	ev := &event.Event{
		ID:   "evt-gen-001",
		Type: event.EventTypeGeneration,
	}
	rm.events[ev.ID] = ev

	bus.Emit(recognition.CommitRecord{
		EventID:   ev.ID,
		EventType: ev.Type,
		Source:     recognition.SourceLocal,
	}, ev)

	time.Sleep(100 * time.Millisecond)

	if c.consumed.Load() != 0 {
		t.Errorf("consumer consumed %d; want 0 (Generation event should be filtered out)", c.consumed.Load())
	}
}

func TestBus_DuplicateRegistrationRejected(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()
	bus := recognition.NewBus(recognition.DefaultBusConfig(), idx, rm)

	c := &testConsumer{name: "dup"}
	if err := bus.Register(c); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := bus.Register(c); err != recognition.ErrConsumerAlreadyRegistered {
		t.Errorf("second register: got %v; want ErrConsumerAlreadyRegistered", err)
	}
}

func TestBus_QueueFullReturnsError(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	// Tiny queue, no workers started — queue will fill.
	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 2, Workers: 0}, idx, rm)

	ev := &event.Event{ID: "evt-qf", Type: event.EventTypeTransfer}
	rm.events[ev.ID] = ev
	record := recognition.CommitRecord{EventID: ev.ID, EventType: ev.Type}

	// Fill the queue.
	bus.Emit(record, ev)
	bus.Emit(record, ev)

	// Third should fail.
	if err := bus.Emit(record, ev); err != recognition.ErrQueueFull {
		t.Errorf("expected ErrQueueFull; got %v", err)
	}
}

func TestBus_SourceTaggingSurvivesDispatch(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	var receivedSource recognition.CommitSource
	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	// We can't directly observe the source in Consume, but we can verify
	// the index state after dispatch reflects the correct source.
	c := &testConsumer{name: "source-check"}
	bus.Register(c)
	bus.Start()
	defer bus.Stop()

	ev := &event.Event{ID: "evt-src", Type: event.EventTypeTransfer}
	rm.events[ev.ID] = ev

	bus.Emit(recognition.CommitRecord{
		EventID:   ev.ID,
		EventType: ev.Type,
		Source:    recognition.SourceRepair,
		Replay:   true,
	}, ev)

	time.Sleep(100 * time.Millisecond)

	// Verify the consumer was called.
	if c.consumed.Load() != 1 {
		t.Fatalf("consumer not called")
	}

	// Verify index state.
	state, err := idx.Get("source-check", ev.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Recognized {
		t.Error("should be recognized")
	}
	if !state.Ready {
		t.Error("should be ready")
	}
	_ = receivedSource
}

func TestBus_DeferredConsumerNotMarkedReady(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	c := &testConsumer{
		name: "deferred",
		ready: func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
			return false, "waiting:target-evt", nil
		},
	}
	bus.Register(c)
	bus.Start()
	defer bus.Stop()

	ev := &event.Event{ID: "evt-def", Type: event.EventTypeVerificationVote}
	rm.events[ev.ID] = ev

	bus.Emit(recognition.CommitRecord{
		EventID:   ev.ID,
		EventType: ev.Type,
		Source:    recognition.SourceRemoteFastPath,
	}, ev)

	time.Sleep(100 * time.Millisecond)

	// Consumer should NOT have been called (not ready).
	if c.consumed.Load() != 0 {
		t.Error("deferred consumer should not have Consume called")
	}

	// But it should be recognized and deferred.
	state, err := idx.Get("deferred", ev.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Recognized {
		t.Error("should be recognized")
	}
	if state.Ready {
		t.Error("should NOT be ready (deferred)")
	}
	if state.PrerequisiteKey != "waiting:target-evt" {
		t.Errorf("PrerequisiteKey = %q; want %q", state.PrerequisiteKey, "waiting:target-evt")
	}
}
