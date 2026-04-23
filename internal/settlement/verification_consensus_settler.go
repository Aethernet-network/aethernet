package settlement

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// v4.1 economic model: locked fee split (basis points out of 10000).
const (
	workerShareBP     = 7300 // 73%
	validatorShareBP  = 2300 // 23%
	generationShareBP = 200  // 2%
	treasuryShareBP   = 200  // 2%
)

// VerificationConsensusSettler processes TaskVerificationConsensus events
// and applies the v4.1 economic distribution: 73/23/2/2 on accept, full
// refund on reject, 50/50 on dispute.
// ValidatorQScoreFn returns the Quality Score Q for a validator in the
// context of a specific family and category. Used for Q-weighted fee
// distribution. Returns 1.0 for neutral (new validators with no history).
type ValidatorQScoreFn func(validatorID crypto.AgentID, family string, category string) float64

type VerificationConsensusSettler struct {
	taskMgr    *tasks.TaskManager
	transfer   *ledger.TransferLedger
	escrowMgr  *escrow.Escrow
	dagScanner DAGScanner // for LookupEscrowLockTransfer on catch-up
	genLedger  *GenerationLedgerCalculator
	treasuryID crypto.AgentID
	qScoreFn   ValidatorQScoreFn // nil → even-split fallback
}

// NewVerificationConsensusSettler creates a settler with the full v4.1
// economic model. qScoreFn may be nil for even-split fallback.
//
// dagScanner is used on the escrow catch-up path (peer node scenario) to
// locate the canonical escrow-lock Transfer event that funds the escrow.
// May be nil only in tests that do not exercise the catch-up path.
func NewVerificationConsensusSettler(
	taskMgr *tasks.TaskManager,
	transfer *ledger.TransferLedger,
	escrowMgr *escrow.Escrow,
	dagScanner DAGScanner,
	genLedger *GenerationLedgerCalculator,
	treasuryID crypto.AgentID,
	qScoreFn ValidatorQScoreFn,
) *VerificationConsensusSettler {
	return &VerificationConsensusSettler{
		taskMgr:    taskMgr,
		transfer:   transfer,
		escrowMgr:  escrowMgr,
		dagScanner: dagScanner,
		genLedger:  genLedger,
		treasuryID: treasuryID,
		qScoreFn:   qScoreFn,
	}
}

// SettleResult captures the outcome of a settlement application.
type SettleResult struct {
	Applied              bool
	AlreadyApplied       bool
	Verdict              string
	WorkerPayout         uint64
	PosterRefund         uint64
	ValidatorPayouts     map[crypto.AgentID]uint64
	GenerationLedger     GenerationLedgerDistribution
	TreasuryAmount       uint64
	TotalDistributed     uint64
}

// Settle applies the v4.1 economic distribution for a verification consensus.
// Idempotent: tasks already in a terminal state produce AlreadyApplied=true.
func (s *VerificationConsensusSettler) Settle(
	_ context.Context,
	payload *event.TaskVerificationConsensusPayload,
	round *taskverification.TaskVerificationRound,
) (SettleResult, error) {
	result := SettleResult{Verdict: payload.FinalVerdict}

	// Look up the task.
	task, err := s.taskMgr.Get(payload.TaskID)
	if err != nil {
		return result, fmt.Errorf("verification_settler: task %s not found: %w", payload.TaskID, err)
	}

	// Idempotent: already in terminal state.
	switch task.Status {
	case tasks.TaskStatusCompleted, tasks.TaskStatusRejected,
		tasks.TaskStatusDisputedResolved, tasks.TaskStatusCancelled:
		result.AlreadyApplied = true
		return result, nil
	}

	// Get escrowed budget.
	entry, err := s.escrowMgr.Get(payload.TaskID)
	if err != nil {
		// If escrow is not locked, try to catch up (peer node scenario).
		// The canonical escrow-lock Transfer has already moved funds; we only
		// need to register metadata linking the escrow entry to that Transfer.
		if !s.escrowMgr.IsLocked(payload.TaskID) {
			if s.dagScanner == nil {
				return result, fmt.Errorf("verification_settler: escrow catch-up required but dagScanner not configured for task %s", payload.TaskID)
			}
			fundingRef, lookupErr := LookupEscrowLockTransfer(
				s.dagScanner, payload.TaskID, crypto.AgentID(task.PosterID), task.Budget)
			if lookupErr != nil {
				return result, fmt.Errorf("verification_settler: escrow catch-up funding-transfer lookup failed: %w", lookupErr)
			}
			if regErr := s.escrowMgr.RegisterEscrow(payload.TaskID, crypto.AgentID(task.PosterID), task.Budget, fundingRef); regErr != nil {
				return result, fmt.Errorf("verification_settler: escrow catch-up register failed: %w", regErr)
			}
			entry, err = s.escrowMgr.Get(payload.TaskID)
			if err != nil {
				return result, fmt.Errorf("verification_settler: escrow still missing after catch-up: %w", err)
			}
		} else {
			return result, fmt.Errorf("verification_settler: escrow get failed: %w", err)
		}
	}

	budget := entry.Amount
	escrowBucket := crypto.AgentID("escrow:" + payload.TaskID)
	workerID := crypto.AgentID(payload.WorkerID)
	posterID := crypto.AgentID(payload.PosterID)

	switch payload.FinalVerdict {
	case "pass":
		return s.settleAccept(budget, escrowBucket, workerID, posterID, payload, round, result)
	case "fail":
		return s.settleReject(budget, escrowBucket, posterID, payload, round, result)
	case "abstain":
		return s.settleDispute(budget, escrowBucket, workerID, posterID, payload, result)
	default:
		return result, fmt.Errorf("verification_settler: unknown verdict %q", payload.FinalVerdict)
	}
}

// settleAccept distributes the task budget per the v4.1 model:
// worker: 73%, validators (Q-weighted): 23%, gen-ledger: 2%, treasury: 2%.
// Integer arithmetic remainders from the share calculations are routed to
// treasury so the sum across all recipients equals the budget exactly.
// Cross-node conservation is verified by §10 success criterion 6.
func (s *VerificationConsensusSettler) settleAccept(
	budget uint64,
	escrowBucket, workerID, posterID crypto.AgentID,
	payload *event.TaskVerificationConsensusPayload,
	round *taskverification.TaskVerificationRound,
	result SettleResult,
) (SettleResult, error) {
	workerAmount := budget * workerShareBP / 10000
	validatorPool := budget * validatorShareBP / 10000
	genPool := budget * generationShareBP / 10000
	treasuryAmount := budget - workerAmount - validatorPool - genPool // absorbs rounding

	// Compute per-validator Q-weighted payouts without executing transfers.
	agreeing := collectAgreeingValidators(round, taskverification.VerdictPass)
	validators := make(map[crypto.AgentID]uint64)
	if len(agreeing) == 0 {
		slog.Warn("verification_settler: no agreeing validators on accept — routing validator pool to treasury",
			"task_id", payload.TaskID)
		treasuryAmount += validatorPool
	} else {
		validators = s.computeValidatorPayouts(agreeing, validatorPool, round.Category)
		result.ValidatorPayouts = validators
	}

	// Compute gen-ledger royalty payouts without executing transfers.
	genRecipients := make(map[crypto.AgentID]uint64)
	if s.genLedger != nil {
		dist := s.genLedger.Calculate(event.EventID(payload.SubmissionEventID), genPool)
		for _, r := range dist.Recipients {
			if r.Amount > 0 {
				genRecipients[crypto.AgentID(r.AgentID)] = r.Amount
			}
		}
		treasuryAmount += dist.Treasury
		result.GenerationLedger = dist
	} else {
		treasuryAmount += genPool
	}

	// Execute all transfers via ReleaseSettlement with per-recipient
	// paid-flag idempotency guards (C-11).
	if err := s.escrowMgr.ReleaseSettlement(
		payload.TaskID,
		workerID, workerAmount,
		posterID, 0,
		validators,
		genRecipients,
		s.treasuryID, treasuryAmount,
	); err != nil {
		return result, fmt.Errorf("verification_settler: release settlement: %w", err)
	}

	result.WorkerPayout = workerAmount
	result.TreasuryAmount = treasuryAmount

	// Task state transition — last step; if we reach here, all payouts
	// completed (or were already completed on a prior attempt via paid flags).
	if err := s.taskMgr.ApplyVerificationConsensusResolution(
		payload.TaskID, "pass", payload.FinalScoreBP, payload.RoundID); err != nil {
		return result, fmt.Errorf("verification_settler: task state: %w", err)
	}
	result.Applied = true
	result.TotalDistributed = workerAmount + sumPayouts(validators) +
		sumGenLedger(result.GenerationLedger) + treasuryAmount

	slog.Info("verification_settler: accept settled",
		"task_id", payload.TaskID, "budget", budget,
		"worker", workerAmount, "validator_pool", validatorPool,
		"gen_ledger", genPool, "treasury", treasuryAmount,
		"agreeing_validators", len(agreeing))
	return result, nil
}

func (s *VerificationConsensusSettler) settleReject(
	budget uint64,
	escrowBucket, posterID crypto.AgentID,
	payload *event.TaskVerificationConsensusPayload,
	round *taskverification.TaskVerificationRound,
	result SettleResult,
) (SettleResult, error) {
	posterAmount := budget * workerShareBP / 10000
	validatorPool := budget * validatorShareBP / 10000
	treasuryAmount := budget - posterAmount - validatorPool

	agreeing := collectAgreeingValidators(round, taskverification.VerdictFail)
	validators := make(map[crypto.AgentID]uint64)
	if len(agreeing) == 0 {
		slog.Warn("verification_settler: no agreeing validators on reject — routing to treasury",
			"task_id", payload.TaskID)
		treasuryAmount += validatorPool
	} else {
		validators = s.computeValidatorPayouts(agreeing, validatorPool, round.Category)
		result.ValidatorPayouts = validators
	}

	if err := s.escrowMgr.ReleaseSettlement(
		payload.TaskID,
		crypto.AgentID(""), 0, // no worker payout on reject
		posterID, posterAmount,
		validators,
		nil, // no gen-ledger on reject
		s.treasuryID, treasuryAmount,
	); err != nil {
		return result, fmt.Errorf("verification_settler: release settlement: %w", err)
	}

	result.PosterRefund = posterAmount
	result.TreasuryAmount = treasuryAmount

	if err := s.taskMgr.ApplyVerificationConsensusResolution(
		payload.TaskID, "fail", 0, payload.RoundID); err != nil {
		return result, fmt.Errorf("verification_settler: task state: %w", err)
	}
	result.Applied = true
	result.TotalDistributed = posterAmount + sumPayouts(validators) + treasuryAmount

	slog.Info("verification_settler: reject settled",
		"task_id", payload.TaskID, "budget", budget,
		"poster", posterAmount, "treasury", treasuryAmount,
		"agreeing_validators", len(agreeing))
	return result, nil
}

func (s *VerificationConsensusSettler) settleDispute(
	budget uint64,
	escrowBucket, workerID, posterID crypto.AgentID,
	payload *event.TaskVerificationConsensusPayload,
	result SettleResult,
) (SettleResult, error) {
	workerPortion := budget * workerShareBP / 10000
	workerAmount := workerPortion / 2
	posterAmount := workerPortion - workerAmount
	treasuryAmount := budget - workerPortion

	if err := s.escrowMgr.ReleaseSettlement(
		payload.TaskID,
		workerID, workerAmount,
		posterID, posterAmount,
		nil, // no validator payouts on dispute
		nil, // no gen-ledger on dispute
		s.treasuryID, treasuryAmount,
	); err != nil {
		return result, fmt.Errorf("verification_settler: release settlement: %w", err)
	}

	result.WorkerPayout = workerAmount
	result.PosterRefund = posterAmount
	result.TreasuryAmount = treasuryAmount

	if err := s.taskMgr.ApplyVerificationConsensusResolution(
		payload.TaskID, "abstain", 0, payload.RoundID); err != nil {
		return result, fmt.Errorf("verification_settler: task state: %w", err)
	}
	result.Applied = true
	result.TotalDistributed = workerAmount + posterAmount + treasuryAmount

	slog.Info("verification_settler: dispute settled",
		"task_id", payload.TaskID, "budget", budget,
		"worker", workerAmount, "poster", posterAmount,
		"treasury", treasuryAmount)
	return result, nil
}

// collectAgreeingValidators returns the AgentIDs of validators whose vote
// matches the given verdict.
func collectAgreeingValidators(round *taskverification.TaskVerificationRound, verdict taskverification.Verdict) []crypto.AgentID {
	seen := make(map[crypto.AgentID]struct{})
	var result []crypto.AgentID
	for _, v := range round.Votes {
		if v.Verdict == verdict {
			if _, ok := seen[v.ValidatorID]; !ok {
				seen[v.ValidatorID] = struct{}{}
				result = append(result, v.ValidatorID)
			}
		}
	}
	return result
}

// distributeByQuality splits a pool among recipients weighted by their
// Quality Score Q. Falls back to even-split when qScoreFn is nil or when
// all Q scores sum to zero.
// computeValidatorPayouts calculates Q-weighted per-validator amounts
// without executing transfers. The returned map is passed to
// ReleaseSettlement which handles the actual transfers with per-recipient
// paid-flag idempotency. Integer arithmetic remainder goes to the last
// validator for determinism.
func (s *VerificationConsensusSettler) computeValidatorPayouts(
	recipients []crypto.AgentID,
	pool uint64,
	category string,
) map[crypto.AgentID]uint64 {
	payouts := make(map[crypto.AgentID]uint64)
	if len(recipients) == 0 || pool == 0 {
		return payouts
	}

	type scored struct {
		id crypto.AgentID
		q  float64
	}
	entries := make([]scored, len(recipients))
	var totalQ float64
	for i, v := range recipients {
		q := 1.0
		if s.qScoreFn != nil {
			q = s.qScoreFn(v, "", category)
		}
		entries[i] = scored{id: v, q: q}
		totalQ += q
	}

	if totalQ == 0 {
		perValidator := pool / uint64(len(recipients))
		var distributed uint64
		for i, v := range recipients {
			amount := perValidator
			if i == len(recipients)-1 {
				amount = pool - distributed
			}
			if amount > 0 {
				payouts[v] = amount
			}
			distributed += amount
		}
		return payouts
	}

	var distributed uint64
	for i, e := range entries {
		var amount uint64
		if i == len(entries)-1 {
			amount = pool - distributed
		} else {
			amount = uint64(float64(pool) * (e.q / totalQ))
		}
		if amount > 0 {
			payouts[e.id] = amount
		}
		distributed += amount
	}
	return payouts
}

func sumPayouts(m map[crypto.AgentID]uint64) uint64 {
	var s uint64
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, v := range m {
		s += v
	}
	return s
}

func sumGenLedger(d GenerationLedgerDistribution) uint64 {
	var s uint64
	for _, r := range d.Recipients {
		s += r.Amount
	}
	return s
}
