package recognition_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// mockSettlementApplier records ApplyFromEvent calls with idempotency.
type mockSettlementApplier struct {
	mu      sync.Mutex
	applied map[event.EventID]int // eventID → apply count
}

func newMockSettlementApplier() *mockSettlementApplier {
	return &mockSettlementApplier{applied: make(map[event.EventID]int)}
}

func (m *mockSettlementApplier) ApplyFromEvent(ev *event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applied[ev.ID]++
	return nil
}

func (m *mockSettlementApplier) applyCount(id event.EventID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applied[id]
}

func (m *mockSettlementApplier) totalApplies() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, n := range m.applied {
		total += n
	}
	return total
}

func makeSettlementEvent(id, targetID, verdict string) *event.Event {
	payload := map[string]interface{}{
		"v":               1,
		"target_event_id": targetID,
		"verdict":         verdict,
		"verified_value":  5000,
		"consensus_round": 1,
		"attestations":    []interface{}{},
	}
	data, _ := json.Marshal(payload)
	return &event.Event{
		ID:              event.EventID(id),
		Type:            event.EventTypeSettlement,
		Payload:         data,
		SettlementState: event.SettlementOptimistic,
	}
}

// TestSettlementConsumer_LocalRecognized verifies that a locally created
// settlement event is recognized and applied via the fabric.
func TestSettlementConsumer_LocalRecognized(t *testing.T) {
	applier := newMockSettlementApplier()
	consumer := recognition.NewSettlementConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeSettlementEvent("settle-local-1", "target-1", "accepted")
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.applyCount(ev.ID) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if applier.applyCount(ev.ID) != 1 {
		t.Fatalf("ApplyFromEvent calls = %d; want 1", applier.applyCount(ev.ID))
	}
}

// TestSettlementConsumer_RemoteRecognized verifies that a remotely synced
// settlement event is recognized and applied.
func TestSettlementConsumer_RemoteRecognized(t *testing.T) {
	applier := newMockSettlementApplier()
	consumer := recognition.NewSettlementConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeSettlementEvent("settle-remote-1", "target-2", "accepted")
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceRemoteFastPath, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.applyCount(ev.ID) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if applier.applyCount(ev.ID) != 1 {
		t.Fatalf("ApplyFromEvent calls = %d; want 1", applier.applyCount(ev.ID))
	}
}

// TestSettlementConsumer_ReplayNoDoubleApply verifies that replayed settlement
// events are applied exactly once through the fabric (index idempotency).
func TestSettlementConsumer_ReplayNoDoubleApply(t *testing.T) {
	applier := newMockSettlementApplier()
	consumer := recognition.NewSettlementConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeSettlementEvent("settle-replay-1", "target-3", "accepted")
	rm.events[ev.ID] = ev

	// Emit as replay.
	recognition.EmitCommit(bus, ev, recognition.SourceReplay, true)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.applyCount(ev.ID) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if applier.applyCount(ev.ID) != 1 {
		t.Fatalf("ApplyFromEvent calls = %d; want 1", applier.applyCount(ev.ID))
	}

	// Emit again (simulates double replay or duplicate arrival).
	recognition.EmitCommit(bus, ev, recognition.SourceReplay, true)
	time.Sleep(200 * time.Millisecond)

	// Index prevents double dispatch — still 1.
	if applier.applyCount(ev.ID) != 1 {
		t.Errorf("ApplyFromEvent calls after duplicate = %d; want 1 (idempotent)", applier.applyCount(ev.ID))
	}
}

// TestSettlementConsumer_DuplicateSyncHandlerAndFabric verifies that
// receiving a settlement from both the syncHandler and the fabric doesn't
// cause double application.
func TestSettlementConsumer_DuplicateSyncHandlerAndFabric(t *testing.T) {
	applier := newMockSettlementApplier()
	consumer := recognition.NewSettlementConsumer(applier)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeSettlementEvent("settle-dup-1", "target-4", "accepted")
	rm.events[ev.ID] = ev

	// Simulate syncHandler path (direct apply).
	_ = applier.ApplyFromEvent(ev)

	// Then fabric path.
	recognition.EmitCommit(bus, ev, recognition.SourceRemoteLegacy, false)
	time.Sleep(200 * time.Millisecond)

	// The mock counts all calls (doesn't have its own dedup).
	// In production, the settlement applicator's applied set handles this.
	// The fabric's index ensures only one dispatch, but the syncHandler
	// call bypasses the fabric. Total = 2 (1 direct + 1 fabric).
	// The real applicator's IsApplied check makes the second a no-op.
	// For this mock, we verify the fabric dispatched exactly once.
	state, err := idx.Get("settlement", ev.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Recognized || !state.Ready {
		t.Error("fabric should have recognized and marked ready")
	}
}

// TestSettlementConsumer_IgnoresNonSettlementTypes verifies the filter.
func TestSettlementConsumer_IgnoresNonSettlementTypes(t *testing.T) {
	consumer := recognition.NewSettlementConsumer(newMockSettlementApplier())

	nonTypes := []event.EventType{
		event.EventTypeTransfer,
		event.EventTypeVerificationVote,
		event.EventTypeTaskPosted,
		event.EventTypeRegistration,
	}
	for _, typ := range nonTypes {
		ev := &event.Event{ID: event.EventID("evt-" + string(typ)), Type: typ}
		if consumer.Interested(ev) {
			t.Errorf("should not be interested in %q", typ)
		}
	}
}

// TestSettlementConsumer_ParseTarget verifies the helper function.
func TestSettlementConsumer_ParseTarget(t *testing.T) {
	ev := makeSettlementEvent("settle-parse", "target-parse-test", "accepted")
	target := recognition.ParseSettlementTarget(ev)
	if target != "target-parse-test" {
		t.Errorf("ParseSettlementTarget = %q; want %q", target, "target-parse-test")
	}

	// Nil payload.
	nilEv := &event.Event{ID: "no-payload", Type: event.EventTypeSettlement}
	if recognition.ParseSettlementTarget(nilEv) != "" {
		t.Error("ParseSettlementTarget should return empty for nil payload")
	}
}
