package ratelimit

import (
	"net"
	"net/http"
	"strings"
)

// Middleware wraps next with rate-limit enforcement using limiter.
// Requests that exceed the rate limit receive HTTP 429 Too Many Requests with
// a Retry-After: 1 header and a JSON error body. The client IP is extracted
// via ExtractIP and used as the rate-limit key.
func Middleware(limiter *Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ExtractIP(r)
		if !limiter.Allow(key) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ExtractIP returns the best-effort client IP address from r.
// It checks headers in order: X-Forwarded-For (first entry), X-Real-IP, then
// the host portion of RemoteAddr (port stripped). Returns the raw RemoteAddr
// string only when net.SplitHostPort fails (e.g. Unix sockets in tests).
func ExtractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the leftmost (original client) address.
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
