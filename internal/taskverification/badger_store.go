package taskverification

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// Key prefixes for BadgerDB storage. All task-verification keys live under
// the "tv:" namespace to avoid collisions with other subsystems.
const (
	prefixRound   = "tv:round:"   // tv:round:<round_id> → canonical round bytes
	prefixBySub   = "tv:by_sub:"  // tv:by_sub:<submission_event_id> → round_id
	prefixByTask  = "tv:by_task:" // tv:by_task:<task_id>:<round_id> → round_id
	prefixByState = "tv:by_state:" // tv:by_state:<state_int>:<round_id> → round_id
)

// BadgerStore implements Store backed by a BadgerDB instance.
type BadgerStore struct {
	db *badger.DB

	// mu serializes SaveRound's read-then-write sequence. Without this,
	// two concurrent SaveRound calls for the same round could both read
	// "not found", both attempt to create, and produce inconsistent
	// secondary indexes (the old-state deletion would be skipped by one).
	// This is a per-instance mutex, not package-level, so distinct
	// BadgerStore instances (e.g., in tests) do not contend.
	mu sync.Mutex
}

// NewBadgerStore creates a BadgerStore backed by the given BadgerDB instance.
func NewBadgerStore(db *badger.DB) *BadgerStore {
	return &BadgerStore{db: db}
}

// Empty reports whether the round store holds any persisted rounds.
// Used by the projection registry StateProbe — see
// docs/plans/2026-04-12-reputation-step-2-retrofit-projections.md §D7.
// Scans the primary "tv:round:" prefix and returns on first hit.
func (s *BadgerStore) Empty(_ context.Context) (bool, error) {
	empty := true
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixRound)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		it.Rewind()
		if it.Valid() {
			empty = false
		}
		return nil
	})
	return empty, err
}

// SaveRound persists a round with all secondary indexes updated atomically
// in a single Badger transaction.
func (s *BadgerStore) SaveRound(_ context.Context, r *TaskVerificationRound) error {
	data, err := r.Canonical()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSerializationFailed, err)
	}

	roundIDBytes := []byte(r.RoundID)
	primaryKey := []byte(prefixRound + string(r.RoundID))
	subKey := []byte(prefixBySub + string(r.SubmissionEventID))
	taskKey := []byte(prefixByTask + r.TaskID + ":" + string(r.RoundID))
	newStateKey := []byte(prefixByState + strconv.Itoa(int(r.State)) + ":" + string(r.RoundID))

	s.mu.Lock()
	defer s.mu.Unlock()

	// Determine old state for index cleanup (if this is an update).
	var oldState *RoundState
	_ = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(primaryKey)
		if err != nil {
			return err // not found — new round
		}
		return item.Value(func(val []byte) error {
			existing, err := RoundFromCanonical(val)
			if err != nil {
				return err
			}
			oldState = &existing.State
			return nil
		})
	})

	return s.db.Update(func(txn *badger.Txn) error {
		// Primary record.
		if err := txn.Set(primaryKey, data); err != nil {
			return fmt.Errorf("%w: set primary: %v", ErrPersistenceFailed, err)
		}

		// Secondary: by submission event ID.
		if err := txn.Set(subKey, roundIDBytes); err != nil {
			return fmt.Errorf("%w: set sub index: %v", ErrPersistenceFailed, err)
		}

		// Secondary: by task ID.
		if err := txn.Set(taskKey, roundIDBytes); err != nil {
			return fmt.Errorf("%w: set task index: %v", ErrPersistenceFailed, err)
		}

		// Secondary: by state. Remove old state entry if state changed.
		if oldState != nil && *oldState != r.State {
			oldStateKey := []byte(prefixByState + strconv.Itoa(int(*oldState)) + ":" + string(r.RoundID))
			_ = txn.Delete(oldStateKey) // best-effort cleanup
		}
		if err := txn.Set(newStateKey, roundIDBytes); err != nil {
			return fmt.Errorf("%w: set state index: %v", ErrPersistenceFailed, err)
		}

		return nil
	})
}

// LoadRound retrieves a round by its primary key.
func (s *BadgerStore) LoadRound(_ context.Context, id RoundID) (*TaskVerificationRound, error) {
	var r *TaskVerificationRound
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixRound + string(id)))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			var decErr error
			r, decErr = RoundFromCanonical(val)
			return decErr
		})
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, ErrRoundNotFound
		}
		return nil, fmt.Errorf("%w: load round: %v", ErrPersistenceFailed, err)
	}
	return r, nil
}

// LoadRoundBySubmissionEvent looks up the round ID via the submission-event
// secondary index, then loads the full round.
func (s *BadgerStore) LoadRoundBySubmissionEvent(_ context.Context, eventID event.EventID) (*TaskVerificationRound, error) {
	var roundID RoundID
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixBySub + string(eventID)))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			roundID = RoundID(val)
			return nil
		})
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, ErrRoundNotFound
		}
		return nil, fmt.Errorf("%w: load by submission: %v", ErrPersistenceFailed, err)
	}
	return s.LoadRound(context.Background(), roundID)
}

// LoadRoundsByTaskID scans the by-task secondary index and loads all matching
// rounds. Returns an empty slice if none exist.
func (s *BadgerStore) LoadRoundsByTaskID(_ context.Context, taskID string) ([]*TaskVerificationRound, error) {
	prefix := []byte(prefixByTask + taskID + ":")
	var roundIDs []RoundID

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			if err := it.Item().Value(func(val []byte) error {
				roundIDs = append(roundIDs, RoundID(val))
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: scan by task: %v", ErrPersistenceFailed, err)
	}

	sort.Slice(roundIDs, func(i, j int) bool { return roundIDs[i] < roundIDs[j] })

	rounds := make([]*TaskVerificationRound, 0, len(roundIDs))
	for _, id := range roundIDs {
		r, err := s.LoadRound(context.Background(), id)
		if err != nil {
			continue // round deleted between scan and load — skip
		}
		rounds = append(rounds, r)
	}
	return rounds, nil
}

// DeleteRound removes a round and all its secondary indexes.
func (s *BadgerStore) DeleteRound(_ context.Context, id RoundID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load the round first to find its secondary index keys.
	var r *TaskVerificationRound
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixRound + string(id)))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			var decErr error
			r, decErr = RoundFromCanonical(val)
			return decErr
		})
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return ErrRoundNotFound
		}
		return fmt.Errorf("%w: load for delete: %v", ErrPersistenceFailed, err)
	}

	return s.db.Update(func(txn *badger.Txn) error {
		// Delete primary.
		if err := txn.Delete([]byte(prefixRound + string(id))); err != nil {
			return err
		}
		// Delete secondary: by submission.
		_ = txn.Delete([]byte(prefixBySub + string(r.SubmissionEventID)))
		// Delete secondary: by task.
		_ = txn.Delete([]byte(prefixByTask + r.TaskID + ":" + string(id)))
		// Delete secondary: by state.
		_ = txn.Delete([]byte(prefixByState + strconv.Itoa(int(r.State)) + ":" + string(id)))
		return nil
	})
}

// ListOpenRounds returns all rounds in RoundStateOpen.
func (s *BadgerStore) ListOpenRounds(ctx context.Context) ([]*TaskVerificationRound, error) {
	return s.ListRoundsByState(ctx, RoundStateOpen)
}

// ListRoundsByState scans the by-state secondary index and loads all
// matching rounds.
func (s *BadgerStore) ListRoundsByState(_ context.Context, state RoundState) ([]*TaskVerificationRound, error) {
	prefix := []byte(prefixByState + strconv.Itoa(int(state)) + ":")
	var roundIDs []RoundID

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			if err := it.Item().Value(func(val []byte) error {
				roundIDs = append(roundIDs, RoundID(val))
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: scan by state: %v", ErrPersistenceFailed, err)
	}

	rounds := make([]*TaskVerificationRound, 0, len(roundIDs))
	for _, id := range roundIDs {
		r, err := s.LoadRound(context.Background(), id)
		if err != nil {
			continue
		}
		rounds = append(rounds, r)
	}
	return rounds, nil
}
