// Package blobsync implements cross-node blob data availability for
// AetherNet's multi-validator verification consensus.
//
// The package provides the BlobRefRegistry for extracting typed blob
// references from DAG event payloads, and (in future prompts) the
// BlobSyncEngine, BlobTransport, and BlobDemandConsumer for replicating
// blobs across validator nodes.
//
// Design reference: docs/blobsync-design.md
//
// Layer: Infrastructure — importable by any layer.
package blobsync
