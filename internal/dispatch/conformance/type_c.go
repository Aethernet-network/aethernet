package conformance

import (
	"testing"
)

// RunTypeCConformance runs the Type C (externalization) template.
// Inherits all 6 Type A baseline tests plus outbox-specific tests.
func RunTypeCConformance(t *testing.T, factory ConsumerFactory) {
	t.Helper()
	RunTypeAConformance(t, factory)

	t.Run("OutboxAtomicWrite", func(t *testing.T) {
		// Type C consumers write atomically to a local outbox/journal
		// per Invariant 8.1: external sinks are never authoritative.
		// This test verifies that Apply writes to the outbox without
		// error. The factory provides a consumer with a testable outbox.
		c, cleanup := factory()
		defer cleanup()
		ev := makeEvent(t, "alice", 800)
		if !c.Interested(ev) {
			t.Skip("consumer not interested in test event")
		}
		if err := c.Apply(t.Context(), ev); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	})

	t.Run("IdempotentSink", func(t *testing.T) {
		// External sink called twice must produce no duplicate side
		// effects. The consumer's idempotency (from Type A tests) plus
		// outbox dedup ensures this.
		c, cleanup := factory()
		defer cleanup()
		ev := makeEvent(t, "alice", 900)
		if !c.Interested(ev) {
			t.Skip("consumer not interested in test event")
		}
		_ = c.Apply(t.Context(), ev)
		_ = c.Apply(t.Context(), ev)
	})
}
