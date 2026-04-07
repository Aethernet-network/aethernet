package recognition

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// TaskEventApplier is the minimal interface for applying task lifecycle
// events. *tasks.TaskManager satisfies this via its ApplyDAGEvent method.
// Defined locally to avoid import cycles (recognition is Infrastructure,
// tasks is Application L3).
type TaskEventApplier interface {
	ApplyDAGEvent(ev *event.Event)
}

// PrerequisiteKeyTaskMetadata returns the prerequisite key that signals
// task metadata is available in the TaskManager for the given task ID.
// Used by consumers that need task metadata (e.g., PosterID, Category)
// that is only available after the TaskPosted event has been applied.
func PrerequisiteKeyTaskMetadata(taskID string) string {
	return "task_metadata:" + taskID
}

// TaskLifecycleConsumer is a CommitConsumer that recognizes task lifecycle
// events (TaskPosted, TaskClaimed, TaskSubmitted, TaskApproved, TaskDisputed)
// and applies them to the TaskManager.
//
// This consumer runs in parallel with the existing syncHandler route.
// ApplyDAGEvent is idempotent — duplicate calls from both paths are safe.
//
// Readiness: all task lifecycle events are immediately ready. There are no
// prerequisites to defer on. The TaskManager internally handles ordering
// (e.g., a TaskClaimed event for a non-existent task is silently skipped).
//
// After applying a TaskPosted event, the consumer signals a prerequisite
// key so that downstream consumers waiting on task metadata (PosterID,
// Category) are activated. This enables deferred activation without
// relying on dispatch ordering.
type TaskLifecycleConsumer struct {
	applier   TaskEventApplier
	activator *Activator
}

// NewTaskLifecycleConsumer creates a consumer wired to the given TaskManager.
func NewTaskLifecycleConsumer(applier TaskEventApplier) *TaskLifecycleConsumer {
	return &TaskLifecycleConsumer{applier: applier}
}

// SetActivator wires the targeted activation system so that after a
// TaskPosted event is applied, downstream consumers waiting on task
// metadata are activated.
func (c *TaskLifecycleConsumer) SetActivator(a *Activator) {
	c.activator = a
}

// Name returns the unique consumer identifier.
func (c *TaskLifecycleConsumer) Name() string { return "task_lifecycle" }

// Interested returns true for task lifecycle event types.
func (c *TaskLifecycleConsumer) Interested(ev *event.Event) bool {
	switch ev.Type {
	case event.EventTypeTaskPosted,
		event.EventTypeTaskClaimed,
		event.EventTypeTaskSubmitted,
		event.EventTypeTaskApproved,
		event.EventTypeTaskDisputed:
		return true
	}
	return false
}

// Ready returns immediately ready for all task events. The TaskManager
// handles internal ordering and idempotency.
func (c *TaskLifecycleConsumer) Ready(_ context.Context, _ *event.Event, _ ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume applies the task lifecycle event to the TaskManager. Idempotent:
// ApplyDAGEvent skips already-applied state transitions.
//
// After applying TaskPosted events, signals the task_metadata prerequisite
// key so downstream consumers are activated.
func (c *TaskLifecycleConsumer) Consume(ctx context.Context, ev *event.Event) error {
	c.applier.ApplyDAGEvent(ev)

	// Signal task metadata availability for TaskPosted events so that
	// consumers waiting on task metadata (e.g., verification round opener)
	// are activated via the deferred activation mechanism.
	if ev.Type == event.EventTypeTaskPosted && c.activator != nil {
		var payload struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.TaskID != "" {
			c.activator.Signal(ctx, PrerequisiteKeyTaskMetadata(payload.TaskID))
		} else {
			slog.Debug("task_lifecycle: could not extract task_id for activation signal",
				"event_id", ev.ID, "err", err)
		}
	}

	return nil
}

// Compile-time assertion.
var _ CommitConsumer = (*TaskLifecycleConsumer)(nil)
