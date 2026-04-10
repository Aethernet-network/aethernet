package blobsync

import "github.com/Aethernet-network/aethernet/internal/blobstore"

// BlobFetchPolicy controls fetch behavior per blob class.
type BlobFetchPolicy struct {
	MaxRetries          uint32
	InitialBackoffMs    uint32
	MaxBackoffMs        uint32
	OriginFirst         bool
	DiscoveryFanout     uint32
	PerFetchTimeoutMs   uint32
	TotalDeadlineMs     uint32
	AbstainOnExhaustion bool
}

// ConsensusBlockingPolicy is the default for consensus-blocking blobs.
var ConsensusBlockingPolicy = BlobFetchPolicy{
	MaxRetries:          5,
	InitialBackoffMs:    100,
	MaxBackoffMs:        2000,
	OriginFirst:         true,
	DiscoveryFanout:     3,
	PerFetchTimeoutMs:   10000,
	TotalDeadlineMs:     30000,
	AbstainOnExhaustion: true,
}

// InformationalPolicy is the default for non-consensus-blocking blobs.
var InformationalPolicy = BlobFetchPolicy{
	MaxRetries:          2,
	InitialBackoffMs:    500,
	MaxBackoffMs:        5000,
	OriginFirst:         true,
	DiscoveryFanout:     1,
	PerFetchTimeoutMs:   15000,
	TotalDeadlineMs:     60000,
	AbstainOnExhaustion: false,
}

// PolicyForBlobKind returns the fetch policy for a given blob kind.
func PolicyForBlobKind(kind blobstore.BlobKind) BlobFetchPolicy {
	ref := blobstore.BlobRef{Kind: kind}
	if ref.ConsensusBlocking() {
		return ConsensusBlockingPolicy
	}
	return InformationalPolicy
}
