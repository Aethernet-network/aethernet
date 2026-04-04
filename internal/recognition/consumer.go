package recognition

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ReadModel provides the minimal read interface that consumers need to check
// prerequisites without introducing package cycles. It is deliberately narrow:
// consumers that need richer queries should define their own interfaces and
// have them injected at wiring time.
type ReadModel interface {
	// HasEvent reports whether the given event ID exists in the local DAG.
	HasEvent(id event.EventID) bool

	// GetEvent retrieves an event by ID. Returns nil if not found.
	GetEvent(id event.EventID) *event.Event
}

// CommitConsumer is the interface implemented by subsystems that need to react
// to DAG commit events. Each consumer is registered with the Commit Bus and
// called for every committed event that passes its Interested filter.
//
// Contract:
//   - Interested is called synchronously during dispatch to filter events.
//     It must be fast and side-effect free.
//   - Ready is called to check whether prerequisites for domain action are
//     met. Return (true, "", nil) when ready; (false, reason, nil) to defer.
//   - Consume performs the domain action. It MUST be idempotent — the bus
//     may call Consume more than once for the same (consumer, event) pair
//     (e.g., on restart replay).
//   - Consume must not hold the DAG mutex or perform unbounded I/O.
type CommitConsumer interface {
	// Name returns a unique, stable identifier for this consumer.
	// Used as the key prefix in the Recognition Index.
	Name() string

	// Interested reports whether this consumer cares about the event.
	// Must be fast and allocation-free on the hot path.
	Interested(ev *event.Event) bool

	// Ready checks whether all prerequisites for consuming this event are
	// met. Returns (true, "", nil) when ready to consume. Returns
	// (false, reason, nil) to defer — the reason is stored in
	// RecognitionState.DeferredReason and the event will be retried when
	// the prerequisite is signaled.
	//
	// The prerequisiteKey (second return) is stored so that targeted
	// activation can wake this specific deferred item later.
	Ready(ctx context.Context, ev *event.Event, view ReadModel) (ready bool, prerequisiteKey string, err error)

	// Consume performs the domain action for the event. Must be idempotent.
	// The event is guaranteed to be in the DAG when Consume is called.
	Consume(ctx context.Context, ev *event.Event) error
}
