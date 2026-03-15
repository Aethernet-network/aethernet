package assurance

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// testCfg returns a default-filled AssuranceConfig for use in tests.
func testCfg() *config.AssuranceConfig {
	d := config.DefaultConfig()
	return &d.Assurance
}

// ---------------------------------------------------------------------------
// ComputeAssuranceFee
// ---------------------------------------------------------------------------

// TestComputeAssuranceFee_LaneNone_ZeroFee verifies that LaneNone returns a
// zero-fee result with no error, regardless of budget.
func TestComputeAssuranceFee_LaneNone_ZeroFee(t *testing.T) {
	cfg := testCfg()
	result, err := ComputeAssuranceFee(1_000_000, LaneNone, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Lane != LaneNone {
		t.Errorf("Lane = %q; want %q", result.Lane, LaneNone)
	}
	if result.Fee != 0 {
		t.Errorf("Fee = %d; want 0", result.Fee)
	}
	if result.NetPayout != 0 {
		t.Errorf("NetPayout = %d; want 0", result.NetPayout)
	}
}

// TestComputeAssuranceFee_BelowMinBudget_Error verifies that a budget below
// MinTaskBudgetAssured returns an error for any non-empty lane.
func TestComputeAssuranceFee_BelowMinBudget_Error(t *testing.T) {
	cfg := testCfg()
	// cfg.MinTaskBudgetAssured = 25_000_000 (25 AET)
	below := cfg.MinTaskBudgetAssured - 1
	for _, lane := range []AssuranceLane{LaneStandard, LaneHighAssurance, LaneEnterprise} {
		_, err := ComputeAssuranceFee(below, lane, cfg)
		if err == nil {
			t.Errorf("lane %q: expected error for budget below minimum, got nil", lane)
		}
	}
}

// TestComputeAssuranceFee_UnknownLane_Error verifies that an unrecognised lane
// value returns an error.
func TestComputeAssuranceFee_UnknownLane_Error(t *testing.T) {
	cfg := testCfg()
	_, err := ComputeAssuranceFee(30_000_000, "premium", cfg)
	if err == nil {
		t.Error("expected error for unknown lane, got nil")
	}
}

// TestComputeAssuranceFee_Standard_FeeAndPayout verifies standard lane fee
// computation: 3% rate, 2 AET floor.
func TestComputeAssuranceFee_Standard_FeeAndPayout(t *testing.T) {
	cfg := testCfg()
	// budget = 100 AET = 100_000_000 µAET → fee = 3_000_000 (3%) > floor
	budget := uint64(100_000_000)
	result, err := ComputeAssuranceFee(budget, LaneStandard, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFee := uint64(3_000_000) // 3%
	if result.Fee != wantFee {
		t.Errorf("Fee = %d; want %d", result.Fee, wantFee)
	}
	if result.NetPayout != budget-wantFee {
		t.Errorf("NetPayout = %d; want %d", result.NetPayout, budget-wantFee)
	}
	if result.Lane != LaneStandard {
		t.Errorf("Lane = %q; want %q", result.Lane, LaneStandard)
	}
}

// TestComputeAssuranceFee_Standard_FloorApplied verifies that the standard
// fee floor (2 AET) is applied when rate×budget < floor.
func TestComputeAssuranceFee_Standard_FloorApplied(t *testing.T) {
	cfg := testCfg()
	// budget = 25 AET = 25_000_000 µAET → rate fee = 750_000 < floor 2_000_000
	budget := uint64(25_000_000)
	result, err := ComputeAssuranceFee(budget, LaneStandard, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFee := cfg.StandardFeeFloor // 2_000_000
	if result.Fee != wantFee {
		t.Errorf("Fee = %d; want floor %d", result.Fee, wantFee)
	}
	if result.NetPayout != budget-wantFee {
		t.Errorf("NetPayout = %d; want %d", result.NetPayout, budget-wantFee)
	}
}

// TestComputeAssuranceFee_HighAssurance_FeeAndPayout verifies high_assurance
// lane: 6% rate, 4 AET floor.
func TestComputeAssuranceFee_HighAssurance_FeeAndPayout(t *testing.T) {
	cfg := testCfg()
	// budget = 100 AET → fee = 6_000_000 (6%) > floor 4_000_000
	budget := uint64(100_000_000)
	result, err := ComputeAssuranceFee(budget, LaneHighAssurance, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFee := uint64(6_000_000)
	if result.Fee != wantFee {
		t.Errorf("Fee = %d; want %d", result.Fee, wantFee)
	}
}

// TestComputeAssuranceFee_Enterprise_FeeAndPayout verifies enterprise lane:
// 8% rate, 8 AET floor.
func TestComputeAssuranceFee_Enterprise_FeeAndPayout(t *testing.T) {
	cfg := testCfg()
	// budget = 200 AET → fee = 16_000_000 (8%) > floor 8_000_000
	budget := uint64(200_000_000)
	result, err := ComputeAssuranceFee(budget, LaneEnterprise, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFee := uint64(16_000_000)
	if result.Fee != wantFee {
		t.Errorf("Fee = %d; want %d", result.Fee, wantFee)
	}
}

// TestComputeAssuranceFee_Enterprise_FloorApplied verifies that the enterprise
// floor (8 AET) is applied when rate×budget < floor.
func TestComputeAssuranceFee_Enterprise_FloorApplied(t *testing.T) {
	cfg := testCfg()
	// budget = 25 AET → rate fee = 2_000_000 < floor 8_000_000
	budget := uint64(25_000_000)
	result, err := ComputeAssuranceFee(budget, LaneEnterprise, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFee := cfg.EnterpriseFeeFloor // 8_000_000
	if result.Fee != wantFee {
		t.Errorf("Fee = %d; want enterprise floor %d", result.Fee, wantFee)
	}
}

// TestComputeAssuranceFee_FeeCappedAtBudget verifies that the fee is capped at
// the budget to prevent NetPayout underflow.
func TestComputeAssuranceFee_FeeCappedAtBudget(t *testing.T) {
	// Artificially high floor to trigger cap.
	cfg := &config.AssuranceConfig{
		MinTaskBudgetAssured: 10,
		StandardFeeRate:      0.03,
		StandardFeeFloor:     999_999_999, // floor > budget
	}
	budget := uint64(25_000_000)
	result, err := ComputeAssuranceFee(budget, LaneStandard, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Fee > budget {
		t.Errorf("Fee %d > budget %d; want capped at budget", result.Fee, budget)
	}
	if result.NetPayout != 0 {
		t.Errorf("NetPayout = %d; want 0 (all fees)", result.NetPayout)
	}
}

// TestComputeAssuranceFee_NetPayoutIsbudgetMinusFee verifies the invariant
// NetPayout = Budget - Fee for all three lanes.
func TestComputeAssuranceFee_NetPayoutIsbudgetMinusFee(t *testing.T) {
	cfg := testCfg()
	budget := uint64(50_000_000)
	for _, lane := range []AssuranceLane{LaneStandard, LaneHighAssurance, LaneEnterprise} {
		result, err := ComputeAssuranceFee(budget, lane, cfg)
		if err != nil {
			t.Fatalf("lane %q: unexpected error: %v", lane, err)
		}
		if result.NetPayout != budget-result.Fee {
			t.Errorf("lane %q: NetPayout %d != budget %d - fee %d", lane, result.NetPayout, budget, result.Fee)
		}
	}
}
