// supply.go — Fixed-supply tokenomics for the AetherNet economy.
//
// AetherNet has a fixed token supply of 1 billion AET (1 quadrillion micro-AET)
// set at genesis. The supply does not expand based on verified generation.
// Generated value measures the economic output of AI work verified on the
// network — it is an accounting and measurement metric, not new token issuance.
//
// The supply ratio is always exactly 1.0 (or fractionally below if tokens
// have been burned via fee collection).
package ledger

import "time"

// Supply constants define the monetary bounds of the AetherNet economy.
const (
	// BaseSupply is the fixed circulating supply, present from genesis,
	// denominated in micro-AET (1 AET = 1,000,000 micro-AET).
	BaseSupply uint64 = 1_000_000_000_000_000
)

// SupplyHealth is a point-in-time snapshot of the currency supply state.
type SupplyHealth struct {
	// CurrentSupply is the fixed genesis supply (BaseSupply).
	CurrentSupply uint64

	// BaseSupply is the genesis supply constant.
	BaseSupply uint64

	// ExpansionAmount is always 0 — fixed supply, no expansion.
	ExpansionAmount uint64

	// SupplyRatio is CurrentSupply / BaseSupply — always 1.0.
	SupplyRatio float64

	// VerifiedGeneration is the total verified AI work value. Reported as
	// a network activity metric, not added to supply.
	VerifiedGeneration uint64

	// MeasurementWindow is the window over which VerifiedGeneration is measured.
	MeasurementWindow time.Duration

	// AtCap is always false — no cap concept in fixed supply.
	AtCap bool

	// Timestamp is the wall-clock time at which HealthMetrics was called.
	Timestamp time.Time
}

// SupplyManager connects the Transfer and Generation ledgers to report
// supply state. The supply is fixed at BaseSupply — generation activity is
// reported as a separate metric but does not expand the supply.
type SupplyManager struct {
	transfer   *TransferLedger
	generation *GenerationLedger
}

// NewSupplyManager returns a SupplyManager backed by the provided ledgers.
func NewSupplyManager(tl *TransferLedger, gl *GenerationLedger) *SupplyManager {
	return &SupplyManager{
		transfer:   tl,
		generation: gl,
	}
}

// CurrentSupply returns the fixed supply. In a fixed-supply model this is
// always BaseSupply.
func (sm *SupplyManager) CurrentSupply() (uint64, error) {
	return BaseSupply, nil
}

// SupplyRatio returns CurrentSupply / BaseSupply — always exactly 1.0.
func (sm *SupplyManager) SupplyRatio() (float64, error) {
	return 1.0, nil
}

// HealthMetrics returns a point-in-time snapshot of the supply state.
// VerifiedGeneration is reported as a network activity metric.
func (sm *SupplyManager) HealthMetrics() (*SupplyHealth, error) {
	verified, err := sm.generation.TotalVerifiedValue(30 * 24 * time.Hour)
	if err != nil {
		verified = 0 // non-fatal — report what we can
	}

	return &SupplyHealth{
		CurrentSupply:      BaseSupply,
		BaseSupply:         BaseSupply,
		ExpansionAmount:    0,
		SupplyRatio:        1.0,
		VerifiedGeneration: verified,
		MeasurementWindow:  30 * 24 * time.Hour,
		AtCap:              false,
		Timestamp:          time.Now(),
	}, nil
}
