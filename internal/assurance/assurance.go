// Package assurance implements the AetherNet assurance-lane fee schedule and
// security-floor enforcement. Assurance lanes provide tiered service guarantees
// backed by protocol-level validator coverage.
//
// Three lanes are defined:
//
//	LaneStandard      ("standard")       3 % fee / 2 AET floor
//	LaneHighAssurance ("high_assurance") 6 % fee / 4 AET floor
//	LaneEnterprise    ("enterprise")     8 % fee / 8 AET floor
//
// Tasks that omit the lane (LaneNone / "") are "unassured": no assurance fee
// is charged and generation-ledger credits are not issued for the worker.
package assurance

import (
	"fmt"
	"math"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// AssuranceLane is the service-guarantee tier for a task.
type AssuranceLane string

const (
	// LaneNone means the task is unassured. No assurance fee is charged and
	// the worker does not receive a generation-ledger credit.
	LaneNone AssuranceLane = ""
	// LaneStandard is the entry-tier assurance lane (3 % / 2 AET floor).
	LaneStandard AssuranceLane = "standard"
	// LaneHighAssurance is the mid-tier assurance lane (6 % / 4 AET floor).
	// Requires a structured category (code, data, api, infrastructure).
	LaneHighAssurance AssuranceLane = "high_assurance"
	// LaneEnterprise is the top-tier assurance lane (8 % / 8 AET floor).
	// Requires a structured category and the highest security floor.
	LaneEnterprise AssuranceLane = "enterprise"
)

// AssuranceFeeResult is the outcome of a fee computation or security check.
type AssuranceFeeResult struct {
	// Lane is the assurance lane that applies. May differ from the requested
	// lane when the security floor is not met (downgrade).
	Lane AssuranceLane
	// Fee is the protocol assurance fee in µAET. Zero for LaneNone.
	Fee uint64
	// NetPayout is Budget − Fee; the amount the worker receives upon settlement.
	// Zero for LaneNone.
	NetPayout uint64
}

// ComputeAssuranceFee calculates the assurance protocol fee for the given
// budget and lane using the schedule in cfg.
//
// Returns an error when:
//   - lane is not a recognised AssuranceLane value (other than LaneNone)
//   - budget < cfg.MinTaskBudgetAssured for any non-empty lane
//
// For LaneNone the returned result has zero Fee and NetPayout; no error.
func ComputeAssuranceFee(budget uint64, lane AssuranceLane, cfg *config.AssuranceConfig) (*AssuranceFeeResult, error) {
	if lane == LaneNone {
		return &AssuranceFeeResult{Lane: LaneNone}, nil
	}
	if budget < cfg.MinTaskBudgetAssured {
		return nil, fmt.Errorf("assurance: budget %d µAET is below the minimum %d µAET required for assured tasks",
			budget, cfg.MinTaskBudgetAssured)
	}

	var rate float64
	var floor uint64
	switch lane {
	case LaneStandard:
		rate = cfg.StandardFeeRate
		floor = cfg.StandardFeeFloor
	case LaneHighAssurance:
		rate = cfg.HighAssuranceFeeRate
		floor = cfg.HighAssuranceFeeFloor
	case LaneEnterprise:
		rate = cfg.EnterpriseFeeRate
		floor = cfg.EnterpriseFeeFloor
	default:
		return nil, fmt.Errorf("assurance: unknown lane %q; valid values: standard, high_assurance, enterprise", lane)
	}

	fee := uint64(math.Round(float64(budget) * rate))
	if fee < floor {
		fee = floor
	}
	// Cap fee at budget to prevent underflow in NetPayout.
	if fee > budget {
		fee = budget
	}
	return &AssuranceFeeResult{
		Lane:      lane,
		Fee:       fee,
		NetPayout: budget - fee,
	}, nil
}
