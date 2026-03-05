package staking_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/staking"
)

// timeParams used for tests that need all time gates satisfied.
// stakedSince=1 (epoch start), now=200 days → daysSinceStake≈199.
const (
	longAgo = int64(1)           // stakedSince: very early, all time gates met
	nowTS   = int64(86400 * 200) // 200 days later
)

// ---------------------------------------------------------------------------
// TrustMultiplier
// ---------------------------------------------------------------------------

// TestTrustMultiplier verifies the task-based multiplier at every tier boundary
// when the time gate is fully satisfied (200 days staked).
func TestTrustMultiplier(t *testing.T) {
	cases := []struct {
		tasks uint64
		want  uint64
	}{
		{0, 1},    // no tasks: 1×
		{24, 1},   // just below 25: still 1×
		{25, 2},   // 25 tasks: 2×
		{49, 2},
		{50, 3},   // 50 tasks: 3×
		{74, 3},
		{75, 4},   // 75 tasks: 4×
		{99, 4},
		{100, 5},  // 100 tasks: 5×
		{200, 5},  // well beyond cap
		{1000, 5},
	}
	for _, tc := range cases {
		got := staking.TrustMultiplier(tc.tasks, longAgo, nowTS)
		if got != tc.want {
			t.Errorf("TrustMultiplier(%d) = %d, want %d", tc.tasks, got, tc.want)
		}
	}
}

// TestTrustMultiplier_TimeGated verifies that meeting the task count alone is
// insufficient — without enough days staked the multiplier stays at 1×.
func TestTrustMultiplier_TimeGated(t *testing.T) {
	// 100 tasks completed, but only 15 days since first stake.
	// Level 2 requires 30 days → should still be 1×.
	const tasks = uint64(100)
	const stakedSince = int64(1)
	const now = int64(86400 * 15) // 15 days

	got := staking.TrustMultiplier(tasks, stakedSince, now)
	if got != 1 {
		t.Errorf("TrustMultiplier with 100 tasks but 15 days = %d, want 1", got)
	}
}

// TestTrustMultiplier_BothRequired verifies that meeting the time threshold
// alone is insufficient — without enough tasks the multiplier stays at 1×.
func TestTrustMultiplier_BothRequired(t *testing.T) {
	// 120 days staked but only 10 tasks completed.
	// Level 2 requires 25 tasks → should still be 1×.
	const tasks = uint64(10)
	const stakedSince = int64(1)
	const now = int64(86400 * 120)

	got := staking.TrustMultiplier(tasks, stakedSince, now)
	if got != 1 {
		t.Errorf("TrustMultiplier with 10 tasks and 120 days = %d, want 1", got)
	}
}

// TestTrustMultiplier_Progressive verifies that each level unlocks at exactly
// the right combination of tasks + days.
func TestTrustMultiplier_Progressive(t *testing.T) {
	cases := []struct {
		tasks uint64
		days  int64
		want  uint64
	}{
		// Level 2: exactly 25 tasks + 30 days.
		{25, 30, 2},
		// One task short of level 2.
		{24, 30, 1},
		// One day short of level 2.
		{25, 29, 1},
		// Level 3: exactly 50 tasks + 60 days.
		{50, 60, 3},
		// Level 4: exactly 75 tasks + 90 days.
		{75, 90, 4},
		// Level 5: exactly 100 tasks + 120 days.
		{100, 120, 5},
		// Level 5 boundary — one day short means level 4.
		{100, 119, 4},
	}
	const base = int64(1)
	for _, tc := range cases {
		now := base + tc.days*86400
		got := staking.TrustMultiplier(tc.tasks, base, now)
		if got != tc.want {
			t.Errorf("TrustMultiplier(tasks=%d, days=%d) = %d, want %d",
				tc.tasks, tc.days, got, tc.want)
		}
	}
}

// TestTrustMultiplier_ZeroStakedSince verifies that an unset staking timestamp
// (zero) always yields 1× regardless of task count.
func TestTrustMultiplier_ZeroStakedSince(t *testing.T) {
	for _, tasks := range []uint64{0, 25, 100, 1000} {
		got := staking.TrustMultiplier(tasks, 0, nowTS)
		if got != 1 {
			t.Errorf("TrustMultiplier(%d, stakedSince=0) = %d, want 1", tasks, got)
		}
	}
}

// ---------------------------------------------------------------------------
// TrustLimit
// ---------------------------------------------------------------------------

// TestTrustLimit verifies that TrustLimit = stake × multiplier.
func TestTrustLimit(t *testing.T) {
	cases := []struct {
		stake uint64
		tasks uint64
		want  uint64
	}{
		{10_000, 0, 10_000},    // 10 000 × 1
		{10_000, 25, 20_000},   // 10 000 × 2
		{10_000, 50, 30_000},   // 10 000 × 3
		{10_000, 75, 40_000},   // 10 000 × 4
		{10_000, 100, 50_000},  // 10 000 × 5
		{0, 100, 0},            // zero stake → zero limit
	}
	for _, tc := range cases {
		got := staking.TrustLimit(tc.stake, tc.tasks, longAgo, nowTS)
		if got != tc.want {
			t.Errorf("TrustLimit(%d, %d) = %d, want %d", tc.stake, tc.tasks, got, tc.want)
		}
	}
}

// TestTrustLimit_OverflowSafe verifies that the function does not panic when
// stake × multiplier would overflow uint64.
func TestTrustLimit_OverflowSafe(t *testing.T) {
	maxU64 := ^uint64(0)
	result := staking.TrustLimit(maxU64, 100, longAgo, nowTS) // would overflow 5×
	if result == 0 {
		t.Errorf("TrustLimit overflow: got 0, want max uint64")
	}
	// Must not panic — the test reaching here is the main assertion.
}

// ---------------------------------------------------------------------------
// EffectiveTasks / decay
// ---------------------------------------------------------------------------

// TestEffectiveTasks_NoDecay verifies that a recently active agent has no decay.
func TestEffectiveTasks_NoDecay(t *testing.T) {
	const tasks = uint64(100)
	lastActive := int64(86400 * 10) // 10 days ago
	now := int64(86400 * 25)        // 15 days of inactivity — less than 30

	got := staking.EffectiveTasks(tasks, lastActive, now)
	if got != tasks {
		t.Errorf("EffectiveTasks: want %d (no decay), got %d", tasks, got)
	}
}

// TestEffectiveTasks_OneDecay verifies a 25-task reduction after 31 inactive days.
func TestEffectiveTasks_OneDecay(t *testing.T) {
	const tasks = uint64(100)
	lastActive := int64(0)           // epoch
	now := int64(86400 * 31)         // 31 days inactive = 1 decay period

	got := staking.EffectiveTasks(tasks, lastActive+1, now)
	want := tasks - 25
	if got != want {
		t.Errorf("EffectiveTasks after 31 days: want %d, got %d", want, got)
	}
}

// TestEffectiveTasks_FullDecay verifies that effective tasks floor at zero.
func TestEffectiveTasks_FullDecay(t *testing.T) {
	const tasks = uint64(50)
	lastActive := int64(1)
	now := int64(86400 * 121) // 121 days inactive = 4 periods × 25 = 100 reduction > 50

	got := staking.EffectiveTasks(tasks, lastActive, now)
	if got != 0 {
		t.Errorf("EffectiveTasks full decay: want 0, got %d", got)
	}
}

// TestEffectiveTasks_ZeroLastActivity verifies that a zero last-activity timestamp
// returns the full task count (never recorded = no decay applied).
func TestEffectiveTasks_ZeroLastActivity(t *testing.T) {
	const tasks = uint64(100)
	got := staking.EffectiveTasks(tasks, 0, nowTS)
	if got != tasks {
		t.Errorf("EffectiveTasks(lastActivity=0): want %d, got %d", tasks, got)
	}
}

// ---------------------------------------------------------------------------
// StakeManager: Stake / Unstake / StakedSince / RecordActivity
// ---------------------------------------------------------------------------

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

// TestStakeManager_StakedSince verifies that the first Stake call records the
// staked-since timestamp and subsequent calls do not overwrite it.
func TestStakeManager_StakedSince(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-since")

	if ts := sm.StakedSince(id); ts != 0 {
		t.Fatalf("StakedSince before Stake: want 0, got %d", ts)
	}

	sm.Stake(id, 100)
	ts1 := sm.StakedSince(id)
	if ts1 == 0 {
		t.Fatal("StakedSince after first Stake: want non-zero, got 0")
	}

	// Second Stake must not overwrite the timestamp.
	sm.Stake(id, 50)
	ts2 := sm.StakedSince(id)
	if ts1 != ts2 {
		t.Errorf("StakedSince changed after second Stake: got %d, want %d", ts2, ts1)
	}
}

// ---------------------------------------------------------------------------
// Slash
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// SlashDefault
// ---------------------------------------------------------------------------

// TestSlashDefault_FullAmount verifies that SlashDefault returns the entire
// staked amount and leaves balance at zero.
func TestSlashDefault_FullAmount(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-e")

	sm.Stake(id, 10_000)
	slashed := sm.SlashDefault(id)
	if slashed != 10_000 {
		t.Errorf("SlashDefault returned %d, want 10000", slashed)
	}
	if got := sm.StakedAmount(id); got != 0 {
		t.Errorf("after SlashDefault: stake = %d, want 0", got)
	}
}

// TestSlashDefault_ResetsStakedSince verifies that SlashDefault clears the
// staking timestamp, forcing the agent to restart trust accumulation.
func TestSlashDefault_ResetsStakedSince(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-f")

	sm.Stake(id, 1_000)
	if ts := sm.StakedSince(id); ts == 0 {
		t.Fatal("StakedSince should be non-zero after Stake")
	}

	sm.SlashDefault(id)
	if ts := sm.StakedSince(id); ts != 0 {
		t.Errorf("StakedSince after SlashDefault = %d, want 0 (reset)", ts)
	}
}

// TestSlashDefault_MustRestake verifies that after SlashDefault the agent's
// trust limit is zero until they stake again.
func TestSlashDefault_MustRestake(t *testing.T) {
	sm := staking.NewStakeManager()
	id := crypto.AgentID("agent-g")

	sm.Stake(id, 10_000)
	sm.SlashDefault(id)

	staked := sm.StakedAmount(id)
	since := sm.StakedSince(id)
	limit := staking.TrustLimit(staked, 100, since, nowTS)
	if limit != 0 {
		t.Errorf("trust limit after SlashDefault = %d, want 0", limit)
	}
}
