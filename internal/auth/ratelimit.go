package auth

import (
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// AgentRateLimiter tracks request rates per agent to prevent nonce spam.
// Safe for concurrent use.
type AgentRateLimiter struct {
	mu       sync.Mutex
	counts   map[crypto.AgentID]*agentWindow
	maxPerHr int
}

type agentWindow struct {
	count    int
	windowAt int64 // Unix hour boundary
}

// NewAgentRateLimiter creates a rate limiter allowing maxPerHour signed
// requests per agent per hour.
func NewAgentRateLimiter(maxPerHour int) *AgentRateLimiter {
	return &AgentRateLimiter{
		counts:   make(map[crypto.AgentID]*agentWindow),
		maxPerHr: maxPerHour,
	}
}

// Allow returns true if the agent has not exceeded the hourly limit.
// Each call that returns true increments the agent's counter.
func (l *AgentRateLimiter) Allow(agentID crypto.AgentID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().Unix()
	hour := now / 3600

	w, exists := l.counts[agentID]
	if !exists || w.windowAt != hour {
		l.counts[agentID] = &agentWindow{count: 1, windowAt: hour}
		return true
	}
	if w.count >= l.maxPerHr {
		return false
	}
	w.count++
	return true
}
