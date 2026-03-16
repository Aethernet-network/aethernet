package replay

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Bootstrap rate source stub
// ---------------------------------------------------------------------------

// stubBootstrapSource is a minimal bootstrapRateSource for testing.
type stubBootstrapSource struct {
	active   bool
	baseline float64
	gen      float64
	newAgent float64
}

func (s *stubBootstrapSource) IsBootstrapActive() bool { return s.active }
func (s *stubBootstrapSource) EffectiveRates() (float64, float64, float64) {
	return s.baseline, s.gen, s.newAgent
}

// ---------------------------------------------------------------------------
// TestShouldReplay_BootstrapRates_Active
// ---------------------------------------------------------------------------

// TestShouldReplay_BootstrapRates_NewAgent_Active verifies that when a
// bootstrapRateSource is wired and active, the elevated newAgent rate is used
// for agents with fewer than 10 tasks.
func TestShouldReplay_BootstrapRates_NewAgent_Active(t *testing.T) {
	// Use a 0-rate policy so only the bootstrap override controls sampling.
	policy := ReplayPolicy{
		SampleRate:             0.0,
		NewAgentSampleRate:     0.0,
		GenerationSampleRate:   0.0,
		AlwaysReplayChallenged: false,
		AlwaysReplayAnomalies:  false,
		LowConfidenceThreshold: 0.0,
	}
	coord := NewReplayCoordinator(policy, newMemStore())
	// Bootstrap active: newAgent rate = 1.0 (always triggers).
	coord.SetBootstrapRateSource(&stubBootstrapSource{
		active:   true,
		baseline: 1.0,
		gen:      1.0,
		newAgent: 1.0,
	})

	ok, reason := coord.ShouldReplay("task-b1", "agent-1", "code", 0.90, false, false, nil, 5)
	if !ok {
		t.Error("ShouldReplay: expected true when bootstrap active with newAgent=1.0 and agentTaskCount=5")
	}
	if reason != "probation" {
		t.Errorf("reason: want %q, got %q", "probation", reason)
	}
}

// TestShouldReplay_BootstrapRates_Inactive_UsesNormalRates verifies that
// when the bootstrap source reports the phase as inactive, the normal (lower)
// rates are used and a zero-rate policy means no sampling.
func TestShouldReplay_BootstrapRates_Inactive_UsesNormalRates(t *testing.T) {
	// Elevated policy rates — these should NOT be used (bootstrap source is wired).
	policy := ReplayPolicy{
		SampleRate:             1.0,
		NewAgentSampleRate:     1.0,
		GenerationSampleRate:   1.0,
		AlwaysReplayChallenged: false,
		AlwaysReplayAnomalies:  false,
		LowConfidenceThreshold: 0.0,
	}
	coord := NewReplayCoordinator(policy, newMemStore())
	// Bootstrap inactive: source returns 0-rate "normal" values.
	coord.SetBootstrapRateSource(&stubBootstrapSource{
		active:   false,
		baseline: 0.0,
		gen:      0.0,
		newAgent: 0.0,
	})

	// agentTaskCount=5 (< 10) — normally triggers probation, but normal rate = 0.
	ok, _ := coord.ShouldReplay("task-b2", "agent-2", "code", 0.90, false, false, nil, 5)
	if ok {
		t.Error("ShouldReplay: expected false when bootstrap inactive and all normal rates = 0")
	}
}

// TestShouldReplay_BootstrapRates_NilSource_FallsBackToPolicy verifies that
// when no bootstrap source is wired, the static ReplayPolicy values govern —
// backward compatible.
func TestShouldReplay_BootstrapRates_NilSource_FallsBackToPolicy(t *testing.T) {
	policy := ReplayPolicy{
		SampleRate:             0.0,
		NewAgentSampleRate:     1.0, // always triggers for new agents
		GenerationSampleRate:   0.0,
		AlwaysReplayChallenged: false,
		AlwaysReplayAnomalies:  false,
		LowConfidenceThreshold: 0.0,
	}
	coord := NewReplayCoordinator(policy, newMemStore())
	// No SetBootstrapRateSource call.

	ok, reason := coord.ShouldReplay("task-b3", "agent-3", "code", 0.90, false, false, nil, 5)
	if !ok {
		t.Error("ShouldReplay: expected true for new agent when NewAgentSampleRate=1.0 (no bootstrap source)")
	}
	if reason != "probation" {
		t.Errorf("reason: want %q, got %q", "probation", reason)
	}
}
