package recognition

import (
	"context"
	"errors"

	"github.com/Aethernet-network/aethernet/internal/dag"
)

// ReplayHistoricalToBusConsumers walks the in-memory DAG in topological
// order and emits each event through the recognition bus with
// CommitSource=SourceReplay, Replay=true. Per-consumer MarkRecognizedOnce
// (in Bus.processConsumer) ensures each consumer observes every interested
// event exactly once even across re-runs.
//
// Called once at startup AFTER SetOnCommit + commitBus.Start() are wired
// and BEFORE network ingestion begins. Closes the LoadFromStore-before-
// SetOnCommit gap identified in F4 plan §8.1: events committed during
// LoadFromStore fire onCommit while it is still nil, so bus consumers
// never observe historical events without this replay pass.
//
// # Signature contract
//
// The function takes a *dag.DAG and a *Bus directly, not an emit-callback,
// because both objects already exist at the call site (cmd/node/main.go
// after the Bus is constructed and SetOnCommit is wired). The replay pass
// is exclusively a recognition-package concern: it knows the bus's Emit
// contract, knows the SourceReplay tag is the correct CommitSource, and
// knows that per-consumer idempotency is provided by the Index's
// MarkRecognizedOnce gate. No abstraction layer adds value here.
//
// The DAG is read via TopologicalSort() so that, for any consumer that
// gates Ready() on a prerequisite event being projected, the prerequisite
// is emitted before its dependents — matching the live ordering the
// fabric provides for forward inserts.
//
// # Invariant
//
// After this function returns without error, every registered bus
// consumer has observed every DAG event matching its Interested() filter
// at least once. "Observed" means the bus has called MarkRecognizedOnce
// → (if first) Ready → (if ready) Consume for the (consumer, event) pair.
//
// # Idempotency
//
// Safe to call multiple times. The Index's MarkRecognizedOnce returns
// false for already-recognized (consumer, event) pairs, short-circuiting
// the bus's processConsumer before re-invoking Consume. Re-runs after a
// crash or during testing are no-ops for already-handled events.
//
// # Backpressure
//
// Bus.Emit is non-blocking and returns ErrQueueFull when the dispatch
// queue is at capacity. The replay pass treats ErrQueueFull as a fatal
// startup error: a healthy startup has the queue drained by the worker
// pool faster than the replay can fill it (the bus is started before this
// runs). If the queue fills, something is wrong upstream and silently
// dropping historical events would re-introduce the bug this function
// closes. Other Emit errors are also returned as fatal.
//
// # Current state: STUB
//
// Returns nil without emitting anything. The synthetic replay-conformance
// test (internal/dispatch/conformance/replay_path_test.go) intentionally
// fails against this stub. F4A step 3 (plan §8.1) implements the body and
// the test flips RED → GREEN. The captured RED failure baseline is in
// internal/dispatch/conformance/testdata/replay_template_red_baseline.txt.
func ReplayHistoricalToBusConsumers(ctx context.Context, d *dag.DAG, bus *Bus) error {
	_ = ctx
	_ = d
	_ = bus
	return nil // STUB — F4A step 3 implements the walk + Emit loop.
}

// ErrReplayNotImplemented is a sentinel returned by tests that want to
// assert the SUT body has not yet landed. It is not currently returned
// by ReplayHistoricalToBusConsumers (the stub returns nil so existing
// startup code paths are unaffected); the test inspects observable
// post-conditions rather than this error to detect the RED state.
//
// Removed in F4A step 3 along with this stub's no-op body.
var ErrReplayNotImplemented = errors.New("recognition: ReplayHistoricalToBusConsumers stub — pending F4A step 3")
