package blobsync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
)

// ── Mock BlobFetcher ────────────────────────────────────────────────────────

type mockFetcher struct {
	mu       sync.Mutex
	blobs    map[[32]byte][]byte          // hash → data
	holders  map[[32]byte][]string        // hash → peer IDs that respond to query
	fetchLog []fetchCall                   // recorded calls for assertions
	failNext map[[32]byte]int             // hash → number of FetchFromPeer failures before success
}

type fetchCall struct {
	PeerID string
	Hash   [32]byte
}

func newMockFetcher() *mockFetcher {
	return &mockFetcher{
		blobs:    make(map[[32]byte][]byte),
		holders:  make(map[[32]byte][]string),
		failNext: make(map[[32]byte]int),
	}
}

func (m *mockFetcher) AddBlob(hash [32]byte, data []byte, holders ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[hash] = data
	if len(holders) > 0 {
		m.holders[hash] = holders
	}
}

func (m *mockFetcher) SetFailCount(hash [32]byte, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext[hash] = n
}

func (m *mockFetcher) FetchFromPeer(ctx context.Context, peerID string, hash [32]byte) ([]byte, error) {
	m.mu.Lock()
	m.fetchLog = append(m.fetchLog, fetchCall{PeerID: peerID, Hash: hash})
	if n, ok := m.failNext[hash]; ok && n > 0 {
		m.failNext[hash] = n - 1
		m.mu.Unlock()
		return nil, fmt.Errorf("mock: fetch failed (remaining failures: %d)", n-1)
	}
	data, ok := m.blobs[hash]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("mock: blob not found")
	}
	return data, nil
}

func (m *mockFetcher) BroadcastQuery(ctx context.Context, hash [32]byte, fanout int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	peers, ok := m.holders[hash]
	if !ok {
		return nil, fmt.Errorf("mock: no holders")
	}
	if fanout < len(peers) {
		return peers[:fanout], nil
	}
	return peers, nil
}

func (m *mockFetcher) FetchLog() []fetchCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]fetchCall, len(m.fetchLog))
	copy(out, m.fetchLog)
	return out
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func makeBlobRef(data []byte, kind blobstore.BlobKind, origin string) blobstore.BlobRef {
	hash := sha256.Sum256(data)
	return blobstore.BlobRef{
		Hash:                 hash,
		Kind:                 kind,
		RequiredForConsensus: kind <= blobstore.BlobKindTrajectory,
		OriginNodeHint:       origin,
	}
}

func newTestEngine(fetcher BlobFetcher, cache *HolderHintCache) (*BlobSyncEngine, *blobstore.SubscribableStore) {
	inner := newMemStore()
	store := blobstore.NewSubscribableStore(inner)
	engine := NewBlobSyncEngine(store, fetcher, cache, 2)
	return engine, store
}

// memStore is a minimal in-memory blobstore.Store for testing.
type memStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemStore() *memStore {
	return &memStore{data: make(map[string][]byte)}
}

func (s *memStore) Put(_ context.Context, data []byte) (string, int64, error) {
	hash := sha256.Sum256(data)
	hex := fmt.Sprintf("%x", hash)
	s.mu.Lock()
	s.data[hex] = data
	s.mu.Unlock()
	return hex, int64(len(data)), nil
}

func (s *memStore) Get(_ context.Context, hash string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.data[hash]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return d, nil
}

func (s *memStore) Has(_ context.Context, hash string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[hash]
	return ok, nil
}

func (s *memStore) Delete(_ context.Context, hash string) error {
	s.mu.Lock()
	delete(s.data, hash)
	s.mu.Unlock()
	return nil
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestEngine_FetchFromOriginHint(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	data := []byte("hello evidence blob")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "origin-peer-1")
	fetcher.AddBlob(ref.Hash, data)

	engine.Start()
	defer engine.Stop()

	engine.RequestFetch(ref)

	// Wait for blob to appear.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.WaitForBlob(ctx, ref.Hash); err != nil {
		t.Fatalf("blob did not arrive: %v", err)
	}

	// Verify the blob is stored correctly.
	got, err := store.GetByHash(ref.Hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("data mismatch: got %q, want %q", got, data)
	}

	// Verify origin was tried (appears in fetch log).
	log := fetcher.FetchLog()
	originFound := false
	for _, call := range log {
		if call.PeerID == "origin-peer-1" {
			originFound = true
			break
		}
	}
	if !originFound {
		t.Error("origin peer was not tried in fetch log")
	}
}

func TestEngine_FetchFromHintCache(t *testing.T) {
	cache := NewHolderHintCache(100)
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, cache)

	data := []byte("cached hint blob")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "")
	fetcher.AddBlob(ref.Hash, data)

	// Pre-populate hint cache.
	cache.Add(HolderHint{
		BlobHash:       ref.Hash,
		HolderPeerID:   "cached-peer",
		ValidUntilUnix: time.Now().Add(10 * time.Minute).Unix(),
	})

	engine.Start()
	defer engine.Stop()

	engine.RequestFetch(ref)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.WaitForBlob(ctx, ref.Hash); err != nil {
		t.Fatalf("blob did not arrive: %v", err)
	}

	// Verify hint cache peer was tried first.
	log := fetcher.FetchLog()
	if len(log) == 0 {
		t.Fatal("no fetch calls recorded")
	}
	if log[0].PeerID != "cached-peer" {
		t.Errorf("first fetch was to %q, want %q", log[0].PeerID, "cached-peer")
	}
}

func TestEngine_FetchViaBroadcastDiscovery(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	data := []byte("broadcast discovery blob")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "")
	// No origin hint, no cache — must discover via broadcast.
	fetcher.AddBlob(ref.Hash, data, "discovered-peer-1", "discovered-peer-2")

	engine.Start()
	defer engine.Stop()

	engine.RequestFetch(ref)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.WaitForBlob(ctx, ref.Hash); err != nil {
		t.Fatalf("blob did not arrive: %v", err)
	}
}

func TestEngine_Deduplication(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	data := []byte("dedup blob")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "origin-1")
	fetcher.AddBlob(ref.Hash, data)

	engine.Start()
	defer engine.Stop()

	// Request the same blob 5 times.
	for i := 0; i < 5; i++ {
		engine.RequestFetch(ref)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.WaitForBlob(ctx, ref.Hash); err != nil {
		t.Fatalf("blob did not arrive: %v", err)
	}

	// Only one fetch session should have executed.
	log := fetcher.FetchLog()
	// At most a few calls (hint + origin + broadcast), but not 5x multiplied.
	if len(log) > 5 {
		t.Errorf("too many fetch calls (%d), dedup may have failed", len(log))
	}
}

func TestEngine_AlreadyLocal_NoFetch(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	data := []byte("already local")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "origin-1")

	// Pre-store the blob.
	if err := store.PutVerified(ref, data); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}

	engine.Start()
	defer engine.Stop()

	engine.RequestFetch(ref)

	// Give the engine a moment to process (it shouldn't).
	time.Sleep(100 * time.Millisecond)

	log := fetcher.FetchLog()
	if len(log) != 0 {
		t.Errorf("fetch was attempted for already-local blob: %d calls", len(log))
	}
}

func TestEngine_Exhaustion(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	data := []byte("unfetchable")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "")
	// Don't add blob to fetcher — all attempts will fail.

	engine.Start()
	defer engine.Stop()

	engine.RequestFetch(ref)

	// Wait long enough for retries to exhaust. With 5 retries, exponential
	// backoff (100ms→200ms→400ms→800ms→2000ms), and per-fetch timeout (10s
	// but mock fails immediately), total is ~3.5s. Use generous deadline.
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	exhausted := false
	for !exhausted {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for fetch exhaustion")
		case <-ticker.C:
			state := engine.FetchStatus(ref.Hash)
			if state == FetchNotStarted {
				exhausted = true
			}
		}
	}

	// Blob should NOT be in store.
	if store.HasByHash(ref.Hash) {
		t.Error("blob should not be stored after exhaustion")
	}
}

func TestEngine_HashMismatch_Rejected(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	data := []byte("correct data")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "bad-peer")

	// Fetcher returns wrong data for this hash.
	fetcher.AddBlob(ref.Hash, []byte("CORRUPTED DATA"))

	engine.Start()
	defer engine.Stop()

	engine.RequestFetch(ref)

	// Wait for retries to exhaust.
	time.Sleep(3 * time.Second)

	if store.HasByHash(ref.Hash) {
		t.Error("corrupted blob should not be stored")
	}
}

func TestEngine_RetryOnFailure(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	data := []byte("retry me")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "retry-peer")
	fetcher.AddBlob(ref.Hash, data)
	// Fail the first 2 FetchFromPeer calls, then succeed.
	fetcher.SetFailCount(ref.Hash, 2)

	engine.Start()
	defer engine.Stop()

	engine.RequestFetch(ref)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.WaitForBlob(ctx, ref.Hash); err != nil {
		t.Fatalf("blob did not arrive after retries: %v", err)
	}
}

func TestEngine_FetchStatus(t *testing.T) {
	fetcher := newMockFetcher()
	engine, _ := newTestEngine(fetcher, nil)

	data := []byte("status check")
	ref := makeBlobRef(data, blobstore.BlobKindEvidence, "")

	// Before any fetch, status should be NotStarted.
	if s := engine.FetchStatus(ref.Hash); s != FetchNotStarted {
		t.Errorf("initial status = %d, want FetchNotStarted", s)
	}
}

func TestEngine_ConcurrentFetches(t *testing.T) {
	fetcher := newMockFetcher()
	engine, store := newTestEngine(fetcher, nil)

	engine.Start()
	defer engine.Stop()

	var refs []blobstore.BlobRef
	for i := 0; i < 10; i++ {
		data := []byte(fmt.Sprintf("concurrent-blob-%d", i))
		ref := makeBlobRef(data, blobstore.BlobKindEvidence, fmt.Sprintf("peer-%d", i))
		fetcher.AddBlob(ref.Hash, data)
		refs = append(refs, ref)
	}

	// Request all concurrently.
	var wg sync.WaitGroup
	for _, ref := range refs {
		wg.Add(1)
		go func(r blobstore.BlobRef) {
			defer wg.Done()
			engine.RequestFetch(r)
		}(ref)
	}
	wg.Wait()

	// Wait for all blobs.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, ref := range refs {
		if err := store.WaitForBlob(ctx, ref.Hash); err != nil {
			t.Errorf("blob %x did not arrive: %v", ref.Hash[:4], err)
		}
	}
}
