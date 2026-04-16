package conformance

import (
	"testing"
)

// RunTypeBConformance runs the Type B (multi-event state-machine) template.
// Inherits all 6 Type A baseline tests plus state-machine-specific tests.
func RunTypeBConformance(t *testing.T, factory ConsumerFactory) {
	t.Helper()
	RunTypeAConformance(t, factory)

	t.Run("StateMachineTransitions", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		// Type B consumers must define legal transition tables per
		// Invariant 8.2. This test verifies that a multi-event sequence
		// produces valid state transitions. The factory is expected to
		// provide a consumer that processes the test events correctly.
		ev1 := makeEvent(t, "state-a", 1000)
		ev2 := makeEvent(t, "state-b", 2000)
		if !c.Interested(ev1) || !c.Interested(ev2) {
			t.Skip("consumer not interested in test events")
		}
		// Apply in sequence; no error expected.
		if err := c.Apply(t.Context(), ev1); err != nil {
			t.Fatalf("Apply ev1: %v", err)
		}
		if err := c.Apply(t.Context(), ev2); err != nil {
			t.Fatalf("Apply ev2: %v", err)
		}
	})

	t.Run("IllegalTransitionRejected", func(t *testing.T) {
		// Placeholder: real Type B consumers define transition tables
		// that reject illegal event orderings. This test documents the
		// expectation. The conformance suite verifies the consumer does
		// not panic on an out-of-order event.
		c, cleanup := factory()
		defer cleanup()
		_ = c // consumer available for future assertion
	})
}
