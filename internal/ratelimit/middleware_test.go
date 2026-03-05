package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethernet/core/internal/ratelimit"
)

// okHandler is a trivial handler that always returns 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// TestMiddleware_Allowed verifies that a request within the rate limit passes
// through to the next handler with a 200 response.
func TestMiddleware_Allowed(t *testing.T) {
	cfg := ratelimit.Config{Rate: 10, Burst: 10, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	h := ratelimit.Middleware(l, okHandler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:9999"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// TestMiddleware_Blocked verifies that a request exceeding the rate limit is
// rejected with HTTP 429.
func TestMiddleware_Blocked(t *testing.T) {
	cfg := ratelimit.Config{Rate: 1, Burst: 1, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	h := ratelimit.Middleware(l, okHandler)

	// First request consumes the only token.
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "10.0.0.2:1234"
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rec1.Code)
	}

	// Second request should be rate-limited.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.2:1235"
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rec2.Code)
	}
}

// TestMiddleware_RetryAfter verifies that a blocked response includes the
// Retry-After header set to "1".
func TestMiddleware_RetryAfter(t *testing.T) {
	cfg := ratelimit.Config{Rate: 1, Burst: 1, CleanupAge: time.Minute}
	l := ratelimit.New(cfg)
	defer l.Stop()

	h := ratelimit.Middleware(l, okHandler)

	// Exhaust the bucket.
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "10.0.0.3:9000"
	h.ServeHTTP(httptest.NewRecorder(), req1)

	// Rate-limited request — check Retry-After.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.3:9001"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req2)

	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After: 1, got %q", got)
	}
}

// TestExtractIP_XForwardedFor verifies that ExtractIP returns the first (leftmost)
// IP from a multi-value X-Forwarded-For header.
func TestExtractIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1, 172.16.0.1")

	got := ratelimit.ExtractIP(req)
	want := "1.2.3.4"
	if got != want {
		t.Fatalf("ExtractIP = %q, want %q", got, want)
	}
}

// TestExtractIP_RemoteAddr verifies that ExtractIP falls back to the host
// portion of RemoteAddr when no forwarding headers are present.
func TestExtractIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.100:54321"

	got := ratelimit.ExtractIP(req)
	want := "192.168.1.100"
	if got != want {
		t.Fatalf("ExtractIP = %q, want %q", got, want)
	}
}
