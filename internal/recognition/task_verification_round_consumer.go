package recognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// TaskMetadataReader provides read-only access to task metadata needed by
// the verification round consumer. *tasks.TaskManager satisfies this via
// its Get method. Defined locally to avoid import cycles.
type TaskMetadataReader interface {
	// GetTaskPosterAndCategory returns the poster ID and category for a task.
	// Returns an error if the task does not exist.
	GetTaskPosterAndCategory(taskID string) (posterID string, category string, err error)
}

// TaskMetadataFunc adapts a function to the TaskMetadataReader interface.
type TaskMetadataFunc func(taskID string) (posterID string, category string, err error)

func (f TaskMetadataFunc) GetTaskPosterAndCategory(taskID string) (string, string, error) {
	return f(taskID)
}

// TaskVerificationRoundConsumer is a CommitConsumer that opens a
// TaskVerificationRound whenever a TaskSubmitted event is recognized.
//
// Round opening is idempotent: the same submission event ID always produces
// the same round ID (deterministic derivation), and the consumer checks for
// an existing round before creating a new one.
//
// The consumer extracts TaskID and WorkerID (ClaimerID) directly from the
// TaskSubmitted event payload. PosterID and Category are not in the
// TaskSubmitted payload — they come from the task store. If the task is not
// yet in the store (concurrent dispatch — the TaskPosted event hasn't been
// applied yet), the consumer defers with a prerequisite key. The
// TaskLifecycleConsumer signals this key after applying TaskPosted.
// PrerequisiteKeyTVRound returns the prerequisite key that signals a
// TaskVerificationRound has been opened for the given round ID. Used by
// the vote consumer to defer until the round exists.
func PrerequisiteKeyTVRound(roundID string) string {
	return "tv_round:" + roundID
}

// TaskVerificationRoundConsumer is a CommitConsumer that opens a
// TaskVerificationRound whenever a TaskSubmitted event is recognized.
type TaskVerificationRoundConsumer struct {
	rounds       taskverification.Store
	taskMeta     TaskMetadataReader
	validatorVer func() uint64 // returns current validator-set version
	clock        func() int64  // returns current Unix timestamp
	activator    *Activator
}

// NewTaskVerificationRoundConsumer creates a consumer that opens verification
// rounds on TaskSubmitted events.
//
// Parameters:
//   - rounds: persistence store for TaskVerificationRound objects
//   - taskMeta: read-only lookup for task poster/category
//   - validatorVer: returns the current validator-set snapshot version
//   - clock: returns the current Unix timestamp (injected for tests)
func NewTaskVerificationRoundConsumer(
	rounds taskverification.Store,
	taskMeta TaskMetadataReader,
	validatorVer func() uint64,
	clock func() int64,
) *TaskVerificationRoundConsumer {
	return &TaskVerificationRoundConsumer{
		rounds:       rounds,
		taskMeta:     taskMeta,
		validatorVer: validatorVer,
		clock:        clock,
	}
}

// SetActivator wires the targeted activation system so that after a round
// is opened, vote consumers waiting for the round are activated.
func (c *TaskVerificationRoundConsumer) SetActivator(a *Activator) {
	c.activator = a
}

// Name returns the unique consumer identifier.
func (c *TaskVerificationRoundConsumer) Name() string { return "task_verification_round" }

// Interested returns true for TaskSubmitted events.
func (c *TaskVerificationRoundConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskSubmitted
}

// Ready always returns true. The TaskSubmitted event's causal parent is the
// TaskPosted event, which means the task is guaranteed to be in the
// TaskManager by the time TaskSubmitted is committed to the DAG. If the
// task metadata lookup fails in Consume (e.g., during a startup race), the
// error is logged and the round creation retries via the recognition fabric.
//
// Note: the original deferred activation design (Ready checks metadata,
// defers if missing) had a race condition: the task_metadata signal fires
// when TaskPosted is applied, but if TaskSubmitted is processed AFTER the
// signal has already fired, the deferred consumer never wakes up. Always-ready
// with error-on-consume avoids this.
func (c *TaskVerificationRoundConsumer) Ready(_ context.Context, _ *event.Event, _ ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume opens a TaskVerificationRound for the submitted task. Idempotent:
// if a round already exists for this submission event ID, returns nil.
func (c *TaskVerificationRoundConsumer) Consume(ctx context.Context, ev *event.Event) error {
	var payload struct {
		TaskID    string `json:"task_id"`
		ClaimerID string `json:"claimer_id"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("task_verification_round: unmarshal payload: %w", err)
	}
	if payload.TaskID == "" || payload.ClaimerID == "" {
		return fmt.Errorf("task_verification_round: missing task_id or claimer_id in payload")
	}

	// Idempotency: check if round already exists for this submission event.
	_, err := c.rounds.LoadRoundBySubmissionEvent(ctx, ev.ID)
	if err == nil {
		// Round already exists — idempotent no-op.
		return nil
	}
	if !errors.Is(err, taskverification.ErrRoundNotFound) {
		return fmt.Errorf("task_verification_round: check existing: %w", err)
	}

	// Look up task metadata for PosterID and Category.
	posterID, category, err := c.taskMeta.GetTaskPosterAndCategory(payload.TaskID)
	if err != nil {
		return fmt.Errorf("task_verification_round: task metadata lookup: %w", err)
	}

	now := c.clock()
	r, err := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID:                payload.TaskID,
		SubmissionEventID:     ev.ID,
		WorkerID:              crypto.AgentID(payload.ClaimerID),
		PosterID:              crypto.AgentID(posterID),
		Category:              category,
		ValidatorSetVersion:   c.validatorVer(),
		Committee:             nil, // bootstrap mode: all active validators
		AnalyzerPolicyID:      "bootstrap_v1",
		DiversityFloor:        taskverification.DefaultDiversityFloor,
		AcceptanceThresholdBP: taskverification.DefaultAcceptanceThresholdBP,
		DeadlineSeconds:       taskverification.DefaultRoundDeadlineSeconds,
		Now:                   now,
	})
	if err != nil {
		return fmt.Errorf("task_verification_round: open round: %w", err)
	}

	if err := c.rounds.SaveRound(ctx, r); err != nil {
		return fmt.Errorf("task_verification_round: save round: %w", err)
	}

	slog.Info("task_verification_round: round opened",
		"round_id", r.RoundID,
		"task_id", r.TaskID,
		"submission_event_id", r.SubmissionEventID,
		"worker_id", r.WorkerID,
		"category", r.Category,
		"deadline_unix", r.DeadlineUnix,
		"validator_set_version", r.ValidatorSetVersion,
	)

	// Signal vote consumers that the round is now available.
	if c.activator != nil {
		c.activator.Signal(ctx, PrerequisiteKeyTVRound(string(r.RoundID)))
	}

	return nil
}

// Compile-time assertion.
var _ CommitConsumer = (*TaskVerificationRoundConsumer)(nil)
