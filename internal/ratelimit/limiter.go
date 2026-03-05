// Package ratelimit provides a token-bucket rate limiter with per-key tracking
// and automatic cleanup of idle buckets.
//
// Typical use: create one Limiter for write endpoints and one for read-only
// endpoints, then apply them through the HTTP middleware in this package.
package ratelimit

import (
	"sync"
	"time"
)

// Config holds token-bucket parameters.
type Config struct {
	Rate       float64       // tokens refilled per second
	Burst      int           // maximum token capacity (initial tokens = Burst)
	CleanupAge time.Duration // idle buckets older than this are removed
}

// DefaultConfig returns settings for write (mutating) endpoints.
// Allows 10 requests per second with a burst of 50.
func DefaultConfig() Config {
	return Config{Rate: 10, Burst: 50, CleanupAge: 5 * time.Minute}
}

// ReadOnlyConfig returns settings for read-only endpoints.
// Allows 30 requests per second with a burst of 100.
func ReadOnlyConfig() Config {
	return Config{Rate: 30, Burst: 100, CleanupAge: 5 * time.Minute}
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// Limiter is a thread-safe token-bucket rate limiter with per-key state.
// Each distinct key (typically a client IP address) gets its own token bucket.
type Limiter struct {
	mu      sync.Mutex
	cfg     Config
	buckets map[string]*bucket
	stop    chan struct{}
}

// New creates a Limiter using cfg and starts a background cleanup goroutine.
// Call Stop when the limiter is no longer needed to release resources.
func New(cfg Config) *Limiter {
	l := &Limiter{
		cfg:     cfg,
		buckets: make(map[string]*bucket),
		stop:    make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

// Allow reports whether a request from key is within the rate limit.
// It consumes one token from key's bucket, refilling tokens since the last call
// at the configured rate. Returns false when no tokens are available.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.cfg.Burst), lastSeen: now}
		l.buckets[key] = b
	}

	// Refill tokens proportional to elapsed time.
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * l.cfg.Rate
	if b.tokens > float64(l.cfg.Burst) {
		b.tokens = float64(l.cfg.Burst)
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ActiveKeys returns the number of tracked per-key buckets.
func (l *Limiter) ActiveKeys() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Stop terminates the background cleanup goroutine.
func (l *Limiter) Stop() {
	close(l.stop)
}

// cleanupLoop runs every minute and removes buckets that have been idle for
// longer than CleanupAge, preventing unbounded memory growth.
func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-l.cfg.CleanupAge)
			l.mu.Lock()
			for k, b := range l.buckets {
				if b.lastSeen.Before(cutoff) {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}
}
