package dispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// TestAdmitOneLogicalKey_PerKeyLockEliminatesRace is the deterministic
// regression test for the F5 5B post-#133 LK race fix (Path A).
//
// Pre-fix shape: two concurrent commit-bus workers processing
// byte-distinct events for the same logical key both pass the line-127
// `rec.State == StateApplied` gate (each reading the pre-Applied
// record), both call IsComplete + DeriveOutcome + Apply, and both
// invoke consumer.Apply. Test counter would observe 2 Apply calls.
//
// Post-fix shape: per-(consumer, key) `sync.Map` lock in
// admitOneLogicalKey serializes the read-modify-write region. The
// second goroutine blocks on the lock until the first transitions
// state to StateApplied; on lock acquisition it re-reads the record,
// sees StateApplied, and returns at the gate. Test counter observes 1
// Apply call.
//
// Synthetic consumer's Apply sleeps 50ms to widen the race window
// reliably — without the sleep the race is intermittent (per the
// pre-fix conformance-test flake). Sleep-driven determinism mirrors
// the recordLocks regression test pattern.
func TestAdmitOneLogicalKey_PerKeyLockEliminatesRace(t *testing.T) {
	t.Parallel()

	d, _ := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("race-consumer")
	c.keyFn = func(_ *event.Event) (LogicalKey, error) {
		// All events project to the same logical key — the race
		// surface this test exercises.
		return "shared-key", nil
	}

	// Slow Apply to widen the race window reliably. Without this
	// sleep the race is intermittent; with it, the test is
	// deterministic regardless of the fix's presence (assertions below
	// distinguish fix-applied from race-present outcomes).
	var applyEntered atomic.Int32
	wrappedConsumer := &slowApplyLKConsumer{
		inner:        c,
		applyEntered: &applyEntered,
		applyDelay:   50 * time.Millisecond,
	}

	if err := d.RegisterLogicalKey(wrappedConsumer); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	// Two byte-distinct events that project to the same logical key
	// (different EventID — different agent — but consumer's keyFn
	// extracts the same "shared-key" from both).
	ev1 := makeTestEvent(t, "alice", "first")
	ev2 := makeTestEvent(t, "charlie", "second")
	if ev1.ID == ev2.ID {
		t.Fatalf("test setup: events should have distinct EventIDs")
	}

	// Spawn two goroutines that race on Admit for the same logical key.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = d.Admit(context.Background(), ev1) }()
	go func() { defer wg.Done(); _ = d.Admit(context.Background(), ev2) }()
	wg.Wait()

	// Per-key Apply guarantee: exactly ONE Apply call should fire.
	if got := applyEntered.Load(); got != 1 {
		t.Fatalf("per-key Apply guarantee violated: Apply fired %d times; expected exactly 1 (lock should serialize race)", got)
	}
}

// slowApplyLKConsumer wraps a synthetic LK consumer to add a delay
// inside Apply for deterministic race-window widening. Counts Apply
// invocations via the injected atomic counter.
type slowApplyLKConsumer struct {
	inner        *syntheticLogicalKeyConsumer
	applyEntered *atomic.Int32
	applyDelay   time.Duration
}

func (s *slowApplyLKConsumer) Name() string                    { return s.inner.Name() }
func (s *slowApplyLKConsumer) Interested(ev *event.Event) bool { return s.inner.Interested(ev) }
func (s *slowApplyLKConsumer) Key(ev *event.Event) (LogicalKey, error) {
	return s.inner.Key(ev)
}
func (s *slowApplyLKConsumer) RoundState(ctx context.Context, k LogicalKey) (RoundState, error) {
	return s.inner.RoundState(ctx, k)
}
func (s *slowApplyLKConsumer) IsComplete(rs RoundState) (bool, error) {
	return s.inner.IsComplete(rs)
}
func (s *slowApplyLKConsumer) DeriveOutcome(rs RoundState) (Outcome, error) {
	return s.inner.DeriveOutcome(rs)
}
func (s *slowApplyLKConsumer) Apply(ctx context.Context, key LogicalKey, outcome Outcome) error {
	s.applyEntered.Add(1)
	time.Sleep(s.applyDelay)
	return s.inner.Apply(ctx, key, outcome)
}
func (s *slowApplyLKConsumer) RecoveryProbe(ctx context.Context, key LogicalKey) (RecoveryStatus, error) {
	return s.inner.RecoveryProbe(ctx, key)
}
