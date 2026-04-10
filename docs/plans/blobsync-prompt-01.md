# BlobSync Prompt 01 Plan — BlobRef Extraction and Blob Classes

**Date:** 2026-04-10
**Status:** Awaiting approval

## Verified Facts

- **Evidence hash field**: `TaskSubmittedPayload.EvidenceBodyHash` (string, SHA-256 hex) at `internal/event/event.go:711`
- **Trajectory blob field**: `TrajectoryCommitPayload.CheckpointHash` (string, SHA-256 hex) at `internal/event/trajectory.go:57` — noted, not wired in this prompt
- **BlobStore interface**: in `internal/blobstore/blobstore.go`. Uses hex-encoded SHA-256 strings for hashes, not `[32]byte`. BlobRef.Hash will be `[32]byte` per the locked design, with helpers to convert to/from hex strings.
- **Package `internal/blobsync/`**: does not exist — will create
- **`event.Event`**: will NOT be modified (per design §5.1, ChatGPT finding 8.1.2)

## Hash Type Decision

The locked design specifies `BlobRef.Hash` as `[32]byte`. The existing BlobStore uses hex-encoded strings. I'll keep `[32]byte` in BlobRef (matching the design) and provide `HexHash() string` and `BlobRefFromHex(hex string) (BlobRef, error)` helpers.

## Files to Create

- `internal/blobstore/refs.go` — BlobRef, BlobKind, ConsensusBlocking
- `internal/blobstore/refs_test.go` — type tests
- `internal/blobsync/doc.go` — package documentation
- `internal/blobsync/ref_registry.go` — BlobRefRegistry
- `internal/blobsync/extractors.go` — TaskSubmitted extractor
- `internal/blobsync/ref_registry_test.go` — registry + extractor tests

## Files to Modify

None. event.Event is unmodified.

## Zero-Value Safety

`BlobKindEvidence` is iota 0. A zero-value BlobRef defaults to Evidence kind → `ConsensusBlocking() == true`. This is the conservative default: an uninitialized ref is treated as consensus-blocking, which is safer than accidentally treating it as optional.

## Test Strategy

refs_test.go: ConsensusBlocking truth table for all 7 kinds, JSON roundtrip, zero-value check.
ref_registry_test.go: register+extract, no-extractor returns nil, duplicate error, empty slice, error propagation, TaskSubmitted extractor, zero evidence hash, concurrent access.

## Dependencies

`blobsync → blobstore` (one-way). `blobsync` also imports `internal/event` for payload parsing. No reverse dependency.
