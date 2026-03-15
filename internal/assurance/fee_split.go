// Package assurance — fee_split.go
//
// ComputeFeeSplit divides an assurance fee among verifier, replay reserve /
// executor, and protocol according to the configured shares. ProcessSettlement
// is the single entry point that computes the split and updates the replay
// reserve in one atomic call.
package assurance

import (
	"github.com/Aethernet-network/aethernet/internal/config"
)

// FeeSplitResult holds the full breakdown of a single assurance-fee allocation.
type FeeSplitResult struct {
	// TotalAssuranceFee is the input fee, preserved for cross-checking.
	TotalAssuranceFee uint64
	// VerifierPayout is the µAET allocated to the task verifier.
	VerifierPayout uint64
	// ReplayReservePortion is the µAET directed to the replay reserve when no
	// replay occurred. Zero when ReplayOccurred is true.
	ReplayReservePortion uint64
	// ReplayExecutorPayout is the µAET paid to the replay executor. Zero when
	// ReplayOccurred is false.
	ReplayExecutorPayout uint64
	// ProtocolPortion is the µAET allocated across treasury, dispute reserve,
	// and canary budget.
	ProtocolPortion uint64
	// TreasuryAmount is the protocol portion routed to the treasury.
	TreasuryAmount uint64
	// DisputeReserveAmount is the protocol portion held for dispute resolution.
	DisputeReserveAmount uint64
	// CanaryReserveAmount is the protocol portion allocated to canary measurement.
	CanaryReserveAmount uint64
	// ReplayOccurred records whether the split was computed for a replayed task.
	ReplayOccurred bool
}

// ComputeFeeSplit calculates how an assurance fee should be distributed.
// It does NOT update any balances — call ProcessSettlement for the full
// side-effecting path.
func ComputeFeeSplit(assuranceFee uint64, replayOccurred bool, cfg *config.AssuranceConfig) *FeeSplitResult {
	result := &FeeSplitResult{
		TotalAssuranceFee: assuranceFee,
		ReplayOccurred:    replayOccurred,
	}

	if assuranceFee == 0 {
		return result
	}

	var verifierPayout, otherPortion uint64
	if !replayOccurred {
		verifierPayout = uint64(float64(assuranceFee) * cfg.VerifierShareNoReplay)
		reservePortion := uint64(float64(assuranceFee) * cfg.ReplayReserveShare)
		protocolPortion := assuranceFee - verifierPayout - reservePortion
		result.VerifierPayout = verifierPayout
		result.ReplayReservePortion = reservePortion
		result.ProtocolPortion = protocolPortion
		otherPortion = protocolPortion
	} else {
		verifierPayout = uint64(float64(assuranceFee) * cfg.VerifierShareWithReplay)
		executorPayout := uint64(float64(assuranceFee) * cfg.ReplayExecutorShare)
		protocolPortion := assuranceFee - verifierPayout - executorPayout
		result.VerifierPayout = verifierPayout
		result.ReplayExecutorPayout = executorPayout
		result.ProtocolPortion = protocolPortion
		otherPortion = protocolPortion
	}

	// Split the protocol portion into treasury, dispute, and canary.
	splitProtocol(otherPortion, cfg, result)
	return result
}

// splitProtocol distributes protocolPortion into treasury, dispute, and canary
// sub-allocations, writing the results into r. Any rounding remainder goes to
// treasury to preserve the total.
func splitProtocol(protocolPortion uint64, cfg *config.AssuranceConfig, r *FeeSplitResult) {
	if protocolPortion == 0 {
		return
	}
	treasury := uint64(float64(protocolPortion) * cfg.TreasuryShareOfProtocol)
	dispute := uint64(float64(protocolPortion) * cfg.DisputeShareOfProtocol)
	canary := uint64(float64(protocolPortion) * cfg.CanaryShareOfProtocol)
	// Assign rounding remainder to treasury.
	remainder := protocolPortion - treasury - dispute - canary
	r.TreasuryAmount = treasury + remainder
	r.DisputeReserveAmount = dispute
	r.CanaryReserveAmount = canary
}

// ComputeReplayExecutorPayout calculates the actual payout for a replay
// executor, drawing from the replay reserve when the share-based payout falls
// below MinReplayPayout.
//
// Returns:
//   - executorPayout: total µAET the executor should receive (share + any reserve draw)
//   - reserveDraw: µAET actually drawn from the reserve (0 if share >= minimum)
func ComputeReplayExecutorPayout(assuranceFee uint64, reserve *ReplayReserve, category string, cfg *config.AssuranceConfig) (executorPayout uint64, reserveDraw uint64) {
	basePayout := uint64(float64(assuranceFee) * cfg.ReplayExecutorShare)
	if basePayout >= cfg.MinReplayPayout {
		return basePayout, 0
	}
	shortfall := cfg.MinReplayPayout - basePayout
	drawn, _ := reserve.Draw(category, shortfall)
	return basePayout + drawn, drawn
}

// ProcessSettlement is the single entry point for assurance-fee accounting at
// task settlement time. It computes the fee split and updates the replay
// reserve:
//
//   - No replay: calls reserve.Accrue(category, replayReservePortion)
//   - Replay:    does NOT accrue; executor payout was already drawn when
//                ComputeReplayExecutorPayout was called
//
// Returns the full FeeSplitResult for the caller to distribute tokens.
func ProcessSettlement(assuranceFee uint64, category string, replayOccurred bool, reserve *ReplayReserve, cfg *config.AssuranceConfig) *FeeSplitResult {
	result := ComputeFeeSplit(assuranceFee, replayOccurred, cfg)
	if reserve == nil {
		return result
	}
	if !replayOccurred && result.ReplayReservePortion > 0 {
		// Best-effort: log on failure is handled inside Accrue via store.
		_ = reserve.Accrue(category, result.ReplayReservePortion)
	}
	return result
}
