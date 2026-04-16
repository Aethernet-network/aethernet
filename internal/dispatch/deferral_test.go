package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// --- Deferral index unit tests ----------------------------------------------

func TestDeferralIndex_AddAndLookup(t *testing.T) {
	d, _ := newTestDispatcher(t)
	d.mu.Lock()
	d.addToDeferralIndex([]event.EventID{"prereq-1", "prereq-2"}, "admission-key-1")
	d.mu.Unlock()

	d.mu.Lock()
	keys1 := d.deferralIndex["prereq-1"]
	keys2 := d.deferralIndex["prereq-2"]
	d.mu.Unlock()

	if len(keys1) != 1 || keys1[0] != "admission-key-1" {
		t.Errorf("prereq-1: got %v want [admission-key-1]", keys1)
	}
	if len(keys2) != 1 || keys2[0] != "admission-key-1" {
		t.Errorf("prereq-2: got %v want [admission-key-1]", keys2)
	}
}

func TestDeferralIndex_CleanOnTransition(t *testing.T) {
	d, _ := newTestDispatcher(t)
	d.mu.Lock()
	d.addToDeferralIndex([]event.EventID{"prereq-1"}, "key-A")
	d.addToDeferralIndex([]event.EventID{"prereq-1"}, "key-B")
	d.removeFromDeferralIndex("key-A")
	remaining := d.deferralIndex["prereq-1"]
	d.mu.Unlock()

	if len(remaining) != 1 || remaining[0] != "key-B" {
		t.Errorf("after remove key-A: got %v want [key-B]", remaining)
	}
}

func TestDeferralIndex_RebuildFromStore(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:   []event.EventID{"tip-1"},
		events: map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion:        1,
		Key:                  key,
		State:                StateReservedPendingPrereqs,
		EventID:              ev.ID,
		EventType:            string(ev.Type),
		MissingPrerequisites: []event.EventID{"missing-prereq"},
		Consumers:            map[string]PerConsumerStatus{"c": ConsumerPending},
	})

	records, _ := store.AllAdmissions()
	d.mu.Lock()
	d.rebuildDeferralIndex(records)
	keys := d.deferralIndex["missing-prereq"]
	d.mu.Unlock()

	if len(keys) != 1 || keys[0] != key {
		t.Errorf("rebuild: got %v want [%s]", keys, key)
	}
}

// --- Integration: full deferral flow ----------------------------------------

func TestDispatcher_DeferralFlow_EndToEnd(t *testing.T) {
	parent := makeTestEvent(t, "alice", "parent")
	child := makeTestEvent(t, "alice", "child")

	// DAG knows parent is ancestor of child, but parent not yet retrievable
	// via Get (simulating the "valid but not yet projected" edge case).
	dag := &stubDAGWithGetOverride{
		stubDAG: stubDAG{
			tips: []event.EventID{"tip-1"},
			ancestors: map[[2]event.EventID]bool{
				{parent.ID, child.ID}: true,
			},
			events: map[event.EventID]*event.Event{
				child.ID: child,
			},
		},
		getMissing: map[event.EventID]bool{parent.ID: true},
	}

	store := newMemStore()
	d := NewDispatcher(store, dag, func() uint64 { return 1 })

	var applyCount atomic.Int64
	c := &prereqConsumer{
		syntheticConsumer: newSyntheticConsumer("test-consumer"),
		prereqs:          []event.EventID{parent.ID},
	}
	c.syntheticConsumer.applyCount = applyCount
	_ = d.Register(c)

	// First Admit: defers because parent is not projected.
	if err := d.Admit(context.Background(), child); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	key, _ := AdmissionKey(child)
	rec, _ := store.GetAdmission(key)
	if rec.State != StateReservedPendingPrereqs {
		t.Fatalf("state after first Admit: got %v want reserved-pending-prerequisites", rec.State)
	}
	if c.syntheticConsumer.applyCount.Load() != 0 {
		t.Fatalf("Apply should not have been called during deferral")
	}

	// Now "project" the parent by making it available via Get.
	dag.getMissing[parent.ID] = false
	dag.events[parent.ID] = parent

	// NotifyProjection triggers re-check.
	d.NotifyProjection(context.Background(), parent.ID)

	rec, _ = store.GetAdmission(key)
	if rec.State != StateApplied {
		t.Errorf("state after NotifyProjection: got %v want applied", rec.State)
	}
	if c.syntheticConsumer.applyCount.Load() != 1 {
		t.Errorf("Apply should have been called once; got %d", c.syntheticConsumer.applyCount.Load())
	}
}

// --- Integration: forgery rejection -----------------------------------------

func TestDispatcher_ForgeryRejection(t *testing.T) {
	unrelated := makeTestEvent(t, "bob", "unrelated")
	child := makeTestEvent(t, "alice", "child")

	dag := &stubDAG{
		tips:      []event.EventID{"tip-1"},
		ancestors: map[[2]event.EventID]bool{}, // unrelated is NOT an ancestor
		events: map[event.EventID]*event.Event{
			child.ID:     child,
			unrelated.ID: unrelated,
		},
	}
	store := newMemStore()
	d := NewDispatcher(store, dag, func() uint64 { return 1 })

	c := &prereqConsumer{
		syntheticConsumer: newSyntheticConsumer("test-consumer"),
		prereqs:          []event.EventID{unrelated.ID},
	}
	_ = d.Register(c)

	err := d.Admit(context.Background(), child)
	if !errors.Is(err, ErrPrerequisiteForgery) {
		t.Fatalf("want ErrPrerequisiteForgery, got %v", err)
	}

	// No admission record should exist for a forged prerequisite.
	key, _ := AdmissionKey(child)
	_, getErr := store.GetAdmission(key)
	if getErr == nil {
		t.Error("admission record should not exist after forgery rejection")
	}
}

// --- Integration: no-prerequisite consumer proceeds directly ----------------

func TestDispatcher_NoPrerequisites_ProceedsDirectly(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticConsumer("test-consumer")
	_ = d.Register(c)

	ev := makeTestEvent(t, "alice", "p1")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if c.applyCount.Load() != 1 {
		t.Errorf("consumer without prerequisites: Apply count %d want 1", c.applyCount.Load())
	}
}

// --- Failover threshold test ------------------------------------------------

func TestFailoverThreshold_AtThresholdFailsRecovery(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAGWithGetOverride{
		stubDAG: stubDAG{
			tips: []event.EventID{"tip-1"},
			ancestors: map[[2]event.EventID]bool{
				{"missing-prereq", ev.ID}: true,
			},
			events: map[event.EventID]*event.Event{ev.ID: ev},
		},
		getMissing: map[event.EventID]bool{"missing-prereq": true},
	}

	store := newMemStore()
	currentEpoch := uint64(100)
	d := NewDispatcher(store, dag, func() uint64 { return currentEpoch })

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion:        1,
		Key:                  key,
		State:                StateReservedPendingPrereqs,
		EventID:              ev.ID,
		EventType:            string(ev.Type),
		MissingPrerequisites: []event.EventID{"missing-prereq"},
		CreatedAtEpoch:       0, // deferred since epoch 0; age = 100 >= threshold
		Consumers:            map[string]PerConsumerStatus{"c": ConsumerPending},
	})

	err := d.Recover(context.Background())
	if err == nil {
		t.Fatal("expected Recover to fail at failover threshold")
	}
	if !containsStr(err.Error(), "manual intervention required") {
		t.Errorf("error should mention manual intervention: %v", err)
	}
}

func TestFailoverThreshold_BelowThresholdContinues(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAGWithGetOverride{
		stubDAG: stubDAG{
			tips: []event.EventID{"tip-1"},
			ancestors: map[[2]event.EventID]bool{
				{"missing-prereq", ev.ID}: true,
			},
			events: map[event.EventID]*event.Event{ev.ID: ev},
		},
		getMissing: map[event.EventID]bool{"missing-prereq": true},
	}

	store := newMemStore()
	currentEpoch := uint64(99)
	d := NewDispatcher(store, dag, func() uint64 { return currentEpoch })

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion:        1,
		Key:                  key,
		State:                StateReservedPendingPrereqs,
		EventID:              ev.ID,
		EventType:            string(ev.Type),
		MissingPrerequisites: []event.EventID{"missing-prereq"},
		CreatedAtEpoch:       0,
		Consumers:            map[string]PerConsumerStatus{"c": ConsumerPending},
	})

	if err := d.Recover(context.Background()); err != nil {
		t.Fatalf("Recover should succeed below threshold: %v", err)
	}
}

// --- Evidence emission tests -------------------------------------------------

func TestEvidenceEmission_AtComplaintThreshold(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAGWithGetOverride{
		stubDAG: stubDAG{
			tips: []event.EventID{"tip-1"},
			ancestors: map[[2]event.EventID]bool{
				{"missing-prereq", ev.ID}: true,
			},
			events: map[event.EventID]*event.Event{ev.ID: ev},
		},
		getMissing: map[event.EventID]bool{"missing-prereq": true},
	}

	store := newMemStore()
	currentEpoch := uint64(30) // exactly at complaint threshold
	d := NewDispatcher(store, dag, func() uint64 { return currentEpoch })

	var emitted []*event.Event
	d.SetEvidenceEmitter(func(ev *event.Event) error {
		emitted = append(emitted, ev)
		return nil
	})

	key, _ := AdmissionKey(ev)
	rec := &AdmissionRecord{
		SchemaVersion:        1,
		Key:                  key,
		State:                StateReservedPendingPrereqs,
		EventID:              ev.ID,
		EventType:            string(ev.Type),
		MissingPrerequisites: []event.EventID{"missing-prereq"},
		CreatedAtEpoch:       0,
		Consumers:            map[string]PerConsumerStatus{"c": ConsumerPending},
	}

	d.checkDeferralThresholds(context.Background(), rec)

	if len(emitted) != 1 {
		t.Fatalf("expected 1 evidence event, got %d", len(emitted))
	}
	if emitted[0].Type != event.EventTypePrerequisiteWithholding {
		t.Errorf("event type: got %s want PrerequisiteWithholding", emitted[0].Type)
	}
	if !rec.EvidenceEmitted {
		t.Error("EvidenceEmitted should be true after emission")
	}
}

func TestEvidenceEmission_BelowThreshold(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	store := newMemStore()
	d := NewDispatcher(store, &stubDAG{tips: []event.EventID{"tip-1"}}, func() uint64 { return 29 })

	var emitted []*event.Event
	d.SetEvidenceEmitter(func(ev *event.Event) error {
		emitted = append(emitted, ev)
		return nil
	})

	key, _ := AdmissionKey(ev)
	rec := &AdmissionRecord{
		Key:                  key,
		State:                StateReservedPendingPrereqs,
		EventID:              ev.ID,
		MissingPrerequisites: []event.EventID{"missing"},
		CreatedAtEpoch:       0,
	}

	d.checkDeferralThresholds(context.Background(), rec)

	if len(emitted) != 0 {
		t.Errorf("should not emit below threshold; got %d events", len(emitted))
	}
}

func TestEvidenceEmission_OnlyOnce(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	store := newMemStore()
	d := NewDispatcher(store, &stubDAG{tips: []event.EventID{"tip-1"}}, func() uint64 { return 50 })

	var emitCount int
	d.SetEvidenceEmitter(func(_ *event.Event) error {
		emitCount++
		return nil
	})

	key, _ := AdmissionKey(ev)
	rec := &AdmissionRecord{
		Key:                  key,
		State:                StateReservedPendingPrereqs,
		EventID:              ev.ID,
		MissingPrerequisites: []event.EventID{"missing"},
		CreatedAtEpoch:       0,
	}
	_ = store.PutAdmission(key, rec)

	d.checkDeferralThresholds(context.Background(), rec)
	d.checkDeferralThresholds(context.Background(), rec) // second call

	if emitCount != 1 {
		t.Errorf("evidence emitted %d times; want exactly 1", emitCount)
	}
}

func TestEvidenceEmission_PayloadFields(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	store := newMemStore()
	d := NewDispatcher(store, &stubDAG{tips: []event.EventID{"tip-1"}}, func() uint64 { return 35 })

	var emitted *event.Event
	d.SetEvidenceEmitter(func(e *event.Event) error {
		emitted = e
		return nil
	})

	key, _ := AdmissionKey(ev)
	rec := &AdmissionRecord{
		Key:                  key,
		State:                StateReservedPendingPrereqs,
		EventID:              ev.ID,
		EventType:            "Transfer",
		MissingPrerequisites: []event.EventID{"prereq-1"},
		CreatedAtEpoch:       0,
	}
	_ = store.PutAdmission(key, rec)

	d.checkDeferralThresholds(context.Background(), rec)

	if emitted == nil {
		t.Fatal("no event emitted")
	}
	payload, err := event.GetPayload[event.PrerequisiteWithholdingPayload](emitted)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.StuckEventID != ev.ID {
		t.Errorf("StuckEventID: got %s want %s", payload.StuckEventID, ev.ID)
	}
	if payload.DeferredSinceEpoch != 0 {
		t.Errorf("DeferredSinceEpoch: got %d want 0", payload.DeferredSinceEpoch)
	}
	if payload.CurrentEpoch != 35 {
		t.Errorf("CurrentEpoch: got %d want 35", payload.CurrentEpoch)
	}
	if len(payload.MissingPrerequisites) != 1 || payload.MissingPrerequisites[0] != "prereq-1" {
		t.Errorf("MissingPrerequisites: got %v want [prereq-1]", payload.MissingPrerequisites)
	}
}

// --- Schema version mismatch tests ------------------------------------------

func TestSchemaVersion_MatchPasses(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:   []event.EventID{"tip-1"},
		events: map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test-consumer")
	c.probeResult = RecoveryCompleted
	_ = d.Register(c)

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion:             1,
		Key:                       key,
		State:                     StateProcessing,
		PrerequisiteSchemaVersion: 0, // matches consumer's version (0)
		EventID:                   ev.ID,
		EventType:                 string(ev.Type),
		Consumers:                 map[string]PerConsumerStatus{"test-consumer": ConsumerPending},
	})

	if err := d.Recover(context.Background()); err != nil {
		t.Fatalf("Recover should succeed on version match: %v", err)
	}
}

func TestSchemaVersion_MismatchFailsStartup(t *testing.T) {
	ev := makeTestEvent(t, "alice", "p1")
	dag := &stubDAG{
		tips:   []event.EventID{"tip-1"},
		events: map[event.EventID]*event.Event{ev.ID: ev},
	}
	d, store := newTestDispatcherWithDAG(t, dag)

	c := newSyntheticConsumer("test-consumer")
	_ = d.Register(c) // consumer declares PrerequisiteSchemaVersion = 0

	key, _ := AdmissionKey(ev)
	_ = store.PutAdmission(key, &AdmissionRecord{
		SchemaVersion:             1,
		Key:                       key,
		State:                     StateProcessing,
		PrerequisiteSchemaVersion: 1, // MISMATCH with consumer's 0
		EventID:                   ev.ID,
		EventType:                 string(ev.Type),
		Consumers:                 map[string]PerConsumerStatus{"test-consumer": ConsumerPending},
	})

	err := d.Recover(context.Background())
	if err == nil {
		t.Fatal("expected Recover to fail on schema version mismatch")
	}
	if !containsStr(err.Error(), "schema version mismatch") {
		t.Errorf("error should mention schema version mismatch: %v", err)
	}
	if !containsStr(err.Error(), "No canonical ledger rollback is implied") {
		t.Errorf("error should include no-rollback language: %v", err)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
