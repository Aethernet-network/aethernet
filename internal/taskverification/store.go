package taskverification

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// Store is the persistence interface for TaskVerificationRound objects.
// Implementations must be safe for concurrent use.
type Store interface {
	// SaveRound persists a round. If a round with the same RoundID already
	// exists, it is updated (upsert semantics). Secondary indexes are
	// maintained atomically with the primary record.
	SaveRound(ctx context.Context, r *TaskVerificationRound) error

	// LoadRound retrieves a round by its primary key.
	// Returns ErrRoundNotFound if no round exists with the given ID.
	LoadRound(ctx context.Context, id RoundID) (*TaskVerificationRound, error)

	// LoadRoundBySubmissionEvent retrieves the round opened for a specific
	// TaskSubmitted event. Returns ErrRoundNotFound if no round exists.
	LoadRoundBySubmissionEvent(ctx context.Context, eventID event.EventID) (*TaskVerificationRound, error)

	// LoadRoundsByTaskID returns all rounds associated with a task ID,
	// ordered by RoundID. Returns an empty slice (not an error) if none exist.
	LoadRoundsByTaskID(ctx context.Context, taskID string) ([]*TaskVerificationRound, error)

	// DeleteRound removes a round and all its secondary indexes.
	// Returns ErrRoundNotFound if the round does not exist.
	DeleteRound(ctx context.Context, id RoundID) error

	// ListOpenRounds returns all rounds in RoundStateOpen.
	ListOpenRounds(ctx context.Context) ([]*TaskVerificationRound, error)

	// ListRoundsByState returns all rounds in the given state.
	ListRoundsByState(ctx context.Context, state RoundState) ([]*TaskVerificationRound, error)
}
