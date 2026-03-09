package ratelimit

import (
	"net"
	"net/http"
	"strings"
)

// privateCIDRs is the set of IP ranges considered private/trusted for
// proxy-forwarded-for headers. X-Forwarded-For and X-Real-IP are only
// trusted when the direct peer (RemoteAddr) is within one of these ranges
// (MEDIUM-7.4: prevent rate-limit bypass via spoofed XFF headers).
var privateCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		if _, n, err := net.ParseCIDR(s); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// isTrustedProxy reports whether remoteIP is within a private/trusted range.
func isTrustedProxy(remoteIP string) bool {
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return false
	}
	for _, cidr := range privateCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

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
//
// X-Forwarded-For and X-Real-IP are only trusted when the direct peer
// (RemoteAddr) is within a private/trusted CIDR range. Direct clients can
// inject arbitrary XFF values, so trusting them unconditionally would allow
// rate-limit bypass (MEDIUM-7.4).
//
// Falls back to the host portion of RemoteAddr (port stripped). Returns the
// raw RemoteAddr string only when net.SplitHostPort fails (e.g. Unix sockets).
func ExtractIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	// Only trust forwarding headers from known private-network proxies.
	if isTrustedProxy(host) {
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
	}
	return host
}
