package taskverification

import "errors"

var (
	// ErrRoundNotFound is returned when a round lookup finds no matching record.
	ErrRoundNotFound = errors.New("taskverification: round not found")

	// ErrRoundAlreadyExists is returned when creating a round whose ID
	// already exists in the store. SaveRound treats this as an update, so
	// callers that need create-only semantics must check first.
	ErrRoundAlreadyExists = errors.New("taskverification: round already exists")

	// ErrInvalidRoundState is returned when a RoundState value is outside
	// the defined enum range.
	ErrInvalidRoundState = errors.New("taskverification: invalid round state")

	// ErrInvalidStateTransition is returned when Transition is called with
	// a target state that is not reachable from the current state.
	ErrInvalidStateTransition = errors.New("taskverification: invalid state transition")

	// ErrInvalidRoundID is returned when a RoundID is empty or malformed.
	ErrInvalidRoundID = errors.New("taskverification: invalid round ID")

	// ErrInvalidDeadline is returned when a round's deadline is in the past
	// or otherwise nonsensical at creation time.
	ErrInvalidDeadline = errors.New("taskverification: invalid deadline")

	// ErrSerializationFailed is returned when canonical encoding or decoding
	// of a round fails.
	ErrSerializationFailed = errors.New("taskverification: serialization failed")

	// ErrPersistenceFailed is returned when a BadgerDB read or write fails
	// for reasons other than key-not-found.
	ErrPersistenceFailed = errors.New("taskverification: persistence failed")
)
