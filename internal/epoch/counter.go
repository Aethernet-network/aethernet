package epoch

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	badger "github.com/dgraph-io/badger/v4"
)

// EpochLength is the number of finalized rounds per protocol epoch.
// Plan §16 locked value.
const EpochLength uint64 = 1000

// Key prefixes. Kept distinct from every other in-repo prefix (prior audit
// inventoried: rep, tv, tvr, cal, esc, xfr, evt, idn, val, stk, meta, rp,
// tsk, key, txf). "ec" = epoch counter.
const (
	keyTotal      = "ec:total"
	prefixCounted = "ec:counted:"
)

// RoundCounter maintains the monotone count of finalized
// TaskVerificationConsensus events and exposes the derived epoch index.
//
// The counter is safe for concurrent use. Apply is atomic per-roundID via a
// BadgerDB update transaction; double-applies for the same roundID are no-ops.
type RoundCounter struct {
	mu        sync.Mutex
	db        *badger.DB
	total     uint64 // cached in memory; authoritative value is in BadgerDB
	callbacks []func(epoch uint64)
}

// NewRoundCounter constructs a counter backed by db. Loads the current
// total from BadgerDB so subsequent reads reflect state carried across
// restarts.
func NewRoundCounter(db *badger.DB) (*RoundCounter, error) {
	if db == nil {
		return nil, errors.New("epoch: NewRoundCounter requires non-nil db")
	}
	c := &RoundCounter{db: db}
	if err := c.loadTotal(); err != nil {
		return nil, fmt.Errorf("epoch: load total: %w", err)
	}
	return c, nil
}

func (c *RoundCounter) loadTotal() error {
	return c.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(keyTotal))
		if errors.Is(err, badger.ErrKeyNotFound) {
			c.total = 0
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if len(val) != 8 {
				return fmt.Errorf("epoch: corrupted total key: %d bytes", len(val))
			}
			c.total = binary.BigEndian.Uint64(val)
			return nil
		})
	})
}

// Apply records that a round with the given roundID has finalized.
// Idempotent: re-applies of the same roundID are no-ops and return
// (epochChanged=false, nil).
//
// Returns epochChanged=true only when this apply caused total/EpochLength
// to advance (i.e., the increment crossed a multiple of EpochLength).
// Registered OnEpochChange callbacks are fired on fresh goroutines in that
// case.
func (c *RoundCounter) Apply(ctx context.Context, roundID string) (bool, error) {
	if roundID == "" {
		return false, errors.New("epoch: Apply requires non-empty roundID")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var (
		alreadyCounted bool
		newTotal       uint64
		epochChanged   bool
	)
	markerKey := []byte(prefixCounted + roundID)
	err := c.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(markerKey); err == nil {
			alreadyCounted = true
			return nil
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}

		prevTotal := c.total
		newTotal = prevTotal + 1
		prevEpoch := prevTotal / EpochLength
		nextEpoch := newTotal / EpochLength
		epochChanged = nextEpoch != prevEpoch

		totalBuf := make([]byte, 8)
		binary.BigEndian.PutUint64(totalBuf, newTotal)
		if err := txn.Set([]byte(keyTotal), totalBuf); err != nil {
			return err
		}
		return txn.Set(markerKey, nil)
	})
	if err != nil {
		return false, fmt.Errorf("epoch: Apply: %w", err)
	}
	if alreadyCounted {
		return false, nil
	}

	// Commit succeeded — now update in-memory state.
	c.total = newTotal

	if epochChanged {
		nextEpoch := newTotal / EpochLength
		// Fire callbacks on fresh goroutines so a slow callback doesn't
		// block the counter or subsequent callbacks.
		for _, cb := range c.callbacks {
			cb := cb
			go cb(nextEpoch)
		}
	}
	return epochChanged, nil
}

// Total returns the current cumulative count of finalized rounds.
func (c *RoundCounter) Total() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// CurrentEpoch returns total / EpochLength. Pure memory read.
func (c *RoundCounter) CurrentEpoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total / EpochLength
}

// Empty implements the projection.StateProbe contract for the counter
// itself: returns (true, nil) iff no rounds have been applied yet.
func (c *RoundCounter) Empty(_ context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total == 0, nil
}

// OnEpochChange registers a callback to be invoked (on a fresh goroutine)
// each time Apply causes the epoch value to advance. Callbacks MUST be
// side-effect-bounded and must not block indefinitely; they receive the
// new epoch value as their argument.
//
// Callbacks are not removable in Step 2 — the expected use case is a
// single registration at startup wiring.
func (c *RoundCounter) OnEpochChange(cb func(epoch uint64)) {
	if cb == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callbacks = append(c.callbacks, cb)
}
