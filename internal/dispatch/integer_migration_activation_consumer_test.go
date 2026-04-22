package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/dispatch/conformance"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// stubActivator records SetShadowMode calls for assertion.
type stubActivator struct {
	mu    sync.Mutex
	calls []bool
}

func (s *stubActivator) SetShadowMode(shadow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, shadow)
}

func (s *stubActivator) Calls() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bool, len(s.calls))
	copy(out, s.calls)
	return out
}

// stubMigrationStore is an in-memory MigrationStateStore. Optional
// injected errors test the failure paths.
type stubMigrationStore struct {
	mu             sync.Mutex
	activated      bool
	eventID        event.EventID
	emittedAtUnix  int64
	putErr         error
	putCalls       int
	getErrOverride error
}

func (s *stubMigrationStore) PutIntegerMigrationActivated(id event.EventID, ts int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putCalls++
	if s.putErr != nil {
		return s.putErr
	}
	s.activated = true
	s.eventID = id
	s.emittedAtUnix = ts
	return nil
}

func (s *stubMigrationStore) GetIntegerMigrationActivated() (bool, event.EventID, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErrOverride != nil {
		return false, "", 0, s.getErrOverride
	}
	return s.activated, s.eventID, s.emittedAtUnix, nil
}

// makeActivationEvent builds a canonical IntegerMigrationActivation event
// for test use.
func makeActivationEvent(t *testing.T, eventID event.EventID, reason string, emittedAt int64) *event.Event {
	t.Helper()
	ev, err := event.New(
		event.EventTypeIntegerMigrationActivation,
		nil,
		event.IntegerMigrationActivationPayload{
			Version:          1,
			EmittingAgent:    "operator-1",
			ActivationReason: reason,
			EmittedAtUnix:    emittedAt,
		},
		"operator-1",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct activation event: %v", err)
	}
	if eventID != "" {
		ev.ID = eventID
	}
	return ev
}

func newConsumer() (*dispatch.IntegerMigrationActivationConsumer, *stubActivator, *stubActivator, *stubMigrationStore) {
	settler := &stubActivator{}
	gen := &stubActivator{}
	store := &stubMigrationStore{}
	c := dispatch.NewIntegerMigrationActivationConsumer(settler, gen, store)
	return c, settler, gen, store
}

func TestActivationConsumer_Name(t *testing.T) {
	c, _, _, _ := newConsumer()
	if c.Name() != "integer_migration_activation" {
		t.Errorf("Name() = %q", c.Name())
	}
}

func TestActivationConsumer_Type(t *testing.T) {
	c, _, _, _ := newConsumer()
	if c.Type() != dispatch.TypeA {
		t.Errorf("Type() = %v want TypeA", c.Type())
	}
}

func TestActivationConsumer_Interested(t *testing.T) {
	c, _, _, _ := newConsumer()
	ev := makeActivationEvent(t, "act-1", "test", 100)
	if !c.Interested(ev) {
		t.Error("should be interested in EventTypeIntegerMigrationActivation")
	}
	other, _ := event.New(event.EventTypeTransfer, nil, event.TransferPayload{
		FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET",
	}, "a", nil, 0)
	if c.Interested(other) {
		t.Error("should NOT be interested in Transfer events")
	}
}

func TestActivationConsumer_Prerequisites_Nil(t *testing.T) {
	c, _, _, _ := newConsumer()
	ev := makeActivationEvent(t, "act-p", "test", 1)
	if prereqs := c.Prerequisites(ev); prereqs != nil {
		t.Errorf("Prerequisites() = %v want nil", prereqs)
	}
}

func TestActivationConsumer_PrerequisiteSchemaVersion(t *testing.T) {
	c, _, _, _ := newConsumer()
	if got := c.PrerequisiteSchemaVersion(); got != 1 {
		t.Errorf("PrerequisiteSchemaVersion() = %d want 1", got)
	}
}

func TestActivationConsumer_Apply_FlipsFlags(t *testing.T) {
	c, settler, gen, _ := newConsumer()
	ev := makeActivationEvent(t, "act-flip", "observation complete", 1234)
	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if calls := settler.Calls(); len(calls) != 1 || calls[0] != false {
		t.Errorf("settler SetShadowMode calls = %v; want [false]", calls)
	}
	if calls := gen.Calls(); len(calls) != 1 || calls[0] != false {
		t.Errorf("genLedger SetShadowMode calls = %v; want [false]", calls)
	}
}

func TestActivationConsumer_Apply_PersistsState(t *testing.T) {
	c, _, _, store := newConsumer()
	ev := makeActivationEvent(t, "act-persist", "reason-x", 9999)
	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	activated, id, ts, err := store.GetIntegerMigrationActivated()
	if err != nil {
		t.Fatalf("GetIntegerMigrationActivated: %v", err)
	}
	if !activated {
		t.Error("store should show activated=true after Apply")
	}
	if id != ev.ID {
		t.Errorf("stored eventID = %q want %q", id, ev.ID)
	}
	if ts != 9999 {
		t.Errorf("stored emittedAtUnix = %d want 9999", ts)
	}
	if store.putCalls != 1 {
		t.Errorf("PutIntegerMigrationActivated called %d times; want 1", store.putCalls)
	}
}

func TestActivationConsumer_Apply_Idempotent(t *testing.T) {
	c, settler, gen, store := newConsumer()
	ev := makeActivationEvent(t, "act-idem", "first call", 1)
	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// Second Apply with the same event must be a no-op: no additional
	// Put calls, no additional SetShadowMode calls.
	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if store.putCalls != 1 {
		t.Errorf("idempotent path called Put %d times; want 1", store.putCalls)
	}
	if len(settler.Calls()) != 1 {
		t.Errorf("idempotent path flipped settler %d times; want 1", len(settler.Calls()))
	}
	if len(gen.Calls()) != 1 {
		t.Errorf("idempotent path flipped genLedger %d times; want 1", len(gen.Calls()))
	}
}

func TestActivationConsumer_Apply_PersistFailure_PreservesFlags(t *testing.T) {
	c, settler, gen, store := newConsumer()
	store.putErr = errors.New("disk full")
	ev := makeActivationEvent(t, "act-fail", "test", 1)
	err := c.Apply(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error when Put fails")
	}
	if !strings.Contains(err.Error(), "persist") {
		t.Errorf("error = %q; want it to mention persist", err)
	}
	if len(settler.Calls()) != 0 {
		t.Errorf("settler flags mutated despite persist failure: %v", settler.Calls())
	}
	if len(gen.Calls()) != 0 {
		t.Errorf("genLedger flags mutated despite persist failure: %v", gen.Calls())
	}
}

func TestActivationConsumer_Apply_MalformedPayload_ReturnsError(t *testing.T) {
	c, _, _, _ := newConsumer()
	ev := &event.Event{
		ID:      "act-bad",
		Type:    event.EventTypeIntegerMigrationActivation,
		Payload: []byte("not valid json"),
	}
	if err := c.Apply(context.Background(), ev); err == nil {
		t.Error("expected error on malformed payload; got nil")
	}
}

func TestActivationConsumer_RecoveryProbe_MatchesStoredEvent(t *testing.T) {
	c, _, _, _ := newConsumer()
	ev := makeActivationEvent(t, "act-probe", "test", 1)
	if err := c.Apply(context.Background(), ev); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	status, err := c.RecoveryProbe(context.Background(), ev)
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryCompleted {
		t.Errorf("probe on matching event = %v; want RecoveryCompleted", status)
	}

	other := makeActivationEvent(t, "act-other", "test", 1)
	status, err = c.RecoveryProbe(context.Background(), other)
	if err != nil {
		t.Fatalf("RecoveryProbe other: %v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("probe on non-matching event = %v; want RecoveryNotStarted", status)
	}
}

func TestActivationConsumer_RecoveryProbe_NotActivated(t *testing.T) {
	c, _, _, _ := newConsumer()
	ev := makeActivationEvent(t, "act-never", "test", 1)
	status, err := c.RecoveryProbe(context.Background(), ev)
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("probe on never-activated = %v; want RecoveryNotStarted", status)
	}
}

func TestActivationConsumer_RecoveryProbe_StoreError_ReturnsNotStarted(t *testing.T) {
	c, _, _, store := newConsumer()
	store.getErrOverride = errors.New("store failure")
	ev := makeActivationEvent(t, "act-err", "test", 1)
	status, err := c.RecoveryProbe(context.Background(), ev)
	if err != nil {
		t.Errorf("RecoveryProbe should swallow store error and return NotStarted; got err=%v", err)
	}
	if status != dispatch.RecoveryNotStarted {
		t.Errorf("probe on store error = %v; want RecoveryNotStarted", status)
	}
}

// TestActivationConsumer_Conformance exercises the consumer against the
// shared Type A conformance suite.
func TestActivationConsumer_Conformance(t *testing.T) {
	conformance.RunTypeAConformance(t, func() (dispatch.Consumer, func()) {
		c := dispatch.NewIntegerMigrationActivationConsumer(
			&stubActivator{}, &stubActivator{}, &stubMigrationStore{})
		return c, func() {}
	})
}
