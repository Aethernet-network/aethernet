package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// TestListTasks_NoFilter returns all tasks.
func TestListTasks_NoFilter(t *testing.T) {
	setup := newTestSetup(t)

	// Post 2 tasks with different categories.
	setup.taskMgr.PostTask("poster", "Task A", "Desc A", "research", 10_000)
	setup.taskMgr.PostTask("poster", "Task B", "Desc B", "writing", 10_000)

	resp, err := http.Get(setup.ts.URL + "/v1/tasks")
	if err != nil {
		t.Fatalf("GET /v1/tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	var result []tasks.Task
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("tasks = %d; want 2", len(result))
	}
}

// TestListTasks_StatusFilter returns only open tasks.
func TestListTasks_StatusFilter(t *testing.T) {
	setup := newTestSetup(t)

	task, _ := setup.taskMgr.PostTask("poster", "Open Task", "Desc", "research", 10_000)
	task2, _ := setup.taskMgr.PostTask("poster", "Claimed Task", "Desc", "research", 10_000)
	setup.taskMgr.ClaimTask(task2.ID, "claimer")

	// Filter by status=open.
	resp, err := http.Get(setup.ts.URL + "/v1/tasks?status=open")
	if err != nil {
		t.Fatalf("GET /v1/tasks?status=open: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	var result []tasks.Task
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("open tasks = %d; want 1", len(result))
	}
	if len(result) > 0 && result[0].ID != task.ID {
		t.Errorf("expected task %q; got %q", task.ID, result[0].ID)
	}
}

// TestListTasks_CategoryFilter returns only matching category.
func TestListTasks_CategoryFilter(t *testing.T) {
	setup := newTestSetup(t)

	setup.taskMgr.PostTask("poster", "Research Task", "Desc", "research", 10_000)
	setup.taskMgr.PostTask("poster", "Writing Task", "Desc", "writing", 10_000)

	resp, err := http.Get(setup.ts.URL + "/v1/tasks?category=research")
	if err != nil {
		t.Fatalf("GET /v1/tasks?category=research: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	var result []tasks.Task
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("research tasks = %d; want 1", len(result))
	}
	if len(result) > 0 && result[0].Category != "research" {
		t.Errorf("category = %q; want 'research'", result[0].Category)
	}
}

// TestListTasks_LimitFilter returns at most N tasks.
func TestListTasks_LimitFilter(t *testing.T) {
	setup := newTestSetup(t)

	for i := 0; i < 5; i++ {
		setup.taskMgr.PostTask("poster", "Task", "Desc", "research", 10_000)
	}

	resp, err := http.Get(setup.ts.URL + "/v1/tasks?limit=2")
	if err != nil {
		t.Fatalf("GET /v1/tasks?limit=2: %v", err)
	}
	defer resp.Body.Close()

	var result []tasks.Task
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result) != 2 {
		t.Errorf("limited tasks = %d; want 2", len(result))
	}
}

// TestListTasks_CombinedFilters uses status + category + limit together.
func TestListTasks_CombinedFilters(t *testing.T) {
	setup := newTestSetup(t)

	setup.taskMgr.PostTask("poster", "R1", "Desc", "research", 10_000)
	setup.taskMgr.PostTask("poster", "R2", "Desc", "research", 10_000)
	setup.taskMgr.PostTask("poster", "W1", "Desc", "writing", 10_000)

	resp, err := http.Get(setup.ts.URL + "/v1/tasks?status=open&category=research&limit=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var result []tasks.Task
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result) != 1 {
		t.Errorf("combined filter tasks = %d; want 1", len(result))
	}
	if len(result) > 0 && result[0].Category != "research" {
		t.Errorf("category = %q; want 'research'", result[0].Category)
	}
}
