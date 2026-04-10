package blobsync

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
)

func TestPolicyForBlobKind(t *testing.T) {
	consensusKinds := []blobstore.BlobKind{
		blobstore.BlobKindEvidence,
		blobstore.BlobKindManifest,
		blobstore.BlobKindTrajectory,
	}
	for _, k := range consensusKinds {
		p := PolicyForBlobKind(k)
		if !p.AbstainOnExhaustion {
			t.Errorf("%s: expected AbstainOnExhaustion=true", k)
		}
		if p.MaxRetries != 5 {
			t.Errorf("%s: MaxRetries=%d; want 5", k, p.MaxRetries)
		}
	}

	infoKinds := []blobstore.BlobKind{
		blobstore.BlobKindMethodology,
		blobstore.BlobKindCitation,
		blobstore.BlobKindDiagnostic,
		blobstore.BlobKindArchival,
	}
	for _, k := range infoKinds {
		p := PolicyForBlobKind(k)
		if p.AbstainOnExhaustion {
			t.Errorf("%s: expected AbstainOnExhaustion=false", k)
		}
		if p.MaxRetries != 2 {
			t.Errorf("%s: MaxRetries=%d; want 2", k, p.MaxRetries)
		}
	}
}
