package blobsync

import (
	"context"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// BlobDemandConsumer is a CommitConsumer that detects missing blobs referenced
// by committed events and enqueues fetch requests via the BlobSyncEngine.
//
// Always-ready (no deferred activation — per lesson from commit 0e6d8cc).
// Idempotent: delegates deduplication to the engine's in-flight tracking.
// Non-blocking: if the engine's demand queue is full, RequestFetch drops the
// request with a log warning rather than blocking the commit bus.
type BlobDemandConsumer struct {
	registry *BlobRefRegistry
	store    *blobstore.SubscribableStore
	engine   *BlobSyncEngine
}

// NewBlobDemandConsumer creates a consumer that bridges the recognition fabric
// to BlobSync. It extracts BlobRefs from committed events, checks the local
// store, and enqueues fetches for any missing blobs.
func NewBlobDemandConsumer(
	registry *BlobRefRegistry,
	store *blobstore.SubscribableStore,
	engine *BlobSyncEngine,
) *BlobDemandConsumer {
	return &BlobDemandConsumer{
		registry: registry,
		store:    store,
		engine:   engine,
	}
}

// Name returns the unique consumer identifier.
func (c *BlobDemandConsumer) Name() string { return "blob_demand" }

// Interested returns true for all events. The BlobRefRegistry handles
// filtering — Extract returns nil for event types with no registered
// extractors. This generalizes cleanly as new event types register extractors.
func (c *BlobDemandConsumer) Interested(_ *event.Event) bool { return true }

// Ready returns always-ready. Per the lesson from commit 0e6d8cc, deferred
// activation signals are not retroactive — consumers processing events whose
// prerequisites are guaranteed by causal ordering must use always-ready.
func (c *BlobDemandConsumer) Ready(_ context.Context, _ *event.Event, _ recognition.ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume extracts BlobRefs from the event, checks the local store for each,
// and enqueues fetch requests for missing blobs via the engine. Returns nil
// always — this consumer detects demand and enqueues; it does not wait for
// fetch completion. The engine's in-flight deduplication prevents duplicate
// fetch sessions for the same hash.
func (c *BlobDemandConsumer) Consume(_ context.Context, ev *event.Event) error {
	refs, err := c.registry.Extract(string(ev.Type), "", ev.Payload)
	if err != nil {
		slog.Debug("blob_demand: extract failed",
			"event_id", ev.ID, "type", ev.Type, "err", err)
		return nil // extraction failure is not fatal — log and move on
	}
	if len(refs) == 0 {
		return nil
	}

	for _, ref := range refs {
		if c.store.HasByHash(ref.Hash) {
			continue
		}
		c.engine.RequestFetch(ref)
		slog.Info("blob_demand: enqueued fetch for missing blob",
			"hash", ref.HexHash(),
			"kind", ref.Kind,
			"consensus_blocking", ref.ConsensusBlocking(),
			"event_id", ev.ID,
		)
	}

	return nil
}

// Compile-time assertion.
var _ recognition.CommitConsumer = (*BlobDemandConsumer)(nil)
