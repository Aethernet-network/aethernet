package conformance

import (
	"context"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
)

// RunTypeAConformance runs the 6 baseline tests for Type A (single-event
// projection) consumers. Every consumer type inherits these tests.
func RunTypeAConformance(t *testing.T, factory ConsumerFactory) {
	t.Helper()

	t.Run("DuplicateLiveDelivery", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		if !c.Interested(makeEvent(t, "alice", 100)) {
			t.Skip("consumer not interested in test event")
		}
		ev := makeEvent(t, "alice", 100)
		ctx := context.Background()
		if err := c.Apply(ctx, ev); err != nil {
			t.Fatalf("first Apply: %v", err)
		}
		if err := c.Apply(ctx, ev); err != nil {
			t.Fatalf("second Apply (idempotent): %v", err)
		}
	})

	t.Run("ReplayDelivery", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		ev := makeEvent(t, "alice", 200)
		if !c.Interested(ev) {
			t.Skip("consumer not interested in test event")
		}
		ctx := context.Background()
		if err := c.Apply(ctx, ev); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		// Replay: second apply must be idempotent.
		if err := c.Apply(ctx, ev); err != nil {
			t.Fatalf("replay Apply: %v", err)
		}
	})

	t.Run("CrashRecovery", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		ev := makeEvent(t, "alice", 300)
		if !c.Interested(ev) {
			t.Skip("consumer not interested in test event")
		}
		ctx := context.Background()
		if err := c.Apply(ctx, ev); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		status, err := c.RecoveryProbe(ctx, ev)
		if err != nil {
			t.Fatalf("RecoveryProbe: %v", err)
		}
		if status != dispatch.RecoveryCompleted {
			t.Errorf("after successful Apply, probe should return Completed; got %v", status)
		}
	})

	t.Run("ConcurrentSameEvent", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		ev := makeEvent(t, "alice", 400)
		if !c.Interested(ev) {
			t.Skip("consumer not interested in test event")
		}
		ctx := context.Background()
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				errs[idx] = c.Apply(ctx, ev)
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("concurrent Apply[%d]: %v", i, err)
			}
		}
	})

	t.Run("ContentHashDiscrimination", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		ev1 := makeEvent(t, "alice", 500)
		ev2 := makeEvent(t, "charlie", 600)
		if !c.Interested(ev1) || !c.Interested(ev2) {
			t.Skip("consumer not interested in test events")
		}
		ctx := context.Background()
		if err := c.Apply(ctx, ev1); err != nil {
			t.Fatalf("Apply ev1: %v", err)
		}
		if err := c.Apply(ctx, ev2); err != nil {
			t.Fatalf("Apply ev2: %v", err)
		}
	})

	t.Run("CausalPrerequisiteDeferral", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		ev := makeEvent(t, "alice", 700)
		prereqs := c.Prerequisites(ev)
		// Part D: consumers may return non-empty Prerequisites.
		// The dispatcher handles deferral; the consumer declares which
		// events it depends on. This test documents that Prerequisites
		// is callable and returns a valid (possibly nil) slice.
		if prereqs != nil {
			for _, p := range prereqs {
				if p == "" {
					t.Error("Prerequisites returned empty EventID")
				}
			}
		}
	})

	t.Run("PrerequisiteSchemaVersion", func(t *testing.T) {
		c, cleanup := factory()
		defer cleanup()
		// PrerequisiteSchemaVersion must return a valid uint32 without
		// panicking. The actual value is consumer-specific.
		_ = c.PrerequisiteSchemaVersion()
	})
}
