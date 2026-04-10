package blobstore

import (
	"encoding/json"
	"testing"
)

func TestConsensusBlocking(t *testing.T) {
	cases := []struct {
		kind    BlobKind
		want    bool
	}{
		{BlobKindEvidence, true},
		{BlobKindManifest, true},
		{BlobKindTrajectory, true},
		{BlobKindMethodology, false},
		{BlobKindCitation, false},
		{BlobKindDiagnostic, false},
		{BlobKindArchival, false},
	}
	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			ref := BlobRef{Kind: tc.kind}
			if got := ref.ConsensusBlocking(); got != tc.want {
				t.Errorf("ConsensusBlocking() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestBlobRef_ZeroValue_IsConsensusBlocking(t *testing.T) {
	var ref BlobRef
	// BlobKindEvidence is iota 0 — zero-value BlobRef defaults to
	// consensus-blocking (conservative).
	if !ref.ConsensusBlocking() {
		t.Error("zero-value BlobRef should be consensus-blocking (BlobKindEvidence is iota 0)")
	}
}

func TestBlobRef_JSONRoundtrip(t *testing.T) {
	original, err := NewBlobRef(
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
		BlobKindEvidence,
		"validator-1",
	)
	if err != nil {
		t.Fatalf("NewBlobRef: %v", err)
	}
	original.SizeHint = 12345

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded BlobRef
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Hash != original.Hash {
		t.Error("Hash mismatch")
	}
	if decoded.Kind != original.Kind {
		t.Errorf("Kind = %v; want %v", decoded.Kind, original.Kind)
	}
	if decoded.SizeHint != original.SizeHint {
		t.Errorf("SizeHint = %d; want %d", decoded.SizeHint, original.SizeHint)
	}
	if decoded.RequiredForConsensus != original.RequiredForConsensus {
		t.Errorf("RequiredForConsensus = %v; want %v", decoded.RequiredForConsensus, original.RequiredForConsensus)
	}
	if decoded.OriginNodeHint != original.OriginNodeHint {
		t.Errorf("OriginNodeHint = %s; want %s", decoded.OriginNodeHint, original.OriginNodeHint)
	}
	if decoded.PersistenceClass != original.PersistenceClass {
		t.Errorf("PersistenceClass = %d; want %d", decoded.PersistenceClass, original.PersistenceClass)
	}
}

func TestNewBlobRef_InvalidHex(t *testing.T) {
	_, err := NewBlobRef("short", BlobKindEvidence, "")
	if err == nil {
		t.Error("expected error for short hex hash")
	}
	_, err = NewBlobRef("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", BlobKindEvidence, "")
	if err == nil {
		t.Error("expected error for non-hex characters")
	}
}

func TestNewBlobRef_PersistenceClass(t *testing.T) {
	evidence, _ := NewBlobRef("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2", BlobKindEvidence, "")
	if evidence.PersistenceClass != 0 {
		t.Errorf("evidence PersistenceClass = %d; want 0 (hot)", evidence.PersistenceClass)
	}

	citation, _ := NewBlobRef("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2", BlobKindCitation, "")
	if citation.PersistenceClass != 1 {
		t.Errorf("citation PersistenceClass = %d; want 1 (warm)", citation.PersistenceClass)
	}
}

func TestHexHash(t *testing.T) {
	hexStr := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
	ref, _ := NewBlobRef(hexStr, BlobKindEvidence, "")
	if got := ref.HexHash(); got != hexStr {
		t.Errorf("HexHash() = %s; want %s", got, hexStr)
	}
}
