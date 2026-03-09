// Package fees implements settlement fee collection for the AetherNet OCS engine.
//
// Every settled transaction pays a 10 basis-point (0.1%) fee split two ways:
//   - 80% credited to the validating agent as a direct incentive
//   - 20% credited to the protocol treasury
//
// All fees stay in circulation — there is no burn. This maximises liquidity and
// keeps fees flowing between agents rather than leaving the network.
//
// Fee collection is optional: when no Collector is wired into the OCS engine the
// settlement path is unchanged and all existing tests continue to pass.
package fees

import (
	"encoding/binary"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// feeStore is the subset of store.Store used by Collector for stat persistence.
// *store.Store from the store package satisfies this interface.
type feeStore interface {
	PutMeta(key string, value []byte) error
	GetMeta(key string) ([]byte, error)
}

const feeStatsKey = "fee_collector_stats"

const (
	// FeeBasisPoints is the settlement fee expressed in basis points (10 bps = 0.1%).
	FeeBasisPoints uint64 = 10

	// ValidatorShare is the percentage of each fee credited to the verifying agent.
	ValidatorShare uint64 = 80

	// TreasuryShare is the percentage credited to the protocol treasury.
	TreasuryShare uint64 = 20

	// BurnShare is zero — all fees stay in circulation.
	BurnShare uint64 = 0
)

// Collector accumulates fee statistics and distributes each collected fee across
// validator, treasury, and burn sinks.
type Collector struct {
	mu              sync.RWMutex
	totalCollected  uint64
	totalBurned     uint64
	treasuryAccrued uint64
	transfer        *ledger.TransferLedger
	store           feeStore // optional; when set, stats are persisted across restarts

	// Configurable fee parameters — default to the package constants.
	feeBPS       uint64 // basis points (default: FeeBasisPoints = 10)
	validatorPct uint64 // validator share percentage (default: ValidatorShare = 80)
	treasuryPct  uint64 // treasury share percentage (default: TreasuryShare = 20)
}

// NewCollector returns a Collector backed by tl for validator and treasury credits.
func NewCollector(tl *ledger.TransferLedger) *Collector {
	return &Collector{
		transfer:     tl,
		feeBPS:       FeeBasisPoints,
		validatorPct: ValidatorShare,
		treasuryPct:  TreasuryShare,
	}
}

// SetFeeParams overrides the fee distribution parameters. All three values are
// applied atomically. bps is in basis points; validatorPct and treasuryPct are
// percentages that should sum to ≤ 100 (any remainder is burned).
// Call before the node starts processing transactions.
func (c *Collector) SetFeeParams(bps, validatorPct, treasuryPct uint64) {
	c.mu.Lock()
	c.feeBPS = bps
	c.validatorPct = validatorPct
	c.treasuryPct = treasuryPct
	c.mu.Unlock()
}

// calculateFee returns the fee for a transaction of the given amount using the
// instance-level basis-point setting. The result is rounded down.
func (c *Collector) calculateFee(amount uint64) uint64 {
	c.mu.RLock()
	bps := c.feeBPS
	c.mu.RUnlock()
	if bps == 0 {
		return 0
	}
	return amount * bps / 10_000
}

// SetStore attaches a persistence backend. When set, cumulative fee statistics
// are loaded immediately and saved after every CollectFee call, so totals
// survive node restarts. Call before the node starts processing transactions.
func (c *Collector) SetStore(s feeStore) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = s
	// Load persisted stats so we resume from the previous session's totals.
	if data, err := s.GetMeta(feeStatsKey); err == nil && len(data) == 24 {
		c.totalCollected = binary.LittleEndian.Uint64(data[0:8])
		c.totalBurned = binary.LittleEndian.Uint64(data[8:16])
		c.treasuryAccrued = binary.LittleEndian.Uint64(data[16:24])
	}
}

// persistStatsLocked writes the current counters to the store.
// Must be called with c.mu held.
func (c *Collector) persistStatsLocked() {
	if c.store == nil {
		return
	}
	var buf [24]byte
	binary.LittleEndian.PutUint64(buf[0:8], c.totalCollected)
	binary.LittleEndian.PutUint64(buf[8:16], c.totalBurned)
	binary.LittleEndian.PutUint64(buf[16:24], c.treasuryAccrued)
	_ = c.store.PutMeta(feeStatsKey, buf[:])
}

// CalculateFee returns the fee for a transaction of the given amount.
// The result is rounded down; amounts below 10 000 micro-AET produce a zero fee.
func CalculateFee(amount uint64) uint64 {
	return amount * FeeBasisPoints / 10_000
}

// CollectFee splits a settlement fee between the validator (80%) and the
// protocol treasury (20%). It credits both via the TransferLedger and tracks
// all splits internally for reporting via Stats. The burn amount is always 0 —
// all fees stay in circulation.
//
// Returns the total fee and the amount burned (always 0). Both are zero when
// the fee rounds to zero (small transaction amounts).
func (c *Collector) CollectFee(
	amount uint64,
	validatorID crypto.AgentID,
	treasuryID crypto.AgentID,
) (fee uint64, burned uint64) {
	fee = c.calculateFee(amount)
	if fee == 0 {
		return 0, 0
	}

	c.mu.RLock()
	vPct := c.validatorPct
	tPct := c.treasuryPct
	c.mu.RUnlock()

	validatorAmount := fee * vPct / 100
	treasuryAmount := fee * tPct / 100
	// Assign remainder to burn to avoid rounding loss.
	burned = fee - validatorAmount - treasuryAmount

	c.mu.Lock()
	c.totalCollected += fee
	c.totalBurned += burned
	c.treasuryAccrued += treasuryAmount
	c.persistStatsLocked()
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

// TrackFee records fee statistics without minting new tokens. Use this instead
// of CollectFee when the fee has already been distributed via escrow bucket
// transfers (e.g. ReleaseNet). This avoids double-crediting the validator and
// treasury while still keeping the cumulative statistics accurate.
func (c *Collector) TrackFee(fee, burned, treasury uint64) {
	if fee == 0 {
		return
	}
	c.mu.Lock()
	c.totalCollected += fee
	c.totalBurned += burned
	c.treasuryAccrued += treasury
	c.persistStatsLocked()
	c.mu.Unlock()
}

// CollectFeeFromRecipient deducts the settlement fee from recipientID's balance
// and redistributes the splits to the validator and treasury via TransferFromBucket.
// Unlike CollectFee, no new tokens are created — the fee is moved from the
// transaction recipient's balance, preserving the total supply invariant.
//
// Returns the total fee deducted and the amount burned (always 0 — all fees
// stay in circulation). Both are zero when the fee rounds to zero.
func (c *Collector) CollectFeeFromRecipient(
	recipientID crypto.AgentID,
	amount uint64,
	validatorID crypto.AgentID,
	treasuryID crypto.AgentID,
) (fee uint64, burned uint64) {
	fee = c.calculateFee(amount)
	if fee == 0 {
		return 0, 0
	}

	c.mu.RLock()
	vPct := c.validatorPct
	tPct := c.treasuryPct
	c.mu.RUnlock()

	validatorAmount := fee * vPct / 100
	treasuryAmount := fee * tPct / 100
	burned = fee - validatorAmount - treasuryAmount

	c.mu.Lock()
	c.totalCollected += fee
	c.totalBurned += burned
	c.treasuryAccrued += treasuryAmount
	c.persistStatsLocked()
	c.mu.Unlock()

	// Move tokens from recipient to each sink — no new tokens created.
	// Best-effort: errors are ignored so fee distribution never blocks settlement.
	if validatorAmount > 0 && validatorID != "" {
		_ = c.transfer.TransferFromBucket(recipientID, validatorID, validatorAmount)
	}
	if treasuryAmount > 0 && treasuryID != "" {
		_ = c.transfer.TransferFromBucket(recipientID, treasuryID, treasuryAmount)
	}
	return fee, burned
}

// Stats returns cumulative fee collection statistics as a point-in-time snapshot.
func (c *Collector) Stats() (collected, burned, treasury uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalCollected, c.totalBurned, c.treasuryAccrued
}
