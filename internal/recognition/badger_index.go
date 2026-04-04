package recognition

import "sync"

// BadgerIndexStore adapts the main store's PutMeta/GetMeta pattern for
// recognition state persistence. It uses the "rcg:" prefix namespace.
//
// This file defines the adapter. The actual store.Store implementation
// of PutRecognition/GetRecognition/etc. will be added to the store
// package when the recognition fabric is wired into the node (Prompt 2+).
// Until then, tests use MemoryIndexStore.

const recognitionPrefix = "rcg:"

// StoreAdapter wraps a generic key-value store to satisfy IndexStore.
// The underlying store must support prefix-scanned reads.
type StoreAdapter struct {
	put    func(key string, data []byte) error
	get    func(key string) ([]byte, error)
	del    func(key string) error
	scan   func(prefix string) (map[string][]byte, error)
}

// NewStoreAdapter creates an IndexStore from generic key-value operations.
func NewStoreAdapter(
	put func(key string, data []byte) error,
	get func(key string) ([]byte, error),
	del func(key string) error,
	scan func(prefix string) (map[string][]byte, error),
) *StoreAdapter {
	return &StoreAdapter{put: put, get: get, del: del, scan: scan}
}

func (s *StoreAdapter) PutRecognition(key string, data []byte) error {
	return s.put(recognitionPrefix+key, data)
}

func (s *StoreAdapter) GetRecognition(key string) ([]byte, error) {
	return s.get(recognitionPrefix + key)
}

func (s *StoreAdapter) DeleteRecognition(key string) error {
	return s.del(recognitionPrefix + key)
}

func (s *StoreAdapter) ScanRecognitionPrefix(prefix string) (map[string][]byte, error) {
	raw, err := s.scan(recognitionPrefix + prefix)
	if err != nil {
		return nil, err
	}
	// Strip the "rcg:" prefix from returned keys.
	result := make(map[string][]byte, len(raw))
	for k, v := range raw {
		if len(k) > len(recognitionPrefix) {
			result[k[len(recognitionPrefix):]] = v
		}
	}
	return result, nil
}

// MemoryIndexStore is a thread-safe in-memory IndexStore for testing.
type MemoryIndexStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryIndexStore creates a test-only in-memory index store.
func NewMemoryIndexStore() *MemoryIndexStore {
	return &MemoryIndexStore{data: make(map[string][]byte)}
}

func (m *MemoryIndexStore) PutRecognition(key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte{}, data...)
	return nil
}

func (m *MemoryIndexStore) GetRecognition(key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return d, nil
}

func (m *MemoryIndexStore) DeleteRecognition(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *MemoryIndexStore) ScanRecognitionPrefix(prefix string) (map[string][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string][]byte)
	for k, v := range m.data {
		if len(prefix) == 0 || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result[k] = v
		}
	}
	return result, nil
}
