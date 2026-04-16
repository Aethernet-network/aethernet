package conformance

import (
	"testing"
)

// RunTypeDConformance runs the Type D (deadline/deferred) template.
// Inherits all 6 Type A baseline tests plus deadline-specific tests.
func RunTypeDConformance(t *testing.T, factory ConsumerFactory) {
	t.Helper()
	RunTypeAConformance(t, factory)

	t.Run("DeadlineBasisCanonical", func(t *testing.T) {
		// Type D consumers record canonical deadline basis in admission
		// state; timers are not canonical truth. This test verifies that
		// Apply does not panic and that the consumer processes a deadline-
		// relevant event.
		c, cleanup := factory()
		defer cleanup()
		ev := makeEvent(t, "alice", 1100)
		if !c.Interested(ev) {
			t.Skip("consumer not interested in test event")
		}
		if err := c.Apply(t.Context(), ev); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	})
}
