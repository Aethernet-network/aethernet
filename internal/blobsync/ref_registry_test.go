package blobsync

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
	"github.com/Aethernet-network/aethernet/internal/event"
)

func TestRegistry_RegisterAndExtract(t *testing.T) {
	r := NewBlobRefRegistry()
	called := false
	_ = r.Register("TestEvent", "", func(payload []byte) ([]blobstore.BlobRef, error) {
		called = true
		ref, _ := blobstore.NewBlobRef(
			"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
			blobstore.BlobKindEvidence, "")
		return []blobstore.BlobRef{ref}, nil
	})

	refs, err := r.Extract("TestEvent", "", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !called {
		t.Error("extractor was not called")
	}
	if len(refs) != 1 {
		t.Fatalf("got %d refs; want 1", len(refs))
	}
	if refs[0].Kind != blobstore.BlobKindEvidence {
		t.Errorf("Kind = %v; want Evidence", refs[0].Kind)
	}
}

func TestRegistry_NoExtractor_ReturnsNil(t *testing.T) {
	r := NewBlobRefRegistry()
	refs, err := r.Extract("UnknownEvent", "", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if refs != nil {
		t.Errorf("expected nil refs for unknown event type; got %v", refs)
	}
}

func TestRegistry_DuplicateRegister_ReturnsError(t *testing.T) {
	r := NewBlobRefRegistry()
	noop := func([]byte) ([]blobstore.BlobRef, error) { return nil, nil }
	_ = r.Register("Dup", "", noop)
	err := r.Register("Dup", "", noop)
	if err == nil {
		t.Error("expected error on duplicate registration")
	}
}

func TestRegistry_ExtractorReturnsEmptySlice(t *testing.T) {
	r := NewBlobRefRegistry()
	_ = r.Register("Empty", "", func([]byte) ([]blobstore.BlobRef, error) {
		return []blobstore.BlobRef{}, nil
	})
	refs, err := r.Extract("Empty", "", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("got %d refs; want 0", len(refs))
	}
}

func TestRegistry_ExtractorReturnsError(t *testing.T) {
	r := NewBlobRefRegistry()
	_ = r.Register("Err", "", func([]byte) ([]blobstore.BlobRef, error) {
		return nil, fmt.Errorf("extractor failed")
	})
	_, err := r.Extract("Err", "", nil)
	if err == nil {
		t.Error("expected error to propagate from extractor")
	}
}

func TestRegistry_VersionedFallback(t *testing.T) {
	r := NewBlobRefRegistry()
	_ = r.Register("Versioned", "", func([]byte) ([]blobstore.BlobRef, error) {
		ref, _ := blobstore.NewBlobRef(
			"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
			blobstore.BlobKindManifest, "")
		return []blobstore.BlobRef{ref}, nil
	})
	// Request with version "v2" — should fall back to version-agnostic.
	refs, err := r.Extract("Versioned", "v2", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("fallback to version-agnostic failed; got %d refs", len(refs))
	}
}

func TestTaskSubmittedExtractor_ValidHash(t *testing.T) {
	hash := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
	payload, _ := json.Marshal(event.TaskSubmittedPayload{
		Version:          1,
		TaskID:           "task-1",
		ClaimerID:        "worker-1",
		EvidenceBodyHash: hash,
	})

	r := NewBlobRefRegistry()
	RegisterBootstrapExtractors(r)

	refs, err := r.Extract(string(event.EventTypeTaskSubmitted), "", payload)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d refs; want 1", len(refs))
	}
	if refs[0].Kind != blobstore.BlobKindEvidence {
		t.Errorf("Kind = %v; want Evidence", refs[0].Kind)
	}
	if refs[0].HexHash() != hash {
		t.Errorf("HexHash = %s; want %s", refs[0].HexHash(), hash)
	}
	if !refs[0].RequiredForConsensus {
		t.Error("evidence blob should be RequiredForConsensus")
	}
}

func TestTaskSubmittedExtractor_EmptyHash(t *testing.T) {
	payload, _ := json.Marshal(event.TaskSubmittedPayload{
		Version:   1,
		TaskID:    "task-2",
		ClaimerID: "worker-2",
		// EvidenceBodyHash is empty
	})

	r := NewBlobRefRegistry()
	RegisterBootstrapExtractors(r)

	refs, err := r.Extract(string(event.EventTypeTaskSubmitted), "", payload)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("got %d refs; want 0 for empty evidence hash", len(refs))
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := NewBlobRefRegistry()
	var wg sync.WaitGroup
	// Register 20 extractors concurrently.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Register(fmt.Sprintf("Type%d", i), "", func([]byte) ([]blobstore.BlobRef, error) {
				return nil, nil
			})
		}(i)
	}
	// Extract concurrently.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = r.Extract(fmt.Sprintf("Type%d", i), "", nil)
		}(i)
	}
	wg.Wait()
}
