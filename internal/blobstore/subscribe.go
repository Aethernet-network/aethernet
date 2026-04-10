package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// subscriberRegistry tracks channels waiting for specific blobs to arrive.
// Thread-safe. Used by Subscribe/WaitForBlob and signaled by PutVerified/Put.
type subscriberRegistry struct {
	mu   sync.Mutex
	subs map[[32]byte][]chan struct{}
}

func newSubscriberRegistry() *subscriberRegistry {
	return &subscriberRegistry{
		subs: make(map[[32]byte][]chan struct{}),
	}
}

// subscribe returns a channel that closes when the blob with the given
// hash becomes available. If called for a hash that's already been
// signaled, the caller should check Has() first.
func (r *subscriberRegistry) subscribe(hash [32]byte) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan struct{})
	r.subs[hash] = append(r.subs[hash], ch)
	return ch
}

// signal closes all subscriber channels for the given hash and removes
// them from the registry. Must NOT be called while holding the BlobStore's
// main lock (lock reentrancy risk per lessons.md).
func (r *subscriberRegistry) signal(hash [32]byte) {
	r.mu.Lock()
	channels, ok := r.subs[hash]
	if ok {
		delete(r.subs, hash)
	}
	r.mu.Unlock()

	for _, ch := range channels {
		close(ch)
	}
}

// SubscribableStore wraps a Store with subscription and verified-put support.
type SubscribableStore struct {
	inner Store
	subs  *subscriberRegistry
}

// NewSubscribableStore wraps an existing Store with subscription support.
func NewSubscribableStore(inner Store) *SubscribableStore {
	return &SubscribableStore{
		inner: inner,
		subs:  newSubscriberRegistry(),
	}
}

// Put delegates to the inner store and signals any subscribers.
func (s *SubscribableStore) Put(ctx context.Context, data []byte) (string, int64, error) {
	hash, size, err := s.inner.Put(ctx, data)
	if err != nil {
		return hash, size, err
	}
	// Signal subscribers. Convert hex hash to [32]byte.
	if decoded, decErr := hex.DecodeString(hash); decErr == nil && len(decoded) == 32 {
		var h [32]byte
		copy(h[:], decoded)
		s.subs.signal(h)
	}
	return hash, size, nil
}

// Get delegates to the inner store.
func (s *SubscribableStore) Get(ctx context.Context, hash string) ([]byte, error) {
	return s.inner.Get(ctx, hash)
}

// Has delegates to the inner store.
func (s *SubscribableStore) Has(ctx context.Context, hash string) (bool, error) {
	return s.inner.Has(ctx, hash)
}

// Delete delegates to the inner store.
func (s *SubscribableStore) Delete(ctx context.Context, hash string) error {
	return s.inner.Delete(ctx, hash)
}

// PutVerified writes bytes after verifying they SHA-256 hash to ref.Hash.
// Returns error on mismatch without storing. Signals subscribers on success.
func (s *SubscribableStore) PutVerified(ref BlobRef, data []byte) error {
	actual := sha256.Sum256(data)
	if actual != ref.Hash {
		return fmt.Errorf("%w: expected %x, got %x", ErrHashMismatch,
			ref.Hash, actual)
	}
	hexHash := ref.HexHash()
	_, _, err := s.inner.Put(context.Background(), data)
	if err != nil {
		return err
	}
	// Signal after successful store (not while holding any store lock).
	s.subs.signal(ref.Hash)
	_ = hexHash // used for logging if needed
	return nil
}

// Subscribe returns a channel that closes when the blob with the given
// hash becomes locally available. If the blob is already present, the
// returned channel is immediately closed.
func (s *SubscribableStore) Subscribe(hash [32]byte) <-chan struct{} {
	hexHash := hex.EncodeToString(hash[:])
	exists, _ := s.inner.Has(context.Background(), hexHash)
	if exists {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.subs.subscribe(hash)
}

// WaitForBlob blocks until the blob is locally available or the context
// expires. Returns ctx.Err() if the context expires first.
func (s *SubscribableStore) WaitForBlob(ctx context.Context, hash [32]byte) error {
	ch := s.Subscribe(hash)
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// HasByHash checks existence using [32]byte hash (BlobSync native format).
func (s *SubscribableStore) HasByHash(hash [32]byte) bool {
	hexHash := hex.EncodeToString(hash[:])
	exists, _ := s.inner.Has(context.Background(), hexHash)
	return exists
}

// GetByHash retrieves a blob using [32]byte hash.
func (s *SubscribableStore) GetByHash(hash [32]byte) ([]byte, error) {
	hexHash := hex.EncodeToString(hash[:])
	return s.inner.Get(context.Background(), hexHash)
}
