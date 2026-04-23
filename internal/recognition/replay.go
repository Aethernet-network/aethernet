package recognition

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
// queue is at capacity. The replay pass treats ErrQueueFull as a
// retryable condition: it spins on the same event with a short backoff
// until the worker pool drains the queue or the context is cancelled.
// Silently dropping historical events would re-introduce the bug this
// function closes; failing fast on transient backpressure would make
// startup brittle on large DAGs. The retry loop is bounded by the
// caller's context — a startup that cannot drain its own bus within
// the context deadline is correctly treated as a fatal startup error.
func ReplayHistoricalToBusConsumers(ctx context.Context, d *dag.DAG, bus *Bus) error {
	if d == nil {
		return fmt.Errorf("recognition: replay: nil dag")
	}
	if bus == nil {
		return fmt.Errorf("recognition: replay: nil bus")
	}

	events, err := d.TopologicalSort()
	if err != nil {
		return fmt.Errorf("recognition: replay: topological sort: %w", err)
	}

	const backpressureBackoff = 5 * time.Millisecond
	now := time.Now()

	emitted := 0
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("recognition: replay: cancelled after %d/%d events: %w",
				emitted, len(events), err)
		}

		record := CommitRecord{
			EventID:     ev.ID,
			EventType:   ev.Type,
			Source:      SourceReplay,
			Replay:      true,
			CommittedAt: now,
		}

		for {
			emitErr := bus.Emit(record, ev)
			if emitErr == nil {
				break
			}
			if emitErr != ErrQueueFull {
				return fmt.Errorf("recognition: replay: emit %s: %w", ev.ID, emitErr)
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("recognition: replay: cancelled while waiting for bus drain at %d/%d: %w",
					emitted, len(events), ctx.Err())
			case <-time.After(backpressureBackoff):
			}
		}
		emitted++
	}

	slog.Info("recognition: replay complete",
		"events_emitted", emitted,
		"consumers", bus.ConsumerCount(),
	)
	return nil
}
