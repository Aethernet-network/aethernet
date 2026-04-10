package blobstore

import (
	"encoding/hex"
	"fmt"
)

// BlobKind identifies what role a blob plays in the protocol.
// The iota ordering is deliberate: BlobKindEvidence is 0 so that a
// zero-value BlobRef defaults to consensus-blocking (conservative).
type BlobKind uint8

const (
	BlobKindEvidence    BlobKind = iota // submission evidence, consensus-blocking
	BlobKindManifest                    // data ingestion manifest, consensus-blocking
	BlobKindMethodology                 // methodology documentation, informational
	BlobKindTrajectory                  // trajectory artifact, consensus-blocking if primary
	BlobKindCitation                    // referenced prior work, informational
	BlobKindDiagnostic                  // analyzer output artifact, informational
	BlobKindArchival                    // historical record, optional
)

var blobKindNames = map[BlobKind]string{
	BlobKindEvidence:    "evidence",
	BlobKindManifest:    "manifest",
	BlobKindMethodology: "methodology",
	BlobKindTrajectory:  "trajectory",
	BlobKindCitation:    "citation",
	BlobKindDiagnostic:  "diagnostic",
	BlobKindArchival:    "archival",
}

func (k BlobKind) String() string {
	if name, ok := blobKindNames[k]; ok {
		return name
	}
	return fmt.Sprintf("BlobKind(%d)", int(k))
}

// BlobRef is a first-class typed reference to a blob. It replaces raw
// hash strings wherever the protocol needs to know about a blob's
// existence, class, and metadata.
//
// Per the locked design (docs/blobsync-design.md §5.1), events carry
// BlobRef lists via the extractor registry, not direct fields on the
// canonical event struct.
type BlobRef struct {
	Hash                 [32]byte // SHA-256 of canonical blob bytes
	Kind                 BlobKind
	SizeHint             uint64 // bytes, 0 if unknown
	RequiredForConsensus bool   // derived from Kind, stored for quick access
	OriginNodeHint       string // validator ID that published the referencing event
	PersistenceClass     uint8  // 0=hot, 1=warm, 2=cold
}

// ConsensusBlocking returns true if this blob must be available for a
// validator to emit a non-abstain vote. This is the source of truth;
// RequiredForConsensus is a cached copy for quick access.
func (b BlobRef) ConsensusBlocking() bool {
	switch b.Kind {
	case BlobKindEvidence, BlobKindManifest, BlobKindTrajectory:
		return true
	default:
		return false
	}
}

// HexHash returns the hash as a hex-encoded string, matching the format
// used by the existing BlobStore interface.
func (b BlobRef) HexHash() string {
	return hex.EncodeToString(b.Hash[:])
}

// NewBlobRef creates a BlobRef from a hex-encoded hash string. Sets
// RequiredForConsensus from ConsensusBlocking() and PersistenceClass
// from the kind (consensus-blocking → hot, otherwise warm).
func NewBlobRef(hexHash string, kind BlobKind, originHint string) (BlobRef, error) {
	if len(hexHash) != 64 {
		return BlobRef{}, fmt.Errorf("blobstore: invalid hex hash length %d, want 64", len(hexHash))
	}
	decoded, err := hex.DecodeString(hexHash)
	if err != nil {
		return BlobRef{}, fmt.Errorf("blobstore: invalid hex hash: %w", err)
	}
	var hash [32]byte
	copy(hash[:], decoded)

	ref := BlobRef{
		Hash:           hash,
		Kind:           kind,
		OriginNodeHint: originHint,
	}
	ref.RequiredForConsensus = ref.ConsensusBlocking()
	if ref.RequiredForConsensus {
		ref.PersistenceClass = 0 // hot
	} else {
		ref.PersistenceClass = 1 // warm
	}
	return ref, nil
}
