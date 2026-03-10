package genesis_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/genesis"
)

// TestAllocationsSum verifies that all genesis bucket allocations sum to TotalSupply.
func TestAllocationsSum(t *testing.T) {
	got := genesis.FoundersAllocation +
		genesis.InvestorsAllocation +
		genesis.EcosystemAllocation +
		genesis.NetworkRewards +
		genesis.TreasuryAllocation +
		genesis.PublicAllocation
	if got != genesis.TotalSupply {
		t.Errorf("allocations sum = %d, want TotalSupply %d (delta %d)",
			got, genesis.TotalSupply, int64(got)-int64(genesis.TotalSupply))
	}
}

// TestOnboardingAllocation_Curve verifies the correct per-agent amount at every
// tier boundary.
func TestOnboardingAllocation_Curve(t *testing.T) {
	cases := []struct {
		agentCount uint64
		want       uint64
		label      string
	}{
		{0, 50_000_000_000, "first agent (tier 1)"},
		{999, 50_000_000_000, "last of tier 1"},
		{1_000, 10_000_000_000, "first of tier 2"},
		{9_999, 10_000_000_000, "last of tier 2"},
		{10_000, 1_000_000_000, "first of tier 3"},
		{99_999, 1_000_000_000, "last of tier 3"},
		{100_000, 100_000_000, "first of tier 4"},
		{799_999, 100_000_000, "last of tier 4"},
		{800_000, 0, "at cap: onboarding closed"},
		{5_000_000, 0, "well beyond cap"},
	}
	for _, tc := range cases {
		got := genesis.OnboardingAllocation(tc.agentCount)
		if got != tc.want {
			t.Errorf("OnboardingAllocation(%d) [%s] = %d, want %d",
				tc.agentCount, tc.label, got, tc.want)
		}
	}
}

// TestOnboardingAllocation_TotalDoesNotExceedPool simulates onboarding all
// OnboardingMaxAgents and verifies the cumulative allocation does not exceed
// OnboardingPoolTotal.
func TestOnboardingAllocation_TotalDoesNotExceedPool(t *testing.T) {
	var total uint64
	for i := uint64(0); i < genesis.OnboardingMaxAgents; i++ {
		total += genesis.OnboardingAllocation(i)
	}
	if total > genesis.OnboardingPoolTotal {
		t.Errorf("total allocation %d exceeds OnboardingPoolTotal %d",
			total, genesis.OnboardingPoolTotal)
	}
	// Pool closes exactly at the cap.
	if got := genesis.OnboardingAllocation(genesis.OnboardingMaxAgents); got != 0 {
		t.Errorf("OnboardingAllocation at cap = %d, want 0", got)
	}
}
