package ratelimit_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/ratelimit"
)

// TestAllow_UnderLimit verifies that requests within the burst limit are all
// allowed without any refill delay.
func TestAllow_UnderLimit(t *testing.T) {
	cfg := ratelimit.Config{Rate: 10, Burst: 5, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	for i := range 5 {
		if !l.Allow("ip1") {
			t.Fatalf("request %d should be allowed (within burst)", i+1)
		}
	}
}

// TestAllow_OverLimit verifies that burst+1 requests in rapid succession causes
// the last request to be denied.
func TestAllow_OverLimit(t *testing.T) {
	cfg := ratelimit.Config{Rate: 1, Burst: 3, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	allowed := 0
	for range 4 {
		if l.Allow("ip2") {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("expected 3 allowed (burst), got %d", allowed)
	}
}

// TestAllow_Refill verifies that tokens refill over time, allowing requests
// that would otherwise be denied immediately after exhausting the bucket.
func TestAllow_Refill(t *testing.T) {
	// 100 tokens/sec so we get a predictable refill in a short sleep.
	cfg := ratelimit.Config{Rate: 100, Burst: 1, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	// Exhaust bucket.
	if !l.Allow("ip3") {
		t.Fatal("first request should be allowed")
	}
	if l.Allow("ip3") {
		t.Fatal("second immediate request should be denied")
	}

	// Wait long enough for at least one token to refill.
	time.Sleep(20 * time.Millisecond)

	if !l.Allow("ip3") {
		t.Fatal("request after refill should be allowed")
	}
}

// TestAllow_DifferentKeys verifies that per-key buckets are independent: one
// key exhausting its budget does not affect another key.
func TestAllow_DifferentKeys(t *testing.T) {
	cfg := ratelimit.Config{Rate: 1, Burst: 1, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	// Exhaust key A.
	if !l.Allow("keyA") {
		t.Fatal("first request for keyA should be allowed")
	}
	if l.Allow("keyA") {
		t.Fatal("second request for keyA should be denied")
	}

	// Key B should be completely independent.
	if !l.Allow("keyB") {
		t.Fatal("keyB should be allowed regardless of keyA state")
	}
}

// TestActiveKeys verifies that ActiveKeys tracks the number of distinct keys
// that have made at least one request.
func TestActiveKeys(t *testing.T) {
	cfg := ratelimit.Config{Rate: 10, Burst: 10, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	if n := l.ActiveKeys(); n != 0 {
		t.Fatalf("expected 0 active keys before any requests, got %d", n)
	}

	l.Allow("alpha")
	l.Allow("beta")
	l.Allow("gamma")
	l.Allow("alpha") // duplicate — should not add a new bucket

	if n := l.ActiveKeys(); n != 3 {
		t.Fatalf("expected 3 active keys, got %d", n)
	}
}
