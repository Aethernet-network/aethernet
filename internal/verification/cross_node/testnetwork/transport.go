// Package testnetwork provides the deterministic in-process event
// transport used by the cross-node byte-equality harness.
//
// In production the recognition fabric → dispatcher → consumer chain is
// driven by fastpath / TCP / per-node arrival timing. Reproducing the
// selection-race bug requires the OPPOSITE of that — a transport with
// no timing variance, where the test author specifies the exact order
// each node receives each event. This package exposes the smallest
// possible interface to do that: hand a node and an event, the
// transport calls the node's TVConsensus consumer Apply directly.
//
// Why a separate package: keeping the transport out of the harness's
// main package documents the strict separation between "the system
// under test" (cross_node.Cluster + Node + Consumer) and "the test
// driver" (testnetwork.Transport). Production code never imports
// testnetwork.
package testnetwork

import (
	"context"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// Applier is the minimal interface the transport needs: a node-shaped
// thing that can run a TVConsensus consumer's Apply. *cross_node.Node
// satisfies this interface via its Apply method.
//
// This interface is the ONLY coupling between testnetwork and the
// cross_node package for the direct-Apply path. We intentionally do not
// import cross_node here to break the obvious circular dependency
// (cross_node tests use testnetwork; testnetwork would otherwise import
// cross_node).
type Applier interface {
	Apply(ctx context.Context, ev *event.Event) error
}

// Admitter is the minimal interface the dispatcher-integrated transport
// path needs: a node-shaped thing that can route an event through
// dispatch.Dispatcher.Admit. *cross_node.Node satisfies this interface
// via its AdmitViaDispatcher method (defined in dispatcher_harness.go).
//
// Same circular-dependency-avoidance rationale as Applier — we keep the
// coupling at the smallest possible interface. Tests that want the
// dispatcher path call DeliverViaDispatcher; tests that want the legacy
// direct-Apply path call DeliverTo / DeliverInOrder.
type Admitter interface {
	AdmitViaDispatcher(ctx context.Context, ev *event.Event) error
}

// Transport is the deterministic delivery driver for a cluster of
// Appliers (nodes). It maintains no state of its own; every method is
// a synchronous direct call into the target node.
type Transport struct{}

// New constructs a transport. There is no configuration; the transport
// is purely a function bag organized as a struct for namespacing.
func New() *Transport {
	return &Transport{}
}

// DeliverTo invokes Apply on a single target node. Returns the
// consumer's error (if any) so the test can assert per-delivery
// behavior — but typically the harness ignores per-Apply errors and
// asserts on the post-delivery cluster snapshot, since the bug
// surfaces in projected state, not in error returns.
func (Transport) DeliverTo(ctx context.Context, target Applier, ev *event.Event) error {
	if target == nil {
		return fmt.Errorf("testnetwork: target Applier is nil")
	}
	if ev == nil {
		return fmt.Errorf("testnetwork: event is nil")
	}
	return target.Apply(ctx, ev)
}

// DeliverInOrder delivers a sequence of events to a single target node
// in the given order. This is the production-equivalent of "events
// arrived on this node's fastpath in this order."
//
// Returns a slice of per-event errors (nil entries indicate success).
// Callers that don't care about per-event errors can ignore the return.
func (t Transport) DeliverInOrder(ctx context.Context, target Applier, evs []*event.Event) []error {
	errs := make([]error, len(evs))
	for i, ev := range evs {
		errs[i] = t.DeliverTo(ctx, target, ev)
	}
	return errs
}

// DeliverToViaDispatcher invokes AdmitViaDispatcher on a single target
// node. Production-equivalent to one canonical event arriving at the
// node's recognition fabric and being dispatched. Returns the
// dispatcher's error (typically nil; the dispatcher reserves errors for
// storage failures, anchor-verification failures, and prerequisite
// forgery — none of which the harness's current corpora can trigger).
func (Transport) DeliverToViaDispatcher(ctx context.Context, target Admitter, ev *event.Event) error {
	if target == nil {
		return fmt.Errorf("testnetwork: target Admitter is nil")
	}
	if ev == nil {
		return fmt.Errorf("testnetwork: event is nil")
	}
	return target.AdmitViaDispatcher(ctx, ev)
}

// DeliverInOrderViaDispatcher delivers a sequence of events to a single
// target node via Dispatcher.Admit, in the given order. The
// dispatcher-integrated parallel of DeliverInOrder.
//
// Returns a slice of per-event errors (nil entries indicate success).
// Mirrors DeliverInOrder's contract: callers that don't care about
// per-event errors can ignore the return.
func (t Transport) DeliverInOrderViaDispatcher(ctx context.Context, target Admitter, evs []*event.Event) []error {
	errs := make([]error, len(evs))
	for i, ev := range evs {
		errs[i] = t.DeliverToViaDispatcher(ctx, target, ev)
	}
	return errs
}

// BroadcastDeterministic delivers a sequence of events to a sequence of
// nodes, where each node receives the events in its own specified
// order. The lengths of nodes and orders must match; orders[i] is a
// permutation over [0, len(evs)).
//
// This is the harness primitive for reproducing the multi-emit
// selection race: by giving different nodes different orderings of the
// same event set, the per-task mutex selects different first-events on
// different nodes.
//
// Delivery is synchronous and serialized per node — node 0 receives all
// its events before node 1 starts. The harness does not rely on
// concurrency for the bug reproduction; the bug is purely about
// per-node ordering, not per-node timing.
func (t Transport) BroadcastDeterministic(
	ctx context.Context,
	nodes []Applier,
	evs []*event.Event,
	orders [][]int,
) error {
	if len(nodes) != len(orders) {
		return fmt.Errorf("testnetwork: nodes (%d) and orders (%d) must have the same length",
			len(nodes), len(orders))
	}
	for nodeIdx, node := range nodes {
		order := orders[nodeIdx]
		if len(order) != len(evs) {
			return fmt.Errorf("testnetwork: orders[%d] length %d != evs length %d",
				nodeIdx, len(order), len(evs))
		}
		ordered := make([]*event.Event, len(evs))
		for i, evIdx := range order {
			if evIdx < 0 || evIdx >= len(evs) {
				return fmt.Errorf("testnetwork: orders[%d][%d] = %d out of range [0, %d)",
					nodeIdx, i, evIdx, len(evs))
			}
			ordered[i] = evs[evIdx]
		}
		t.DeliverInOrder(ctx, node, ordered)
	}
	return nil
}
