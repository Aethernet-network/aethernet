package recognition_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// mockOCSSubmitter records calls to SubmitFromSync for testing.
type mockOCSSubmitter struct {
	mu      sync.Mutex
	calls   []event.EventID
	errOnce error // return this error once, then nil
}

func (m *mockOCSSubmitter) SubmitFromSync(ev *event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, ev.ID)
	if m.errOnce != nil {
		err := m.errOnce
		m.errOnce = nil
		return err
	}
	return nil
}

func (m *mockOCSSubmitter) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockOCSSubmitter) lastID() event.EventID {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return ""
	}
	return m.calls[len(m.calls)-1]
}

func makeOptimisticTransfer(id string) *event.Event {
	return &event.Event{
		ID:              event.EventID(id),
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementOptimistic,
	}
}

func makeOptimisticGeneration(id string) *event.Event {
	return &event.Event{
		ID:              event.EventID(id),
		Type:            event.EventTypeGeneration,
		SettlementState: event.SettlementOptimistic,
	}
}

func makeOptimisticTaskSettlement(id string) *event.Event {
	return &event.Event{
		ID:              event.EventID(id),
		Type:            event.EventTypeTaskSettlement,
		SettlementState: event.SettlementOptimistic,
	}
}

// TestOCSSubmitConsumer_LocalTransferBecomesPending verifies that a locally
// committed Transfer event reaches the OCS pending queue via the fabric.
func TestOCSSubmitConsumer_LocalTransferBecomesPending(t *testing.T) {
	mock := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(mock)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeOptimisticTransfer("evt-local-transfer")
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mock.callCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if mock.callCount() != 1 {
		t.Fatalf("SubmitFromSync calls = %d; want 1", mock.callCount())
	}
	if mock.lastID() != "evt-local-transfer" {
		t.Errorf("last ID = %q; want %q", mock.lastID(), "evt-local-transfer")
	}
}

// TestOCSSubmitConsumer_RemoteTransferBecomesPending verifies that a remotely
// committed Transfer event reaches the OCS pending queue via the fabric.
func TestOCSSubmitConsumer_RemoteTransferBecomesPending(t *testing.T) {
	mock := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(mock)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeOptimisticTransfer("evt-remote-transfer")
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceRemoteFastPath, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mock.callCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if mock.callCount() != 1 {
		t.Fatalf("SubmitFromSync calls = %d; want 1", mock.callCount())
	}
}

// TestOCSSubmitConsumer_ReplayedTransferBecomesPending verifies that a
// replayed optimistic Transfer event reaches the OCS pending queue.
func TestOCSSubmitConsumer_ReplayedTransferBecomesPending(t *testing.T) {
	mock := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(mock)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeOptimisticTransfer("evt-replay-transfer")
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceReplay, true)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mock.callCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if mock.callCount() != 1 {
		t.Fatalf("SubmitFromSync calls = %d; want 1", mock.callCount())
	}
}

// TestOCSSubmitConsumer_DuplicateRecognitionIdempotent verifies that
// receiving the same event twice (e.g., from both syncHandler and fabric)
// results in only one SubmitFromSync call via the fabric, and the index
// prevents re-processing.
func TestOCSSubmitConsumer_DuplicateRecognitionIdempotent(t *testing.T) {
	mock := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(mock)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	ev := makeOptimisticTransfer("evt-dup-transfer")
	rm.events[ev.ID] = ev

	// Emit twice — simulate syncHandler + fabric both seeing the event.
	recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)
	recognition.EmitCommit(bus, ev, recognition.SourceRemoteLegacy, false)

	time.Sleep(200 * time.Millisecond)

	// The recognition index ensures idempotency — second dispatch is a no-op.
	if mock.callCount() != 1 {
		t.Errorf("SubmitFromSync calls = %d; want 1 (idempotent)", mock.callCount())
	}

	// Index shows recognized + ready.
	state, err := idx.Get("ocs_submit", "evt-dup-transfer")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Recognized || !state.Ready {
		t.Error("state should be recognized and ready")
	}
}

// TestOCSSubmitConsumer_SettledEventNotRepended verifies that an already-
// settled event is recognized but not re-pended in the OCS.
func TestOCSSubmitConsumer_SettledEventNotRepended(t *testing.T) {
	mock := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(mock)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	// Event is already settled — not optimistic.
	ev := &event.Event{
		ID:              "evt-settled-transfer",
		Type:            event.EventTypeTransfer,
		SettlementState: event.SettlementSettled,
	}
	rm.events[ev.ID] = ev

	recognition.EmitCommit(bus, ev, recognition.SourceReplay, true)

	time.Sleep(200 * time.Millisecond)

	// SubmitFromSync should NOT be called for settled events.
	if mock.callCount() != 0 {
		t.Errorf("SubmitFromSync calls = %d; want 0 (settled event should not be re-pended)", mock.callCount())
	}

	// But the event should still be recognized and ready (consumed without action).
	state, err := idx.Get("ocs_submit", "evt-settled-transfer")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Recognized {
		t.Error("settled event should still be recognized")
	}
	if !state.Ready {
		t.Error("settled event should be marked ready (no-op consume)")
	}
}

// TestOCSSubmitConsumer_IgnoresNonOCSTypes verifies that the consumer does
// not react to non-economic event types.
func TestOCSSubmitConsumer_IgnoresNonOCSTypes(t *testing.T) {
	mock := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(mock)

	// Directly test the Interested filter.
	nonOCSTypes := []event.EventType{
		event.EventTypeVerificationVote,
		event.EventTypeSettlement,
		event.EventTypeRegistration,
		event.EventTypeTaskPosted,
		event.EventTypeTaskClaimed,
		event.EventTypeTaskSubmitted,
	}

	for _, typ := range nonOCSTypes {
		ev := &event.Event{ID: event.EventID("evt-" + string(typ)), Type: typ}
		if consumer.Interested(ev) {
			t.Errorf("consumer should not be interested in %q", typ)
		}
	}

	// OCS types should match.
	ocsTypes := []event.EventType{
		event.EventTypeTransfer,
		event.EventTypeGeneration,
		event.EventTypeTaskSettlement,
	}
	for _, typ := range ocsTypes {
		ev := &event.Event{ID: event.EventID("evt-" + string(typ)), Type: typ}
		if !consumer.Interested(ev) {
			t.Errorf("consumer should be interested in %q", typ)
		}
	}
}

// TestOCSSubmitConsumer_AllThreeTypes verifies that Transfer, Generation,
// and TaskSettlement all route through the consumer correctly.
func TestOCSSubmitConsumer_AllThreeTypes(t *testing.T) {
	mock := &mockOCSSubmitter{}
	consumer := recognition.NewOCSSubmitConsumer(mock)

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)
	bus.Register(consumer)
	bus.Start()
	defer bus.Stop()

	events := []*event.Event{
		makeOptimisticTransfer("evt-t"),
		makeOptimisticGeneration("evt-g"),
		makeOptimisticTaskSettlement("evt-ts"),
	}
	for _, ev := range events {
		rm.events[ev.ID] = ev
		recognition.EmitCommit(bus, ev, recognition.SourceLocal, false)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mock.callCount() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if mock.callCount() != 3 {
		t.Fatalf("SubmitFromSync calls = %d; want 3", mock.callCount())
	}
}

// TestOCSSubmitConsumer_IntegrationWithBus verifies end-to-end flow through
// the bus with multiple consumers where only ocs_submit handles OCS types.
func TestOCSSubmitConsumer_IntegrationWithBus(t *testing.T) {
	mock := &mockOCSSubmitter{}
	ocsConsumer := recognition.NewOCSSubmitConsumer(mock)

	var otherConsumed atomic.Int64
	otherConsumer := &testConsumer{
		name: "other",
		interested: func(ev *event.Event) bool {
			return ev.Type == event.EventTypeRegistration
		},
	}
	otherConsumer.consumed = otherConsumed

	store := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(store)
	rm := makeTestReadModel()

	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 2}, idx, rm)
	bus.Register(ocsConsumer)
	bus.Register(otherConsumer)
	bus.Start()
	defer bus.Stop()

	// Transfer → ocs_submit handles, other ignores.
	transfer := makeOptimisticTransfer("evt-multi-t")
	rm.events[transfer.ID] = transfer
	recognition.EmitCommit(bus, transfer, recognition.SourceLocal, false)

	// Registration → other handles, ocs_submit ignores.
	reg := &event.Event{
		ID:   "evt-multi-reg",
		Type: event.EventTypeRegistration,
	}
	rm.events[reg.ID] = reg
	recognition.EmitCommit(bus, reg, recognition.SourceLocal, false)

	time.Sleep(200 * time.Millisecond)

	if mock.callCount() != 1 {
		t.Errorf("ocs SubmitFromSync calls = %d; want 1", mock.callCount())
	}
	if otherConsumer.consumed.Load() != 1 {
		t.Errorf("other consumed = %d; want 1", otherConsumer.consumed.Load())
	}
}
