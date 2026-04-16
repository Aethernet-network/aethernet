package dispatch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// --- In-memory test store ---------------------------------------------------

type memAdmissionStore struct {
	mu      sync.Mutex
	records map[string]*AdmissionRecord
}

func newMemStore() *memAdmissionStore {
	return &memAdmissionStore{records: make(map[string]*AdmissionRecord)}
}

func (m *memAdmissionStore) GetAdmission(key string) (*AdmissionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key]
	if !ok {
		return nil, errors.New("Key not found")
	}
	cp := *rec
	cp.Consumers = make(map[string]PerConsumerStatus, len(rec.Consumers))
	for k, v := range rec.Consumers {
		cp.Consumers[k] = v
	}
	return &cp, nil
}

func (m *memAdmissionStore) PutAdmission(key string, rec *AdmissionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *rec
	cp.Consumers = make(map[string]PerConsumerStatus, len(rec.Consumers))
	for k, v := range rec.Consumers {
		cp.Consumers[k] = v
	}
	m.records[key] = &cp
	return nil
}

func (m *memAdmissionStore) AllAdmissions() ([]*AdmissionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*AdmissionRecord
	for _, rec := range m.records {
		cp := *rec
		cp.Consumers = make(map[string]PerConsumerStatus, len(rec.Consumers))
		for k, v := range rec.Consumers {
			cp.Consumers[k] = v
		}
		result = append(result, &cp)
	}
	return result, nil
}

// --- Synthetic consumer -----------------------------------------------------

type syntheticConsumer struct {
	name         string
	consumerType ConsumerType
	interested   func(*event.Event) bool
	applyCount   atomic.Int64
	applyErr     error
	probeResult  RecoveryStatus
	probeErr     error
}

func newSyntheticConsumer(name string) *syntheticConsumer {
	return &syntheticConsumer{
		name:         name,
		consumerType: TypeA,
		interested:   func(_ *event.Event) bool { return true },
		probeResult:  RecoveryNotStarted,
	}
}

func (c *syntheticConsumer) Name() string                        { return c.name }
func (c *syntheticConsumer) Type() ConsumerType                  { return c.consumerType }
func (c *syntheticConsumer) Interested(ev *event.Event) bool     { return c.interested(ev) }
func (c *syntheticConsumer) Prerequisites(_ *event.Event) []event.EventID { return nil }
func (c *syntheticConsumer) PrerequisiteSchemaVersion() uint32   { return 0 }

func (c *syntheticConsumer) Apply(_ context.Context, _ *event.Event) error {
	c.applyCount.Add(1)
	return c.applyErr
}

func (c *syntheticConsumer) RecoveryProbe(_ context.Context, _ *event.Event) (RecoveryStatus, error) {
	return c.probeResult, c.probeErr
}

// --- Test helpers ------------------------------------------------------------

func newTestDispatcher(t *testing.T) (*Dispatcher, *memAdmissionStore) {
	t.Helper()
	store := newMemStore()
	dag := &stubDAG{
		tips:      []event.EventID{"tip-1"},
		ancestors: map[[2]event.EventID]bool{},
		events:    make(map[event.EventID]*event.Event),
	}
	d := NewDispatcher(store, dag, func() uint64 { return 1 })
	return d, store
}

func newTestDispatcherWithDAG(t *testing.T, dag *stubDAG) (*Dispatcher, *memAdmissionStore) {
	t.Helper()
	store := newMemStore()
	d := NewDispatcher(store, dag, func() uint64 { return 1 })
	return d, store
}

// --- State machine tests ----------------------------------------------------

func TestAdmissionState_FirstDeliveryCreatesRecord(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := newSyntheticConsumer("test-consumer")
	if err := d.Register(c); err != nil {
		t.Fatal(err)
	}

	ev := makeTestEvent(t, "alice", "p1")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	key, _ := AdmissionKey(ev)
	rec, err := store.GetAdmission(key)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec.State != StateApplied {
		t.Errorf("state: got %v want applied", rec.State)
	}
	if rec.Consumers["test-consumer"] != ConsumerApplied {
		t.Errorf("consumer status: got %v want applied", rec.Consumers["test-consumer"])
	}
	if c.applyCount.Load() != 1 {
		t.Errorf("apply count: got %d want 1", c.applyCount.Load())
	}
}

func TestAdmissionState_AppliedIsTerminal(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticConsumer("test-consumer")
	if err := d.Register(c); err != nil {
		t.Fatal(err)
	}

	ev := makeTestEvent(t, "alice", "p1")
	_ = d.Admit(context.Background(), ev)
	_ = d.Admit(context.Background(), ev) // second delivery

	if c.applyCount.Load() != 1 {
		t.Errorf("apply count after duplicate: got %d want 1", c.applyCount.Load())
	}
}

func TestAdmissionState_DuplicateDelivery_ExactlyOnce(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticConsumer("test-consumer")
	_ = d.Register(c)

	ev := makeTestEvent(t, "alice", "p1")
	for i := 0; i < 5; i++ {
		_ = d.Admit(context.Background(), ev)
	}
	if c.applyCount.Load() != 1 {
		t.Errorf("apply count after 5 deliveries: got %d want 1", c.applyCount.Load())
	}
}

func TestAdmissionState_FailAndRetry(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := newSyntheticConsumer("test-consumer")
	c.applyErr = errors.New("transient failure")
	_ = d.Register(c)

	ev := makeTestEvent(t, "alice", "p1")
	_ = d.Admit(context.Background(), ev)

	key, _ := AdmissionKey(ev)
	rec, _ := store.GetAdmission(key)
	if rec.State != StateFailedRetryable {
		t.Fatalf("state after failure: got %v want failed-retryable", rec.State)
	}

	// Fix the consumer and re-deliver.
	c.applyErr = nil
	_ = d.Admit(context.Background(), ev)

	rec, _ = store.GetAdmission(key)
	if rec.State != StateApplied {
		t.Errorf("state after retry: got %v want applied", rec.State)
	}
	if c.applyCount.Load() != 2 {
		t.Errorf("apply count: got %d want 2 (one fail + one success)", c.applyCount.Load())
	}
}

func TestAdmissionState_NoInterestedConsumers(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticConsumer("test-consumer")
	c.interested = func(_ *event.Event) bool { return false }
	_ = d.Register(c)

	ev := makeTestEvent(t, "alice", "p1")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit with no interested consumers: %v", err)
	}
	if c.applyCount.Load() != 0 {
		t.Errorf("apply count: got %d want 0", c.applyCount.Load())
	}
}

// --- Per-consumer tracking tests --------------------------------------------

func TestPerConsumer_MultipleConsumers_PartialFailure(t *testing.T) {
	d, store := newTestDispatcher(t)
	good := newSyntheticConsumer("good")
	bad := newSyntheticConsumer("bad")
	bad.applyErr = errors.New("fail")

	_ = d.Register(good)
	_ = d.Register(bad)

	ev := makeTestEvent(t, "alice", "p1")
	_ = d.Admit(context.Background(), ev)

	key, _ := AdmissionKey(ev)
	rec, _ := store.GetAdmission(key)

	if rec.Consumers["good"] != ConsumerApplied {
		t.Errorf("good consumer: got %v want applied", rec.Consumers["good"])
	}
	if rec.Consumers["bad"] != ConsumerFailedRetryable {
		t.Errorf("bad consumer: got %v want failed-retryable", rec.Consumers["bad"])
	}
	if rec.State != StateFailedRetryable {
		t.Errorf("top-level: got %v want failed-retryable", rec.State)
	}

	// Fix bad consumer and retry.
	bad.applyErr = nil
	_ = d.Admit(context.Background(), ev)

	rec, _ = store.GetAdmission(key)
	if rec.State != StateApplied {
		t.Errorf("after retry: got %v want applied", rec.State)
	}
	// Good consumer should NOT have been re-invoked.
	if good.applyCount.Load() != 1 {
		t.Errorf("good apply count: got %d want 1", good.applyCount.Load())
	}
	if bad.applyCount.Load() != 2 {
		t.Errorf("bad apply count: got %d want 2", bad.applyCount.Load())
	}
}

func TestPerConsumer_AllAppliedAdvancesTopLevel(t *testing.T) {
	consumers := map[string]PerConsumerStatus{
		"a": ConsumerApplied,
		"b": ConsumerApplied,
	}
	if got := computeTopLevelState(consumers); got != StateApplied {
		t.Errorf("got %v want applied", got)
	}
}

func TestPerConsumer_OneFailedCausesTopLevelFailed(t *testing.T) {
	consumers := map[string]PerConsumerStatus{
		"a": ConsumerApplied,
		"b": ConsumerFailedRetryable,
	}
	if got := computeTopLevelState(consumers); got != StateFailedRetryable {
		t.Errorf("got %v want failed-retryable", got)
	}
}

// --- Registration tests -----------------------------------------------------

func TestRegister_ValidConsumer(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticConsumer("test")
	if err := d.Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if d.ConsumerCount() != 1 {
		t.Errorf("consumer count: got %d want 1", d.ConsumerCount())
	}
}

func TestRegister_DuplicateName(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c1 := newSyntheticConsumer("dup")
	c2 := newSyntheticConsumer("dup")
	_ = d.Register(c1)
	if err := d.Register(c2); err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestRegister_InvalidType(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticConsumer("test")
	c.consumerType = 0 // invalid
	if err := d.Register(c); err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestRegister_EmptyName(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticConsumer("")
	if err := d.Register(c); err == nil {
		t.Fatal("expected error for empty name")
	}
}

// --- Crash recovery tests ---------------------------------------------------

func TestRecovery_Processing_ProbeCompleted(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:      []event.EventID{"tip-1"},
		ancestors: map[[2]event.EventID]bool{},
		events:    map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test-consumer")
	c.probeResult = RecoveryCompleted
	_ = d.Register(c)

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion: 1,
		Key:           key,
		State:         StateProcessing,
		EventID:       ev.ID,
		EventType:     string(ev.Type),
		Consumers:     map[string]PerConsumerStatus{"test-consumer": ConsumerPending},
	})

	if err := d.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	rec, _ := store.GetAdmission(key)
	if rec.State != StateApplied {
		t.Errorf("state: got %v want applied", rec.State)
	}
	if rec.Consumers["test-consumer"] != ConsumerApplied {
		t.Errorf("consumer status: got %v want applied", rec.Consumers["test-consumer"])
	}
}

func TestRecovery_Processing_ProbeNotStarted(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:      []event.EventID{"tip-1"},
		ancestors: map[[2]event.EventID]bool{},
		events:    map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test-consumer")
	c.probeResult = RecoveryNotStarted
	_ = d.Register(c)

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion: 1,
		Key:           key,
		State:         StateProcessing,
		EventID:       ev.ID,
		EventType:     string(ev.Type),
		Consumers:     map[string]PerConsumerStatus{"test-consumer": ConsumerPending},
	})

	if err := d.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	rec, _ := store.GetAdmission(key)
	if rec.State != StateFailedRetryable {
		t.Errorf("state: got %v want failed-retryable", rec.State)
	}
}

func TestRecovery_Processing_ProbeError_FailsStartup(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:      []event.EventID{"tip-1"},
		ancestors: map[[2]event.EventID]bool{},
		events:    map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test-consumer")
	c.probeErr = errors.New("partial state detected")
	_ = d.Register(c)

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion: 1,
		Key:           key,
		State:         StateProcessing,
		EventID:       ev.ID,
		EventType:     string(ev.Type),
		Consumers:     map[string]PerConsumerStatus{"test-consumer": ConsumerPending},
	})

	err := d.Recover(context.Background())
	if err == nil {
		t.Fatal("expected Recover to fail on probe error")
	}
}

func TestRecovery_Applied_NoAction(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:   []event.EventID{"tip-1"},
		events: map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion: 1,
		Key:           key,
		State:         StateApplied,
		EventID:       ev.ID,
		EventType:     string(ev.Type),
		Consumers:     map[string]PerConsumerStatus{"test-consumer": ConsumerApplied},
	})

	if err := d.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
}

func TestRecovery_FailedRetryable_NoAction(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:   []event.EventID{"tip-1"},
		events: map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion: 1,
		Key:           key,
		State:         StateFailedRetryable,
		EventID:       ev.ID,
		EventType:     string(ev.Type),
		Consumers:     map[string]PerConsumerStatus{"test-consumer": ConsumerFailedRetryable},
	})

	if err := d.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
}

func TestRecovery_OrphanedConsumer_Skipped(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:   []event.EventID{"tip-1"},
		events: map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)
	// No consumers registered — the record has an orphaned consumer.

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion: 1,
		Key:           key,
		State:         StateProcessing,
		EventID:       ev.ID,
		EventType:     string(ev.Type),
		Consumers:     map[string]PerConsumerStatus{"removed-consumer": ConsumerPending},
	})

	if err := d.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	rec, _ := store.GetAdmission(key)
	// Orphaned pending consumer → state stays processing (no consumers to resolve it)
	if rec.State != StateProcessing {
		t.Errorf("state: got %v want processing (orphaned consumer unresolved)", rec.State)
	}
}

// --- Consumer panic test ----------------------------------------------------

func TestApply_PanicRecovery(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := &panicConsumer{name: "panicker"}
	_ = d.Register(c)

	ev := makeTestEvent(t, "alice", "p1")
	_ = d.Admit(context.Background(), ev)

	key, _ := AdmissionKey(ev)
	rec, _ := store.GetAdmission(key)
	if rec.State != StateFailedRetryable {
		t.Errorf("state after panic: got %v want failed-retryable", rec.State)
	}
}

type panicConsumer struct{ name string }

func (c *panicConsumer) Name() string                                                         { return c.name }
func (c *panicConsumer) Type() ConsumerType                                                   { return TypeA }
func (c *panicConsumer) Interested(_ *event.Event) bool                                       { return true }
func (c *panicConsumer) Apply(_ context.Context, _ *event.Event) error                        { panic("boom") }
func (c *panicConsumer) RecoveryProbe(_ context.Context, _ *event.Event) (RecoveryStatus, error) { return RecoveryNotStarted, nil }
func (c *panicConsumer) Prerequisites(_ *event.Event) []event.EventID                         { return nil }
func (c *panicConsumer) PrerequisiteSchemaVersion() uint32                                    { return 0 }

// --- Integration test: full admission flow ----------------------------------

func TestDispatcher_FullAdmissionFlow_TypeA(t *testing.T) {
	d, store := newTestDispatcher(t)

	c := newSyntheticConsumer("settlement.SettlementConsumer")
	_ = d.Register(c)

	ev := makeTestEvent(t, "alice", "transfer")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if c.applyCount.Load() != 1 {
		t.Fatalf("apply count: got %d want 1", c.applyCount.Load())
	}

	key, _ := AdmissionKey(ev)
	rec, err := store.GetAdmission(key)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec.State != StateApplied {
		t.Errorf("state: got %v want applied", rec.State)
	}
	if rec.Consumers["settlement.SettlementConsumer"] != ConsumerApplied {
		t.Errorf("per-consumer: got %v want applied", rec.Consumers["settlement.SettlementConsumer"])
	}
	if rec.EventID != ev.ID {
		t.Errorf("event ID: got %s want %s", rec.EventID, ev.ID)
	}
}
