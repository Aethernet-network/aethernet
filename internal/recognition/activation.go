package recognition

import (
	"context"
	"log/slog"
)

// Activator manages targeted activation of deferred recognition items.
// When a prerequisite is satisfied, Signal(prereqKey) wakes all consumers
// waiting on that key and retries their Consume path.
type Activator struct {
	index *Index
	bus   *Bus
}

// NewActivator creates an Activator wired to the given index and bus.
func NewActivator(index *Index, bus *Bus) *Activator {
	return &Activator{index: index, bus: bus}
}

// Signal notifies all deferred consumers waiting on the given prerequisite
// key that it has been satisfied. Each matching consumer is retried through
// the bus's consume path.
func (a *Activator) Signal(ctx context.Context, prereqKey string) {
	deferred := a.index.DeferredByPrerequisite(prereqKey)
	if len(deferred) == 0 {
		return
	}

	slog.Info("recognition: activation signal",
		"prerequisite_key", prereqKey,
		"deferred_count", len(deferred),
	)

	for _, state := range deferred {
		consumerName := state.ConsumerName
		eventID := state.EventID

		consumer := a.bus.getConsumer(consumerName)
		if consumer == nil {
			continue
		}

		ev := a.bus.getEvent(eventID)
		if ev == nil {
			continue
		}

		// Re-check readiness.
		ready, newPrereq, err := consumer.Ready(ctx, ev, a.bus.readModel)
		if err != nil {
			slog.Warn("recognition: activation ready check failed",
				"consumer", consumerName,
				"event_id", eventID,
				"err", err,
			)
			continue
		}

		if !ready {
			// Still not ready — update prerequisite if changed.
			if newPrereq != state.PrerequisiteKey {
				_ = a.index.SetDeferred(consumerName, eventID, state.DeferredReason, newPrereq)
			}
			continue
		}

		// Ready now — consume.
		if err := consumer.Consume(ctx, ev); err != nil {
			slog.Warn("recognition: activation consume failed",
				"consumer", consumerName,
				"event_id", eventID,
				"err", err,
			)
			continue
		}

		_ = a.index.MarkReady(consumerName, eventID)
		slog.Info("recognition: deferred item activated",
			"consumer", consumerName,
			"event_id", eventID,
			"prerequisite_key", prereqKey,
		)
	}
}
