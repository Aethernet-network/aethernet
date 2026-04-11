package roundprogress

import "sync"

// SnapshotStore persists the latest RoundProgressSnapshot per
// (roundID, validatorID, analyzerFamily). The snapshot IS the authoritative
// state — no replay needed on restart.
type SnapshotStore interface {
	// Get returns the latest snapshot for the given key, or nil if none exists.
	Get(roundID, validatorID, analyzerFamily string) (*RoundProgressSnapshot, error)

	// GetAllForRound returns all snapshots for a given round.
	GetAllForRound(roundID string) ([]*RoundProgressSnapshot, error)

	// Put stores or replaces the snapshot for the given key.
	Put(snap *RoundProgressSnapshot) error

	// DeleteRound removes all snapshots for a finalized round (garbage collection).
	DeleteRound(roundID string) error
}

// MemorySnapshotStore is an in-memory SnapshotStore for testing.
// Thread-safe via sync.Mutex.
type MemorySnapshotStore struct {
	mu    sync.Mutex
	snaps map[string]*RoundProgressSnapshot // key: "roundID:validatorID:family"
}

// NewMemorySnapshotStore creates an in-memory store.
func NewMemorySnapshotStore() *MemorySnapshotStore {
	return &MemorySnapshotStore{
		snaps: make(map[string]*RoundProgressSnapshot),
	}
}

func snapKey(roundID, validatorID, family string) string {
	return roundID + ":" + validatorID + ":" + family
}

func (s *MemorySnapshotStore) Get(roundID, validatorID, family string) (*RoundProgressSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.snaps[snapKey(roundID, validatorID, family)]
	if !ok {
		return nil, nil
	}
	cp := *snap
	return &cp, nil
}

func (s *MemorySnapshotStore) GetAllForRound(roundID string) ([]*RoundProgressSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := roundID + ":"
	var result []*RoundProgressSnapshot
	for k, v := range s.snaps {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			cp := *v
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (s *MemorySnapshotStore) Put(snap *RoundProgressSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *snap
	s.snaps[snapKey(snap.RoundID, snap.ValidatorID, snap.AnalyzerFamily)] = &cp
	return nil
}

func (s *MemorySnapshotStore) DeleteRound(roundID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := roundID + ":"
	for k := range s.snaps {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(s.snaps, k)
		}
	}
	return nil
}
