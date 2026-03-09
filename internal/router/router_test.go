package router

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// taskManagerSource adapts *tasks.TaskManager to the TaskSource interface.
// This adapter lives in the test file so the router package itself never
// imports the tasks package.
type taskManagerSource struct {
	tm *tasks.TaskManager
}

func (s *taskManagerSource) OpenTasks() []RoutableTask {
	open := s.tm.OpenTasks(0)
	result := make([]RoutableTask, len(open))
	for i, t := range open {
		result[i] = t
	}
	return result
}

// newTestRouter returns a Router with a freshly seeded TaskManager and a
// claim function that records claimed taskIDs into the provided map.
func newTestRouter(claimed *sync.Map, repFn func(crypto.AgentID, string) (uint64, float64, float64, float64)) (*Router, *tasks.TaskManager) {
	tm := tasks.NewTaskManager()
	claimFn := func(taskID string, agentID crypto.AgentID) error {
		claimed.Store(taskID, agentID)
		return tm.ClaimTask(taskID, agentID)
	}
	r := New(&taskManagerSource{tm: tm}, claimFn, repFn, 5*time.Second)
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

// countClaimed counts all entries in a sync.Map.
func countClaimed(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

// ---------------------------------------------------------------------------
// Existing tests (updated for map-based Stats and progressive budget)
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
	task.Tags = []string{"nlp", "summarisation"}
	score := r.scoreMatch(r.capabilities["tagger"], task)
	if score <= 0 {
		t.Errorf("expected positive score from tag overlap, got %.3f", score)
	}
}

func TestRouteMatch_NoMatch(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "coder",
		Categories: []string{"code"},
		Available:  true,
	})

	postTask(tm, "poster-z", "Research task", "research", 10_000)
	r.routePending()

	if countClaimed(&claimed) != 0 {
		t.Errorf("expected 0 claims (no category match), got %d", countClaimed(&claimed))
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

	postTask(tm, "self-poster", "My own task", "research", 10_000)
	r.routePending()

	if countClaimed(&claimed) != 0 {
		t.Errorf("expected 0 claims (self-exclusion), got %d", countClaimed(&claimed))
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

	postTask(tm, "poster-a", "Cheap task", "writing", 5_000)
	r.routePending()

	if countClaimed(&claimed) != 0 {
		t.Errorf("expected 0 claims (over budget), got %d", countClaimed(&claimed))
	}
}

func TestRouteMatch_BestScore(t *testing.T) {
	var claimed sync.Map
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		if agentID == "expert" {
			return 50, 0.95, 30, 0.98
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

	// Budget must exceed MaxNewcomerBudget so the newcomer slot is skipped.
	// The novice (0 tasks, maxBudget=2M) is excluded from the regular slot
	// because 6M > 2M. The expert (50 tasks, maxBudget=50M) wins.
	task := postTask(tm, "poster-b", "ML task", "ml", 6_000_000)
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
		ActiveTasks:   2,
		Available:     true,
	})

	postTask(tm, "poster-c", "Data task", "data", 30_000)
	r.routePending()

	if countClaimed(&claimed) != 0 {
		t.Errorf("expected 0 claims (agent at max concurrency), got %d", countClaimed(&claimed))
	}
}

func TestSetAvailability(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "on-off",
		Categories: []string{"testing"},
		Available:  false,
	})

	postTask(tm, "poster-d", "Test task 1", "testing", 10_000)
	r.routePending()

	if countClaimed(&claimed) != 0 {
		t.Error("agent unavailable — should not receive tasks")
	}

	ok := r.SetAvailability("on-off", true)
	if !ok {
		t.Fatal("SetAvailability returned false for known agent")
	}

	postTask(tm, "poster-d", "Test task 2", "testing", 10_000)
	r.routePending()

	// Both task 1 (previously unrouted) and task 2 are open, so 2 total claims.
	if countClaimed(&claimed) != 2 {
		t.Errorf("expected 2 claims after enabling availability, got %d", countClaimed(&claimed))
	}

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

	for i := 0; i < 3; i++ {
		postTask(tm, fmt.Sprintf("poster-%d", i), fmt.Sprintf("NLP task %d", i), "nlp", 10_000)
	}
	r.routePending()

	routes := r.RecentRoutes(0)
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}

	last2 := r.RecentRoutes(2)
	if len(last2) != 2 {
		t.Fatalf("expected 2 recent routes, got %d", len(last2))
	}

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

	postTask(tm, "poster-e", "Stats task 1", "stats", 5_000)
	postTask(tm, "poster-e", "Stats task 2", "stats", 5_000)

	stats := r.Stats()
	if stats["registered_agents"] != 1 {
		t.Errorf("registered agents: want 1, got %v", stats["registered_agents"])
	}
	if stats["available_agents"] != 1 {
		t.Errorf("available agents: want 1, got %v", stats["available_agents"])
	}
	if stats["pending_tasks"] != 2 {
		t.Errorf("pending tasks: want 2, got %v", stats["pending_tasks"])
	}
	if stats["total_routed"] != 0 {
		t.Errorf("total routed: want 0, got %v", stats["total_routed"])
	}

	r.routePending()

	stats = r.Stats()
	if stats["total_routed"] != 2 {
		t.Errorf("after routing: total_routed want 2, got %v", stats["total_routed"])
	}
	if stats["pending_tasks"] != 0 {
		t.Errorf("after routing: pending_tasks want 0, got %v", stats["pending_tasks"])
	}
}

// ---------------------------------------------------------------------------
// Newcomer fairness tests
// ---------------------------------------------------------------------------

func TestNewcomerAllocation(t *testing.T) {
	var claimed sync.Map
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		if agentID == "established" {
			return 15, 0.90, 30.0, 0.95
		}
		return 0, 0, 0, 0 // newcomer-agent has 0 tasks
	}
	r, tm := newTestRouter(&claimed, repFn)

	r.RegisterCapability(AgentCapability{
		AgentID:    "newcomer-agent",
		Categories: []string{"research"},
		Available:  true,
	})
	r.RegisterCapability(AgentCapability{
		AgentID:    "established",
		Categories: []string{"research"},
		Available:  true,
	})

	// Post 10 tasks — all small budget so newcomer slot fires for eligible ones.
	for i := 0; i < 10; i++ {
		postTask(tm, fmt.Sprintf("poster-%d", i), fmt.Sprintf("Research task %d", i), "research", 100_000)
	}
	r.routePending()

	routes := r.RecentRoutes(0)
	if len(routes) != 10 {
		t.Fatalf("expected 10 routes, got %d", len(routes))
	}

	newcomerCount := 0
	for _, rr := range routes {
		if rr.IsNewcomer {
			newcomerCount++
		}
	}
	// With the ratio controller targeting 20%, we expect exactly 2 newcomer routes
	// from 10 tasks (tasks 1 and 6, per the controller trace).
	if newcomerCount != 2 {
		t.Errorf("expected 2 newcomer routes (20%% of 10), got %d", newcomerCount)
	}
}

func TestNewcomerMatch_NoCategoryPenalty(t *testing.T) {
	var claimed sync.Map
	// Newcomer has 0 tasks — would score 0 on reputation in the established pool.
	r, tm := newTestRouter(&claimed, noReputation)

	r.RegisterCapability(AgentCapability{
		AgentID:    "fresh-agent",
		Categories: []string{"writing"},
		Available:  true,
	})

	// Small budget (< MaxNewcomerBudget) triggers the newcomer slot.
	task := postTask(tm, "poster-n", "Write a blog post", "writing", 200_000)
	r.routePending()

	if _, ok := claimed.Load(task.ID); !ok {
		t.Error("newcomer with matching category should receive the task")
	}

	routes := r.RecentRoutes(0)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if !routes[0].IsNewcomer {
		t.Error("route should be marked IsNewcomer=true")
	}
	if routes[0].AgentID != "fresh-agent" {
		t.Errorf("expected fresh-agent, got %s", routes[0].AgentID)
	}
}

func TestNewcomerMatch_PriceAdvantage(t *testing.T) {
	var claimed sync.Map
	r, tm := newTestRouter(&claimed, noReputation)

	// Two newcomers in the same category — cheaper one should win.
	r.RegisterCapability(AgentCapability{
		AgentID:      "cheap-newcomer",
		Categories:   []string{"translation"},
		PricePerTask: 1_000,
		Available:    true,
	})
	r.RegisterCapability(AgentCapability{
		AgentID:      "pricey-newcomer",
		Categories:   []string{"translation"},
		PricePerTask: 5_000,
		Available:    true,
	})

	task := postTask(tm, "poster-p", "Translate document", "translation", 10_000)
	r.routePending()

	v, ok := claimed.Load(task.ID)
	if !ok {
		t.Fatal("task not claimed")
	}
	if v.(crypto.AgentID) != "cheap-newcomer" {
		t.Errorf("expected cheap-newcomer to win via price advantage, got %s", v)
	}
}

func TestProgressiveBudget(t *testing.T) {
	// Test maxBudgetForAgent tiers directly via the router.
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		switch agentID {
		case "zero-tasks":
			return 0, 0, 0, 0
		case "three-tasks":
			return 3, 0.7, 60, 0.85
		case "fifty-tasks":
			return 50, 0.85, 45, 0.90
		case "veteran":
			return 100, 0.85, 40, 0.95 // avgScore > 0.8 → unlimited
		}
		return 0, 0, 0, 0
	}

	r, _ := newTestRouter(nil, repFn)
	// claimFn is nil — we only test maxBudgetForAgent, not routing.
	r2 := New(&taskManagerSource{tm: tasks.NewTaskManager()}, func(string, crypto.AgentID) error { return nil }, repFn, time.Second)

	cases := []struct {
		agentID   crypto.AgentID
		wantMax   uint64
	}{
		{"zero-tasks", 2_000_000},
		{"three-tasks", 5_000_000},
		{"fifty-tasks", 50_000_000},
		{"veteran", ^uint64(0)}, // unlimited
	}
	for _, tc := range cases {
		got := r2.maxBudgetForAgent(tc.agentID, "any")
		if got != tc.wantMax {
			t.Errorf("maxBudgetForAgent(%s): want %d, got %d", tc.agentID, tc.wantMax, got)
		}
	}
	_ = r // suppress unused warning
}

func TestNewcomerGraduation(t *testing.T) {
	var claimed sync.Map
	// Agent has exactly NewcomerThreshold tasks — graduated.
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		if agentID == "grad-agent" {
			return NewcomerThreshold, 0.80, 40, 0.90
		}
		return 0, 0, 0, 0
	}
	r, tm := newTestRouter(&claimed, repFn)

	r.RegisterCapability(AgentCapability{
		AgentID:    "grad-agent",
		Categories: []string{"research"},
		Available:  true,
	})

	// isNewcomer should be false for a graduated agent.
	if r.isNewcomer("grad-agent", "research") {
		t.Error("grad-agent should have graduated from newcomer pool (>=10 tasks)")
	}

	// Even though the task triggers the newcomer slot (budget <= MaxNewcomerBudget),
	// findNewcomerMatchLocked skips graduated agents. The task should still be
	// routed via the regular slot (findBestMatchLocked).
	// grad-agent has 10 tasks → maxBudgetForAgent = 20M. Budget 100K < 20M. ✓
	task := postTask(tm, "poster-g", "Research task", "research", 100_000)
	r.routePending()

	if _, ok := claimed.Load(task.ID); !ok {
		t.Error("graduated agent should still receive tasks via regular slot")
	}

	routes := r.RecentRoutes(0)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].IsNewcomer {
		t.Error("graduated agent should not be marked IsNewcomer")
	}
}

func TestNewcomerFallback(t *testing.T) {
	var claimed sync.Map
	// Only one established agent — no newcomers registered.
	repFn := func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64) {
		return 20, 0.85, 30, 0.90 // established agent
	}
	r, tm := newTestRouter(&claimed, repFn)

	r.RegisterCapability(AgentCapability{
		AgentID:    "veteran-agent",
		Categories: []string{"data"},
		Available:  true,
	})

	// Task budget ≤ MaxNewcomerBudget — newcomer slot fires, but falls back
	// to established agent when no newcomers are found.
	// veteran has 20 tasks → maxBudgetForAgent = 20M. Budget 500K < 20M. ✓
	task := postTask(tm, "poster-f", "Data pipeline", "data", 500_000)
	r.routePending()

	v, ok := claimed.Load(task.ID)
	if !ok {
		t.Fatal("task should be routed to established agent as fallback")
	}
	if v.(crypto.AgentID) != "veteran-agent" {
		t.Errorf("expected veteran-agent as fallback, got %s", v)
	}

	routes := r.RecentRoutes(0)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	// The route is NOT marked newcomer — veteran handled it.
	if routes[0].IsNewcomer {
		t.Error("veteran fallback route should not be marked IsNewcomer")
	}
}

func TestFirstTaskBoost(t *testing.T) {
	rm := reputation.NewReputationManager()

	// First task with high verification score (> 0.8).
	rm.RecordCompletion("new-agent", "writing", 10_000, 0.95, 60.0)

	rep := rm.GetReputation("new-agent")
	if rep.OverallScore < 30 {
		t.Errorf("first high-quality task should floor OverallScore at 30, got %.2f", rep.OverallScore)
	}

	// Low-quality first task (≤ 0.8) should NOT get the boost.
	rm2 := reputation.NewReputationManager()
	rm2.RecordCompletion("low-scorer", "writing", 10_000, 0.70, 90.0)
	rep2 := rm2.GetReputation("low-scorer")
	// Low-quality completion has few tasks and low rate; natural score should be < 30.
	if rep2.OverallScore >= 30 {
		t.Errorf("low-quality first task should NOT receive the boost, got %.2f", rep2.OverallScore)
	}
}
