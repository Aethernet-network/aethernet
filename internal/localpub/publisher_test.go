package localpub

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// mockDAG records Add calls and optionally returns an error.
type mockDAG struct {
	mu      sync.Mutex
	added   []*event.Event
	addErr  error
	addFunc func(ev *event.Event) error // optional per-call control
}

func (m *mockDAG) Add(ev *event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addFunc != nil {
		return m.addFunc(ev)
	}
	if m.addErr != nil {
		return m.addErr
	}
	m.added = append(m.added, ev)
	return nil
}

func (m *mockDAG) addedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.added)
}

// mockDisseminator records SubmitLocalEvent and Broadcast calls.
type mockDisseminator struct {
	mu              sync.Mutex
	submitted       []*event.Event
	broadcast       []*event.Event
	submitErr       error
	broadcastErr    error
}

func (m *mockDisseminator) SubmitLocalEvent(ev *event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submitted = append(m.submitted, ev)
	return m.submitErr
}

func (m *mockDisseminator) Broadcast(ev *event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.broadcast = append(m.broadcast, ev)
	return m.broadcastErr
}

func (m *mockDisseminator) submittedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.submitted)
}

func (m *mockDisseminator) broadcastCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.broadcast)
}

// testEvent creates a minimal event for testing.
func testEvent(id string) *event.Event {
	return &event.Event{
		ID:      event.EventID(id),
		Type:    event.EventTypeTransfer,
		AgentID: "test-agent",
	}
}

// ---------------------------------------------------------------------------
// Core publication path
// ---------------------------------------------------------------------------

func TestPublish_DAGThenBroadcast(t *testing.T) {
	dag := &mockDAG{}
	dis := &mockDisseminator{}
	pub := New(dag, dis)

	ev := testEvent("ev-1")
	if err := pub.Publish(ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if dag.addedCount() != 1 {
		t.Fatalf("expected 1 DAG add, got %d", dag.addedCount())
	}
	if dis.submittedCount() != 1 {
		t.Fatalf("expected 1 SubmitLocalEvent, got %d", dis.submittedCount())
	}
	if dis.broadcastCount() != 1 {
		t.Fatalf("expected 1 Broadcast, got %d", dis.broadcastCount())
	}
}

func TestPublish_DAGAddFails_NoBroadcast(t *testing.T) {
	dag := &mockDAG{addErr: errors.New("duplicate event")}
	dis := &mockDisseminator{}
	pub := New(dag, dis)

	ev := testEvent("ev-dup")
	err := pub.Publish(ev)
	if !errors.Is(err, ErrDAGAddFailed) {
		t.Fatalf("expected ErrDAGAddFailed, got %v", err)
	}

	// No broadcast should have occurred.
	if dis.submittedCount() != 0 {
		t.Fatal("SubmitLocalEvent should NOT be called when dag.Add fails")
	}
	if dis.broadcastCount() != 0 {
		t.Fatal("Broadcast should NOT be called when dag.Add fails")
	}
}

func TestPublish_NilEvent(t *testing.T) {
	dag := &mockDAG{}
	pub := New(dag, nil)

	err := pub.Publish(nil)
	if !errors.Is(err, ErrNilEvent) {
		t.Fatalf("expected ErrNilEvent, got %v", err)
	}
}

func TestPublish_NilDisseminator_DAGOnly(t *testing.T) {
	dag := &mockDAG{}
	pub := New(dag, nil) // no disseminator (pre-networking)

	ev := testEvent("ev-pre-net")
	if err := pub.Publish(ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if dag.addedCount() != 1 {
		t.Fatal("event should be added to DAG even without disseminator")
	}
	// No panic — nil disseminator is handled gracefully.
}

// ---------------------------------------------------------------------------
// Broadcast errors are non-fatal
// ---------------------------------------------------------------------------

func TestPublish_SubmitLocalEventError_NonFatal(t *testing.T) {
	dag := &mockDAG{}
	dis := &mockDisseminator{submitErr: errors.New("ingest full")}
	pub := New(dag, dis)

	ev := testEvent("ev-submit-err")
	err := pub.Publish(ev)
	if err != nil {
		t.Fatalf("Publish should succeed despite SubmitLocalEvent error: %v", err)
	}

	// DAG add succeeded.
	if dag.addedCount() != 1 {
		t.Fatal("event should be in DAG")
	}
	// Broadcast was still attempted despite SubmitLocalEvent failure.
	if dis.broadcastCount() != 1 {
		t.Fatal("Broadcast should be attempted even if SubmitLocalEvent fails")
	}
}

func TestPublish_BroadcastError_NonFatal(t *testing.T) {
	dag := &mockDAG{}
	dis := &mockDisseminator{broadcastErr: errors.New("no peers")}
	pub := New(dag, dis)

	ev := testEvent("ev-broadcast-err")
	err := pub.Publish(ev)
	if err != nil {
		t.Fatalf("Publish should succeed despite Broadcast error: %v", err)
	}

	if dag.addedCount() != 1 {
		t.Fatal("event should be in DAG")
	}
}

// ---------------------------------------------------------------------------
// SetDisseminator wires networking after startup
// ---------------------------------------------------------------------------

func TestSetDisseminator_WiresAfterStartup(t *testing.T) {
	dag := &mockDAG{}
	pub := New(dag, nil) // start without disseminator

	ev1 := testEvent("ev-before-net")
	_ = pub.Publish(ev1)

	if dag.addedCount() != 1 {
		t.Fatal("event should be in DAG")
	}

	// Wire disseminator after networking starts.
	dis := &mockDisseminator{}
	pub.SetDisseminator(dis)

	ev2 := testEvent("ev-after-net")
	_ = pub.Publish(ev2)

	if dag.addedCount() != 2 {
		t.Fatal("second event should be in DAG")
	}
	if dis.submittedCount() != 1 {
		t.Fatal("second event should be submitted via disseminator")
	}
	if dis.broadcastCount() != 1 {
		t.Fatal("second event should be broadcast via disseminator")
	}
}

// ---------------------------------------------------------------------------
// PublishBatch
// ---------------------------------------------------------------------------

func TestPublishBatch_AllSucceed(t *testing.T) {
	dag := &mockDAG{}
	dis := &mockDisseminator{}
	pub := New(dag, dis)

	events := []*event.Event{
		testEvent("batch-1"),
		testEvent("batch-2"),
		testEvent("batch-3"),
	}

	n, err := pub.PublishBatch(events)
	if err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 published, got %d", n)
	}
	if dag.addedCount() != 3 {
		t.Fatalf("expected 3 DAG adds, got %d", dag.addedCount())
	}
}

func TestPublishBatch_PartialFailure(t *testing.T) {
	callCount := 0
	dag := &mockDAG{
		addFunc: func(ev *event.Event) error {
			callCount++
			if callCount == 2 {
				return errors.New("duplicate")
			}
			return nil
		},
	}
	dis := &mockDisseminator{}
	pub := New(dag, dis)

	events := []*event.Event{
		testEvent("b1"),
		testEvent("b2"), // will fail
		testEvent("b3"),
	}

	n, err := pub.PublishBatch(events)
	if err == nil {
		t.Fatal("expected error from partial failure")
	}
	if n != 2 {
		t.Fatalf("expected 2 published (b1 and b3), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Ordering: DAG add must happen before broadcast
// ---------------------------------------------------------------------------

func TestPublish_OrderingDAGBeforeBroadcast(t *testing.T) {
	// Track the order of operations.
	var order []string
	var mu sync.Mutex

	dag := &mockDAG{
		addFunc: func(ev *event.Event) error {
			mu.Lock()
			order = append(order, "dag.Add")
			mu.Unlock()
			return nil
		},
	}
	dis := &mockDisseminator{}
	// Wrap to track ordering.
	originalSubmit := dis.SubmitLocalEvent
	dis2 := &orderTrackingDisseminator{
		order: &order,
		mu:    &mu,
		inner: dis,
	}
	_ = originalSubmit // suppress unused

	pub := New(dag, dis2)
	_ = pub.Publish(testEvent("order-test"))

	mu.Lock()
	defer mu.Unlock()

	if len(order) < 3 {
		t.Fatalf("expected at least 3 operations, got %d: %v", len(order), order)
	}
	if order[0] != "dag.Add" {
		t.Fatalf("dag.Add must be first, got %v", order)
	}
	if order[1] != "SubmitLocalEvent" {
		t.Fatalf("SubmitLocalEvent must be second, got %v", order)
	}
	if order[2] != "Broadcast" {
		t.Fatalf("Broadcast must be third, got %v", order)
	}
}

type orderTrackingDisseminator struct {
	order *[]string
	mu    *sync.Mutex
	inner *mockDisseminator
}

func (d *orderTrackingDisseminator) SubmitLocalEvent(ev *event.Event) error {
	d.mu.Lock()
	*d.order = append(*d.order, "SubmitLocalEvent")
	d.mu.Unlock()
	return d.inner.SubmitLocalEvent(ev)
}

func (d *orderTrackingDisseminator) Broadcast(ev *event.Event) error {
	d.mu.Lock()
	*d.order = append(*d.order, "Broadcast")
	d.mu.Unlock()
	return d.inner.Broadcast(ev)
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestPublish_ConcurrentSafe(t *testing.T) {
	dag := &mockDAG{}
	dis := &mockDisseminator{}
	pub := New(dag, dis)

	const goroutines = 20
	var wg sync.WaitGroup
	var published atomic.Int64

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := testEvent(string(rune('A' + i)))
			if err := pub.Publish(ev); err == nil {
				published.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if published.Load() != goroutines {
		t.Fatalf("expected %d published, got %d", goroutines, published.Load())
	}
	if dag.addedCount() != goroutines {
		t.Fatalf("expected %d DAG adds, got %d", goroutines, dag.addedCount())
	}
}

// ---------------------------------------------------------------------------
// Inbound/synced events must NOT use publisher
// ---------------------------------------------------------------------------

func TestPublish_InboundEventsMustNotUsePublisher(t *testing.T) {
	// This is a documentation/design test — the Publisher interface does not
	// distinguish local vs. synced events. The protection is architectural:
	// the sync handler calls dag.Add directly (for inbound events from peers),
	// while the Publisher is only used by local event authors.
	//
	// This test verifies that the Publisher adds events to the DAG (which
	// would cause a duplicate error for already-synced events), preventing
	// accidental republication.

	firstCall := true
	dag := &mockDAG{
		addFunc: func(ev *event.Event) error {
			if !firstCall {
				return errors.New("duplicate: already in DAG")
			}
			firstCall = false
			return nil
		},
	}
	dis := &mockDisseminator{}
	pub := New(dag, dis)

	ev := testEvent("synced-event")

	// First publish succeeds (simulating local creation).
	if err := pub.Publish(ev); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	// Second publish fails (simulating re-publication of an already-synced event).
	err := pub.Publish(ev)
	if !errors.Is(err, ErrDAGAddFailed) {
		t.Fatalf("re-publishing a synced event should fail with ErrDAGAddFailed, got %v", err)
	}

	// Only one broadcast should have occurred.
	if dis.submittedCount() != 1 {
		t.Fatalf("expected 1 submit, got %d", dis.submittedCount())
	}
	if dis.broadcastCount() != 1 {
		t.Fatalf("expected 1 broadcast, got %d", dis.broadcastCount())
	}
}

// ---------------------------------------------------------------------------
// Multiple event types work identically
// ---------------------------------------------------------------------------

func TestPublish_AllEventTypes(t *testing.T) {
	types := []event.EventType{
		event.EventTypeTransfer,
		event.EventTypeRegistration,
		event.EventTypeVerificationVote,
		event.EventTypeSettlement,
		event.EventTypeGenesisFunding,
		event.EventTypeTrajectoryCommit,
		event.EventTypeTaskPosted,
		event.EventTypeValidatorJoin,
	}

	for _, et := range types {
		dag := &mockDAG{}
		dis := &mockDisseminator{}
		pub := New(dag, dis)

		ev := &event.Event{
			ID:      event.EventID("ev-" + string(et)),
			Type:    et,
			AgentID: "test",
		}

		if err := pub.Publish(ev); err != nil {
			t.Fatalf("Publish(%s): %v", et, err)
		}
		if dag.addedCount() != 1 || dis.submittedCount() != 1 || dis.broadcastCount() != 1 {
			t.Fatalf("Publish(%s): expected 1 add + 1 submit + 1 broadcast", et)
		}
	}
}
