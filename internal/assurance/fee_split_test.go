package assurance

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// testAssuranceCfg returns the default AssuranceConfig for tests.
func testAssuranceCfg() *config.AssuranceConfig {
	return &config.AssuranceConfig{
		// Fee split — no replay
		VerifierShareNoReplay: 0.60,
		ReplayReserveShare:    0.25,
		ProtocolShareNoReplay: 0.15,
		// Fee split — replay
		VerifierShareWithReplay: 0.40,
		ReplayExecutorShare:     0.45,
		ProtocolShareReplay:     0.15,
		// Protocol-side breakdown
		TreasuryShareOfProtocol: 0.667,
		DisputeShareOfProtocol:  0.200,
		CanaryShareOfProtocol:   0.133,
		// Replay economics
		MinReplayPayout:             5_000_000,
		ReplayReserveCircuitBreaker: 0.20,
	}
}

// fee of 100_000_000 µAET (100 AET) used in most tests for easy mental-math.
const testFee = uint64(100_000_000)

// ---------------------------------------------------------------------------
// ComputeFeeSplit — no replay
// ---------------------------------------------------------------------------

func TestComputeFeeSplit_NoReplay_Splits(t *testing.T) {
	cfg := testAssuranceCfg()
	result := ComputeFeeSplit(testFee, false, cfg)

	if result.ReplayOccurred {
		t.Error("ReplayOccurred should be false")
	}
	wantVerifier := uint64(60_000_000)  // 60%
	wantReserve := uint64(25_000_000)   // 25%
	wantProtocol := uint64(15_000_000)  // 15%

	if result.VerifierPayout != wantVerifier {
		t.Errorf("VerifierPayout: got %d, want %d", result.VerifierPayout, wantVerifier)
	}
	if result.ReplayReservePortion != wantReserve {
		t.Errorf("ReplayReservePortion: got %d, want %d", result.ReplayReservePortion, wantReserve)
	}
	if result.ProtocolPortion != wantProtocol {
		t.Errorf("ProtocolPortion: got %d, want %d", result.ProtocolPortion, wantProtocol)
	}
	if result.ReplayExecutorPayout != 0 {
		t.Errorf("ReplayExecutorPayout should be 0 when no replay, got %d", result.ReplayExecutorPayout)
	}
}

// ---------------------------------------------------------------------------
// ComputeFeeSplit — with replay
// ---------------------------------------------------------------------------

func TestComputeFeeSplit_WithReplay_Splits(t *testing.T) {
	cfg := testAssuranceCfg()
	result := ComputeFeeSplit(testFee, true, cfg)

	if !result.ReplayOccurred {
		t.Error("ReplayOccurred should be true")
	}
	wantVerifier := uint64(40_000_000)  // 40%
	wantExecutor := uint64(45_000_000)  // 45%
	wantProtocol := uint64(15_000_000)  // 15%

	if result.VerifierPayout != wantVerifier {
		t.Errorf("VerifierPayout: got %d, want %d", result.VerifierPayout, wantVerifier)
	}
	if result.ReplayExecutorPayout != wantExecutor {
		t.Errorf("ReplayExecutorPayout: got %d, want %d", result.ReplayExecutorPayout, wantExecutor)
	}
	if result.ProtocolPortion != wantProtocol {
		t.Errorf("ProtocolPortion: got %d, want %d", result.ProtocolPortion, wantProtocol)
	}
	if result.ReplayReservePortion != 0 {
		t.Errorf("ReplayReservePortion should be 0 when replay occurred, got %d", result.ReplayReservePortion)
	}
}

// ---------------------------------------------------------------------------
// Protocol portion breakdown: treasury / dispute / canary ratios
// ---------------------------------------------------------------------------

func TestComputeFeeSplit_ProtocolBreakdown(t *testing.T) {
	cfg := testAssuranceCfg()
	result := ComputeFeeSplit(testFee, false, cfg)
	// ProtocolPortion = 15_000_000
	// treasury ≈ 66.7% = 10_005_000; after remainder → treasury gets any leftover
	// dispute  = 20.0% = 3_000_000
	// canary   = 13.3% = 1_995_000
	// sum = 10_005_000 + 3_000_000 + 1_995_000 = 15_000_000 ✓

	total := result.TreasuryAmount + result.DisputeReserveAmount + result.CanaryReserveAmount
	if total != result.ProtocolPortion {
		t.Errorf("protocol sub-totals %d != ProtocolPortion %d", total, result.ProtocolPortion)
	}
	if result.DisputeReserveAmount != 3_000_000 {
		t.Errorf("DisputeReserveAmount: got %d, want 3_000_000", result.DisputeReserveAmount)
	}
	if result.CanaryReserveAmount != 1_995_000 {
		t.Errorf("CanaryReserveAmount: got %d, want 1_995_000", result.CanaryReserveAmount)
	}
}

// ---------------------------------------------------------------------------
// Rounding: all portions sum to total fee (no µAET lost)
// ---------------------------------------------------------------------------

func TestComputeFeeSplit_RoundingNoLoss_NoReplay(t *testing.T) {
	cfg := testAssuranceCfg()
	// Use an odd fee to stress rounding.
	oddFee := uint64(99_999_999)
	result := ComputeFeeSplit(oddFee, false, cfg)

	sum := result.VerifierPayout + result.ReplayReservePortion +
		result.TreasuryAmount + result.DisputeReserveAmount + result.CanaryReserveAmount
	if sum != oddFee {
		t.Errorf("parts sum to %d, want %d (lost %d)", sum, oddFee, oddFee-sum)
	}
}

func TestComputeFeeSplit_RoundingNoLoss_WithReplay(t *testing.T) {
	cfg := testAssuranceCfg()
	oddFee := uint64(99_999_999)
	result := ComputeFeeSplit(oddFee, true, cfg)

	sum := result.VerifierPayout + result.ReplayExecutorPayout +
		result.TreasuryAmount + result.DisputeReserveAmount + result.CanaryReserveAmount
	if sum != oddFee {
		t.Errorf("parts sum to %d, want %d (lost %d)", sum, oddFee, oddFee-sum)
	}
}

// ---------------------------------------------------------------------------
// ComputeReplayExecutorPayout — base payout >= minimum → no reserve draw
// ---------------------------------------------------------------------------

func TestComputeReplayExecutorPayout_AboveMinimum(t *testing.T) {
	cfg := testAssuranceCfg()
	// testFee = 100 AET; 45% = 45 AET >> MinReplayPayout (5 AET)
	reserve := NewReplayReserve(cfg, nil)

	payout, draw := ComputeReplayExecutorPayout(testFee, reserve, "code", cfg)
	want := uint64(float64(testFee) * cfg.ReplayExecutorShare)
	if payout != want {
		t.Errorf("payout: got %d, want %d", payout, want)
	}
	if draw != 0 {
		t.Errorf("draw should be 0, got %d", draw)
	}
}

// ---------------------------------------------------------------------------
// ComputeReplayExecutorPayout — base payout < minimum → reserve draw makes up shortfall
// ---------------------------------------------------------------------------

func TestComputeReplayExecutorPayout_BelowMinimum_ReserveCovers(t *testing.T) {
	cfg := testAssuranceCfg()
	cfg.MinReplayPayout = 10_000_000 // 10 AET — larger than 45% of a tiny fee

	// Fee so small that 45% = 450_000, well below MinReplayPayout = 10_000_000.
	smallFee := uint64(1_000_000)
	reserve := NewReplayReserve(cfg, nil)
	// Pre-fund the reserve with enough to cover the shortfall.
	_ = reserve.Accrue("code", 20_000_000)

	payout, draw := ComputeReplayExecutorPayout(smallFee, reserve, "code", cfg)
	basePayout := uint64(float64(smallFee) * cfg.ReplayExecutorShare) // 450_000
	wantShortfall := cfg.MinReplayPayout - basePayout                  // 9_550_000
	if payout != cfg.MinReplayPayout {
		t.Errorf("payout: got %d, want %d (MinReplayPayout)", payout, cfg.MinReplayPayout)
	}
	if draw != wantShortfall {
		t.Errorf("draw: got %d, want %d", draw, wantShortfall)
	}
}

// ---------------------------------------------------------------------------
// ComputeReplayExecutorPayout — reserve insufficient → executor gets whatever is available
// ---------------------------------------------------------------------------

func TestComputeReplayExecutorPayout_ReserveInsufficient(t *testing.T) {
	cfg := testAssuranceCfg()
	cfg.MinReplayPayout = 10_000_000

	smallFee := uint64(1_000_000)
	reserve := NewReplayReserve(cfg, nil)
	// Only fund 1 AET in reserve — less than the shortfall.
	_ = reserve.Accrue("code", 1_000_000)

	payout, draw := ComputeReplayExecutorPayout(smallFee, reserve, "code", cfg)
	basePayout := uint64(float64(smallFee) * cfg.ReplayExecutorShare) // 450_000
	// shortfall = 9_550_000 but reserve only has 1_000_000
	if draw != 1_000_000 {
		t.Errorf("draw: got %d, want 1_000_000 (reserve exhausted)", draw)
	}
	if payout != basePayout+1_000_000 {
		t.Errorf("payout: got %d, want %d", payout, basePayout+1_000_000)
	}
	// Reserve should now be empty.
	if bal := reserve.Balance("code"); bal != 0 {
		t.Errorf("reserve should be 0 after exhaustion, got %d", bal)
	}
}

// ---------------------------------------------------------------------------
// ProcessSettlement — no replay → reserve accrues
// ---------------------------------------------------------------------------

func TestProcessSettlement_NoReplay_AccruesReserve(t *testing.T) {
	cfg := testAssuranceCfg()
	reserve := NewReplayReserve(cfg, nil)

	result := ProcessSettlement(testFee, "code", false, reserve, cfg)

	if result.ReplayOccurred {
		t.Error("ReplayOccurred should be false")
	}
	wantAccrued := result.ReplayReservePortion
	if wantAccrued == 0 {
		t.Fatal("expected non-zero reserve portion")
	}
	if bal := reserve.Balance("code"); bal != wantAccrued {
		t.Errorf("reserve balance: got %d, want %d", bal, wantAccrued)
	}
}

// ---------------------------------------------------------------------------
// ProcessSettlement — with replay → reserve not accrued
// ---------------------------------------------------------------------------

func TestProcessSettlement_WithReplay_NoAccrual(t *testing.T) {
	cfg := testAssuranceCfg()
	reserve := NewReplayReserve(cfg, nil)

	result := ProcessSettlement(testFee, "code", true, reserve, cfg)

	if !result.ReplayOccurred {
		t.Error("ReplayOccurred should be true")
	}
	// Reserve should NOT accrue when replay occurred.
	if bal := reserve.Balance("code"); bal != 0 {
		t.Errorf("reserve should not accrue on replay, got balance %d", bal)
	}
}

// ---------------------------------------------------------------------------
// Zero fee edge case
// ---------------------------------------------------------------------------

func TestComputeFeeSplit_ZeroFee(t *testing.T) {
	cfg := testAssuranceCfg()
	result := ComputeFeeSplit(0, false, cfg)
	if result.TotalAssuranceFee != 0 {
		t.Errorf("expected zero result, got %+v", result)
	}
	if result.VerifierPayout+result.ReplayReservePortion+result.ProtocolPortion != 0 {
		t.Error("all portions should be zero for zero fee")
	}
}
