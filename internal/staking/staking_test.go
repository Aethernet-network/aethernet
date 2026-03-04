package staking_test

import (
	"testing"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/staking"
)

// TestTrustMultiplier verifies the task-based multiplier at every tier boundary.
func TestTrustMultiplier(t *testing.T) {
	cases := []struct {
		tasks uint64
		want  uint64
	}{
		{0, 1},   // no tasks: 1×
		{24, 1},  // just below 25: still 1×
		{25, 2},  // 25 tasks: 2×
		{49, 2},
		{50, 3},  // 50 tasks: 3×
		{74, 3},
		{75, 4},  // 75 tasks: 4×
		{99, 4},
		{100, 5}, // 100 tasks: capped at 5×
		{200, 5}, // well beyond cap
		{1000, 5},
	}
	for _, tc := range cases {
		got := staking.TrustMultiplier(tc.tasks)
		if got != tc.want {
			t.Errorf("TrustMultiplier(%d) = %d, want %d", tc.tasks, got, tc.want)
		}
	}
}

// TestTrustLimit verifies that TrustLimit = stake × multiplier.
func TestTrustLimit(t *testing.T) {
	cases := []struct {
		stake uint64
		tasks uint64
		want  uint64
	}{
		{10_000, 0, 10_000},   // 10 000 × 1
		{10_000, 25, 20_000},  // 10 000 × 2
		{10_000, 50, 30_000},  // 10 000 × 3
		{10_000, 75, 40_000},  // 10 000 × 4
		{10_000, 100, 50_000}, // 10 000 × 5
		{0, 100, 0},           // zero stake → zero limit
	}
	for _, tc := range cases {
		got := staking.TrustLimit(tc.stake, tc.tasks)
		if got != tc.want {
			t.Errorf("TrustLimit(%d, %d) = %d, want %d", tc.stake, tc.tasks, got, tc.want)
		}
	}
}

// TestTrustLimit_OverflowSafe verifies that the function does not panic when
// stake × multiplier would overflow uint64.
func TestTrustLimit_OverflowSafe(t *testing.T) {
	maxU64 := ^uint64(0)
	// Any stake that would overflow with multiplier > 1.
	result := staking.TrustLimit(maxU64, 100) // would overflow 5×
	if result == 0 {
		t.Errorf("TrustLimit overflow: got 0, want max uint64")
	}
	// Must not panic — the test reaching here is the main assertion.
}

// TestStakeUnstake verifies that stake and unstake update the balance correctly.
func TestStakeUnstake(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-a")

	sm.Stake(id, 100)
	if got := sm.StakedAmount(id); got != 100 {
		t.Fatalf("after Stake(100): got %d, want 100", got)
	}

	if ok := sm.Unstake(id, 50); !ok {
		t.Fatal("Unstake(50) returned false, want true")
	}
	if got := sm.StakedAmount(id); got != 50 {
		t.Errorf("after Unstake(50): got %d, want 50", got)
	}
}

// TestUnstake_Insufficient verifies that Unstake returns false and leaves
// the balance unchanged when the requested amount exceeds the staked amount.
func TestUnstake_Insufficient(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-b")

	sm.Stake(id, 10)
	if ok := sm.Unstake(id, 100); ok {
		t.Error("Unstake(100) with only 10 staked returned true, want false")
	}
	if got := sm.StakedAmount(id); got != 10 {
		t.Errorf("balance after failed unstake = %d, want 10", got)
	}
}

// TestSlash verifies that slashing reduces stake by the given percentage and
// returns the slashed amount.
func TestSlash(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-c")

	sm.Stake(id, 10_000)
	slashed := sm.Slash(id, 10) // 10% of 10 000 = 1 000
	if slashed != 1_000 {
		t.Errorf("Slash(10%%) returned %d, want 1000", slashed)
	}
	if got := sm.StakedAmount(id); got != 9_000 {
		t.Errorf("after 10%% slash: stake = %d, want 9000", got)
	}
}

// TestSlash_Capped verifies that a percentage > 100 is treated as 100 (full slash).
func TestSlash_Capped(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-d")

	sm.Stake(id, 5_000)
	slashed := sm.Slash(id, 150) // capped to 100%
	if slashed != 5_000 {
		t.Errorf("Slash(150%%) returned %d, want 5000 (full slash)", slashed)
	}
	if got := sm.StakedAmount(id); got != 0 {
		t.Errorf("after full slash: stake = %d, want 0", got)
	}
}
