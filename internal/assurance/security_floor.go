package assurance

import (
	"sync"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// CategorySecurityState holds the current validator coverage for a task
// category. ValidatorCount is the number of active validators qualified for
// that category.
type CategorySecurityState struct {
	Category       string
	ValidatorCount float64
}

// SecurityFloor enforces minimum validator coverage requirements before
// high_assurance and enterprise lanes can be offered for a given category.
// All categories can access LaneStandard provided they meet the standard floor.
//
// Callers record live coverage via SetState; PostTask (or the API layer)
// calls CheckLane before accepting a task in an assured lane.
type SecurityFloor struct {
	mu     sync.RWMutex
	states map[string]float64 // category → validatorCount
	cfg    *config.AssuranceConfig
}

// NewSecurityFloor creates a SecurityFloor wired to the given AssuranceConfig.
func NewSecurityFloor(cfg *config.AssuranceConfig) *SecurityFloor {
	return &SecurityFloor{
		states: make(map[string]float64),
		cfg:    cfg,
	}
}

// SetState records the current validator coverage for a category. Subsequent
// CheckLane calls for that category use this count. Thread-safe.
func (sf *SecurityFloor) SetState(s CategorySecurityState) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.states[s.Category] = s.ValidatorCount
}

// CheckLane returns the best AssuranceFeeResult available for the given
// category at the requested lane given the current security state.
//
//   - If the requested lane is achievable (validator count ≥ floor), the result
//     carries Lane == requested lane and zero Fee/NetPayout (fee is computed
//     separately by ComputeAssuranceFee).
//   - If the floor is not met, the result carries the best available downgraded
//     lane (possibly LaneNone). The caller must treat Lane != requestedLane as
//     an insufficient_category_security condition.
//   - For LaneNone the result is always Lane == LaneNone.
func (sf *SecurityFloor) CheckLane(category string, lane AssuranceLane) *AssuranceFeeResult {
	if lane == LaneNone {
		return &AssuranceFeeResult{Lane: LaneNone}
	}
	sf.mu.RLock()
	count := sf.states[category]
	sf.mu.RUnlock()

	required := sf.floorForLane(lane)
	if count >= required {
		return &AssuranceFeeResult{Lane: lane}
	}
	// Floor not met — return the best available downgraded lane.
	offered := sf.bestAvailableLane(count)
	return &AssuranceFeeResult{Lane: offered}
}

// IsStructuredCategory returns true when category is in the configured
// StructuredCategories list. Only structured categories qualify for
// LaneHighAssurance and LaneEnterprise.
func (sf *SecurityFloor) IsStructuredCategory(category string) bool {
	for _, c := range sf.cfg.StructuredCategories {
		if c == category {
			return true
		}
	}
	return false
}

// floorForLane returns the minimum validator count required for the lane.
func (sf *SecurityFloor) floorForLane(lane AssuranceLane) float64 {
	switch lane {
	case LaneStandard:
		return sf.cfg.SecurityFloorStandard
	case LaneHighAssurance:
		return sf.cfg.SecurityFloorHigh
	case LaneEnterprise:
		return sf.cfg.SecurityFloorEnterprise
	default:
		return 0
	}
}

// bestAvailableLane returns the highest lane achievable with the given count.
func (sf *SecurityFloor) bestAvailableLane(count float64) AssuranceLane {
	if count >= sf.cfg.SecurityFloorEnterprise {
		return LaneEnterprise
	}
	if count >= sf.cfg.SecurityFloorHigh {
		return LaneHighAssurance
	}
	if count >= sf.cfg.SecurityFloorStandard {
		return LaneStandard
	}
	return LaneNone
}
