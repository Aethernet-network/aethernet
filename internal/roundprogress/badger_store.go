package roundprogress

import (
	"encoding/json"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

const keyPrefix = "rp:snap:"

// BadgerSnapshotStore persists RoundProgressSnapshots to BadgerDB.
// Key format: rp:snap:<roundID>:<validatorID>:<analyzerFamily>
type BadgerSnapshotStore struct {
	db *badger.DB
}

// NewBadgerSnapshotStore creates a store backed by the given BadgerDB instance.
// The caller owns the DB lifecycle — the store does not close it.
func NewBadgerSnapshotStore(db *badger.DB) *BadgerSnapshotStore {
	return &BadgerSnapshotStore{db: db}
}

func badgerKey(roundID, validatorID, family string) []byte {
	return []byte(keyPrefix + roundID + ":" + validatorID + ":" + family)
}

func roundPrefix(roundID string) []byte {
	return []byte(keyPrefix + roundID + ":")
}

// Get returns the latest snapshot for the given key, or nil if none exists.
func (s *BadgerSnapshotStore) Get(roundID, validatorID, family string) (*RoundProgressSnapshot, error) {
	var snap RoundProgressSnapshot
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(badgerKey(roundID, validatorID, family))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &snap)
		})
	})
	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("roundprogress: get snapshot: %w", err)
	}
	return &snap, nil
}

// GetAllForRound returns all snapshots for a given round using prefix scan.
func (s *BadgerSnapshotStore) GetAllForRound(roundID string) ([]*RoundProgressSnapshot, error) {
	prefix := roundPrefix(roundID)
	var result []*RoundProgressSnapshot

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			var snap RoundProgressSnapshot
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &snap)
			}); err != nil {
				return err
			}
			result = append(result, &snap)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("roundprogress: get all for round: %w", err)
	}
	return result, nil
}

// Put stores or replaces the snapshot for the given key.
func (s *BadgerSnapshotStore) Put(snap *RoundProgressSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("roundprogress: marshal snapshot: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(badgerKey(snap.RoundID, snap.ValidatorID, snap.AnalyzerFamily), data)
	})
}

// DeleteRound removes all snapshots for a finalized round.
func (s *BadgerSnapshotStore) DeleteRound(roundID string) error {
	prefix := roundPrefix(roundID)
	return s.db.Update(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		var keys [][]byte
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := it.Item().KeyCopy(nil)
			keys = append(keys, key)
		}
		for _, key := range keys {
			if err := txn.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}
