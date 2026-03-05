// Package fees implements settlement fee collection for the AetherNet OCS engine.
//
// Every settled transaction pays a 10 basis-point (0.1%) fee split three ways:
//   - 70% credited to the validating agent as a direct incentive
//   - 20% credited to the protocol treasury
//   - 10% burned (removed from circulation by not crediting anyone)
//
// Fee collection is optional: when no Collector is wired into the OCS engine the
// settlement path is unchanged and all existing tests continue to pass.
package fees

import (
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

const (
	// FeeBasisPoints is the settlement fee expressed in basis points (10 bps = 0.1%).
	FeeBasisPoints uint64 = 10

	// ValidatorShare is the percentage of each fee credited to the verifying agent.
	ValidatorShare uint64 = 70

	// TreasuryShare is the percentage credited to the protocol treasury.
	TreasuryShare uint64 = 20

	// BurnShare is the percentage permanently removed from circulation.
	BurnShare uint64 = 10
)

// Collector accumulates fee statistics and distributes each collected fee across
// validator, treasury, and burn sinks.
type Collector struct {
	mu              sync.RWMutex
	totalCollected  uint64
	totalBurned     uint64
	treasuryAccrued uint64
	transfer        *ledger.TransferLedger
}

// NewCollector returns a Collector backed by tl for validator and treasury credits.
func NewCollector(tl *ledger.TransferLedger) *Collector {
	return &Collector{transfer: tl}
}

// CalculateFee returns the fee for a transaction of the given amount.
// The result is rounded down; amounts below 10 000 micro-AET produce a zero fee.
func CalculateFee(amount uint64) uint64 {
	return amount * FeeBasisPoints / 10_000
}

// CollectFee splits a settlement fee three ways: validator share, treasury share,
// and burn. It credits validator and treasury via the TransferLedger and tracks
// all splits internally for reporting via Stats.
//
// Returns the total fee and the amount burned. Both are zero when the fee
// rounds to zero (small transaction amounts).
func (c *Collector) CollectFee(
	amount uint64,
	validatorID crypto.AgentID,
	treasuryID crypto.AgentID,
) (fee uint64, burned uint64) {
	fee = CalculateFee(amount)
	if fee == 0 {
		return 0, 0
	}

	validatorAmount := fee * ValidatorShare / 100
	treasuryAmount := fee * TreasuryShare / 100
	// Assign remainder to burn to avoid rounding loss.
	burned = fee - validatorAmount - treasuryAmount

	c.mu.Lock()
	c.totalCollected += fee
	c.totalBurned += burned
	c.treasuryAccrued += treasuryAmount
	c.mu.Unlock()

	// Best-effort credits — errors are ignored because fee distribution should
	// never block settlement.
	if validatorAmount > 0 {
		_ = c.transfer.FundAgent(validatorID, validatorAmount)
	}
	if treasuryAmount > 0 {
		_ = c.transfer.FundAgent(treasuryID, treasuryAmount)
	}
	return fee, burned
}

// Stats returns cumulative fee collection statistics as a point-in-time snapshot.
func (c *Collector) Stats() (collected, burned, treasury uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalCollected, c.totalBurned, c.treasuryAccrued
}
