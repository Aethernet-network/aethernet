package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// deriveDispute implements Plan v3 §2.3 step 6 dispute branch.
// No validator payouts, no gen-ledger; worker portion is split 50/50
// between worker and poster; treasury absorbs the remainder.
//
// Mirrors pre-5B settler integer-path arithmetic at
// internal/settlement/verification_consensus_settler.go:331-334:
//   workerPortion  = budget * WorkerShareBP / SharesDenominator
//   workerAmount   = workerPortion / 2
//   posterAmount   = workerPortion - workerAmount  (absorbs the 50/50 odd cent)
//   treasuryAmount = budget - workerPortion
//
// The 50/50 split absorbs its own rounding into posterAmount; the
// treasuryAmount carries the (validator + generation) pool weight that
// would have been allocated under accept/reject.
func deriveDispute(
	round *taskverification.TaskVerificationRound,
	_ DerivationInputs,
	budget uint64,
	posterID crypto.AgentID,
	fundingRef event.EventID,
	treasuryID crypto.AgentID,
) (records []PayoutRecord, status DerivationStatus, cause DeferredCause, summary partialSummary, err error) {
	workerPortion := budget * WorkerShareBP / SharesDenominator
	workerAmount := workerPortion / 2
	posterAmount := workerPortion - workerAmount
	treasuryAmount := budget - workerPortion

	settlementKey := SettlementKey{
		RoundID:          string(round.RoundID),
		TaskID:           round.TaskID,
		FundingReference: fundingRef,
	}

	if workerAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: round.WorkerID, Role: RoleWorker},
			Amount:            Amount{Value: workerAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagWorkerPayout},
		})
	}
	if posterAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: posterID, Role: RolePosterRefund},
			Amount:            Amount{Value: posterAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagPosterRefund},
		})
	}
	if treasuryAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: treasuryID, Role: RoleTreasury},
			Amount:            Amount{Value: treasuryAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagTreasuryRemainder},
		})
	}

	return records, StatusDerived, 0, summary, nil
}
