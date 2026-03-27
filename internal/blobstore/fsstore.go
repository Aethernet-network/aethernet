package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// DefaultMaxBlobSize is the maximum blob size (10 MiB).
const DefaultMaxBlobSize = 10 * 1024 * 1024

// hashPattern validates a 64-character hex SHA-256 hash.
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// FSStore is a filesystem-backed content-addressed blob store.
// Blobs are stored at data/{hash[0:2]}/{hash[2:4]}/{hash}.
// Writes are atomic: data is written to a temp file and renamed.
type FSStore struct {
	root        string
	maxBlobSize int64
}

// NewFSStore creates a filesystem blob store rooted at the given directory.
// The directory is created if it does not exist.
func NewFSStore(root string, maxBlobSize int64) (*FSStore, error) {
	if maxBlobSize <= 0 {
		maxBlobSize = DefaultMaxBlobSize
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("blobstore: create root dir: %w", err)
	}
	return &FSStore{root: root, maxBlobSize: maxBlobSize}, nil
}

// blobPath returns the filesystem path for a given hash.
func (s *FSStore) blobPath(hash string) string {
	return filepath.Join(s.root, hash[0:2], hash[2:4], hash)
}

// Put writes data and returns its content hash. Atomic via temp+rename.
// Idempotent: if the blob already exists, returns the hash without error.
func (s *FSStore) Put(_ context.Context, data []byte) (string, int64, error) {
	if int64(len(data)) > s.maxBlobSize {
		return "", 0, fmt.Errorf("%w: %d bytes exceeds limit %d", ErrBlobTooLarge, len(data), s.maxBlobSize)
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	size := int64(len(data))

	path := s.blobPath(hash)

	// Idempotent: if the blob already exists, return immediately.
	if info, err := os.Stat(path); err == nil && info.Size() == size {
		return hash, size, nil
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", 0, fmt.Errorf("blobstore: create dir: %w", err)
	}

	// Atomic write: temp file + rename.
	tmp, err := os.CreateTemp(dir, ".blob-*")
	if err != nil {
		return "", 0, fmt.Errorf("blobstore: create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("blobstore: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("blobstore: close temp: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("blobstore: rename: %w", err)
	}

	return hash, size, nil
}

// Get retrieves a blob by its content hash.
func (s *FSStore) Get(_ context.Context, hash string) ([]byte, error) {
	if !isValidHash(hash) {
		return nil, ErrInvalidHash
	}

	path := s.blobPath(hash)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, fmt.Errorf("blobstore: open: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("blobstore: read: %w", err)
	}

	// Verify content hash on read.
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != hash {
		return nil, fmt.Errorf("%w: stored=%s computed=%s", ErrHashMismatch, hash, got)
	}

	return data, nil
}

// Has reports whether a blob exists.
func (s *FSStore) Has(_ context.Context, hash string) (bool, error) {
	if !isValidHash(hash) {
		return false, ErrInvalidHash
	}
	_, err := os.Stat(s.blobPath(hash))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Delete removes a blob. Idempotent: returns nil if not found.
func (s *FSStore) Delete(_ context.Context, hash string) error {
	if !isValidHash(hash) {
		return ErrInvalidHash
	}
	err := os.Remove(s.blobPath(hash))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("blobstore: delete: %w", err)
	}
	return nil
}

func isValidHash(h string) bool {
	return hashPattern.MatchString(h)
}
