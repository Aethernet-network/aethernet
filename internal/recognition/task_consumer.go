package recognition

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// TaskEventApplier is the minimal interface for applying task lifecycle
// events. *tasks.TaskManager satisfies this via its ApplyDAGEvent method.
// Defined locally to avoid import cycles (recognition is Infrastructure,
// tasks is Application L3).
type TaskEventApplier interface {
	ApplyDAGEvent(ev *event.Event)
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
type TaskLifecycleConsumer struct {
	applier TaskEventApplier
}

// NewTaskLifecycleConsumer creates a consumer wired to the given TaskManager.
func NewTaskLifecycleConsumer(applier TaskEventApplier) *TaskLifecycleConsumer {
	return &TaskLifecycleConsumer{applier: applier}
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
func (c *TaskLifecycleConsumer) Consume(_ context.Context, ev *event.Event) error {
	c.applier.ApplyDAGEvent(ev)
	return nil
}

// Compile-time assertion.
var _ CommitConsumer = (*TaskLifecycleConsumer)(nil)
