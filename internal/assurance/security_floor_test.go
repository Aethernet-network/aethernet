package assurance

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// newTestFloor returns a SecurityFloor with default config for testing.
func newTestFloor() *SecurityFloor {
	d := config.DefaultConfig()
	return NewSecurityFloor(&d.Assurance)
}

// ---------------------------------------------------------------------------
// CheckLane
// ---------------------------------------------------------------------------

// TestSecurityFloor_LaneNone_AlwaysPasses verifies that LaneNone is always
// returned unchanged regardless of validator count.
func TestSecurityFloor_LaneNone_AlwaysPasses(t *testing.T) {
	sf := newTestFloor()
	// No state set — zero validators.
	result := sf.CheckLane("code", LaneNone)
	if result.Lane != LaneNone {
		t.Errorf("Lane = %q; want %q", result.Lane, LaneNone)
	}
}

// TestSecurityFloor_Standard_MetFloor verifies that LaneStandard is returned
// when validator count ≥ SecurityFloorStandard (3.0).
func TestSecurityFloor_Standard_MetFloor(t *testing.T) {
	sf := newTestFloor()
	sf.SetState(CategorySecurityState{Category: "code", ValidatorCount: 3.0})

	result := sf.CheckLane("code", LaneStandard)
	if result.Lane != LaneStandard {
		t.Errorf("Lane = %q; want %q", result.Lane, LaneStandard)
	}
}

// TestSecurityFloor_Standard_BelowFloor_DowngradedToNone verifies that when
// validator count < SecurityFloorStandard the check returns LaneNone.
func TestSecurityFloor_Standard_BelowFloor_DowngradedToNone(t *testing.T) {
	sf := newTestFloor()
	sf.SetState(CategorySecurityState{Category: "code", ValidatorCount: 2.9})

	result := sf.CheckLane("code", LaneStandard)
	if result.Lane != LaneNone {
		t.Errorf("Lane = %q; want %q (below standard floor)", result.Lane, LaneNone)
	}
}

// TestSecurityFloor_High_MetFloor verifies LaneHighAssurance is returned when
// validator count ≥ SecurityFloorHigh (5.0).
func TestSecurityFloor_High_MetFloor(t *testing.T) {
	sf := newTestFloor()
	sf.SetState(CategorySecurityState{Category: "data", ValidatorCount: 5.0})

	result := sf.CheckLane("data", LaneHighAssurance)
	if result.Lane != LaneHighAssurance {
		t.Errorf("Lane = %q; want %q", result.Lane, LaneHighAssurance)
	}
}

// TestSecurityFloor_High_BelowFloor_DowngradedToStandard verifies that when
// validator count is in [3.0, 5.0) the check for high_assurance downgrades to
// LaneStandard.
func TestSecurityFloor_High_BelowFloor_DowngradedToStandard(t *testing.T) {
	sf := newTestFloor()
	sf.SetState(CategorySecurityState{Category: "data", ValidatorCount: 4.0})

	result := sf.CheckLane("data", LaneHighAssurance)
	if result.Lane != LaneStandard {
		t.Errorf("Lane = %q; want %q (4 validators meets standard, not high)", result.Lane, LaneStandard)
	}
}

// TestSecurityFloor_Enterprise_MetFloor verifies LaneEnterprise is returned
// when validator count ≥ SecurityFloorEnterprise (10.0).
func TestSecurityFloor_Enterprise_MetFloor(t *testing.T) {
	sf := newTestFloor()
	sf.SetState(CategorySecurityState{Category: "api", ValidatorCount: 10.0})

	result := sf.CheckLane("api", LaneEnterprise)
	if result.Lane != LaneEnterprise {
		t.Errorf("Lane = %q; want %q", result.Lane, LaneEnterprise)
	}
}

// TestSecurityFloor_Enterprise_BelowFloor_DowngradedToHigh verifies that when
// count ∈ [5.0, 10.0) the enterprise check downgrades to LaneHighAssurance.
func TestSecurityFloor_Enterprise_BelowFloor_DowngradedToHigh(t *testing.T) {
	sf := newTestFloor()
	sf.SetState(CategorySecurityState{Category: "api", ValidatorCount: 7.0})

	result := sf.CheckLane("api", LaneEnterprise)
	if result.Lane != LaneHighAssurance {
		t.Errorf("Lane = %q; want %q (7 validators meets high, not enterprise)", result.Lane, LaneHighAssurance)
	}
}

// TestSecurityFloor_UnknownCategory_ZeroValidators verifies that an unseen
// category has zero validators and standard floor is not met.
func TestSecurityFloor_UnknownCategory_ZeroValidators(t *testing.T) {
	sf := newTestFloor()
	// No state for "unknown-category".
	result := sf.CheckLane("unknown-category", LaneStandard)
	if result.Lane != LaneNone {
		t.Errorf("Lane = %q; want %q (zero validators)", result.Lane, LaneNone)
	}
}

// TestSecurityFloor_SetState_UpdateOverwrites verifies that a second SetState
// call for the same category updates the count.
func TestSecurityFloor_SetState_UpdateOverwrites(t *testing.T) {
	sf := newTestFloor()
	sf.SetState(CategorySecurityState{Category: "code", ValidatorCount: 2.0})
	sf.SetState(CategorySecurityState{Category: "code", ValidatorCount: 5.0})

	result := sf.CheckLane("code", LaneHighAssurance)
	if result.Lane != LaneHighAssurance {
		t.Errorf("Lane = %q; want %q (count updated to 5.0)", result.Lane, LaneHighAssurance)
	}
}

// ---------------------------------------------------------------------------
// IsStructuredCategory
// ---------------------------------------------------------------------------

// TestIsStructuredCategory_Defaults verifies that the default structured
// categories (code, data, api, infrastructure) are all recognised.
func TestIsStructuredCategory_Defaults(t *testing.T) {
	sf := newTestFloor()
	for _, cat := range []string{"code", "data", "api", "infrastructure"} {
		if !sf.IsStructuredCategory(cat) {
			t.Errorf("IsStructuredCategory(%q) = false; want true", cat)
		}
	}
}

// TestIsStructuredCategory_NonStructured verifies that unstructured categories
// are not in the structured list.
func TestIsStructuredCategory_NonStructured(t *testing.T) {
	sf := newTestFloor()
	for _, cat := range []string{"writing", "research", "design", "", "unknown"} {
		if sf.IsStructuredCategory(cat) {
			t.Errorf("IsStructuredCategory(%q) = true; want false", cat)
		}
	}
}

// ---------------------------------------------------------------------------
// SecurityFloor thread safety
// ---------------------------------------------------------------------------

// TestSecurityFloor_ConcurrentSetAndCheck verifies that concurrent SetState and
// CheckLane calls do not race. Run with -race.
func TestSecurityFloor_ConcurrentSetAndCheck(t *testing.T) {
	sf := newTestFloor()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			sf.SetState(CategorySecurityState{Category: "code", ValidatorCount: float64(i)})
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		sf.CheckLane("code", LaneStandard)
	}
	<-done
}
