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
// **Forward-compat scaffolding (Shape 3, founder direction 2026-04-25)**:
// NOT reachable from F5 5B LK-consumer-driven settlement path.
// DeriveSettlement's terminalVerdict switch only handles
// TerminalAccept | TerminalReject (per the LK consumer's IsComplete
// seal-rule which requires passSealed || failSealed). Round-disputed
// cases (RoundStateDisputed = abstain on deadline expiry without
// supermajority) route through a SEPARATE pipeline:
//
//   1. DeadlineChecker emits TaskVerificationConsensus event with
//      FinalVerdict="abstain" → recognition fabric transitions
//      round.State to RoundStateDisputed
//   2. Dispatcher routes the TVConsensus event to TVConsensusLogicalKeyConsumer
//   3. LK consumer's IsComplete returns false (no seal achieved)
//   4. Apply NEVER fires → DeriveSettlement is NEVER invoked for
//      RoundStateDisputed cases
//   5. Task disputes (poster-initiated, task.Status==TaskStatusDisputed)
//      route through autovalidator.processDisputedTasks via legacy
//      escrow.ReleaseNet (one of the 11 non-settlement
//      CanonicalSyntheticID callers per #134 audit)
//
// deriveDispute is preserved for:
//   - Unit-test coverage of the dispute calculation (Plan v3 §2.3 step 6
//     reference implementation)
//   - Forward-compat scaffolding for a future workstream that wires
//     round-disputed cases into the canonical settlement path. When
//     that ships, DeriveSettlement's switch + the LK consumer's
//     Outcome.Verdict will both extend to carry a Dispute variant; this
//     function then becomes reachable through the canonical path
//     without arithmetic changes.
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
