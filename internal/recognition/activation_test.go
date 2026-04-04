package recognition_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

func TestActivation_SignalWakesDeferredConsumers(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	var consumed atomic.Int64
	readyFlag := atomic.Bool{}

	c := &testConsumer{
		name: "activatable",
		ready: func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
			if readyFlag.Load() {
				return true, "", nil
			}
			return false, "prereq:target-pending", nil
		},
	}
	c.consumed = consumed // share the counter

	bus.Register(c)
	bus.Start()
	defer bus.Stop()

	// Emit event — consumer will defer.
	ev := &event.Event{ID: "evt-act", Type: event.EventTypeVerificationVote}
	rm.events[ev.ID] = ev

	bus.Emit(recognition.CommitRecord{
		EventID:   ev.ID,
		EventType: ev.Type,
		Source:    recognition.SourceRemoteFastPath,
	}, ev)

	// Wait for the bus worker to process and create the deferred state.
	deadline := time.Now().Add(2 * time.Second)
	var state *recognition.RecognitionState
	for time.Now().Before(deadline) {
		s, err := idx.Get("activatable", ev.ID)
		if err == nil && s.Recognized {
			state = s
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state == nil {
		t.Fatal("bus worker did not create deferred state in time")
	}
	if state.Ready {
		t.Fatal("should be deferred, not ready")
	}

	// Now satisfy the prerequisite and signal.
	readyFlag.Store(true)

	activator := recognition.NewActivator(idx, bus)
	activator.Signal(context.Background(), "prereq:target-pending")

	time.Sleep(100 * time.Millisecond)

	// Verify activated.
	state, err := idx.Get("activatable", ev.ID)
	if err != nil {
		t.Fatalf("Get after activation: %v", err)
	}
	if !state.Ready {
		t.Error("should be ready after activation signal")
	}
}

func TestActivation_SignalNoMatchIsNoop(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()
	bus := recognition.NewBus(recognition.DefaultBusConfig(), idx, rm)

	activator := recognition.NewActivator(idx, bus)

	// Signal with no matching deferred items — should not panic.
	activator.Signal(context.Background(), "prereq:nonexistent")
}

func TestActivation_DeferredDataStructurePersists(t *testing.T) {
	store := recognition.NewMemoryIndexStore()

	// Write deferred state via index 1.
	idx1 := recognition.NewIndex(store)
	idx1.SetDeferred("act-persist", "evt-defer-persist", "waiting for target", "prereq:target-x")

	// Simulate restart — new index from same store.
	idx2 := recognition.NewIndex(store)
	idx2.LoadFromStore()

	// Verify deferred state survived.
	deferred := idx2.DeferredByPrerequisite("prereq:target-x")
	if len(deferred) != 1 {
		t.Fatalf("DeferredByPrerequisite = %d; want 1", len(deferred))
	}
	if deferred[0].ConsumerName != "act-persist" {
		t.Errorf("ConsumerName = %q; want %q", deferred[0].ConsumerName, "act-persist")
	}
	if deferred[0].EventID != "evt-defer-persist" {
		t.Errorf("EventID = %q; want %q", deferred[0].EventID, "evt-defer-persist")
	}
}
