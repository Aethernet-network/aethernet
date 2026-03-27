// Package blobstore provides content-addressed blob storage for AetherNet.
//
// Blobs are stored by their SHA-256 content hash. The primary use case is
// storing CheckpointBody artifacts referenced by TrajectoryCommitPayload
// events in the DAG.
//
// Layer: Infrastructure — importable by any layer.
package blobstore

import (
	"context"
	"errors"
)

// Store is the content-addressed blob storage interface.
type Store interface {
	// Put writes data and returns its content hash (hex-encoded SHA-256)
	// and size. If a blob with the same hash already exists, Put is
	// idempotent and returns the existing hash without error.
	Put(ctx context.Context, data []byte) (hash string, size int64, err error)

	// Get retrieves a blob by its content hash. Returns ErrBlobNotFound
	// if the blob does not exist.
	Get(ctx context.Context, hash string) ([]byte, error)

	// Has reports whether a blob with the given hash exists.
	Has(ctx context.Context, hash string) (bool, error)

	// Delete removes a blob by its content hash. Returns nil if the blob
	// does not exist (idempotent).
	Delete(ctx context.Context, hash string) error
}

// Sentinel errors.
var (
	// ErrInvalidHash is returned when a hash string is not a valid
	// 64-character hex-encoded SHA-256 hash.
	ErrInvalidHash = errors.New("blobstore: invalid hash")

	// ErrBlobNotFound is returned by Get when the requested blob
	// does not exist.
	ErrBlobNotFound = errors.New("blobstore: blob not found")

	// ErrHashMismatch is returned when the content hash of stored data
	// does not match the expected hash.
	ErrHashMismatch = errors.New("blobstore: content hash mismatch")

	// ErrBlobTooLarge is returned when the blob exceeds the maximum
	// allowed size.
	ErrBlobTooLarge = errors.New("blobstore: blob too large")
)
