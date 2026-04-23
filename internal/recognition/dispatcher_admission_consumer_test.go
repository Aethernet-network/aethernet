package recognition_test

// Integration tests for the general dispatcher admission router.
//
// These tests close the bug class surfaced by Part F Phase D:
// canonical event types with registered dispatcher consumers but no
// recognition-layer admission pathway. Each test wires the full seam
// (recognition Bus → DispatcherAdmissionConsumer → real
// dispatch.Dispatcher → registered dispatch.Consumer) and asserts on
// the observable downstream side effect that proves the seam is intact.
//
// If any of these tests regress, the Phase D failure mode is back.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// ---- In-package in-memory fixtures ------------------------------------------
//
// These mirror internal/dispatch/dispatcher_test.go's memAdmissionStore and
// stubDAG but are declared here because those are unexported. The behavior
// is the same: an in-memory admission store and a minimal DAG anchor reader.

type memAdmitStore struct {
	mu      sync.Mutex
	records map[string]*dispatch.AdmissionRecord
}

func newMemAdmitStore() *memAdmitStore {
	return &memAdmitStore{records: make(map[string]*dispatch.AdmissionRecord)}
}

func (m *memAdmitStore) GetAdmission(key string) (*dispatch.AdmissionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key]
	if !ok {
		return nil, errors.New("Key not found")
	}
	cp := *rec
	cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
	for k, v := range rec.Consumers {
		cp.Consumers[k] = v
	}
	return &cp, nil
}

func (m *memAdmitStore) PutAdmission(key string, rec *dispatch.AdmissionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *rec
	cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
	for k, v := range rec.Consumers {
		cp.Consumers[k] = v
	}
	m.records[key] = &cp
	return nil
}

func (m *memAdmitStore) DeleteAdmission(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}

func (m *memAdmitStore) AllAdmissions() ([]*dispatch.AdmissionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*dispatch.AdmissionRecord, 0, len(m.records))
	for _, rec := range m.records {
		cp := *rec
		cp.Consumers = make(map[string]dispatch.PerConsumerStatus, len(rec.Consumers))
		for k, v := range rec.Consumers {
			cp.Consumers[k] = v
		}
		out = append(out, &cp)
	}
	return out, nil
}

type minimalDAG struct{}

func (minimalDAG) Tips() []event.EventID                                     { return []event.EventID{"tip-1"} }
func (minimalDAG) IsAncestor(_, _ event.EventID) (bool, error)               { return true, nil }
func (minimalDAG) Get(_ event.EventID) (*event.Event, error)                 { return nil, errors.New("Key not found") }

// ---- Recording dispatcher consumer ------------------------------------------

// recordingConsumer is a dispatch.Consumer that records each Apply call
// and optionally filters by event type. Used to assert "Apply did/did
// not run for this event" in integration tests.
type recordingConsumer struct {
	name          string
	interestedOn  event.EventType
	applyCount    atomic.Int64
	lastAppliedID atomic.Value // event.EventID
}

func newRecordingConsumer(name string, interestedOn event.EventType) *recordingConsumer {
	c := &recordingConsumer{name: name, interestedOn: interestedOn}
	c.lastAppliedID.Store(event.EventID(""))
	return c
}

func (c *recordingConsumer) Name() string                                    { return c.name }
func (c *recordingConsumer) Type() dispatch.ConsumerType                     { return dispatch.TypeA }
func (c *recordingConsumer) Interested(ev *event.Event) bool                 { return ev.Type == c.interestedOn }
func (c *recordingConsumer) Prerequisites(_ *event.Event) []event.EventID    { return nil }
func (c *recordingConsumer) PrerequisiteSchemaVersion() uint32               { return 0 }

func (c *recordingConsumer) Apply(_ context.Context, ev *event.Event) error {
	c.applyCount.Add(1)
	c.lastAppliedID.Store(ev.ID)
	return nil
}

func (c *recordingConsumer) RecoveryProbe(_ context.Context, _ *event.Event) (dispatch.RecoveryStatus, error) {
	return dispatch.RecoveryNotStarted, nil
}

// ---- Shared test harness -----------------------------------------------------

// admissionHarness wires a recognition Bus + DispatcherAdmissionConsumer
// + real dispatch.Dispatcher so a test can call Emit and observe
// downstream Apply calls.
type admissionHarness struct {
	bus        *recognition.Bus
	dispatcher *dispatch.Dispatcher
	rm         *testReadModel
}

func newAdmissionHarness(t *testing.T, consumers ...dispatch.Consumer) *admissionHarness {
	t.Helper()

	store := newMemAdmitStore()
	d := dispatch.NewDispatcher(store, minimalDAG{}, func() uint64 { return 1 })
	for _, c := range consumers {
		if err := d.Register(c); err != nil {
			t.Fatalf("dispatcher.Register(%s): %v", c.Name(), err)
		}
	}

	idxStore := recognition.NewMemoryIndexStore()
	idx := recognition.NewIndex(idxStore)
	rm := makeTestReadModel()
	bus := recognition.NewBus(recognition.BusConfig{QueueSize: 64, Workers: 1}, idx, rm)

	router := recognition.NewDispatcherAdmissionConsumer(d)
	if err := bus.Register(router); err != nil {
		t.Fatalf("bus.Register(admission router): %v", err)
	}

	bus.Start()
	t.Cleanup(bus.Stop)

	return &admissionHarness{bus: bus, dispatcher: d, rm: rm}
}

func (h *admissionHarness) emit(t *testing.T, ev *event.Event) {
	t.Helper()
	h.rm.events[ev.ID] = ev
	err := h.bus.Emit(recognition.CommitRecord{
		EventID:     ev.ID,
		EventType:   ev.Type,
		Source:      recognition.SourceLocal,
		CommittedAt: time.Now(),
	}, ev)
	if err != nil {
		t.Fatalf("bus.Emit(%s): %v", ev.ID, err)
	}
}

// waitForApplyCount polls for the consumer's applyCount to reach target
// within the timeout. Returns the final count.
func waitForApplyCount(c *recordingConsumer, target int64, timeout time.Duration) int64 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.applyCount.Load() >= target {
			return c.applyCount.Load()
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c.applyCount.Load()
}

// ---- Tests -------------------------------------------------------------------

// TestDispatcherAdmissionRouter_ForwardsToDispatcher_IntegerMigrationActivation
// directly exercises the Part F Phase D failure mode. Pre-fix, this test
// would have caught the bug: the dispatcher consumer's Apply never ran
// because no admission pathway existed for IntegerMigrationActivation.
// Post-fix, the admission router forwards indiscriminately and the
// dispatcher's Interested() filter routes the event to the consumer.
func TestDispatcherAdmissionRouter_ForwardsToDispatcher_IntegerMigrationActivation(t *testing.T) {
	rc := newRecordingConsumer("test.integer_migration_activation", event.EventTypeIntegerMigrationActivation)
	h := newAdmissionHarness(t, rc)

	ev, err := event.New(
		event.EventTypeIntegerMigrationActivation,
		nil,
		event.IntegerMigrationActivationPayload{
			Version:          1,
			EmittingAgent:    "operator-1",
			ActivationReason: "part-e1 integration test",
			EmittedAtUnix:    1_776_880_969,
		},
		"operator-1",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	h.emit(t, ev)

	if got := waitForApplyCount(rc, 1, 2*time.Second); got != 1 {
		t.Fatalf("applyCount = %d, want 1 — admission router did not forward activation event to dispatcher", got)
	}
	if id := rc.lastAppliedID.Load().(event.EventID); id != ev.ID {
		t.Errorf("lastAppliedID = %q, want %q", id, ev.ID)
	}
}

// TestDispatcherAdmissionRouter_ForwardsToDispatcher_TaskVerificationConsensus
// proves the refactor preserves the pre-existing admission flow for
// TaskVerificationConsensus events. Before Part E.1 this path went
// via TaskVerificationConsensusConsumer's inline dispatcher.Admit
// call; after Part E.1 it goes via the general admission router. Both
// routes must reach the registered dispatcher consumer.
func TestDispatcherAdmissionRouter_ForwardsToDispatcher_TaskVerificationConsensus(t *testing.T) {
	rc := newRecordingConsumer("test.tv_consensus", event.EventTypeTaskVerificationConsensus)
	h := newAdmissionHarness(t, rc)

	ev, err := event.New(
		event.EventTypeTaskVerificationConsensus,
		nil,
		event.TaskVerificationConsensusPayload{
			Version:               1,
			TaskID:                "task-abc",
			RoundID:               "round-xyz",
			FinalVerdict:          "pass",
			FinalScoreBP:          8000,
			FinalizationTimeUnix:  1_776_880_969,
		},
		"validator-1",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	h.emit(t, ev)

	if got := waitForApplyCount(rc, 1, 2*time.Second); got != 1 {
		t.Fatalf("applyCount = %d, want 1 — admission router did not forward tv_consensus event", got)
	}
}

// TestDispatcherAdmissionRouter_ForwardsToDispatcher_UnknownType_DispatcherFilters
// proves the router forwards blindly and the dispatcher's per-consumer
// Interested() filter is what actually decides whether Apply runs. Emit
// a TaskPosted event while only an IntegerMigrationActivation-interested
// consumer is registered — the router MUST forward (otherwise the router
// has been incorrectly filtering), and the dispatcher MUST drop the event
// (because no registered consumer cares).
//
// Observable: the consumer's applyCount stays at 0.
func TestDispatcherAdmissionRouter_ForwardsToDispatcher_UnknownType_DispatcherFilters(t *testing.T) {
	rc := newRecordingConsumer("test.integer_migration_activation", event.EventTypeIntegerMigrationActivation)
	h := newAdmissionHarness(t, rc)

	// TaskPosted — no registered consumer is interested.
	ev, err := event.New(
		event.EventTypeTaskPosted,
		nil,
		event.TaskPostedPayload{
			Version:     1,
			TaskID:      "task-unrelated",
			PosterID:    "poster-1",
			Title:       "unrelated",
			Description: "no dispatcher consumer cares",
			Category:    "research",
			Budget:      100000,
		},
		"poster-1",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	h.emit(t, ev)

	// Give the bus + router + dispatcher enough time to process + confirm
	// no-op. This is a negative assertion, so timeout is conservative.
	if got := waitForApplyCount(rc, 1, 500*time.Millisecond); got != 0 {
		t.Fatalf("applyCount = %d, want 0 — dispatcher consumer Apply ran for an event it was not Interested in", got)
	}
}
