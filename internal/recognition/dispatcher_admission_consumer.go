package recognition

import (
	"context"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// DispatcherAdmitter is the subset of dispatch.Dispatcher this consumer
// calls. Defined locally so the recognition package does not import the
// dispatch package. (Relocated from task_verification_consensus_consumer.go
// as part of Part E.1 — it now serves the general admission router rather
// than a single per-event-type forwarder.)
type DispatcherAdmitter interface {
	Admit(ctx context.Context, ev *event.Event) error
}

// DispatcherAdmissionConsumer is a recognition-layer CommitConsumer that
// forwards every committed event to the dispatcher's admission surface.
// Per-event-type routing is the dispatcher's responsibility via each
// dispatch.Consumer's Interested() method; this consumer deliberately
// does not filter by event type.
//
// Closes the bug class surfaced by Part F Phase D. Prior to Part E.1,
// recognition→dispatcher forwarding was hand-written per event type and
// lived inside TaskVerificationConsensusConsumer.Consume (hard-coded to
// TaskVerificationConsensus events). Canonical event types with
// dispatcher consumers but no recognition-layer responsibility — for
// example EventTypeIntegerMigrationActivation — had no admission
// pathway at all, so their dispatcher consumers' Apply methods never
// ran even after the event landed in every node's DAG.
//
// Design rules:
//
//   - No event-type filter. Filtering is the dispatcher's job.
//   - No Ready-gate. Ready always returns (true, "", nil). The dispatcher
//     has its own prerequisite-gating system (C-8); layering the
//     recognition bus's deferral mechanism on top would double-count.
//   - Log-and-swallow on admission errors. Recognition-layer commit
//     processing must not be blocked by dispatcher issues; the
//     dispatcher has its own recovery path (per-consumer RecoveryProbe
//     at startup) that re-attempts admission after a node restart.
//
// Idempotency: dispatch.Dispatcher.Admit is idempotent per
// (event_id, consumer_name) via its admission-state-machine (C-1).
// Re-admission during replay or duplicate commits is safe.
//
// Ordering: the recognition bus invokes the consumers subscribed to a
// single commit sequentially inside one worker, but the order across
// consumers is non-deterministic (map iteration). This is correctness-
// safe here because the admission router's downstream work (dispatch
// consumers reading the event payload and replay-safe ledger state) is
// independent of every other recognition consumer's local state
// mutations.
type DispatcherAdmissionConsumer struct {
	dispatcher DispatcherAdmitter
}

// NewDispatcherAdmissionConsumer constructs the router. The dispatcher
// reference is required.
func NewDispatcherAdmissionConsumer(d DispatcherAdmitter) *DispatcherAdmissionConsumer {
	return &DispatcherAdmissionConsumer{dispatcher: d}
}

// Name returns the unique consumer identifier used by the Recognition
// Index as a key prefix.
func (c *DispatcherAdmissionConsumer) Name() string {
	return "dispatcher_admission_router"
}

// Interested returns true for every event. Per-type routing happens
// inside the dispatcher via dispatch.Consumer.Interested.
func (c *DispatcherAdmissionConsumer) Interested(_ *event.Event) bool {
	return true
}

// Ready is always true — the router does not participate in the
// recognition bus's deferral mechanism. The dispatcher performs its
// own prerequisite checks before invoking consumers' Apply methods.
func (c *DispatcherAdmissionConsumer) Ready(_ context.Context, _ *event.Event, _ ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume forwards the event to dispatcher.Admit. Admission errors are
// logged at WARN but not propagated to the caller: the recognition bus
// must not block on dispatcher issues, and dispatch.Dispatcher.Recover
// re-attempts non-terminal records on the next node restart.
func (c *DispatcherAdmissionConsumer) Consume(ctx context.Context, ev *event.Event) error {
	if err := c.dispatcher.Admit(ctx, ev); err != nil {
		slog.Warn("dispatcher_admission_router: admit failed",
			"event_id", ev.ID,
			"event_type", ev.Type,
			"err", err,
		)
	}
	return nil
}

// Compile-time assertion that DispatcherAdmissionConsumer satisfies
// CommitConsumer.
var _ CommitConsumer = (*DispatcherAdmissionConsumer)(nil)
