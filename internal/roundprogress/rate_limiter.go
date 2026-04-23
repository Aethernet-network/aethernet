package roundprogress

import "sync"

const defaultRateLimitIntervalSec int64 = 10

// RateLimiter enforces a minimum interval between progress updates
// per (validator, round, family). Default: 10 seconds.
type RateLimiter struct {
	intervalSec int64
	last        map[string]int64 // key → last update unix
	mu          sync.Mutex
}

// NewRateLimiter creates a rate limiter with the given minimum interval.
// Pass 0 for the default (10s). Pass -1 to disable rate limiting (always allow).
func NewRateLimiter(intervalSec int64) *RateLimiter {
	if intervalSec == 0 {
		intervalSec = defaultRateLimitIntervalSec
	}
	return &RateLimiter{
		intervalSec: intervalSec,
		last:        make(map[string]int64),
	}
}

func rateLimitKey(roundID, validatorID, family string) string {
	return roundID + ":" + validatorID + ":" + family
}

// Allow returns true if the update is within the rate limit. If allowed,
// records the timestamp. All times are unix seconds.
func (rl *RateLimiter) Allow(roundID, validatorID, family string, nowUnix int64) bool {
	key := rateLimitKey(roundID, validatorID, family)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.intervalSec < 0 {
		return true // rate limiting disabled
	}
	lastUpdate, ok := rl.last[key]
	if ok && (nowUnix-lastUpdate) < rl.intervalSec {
		return false
	}
	rl.last[key] = nowUnix
	return true
}

// CleanupRound removes all rate limit entries for a finalized round.
func (rl *RateLimiter) CleanupRound(roundID string) {
	prefix := roundID + ":"
	rl.mu.Lock()
	defer rl.mu.Unlock()
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for k := range rl.last {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(rl.last, k)
		}
	}
}
