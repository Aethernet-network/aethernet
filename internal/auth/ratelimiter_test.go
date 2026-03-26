package auth_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/auth"
)

func TestEndpointRateLimiter_PerAgent(t *testing.T) {
	limits := map[string]auth.EndpointLimit{
		"/v1/tasks": {PerAgentPerHr: 3, PerIPPerHr: 100, GlobalPerHr: 1000},
	}
	rl := auth.NewEndpointRateLimiter(limits)

	for i := 0; i < 3; i++ {
		ok, _ := rl.Allow("/v1/tasks", "agent-1", "1.2.3.4")
		if !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	ok, retry := rl.Allow("/v1/tasks", "agent-1", "1.2.3.4")
	if ok {
		t.Error("4th request should be rejected (per-agent limit)")
	}
	if retry <= 0 {
		t.Error("retry-after should be positive")
	}

	// Different agent should still be allowed.
	ok, _ = rl.Allow("/v1/tasks", "agent-2", "1.2.3.4")
	if !ok {
		t.Error("different agent should be allowed")
	}
}

func TestEndpointRateLimiter_PerIP(t *testing.T) {
	limits := map[string]auth.EndpointLimit{
		"/v1/agents": {PerAgentPerHr: 0, PerIPPerHr: 2, GlobalPerHr: 100},
	}
	rl := auth.NewEndpointRateLimiter(limits)

	rl.Allow("/v1/agents", "", "10.0.0.1")
	rl.Allow("/v1/agents", "", "10.0.0.1")

	ok, _ := rl.Allow("/v1/agents", "", "10.0.0.1")
	if ok {
		t.Error("3rd request from same IP should be rejected")
	}

	// Different IP should be allowed.
	ok, _ = rl.Allow("/v1/agents", "", "10.0.0.2")
	if !ok {
		t.Error("different IP should be allowed")
	}
}

func TestEndpointRateLimiter_Global(t *testing.T) {
	limits := map[string]auth.EndpointLimit{
		"/v1/faucet": {PerAgentPerHr: 100, PerIPPerHr: 100, GlobalPerHr: 3},
	}
	rl := auth.NewEndpointRateLimiter(limits)

	rl.Allow("/v1/faucet", "a1", "1.1.1.1")
	rl.Allow("/v1/faucet", "a2", "2.2.2.2")
	rl.Allow("/v1/faucet", "a3", "3.3.3.3")

	ok, _ := rl.Allow("/v1/faucet", "a4", "4.4.4.4")
	if ok {
		t.Error("4th request should be rejected by global limit")
	}
}

func TestEndpointRateLimiter_NoLimit(t *testing.T) {
	rl := auth.NewEndpointRateLimiter(map[string]auth.EndpointLimit{})

	ok, _ := rl.Allow("/v1/unknown", "agent", "1.1.1.1")
	if !ok {
		t.Error("endpoint with no limit should always be allowed")
	}
}

func TestEndpointRateLimiter_Remaining(t *testing.T) {
	limits := map[string]auth.EndpointLimit{
		"/v1/tasks": {PerAgentPerHr: 5, PerIPPerHr: 100, GlobalPerHr: 1000},
	}
	rl := auth.NewEndpointRateLimiter(limits)

	rem := rl.Remaining("/v1/tasks", "agent-1")
	if rem != 5 {
		t.Errorf("remaining = %d; want 5", rem)
	}

	rl.Allow("/v1/tasks", "agent-1", "1.1.1.1")
	rl.Allow("/v1/tasks", "agent-1", "1.1.1.1")

	rem = rl.Remaining("/v1/tasks", "agent-1")
	if rem != 3 {
		t.Errorf("remaining = %d; want 3", rem)
	}
}
