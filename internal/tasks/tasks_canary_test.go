package tasks

import (
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/canary"
)

// ---------------------------------------------------------------------------
// Test stubs
// ---------------------------------------------------------------------------

// alwaysInject is a taskCanaryInjector stub that always injects.
type alwaysInject struct {
	mu      sync.Mutex
	linked  []string // taskIDs linked
	canaries []*canary.CanaryTask
}

func (s *alwaysInject) ShouldInject() bool { return true }

func (s *alwaysInject) NextCanary(category string) *canary.CanaryTask {
	c := canary.NewCanaryTask(category, canary.TypeKnownGood, true, nil, "sha256:test")
	s.mu.Lock()
	s.canaries = append(s.canaries, c)
	s.mu.Unlock()
	return c
}

func (s *alwaysInject) LinkTask(c *canary.CanaryTask, taskID string) error {
	s.mu.Lock()
	s.linked = append(s.linked, taskID)
	s.mu.Unlock()
	return nil
}

func (s *alwaysInject) linkedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.linked)
}

func (s *alwaysInject) linkedTaskID(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.linked[i]
}

// neverInject is a taskCanaryInjector stub that never injects.
type neverInject struct {
	mu     sync.Mutex
	called int
}

func (s *neverInject) ShouldInject() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called++
	return false
}

func (s *neverInject) NextCanary(_ string) *canary.CanaryTask { return nil }
func (s *neverInject) LinkTask(_ *canary.CanaryTask, _ string) error { return nil }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPostTask_CanaryInjection_Enabled verifies that when a canary injector is
// wired and ShouldInject() returns true, LinkTask is called with the correct
// task ID after the task is created.
func TestPostTask_CanaryInjection_Enabled(t *testing.T) {
	tm := NewTaskManager()
	inj := &alwaysInject{}
	tm.SetCanaryInjector(inj)

	task, err := tm.PostTask("poster-1", "A task title", "description", "code", 500_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	if inj.linkedCount() != 1 {
		t.Fatalf("LinkTask call count = %d; want 1", inj.linkedCount())
	}
	if got := inj.linkedTaskID(0); got != task.ID {
		t.Errorf("LinkTask received task ID %q; want %q", got, task.ID)
	}
}

// TestPostTask_CanaryInjection_Disabled verifies that when no canary injector
// is wired, PostTask completes normally and no injection occurs.
func TestPostTask_CanaryInjection_Disabled(t *testing.T) {
	tm := NewTaskManager()
	// No SetCanaryInjector call.

	task, err := tm.PostTask("poster-1", "Another task", "desc", "research", 500_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	// No panic or error — test passes.
}

// TestPostTask_CanaryInjection_ShouldInjectFalse verifies that when
// ShouldInject() returns false, LinkTask is never called.
func TestPostTask_CanaryInjection_ShouldInjectFalse(t *testing.T) {
	tm := NewTaskManager()
	inj := &neverInject{}
	tm.SetCanaryInjector(inj)

	_, err := tm.PostTask("poster-1", "Task title", "desc", "writing", 500_000)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	inj.mu.Lock()
	called := inj.called
	inj.mu.Unlock()

	if called != 1 {
		t.Errorf("ShouldInject called %d times; want 1", called)
	}
	// No LinkTask call because ShouldInject returned false.
}

// TestPostTask_CanaryInjection_LinkedTaskIDMatchesCreated verifies that the task
// ID passed to LinkTask exactly matches the ID of the task returned by PostTask.
// This is the property that makes the injection detectable by the evaluation path.
func TestPostTask_CanaryInjection_LinkedTaskIDMatchesCreated(t *testing.T) {
	tm := NewTaskManager()
	inj := &alwaysInject{}
	tm.SetCanaryInjector(inj)

	tasks := make([]string, 5)
	for i := range tasks {
		task, err := tm.PostTask("poster-1", "Task", "desc", "code", 500_000)
		if err != nil {
			t.Fatalf("PostTask %d: %v", i, err)
		}
		tasks[i] = task.ID
	}

	if inj.linkedCount() != 5 {
		t.Fatalf("expected 5 LinkTask calls, got %d", inj.linkedCount())
	}
	for i, id := range tasks {
		if got := inj.linkedTaskID(i); got != id {
			t.Errorf("task %d: linked ID %q != posted task ID %q", i, got, id)
		}
	}
}
