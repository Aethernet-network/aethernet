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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
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
	prefixMeta       = "meta:" // generic metadata (genesis marker, onboarding counter, …)
	prefixAPIKey      = "key:"  // platform developer API keys
	prefixEscrow      = "esc:"  // task escrow entries
	prefixValidator      = "val:"  // validator registry records
	prefixReplayReserve  = "rsvr:" // per-category replay reserve balances
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

// DeleteTransfer removes the TransferEntry stored under "txf:<id>".
// Used by TransferLedger.ResetOptimisticOutflows to purge stale entries.
func (s *Store) DeleteTransfer(id event.EventID) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(prefixTransfer + string(id)))
	})
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

// DeleteIdentity removes the CapabilityFingerprint for the given agentID from
// the store. Returns nil when the key does not exist (idempotent delete).
func (s *Store) DeleteIdentity(id crypto.AgentID) error {
	return s.db.Update(func(txn *badger.Txn) error {
		err := txn.Delete([]byte(prefixIdentity + string(id)))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		return err
	})
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
// Stake metadata (stakedSince, lastActivity timestamps + staked amount)
// ---------------------------------------------------------------------------

// stakeMetaValue encodes two int64 timestamps and one uint64 amount as a
// 24-byte big-endian blob.
// Index  0– 7 = stakedSince (int64 as uint64)
// Index  8–15 = lastActivity (int64 as uint64)
// Index 16–23 = stakedAmount (uint64) — added in v2; absent in old 16-byte blobs
func stakeMetaValue(stakedSince, lastActivity int64, stakedAmount uint64) []byte {
	buf := make([]byte, 24)
	binary.BigEndian.PutUint64(buf[0:8], uint64(stakedSince))
	binary.BigEndian.PutUint64(buf[8:16], uint64(lastActivity))
	binary.BigEndian.PutUint64(buf[16:24], stakedAmount)
	return buf
}

func parseStakeMetaValue(val []byte) (stakedSince, lastActivity int64, stakedAmount uint64) {
	if len(val) < 16 {
		return 0, 0, 0
	}
	stakedSince = int64(binary.BigEndian.Uint64(val[0:8]))
	lastActivity = int64(binary.BigEndian.Uint64(val[8:16]))
	if len(val) >= 24 {
		stakedAmount = binary.BigEndian.Uint64(val[16:24])
	}
	return
}

// PutStakeMeta stores stakedSince, lastActivity, and stakedAmount for agentID
// under the key "stk:<agentID>".
func (s *Store) PutStakeMeta(agentID crypto.AgentID, stakedSince int64, lastActivity int64, stakedAmount uint64) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixStakeMeta+string(agentID)), stakeMetaValue(stakedSince, lastActivity, stakedAmount))
	})
}

// GetStakeMeta retrieves the stakedSince, lastActivity, and stakedAmount for agentID.
// Returns (0, 0, 0, nil) if the key does not exist.
func (s *Store) GetStakeMeta(agentID crypto.AgentID) (stakedSince int64, lastActivity int64, stakedAmount uint64, err error) {
	err = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixStakeMeta + string(agentID)))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil // not an error — agent simply has no stored meta
			}
			return err
		}
		return item.Value(func(val []byte) error {
			stakedSince, lastActivity, stakedAmount = parseStakeMetaValue(val)
			return nil
		})
	})
	return
}

// AllStakeMeta returns all stored stake metadata as a map from AgentID to a
// [3]int64 array where index 0 = stakedSince, index 1 = lastActivity, and
// index 2 = stakedAmount (as int64 for uniformity; always non-negative).
func (s *Store) AllStakeMeta() (map[crypto.AgentID][3]int64, error) {
	result := make(map[crypto.AgentID][3]int64)
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
				ss, la, amt := parseStakeMetaValue(val)
				result[crypto.AgentID(key)] = [3]int64{ss, la, int64(amt)}
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
// Generic metadata key-value (genesis markers, counters, …)
// ---------------------------------------------------------------------------

// PutMeta stores an arbitrary byte value under "meta:<key>".
func (s *Store) PutMeta(key string, value []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixMeta+key), value)
	})
}

// GetMeta retrieves the byte value stored under "meta:<key>".
// Returns (nil, nil) when the key does not exist.
func (s *Store) GetMeta(key string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixMeta + key))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil
			}
			return err
		}
		return item.Value(func(val []byte) error {
			data = make([]byte, len(val))
			copy(data, val)
			return nil
		})
	})
	return data, err
}

// ---------------------------------------------------------------------------
// Platform developer API keys (raw JSON blobs)
// ---------------------------------------------------------------------------

// PutAPIKey stores a raw JSON-encoded APIKey blob under "key:<key>".
func (s *Store) PutAPIKey(key string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixAPIKey+key), data)
	})
}

// GetAPIKey retrieves the raw JSON blob for the API key.
// Returns (nil, nil) when the key does not exist.
func (s *Store) GetAPIKey(key string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixAPIKey + key))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil
			}
			return err
		}
		return item.Value(func(val []byte) error {
			data = make([]byte, len(val))
			copy(data, val)
			return nil
		})
	})
	return data, err
}

// AllAPIKeys returns all stored API key blobs as a map from key string to raw JSON.
func (s *Store) AllAPIKeys() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixAPIKey)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixAPIKey)
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

// ---------------------------------------------------------------------------
// Service registry listings (raw JSON blobs)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Task marketplace entries (raw JSON blobs)
// ---------------------------------------------------------------------------

const prefixTask = "tsk:"

// PutTask stores a raw JSON-encoded Task under "tsk:<id>".
func (s *Store) PutTask(id string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixTask+id), data)
	})
}

// GetTask retrieves the raw JSON blob for the task identified by id.
// Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetTask(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixTask + id))
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
		return nil, fmt.Errorf("store: get task %s: %w", id, err)
	}
	return data, nil
}

// DeleteTask removes the stored task with the given id. It is a no-op when
// the id is not found.
func (s *Store) DeleteTask(id string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		err := txn.Delete([]byte(prefixTask + id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		return err
	})
}

// AllTasks returns all stored task blobs as a map from task ID to raw JSON.
func (s *Store) AllTasks() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixTask)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixTask)
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

// ---------------------------------------------------------------------------
// Reputation records (raw JSON blobs)
// ---------------------------------------------------------------------------

const prefixReputation = "rep:"

// PutReputation stores a raw JSON-encoded AgentReputation under "rep:<agentID>".
func (s *Store) PutReputation(agentID string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixReputation+agentID), data)
	})
}

// GetReputation retrieves the raw JSON blob for the reputation identified by agentID.
// Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetReputation(agentID string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixReputation + agentID))
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
		return nil, fmt.Errorf("store: get reputation %s: %w", agentID, err)
	}
	return data, nil
}

// AllReputations returns all stored reputation blobs as a map from agentID to raw JSON.
func (s *Store) AllReputations() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixReputation)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixReputation)
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

// ---------------------------------------------------------------------------
// Escrow entries
// ---------------------------------------------------------------------------

// PutEscrow serialises entry to JSON and stores it under "esc:<entry.TaskID>".
func (s *Store) PutEscrow(entry *escrow.EscrowEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: marshal escrow: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixEscrow+entry.TaskID), data)
	})
}

// GetEscrow retrieves the EscrowEntry stored under "esc:<taskID>".
func (s *Store) GetEscrow(taskID string) (*escrow.EscrowEntry, error) {
	var entry escrow.EscrowEntry
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixEscrow + taskID))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &entry)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: get escrow %s: %w", taskID, err)
	}
	return &entry, nil
}

// AllEscrowEntries returns every EscrowEntry in the store in unspecified order.
func (s *Store) AllEscrowEntries() ([]*escrow.EscrowEntry, error) {
	var entries []*escrow.EscrowEntry
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixEscrow)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var entry escrow.EscrowEntry
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

// DeleteEscrow removes the EscrowEntry stored under "esc:<taskID>".
// Returns nil when the key does not exist (idempotent delete).
func (s *Store) DeleteEscrow(taskID string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		err := txn.Delete([]byte(prefixEscrow + taskID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		return err
	})
}

// ---------------------------------------------------------------------------
// Replay job and outcome entries (raw JSON blobs)
// ---------------------------------------------------------------------------

const prefixReplayJob     = "rpj:"
const prefixReplayOutcome = "rpo:"

// PutReplayJob stores a raw JSON-encoded ReplayJob under "rpj:<id>".
func (s *Store) PutReplayJob(id string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixReplayJob+id), data)
	})
}

// GetReplayJob retrieves the raw JSON blob for the ReplayJob identified by id.
// Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetReplayJob(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixReplayJob + id))
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
		return nil, fmt.Errorf("store: get replay job %s: %w", id, err)
	}
	return data, nil
}

// AllReplayJobs returns all stored ReplayJob blobs as a map from job ID to raw JSON.
func (s *Store) AllReplayJobs() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixReplayJob)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixReplayJob)
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

// PutReplayOutcome stores a raw JSON-encoded ReplayOutcome under "rpo:<id>".
func (s *Store) PutReplayOutcome(id string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixReplayOutcome+id), data)
	})
}

// GetReplayOutcome retrieves the raw JSON blob for the ReplayOutcome identified by id.
// Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetReplayOutcome(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixReplayOutcome + id))
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
		return nil, fmt.Errorf("store: get replay outcome %s: %w", id, err)
	}
	return data, nil
}

// AllReplayOutcomes returns all stored ReplayOutcome blobs as a map from job ID to raw JSON.
func (s *Store) AllReplayOutcomes() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixReplayOutcome)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixReplayOutcome)
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

// ---------------------------------------------------------------------------
// Atomic multi-step operations (Fix 7: BadgerDB transactions)
// ---------------------------------------------------------------------------

// RunInTransaction executes fn within a single BadgerDB read-write transaction.
// If fn returns an error, the transaction is rolled back. Use this for
// multi-step operations that must be atomic (e.g. escrow release + entry delete).
func (s *Store) RunInTransaction(fn func(txn *badger.Txn) error) error {
	return s.db.Update(fn)
}

// ---------------------------------------------------------------------------
// Consensus vote persistence (Fix 6: VotingRound state survives restart)
// ---------------------------------------------------------------------------

// Key format: "vot:<eventID>/<voterID>" → JSON-encoded consensus.PersistedVote.
const prefixVote = "vot:"

func voteKey(eventID, voterID string) []byte {
	return []byte(prefixVote + eventID + "/" + voterID)
}

// PutVote stores a single vote in the persistence layer.
// Key: "vot:<eventID>/<voterID>".
func (s *Store) PutVote(eventID, voterID string, verdict bool) error {
	data, err := json.Marshal(consensus.PersistedVote{EventID: eventID, VoterID: voterID, Verdict: verdict})
	if err != nil {
		return fmt.Errorf("store: marshal vote: %w", err)
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(voteKey(eventID, voterID), data)
	})
}

// GetVotes returns all votes for eventID.
func (s *Store) GetVotes(eventID string) ([]consensus.PersistedVote, error) {
	var votes []consensus.PersistedVote
	prefix := []byte(prefixVote + eventID + "/")
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var v consensus.PersistedVote
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &v)
			}); err != nil {
				return err
			}
			votes = append(votes, v)
		}
		return nil
	})
	return votes, err
}

// DeleteVotes removes all persisted votes for eventID (called after finalization).
func (s *Store) DeleteVotes(eventID string) error {
	prefix := []byte(prefixVote + eventID + "/")
	// Collect keys first (cannot delete inside View).
	var keys [][]byte
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = false // keys only
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			k := it.Item().KeyCopy(nil)
			keys = append(keys, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.db.Update(func(txn *badger.Txn) error {
		for _, k := range keys {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// AllVoteEventIDs returns the unique event IDs for which votes are stored.
func (s *Store) AllVoteEventIDs() ([]string, error) {
	seen := make(map[string]struct{})
	prefixBytes := []byte(prefixVote)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefixBytes
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixVote)
		for it.Rewind(); it.Valid(); it.Next() {
			key := string(it.Item().Key()[prefixLen:])
			// key format: "<eventID>/<voterID>" — extract eventID
			if idx := strings.Index(key, "/"); idx >= 0 {
				seen[key[:idx]] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// Canary task entries (raw JSON blobs) + secondary taskID index
// ---------------------------------------------------------------------------

const prefixCanary          = "cnr:"
const prefixCanaryTaskIndex = "cnrt:"

// PutCanary stores a raw JSON-encoded CanaryTask under "cnr:<id>".
func (s *Store) PutCanary(id string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixCanary+id), data)
	})
}

// GetCanary retrieves the raw JSON blob for the CanaryTask identified by id.
// Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetCanary(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixCanary + id))
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
		return nil, fmt.Errorf("store: get canary %s: %w", id, err)
	}
	return data, nil
}

// PutCanaryTaskIndex stores a mapping from taskID → canaryID under
// "cnrt:<taskID>". Used by GetCanaryByTaskID for O(1) canary lookup by task.
func (s *Store) PutCanaryTaskIndex(taskID, canaryID string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixCanaryTaskIndex+taskID), []byte(canaryID))
	})
}

// GetCanaryByTaskID looks up the canaryID for taskID from the secondary index,
// then returns the full CanaryTask JSON blob.
// Returns an error wrapping badger.ErrKeyNotFound when taskID is not a canary.
func (s *Store) GetCanaryByTaskID(taskID string) ([]byte, error) {
	// Step 1: resolve canaryID from the secondary index.
	var canaryID string
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixCanaryTaskIndex + taskID))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			canaryID = string(val)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: get canary task index %s: %w", taskID, err)
	}
	// Step 2: retrieve the CanaryTask blob.
	return s.GetCanary(canaryID)
}

// AllCanaries returns all stored CanaryTask blobs as a map from canary ID to
// raw JSON. Only "cnr:" prefixed keys are scanned; "cnrt:" index entries are
// not included.
func (s *Store) AllCanaries() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixCanary)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixCanary)
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

// ---------------------------------------------------------------------------
// Calibration signal entries (raw JSON blobs)
// ---------------------------------------------------------------------------

const prefixCalibration = "cal:"

// PutCalibrationSignal stores a raw JSON-encoded CalibrationSignal under "cal:<id>".
func (s *Store) PutCalibrationSignal(id string, data []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixCalibration+id), data)
	})
}

// GetCalibrationSignal retrieves the raw JSON blob for the CalibrationSignal
// identified by id. Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetCalibrationSignal(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixCalibration + id))
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
		return nil, fmt.Errorf("store: get calibration signal %s: %w", id, err)
	}
	return data, nil
}

// AllCalibrationSignals returns all stored CalibrationSignal blobs as a map
// from signal ID to raw JSON.
func (s *Store) AllCalibrationSignals() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixCalibration)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixCalibration)
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

// CalibrationSignalsByActor scans the "cal:" prefix and returns raw JSON blobs
// whose actor_id field matches the given actorID. This is an in-process filter
// scan — calibration queries are infrequent measurement reads, not hot-path
// operations, so a full scan is acceptable.
func (s *Store) CalibrationSignalsByActor(actorID string) ([][]byte, error) {
	var result [][]byte
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixCalibration)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			if err := item.Value(func(val []byte) error {
				// Extract actor_id without full unmarshal.
				var partial struct {
					ActorID string `json:"actor_id"`
				}
				if err := json.Unmarshal(val, &partial); err != nil {
					return nil // skip malformed entries rather than aborting the scan
				}
				if partial.ActorID == actorID {
					blob := make([]byte, len(val))
					copy(blob, val)
					result = append(result, blob)
				}
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
// Validator registry records (raw JSON blobs)
// ---------------------------------------------------------------------------

// PutValidator stores a raw JSON-encoded Validator under "val:<id>".
func (s *Store) PutValidator(id string, data []byte) error {
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixValidator+id), data)
	}); err != nil {
		slog.Error("store: failed to persist validator", "id", id, "err", err)
		return err
	}
	return nil
}

// GetValidator retrieves the raw JSON blob for the validator identified by id.
// Returns an error wrapping badger.ErrKeyNotFound when absent.
func (s *Store) GetValidator(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixValidator + id))
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
		return nil, fmt.Errorf("store: get validator %s: %w", id, err)
	}
	return data, nil
}

// DeleteValidator removes the stored validator with the given id. It is a
// no-op when the id is not found.
func (s *Store) DeleteValidator(id string) error {
	return s.db.Update(func(txn *badger.Txn) error {
		err := txn.Delete([]byte(prefixValidator + id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		return err
	})
}

// AllValidators returns all stored validator blobs as a map from validator ID
// to raw JSON.
func (s *Store) AllValidators() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixValidator)
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixLen := len(prefixValidator)
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

// ---------------------------------------------------------------------------
// Replay reserve balances (uint64 per category)
// ---------------------------------------------------------------------------

// PutReplayReserve stores the balance for a per-category replay reserve under
// "rsvr:<category>". The balance is encoded as big-endian uint64.
func (s *Store) PutReplayReserve(category string, balance uint64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, balance)
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(prefixReplayReserve+category), buf)
	}); err != nil {
		slog.Error("store: failed to persist replay reserve", "category", category, "err", err)
		return err
	}
	return nil
}

// GetReplayReserve retrieves the replay reserve balance for category.
// Returns 0, nil when no balance has been recorded yet.
func (s *Store) GetReplayReserve(category string) (uint64, error) {
	var balance uint64
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(prefixReplayReserve + category))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil // zero balance is the default
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if len(val) == 8 {
				balance = binary.BigEndian.Uint64(val)
			}
			return nil
		})
	})
	if err != nil {
		return 0, fmt.Errorf("store: get replay reserve %s: %w", category, err)
	}
	return balance, nil
}
