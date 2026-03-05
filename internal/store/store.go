// Package store implements the AetherNet persistence layer using BadgerDB.
//
// Every mutable component — the causal DAG, the dual ledger, the OCS engine,
// and the identity registry — talks to this layer for durable writes. The store
// uses a key-prefix namespace to colocate related data while keeping all state
// in a single BadgerDB instance.
//
// All reads use db.View (read-only transactions); all writes use db.Update
// (read-write transactions). A background goroutine runs value-log GC every
// five minutes to reclaim space from stale values.
package store

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/dgraph-io/badger/v4"
)

// Key prefixes namespace each data type within a single BadgerDB instance.
const (
	prefixEvent      = "evt:"
	prefixTransfer   = "txf:"
	prefixGeneration = "gen:"
	prefixPending    = "ocs:"
	prefixIdentity   = "idn:"
	prefixStakeMeta  = "stk:"
	prefixRegistry   = "reg:"
)

// Store is the durable persistence layer for a single AetherNet node.
// It wraps a BadgerDB instance and exposes typed put/get/scan operations
// for every major data type. It is safe for concurrent use by multiple goroutines.
type Store struct {
	db   *badger.DB
	path string
	done chan struct{} // closed to stop the background GC goroutine
}

// NewStore opens (or creates) a BadgerDB database at path and starts the
// background value-log GC goroutine.
func NewStore(path string) (*Store, error) {
	opts := badger.DefaultOptions(path)
	opts.Logger = nil // suppress BadgerDB's internal info logging

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}

	s := &Store{
		db:   db,
		path: path,
		done: make(chan struct{}),
	}
	go s.gcLoop()
	return s, nil
}

// gcLoop runs BadgerDB value-log GC every 5 minutes until done is closed.
// ErrNoRewrite is expected when there is nothing to GC; all other errors are
// silently ignored — GC is best-effort housekeeping, not critical-path logic.
func (s *Store) gcLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			_ = s.db.RunValueLogGC(0.5)
		}
	}
}

// Close stops the GC goroutine and closes the underlying BadgerDB instance.
// After Close returns the Store must not be used.
func (s *Store) Close() error {
	close(s.done)
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// PutEvent serialises e to JSON and stores it under "evt:<e.ID>".
func (s *Store) PutEvent(e *event.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("store: marshal event: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixEvent+string(e.ID)), data)
	})
}

// GetEvent retrieves the event stored under "evt:<id>".
// Returns an error wrapping badger.ErrKeyNotFound if the event is absent.
func (s *Store) GetEvent(id event.EventID) (*event.Event, error) {
	var e event.Event
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixEvent + string(id)))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &e)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: get event %s: %w", id, err)
	}
	return &e, nil
}

// AllEvents returns every event in the store in unspecified order.
// Callers that require topological ordering must sort the result themselves.
func (s *Store) AllEvents() ([]*event.Event, error) {
	var events []*event.Event
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixEvent)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var e event.Event
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &e)
			}); err != nil {
				return err
			}
			events = append(events, &e)
		}
		return nil
	})
	return events, err
}

// ---------------------------------------------------------------------------
// Transfer entries
// ---------------------------------------------------------------------------

// PutTransfer serialises entry to JSON and stores it under "txf:<entry.EventID>".
func (s *Store) PutTransfer(entry *ledger.TransferEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: marshal transfer: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixTransfer+string(entry.EventID)), data)
	})
}

// GetTransfer retrieves the TransferEntry stored under "txf:<id>".
func (s *Store) GetTransfer(id event.EventID) (*ledger.TransferEntry, error) {
	var entry ledger.TransferEntry
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixTransfer + string(id)))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &entry)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: get transfer %s: %w", id, err)
	}
	return &entry, nil
}

// AllTransfers returns every TransferEntry in the store in unspecified order.
func (s *Store) AllTransfers() ([]*ledger.TransferEntry, error) {
	var entries []*ledger.TransferEntry
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixTransfer)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var entry ledger.TransferEntry
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &entry)
			}); err != nil {
				return err
			}
			entries = append(entries, &entry)
		}
		return nil
	})
	return entries, err
}

// ---------------------------------------------------------------------------
// Generation entries
// ---------------------------------------------------------------------------

// PutGeneration serialises entry to JSON and stores it under "gen:<entry.EventID>".
func (s *Store) PutGeneration(entry *ledger.GenerationEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: marshal generation: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixGeneration+string(entry.EventID)), data)
	})
}

// GetGeneration retrieves the GenerationEntry stored under "gen:<id>".
func (s *Store) GetGeneration(id event.EventID) (*ledger.GenerationEntry, error) {
	var entry ledger.GenerationEntry
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixGeneration + string(id)))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &entry)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: get generation %s: %w", id, err)
	}
	return &entry, nil
}

// AllGenerations returns every GenerationEntry in the store in unspecified order.
func (s *Store) AllGenerations() ([]*ledger.GenerationEntry, error) {
	var entries []*ledger.GenerationEntry
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixGeneration)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var entry ledger.GenerationEntry
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &entry)
			}); err != nil {
				return err
			}
			entries = append(entries, &entry)
		}
		return nil
	})
	return entries, err
}

// ---------------------------------------------------------------------------
// OCS pending items
// ---------------------------------------------------------------------------

// PutPending serialises item to JSON and stores it under "ocs:<item.EventID>".
func (s *Store) PutPending(item *ocs.PendingItem) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("store: marshal pending: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixPending+string(item.EventID)), data)
	})
}

// DeletePending removes the PendingItem stored under "ocs:<id>".
func (s *Store) DeletePending(id event.EventID) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(prefixPending + string(id)))
	})
}

// AllPending returns every PendingItem in the store in unspecified order.
func (s *Store) AllPending() ([]*ocs.PendingItem, error) {
	var items []*ocs.PendingItem
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixPending)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var pi ocs.PendingItem
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &pi)
			}); err != nil {
				return err
			}
			items = append(items, &pi)
		}
		return nil
	})
	return items, err
}

// ---------------------------------------------------------------------------
// Identity fingerprints
// ---------------------------------------------------------------------------

// PutIdentity serialises fp to JSON and stores it under "idn:<fp.AgentID>".
func (s *Store) PutIdentity(fp *identity.CapabilityFingerprint) error {
	data, err := json.Marshal(fp)
	if err != nil {
		return fmt.Errorf("store: marshal identity: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixIdentity+string(fp.AgentID)), data)
	})
}

// GetIdentity retrieves the CapabilityFingerprint stored under "idn:<id>".
func (s *Store) GetIdentity(id crypto.AgentID) (*identity.CapabilityFingerprint, error) {
	var fp identity.CapabilityFingerprint
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixIdentity + string(id)))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &fp)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: get identity %s: %w", id, err)
	}
	return &fp, nil
}

// AllIdentities returns every CapabilityFingerprint in the store in unspecified order.
func (s *Store) AllIdentities() ([]*identity.CapabilityFingerprint, error) {
	var fps []*identity.CapabilityFingerprint
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixIdentity)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var fp identity.CapabilityFingerprint
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &fp)
			}); err != nil {
				return err
			}
			fps = append(fps, &fp)
		}
		return nil
	})
	return fps, err
}

// ---------------------------------------------------------------------------
// Stake metadata (stakedSince, lastActivity timestamps)
// ---------------------------------------------------------------------------

// stakeMetaValue encodes two int64 timestamps as a 16-byte big-endian blob.
// Index 0–7 = stakedSince, index 8–15 = lastActivity.
func stakeMetaValue(stakedSince, lastActivity int64) []byte {
	buf := make([]byte, 16)
	binary.BigEndian.PutUint64(buf[0:8], uint64(stakedSince))
	binary.BigEndian.PutUint64(buf[8:16], uint64(lastActivity))
	return buf
}

func parseStakeMetaValue(val []byte) (stakedSince, lastActivity int64) {
	if len(val) < 16 {
		return 0, 0
	}
	stakedSince = int64(binary.BigEndian.Uint64(val[0:8]))
	lastActivity = int64(binary.BigEndian.Uint64(val[8:16]))
	return
}

// PutStakeMeta stores stakedSince and lastActivity timestamps for agentID
// under the key "stk:<agentID>".
func (s *Store) PutStakeMeta(agentID crypto.AgentID, stakedSince int64, lastActivity int64) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixStakeMeta+string(agentID)), stakeMetaValue(stakedSince, lastActivity))
	})
}

// GetStakeMeta retrieves the stakedSince and lastActivity timestamps for agentID.
// Returns (0, 0, nil) if the key does not exist.
func (s *Store) GetStakeMeta(agentID crypto.AgentID) (stakedSince int64, lastActivity int64, err error) {
	err = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixStakeMeta + string(agentID)))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil // not an error — agent simply has no stored meta
			}
			return err
		}
		return item.Value(func(val []byte) error {
			stakedSince, lastActivity = parseStakeMetaValue(val)
			return nil
		})
	})
	return
}

// AllStakeMeta returns all stored stake metadata as a map from AgentID to a
// [2]int64 array where index 0 = stakedSince and index 1 = lastActivity.
func (s *Store) AllStakeMeta() (map[crypto.AgentID][2]int64, error) {
	result := make(map[crypto.AgentID][2]int64)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixStakeMeta)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixStakeMeta)
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key()[prefixLen:]) // strip prefix
			if err := item.Value(func(val []byte) error {
				ss, la := parseStakeMetaValue(val)
				result[crypto.AgentID(key)] = [2]int64{ss, la}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// ---------------------------------------------------------------------------
// Service registry listings (raw JSON blobs)
// ---------------------------------------------------------------------------

// PutListing stores a raw JSON-encoded ServiceListing under "reg:<agentID>".
func (s *Store) PutListing(agentID string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixRegistry+agentID), data)
	})
}

// GetListing retrieves the raw JSON blob for the listing identified by agentID.
// Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetListing(agentID string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixRegistry + agentID))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			data = make([]byte, len(val))
			copy(data, val)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: get listing %s: %w", agentID, err)
	}
	return data, nil
}

// AllListings returns all stored listing blobs as a map from agentID to raw JSON.
func (s *Store) AllListings() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixRegistry)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixRegistry)
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := string(item.Key()[prefixLen:])
			if err := item.Value(func(val []byte) error {
				blob := make([]byte, len(val))
				copy(blob, val)
				result[key] = blob
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}
