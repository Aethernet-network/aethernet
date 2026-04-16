package dispatch

import (
	"context"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// Consumer is the interface that canonical-event consumers must implement
// to register with the CanonicalEventDispatcher. The dispatcher guarantees
// exactly-once successful Apply per (event, consumer) pair (C-1).
//
// Every consumer must satisfy the invariants for its declared Type (§12):
//   - Type A: Apply commits its authoritative local durable state
//     transition within a single BadgerDB transaction (C-11).
//   - Type B: Apply + an explicit state-machine transition table (8.2).
//   - Type C: Apply writes atomically to a local outbox/journal;
//     external sinks are never authoritative (8.1).
//   - Type D: Apply records canonical deadline basis in admission state;
//     timers are not canonical truth.
type Consumer interface {
	// Name returns a unique, stable identifier for this consumer
	// (e.g., "settlement.SettlementConsumer"). Used as the per-consumer
	// key in the admission record and as the registry key.
	Name() string

	// Type returns the consumer's taxonomy category per §12.
	Type() ConsumerType

	// Interested reports whether this consumer should be invoked for ev.
	// Must be fast and allocation-free.
	Interested(ev *event.Event) bool

	// Apply processes a canonical event. Must commit its authoritative
	// local durable state transition atomically per C-11. Must be
	// idempotent: the dispatcher may re-invoke Apply on re-delivery of
	// events in failed-retryable state, but consumers in per-consumer
	// applied state are never re-invoked.
	Apply(ctx context.Context, ev *event.Event) error

	// RecoveryProbe determines whether Apply completed for ev during a
	// prior invocation interrupted by a crash. Per C-14: probes must be
	// evidence-based (return RecoveryCompleted only with positive evidence
	// in durable state), monotonic, and replay-safe. Must not consult
	// wall-clock time, ephemeral memory, or external systems. Absence of
	// evidence is not evidence of absence — return RecoveryNotStarted
	// when uncertain.
	RecoveryProbe(ctx context.Context, ev *event.Event) (RecoveryStatus, error)

	// Prerequisites returns the EventIDs that must be projected locally
	// before this consumer can process ev. Part D implements the actual
	// prerequisite-checking logic; Part C consumers return nil.
	Prerequisites(ev *event.Event) []event.EventID

	// PrerequisiteSchemaVersion returns the version of the prerequisite
	// semantics this consumer uses. Stored in the admission record at
	// reservation time per D-6. Part C consumers return 0.
	PrerequisiteSchemaVersion() uint32
}

// validateConsumer performs structural validation at registration time
// per invariant C-8. Behavioral conformance is enforced in CI via the
// conformance test suite; startup checks structure only.
func validateConsumer(c Consumer) error {
	if c.Name() == "" {
		return fmt.Errorf("dispatch: consumer Name() must be non-empty")
	}
	switch c.Type() {
	case TypeA, TypeB, TypeC, TypeD:
		// valid
	default:
		return fmt.Errorf("dispatch: consumer %q has invalid Type %d", c.Name(), c.Type())
	}
	return nil
}
