package blobstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/blobstore"
)

func newTestStore(t *testing.T) *blobstore.FSStore {
	t.Helper()
	dir := t.TempDir()
	store, err := blobstore.NewFSStore(dir, blobstore.DefaultMaxBlobSize)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	return store
}

func TestFSStore_PutGet_Roundtrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	data := []byte("hello trajectory checkpoint")
	hash, size, err := store.Put(ctx, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if size != int64(len(data)) {
		t.Errorf("size = %d; want %d", size, len(data))
	}
	if len(hash) != 64 {
		t.Errorf("hash length = %d; want 64", len(hash))
	}

	got, err := store.Get(ctx, hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("retrieved data does not match stored data")
	}
}

func TestFSStore_DeterministicHash(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	data := []byte("deterministic content")
	h1, _, _ := store.Put(ctx, data)
	h2, _, _ := store.Put(ctx, data)

	if h1 != h2 {
		t.Error("same data should produce same hash")
	}

	// Verify against manual SHA-256.
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])
	if h1 != expected {
		t.Errorf("hash = %q; want %q", h1, expected)
	}
}

func TestFSStore_IdempotentPut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	data := []byte("idempotent write")
	h1, s1, err1 := store.Put(ctx, data)
	h2, s2, err2 := store.Put(ctx, data)

	if err1 != nil || err2 != nil {
		t.Fatalf("Put errors: %v, %v", err1, err2)
	}
	if h1 != h2 || s1 != s2 {
		t.Error("idempotent Put should return same hash and size")
	}
}

func TestFSStore_OversizedBlob_Rejected(t *testing.T) {
	dir := t.TempDir()
	store, _ := blobstore.NewFSStore(dir, 100) // 100 byte limit
	ctx := context.Background()

	data := make([]byte, 200)
	_, _, err := store.Put(ctx, data)
	if err == nil {
		t.Fatal("should reject oversized blob")
	}
	if !errors.Is(err, blobstore.ErrBlobTooLarge) {
		t.Errorf("error = %v; want ErrBlobTooLarge", err)
	}
}

func TestFSStore_MissingBlob(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("should return error for missing blob")
	}
	if !errors.Is(err, blobstore.ErrBlobNotFound) {
		t.Errorf("error = %v; want ErrBlobNotFound", err)
	}
}

func TestFSStore_InvalidHash(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, "not-a-valid-hash")
	if err == nil {
		t.Fatal("should reject invalid hash")
	}
	if !errors.Is(err, blobstore.ErrInvalidHash) {
		t.Errorf("error = %v; want ErrInvalidHash", err)
	}
}

func TestFSStore_Has(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	data := []byte("check existence")
	hash, _, _ := store.Put(ctx, data)

	exists, err := store.Has(ctx, hash)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !exists {
		t.Error("Has should return true for stored blob")
	}

	exists, err = store.Has(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("Has (missing): %v", err)
	}
	if exists {
		t.Error("Has should return false for missing blob")
	}
}

func TestFSStore_Delete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	data := []byte("delete me")
	hash, _, _ := store.Put(ctx, data)

	if err := store.Delete(ctx, hash); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, _ := store.Has(ctx, hash)
	if exists {
		t.Error("blob should not exist after delete")
	}

	// Idempotent delete.
	if err := store.Delete(ctx, hash); err != nil {
		t.Errorf("second Delete should be idempotent: %v", err)
	}
}

func TestFSStore_Delete_InvalidHash(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	err := store.Delete(ctx, "bad-hash")
	if !errors.Is(err, blobstore.ErrInvalidHash) {
		t.Errorf("Delete(bad hash) = %v; want ErrInvalidHash", err)
	}
}

func TestFSStore_AtomicWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Write a blob and verify it's immediately readable.
	data := []byte("atomic write test content that needs to survive rename")
	hash, _, err := store.Put(ctx, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, hash)
	if err != nil {
		t.Fatalf("Get after atomic write: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("data mismatch after atomic write")
	}
}

func TestFSStore_EmptyBlob(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	data := []byte{}
	hash, size, err := store.Put(ctx, data)
	if err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	if size != 0 {
		t.Errorf("size = %d; want 0", size)
	}

	got, err := store.Get(ctx, hash)
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if len(got) != 0 {
		t.Error("empty blob should return empty data")
	}
}
