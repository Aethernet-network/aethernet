// Package router implements AetherNet's autonomous task routing engine.
//
// The Router watches for open tasks in the marketplace and automatically
// matches each task to the best available registered agent. This transforms
// AetherNet from a pull-based marketplace (agents poll for tasks) into a
// push-based routing engine (the protocol pushes tasks to agents).
//
// Scoring is a weighted composite of four signals:
//
//	Category match  40% — exact category or tag overlap
//	Reputation      30% — avg verification score × completion rate
//	Price           15% — task budget ÷ agent price (lower price = better)
//	Availability    15% — 1 − (active_tasks / max_concurrent)
//
// Agents register their capabilities via RegisterCapability, opting in to
// automatic task assignment. The routing loop runs every [interval] seconds.
// On match the Router calls the provided claimFunc to atomically claim the
// task, then sends a signed webhook notification to the agent's endpoint.
package router

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

// AgentCapability describes a registered agent's routing profile.
type AgentCapability struct {
	AgentID       crypto.AgentID `json:"agent_id"`
	Categories    []string       `json:"categories"`
	Tags          []string       `json:"tags,omitempty"`
	Description   string         `json:"description,omitempty"`
	PricePerTask  uint64         `json:"price_per_task"` // micro-AET; 0 = any budget
	MaxConcurrent int            `json:"max_concurrent"` // 0 = unlimited
	ActiveTasks   int            `json:"active_tasks"`
	Available     bool           `json:"available"`
	Webhook       *WebhookConfig `json:"webhook,omitempty"`

	// Snapshot reputation metrics — populated by the Router from reputationFunc
	// when the capability is first registered and refreshed each routing pass.
	TasksCompleted uint64  `json:"tasks_completed"`
	AvgScore       float64 `json:"avg_score"`
	AvgDelivery    float64 `json:"avg_delivery_secs"`
	CompletionRate float64 `json:"completion_rate"`
}

// RouteResult records a single routing decision.
type RouteResult struct {
	TaskID   string         `json:"task_id"`
	AgentID  crypto.AgentID `json:"agent_id"`
	Score    float64        `json:"match_score"`
	Reason   string         `json:"reason"`
	RoutedAt int64          `json:"routed_at"`
}

// RouterStats holds aggregate statistics about the routing engine.
type RouterStats struct {
	RegisteredAgents int `json:"registered_agents"`
	AvailableAgents  int `json:"available_agents"`
	TotalRouted      int `json:"total_routed"`
	PendingTasks     int `json:"pending_tasks"`
}

// Router watches the task marketplace and pushes open tasks to the best
// matching registered agent.
//
// It is safe for concurrent use by multiple goroutines.
type Router struct {
	mu             sync.RWMutex
	capabilities   map[crypto.AgentID]*AgentCapability
	routeHistory   []*RouteResult
	taskManager    *tasks.TaskManager
	claimFunc      func(taskID string, agentID crypto.AgentID) error
	reputationFunc func(agentID crypto.AgentID, category string) (completed uint64, avgScore float64, avgDelivery float64, completionRate float64)
	stop           chan struct{}
	interval       time.Duration
}

// New creates a Router backed by the provided task manager.
//
//   - claimFn is called to atomically claim a matched task. It must be safe for
//     concurrent calls.
//   - repFn returns live reputation metrics for an agent in a specific category.
//     It may be nil (all reputation scores will be 0).
//   - interval controls how often the routing loop runs.
func New(
	tm *tasks.TaskManager,
	claimFn func(taskID string, agentID crypto.AgentID) error,
	repFn func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64),
	interval time.Duration,
) *Router {
	return &Router{
		capabilities:   make(map[crypto.AgentID]*AgentCapability),
		taskManager:    tm,
		claimFunc:      claimFn,
		reputationFunc: repFn,
		stop:           make(chan struct{}),
		interval:       interval,
	}
}

// RegisterCapability registers or updates an agent's routing profile.
// The call is safe to make while the router is running.
func (r *Router) RegisterCapability(cap AgentCapability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := cap
	if cp.Webhook != nil {
		wh := *cp.Webhook
		cp.Webhook = &wh
	}
	r.capabilities[cap.AgentID] = &cp
}

// UnregisterCapability removes an agent from automatic task routing.
// It is a no-op when the agent is not registered.
func (r *Router) UnregisterCapability(agentID crypto.AgentID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.capabilities, agentID)
}

// SetAvailability toggles whether the router will assign tasks to agentID.
// Returns false when the agent is not registered.
func (r *Router) SetAvailability(agentID crypto.AgentID, available bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cap, ok := r.capabilities[agentID]
	if !ok {
		return false
	}
	cap.Available = available
	return true
}

// Start launches the background routing loop. It runs an initial pass after a
// 10-second warmup delay, then repeats every r.interval.
// Call Stop to shut the loop down.
func (r *Router) Start() {
	go func() {
		// Give the rest of the stack time to finish starting before the first pass.
		select {
		case <-r.stop:
			return
		case <-time.After(10 * time.Second):
		}
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				r.routePending()
			}
		}
	}()
}

// Stop signals the routing loop to exit. It is safe to call multiple times.
func (r *Router) Stop() {
	select {
	case <-r.stop:
		// already stopped
	default:
		close(r.stop)
	}
}

// RecentRoutes returns the most recent routing decisions, ordered oldest-first.
// limit ≤ 0 returns all recorded decisions (up to the internal cap of 100).
func (r *Router) RecentRoutes(limit int) []*RouteResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := len(r.routeHistory)
	if limit <= 0 || limit >= n {
		cp := make([]*RouteResult, n)
		copy(cp, r.routeHistory)
		return cp
	}
	cp := make([]*RouteResult, limit)
	copy(cp, r.routeHistory[n-limit:])
	return cp
}

// Stats returns a point-in-time aggregate view of the routing engine.
func (r *Router) Stats() RouterStats {
	r.mu.RLock()
	registered := len(r.capabilities)
	available := 0
	for _, cap := range r.capabilities {
		if cap.Available {
			available++
		}
	}
	totalRouted := len(r.routeHistory)
	r.mu.RUnlock()

	pending := len(r.taskManager.OpenTasks(0))

	return RouterStats{
		RegisteredAgents: registered,
		AvailableAgents:  available,
		TotalRouted:      totalRouted,
		PendingTasks:     pending,
	}
}

// RegisteredAgents returns a snapshot of all currently registered capabilities.
func (r *Router) RegisteredAgents() []*AgentCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*AgentCapability, 0, len(r.capabilities))
	for _, cap := range r.capabilities {
		cp := *cap
		result = append(result, &cp)
	}
	return result
}

// ---------------------------------------------------------------------------
// Internal routing logic
// ---------------------------------------------------------------------------

// routePending fetches all open tasks and attempts to match each to the best
// available registered agent. Successfully matched tasks are claimed via
// claimFunc and a webhook notification is sent asynchronously.
func (r *Router) routePending() {
	open := r.taskManager.OpenTasks(0)
	for _, task := range open {
		best, score := r.findBestMatch(task)
		if best == nil {
			continue
		}
		if err := r.claimFunc(task.ID, best.AgentID); err != nil {
			// Task may have been claimed by another agent between the open-task
			// listing and our claim attempt — not an error worth logging loudly.
			continue
		}

		result := &RouteResult{
			TaskID:   task.ID,
			AgentID:  best.AgentID,
			Score:    score,
			Reason:   fmt.Sprintf("category=%q score=%.3f", task.Category, score),
			RoutedAt: time.Now().UnixNano(),
		}

		r.mu.Lock()
		r.routeHistory = append(r.routeHistory, result)
		// Cap history at 100 entries to bound memory usage.
		if len(r.routeHistory) > 100 {
			r.routeHistory = r.routeHistory[len(r.routeHistory)-100:]
		}
		r.mu.Unlock()

		// Webhook notification is best-effort and must not block the routing loop.
		go func(agentID crypto.AgentID, t *tasks.Task) {
			_ = r.NotifyAgent(agentID, t)
		}(best.AgentID, task)
	}
}

// findBestMatch returns the highest-scoring registered agent for task,
// together with the composite score. Returns (nil, 0) when no eligible
// agent is found.
func (r *Router) findBestMatch(task *tasks.Task) (*AgentCapability, float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var bestAgent *AgentCapability
	bestScore := 0.0 // require a positive score; zero means no category alignment

	for _, cap := range r.capabilities {
		if !cap.Available {
			continue
		}
		if cap.MaxConcurrent > 0 && cap.ActiveTasks >= cap.MaxConcurrent {
			continue
		}
		// An agent must not be routed its own tasks.
		if string(cap.AgentID) == task.PosterID {
			continue
		}
		// Agent's price must fit within the task budget.
		if cap.PricePerTask > 0 && task.Budget > 0 && cap.PricePerTask > task.Budget {
			continue
		}

		score := r.scoreMatch(cap, task)
		if score > bestScore {
			bestScore = score
			bestAgent = cap
		}
	}

	return bestAgent, bestScore
}

// scoreMatch computes the composite match score between an agent capability
// and a task using the four-factor weighted model.
//
//	category  40%
//	reputation 30%
//	price      15%
//	availability 15%
func (r *Router) scoreMatch(cap *AgentCapability, task *tasks.Task) float64 {
	// ── Category match (40%) ────────────────────────────────────────────────
	categoryScore := 0.0
	for _, c := range cap.Categories {
		if strings.EqualFold(c, task.Category) {
			categoryScore = 1.0
			break
		}
	}
	// If no exact category match, try tag overlap as a partial signal.
	if categoryScore == 0 && len(task.Tags) > 0 {
		categoryScore = tagOverlap(cap.Tags, task.Tags) * 0.5
	}
	// Require at least some category/tag alignment — never route a task to
	// an agent with zero relevance just because of price/availability.
	if categoryScore == 0 {
		return 0
	}

	// ── Reputation (30%) ────────────────────────────────────────────────────
	reputationScore := 0.0
	if r.reputationFunc != nil {
		completed, avgScore, _, completionRate := r.reputationFunc(cap.AgentID, task.Category)
		if completed > 0 {
			// Blend quality (avg score) and reliability (completion rate).
			reputationScore = avgScore*0.6 + completionRate*0.4
		}
	}

	// ── Price efficiency (15%) ──────────────────────────────────────────────
	priceScore := 1.0
	if cap.PricePerTask > 0 && task.Budget > 0 {
		// Ratio > 1 means the budget covers the price comfortably — cap at 1.
		ratio := float64(task.Budget) / float64(cap.PricePerTask)
		if ratio < 1.0 {
			priceScore = ratio
		}
	}

	// ── Availability (15%) ──────────────────────────────────────────────────
	availabilityScore := 1.0
	if cap.MaxConcurrent > 0 {
		availabilityScore = 1.0 - float64(cap.ActiveTasks)/float64(cap.MaxConcurrent)
		if availabilityScore < 0 {
			availabilityScore = 0
		}
	}

	return categoryScore*0.40 + reputationScore*0.30 + priceScore*0.15 + availabilityScore*0.15
}

// tagOverlap returns the fraction of taskTags that appear in agentTags
// (case-insensitive). Returns 0 when either slice is empty.
func tagOverlap(agentTags, taskTags []string) float64 {
	if len(taskTags) == 0 || len(agentTags) == 0 {
		return 0
	}
	agentSet := make(map[string]struct{}, len(agentTags))
	for _, t := range agentTags {
		agentSet[strings.ToLower(t)] = struct{}{}
	}
	matches := 0
	for _, t := range taskTags {
		if _, ok := agentSet[strings.ToLower(t)]; ok {
			matches++
		}
	}
	return float64(matches) / float64(len(taskTags))
}
