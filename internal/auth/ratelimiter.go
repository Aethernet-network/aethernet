package auth

import (
	"log/slog"
	"sync"
	"time"
)

// EndpointLimit defines rate limits for a specific endpoint.
type EndpointLimit struct {
	PerAgentPerHr int
	PerIPPerHr    int
	GlobalPerHr   int
}

// DefaultLimits returns the production rate limits per endpoint.
func DefaultLimits() map[string]EndpointLimit {
	return map[string]EndpointLimit{
		// L1: Core protocol
		"/v1/agents":        {PerAgentPerHr: 0, PerIPPerHr: 3, GlobalPerHr: 30},
		"/v1/faucet":        {PerAgentPerHr: 1, PerIPPerHr: 3, GlobalPerHr: 84},
		"/v1/transfer":      {PerAgentPerHr: 120, PerIPPerHr: 300, GlobalPerHr: 3000},
		"/v1/stake":         {PerAgentPerHr: 20, PerIPPerHr: 100, GlobalPerHr: 500},
		"/v1/unstake":       {PerAgentPerHr: 20, PerIPPerHr: 100, GlobalPerHr: 500},
		// L2: Coordination
		"/v1/registry":              {PerAgentPerHr: 10, PerIPPerHr: 20, GlobalPerHr: 200},
		"/v1/router/register":       {PerAgentPerHr: 10, PerIPPerHr: 20, GlobalPerHr: 200},
		"/v1/router/unregister":     {PerAgentPerHr: 10, PerIPPerHr: 20, GlobalPerHr: 200},
		"/v1/router/availability":   {PerAgentPerHr: 60, PerIPPerHr: 200, GlobalPerHr: 2000},
		"/v1/platform/keys":         {PerAgentPerHr: 5, PerIPPerHr: 10, GlobalPerHr: 50},
		// L3: Application
		"/v1/tasks":         {PerAgentPerHr: 20, PerIPPerHr: 50, GlobalPerHr: 500},
		"/v1/tasks/claim":   {PerAgentPerHr: 60, PerIPPerHr: 200, GlobalPerHr: 2000},
		"/v1/tasks/submit":  {PerAgentPerHr: 60, PerIPPerHr: 200, GlobalPerHr: 2000},
		"/v1/tasks/approve": {PerAgentPerHr: 60, PerIPPerHr: 200, GlobalPerHr: 2000},
		"/v1/tasks/dispute": {PerAgentPerHr: 10, PerIPPerHr: 50, GlobalPerHr: 200},
		"/v1/tasks/cancel":  {PerAgentPerHr: 20, PerIPPerHr: 50, GlobalPerHr: 500},
		"/v1/challenge":     {PerAgentPerHr: 10, PerIPPerHr: 50, GlobalPerHr: 200},
	}
}

// EndpointRateLimiter enforces per-agent, per-IP, and global rate limits
// on a per-endpoint basis. Safe for concurrent use.
type EndpointRateLimiter struct {
	mu      sync.Mutex
	limits  map[string]EndpointLimit
	windows map[string]*rlWindow // unified counter map, keyed by bucket
}

type rlWindow struct {
	count    int
	windowAt int64 // Unix hour
}

// NewEndpointRateLimiter creates a rate limiter with the given endpoint limits.
func NewEndpointRateLimiter(limits map[string]EndpointLimit) *EndpointRateLimiter {
	return &EndpointRateLimiter{
		limits:  limits,
		windows: make(map[string]*rlWindow),
	}
}

// Allow checks rate limits for an endpoint. Returns (allowed, retryAfterSecs).
func (rl *EndpointRateLimiter) Allow(endpoint, agentID, ip string) (bool, int) {
	limit, ok := rl.limits[endpoint]
	if !ok {
		return true, 0
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now().Unix()
	hour := now / 3600
	retryAfter := int(3600 - (now % 3600))

	// Check all three dimensions before incrementing any.
	if agentID != "" && limit.PerAgentPerHr > 0 {
		if !rl.check(endpoint+":a:"+agentID, hour, limit.PerAgentPerHr) {
			slog.Warn("rate limit: per-agent exceeded",
				"agent", agentID, "endpoint", endpoint, "limit", limit.PerAgentPerHr)
			return false, retryAfter
		}
	}
	if ip != "" && limit.PerIPPerHr > 0 {
		if !rl.check(endpoint+":i:"+ip, hour, limit.PerIPPerHr) {
			slog.Warn("rate limit: per-IP exceeded",
				"ip", ip, "endpoint", endpoint, "limit", limit.PerIPPerHr)
			return false, retryAfter
		}
	}
	if limit.GlobalPerHr > 0 {
		if !rl.check(endpoint+":g", hour, limit.GlobalPerHr) {
			slog.Warn("rate limit: global exceeded",
				"endpoint", endpoint, "limit", limit.GlobalPerHr)
			return false, retryAfter
		}
	}

	// All passed — increment.
	if agentID != "" && limit.PerAgentPerHr > 0 {
		rl.inc(endpoint+":a:"+agentID, hour)
	}
	if ip != "" && limit.PerIPPerHr > 0 {
		rl.inc(endpoint+":i:"+ip, hour)
	}
	if limit.GlobalPerHr > 0 {
		rl.inc(endpoint+":g", hour)
	}
	return true, 0
}

func (rl *EndpointRateLimiter) check(key string, hour int64, max int) bool {
	w, ok := rl.windows[key]
	if !ok || w.windowAt != hour {
		return true
	}
	return w.count < max
}

func (rl *EndpointRateLimiter) inc(key string, hour int64) {
	w, ok := rl.windows[key]
	if !ok || w.windowAt != hour {
		rl.windows[key] = &rlWindow{count: 1, windowAt: hour}
		return
	}
	w.count++
}

// Remaining returns remaining requests for an agent on an endpoint.
// Returns -1 if no limit is configured.
func (rl *EndpointRateLimiter) Remaining(endpoint, agentID string) int {
	limit, ok := rl.limits[endpoint]
	if !ok || limit.PerAgentPerHr <= 0 {
		return -1
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	hour := time.Now().Unix() / 3600
	w, ok := rl.windows[endpoint+":a:"+agentID]
	if !ok || w.windowAt != hour {
		return limit.PerAgentPerHr
	}
	rem := limit.PerAgentPerHr - w.count
	if rem < 0 {
		return 0
	}
	return rem
}
