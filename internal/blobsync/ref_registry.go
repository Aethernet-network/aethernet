package blobsync

import (
	"fmt"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
)

// BlobRefExtractor extracts blob references from a raw event payload.
// Registered per (event_type, payload_version) in the BlobRefRegistry.
type BlobRefExtractor func(payload []byte) ([]blobstore.BlobRef, error)

// BlobRefRegistry maps (event_type, payload_version) to extractors.
// This keeps event.Event clean of blob-specific methods (per locked
// design §5.1 and ChatGPT finding 8.1.2). New event types register
// their extractors at startup.
type BlobRefRegistry struct {
	mu         sync.RWMutex
	extractors map[string]BlobRefExtractor // key: "eventType:payloadVersion"
}

// NewBlobRefRegistry creates an empty registry.
func NewBlobRefRegistry() *BlobRefRegistry {
	return &BlobRefRegistry{
		extractors: make(map[string]BlobRefExtractor),
	}
}

func registryKey(eventType, payloadVersion string) string {
	return eventType + ":" + payloadVersion
}

// Register adds an extractor for a given event type and payload version.
// If payloadVersion is empty, the extractor is version-agnostic.
// Returns error if an extractor is already registered for the same key.
func (r *BlobRefRegistry) Register(eventType, payloadVersion string, extractor BlobRefExtractor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := registryKey(eventType, payloadVersion)
	if _, exists := r.extractors[key]; exists {
		return fmt.Errorf("blobsync: extractor already registered for %q", key)
	}
	r.extractors[key] = extractor
	return nil
}

// Extract returns all BlobRefs for a given event payload. Returns nil, nil
// for event types with no registered extractor. Tries the versioned key
// first, then falls back to the version-agnostic key.
func (r *BlobRefRegistry) Extract(eventType, payloadVersion string, payload []byte) ([]blobstore.BlobRef, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try versioned key first.
	key := registryKey(eventType, payloadVersion)
	if extractor, ok := r.extractors[key]; ok {
		return extractor(payload)
	}
	// Fall back to version-agnostic key.
	if payloadVersion != "" {
		agnosticKey := registryKey(eventType, "")
		if extractor, ok := r.extractors[agnosticKey]; ok {
			return extractor(payload)
		}
	}
	return nil, nil
}
