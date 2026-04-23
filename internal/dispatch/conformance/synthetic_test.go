package conformance_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/dispatch/conformance"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// syntheticTypeA is a minimal Type A consumer backed by an in-memory map.
type syntheticTypeA struct {
	mu      sync.Mutex
	applied map[event.EventID]bool
}

func (c *syntheticTypeA) Name() string         { return "synthetic-type-a" }
func (c *syntheticTypeA) Type() dispatch.ConsumerType { return dispatch.TypeA }
func (c *syntheticTypeA) Interested(_ *event.Event) bool { return true }
func (c *syntheticTypeA) Prerequisites(_ *event.Event) []event.EventID { return nil }
func (c *syntheticTypeA) PrerequisiteSchemaVersion() uint32 { return 0 }

func (c *syntheticTypeA) Apply(_ context.Context, ev *event.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applied[ev.ID] = true
	return nil
}

func (c *syntheticTypeA) RecoveryProbe(_ context.Context, ev *event.Event) (dispatch.RecoveryStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.applied[ev.ID] {
		return dispatch.RecoveryCompleted, nil
	}
	return dispatch.RecoveryNotStarted, nil
}

func TestTypeA_SyntheticConsumer(t *testing.T) {
	conformance.RunTypeAConformance(t, func() (dispatch.Consumer, func()) {
		c := &syntheticTypeA{applied: make(map[event.EventID]bool)}
		return c, func() {}
	})
}

// syntheticTypeB is a minimal Type B consumer.
type syntheticTypeB struct{ syntheticTypeA }

func (c *syntheticTypeB) Name() string         { return "synthetic-type-b" }
func (c *syntheticTypeB) Type() dispatch.ConsumerType { return dispatch.TypeB }

func TestTypeB_SyntheticConsumer(t *testing.T) {
	conformance.RunTypeBConformance(t, func() (dispatch.Consumer, func()) {
		c := &syntheticTypeB{syntheticTypeA{applied: make(map[event.EventID]bool)}}
		return c, func() {}
	})
}

// syntheticTypeC is a minimal Type C consumer with an in-memory outbox.
type syntheticTypeC struct {
	syntheticTypeA
	outbox []event.EventID
}

func (c *syntheticTypeC) Name() string         { return "synthetic-type-c" }
func (c *syntheticTypeC) Type() dispatch.ConsumerType { return dispatch.TypeC }

func (c *syntheticTypeC) Apply(ctx context.Context, ev *event.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.applied[ev.ID] {
		c.outbox = append(c.outbox, ev.ID)
	}
	c.applied[ev.ID] = true
	return nil
}

func TestTypeC_SyntheticConsumer(t *testing.T) {
	conformance.RunTypeCConformance(t, func() (dispatch.Consumer, func()) {
		c := &syntheticTypeC{syntheticTypeA: syntheticTypeA{applied: make(map[event.EventID]bool)}}
		return c, func() {}
	})
}

// syntheticTypeD is a minimal Type D consumer.
type syntheticTypeD struct{ syntheticTypeA }

func (c *syntheticTypeD) Name() string         { return "synthetic-type-d" }
func (c *syntheticTypeD) Type() dispatch.ConsumerType { return dispatch.TypeD }

func TestTypeD_SyntheticConsumer(t *testing.T) {
	conformance.RunTypeDConformance(t, func() (dispatch.Consumer, func()) {
		c := &syntheticTypeD{syntheticTypeA{applied: make(map[event.EventID]bool)}}
		return c, func() {}
	})
}

// --- Synthetic LogicalKeyConsumer for F4B Type E conformance ---------------

// syntheticTypeE is a minimal LogicalKeyConsumer (Type E) backed by an
// in-memory map. Used to exercise RunLogicalKeyConformance without
// requiring a production logical-key consumer to land first.
//
// Behavior:
//   - Interested for every event (the suite filters to baseline events
//     it constructs itself).
//   - Key projects the event's FromAgent field as the LogicalKey, so
//     two events from the same agent share a key (validates the per-
//     key Apply guarantee under the harness).
//   - IsComplete always returns true (the conformance suite expects to
//     reach Apply on baseline events; per-package wrappers populate
//     state for non-trivially-complete consumers).
//   - DeriveOutcome returns a constant Outcome.
//   - Apply records the (key, outcome) tuple in an in-memory map.
type syntheticTypeE struct {
	mu      sync.Mutex
	applied map[dispatch.LogicalKey]int
	last    dispatch.Outcome
}

func (c *syntheticTypeE) Name() string                    { return "synthetic-type-e" }
func (c *syntheticTypeE) Interested(_ *event.Event) bool  { return true }

func (c *syntheticTypeE) Key(ev *event.Event) (dispatch.LogicalKey, error) {
	// Project the FromAgent field of TransferPayload as the logical key.
	// Falls back to ev.ID for any non-Transfer event.
	if ev.Type != event.EventTypeTransfer {
		return dispatch.LogicalKey(ev.ID), nil
	}
	payload, err := event.GetPayload[event.TransferPayload](ev)
	if err != nil {
		return dispatch.LogicalKey(ev.ID), nil
	}
	return dispatch.LogicalKey(payload.FromAgent), nil
}

func (c *syntheticTypeE) RoundState(_ context.Context, k dispatch.LogicalKey) (dispatch.RoundState, error) {
	return dispatch.RoundState{LogicalKey: k}, nil
}

func (c *syntheticTypeE) IsComplete(_ dispatch.RoundState) (bool, error) {
	return true, nil
}

func (c *syntheticTypeE) DeriveOutcome(_ dispatch.RoundState) (dispatch.Outcome, error) {
	return dispatch.Outcome{Verdict: dispatch.VerdictAccept, ScoreBP: 7777}, nil
}

func (c *syntheticTypeE) Apply(_ context.Context, key dispatch.LogicalKey, outcome dispatch.Outcome) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applied[key]++
	c.last = outcome
	return nil
}

func (c *syntheticTypeE) RecoveryProbe(_ context.Context, key dispatch.LogicalKey) (dispatch.RecoveryStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.applied[key] > 0 {
		return dispatch.RecoveryCompleted, nil
	}
	return dispatch.RecoveryNotStarted, nil
}

func TestTypeE_SyntheticConsumer(t *testing.T) {
	conformance.RunLogicalKeyConformance(t, func() (dispatch.LogicalKeyConsumer, func()) {
		c := &syntheticTypeE{applied: make(map[dispatch.LogicalKey]int)}
		return c, func() {}
	})
}
