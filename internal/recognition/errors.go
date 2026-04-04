package recognition

import "errors"

// Sentinel errors for the recognition package.
var (
	// ErrQueueFull is returned when the commit bus dispatch queue is at
	// capacity. Callers should treat this as a backpressure signal.
	ErrQueueFull = errors.New("recognition: dispatch queue full")

	// ErrConsumerAlreadyRegistered is returned when a consumer with the
	// same Name() is registered twice.
	ErrConsumerAlreadyRegistered = errors.New("recognition: consumer already registered")

	// ErrNotFound is returned when a recognition state entry does not exist.
	ErrNotFound = errors.New("recognition: state not found")
)
