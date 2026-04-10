# BlobSync Prompt 02 Plan — Transport Channel and Engine

**Date:** 2026-04-10
**Status:** Awaiting approval

## Verified Facts

- **Message types are strings** (`"v2_*"` prefix for V2, plain strings for V1)
- **Existing blob messages exist**: `MsgBlobRequest` and `MsgBlobResponse` in `protocol.go` for body-completion fallback. I will reuse these for simple fetch (they carry Hash+Data). I will add new `MsgBlobQuery` and `MsgBlobQueryResponse` for discovery ("do you have this?").
- **No request-response correlation** — hash serves as implicit correlation key, matching the existing body-fetch pattern. Pending request tracking via `map[[32]byte]chan *BlobQueryResponse`.
- **Send is non-blocking** (256-slot buffered channel, returns error on full)
- **Per-message size limit**: 4 MiB — blob data must be chunked if larger
- **Peer iteration**: snapshot under `n.mu.RLock()`, send after releasing lock (no lock during sends)
- **Startup**: BlobSyncEngine must be created before `node.Start()`, lifecycle managed by caller's signal wait loop (per lesson `1cfb8ed`)

## Key Design Decisions

1. **Reuse existing MsgBlobRequest/MsgBlobResponse** for fetch. Add only `MsgBlobQuery`/`MsgBlobQueryResponse` for discovery.
2. **Request correlation**: hash-based (same pattern as existing body fetch). Pending requests stored in `map[[32]byte]chan response`.
3. **No chunking in v1**: blobs are evidence bodies (typically 10-50KB). 4 MiB limit is far above. Chunking deferred to v2 when data ingestion manifests arrive.
4. **BlobServingReputation**: companion store in `internal/blobsync/` under `blobserve:` BadgerDB prefix (cleaner than extending ValidatorReputationStore).
5. **Worker pool**: 4 workers draining the demand queue (configurable).
6. **HolderHintCache**: simplified for v1 — no signature verification (signed hints deferred to when cross-node trust is needed). Bounded LRU with TTL expiry.

## Files to Create

- `internal/blobstore/subscribe.go` — PutVerified, Subscribe, WaitForBlob
- `internal/blobstore/subscribe_test.go`
- `internal/blobsync/holder_cache.go` + test
- `internal/blobsync/policy.go` + test
- `internal/blobsync/protocol.go` — new message types
- `internal/blobsync/transport.go` — send/receive blob messages
- `internal/blobsync/engine.go` + test — fetch coordinator
- `internal/blobsync/serving_reputation.go` — per-peer serving stats

## Files to Modify

- `internal/network/protocol.go` — add MsgBlobQuery, MsgBlobQueryResponse constants
- `internal/network/node.go` — add handleBlobQuery handler in switch
- `internal/network/compat.go` — add new types to IsV2Message
- `cmd/node/main.go` — wire BlobSyncEngine lifecycle

## Test Strategy

BlobStore extensions: PutVerified match/mismatch, Subscribe notification, WaitForBlob timeout.
HolderHintCache: add/get, expiry, LRU eviction, blacklist.
Policy: kind→policy mapping for all 7 kinds.
Engine: deduplication, origin-first fetch, hint cache hit, exhaustion.

## Dependencies

`blobsync → blobstore, network, crypto`. No reverse dependencies.
