// Package router implements AetherNet's autonomous task routing engine.
//
// The Router watches for open tasks in the marketplace and automatically
// matches each task to the best available registered agent. This transforms
// AetherNet from a pull-based marketplace (agents poll for tasks) into a
// push-based routing engine (the protocol pushes tasks to agents).
//
// Scoring for established agents is a weighted composite of four signals:
//
//	Category match  40% — exact category or tag overlap
//	Reputation      30% — avg verification score × completion rate
//	Price           15% — task budget ÷ agent price (lower price = better)
//	Availability    15% — 1 − (active_tasks / max_concurrent)
//
// Newcomer fairness: 20% of task assignments are reserved for agents with
// fewer than NewcomerThreshold completed tasks in the relevant category.
// Newcomers compete on capability match, price, and availability only —
// they are not penalised for zero reputation. Progressive budget tiers
// ensure new agents start with smaller tasks and earn larger ones as their
// track record grows.
//
// The router depends only on L1/L2 interfaces (crypto, RoutableTask) and
// never imports the tasks package directly. The TaskSource interface decouples
// the router from the L3 task marketplace implementation.
package router

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

const (
	// RoutingTimeout is the maximum time the router waits for a routed agent
	// to claim a task before clearing the assignment so it can be re-routed.
	RoutingTimeout = 60 * time.Second

	// NewcomerThreshold is the number of category-specific task completions
	// required before an agent graduates from the newcomer pool.
	NewcomerThreshold = uint64(10)

	// NewcomerAllocation is the target fraction of task routes reserved for
	// newcomer agents. The router uses a ratio controller to maintain this level.
	NewcomerAllocation = 0.20

	// MaxNewcomerBudget is the maximum task budget (micro-AET) that may be
	// routed via the newcomer slot. High-value tasks always go to established agents.
	MaxNewcomerBudget = uint64(5_000_000)
)

// RoutableTask is the minimal interface the router needs to match and route a task.
// It is implemented by tasks.Task but the router never imports the tasks package
// directly, keeping the L2 router decoupled from the L3 task marketplace.
type RoutableTask interface {
	GetID() string
	GetCategory() string
	GetBudget() uint64
	GetStatus() string
	GetPosterID() string
	GetTags() []string
	GetTitle() string
	GetDescription() string
	GetRoutedTo() string
	GetRoutedAt() int64
}

// TaskSource provides open tasks to the router without coupling to a specific
// task manager implementation. cmd/node/main.go provides an adapter that
// converts *tasks.TaskManager to this interface.
type TaskSource interface {
	OpenTasks() []RoutableTask
}

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

// matchCandidate holds the result of a match-scoring pass.
type matchCandidate struct {
	AgentID crypto.AgentID
	Score   float64
	Reason  string
}

// RouteResult records a single routing decision.
type RouteResult struct {
	TaskID     string         `json:"task_id"`
	AgentID    crypto.AgentID `json:"agent_id"`
	Score      float64        `json:"match_score"`
	Reason     string         `json:"reason"`
	RoutedAt   int64          `json:"routed_at"`
	IsNewcomer bool           `json:"is_newcomer"`
}

// Router watches the task marketplace and pushes open tasks to the best
// matching registered agent.
//
// It is safe for concurrent use by multiple goroutines.
type Router struct {
	mu             sync.RWMutex
	capabilities   map[crypto.AgentID]*AgentCapability
	routeHistory   []*RouteResult
	taskSource     TaskSource
	claimFunc      func(taskID string, agentID crypto.AgentID) error
	clearRoutedTo  func(taskID string) error
	reputationFunc func(agentID crypto.AgentID, category string) (completed uint64, avgScore float64, avgDelivery float64, completionRate float64)
	stop           chan struct{}
	interval       time.Duration

	// Configurable routing parameters — default to the package constants.
	newcomerThreshold  uint64
	newcomerAllocation float64
	maxNewcomerBudget  uint64
	webhookTimeout     time.Duration
}

// New creates a Router backed by the provided task source.
//
//   - ts is the source of open tasks. May be nil; the router will simply never
//     route any tasks when nil (all routing passes return immediately).
//   - claimFn is called to atomically claim a matched task. It must be safe for
//     concurrent calls.
//   - repFn returns live reputation metrics for an agent in a specific category.
//     It may be nil (all reputation scores will be 0 and all agents treated as newcomers).
//   - interval controls how often the routing loop runs.
func New(
	ts TaskSource,
	claimFn func(taskID string, agentID crypto.AgentID) error,
	repFn func(agentID crypto.AgentID, category string) (uint64, float64, float64, float64),
	interval time.Duration,
) *Router {
	return &Router{
		capabilities:       make(map[crypto.AgentID]*AgentCapability),
		taskSource:         ts,
		claimFunc:          claimFn,
		reputationFunc:     repFn,
		stop:               make(chan struct{}),
		interval:           interval,
		newcomerThreshold:  NewcomerThreshold,
		newcomerAllocation: NewcomerAllocation,
		maxNewcomerBudget:  MaxNewcomerBudget,
		webhookTimeout:     5 * time.Second,
	}
}

// SetNewcomerParams overrides the newcomer fairness parameters.
// threshold is the per-category task count before graduation from the newcomer pool;
// allocation is the target fraction of routes reserved for newcomers (0.0–1.0);
// maxBudget is the maximum task budget (micro-AET) routable via the newcomer slot.
// Call before Start.
func (r *Router) SetNewcomerParams(threshold uint64, allocation float64, maxBudget uint64) {
	r.mu.Lock()
	r.newcomerThreshold = threshold
	r.newcomerAllocation = allocation
	r.maxNewcomerBudget = maxBudget
	r.mu.Unlock()
}

// SetWebhookTimeout overrides the HTTP client timeout for webhook deliveries.
// Call before Start.
func (r *Router) SetWebhookTimeout(d time.Duration) {
	r.mu.Lock()
	r.webhookTimeout = d
	r.mu.Unlock()
}

// SetClearRoutedToFunc registers the callback the router calls to expire a
// stale routing assignment. When a task has been routed but not claimed within
// RoutingTimeout, the router calls fn(taskID) to clear routed_to so the task
// can be re-routed on the next cycle. Call before Start.
func (r *Router) SetClearRoutedToFunc(fn func(taskID string) error) {
	r.mu.Lock()
	r.clearRoutedTo = fn
	r.mu.Unlock()
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

// Stats returns a point-in-time aggregate view of the routing engine,
// including newcomer fairness metrics.
func (r *Router) Stats() map[string]any {
	r.mu.RLock()
	registered := len(r.capabilities)
	available := r.countAvailableLocked()
	totalRouted := len(r.routeHistory)
	newcomerRoutes := 0
	for _, rr := range r.routeHistory {
		if rr.IsNewcomer {
			newcomerRoutes++
		}
	}
	r.mu.RUnlock()

	pending := 0
	if r.taskSource != nil {
		pending = len(r.taskSource.OpenTasks())
	}

	r.mu.RLock()
	threshold := r.newcomerThreshold
	allocation := r.newcomerAllocation
	r.mu.RUnlock()

	return map[string]any{
		"registered_agents":   registered,
		"available_agents":    available,
		"total_routed":        totalRouted,
		"pending_tasks":       pending,
		"newcomer_routes":     newcomerRoutes,
		"newcomer_threshold":  threshold,
		"newcomer_allocation": allocation,
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

// routePending fetches all open tasks, then acquires the write lock and
// attempts to match each task to the best available registered agent.
// It maintains a 20% allocation for newcomer agents via a ratio controller.
func (r *Router) routePending() {
	if r.taskSource == nil {
		return
	}
	open := r.taskSource.OpenTasks()
	if len(open) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	routeCount := 0
	newcomerRoutes := 0

	for _, task := range open {
		// If this task is already routed, check whether the assignment has
		// expired. If the routed agent hasn't claimed within RoutingTimeout,
		// clear the assignment so we can re-route it below. If the assignment
		// is still fresh, skip the task entirely.
		if task.GetRoutedTo() != "" {
			routedAt := task.GetRoutedAt()
			if routedAt == 0 || time.Since(time.Unix(0, routedAt)) < RoutingTimeout {
				continue // still within the claim window
			}
			// Assignment expired — clear it and fall through to re-route.
			if r.clearRoutedTo != nil {
				if err := r.clearRoutedTo(task.GetID()); err != nil {
					slog.Debug("router: could not clear expired route",
						"task_id", task.GetID(),
						"was_routed_to", task.GetRoutedTo(),
						"err", err)
					continue
				}
				slog.Info("router: routing assignment expired, re-routing",
					"task_id", task.GetID(),
					"was_routed_to", task.GetRoutedTo())
			} else {
				continue // no clear func configured — leave as-is
			}
		}

		routeCount++

		// Ratio controller: use the newcomer slot when the current newcomer
		// fraction is below the target allocation.
		newcomerRatio := float64(newcomerRoutes) / float64(routeCount)
		useNewcomer := newcomerRatio < r.newcomerAllocation

		var match *matchCandidate
		if useNewcomer && task.GetBudget() <= r.maxNewcomerBudget {
			match = r.findNewcomerMatchLocked(task)
		}
		// Fall through to the best overall match when no newcomer is available.
		if match == nil {
			match = r.findBestMatchLocked(task)
		}
		if match == nil {
			continue
		}

		if err := r.claimFunc(task.GetID(), match.AgentID); err != nil {
			continue
		}

		isNewcomer := r.isNewcomer(match.AgentID, task.GetCategory())
		if isNewcomer {
			newcomerRoutes++
		}

		result := &RouteResult{
			TaskID:     task.GetID(),
			AgentID:    match.AgentID,
			Score:      match.Score,
			Reason:     match.Reason,
			RoutedAt:   time.Now().UnixNano(),
			IsNewcomer: isNewcomer,
		}
		r.routeHistory = append(r.routeHistory, result)
		if len(r.routeHistory) > 100 {
			r.routeHistory = r.routeHistory[len(r.routeHistory)-100:]
		}

		// Webhook notification is best-effort and must not block the routing loop.
		// Spawn the goroutine with captured values so it doesn't race against the
		// loop variable; it will acquire RLock after we release the write lock.
		go func(agentID crypto.AgentID, t RoutableTask) {
			_ = r.NotifyAgent(agentID, t)
		}(match.AgentID, task)
	}
}

// findBestMatchLocked returns the highest-scoring agent for task considering
// all registered agents (newcomers and established alike). Assumes r.mu is held.
func (r *Router) findBestMatchLocked(task RoutableTask) *matchCandidate {
	var best *matchCandidate
	bestScore := 0.0 // require positive score (zero means no category alignment)

	for _, cap := range r.capabilities {
		if !cap.Available {
			continue
		}
		if cap.MaxConcurrent > 0 && cap.ActiveTasks >= cap.MaxConcurrent {
			continue
		}
		if string(cap.AgentID) == task.GetPosterID() {
			continue
		}
		if cap.PricePerTask > 0 && task.GetBudget() > 0 && cap.PricePerTask > task.GetBudget() {
			continue
		}
		// Progressive budget ceiling: skip agents whose track record doesn't
		// yet qualify them for tasks of this size.
		if task.GetBudget() > r.maxBudgetForAgent(cap.AgentID, task.GetCategory()) {
			continue
		}

		score := r.scoreMatch(cap, task)
		if score > bestScore {
			bestScore = score
			best = &matchCandidate{
				AgentID: cap.AgentID,
				Score:   score,
				Reason:  fmt.Sprintf("category=%q score=%.3f", task.GetCategory(), score),
			}
		}
	}
	return best
}

// findNewcomerMatchLocked finds the best agent from the newcomer pool only.
// Newcomers are scored WITHOUT reputation weight — they compete on capability
// match, price, and availability only. Assumes r.mu is held.
func (r *Router) findNewcomerMatchLocked(task RoutableTask) *matchCandidate {
	var candidates []matchCandidate

	for _, cap := range r.capabilities {
		if !cap.Available || (cap.MaxConcurrent > 0 && cap.ActiveTasks >= cap.MaxConcurrent) {
			continue
		}
		if string(cap.AgentID) == task.GetPosterID() {
			continue
		}
		if cap.PricePerTask > 0 && cap.PricePerTask > task.GetBudget() {
			continue
		}
		if task.GetBudget() > r.maxBudgetForAgent(cap.AgentID, task.GetCategory()) {
			continue
		}
		if !r.isNewcomer(cap.AgentID, task.GetCategory()) {
			continue
		}

		score, reason := r.scoreNewcomer(task, cap)
		if score > 0 {
			candidates = append(candidates, matchCandidate{
				AgentID: cap.AgentID,
				Score:   score,
				Reason:  "newcomer+" + reason,
			})
		}
	}

	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	return &candidates[0]
}

// scoreNewcomer scores a newcomer agent WITHOUT reputation weight.
// Weights: category match 50%, price efficiency 30%, tag specificity 20%.
// Returns (0, "") when the agent has no category or tag alignment with the task.
func (r *Router) scoreNewcomer(task RoutableTask, cap *AgentCapability) (float64, string) {
	score := 0.0
	var reasons []string

	// Category match (50%)
	categoryMatch := false
	for _, cat := range cap.Categories {
		if strings.EqualFold(cat, task.GetCategory()) {
			categoryMatch = true
			break
		}
	}
	if categoryMatch {
		score += 0.5
		reasons = append(reasons, "category")
	} else {
		tagOvl := tagOverlap(cap.Tags, task.GetTags())
		if tagOvl > 0 {
			score += 0.2 * tagOvl
			reasons = append(reasons, "tags")
		} else {
			return 0, "" // no relevance at all
		}
	}

	// Price efficiency (30%): newcomers can compete on price
	if cap.PricePerTask > 0 && task.GetBudget() > 0 {
		priceRatio := 1.0 - float64(cap.PricePerTask)/float64(task.GetBudget())
		if priceRatio < 0 {
			priceRatio = 0
		}
		score += 0.3 * priceRatio
		reasons = append(reasons, "price")
	} else {
		score += 0.2 // default when no price constraint
	}

	// Tag specificity (20%): reward detailed capability descriptions
	if len(cap.Tags) > 0 && len(task.GetTags()) > 0 {
		score += 0.2 * tagOverlap(cap.Tags, task.GetTags())
		reasons = append(reasons, "specificity")
	} else {
		score += 0.1
	}

	return score, strings.Join(reasons, "+")
}

// isNewcomer returns true when agentID has fewer than NewcomerThreshold
// completed tasks in category. Agents with no reputation data are treated
// as newcomers.
func (r *Router) isNewcomer(agentID crypto.AgentID, category string) bool {
	if r.reputationFunc == nil {
		return true
	}
	completed, _, _, _ := r.reputationFunc(agentID, category)
	return completed < r.newcomerThreshold
}

// maxBudgetForAgent returns the maximum task budget (micro-AET) that agentID
// qualifies for based on their completed task count in category.
//
// Progressive tiers:
//
//	0–2 tasks:          2 AET  (2,000,000 micro)
//	3–9 tasks:          5 AET  (5,000,000 micro)
//	10–49 tasks:       20 AET  (20,000,000 micro)
//	50–99 tasks:       50 AET  (50,000,000 micro)
//	100+ tasks, avgScore > 0.8: unlimited
func (r *Router) maxBudgetForAgent(agentID crypto.AgentID, category string) uint64 {
	if r.reputationFunc == nil {
		return r.maxNewcomerBudget
	}
	completed, avgScore, _, _ := r.reputationFunc(agentID, category)
	switch {
	case completed >= 100 && avgScore > 0.8:
		return ^uint64(0) // unlimited
	case completed >= 50:
		return 50_000_000
	case completed >= 10:
		return 20_000_000
	case completed >= 3:
		return 5_000_000
	default:
		return 2_000_000
	}
}

// countAvailableLocked counts available agents. Must be called with r.mu held.
func (r *Router) countAvailableLocked() int {
	count := 0
	for _, cap := range r.capabilities {
		if cap.Available {
			count++
		}
	}
	return count
}

// scoreMatch computes the composite match score between an agent capability
// and a task using the four-factor weighted model.
//
//	category  40%
//	reputation 30%
//	price      15%
//	availability 15%
//
// scoreMatch does not acquire any locks and is safe to call from locked context.
func (r *Router) scoreMatch(cap *AgentCapability, task RoutableTask) float64 {
	// ── Category match (40%) ────────────────────────────────────────────────
	categoryScore := 0.0
	for _, c := range cap.Categories {
		if strings.EqualFold(c, task.GetCategory()) {
			categoryScore = 1.0
			break
		}
	}
	// If no exact category match, try tag overlap as a partial signal.
	if categoryScore == 0 && len(task.GetTags()) > 0 {
		categoryScore = tagOverlap(cap.Tags, task.GetTags()) * 0.5
	}
	// Require at least some category/tag alignment — never route a task to
	// an agent with zero relevance just because of price/availability.
	if categoryScore == 0 {
		return 0
	}

	// ── Reputation (30%) ────────────────────────────────────────────────────
	reputationScore := 0.0
	if r.reputationFunc != nil {
		completed, avgScore, _, completionRate := r.reputationFunc(cap.AgentID, task.GetCategory())
		if completed > 0 {
			// Blend quality (avg score) and reliability (completion rate).
			reputationScore = avgScore*0.6 + completionRate*0.4
		}
	}

	// ── Price efficiency (15%) ──────────────────────────────────────────────
	priceScore := 1.0
	if cap.PricePerTask > 0 && task.GetBudget() > 0 {
		// Ratio > 1 means the budget covers the price comfortably — cap at 1.
		ratio := float64(task.GetBudget()) / float64(cap.PricePerTask)
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
