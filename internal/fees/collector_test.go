package fees_test

import (
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// TestCalculateFee verifies the 10 bps fee calculation at representative amounts.
func TestCalculateFee(t *testing.T) {
	cases := []struct {
		amount uint64
		want   uint64
	}{
		{10_000, 10},      // 10000 * 10 / 10000 = 10
		{1_000_000, 1000}, // 1000000 * 10 / 10000 = 1000
		{100, 0},          // rounds down to 0
		{9_999, 9},        // 9999 * 10 / 10000 = 9
		{0, 0},
	}
	for _, tc := range cases {
		got := fees.CalculateFee(tc.amount)
		if got != tc.want {
			t.Errorf("CalculateFee(%d) = %d, want %d", tc.amount, got, tc.want)
		}
	}
}

// TestCollectFee_Split verifies that a collected fee is split across validator
// (70%), treasury (20%), and burn (10%) with no rounding loss.
func TestCollectFee_Split(t *testing.T) {
	tl := ledger.NewTransferLedger()
	validatorID := crypto.AgentID("test-validator")
	treasuryID := crypto.AgentID("test-treasury")

	c := fees.NewCollector(tl)

	const amount = 1_000_000 // fee = 1000
	fee, burned := c.CollectFee(amount, validatorID, treasuryID)
	if fee != 1000 {
		t.Fatalf("fee = %d, want 1000", fee)
	}

	wantValidator := uint64(700) // 70% of 1000
	wantTreasury := uint64(200)  // 20% of 1000
	wantBurned := uint64(100)    // 10% of 1000

	if burned != wantBurned {
		t.Errorf("burned = %d, want %d", burned, wantBurned)
	}

	gotValidator, err := tl.Balance(validatorID)
	if err != nil {
		t.Fatalf("validator balance: %v", err)
	}
	if gotValidator != wantValidator {
		t.Errorf("validator balance = %d, want %d", gotValidator, wantValidator)
	}

	gotTreasury, err := tl.Balance(treasuryID)
	if err != nil {
		t.Fatalf("treasury balance: %v", err)
	}
	if gotTreasury != wantTreasury {
		t.Errorf("treasury balance = %d, want %d", gotTreasury, wantTreasury)
	}

	// No rounding loss: validator + treasury + burned == fee.
	if wantValidator+wantTreasury+wantBurned != fee {
		t.Errorf("split sum %d != fee %d", wantValidator+wantTreasury+wantBurned, fee)
	}
}

// TestCollectFee_Stats verifies cumulative stats after multiple fee collections.
func TestCollectFee_Stats(t *testing.T) {
	tl := ledger.NewTransferLedger()
	c := fees.NewCollector(tl)

	validatorID := crypto.AgentID("v")
	treasuryID := crypto.AgentID("t")

	for i := 0; i < 3; i++ {
		c.CollectFee(1_000_000, validatorID, treasuryID) // fee = 1000 each
	}

	collected, burned, treasury := c.Stats()
	if collected != 3_000 {
		t.Errorf("totalCollected = %d, want 3000", collected)
	}
	if burned != 300 {
		t.Errorf("totalBurned = %d, want 300", burned)
	}
	if treasury != 600 {
		t.Errorf("treasuryAccrued = %d, want 600", treasury)
	}
}
