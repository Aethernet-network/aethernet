package assurance

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Stub validatorCounter
// ---------------------------------------------------------------------------

type stubCounter struct{ n int }

func (s *stubCounter) ActiveEligibleCount() int { return s.n }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// bootstrapCfg returns an AssuranceConfig with BootstrapDurationDays=90,
// BootstrapMinValidators=20, and representative rate/reward values.
func bootstrapCfg() *config.AssuranceConfig {
	cfg := config.DefaultConfig()
	return &cfg.Assurance
}

var normalRates = BootstrapRates{
	BaselineReplay:   0.20,
	GenerationReplay: 0.35,
	NewAgentReplay:   0.50,
}

// newOverride creates a BootstrapOverride with a configurable launch time and
// validator count.
func newOverride(launchTime time.Time, validatorCount int) *BootstrapOverride {
	return NewBootstrapOverride(bootstrapCfg(), &stubCounter{n: validatorCount}, launchTime, normalRates)
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_IsActive_BothConditionsNotMet
// ---------------------------------------------------------------------------

func TestBootstrapOverride_IsActive_BothConditionsNotMet(t *testing.T) {
	// Launch was recent (< 90 days ago) and validators < 20.
	bo := newOverride(time.Now().Add(-time.Hour), 5)
	if !bo.IsBootstrapActive() {
		t.Error("expected bootstrap active when neither duration nor validators threshold met")
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_IsActive_TimeNotMet
// ---------------------------------------------------------------------------

func TestBootstrapOverride_IsActive_TimeNotMet(t *testing.T) {
	// Enough validators but duration not elapsed.
	bo := newOverride(time.Now().Add(-time.Hour), 25)
	if !bo.IsBootstrapActive() {
		t.Error("expected bootstrap active when duration threshold not met")
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_IsActive_ValidatorsNotMet
// ---------------------------------------------------------------------------

func TestBootstrapOverride_IsActive_ValidatorsNotMet(t *testing.T) {
	// Duration elapsed but not enough validators.
	oldLaunch := time.Now().Add(-91 * 24 * time.Hour)
	bo := newOverride(oldLaunch, 5)
	if !bo.IsBootstrapActive() {
		t.Error("expected bootstrap active when validator count threshold not met")
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_IsActive_BothConditionsMet
// ---------------------------------------------------------------------------

func TestBootstrapOverride_IsActive_BothConditionsMet(t *testing.T) {
	// Both: duration elapsed AND validators >= minimum.
	oldLaunch := time.Now().Add(-91 * 24 * time.Hour)
	bo := newOverride(oldLaunch, 25)
	if bo.IsBootstrapActive() {
		t.Error("expected bootstrap inactive when both conditions are satisfied")
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_EffectiveRates_Active
// ---------------------------------------------------------------------------

func TestBootstrapOverride_EffectiveRates_Active(t *testing.T) {
	bo := newOverride(time.Now().Add(-time.Hour), 5) // active
	rates := bo.EffectiveReplayRates()
	cfg := bootstrapCfg()
	if rates.BaselineReplay != cfg.BootstrapBaselineReplay {
		t.Errorf("BaselineReplay: got %v, want %v", rates.BaselineReplay, cfg.BootstrapBaselineReplay)
	}
	if rates.GenerationReplay != cfg.BootstrapGenerationReplay {
		t.Errorf("GenerationReplay: got %v, want %v", rates.GenerationReplay, cfg.BootstrapGenerationReplay)
	}
	if rates.NewAgentReplay != cfg.BootstrapNewAgentReplay {
		t.Errorf("NewAgentReplay: got %v, want %v", rates.NewAgentReplay, cfg.BootstrapNewAgentReplay)
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_EffectiveRates_Inactive
// ---------------------------------------------------------------------------

func TestBootstrapOverride_EffectiveRates_Inactive(t *testing.T) {
	oldLaunch := time.Now().Add(-91 * 24 * time.Hour)
	bo := newOverride(oldLaunch, 25) // inactive
	rates := bo.EffectiveReplayRates()
	if rates.BaselineReplay != normalRates.BaselineReplay {
		t.Errorf("BaselineReplay: got %v, want %v (normal)", rates.BaselineReplay, normalRates.BaselineReplay)
	}
	if rates.GenerationReplay != normalRates.GenerationReplay {
		t.Errorf("GenerationReplay: got %v, want %v (normal)", rates.GenerationReplay, normalRates.GenerationReplay)
	}
	if rates.NewAgentReplay != normalRates.NewAgentReplay {
		t.Errorf("NewAgentReplay: got %v, want %v (normal)", rates.NewAgentReplay, normalRates.NewAgentReplay)
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_ComputeReward_ZeroVolume
// ---------------------------------------------------------------------------

func TestBootstrapOverride_ComputeReward_ZeroVolume(t *testing.T) {
	bo := newOverride(time.Now(), 0)
	// month 0, zero volume → full base reward
	reward := bo.ComputeBootstrapReward(0, 0)
	if reward != bootstrapCfg().BootstrapBaseReward {
		t.Errorf("ComputeBootstrapReward(0, 0): got %d, want %d", reward, bootstrapCfg().BootstrapBaseReward)
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_ComputeReward_HalfVolume
// ---------------------------------------------------------------------------

func TestBootstrapOverride_ComputeReward_HalfVolume(t *testing.T) {
	bo := newOverride(time.Now(), 0)
	cfg := bootstrapCfg()
	halfVolume := cfg.BootstrapTargetMonthlyVolume / 2
	reward := bo.ComputeBootstrapReward(0, halfVolume)
	// Linear decay: ~50% of base reward (allow ±1 µAET for integer rounding).
	wantApprox := cfg.BootstrapBaseReward / 2
	if reward < wantApprox-1 || reward > wantApprox+1 {
		t.Errorf("ComputeBootstrapReward at half volume: got %d, want ~%d", reward, wantApprox)
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_ComputeReward_AtTarget
// ---------------------------------------------------------------------------

func TestBootstrapOverride_ComputeReward_AtTarget(t *testing.T) {
	bo := newOverride(time.Now(), 0)
	cfg := bootstrapCfg()
	reward := bo.ComputeBootstrapReward(0, cfg.BootstrapTargetMonthlyVolume)
	if reward != 0 {
		t.Errorf("ComputeBootstrapReward at target volume: got %d, want 0", reward)
	}
}

// ---------------------------------------------------------------------------
// TestBootstrapOverride_ComputeReward_PastSunset
// ---------------------------------------------------------------------------

func TestBootstrapOverride_ComputeReward_PastSunset(t *testing.T) {
	bo := newOverride(time.Now(), 0)
	cfg := bootstrapCfg()
	// Past sunset months → zero regardless of volume.
	reward := bo.ComputeBootstrapReward(cfg.BootstrapSunsetMonths, 0)
	if reward != 0 {
		t.Errorf("ComputeBootstrapReward past sunset: got %d, want 0", reward)
	}
}
