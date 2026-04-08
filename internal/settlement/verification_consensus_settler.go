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
type VerificationConsensusSettler struct {
	taskMgr     *tasks.TaskManager
	transfer    *ledger.TransferLedger
	escrowMgr   *escrow.Escrow
	genLedger   *GenerationLedgerCalculator
	treasuryID  crypto.AgentID
}

// NewVerificationConsensusSettler creates a settler with the full v4.1
// economic model.
func NewVerificationConsensusSettler(
	taskMgr *tasks.TaskManager,
	transfer *ledger.TransferLedger,
	escrowMgr *escrow.Escrow,
	genLedger *GenerationLedgerCalculator,
	treasuryID crypto.AgentID,
) *VerificationConsensusSettler {
	return &VerificationConsensusSettler{
		taskMgr:    taskMgr,
		transfer:   transfer,
		escrowMgr:  escrowMgr,
		genLedger:  genLedger,
		treasuryID: treasuryID,
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
		if !s.escrowMgr.IsLocked(payload.TaskID) {
			if holdErr := s.escrowMgr.Hold(payload.TaskID, crypto.AgentID(task.PosterID), task.Budget); holdErr != nil {
				return result, fmt.Errorf("verification_settler: escrow catch-up failed: %w", holdErr)
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

	// 1. Worker payout.
	if err := s.transfer.TransferFromBucket(escrowBucket, workerID, workerAmount); err != nil {
		return result, fmt.Errorf("verification_settler: worker transfer: %w", err)
	}
	result.WorkerPayout = workerAmount

	// 2. Validator payouts: even-split among consensus-agreeing validators.
	// TODO prompt 08: replace even-split with Quality-Score-weighted distribution.
	// Weight each validator by their Quality Score Q for this category/family,
	// as computed by the reputation store landed in prompt 08.
	agreeing := collectAgreeingValidators(round, taskverification.VerdictPass)
	if len(agreeing) == 0 {
		slog.Warn("verification_settler: no agreeing validators on accept — routing validator pool to treasury",
			"task_id", payload.TaskID)
		treasuryAmount += validatorPool
	} else {
		result.ValidatorPayouts = distributeEvenly(s.transfer, escrowBucket, agreeing, validatorPool)
	}

	// 3. Generation Ledger royalties (2% on accept only).
	if s.genLedger != nil {
		dist := s.genLedger.Calculate(event.EventID(payload.SubmissionEventID), genPool)
		for _, r := range dist.Recipients {
			if r.Amount > 0 {
				_ = s.transfer.TransferFromBucket(escrowBucket, crypto.AgentID(r.AgentID), r.Amount)
			}
		}
		treasuryAmount += dist.Treasury
		result.GenerationLedger = dist
	} else {
		treasuryAmount += genPool
	}

	// 4. Treasury.
	if treasuryAmount > 0 {
		if err := s.transfer.TransferFromBucket(escrowBucket, s.treasuryID, treasuryAmount); err != nil {
			return result, fmt.Errorf("verification_settler: treasury transfer: %w", err)
		}
	}
	result.TreasuryAmount = treasuryAmount

	// 5. Task state transition.
	if err := s.taskMgr.ApplyVerificationConsensusResolution(
		payload.TaskID, "pass", payload.FinalScoreBP, payload.RoundID); err != nil {
		return result, fmt.Errorf("verification_settler: task state: %w", err)
	}
	result.Applied = true
	result.TotalDistributed = workerAmount + sumPayouts(result.ValidatorPayouts) +
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
	// 4% to treasury: 2% protocol + 2% redirected Generation Ledger
	treasuryAmount := budget - posterAmount - validatorPool

	// 1. Poster refund (73% portion).
	if err := s.transfer.TransferFromBucket(escrowBucket, posterID, posterAmount); err != nil {
		return result, fmt.Errorf("verification_settler: poster refund: %w", err)
	}
	result.PosterRefund = posterAmount

	// 2. Validator payouts: consensus-agreeing (fail-voting) validators.
	agreeing := collectAgreeingValidators(round, taskverification.VerdictFail)
	if len(agreeing) == 0 {
		slog.Warn("verification_settler: no agreeing validators on reject — routing to treasury",
			"task_id", payload.TaskID)
		treasuryAmount += validatorPool
	} else {
		result.ValidatorPayouts = distributeEvenly(s.transfer, escrowBucket, agreeing, validatorPool)
	}

	// 3. Treasury (protocol fee + redirected Generation Ledger).
	if treasuryAmount > 0 {
		if err := s.transfer.TransferFromBucket(escrowBucket, s.treasuryID, treasuryAmount); err != nil {
			return result, fmt.Errorf("verification_settler: treasury transfer: %w", err)
		}
	}
	result.TreasuryAmount = treasuryAmount

	// 4. Task state.
	if err := s.taskMgr.ApplyVerificationConsensusResolution(
		payload.TaskID, "fail", 0, payload.RoundID); err != nil {
		return result, fmt.Errorf("verification_settler: task state: %w", err)
	}
	result.Applied = true
	result.TotalDistributed = posterAmount + sumPayouts(result.ValidatorPayouts) + treasuryAmount

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
	// 50/50 split of the 73% worker portion. Extra micro-AET to poster on odd amounts.
	workerPortion := budget * workerShareBP / 10000
	workerAmount := workerPortion / 2
	posterAmount := workerPortion - workerAmount // poster gets the extra on odd splits
	// 27% to treasury: 23% validator + 2% Generation Ledger + 2% protocol
	treasuryAmount := budget - workerPortion

	// TODO post-prompt-08: Consider Quality-weighted dispute resolution where the 73% worker
	// portion split is weighted by the worker's historical Quality Score. A worker with a
	// history of high-quality work being disputed may deserve closer to 70% of the worker
	// portion; a worker with borderline history may deserve closer to 30%. Requires the
	// reputation/Quality Score infrastructure from prompt 08.

	// 1. Worker half.
	if workerAmount > 0 {
		if err := s.transfer.TransferFromBucket(escrowBucket, workerID, workerAmount); err != nil {
			return result, fmt.Errorf("verification_settler: worker dispute transfer: %w", err)
		}
	}
	result.WorkerPayout = workerAmount

	// 2. Poster half.
	if posterAmount > 0 {
		if err := s.transfer.TransferFromBucket(escrowBucket, posterID, posterAmount); err != nil {
			return result, fmt.Errorf("verification_settler: poster dispute transfer: %w", err)
		}
	}
	result.PosterRefund = posterAmount

	// 3. Treasury (all non-worker portions redirected).
	if treasuryAmount > 0 {
		if err := s.transfer.TransferFromBucket(escrowBucket, s.treasuryID, treasuryAmount); err != nil {
			return result, fmt.Errorf("verification_settler: treasury dispute transfer: %w", err)
		}
	}
	result.TreasuryAmount = treasuryAmount

	// 4. Task state.
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

// distributeEvenly splits a pool among recipients. Remainder goes to the last.
func distributeEvenly(
	transfer *ledger.TransferLedger,
	from crypto.AgentID,
	recipients []crypto.AgentID,
	pool uint64,
) map[crypto.AgentID]uint64 {
	payouts := make(map[crypto.AgentID]uint64)
	if len(recipients) == 0 || pool == 0 {
		return payouts
	}
	perValidator := pool / uint64(len(recipients))
	var distributed uint64
	for i, v := range recipients {
		amount := perValidator
		if i == len(recipients)-1 {
			amount = pool - distributed // last gets remainder
		}
		if amount > 0 {
			_ = transfer.TransferFromBucket(from, v, amount)
			payouts[v] = amount
		}
		distributed += amount
	}
	return payouts
}

func sumPayouts(m map[crypto.AgentID]uint64) uint64 {
	var s uint64
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
