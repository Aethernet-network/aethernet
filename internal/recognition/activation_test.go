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

func TestActivation_TargetedNotGlobalScan(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()
	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	// Register two consumers with different prereq keys.
	readyA := atomic.Bool{}
	consumerA := &testConsumer{
		name: "consumer-a",
		ready: func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
			if readyA.Load() {
				return true, "", nil
			}
			return false, "prereq:key-a", nil
		},
	}
	readyB := atomic.Bool{}
	consumerB := &testConsumer{
		name: "consumer-b",
		ready: func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
			if readyB.Load() {
				return true, "", nil
			}
			return false, "prereq:key-b", nil
		},
	}

	bus.Register(consumerA)
	bus.Register(consumerB)
	bus.Start()
	defer bus.Stop()

	evA := &event.Event{ID: "evt-targeted-a", Type: event.EventTypeTransfer}
	evB := &event.Event{ID: "evt-targeted-b", Type: event.EventTypeTransfer}
	rm.events[evA.ID] = evA
	rm.events[evB.ID] = evB

	// Emit both — both deferred on different keys.
	recognition.EmitCommit(bus, evA, recognition.SourceLocal, false)
	recognition.EmitCommit(bus, evB, recognition.SourceLocal, false)

	// Wait for deferral.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sA, _ := idx.Get("consumer-a", evA.ID)
		sB, _ := idx.Get("consumer-b", evB.ID)
		if sA != nil && sA.Recognized && sB != nil && sB.Recognized {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Signal only key-a — only consumer-a should activate.
	readyA.Store(true)
	activator := recognition.NewActivator(idx, bus)
	activator.Signal(context.Background(), "prereq:key-a")

	time.Sleep(100 * time.Millisecond)

	stateA, _ := idx.Get("consumer-a", evA.ID)
	stateB, _ := idx.Get("consumer-b", evB.ID)

	if stateA == nil || !stateA.Ready {
		t.Error("consumer-a should be activated (prereq:key-a signaled)")
	}
	if stateB != nil && stateB.Ready {
		t.Error("consumer-b should NOT be activated (prereq:key-b not signaled)")
	}
}

func TestActivation_RepeatedSignalIdempotent(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()
	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	var consumed atomic.Int64
	readyFlag := atomic.Bool{}
	c := &testConsumer{
		name: "idempotent-signal",
		ready: func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
			if readyFlag.Load() {
				return true, "", nil
			}
			return false, "prereq:idem", nil
		},
	}
	c.consumed = consumed

	bus.Register(c)
	bus.Start()
	defer bus.Stop()

	ev := &event.Event{ID: "evt-idem", Type: event.EventTypeTransfer}
	rm.events[ev.ID] = ev
	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)

	// Wait for deferral.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := idx.Get("idempotent-signal", ev.ID)
		if s != nil && s.Recognized && !s.Ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	readyFlag.Store(true)
	activator := recognition.NewActivator(idx, bus)

	// Signal multiple times — should only consume once.
	activator.Signal(context.Background(), "prereq:idem")
	activator.Signal(context.Background(), "prereq:idem")
	activator.Signal(context.Background(), "prereq:idem")

	time.Sleep(100 * time.Millisecond)

	// Item should be consumed exactly once (marked ready after first signal).
	state, _ := idx.Get("idempotent-signal", ev.ID)
	if state == nil || !state.Ready {
		t.Fatal("should be ready after activation")
	}
	// Second and third signals find no deferred items (already ready).
}

func TestActivation_FailedRetryRemainsDeferred(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()
	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	// Consumer that is never ready.
	c := &testConsumer{
		name: "never-ready",
		ready: func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
			return false, "prereq:stuck", nil
		},
	}

	bus.Register(c)
	bus.Start()
	defer bus.Stop()

	ev := &event.Event{ID: "evt-stuck", Type: event.EventTypeTransfer}
	rm.events[ev.ID] = ev
	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)

	// Wait for deferral.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := idx.Get("never-ready", ev.ID)
		if s != nil && s.Recognized {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	activator := recognition.NewActivator(idx, bus)

	// Signal — retry fails, item remains deferred.
	activator.Signal(context.Background(), "prereq:stuck")

	state, _ := idx.Get("never-ready", ev.ID)
	if state == nil {
		t.Fatal("state should exist")
	}
	if state.Ready {
		t.Error("should NOT be ready (consumer never ready)")
	}
	if state.Attempts < 1 {
		t.Error("attempts should be incremented")
	}
}

func TestActivation_MaxAttemptsAbandonsItem(t *testing.T) {
	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()
	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	// Consumer never ready.
	c := &testConsumer{
		name: "max-attempts",
		ready: func(ctx context.Context, ev *event.Event, view recognition.ReadModel) (bool, string, error) {
			return false, "prereq:max", nil
		},
	}
	bus.Register(c)

	ev := &event.Event{ID: "evt-max-att", Type: event.EventTypeTransfer}
	rm.events[ev.ID] = ev

	// Manually set deferred state with attempts at max - 1.
	idx.SetDeferred("max-attempts", ev.ID, "waiting", "prereq:max")
	existing, _ := idx.Get("max-attempts", ev.ID)
	// Set attempts to MaxActivationAttempts via repeated IncrementAttempt.
	for i := 0; i < recognition.MaxActivationAttempts; i++ {
		idx.IncrementAttempt("max-attempts", ev.ID)
	}
	_ = existing

	activator := recognition.NewActivator(idx, bus)
	activator.Signal(context.Background(), "prereq:max")

	state, _ := idx.Get("max-attempts", ev.ID)
	if state == nil {
		t.Fatal("state should exist")
	}
	if !state.Ready {
		t.Error("should be marked ready (abandoned after max attempts)")
	}
}
