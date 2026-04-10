package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func newTestSubscribableStore(t *testing.T) *SubscribableStore {
	t.Helper()
	dir := t.TempDir()
	fs, err := NewFSStore(dir, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	return NewSubscribableStore(fs)
}

func TestPutVerified_MatchingHash(t *testing.T) {
	s := newTestSubscribableStore(t)
	data := []byte("verified blob content")
	hash := sha256.Sum256(data)
	ref := BlobRef{Hash: hash, Kind: BlobKindEvidence}

	if err := s.PutVerified(ref, data); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}
	if !s.HasByHash(hash) {
		t.Error("blob should exist after PutVerified")
	}
}

func TestPutVerified_MismatchedHash(t *testing.T) {
	s := newTestSubscribableStore(t)
	data := []byte("some data")
	var wrongHash [32]byte // all zeros
	ref := BlobRef{Hash: wrongHash, Kind: BlobKindEvidence}

	err := s.PutVerified(ref, data)
	if err == nil {
		t.Fatal("expected error for mismatched hash")
	}
	if s.HasByHash(wrongHash) {
		t.Error("blob should NOT be stored on hash mismatch")
	}
}

func TestSubscribe_AlreadyPresent(t *testing.T) {
	s := newTestSubscribableStore(t)
	data := []byte("already here")
	hash, _, _ := s.Put(context.Background(), data)

	var h [32]byte
	decoded, _ := hex.DecodeString(hash)
	copy(h[:], decoded)

	ch := s.Subscribe(h)
	select {
	case <-ch:
		// expected — channel should be closed immediately
	default:
		t.Error("Subscribe on existing blob should return closed channel")
	}
}

func TestSubscribe_NotifyOnPut(t *testing.T) {
	s := newTestSubscribableStore(t)
	data := []byte("future blob")
	hash := sha256.Sum256(data)

	ch := s.Subscribe(hash)

	// Blob not yet stored — channel should be open.
	select {
	case <-ch:
		t.Fatal("channel should NOT be closed before blob is stored")
	default:
	}

	// Store the blob via PutVerified.
	ref := BlobRef{Hash: hash, Kind: BlobKindEvidence}
	if err := s.PutVerified(ref, data); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}

	// Channel should now be closed.
	select {
	case <-ch:
		// expected
	case <-time.After(time.Second):
		t.Fatal("subscriber was not notified within 1s")
	}
}

func TestSubscribe_MultipleSubscribers(t *testing.T) {
	s := newTestSubscribableStore(t)
	data := []byte("shared blob")
	hash := sha256.Sum256(data)

	ch1 := s.Subscribe(hash)
	ch2 := s.Subscribe(hash)
	ch3 := s.Subscribe(hash)

	ref := BlobRef{Hash: hash, Kind: BlobKindEvidence}
	_ = s.PutVerified(ref, data)

	for i, ch := range []<-chan struct{}{ch1, ch2, ch3} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d was not notified", i)
		}
	}
}

func TestWaitForBlob_ContextCancelled(t *testing.T) {
	s := newTestSubscribableStore(t)
	var hash [32]byte // blob that never arrives

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := s.WaitForBlob(ctx, hash)
	if err == nil {
		t.Fatal("expected context error")
	}
}

