package epoch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
)

func newTestDB(t *testing.T) *badger.DB {
	t.Helper()
	opts := badger.DefaultOptions(t.TempDir())
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCounter_EmptyOnFreshDB(t *testing.T) {
	db := newTestDB(t)
	c, err := NewRoundCounter(db)
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	empty, err := c.Empty(context.Background())
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	if !empty {
		t.Fatalf("fresh counter must be empty")
	}
	if c.Total() != 0 {
		t.Fatalf("total: want 0, got %d", c.Total())
	}
	if c.CurrentEpoch() != 0 {
		t.Fatalf("epoch: want 0, got %d", c.CurrentEpoch())
	}
}

func TestCounter_ApplyRequiresRoundID(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	_, err = c.Apply(context.Background(), "")
	if err == nil {
		t.Fatalf("empty roundID must error")
	}
}

func TestCounter_ApplyIncrements(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	ctx := context.Background()
	changed, err := c.Apply(ctx, "round-1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if changed {
		// At EpochLength=1000, applying 1 round does not change epoch (both are 0).
		t.Fatalf("epochChanged must be false on first apply (EpochLength=%d)", EpochLength)
	}
	if c.Total() != 1 {
		t.Fatalf("total: want 1, got %d", c.Total())
	}
	if c.CurrentEpoch() != 0 {
		t.Fatalf("epoch: want 0, got %d", c.CurrentEpoch())
	}
	empty, _ := c.Empty(ctx)
	if empty {
		t.Fatalf("counter with 1 applied round must not be empty")
	}
}

func TestCounter_ApplyIdempotent(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := c.Apply(ctx, "round-X")
		if err != nil {
			t.Fatalf("apply #%d: %v", i, err)
		}
	}
	if c.Total() != 1 {
		t.Fatalf("idempotent apply: want total 1, got %d", c.Total())
	}
}

func TestCounter_ApplyDifferentRoundIDs(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := c.Apply(ctx, string(rune('A'+i)))
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	if c.Total() != 5 {
		t.Fatalf("want total 5, got %d", c.Total())
	}
}

// Verifies that the epoch boundary check uses integer division and crosses
// cleanly at the EpochLength-th apply.
func TestCounter_EpochBoundaryCrossing(t *testing.T) {
	// Use a custom, smaller EpochLength via a subtest-friendly counter.
	// EpochLength is a const, so we cannot vary it here; instead, test the
	// boundary crossing by applying exactly EpochLength rounds and checking
	// that the (EpochLength)th apply advances the epoch from 0 to 1.
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	ctx := context.Background()

	var epochChanged bool
	for i := uint64(1); i <= EpochLength; i++ {
		changed, err := c.Apply(ctx, makeRoundID(i))
		if err != nil {
			t.Fatalf("apply #%d: %v", i, err)
		}
		if i < EpochLength && changed {
			t.Fatalf("epoch must not change before the %dth apply; changed at i=%d", EpochLength, i)
		}
		if i == EpochLength && !changed {
			t.Fatalf("epoch must change on the %dth apply", EpochLength)
		}
		if changed {
			epochChanged = true
		}
	}
	if !epochChanged {
		t.Fatalf("expected epoch to change at boundary")
	}
	if c.CurrentEpoch() != 1 {
		t.Fatalf("after %d applies, epoch must be 1, got %d", EpochLength, c.CurrentEpoch())
	}
}

func TestCounter_PersistenceAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	openDB := func() *badger.DB {
		opts := badger.DefaultOptions(dir)
		opts.Logger = nil
		db, err := badger.Open(opts)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return db
	}

	// First run: apply 3 rounds.
	db1 := openDB()
	c1, err := NewRoundCounter(db1)
	if err != nil {
		t.Fatalf("first NewRoundCounter: %v", err)
	}
	ctx := context.Background()
	for _, rid := range []string{"r1", "r2", "r3"} {
		if _, err := c1.Apply(ctx, rid); err != nil {
			t.Fatalf("apply %s: %v", rid, err)
		}
	}
	if c1.Total() != 3 {
		t.Fatalf("pre-close total: want 3, got %d", c1.Total())
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second run: reload; total must be 3.
	db2 := openDB()
	defer db2.Close()
	c2, err := NewRoundCounter(db2)
	if err != nil {
		t.Fatalf("second NewRoundCounter: %v", err)
	}
	if c2.Total() != 3 {
		t.Fatalf("post-restart total: want 3, got %d", c2.Total())
	}

	// Re-applying a prior roundID must be a no-op.
	changed, err := c2.Apply(ctx, "r1")
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if changed {
		t.Fatalf("reapply must not change epoch")
	}
	if c2.Total() != 3 {
		t.Fatalf("reapply must not increment: got %d", c2.Total())
	}

	// New roundID increments normally.
	if _, err := c2.Apply(ctx, "r4"); err != nil {
		t.Fatalf("new apply: %v", err)
	}
	if c2.Total() != 4 {
		t.Fatalf("want 4, got %d", c2.Total())
	}
}

func TestCounter_OnEpochChange(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	ctx := context.Background()

	var fired int32
	var wg sync.WaitGroup
	wg.Add(1)
	c.OnEpochChange(func(epoch uint64) {
		if epoch != 1 {
			t.Errorf("callback epoch: want 1, got %d", epoch)
		}
		atomic.AddInt32(&fired, 1)
		wg.Done()
	})

	for i := uint64(1); i <= EpochLength; i++ {
		if _, err := c.Apply(ctx, makeRoundID(i)); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	wg.Wait()
	if atomic.LoadInt32(&fired) != 1 {
		t.Fatalf("callback must fire exactly once per epoch advance, got %d", fired)
	}
}

func TestCounter_OnEpochChange_NilCallbackIgnored(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	c.OnEpochChange(nil) // must not panic or register a nil
	// Fire a non-epoch-change apply; must not panic.
	if _, err := c.Apply(context.Background(), "r1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func TestCounter_ConcurrentApplies(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	ctx := context.Background()

	const workers = 8
	const perWorker = 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// Each worker uses a unique roundID namespace so collisions don't happen.
				rid := makeRoundIDFrom("w", uint64(w), uint64(i))
				if _, err := c.Apply(ctx, rid); err != nil {
					t.Errorf("apply: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	want := uint64(workers * perWorker)
	if c.Total() != want {
		t.Fatalf("concurrent total: want %d, got %d", want, c.Total())
	}
}

// makeRoundID returns a unique, deterministic test round ID.
func makeRoundID(i uint64) string {
	return "rid-" + uint64ToHex(i)
}

func makeRoundIDFrom(prefix string, a, b uint64) string {
	return prefix + "-" + uint64ToHex(a) + "-" + uint64ToHex(b)
}

func uint64ToHex(x uint64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = digits[x&0xf]
		x >>= 4
	}
	return string(b[:])
}
