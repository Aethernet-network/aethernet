package blobsync

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
)

// FetchState tracks the lifecycle of a single blob fetch.
type FetchState int

const (
	FetchNotStarted FetchState = iota
	FetchInFlight
	FetchCompleted
	FetchFailed
)

// FetchSession tracks per-fetch state.
type FetchSession struct {
	Ref       blobstore.BlobRef
	Policy    BlobFetchPolicy
	State     FetchState
	Attempts  uint32
	StartedAt time.Time
	LastError error
	done      chan struct{}
}

// BlobFetcher abstracts the network layer for fetching blobs from peers.
// Implemented by the transport layer.
type BlobFetcher interface {
	// FetchFromPeer requests a blob from a specific peer. Returns the blob
	// data or error. The caller is responsible for hash verification.
	FetchFromPeer(ctx context.Context, peerID string, hash [32]byte) ([]byte, error)

	// BroadcastQuery sends a "do you have this?" query to up to fanout
	// peers and returns the peer IDs that responded affirmatively.
	BroadcastQuery(ctx context.Context, hash [32]byte, fanout int) ([]string, error)
}

// BlobSyncEngine is the fetch coordinator. Single instance per node.
// Receives fetch requests, deduplicates them, and executes fetches
// using the origin-first → hint-cache → broadcast-discovery algorithm.
type BlobSyncEngine struct {
	store     *blobstore.SubscribableStore
	fetcher   BlobFetcher
	hintCache *HolderHintCache

	demandQueue chan blobstore.BlobRef
	inFlight    map[[32]byte]*FetchSession
	mu          sync.Mutex

	workers int
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewBlobSyncEngine creates an engine. Call Start() to begin processing.
func NewBlobSyncEngine(
	store *blobstore.SubscribableStore,
	fetcher BlobFetcher,
	hintCache *HolderHintCache,
	workers int,
) *BlobSyncEngine {
	if workers <= 0 {
		workers = 4
	}
	return &BlobSyncEngine{
		store:       store,
		fetcher:     fetcher,
		hintCache:   hintCache,
		demandQueue: make(chan blobstore.BlobRef, 1024),
		inFlight:    make(map[[32]byte]*FetchSession),
		workers:     workers,
	}
}

// RequestFetch enqueues a fetch request. Deduplicates: if a fetch for
// this hash is already in-flight or the blob is already local, does nothing.
func (e *BlobSyncEngine) RequestFetch(ref blobstore.BlobRef) {
	// Already have it locally?
	if e.store.HasByHash(ref.Hash) {
		return
	}

	e.mu.Lock()
	if _, exists := e.inFlight[ref.Hash]; exists {
		e.mu.Unlock()
		return // already in-flight
	}
	e.inFlight[ref.Hash] = &FetchSession{
		Ref:       ref,
		Policy:    PolicyForBlobKind(ref.Kind),
		State:     FetchInFlight,
		StartedAt: time.Now(),
		done:      make(chan struct{}),
	}
	e.mu.Unlock()

	// Non-blocking enqueue.
	select {
	case e.demandQueue <- ref:
	default:
		slog.Warn("blobsync: demand queue full, dropping fetch request",
			"hash", ref.HexHash(), "kind", ref.Kind)
		e.mu.Lock()
		delete(e.inFlight, ref.Hash)
		e.mu.Unlock()
	}
}

// FetchStatus returns the current state of a fetch.
func (e *BlobSyncEngine) FetchStatus(hash [32]byte) FetchState {
	if e.store.HasByHash(hash) {
		return FetchCompleted
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if session, ok := e.inFlight[hash]; ok {
		return session.State
	}
	return FetchNotStarted
}

// Start launches worker goroutines. Lifecycle is managed by the caller.
func (e *BlobSyncEngine) Start() {
	e.ctx, e.cancel = context.WithCancel(context.Background())
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker()
	}
	slog.Info("blobsync: engine started", "workers", e.workers)
}

// Stop cancels the context and waits for workers to finish.
func (e *BlobSyncEngine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
	slog.Info("blobsync: engine stopped")
}

func (e *BlobSyncEngine) worker() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case ref, ok := <-e.demandQueue:
			if !ok {
				return
			}
			e.executeFetch(ref)
		}
	}
}

func (e *BlobSyncEngine) executeFetch(ref blobstore.BlobRef) {
	e.mu.Lock()
	session, ok := e.inFlight[ref.Hash]
	if !ok {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	policy := session.Policy
	deadline := time.Now().Add(time.Duration(policy.TotalDeadlineMs) * time.Millisecond)

	var lastErr error
	for attempt := uint32(0); attempt < policy.MaxRetries; attempt++ {
		if time.Now().After(deadline) {
			break
		}

		session.Attempts = attempt + 1
		fetchCtx, fetchCancel := context.WithTimeout(e.ctx,
			time.Duration(policy.PerFetchTimeoutMs)*time.Millisecond)

		data, err := e.tryFetch(fetchCtx, ref, attempt)
		fetchCancel()

		if err == nil && data != nil {
			// Verify and store.
			if storeErr := e.store.PutVerified(ref, data); storeErr != nil {
				lastErr = storeErr
				slog.Warn("blobsync: hash verification failed",
					"hash", ref.HexHash(), "err", storeErr)
				continue
			}

			// Success.
			e.mu.Lock()
			session.State = FetchCompleted
			close(session.done)
			delete(e.inFlight, ref.Hash)
			e.mu.Unlock()

			slog.Info("blobsync: blob fetched",
				"hash", ref.HexHash(),
				"kind", ref.Kind,
				"size", len(data),
				"attempts", session.Attempts)
			return
		}

		lastErr = err
		// Backoff before retry.
		backoff := time.Duration(policy.InitialBackoffMs) * time.Millisecond
		for i := uint32(0); i < attempt; i++ {
			backoff *= 2
			if backoff > time.Duration(policy.MaxBackoffMs)*time.Millisecond {
				backoff = time.Duration(policy.MaxBackoffMs) * time.Millisecond
				break
			}
		}
		select {
		case <-time.After(backoff):
		case <-e.ctx.Done():
			break
		}
	}

	// Exhausted.
	e.mu.Lock()
	session.State = FetchFailed
	session.LastError = lastErr
	close(session.done)
	delete(e.inFlight, ref.Hash)
	e.mu.Unlock()

	slog.Warn("blobsync: fetch exhausted",
		"hash", ref.HexHash(),
		"kind", ref.Kind,
		"attempts", session.Attempts,
		"err", lastErr)
}

func (e *BlobSyncEngine) tryFetch(ctx context.Context, ref blobstore.BlobRef, attempt uint32) ([]byte, error) {
	// 1. Try hint cache.
	if e.hintCache != nil {
		hints := e.hintCache.Get(ref.Hash)
		for _, hint := range hints {
			data, err := e.fetcher.FetchFromPeer(ctx, hint.HolderPeerID, ref.Hash)
			if err == nil {
				return data, nil
			}
			e.hintCache.Remove(ref.Hash, hint.HolderPeerID)
		}
	}

	// 2. Try origin hint.
	if ref.OriginNodeHint != "" {
		data, err := e.fetcher.FetchFromPeer(ctx, ref.OriginNodeHint, ref.Hash)
		if err == nil {
			return data, nil
		}
	}

	// 3. Broadcast discovery (bounded fanout).
	if e.fetcher != nil {
		policy := PolicyForBlobKind(ref.Kind)
		peers, err := e.fetcher.BroadcastQuery(ctx, ref.Hash, int(policy.DiscoveryFanout))
		if err == nil {
			for _, peerID := range peers {
				data, fetchErr := e.fetcher.FetchFromPeer(ctx, peerID, ref.Hash)
				if fetchErr == nil {
					return data, nil
				}
			}
		}
	}

	return nil, lastErrorOrDefault(ref)
}

func lastErrorOrDefault(ref blobstore.BlobRef) error {
	return &FetchError{Hash: ref.Hash, Message: "all fetch attempts exhausted"}
}

// FetchError is returned when a blob fetch fails.
type FetchError struct {
	Hash    [32]byte
	Message string
}

func (e *FetchError) Error() string {
	return "blobsync: " + e.Message
}
