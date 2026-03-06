package router

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestRouter returns a Router with a freshly seeded TaskManager and a
// claim function that records claimed taskIDs into the provided map.
func newTestRouter(claimed *sync.Map, repFn func(crypto.AgentID, string) (uint64, float64, float64, float64)) (*Router, *tasks.TaskManager) {
	tm := tasks.NewTaskManager()
	claimFn := func(taskID string, agentID crypto.AgentID) error {
		claimed.Store(taskID, agentID)
		return tm.ClaimTask(taskID, agentID)
	}
	r := New(tm, claimFn, repFn, 5*time.Second)
	return r, tm
}

func noReputation(_ crypto.AgentID, _ string) (uint64, float64, float64, float64) {
	return 0, 0, 0, 0
}

func postTask(tm *tasks.TaskManager, posterID, title, category string, budget uint64) *tasks.Task {
	task, err := tm.PostTask(posterID, title, "test description", category, budget)
	if err != nil {
		panic(fmt.Sprintf("postTask: %v", err))
	}
	return task
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRegisterCapability(t *testing.T) {
	var claimed sync.Map
	r, _ := newTestRouter(&claimed, noReputation)

	cap := AgentCapability{
		AgentID:    "agent-alpha",
		Categories: []string{"research"},
		Available:  true,
	}
	r.RegisterCapability(cap)

	agents := r.RegisteredAgents()
	if len(agents) != 1 {
		t.Fatalf("expected 1 registered agent, got %d", len(agents))
	}
	if agents[0].AgentID != "agent-alpha" {
		t.Errorf("unexpected agent_id: %s", agents[0].AgentID)
	}

	// Unregister and verify removal.
	r.UnregisterCapability("agent-alpha")
	if len(r.RegisteredAgents()) != 0 {
		t.Error("expected no agents after unregister")
	}
}

func TestRouteMatch_CategoryMatch(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "worker-1",
		Categories: []string{"research"},
		Available:  true,
	})

	task := postTask(tm, "poster-x", "Summarise papers", "research", 50_000)
	r.routePending()

	if _, ok := claimed.Load(task.ID); !ok {
		t.Errorf("task %s should have been claimed by router", task.ID)
	}

	routes := r.RecentRoutes(0)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route result, got %d", len(routes))
	}
	if routes[0].AgentID != "worker-1" {
		t.Errorf("unexpected routed agent: %s", routes[0].AgentID)
	}
}

func TestRouteMatch_TagOverlap(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	// Agent with matching tags but wrong category.
	r.RegisterCapability(AgentCapability{
		AgentID:    "tagger",
		Categories: []string{"misc"},
		Tags:       []string{"nlp", "summarisation", "english"},
		Available:  true,
	})

	task, err := tm.PostTask("poster-y", "Tag task", "test description", "other", 20_000)
	if err != nil {
		t.Fatal(err)
	}
	// Assign matching tags to the task manually via the struct — tasks package
	// does not expose a tag setter, so we directly use the returned pointer.
	task.Tags = []string{"nlp", "summarisation"}
	// Store the modified task back by re-posting won't work; instead verify
	// that the agent is eligible at all (score > 0 via tagOverlap).
	score := r.scoreMatch(r.capabilities["tagger"], task)
	if score <= 0 {
		t.Errorf("expected positive score from tag overlap, got %.3f", score)
	}
}

func TestRouteMatch_NoMatch(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	// Agent registered for "code" only.
	r.RegisterCapability(AgentCapability{
		AgentID:    "coder",
		Categories: []string{"code"},
		Available:  true,
	})

	postTask(tm, "poster-z", "Research task", "research", 10_000)
	r.routePending()

	count := 0
	claimed.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("expected 0 claims (no category match), got %d", count)
	}
}

func TestRouteMatch_SelfExcluded(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "self-poster",
		Categories: []string{"research"},
		Available:  true,
	})

	// The registered agent is also the task poster.
	postTask(tm, "self-poster", "My own task", "research", 10_000)
	r.routePending()

	count := 0
	claimed.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("expected 0 claims (self-exclusion), got %d", count)
	}
}

func TestRouteMatch_OverBudget(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:      "pricey",
		Categories:   []string{"writing"},
		PricePerTask: 100_000,
		Available:    true,
	})

	postTask(tm, "poster-a", "Cheap task", "writing", 5_000) // budget < price
	r.routePending()

	count := 0
	claimed.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("expected 0 claims (over budget), got %d", count)
	}
}

func TestRouteMatch_BestScore(t *testing.T) {
	var claimed sync.Map
	// Give "expert" a strong reputation score.
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		if agentID == "expert" {
			return 50, 0.95, 30, 0.98 // lots of completions, high score
		}
		return 0, 0, 0, 0
	}
	r, tm := newTestRouter(&claimed, repFn)

	r.RegisterCapability(AgentCapability{
		AgentID:    "novice",
		Categories: []string{"ml"},
		Available:  true,
	})
	r.RegisterCapability(AgentCapability{
		AgentID:    "expert",
		Categories: []string{"ml"},
		Available:  true,
	})

	task := postTask(tm, "poster-b", "ML task", "ml", 50_000)
	r.routePending()

	v, ok := claimed.Load(task.ID)
	if !ok {
		t.Fatal("task not claimed")
	}
	if v.(crypto.AgentID) != "expert" {
		t.Errorf("expected 'expert' to win, got %s", v)
	}
}

func TestRouteMatch_MaxConcurrency(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:       "busy",
		Categories:    []string{"data"},
		MaxConcurrent: 2,
		ActiveTasks:   2, // at capacity
		Available:     true,
	})

	postTask(tm, "poster-c", "Data task", "data", 30_000)
	r.routePending()

	count := 0
	claimed.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("expected 0 claims (agent at max concurrency), got %d", count)
	}
}

func TestSetAvailability(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "on-off",
		Categories: []string{"testing"},
		Available:  false, // starts unavailable
	})

	postTask(tm, "poster-d", "Test task 1", "testing", 10_000)
	r.routePending()

	count := 0
	claimed.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Error("agent unavailable — should not receive tasks")
	}

	// Make agent available, post a new task.
	ok := r.SetAvailability("on-off", true)
	if !ok {
		t.Fatal("SetAvailability returned false for known agent")
	}

	postTask(tm, "poster-d", "Test task 2", "testing", 10_000)
	r.routePending()

	// Both task 1 (previously unrouted) and task 2 are open, so 2 total claims.
	count = 0
	claimed.Range(func(_, _ any) bool { count++; return true })
	if count != 2 {
		t.Errorf("expected 2 claims after enabling availability, got %d", count)
	}

	// SetAvailability on unknown agent returns false.
	if r.SetAvailability("nobody", true) {
		t.Error("expected false for unknown agent")
	}
}

func TestRecentRoutes(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "route-agent",
		Categories: []string{"nlp"},
		Available:  true,
	})

	// Post and route 3 tasks.
	for i := 0; i < 3; i++ {
		postTask(tm, fmt.Sprintf("poster-%d", i), fmt.Sprintf("NLP task %d", i), "nlp", 10_000)
	}
	r.routePending()

	routes := r.RecentRoutes(0)
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}

	// RecentRoutes(2) should return only the last 2.
	last2 := r.RecentRoutes(2)
	if len(last2) != 2 {
		t.Fatalf("expected 2 recent routes, got %d", len(last2))
	}

	// All routes should name the same agent.
	for _, rr := range routes {
		if rr.AgentID != "route-agent" {
			t.Errorf("unexpected agent in route: %s", rr.AgentID)
		}
	}
}

func TestStats(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "stat-agent",
		Categories: []string{"stats"},
		Available:  true,
	})

	// 2 open tasks initially.
	postTask(tm, "poster-e", "Stats task 1", "stats", 5_000)
	postTask(tm, "poster-e", "Stats task 2", "stats", 5_000)

	stats := r.Stats()
	if stats.RegisteredAgents != 1 {
		t.Errorf("registered agents: want 1, got %d", stats.RegisteredAgents)
	}
	if stats.AvailableAgents != 1 {
		t.Errorf("available agents: want 1, got %d", stats.AvailableAgents)
	}
	if stats.PendingTasks != 2 {
		t.Errorf("pending tasks: want 2, got %d", stats.PendingTasks)
	}
	if stats.TotalRouted != 0 {
		t.Errorf("total routed: want 0, got %d", stats.TotalRouted)
	}

	r.routePending()

	stats = r.Stats()
	if stats.TotalRouted != 2 {
		t.Errorf("after routing: total_routed want 2, got %d", stats.TotalRouted)
	}
	if stats.PendingTasks != 0 {
		t.Errorf("after routing: pending_tasks want 0, got %d", stats.PendingTasks)
	}
}
