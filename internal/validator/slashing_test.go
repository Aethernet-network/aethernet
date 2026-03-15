package validator

import (
	"errors"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// slashCfg returns a ValidatorConfig pre-populated with the Part 5 defaults
// and with cap enforcement disabled so slash tests are not affected by
// assignment-engine behaviour.
func slashCfg() *config.ValidatorConfig {
	cfg := config.DefaultConfig()
	cfg.Validator.CapEnforcementMinValidators = 100 // disable for slash tests
	return &cfg.Validator
}

// newActiveValidator registers a fully active (genesis) validator with 100 AET
// stake (100_000_000_000 µAET) and returns a (registry, validator) pair.
func newActiveValidator(t *testing.T) (*ValidatorRegistry, *Validator) {
	t.Helper()
	reg := NewValidatorRegistry(slashCfg(), nil)
	v, err := reg.Register("agent-slash-"+t.Name(), 100_000_000_000, []string{"code"}, true)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg, v
}

// newSlashEngine creates a SlashEngine against a freshly registered active validator.
func newSlashEngine(t *testing.T) (*SlashEngine, *ValidatorRegistry, *Validator) {
	t.Helper()
	reg, v := newActiveValidator(t)
	eng := NewSlashEngine(reg, slashCfg())
	return eng, reg, v
}

// ---------------------------------------------------------------------------
// TestSlashEngine_FraudulentApproval
// ---------------------------------------------------------------------------

func TestSlashEngine_FraudulentApproval(t *testing.T) {
	eng, _, v := newSlashEngine(t)

	res, err := eng.Slash(v.ID, OffenseFraudulentApproval)
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if res.Offense != OffenseFraudulentApproval {
		t.Errorf("Offense: got %q, want %q", res.Offense, OffenseFraudulentApproval)
	}
	if res.SlashPercentage != 0.30 {
		t.Errorf("SlashPercentage: got %v, want 0.30", res.SlashPercentage)
	}
	want := uint64(0.30 * 100_000_000_000)
	if res.SlashAmount != want {
		t.Errorf("SlashAmount: got %d, want %d", res.SlashAmount, want)
	}
	if res.CooldownDays != 30 {
		t.Errorf("CooldownDays: got %d, want 30", res.CooldownDays)
	}
	if res.PermanentExclusion {
		t.Error("PermanentExclusion should be false for FraudulentApproval")
	}
	// Challenger gets 50 %, reserve gets the rest.
	if res.ChallengerShare != want/2 {
		t.Errorf("ChallengerShare: got %d, want %d", res.ChallengerShare, want/2)
	}
	if res.ReserveShare != want-want/2 {
		t.Errorf("ReserveShare: got %d, want %d", res.ReserveShare, want-want/2)
	}
	// Validator should now be suspended.
	got, err := eng.registry.Get(v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusSuspended {
		t.Errorf("Status: got %q, want suspended", got.Status)
	}
	if got.SlashCount != 1 {
		t.Errorf("SlashCount: got %d, want 1", got.SlashCount)
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_DishonestReplay
// ---------------------------------------------------------------------------

func TestSlashEngine_DishonestReplay(t *testing.T) {
	eng, _, v := newSlashEngine(t)

	res, err := eng.Slash(v.ID, OffenseDishonestReplay)
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if res.SlashPercentage != 0.40 {
		t.Errorf("SlashPercentage: got %v, want 0.40", res.SlashPercentage)
	}
	if res.CooldownDays != 60 {
		t.Errorf("CooldownDays: got %d, want 60", res.CooldownDays)
	}
	if res.PermanentExclusion {
		t.Error("PermanentExclusion should be false for DishonestReplay")
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_Collusion_First
// ---------------------------------------------------------------------------

func TestSlashEngine_Collusion_First(t *testing.T) {
	eng, _, v := newSlashEngine(t)

	res, err := eng.Slash(v.ID, OffenseCollusion)
	if err != nil {
		t.Fatalf("Slash: %v", err)
	}
	if res.SlashPercentage != 0.75 {
		t.Errorf("SlashPercentage: got %v, want 0.75", res.SlashPercentage)
	}
	if res.CooldownDays != 180 {
		t.Errorf("CooldownDays: got %d, want 180", res.CooldownDays)
	}
	// First offense — NOT permanent.
	if res.PermanentExclusion {
		t.Error("first collusion offense must not trigger permanent exclusion")
	}
	got, _ := eng.registry.Get(v.ID)
	if got.Status != StatusSuspended {
		t.Errorf("Status after first collusion: got %q, want suspended", got.Status)
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_Collusion_Repeat
// ---------------------------------------------------------------------------

func TestSlashEngine_Collusion_Repeat(t *testing.T) {
	eng, _, v := newSlashEngine(t)

	// First collusion.
	_, err := eng.Slash(v.ID, OffenseCollusion)
	if err != nil {
		t.Fatalf("first Slash: %v", err)
	}

	// Second collusion → permanent exclusion.
	res, err := eng.Slash(v.ID, OffenseCollusion)
	if err != nil {
		t.Fatalf("second Slash: %v", err)
	}
	if !res.PermanentExclusion {
		t.Error("second collusion offense must trigger permanent exclusion")
	}
	got, _ := eng.registry.Get(v.ID)
	if got.Status != StatusExcluded {
		t.Errorf("Status after repeat collusion: got %q, want excluded", got.Status)
	}
	if got.SlashCount != 2 {
		t.Errorf("SlashCount: got %d, want 2", got.SlashCount)
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_CollusionRepeatExclusion_Disabled
// ---------------------------------------------------------------------------

func TestSlashEngine_CollusionRepeatExclusion_Disabled(t *testing.T) {
	reg, v := newActiveValidator(t)
	cfg := slashCfg()
	cfg.CollusionRepeatExclusion = false
	eng := NewSlashEngine(reg, cfg)

	// Two collusion slashes — no permanent exclusion when feature is disabled.
	for i := 0; i < 2; i++ {
		res, err := eng.Slash(v.ID, OffenseCollusion)
		if err != nil {
			t.Fatalf("Slash %d: %v", i+1, err)
		}
		if res.PermanentExclusion {
			t.Errorf("Slash %d: unexpected permanent exclusion when feature is disabled", i+1)
		}
	}
	got, _ := reg.Get(v.ID)
	if got.Status == StatusExcluded {
		t.Error("validator must not be excluded when CollusionRepeatExclusion=false")
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_CanResume_BeforeCooldown
// ---------------------------------------------------------------------------

func TestSlashEngine_CanResume_BeforeCooldown(t *testing.T) {
	eng, _, v := newSlashEngine(t)
	if _, err := eng.Slash(v.ID, OffenseFraudulentApproval); err != nil {
		t.Fatalf("Slash: %v", err)
	}
	// Cooldown is 30 days — clearly not expired yet.
	ok, until := eng.CanResume(v.ID)
	if ok {
		t.Error("CanResume should be false while in cooldown")
	}
	if until.IsZero() {
		t.Error("CooldownUntil should be non-zero while in cooldown")
	}
	if until.Before(time.Now()) {
		t.Errorf("CooldownUntil %v is in the past (unexpected for a 30-day cooldown)", until)
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_CanResume_AfterCooldown
// ---------------------------------------------------------------------------

func TestSlashEngine_CanResume_AfterCooldown(t *testing.T) {
	eng, reg, v := newSlashEngine(t)
	if _, err := eng.Slash(v.ID, OffenseFraudulentApproval); err != nil {
		t.Fatalf("Slash: %v", err)
	}
	// Force SuspendedUntil into the past so cooldown appears expired.
	reg.mu.Lock()
	reg.validators[v.ID].SuspendedUntil = time.Now().Add(-time.Hour)
	reg.mu.Unlock()

	ok, until := eng.CanResume(v.ID)
	if !ok {
		t.Error("CanResume should be true after cooldown expires")
	}
	if !until.IsZero() {
		t.Errorf("CooldownUntil should be zero when resumable, got %v", until)
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_Resume_Success
// ---------------------------------------------------------------------------

func TestSlashEngine_Resume_Success(t *testing.T) {
	eng, reg, v := newSlashEngine(t)
	if _, err := eng.Slash(v.ID, OffenseFraudulentApproval); err != nil {
		t.Fatalf("Slash: %v", err)
	}
	// Expire the cooldown.
	reg.mu.Lock()
	reg.validators[v.ID].SuspendedUntil = time.Now().Add(-time.Hour)
	reg.mu.Unlock()

	if err := eng.Resume(v.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, _ := reg.Get(v.ID)
	if got.Status != StatusActive {
		t.Errorf("Status after Resume: got %q, want active", got.Status)
	}
	if !got.SuspendedUntil.IsZero() {
		t.Errorf("SuspendedUntil should be zero after Resume, got %v", got.SuspendedUntil)
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_Resume_BeforeCooldown_Fails
// ---------------------------------------------------------------------------

func TestSlashEngine_Resume_BeforeCooldown_Fails(t *testing.T) {
	eng, _, v := newSlashEngine(t)
	if _, err := eng.Slash(v.ID, OffenseFraudulentApproval); err != nil {
		t.Fatalf("Slash: %v", err)
	}
	err := eng.Resume(v.ID)
	if err == nil {
		t.Fatal("Resume should fail before cooldown expires")
	}
	if !errors.Is(err, ErrSlashInCooldown) {
		t.Errorf("expected ErrSlashInCooldown, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestSlashEngine_ValidatorNotFound
// ---------------------------------------------------------------------------

func TestSlashEngine_ValidatorNotFound(t *testing.T) {
	reg := NewValidatorRegistry(slashCfg(), nil)
	eng := NewSlashEngine(reg, slashCfg())

	_, err := eng.Slash("nonexistent-id", OffenseFraudulentApproval)
	if err == nil {
		t.Fatal("expected error for unknown validator")
	}
	if !errors.Is(err, ErrSlashValidatorNotFound) {
		t.Errorf("expected ErrSlashValidatorNotFound, got %v", err)
	}
}
