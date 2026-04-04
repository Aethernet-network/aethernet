package recognition

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// IndexStore is the persistence interface for the Recognition Index.
// Implemented by badger_index.go for production and by in-memory stores
// for testing. Uses the same prefix-key pattern as the rest of the store.
type IndexStore interface {
	PutRecognition(key string, data []byte) error
	GetRecognition(key string) ([]byte, error)
	DeleteRecognition(key string) error
	// ScanRecognitionPrefix returns all entries whose key starts with prefix.
	ScanRecognitionPrefix(prefix string) (map[string][]byte, error)
}

// Index is the Recognition Index — a persistent, idempotent tracker of
// per-consumer recognition state for each event. It is safe for concurrent
// use. The in-memory map provides fast-path reads; the optional store
// provides crash-safe persistence.
//
// A secondary index (prereqIndex) maps prerequisite keys to the set of
// (consumer:eventID) composite keys waiting on that prerequisite. This
// provides O(1) targeted activation without scanning all items.
type Index struct {
	mu          sync.RWMutex
	items       map[string]*RecognitionState // key: "consumer:eventID"
	prereqIndex map[string]map[string]struct{} // prereqKey → set of composite keys
	store       IndexStore                     // optional; nil = in-memory only
}

// NewIndex creates a Recognition Index. If store is non-nil, state is
// persisted on every write for crash safety.
func NewIndex(store IndexStore) *Index {
	return &Index{
		items:       make(map[string]*RecognitionState),
		prereqIndex: make(map[string]map[string]struct{}),
		store:       store,
	}
}

// stateKey constructs the composite key for a (consumer, event) pair.
func stateKey(consumer string, eventID event.EventID) string {
	return consumer + ":" + string(eventID)
}

// Get retrieves the recognition state for a (consumer, event) pair.
// Returns (nil, ErrNotFound) if no state exists.
func (idx *Index) Get(consumer string, eventID event.EventID) (*RecognitionState, error) {
	key := stateKey(consumer, eventID)

	idx.mu.RLock()
	if s, ok := idx.items[key]; ok {
		idx.mu.RUnlock()
		return s, nil
	}
	idx.mu.RUnlock()

	// Try persistent store.
	if idx.store != nil {
		data, err := idx.store.GetRecognition(key)
		if err != nil {
			return nil, err
		}
		if data != nil {
			var s RecognitionState
			if err := json.Unmarshal(data, &s); err != nil {
				return nil, fmt.Errorf("recognition: unmarshal state: %w", err)
			}
			// Populate in-memory cache.
			idx.mu.Lock()
			idx.items[key] = &s
			idx.mu.Unlock()
			return &s, nil
		}
	}

	return nil, ErrNotFound
}

// Put writes or updates the recognition state. Maintains the prerequisite
// secondary index and persists to store if available.
func (idx *Index) Put(state *RecognitionState) error {
	key := stateKey(state.ConsumerName, state.EventID)

	idx.mu.Lock()
	// Remove from old prereq index if the item existed with a different key.
	if old, exists := idx.items[key]; exists && old.PrerequisiteKey != "" {
		if old.PrerequisiteKey != state.PrerequisiteKey {
			if set, ok := idx.prereqIndex[old.PrerequisiteKey]; ok {
				delete(set, key)
				if len(set) == 0 {
					delete(idx.prereqIndex, old.PrerequisiteKey)
				}
			}
		}
	}
	idx.items[key] = state
	// Add to new prereq index if deferred.
	if state.PrerequisiteKey != "" && !state.Ready {
		if idx.prereqIndex[state.PrerequisiteKey] == nil {
			idx.prereqIndex[state.PrerequisiteKey] = make(map[string]struct{})
		}
		idx.prereqIndex[state.PrerequisiteKey][key] = struct{}{}
	} else if state.PrerequisiteKey == "" || state.Ready {
		// Clean up prereq index when item becomes ready or prereq cleared.
		for pk, set := range idx.prereqIndex {
			if _, ok := set[key]; ok {
				delete(set, key)
				if len(set) == 0 {
					delete(idx.prereqIndex, pk)
				}
			}
		}
	}
	idx.mu.Unlock()

	if idx.store != nil {
		data, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("recognition: marshal state: %w", err)
		}
		return idx.store.PutRecognition(key, data)
	}
	return nil
}

// MarkRecognized idempotently sets Recognized=true for the (consumer, event)
// pair. Creates the state entry if it doesn't exist. Returns the updated state.
func (idx *Index) MarkRecognized(consumer string, eventID event.EventID) (*RecognitionState, error) {
	existing, err := idx.Get(consumer, eventID)
	if err != nil && err != ErrNotFound {
		return nil, err
	}

	if existing != nil && existing.Recognized {
		cp := *existing
		return &cp, nil // already recognized — idempotent
	}

	var updated RecognitionState
	if existing != nil {
		updated = *existing
	} else {
		updated = RecognitionState{
			ConsumerName: consumer,
			EventID:      eventID,
		}
	}
	updated.Recognized = true
	if err := idx.Put(&updated); err != nil {
		return nil, err
	}
	cp := updated
	return &cp, nil
}

// MarkReady idempotently sets Ready=true and clears DeferredReason.
func (idx *Index) MarkReady(consumer string, eventID event.EventID) error {
	existing, err := idx.Get(consumer, eventID)
	if err != nil {
		return err
	}
	if existing.Ready {
		return nil // already ready — idempotent
	}
	// Work on a copy to avoid racing with concurrent readers.
	updated := *existing
	updated.Ready = true
	updated.DeferredReason = ""
	updated.PrerequisiteKey = ""
	return idx.Put(&updated)
}

// SetDeferred records that a consumer has recognized the event but is
// waiting for a prerequisite. Stores the reason and prerequisite key
// for targeted activation.
func (idx *Index) SetDeferred(consumer string, eventID event.EventID, reason, prereqKey string) error {
	existing, err := idx.Get(consumer, eventID)
	if err != nil && err != ErrNotFound {
		return err
	}
	var updated RecognitionState
	if existing != nil {
		updated = *existing
	} else {
		updated = RecognitionState{
			ConsumerName: consumer,
			EventID:      eventID,
		}
	}
	updated.Recognized = true
	updated.Ready = false
	updated.DeferredReason = reason
	updated.PrerequisiteKey = prereqKey
	return idx.Put(&updated)
}

// IncrementAttempt atomically increments the attempt count and updates
// the last-attempt timestamp for a (consumer, event) pair. Thread-safe.
func (idx *Index) IncrementAttempt(consumer string, eventID event.EventID) {
	key := stateKey(consumer, eventID)
	idx.mu.Lock()
	s, ok := idx.items[key]
	if ok {
		updated := *s
		updated.Attempts++
		updated.LastAttemptUnix = time.Now().Unix()
		idx.items[key] = &updated
		// Persist asynchronously — attempt metadata is best-effort.
		if idx.store != nil {
			data, err := json.Marshal(&updated)
			if err == nil {
				_ = idx.store.PutRecognition(key, data)
			}
		}
	}
	idx.mu.Unlock()
}

// DeferredByPrerequisite returns all deferred states that are waiting on
// the given prerequisite key. Returns deep copies to avoid data races
// with concurrent bus workers. Uses the secondary prerequisite index for
// O(1) lookup instead of scanning all items.
func (idx *Index) DeferredByPrerequisite(prereqKey string) []*RecognitionState {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	keys, ok := idx.prereqIndex[prereqKey]
	if !ok {
		return nil
	}

	result := make([]*RecognitionState, 0, len(keys))
	for key := range keys {
		s, exists := idx.items[key]
		if exists && !s.Ready {
			cp := *s
			result = append(result, &cp)
		}
	}
	return result
}

// LoadFromStore populates the in-memory index from the persistent store,
// rebuilding the prerequisite secondary index. Called during node startup
// for crash recovery.
func (idx *Index) LoadFromStore() error {
	if idx.store == nil {
		return nil
	}
	entries, err := idx.store.ScanRecognitionPrefix("")
	if err != nil {
		return fmt.Errorf("recognition: load from store: %w", err)
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	for key, data := range entries {
		var s RecognitionState
		if err := json.Unmarshal(data, &s); err != nil {
			continue // skip corrupt entries
		}
		idx.items[key] = &s
		// Rebuild prereq secondary index.
		if s.PrerequisiteKey != "" && !s.Ready {
			if idx.prereqIndex[s.PrerequisiteKey] == nil {
				idx.prereqIndex[s.PrerequisiteKey] = make(map[string]struct{})
			}
			idx.prereqIndex[s.PrerequisiteKey][key] = struct{}{}
		}
	}
	return nil
}
