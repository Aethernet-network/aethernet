// supply.go — Currency supply algorithm for the AetherNet dual-ledger system.
//
// AetherNet's money supply is not fixed at genesis nor inflated on a
// predetermined schedule. Instead it expands in direct proportion to verified
// economic activity: new AET enters the system only when validators confirm that
// real AI computation produced it. This ties monetary expansion to productive
// output rather than the passage of time or miner luck.
//
// The expansion window is a rolling 30-day view of TotalVerifiedValue from the
// Generation Ledger. The supply "breathes" — contracting naturally if generation
// activity falls and expanding up to a hard cap of 10× BaseSupply as the network
// scales.
package ledger

import (
	"fmt"
	"time"
)

// Supply constants define the monetary bounds of the AetherNet economy.
const (
	// BaseSupply is the minimum circulating supply, present from genesis,
	// denominated in micro-AET (1 AET = 1,000,000 micro-AET).
	BaseSupply uint64 = 1_000_000_000_000_000

	// MaxSupplyMultiplier caps how far verified generation can expand the supply
	// relative to BaseSupply. A multiplier of 10 means the total supply can
	// reach at most 10 quadrillion micro-AET (10 billion AET) before the cap applies.
	MaxSupplyMultiplier uint64 = 10

	// MeasurementWindow is the trailing duration over which TotalVerifiedValue
	// is summed to derive the current expansion amount. A 30-day window smooths
	// short-term spikes while remaining responsive to sustained shifts in
	// network-wide AI compute output.
	MeasurementWindow = 30 * 24 * time.Hour
)

// SupplyHealth is a point-in-time snapshot of the currency supply state.
// All fields are derived consistently from the same underlying ledger reads.
type SupplyHealth struct {
	// CurrentSupply is BaseSupply plus any expansion from verified generation,
	// capped at BaseSupply × MaxSupplyMultiplier.
	CurrentSupply uint64

	// BaseSupply is the genesis floor supply, equal to the package constant.
	BaseSupply uint64

	// ExpansionAmount is the verified-generation contribution to supply above
	// BaseSupply at the moment of the snapshot.
	ExpansionAmount uint64

	// SupplyRatio is CurrentSupply / BaseSupply as a float64. A value of 1.0
	// means no expansion has occurred; 10.0 means the supply cap has been reached.
	SupplyRatio float64

	// VerifiedGeneration is the raw TotalVerifiedValue over MeasurementWindow
	// before the expansion cap is applied.
	VerifiedGeneration uint64

	// MeasurementWindow echoes the rolling window used for this snapshot.
	MeasurementWindow time.Duration

	// AtCap is true when CurrentSupply has reached BaseSupply × MaxSupplyMultiplier.
	// Further verified generation does not increase supply while AtCap is true.
	AtCap bool

	// Timestamp is the wall-clock time at which HealthMetrics was called.
	Timestamp time.Time
}

// SupplyManager connects the Transfer and Generation ledgers to implement the
// supply algorithm. It is stateless beyond the ledger references; all supply
// values are derived on demand from live ledger state rather than cached.
// SupplyManager is safe for concurrent use — it delegates locking to the
// underlying ledgers.
type SupplyManager struct {
	transfer   *TransferLedger
	generation *GenerationLedger
}

// NewSupplyManager returns a SupplyManager backed by the provided ledgers.
// Both ledgers must be non-nil and fully initialised before use.
func NewSupplyManager(tl *TransferLedger, gl *GenerationLedger) *SupplyManager {
	return &SupplyManager{
		transfer:   tl,
		generation: gl,
	}
}

// ExpansionFromGeneration returns the amount by which verified AI generation has
// expanded the supply above BaseSupply over the trailing MeasurementWindow.
//
// The raw TotalVerifiedValue is capped at (BaseSupply × MaxSupplyMultiplier) − 1
// to prevent integer overflow when added to BaseSupply in CurrentSupply and to
// bound expansion to the maximum meaningful range.
func (sm *SupplyManager) ExpansionFromGeneration() (uint64, error) {
	verified, err := sm.generation.TotalVerifiedValue(MeasurementWindow)
	if err != nil {
		return 0, fmt.Errorf("ledger: supply expansion query failed: %w", err)
	}

	cap := BaseSupply*MaxSupplyMultiplier - 1
	if verified > cap {
		return cap, nil
	}
	return verified, nil
}

// CurrentSupply returns the total circulating supply: BaseSupply plus the
// expansion earned by verified generation over the MeasurementWindow.
//
// The result is capped at BaseSupply × MaxSupplyMultiplier. The supply
// floor is always BaseSupply — it never falls below genesis issuance.
func (sm *SupplyManager) CurrentSupply() (uint64, error) {
	expansion, err := sm.ExpansionFromGeneration()
	if err != nil {
		return 0, err
	}

	supply := BaseSupply + expansion
	maxSupply := BaseSupply * MaxSupplyMultiplier
	if supply > maxSupply {
		supply = maxSupply
	}
	return supply, nil
}

// SupplyRatio returns CurrentSupply divided by BaseSupply as a float64.
//
// 1.0 indicates no expansion (only genesis supply circulates).
// 10.0 indicates the supply cap has been reached.
// Values between reflect proportional expansion from verified AI generation.
func (sm *SupplyManager) SupplyRatio() (float64, error) {
	current, err := sm.CurrentSupply()
	if err != nil {
		return 0, err
	}
	return float64(current) / float64(BaseSupply), nil
}

// HealthMetrics returns a full point-in-time snapshot of the supply state.
// All fields are derived from a single logical read so they are mutually
// consistent, though the underlying ledgers are not locked across the whole
// call — individual ledger operations remain independently atomic.
func (sm *SupplyManager) HealthMetrics() (*SupplyHealth, error) {
	// Read raw verified generation before the expansion cap is applied so it
	// can be reported independently in the snapshot.
	verified, err := sm.generation.TotalVerifiedValue(MeasurementWindow)
	if err != nil {
		return nil, fmt.Errorf("ledger: health metrics query failed: %w", err)
	}

	expansionCap := BaseSupply*MaxSupplyMultiplier - 1
	expansion := verified
	if expansion > expansionCap {
		expansion = expansionCap
	}

	maxSupply := BaseSupply * MaxSupplyMultiplier
	current := BaseSupply + expansion
	if current > maxSupply {
		current = maxSupply
	}

	return &SupplyHealth{
		CurrentSupply:      current,
		BaseSupply:         BaseSupply,
		ExpansionAmount:    current - BaseSupply,
		SupplyRatio:        float64(current) / float64(BaseSupply),
		VerifiedGeneration: verified,
		MeasurementWindow:  MeasurementWindow,
		AtCap:              current >= maxSupply,
		Timestamp:          time.Now(),
	}, nil
}
