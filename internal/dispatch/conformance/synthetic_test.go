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
