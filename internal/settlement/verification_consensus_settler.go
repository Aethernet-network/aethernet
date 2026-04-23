package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
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
// distribution. Returns NeutralBP (10000, equivalent to the prior 1.0
// float) for new validators with no history.
//
// Return type migrated from float64 to protocolmath.BasisPoints in the
// canonical-distribution-integer-migration workstream (commit-3). Callers
// from the reputation store return BasisPoints natively; external
// callers that previously produced float values should scale by 10000
// and clamp to MaxBasisPoints.
type ValidatorQScoreFn func(validatorID crypto.AgentID, family string, category string) protocolmath.BasisPoints

type VerificationConsensusSettler struct {
	taskMgr    *tasks.TaskManager
	transfer   *ledger.TransferLedger
	escrowMgr  *escrow.Escrow
	dagScanner DAGScanner // for LookupEscrowLockTransfer on catch-up
	genLedger  *GenerationLedgerCalculator
	treasuryID crypto.AgentID
	qScoreFn   ValidatorQScoreFn // nil → even-split fallback

	// mu guards shadowMode. Reads happen on every settlement call (via
	// isShadowMode); writes happen rarely (once per IntegerMigration
	// activation event, via SetShadowMode). sync.RWMutex amortizes the
	// read cost; see Part E of the canonical-distribution-integer-migration
	// workstream for the activation consumer that drives the write.
	mu         sync.RWMutex
	shadowMode bool
}

// isShadowMode returns the current shadow-mode flag under RLock. Called
// on every settlement path.
func (s *VerificationConsensusSettler) isShadowMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shadowMode
}

// SetShadowMode switches the settler between shadow mode (legacy float
// path is canonical, integer path runs alongside for delta logging) and
// integer-canonical mode (integer path is canonical; legacy float path
// is not run). Called by the IntegerMigrationActivationConsumer when
// the activation event is applied, and at startup when a prior
// activation is loaded from the store.
//
// Thread-safe. One-way transition is the protocol-level expectation,
// but SetShadowMode itself accepts either value so tests can restore
// shadow mode after exercising the integer path in isolation.
func (s *VerificationConsensusSettler) SetShadowMode(shadow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shadowMode = shadow
}

// NewVerificationConsensusSettler creates a settler with the full v4.1
// economic model. qScoreFn may be nil for even-split fallback.
//
// dagScanner is used on the escrow catch-up path (peer node scenario) to
// locate the canonical escrow-lock Transfer event that funds the escrow.
// May be nil only in tests that do not exercise the catch-up path.
//
// shadowMode: pass true for Part B's shadow-compare behavior. Part E's
// activation consumer flips this to false at runtime when the
// EventTypeIntegerMigrationActivation event is projected, and startup
// restores it to false on a post-activation restart via the adapter in
// cmd/node/main.go.
func NewVerificationConsensusSettler(
	taskMgr *tasks.TaskManager,
	transfer *ledger.TransferLedger,
	escrowMgr *escrow.Escrow,
	dagScanner DAGScanner,
	genLedger *GenerationLedgerCalculator,
	treasuryID crypto.AgentID,
	qScoreFn ValidatorQScoreFn,
	shadowMode bool,
) *VerificationConsensusSettler {
	return &VerificationConsensusSettler{
		taskMgr:    taskMgr,
		transfer:   transfer,
		escrowMgr:  escrowMgr,
		dagScanner: dagScanner,
		genLedger:  genLedger,
		treasuryID: treasuryID,
		qScoreFn:   qScoreFn,
		shadowMode: shadowMode,
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
		validators = s.computeValidatorPayouts(payload.TaskID, agreeing, validatorPool, round.Category)
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
		validators = s.computeValidatorPayouts(payload.TaskID, agreeing, validatorPool, round.Category)
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

// computeValidatorPayouts calculates Q-weighted per-validator amounts
// without executing transfers. The returned map is passed to
// ReleaseSettlement which handles the actual transfers with per-recipient
// paid-flag idempotency.
//
// This is the shadow-gated entry point introduced in the canonical-
// distribution-integer-migration workstream. When shadowMode is true, the
// legacy float path (computeValidatorPayoutsFloat) is canonical; the
// integer path (computeValidatorPayoutsInteger) runs alongside and the
// per-recipient delta is logged via shadow_delta. When shadowMode is
// false (Part E and beyond), the integer path is the only path that runs.
//
// taskID is threaded in purely so the shadow_delta log line can identify
// which settlement the delta belongs to; it has no arithmetic effect.
func (s *VerificationConsensusSettler) computeValidatorPayouts(
	taskID string,
	recipients []crypto.AgentID,
	pool uint64,
	category string,
) map[crypto.AgentID]uint64 {
	if s.isShadowMode() {
		floatResult := s.computeValidatorPayoutsFloat(recipients, pool, category)
		intResult := s.computeValidatorPayoutsInteger(recipients, pool, category)
		s.logShadowDelta("validator_distribution", taskID, recipients, floatResult, intResult, pool)
		return floatResult
	}
	return s.computeValidatorPayoutsInteger(recipients, pool, category)
}

// computeValidatorPayoutsFloat is the legacy float-arithmetic payout
// computation, preserved verbatim from the pre-migration codebase. While
// shadowMode remains true on every node, this is the canonical path: its
// output is what the settler returns and what ReleaseSettlement applies.
// Removed when the shadow gate flips to integer-canonical (separate
// workstream).
//
// Behavior note carried forward from the pre-migration code: the float
// path absorbs its rounding remainder at the caller-slice-last recipient.
// Because the caller-supplied recipients slice ordering is not guaranteed
// to be stable across nodes (collectAgreeingValidators iterates a Votes
// slice whose ordering depends on local receive-order), the float path's
// remainder-absorption recipient can differ across nodes by the handful
// of µAET represented by the rounding remainder. The integer path (via
// protocolmath.AllocateWithCeiling) fixes this by sorting on CanonicalKey
// before allocation.
func (s *VerificationConsensusSettler) computeValidatorPayoutsFloat(
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
			// Down-convert BasisPoints to float64 inside the legacy path so
			// the pre-migration arithmetic is preserved unchanged. Removed
			// when the float path itself is removed.
			q = float64(s.qScoreFn(v, "", category)) / 10000.0
		}
		entries[i] = scored{id: v, q: q}
		totalQ += q
	}

	if totalQ == 0 {
		return evenSplitFallback(recipients, pool)
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

// computeValidatorPayoutsInteger is the integer-arithmetic payout
// computation introduced by the canonical-distribution-integer-migration
// workstream. It pre-clamps negative Q to zero (so protocolmath's
// ErrInvariantViolation remains a true impossibility-signal rather than a
// routine handling path for upstream bugs) and routes the allocation
// through protocolmath.AllocateWithCeiling for deterministic cross-node
// results.
//
// Remainder-absorption: protocolmath sorts recipients by CanonicalKey
// (AgentID bytes) and routes the rounding remainder to the sorted-last
// recipient. This is deterministic across nodes regardless of receive-
// ordering of the vote events, which is a correctness improvement over
// the float path; see the float-path doc comment for the pre-migration
// non-determinism.
func (s *VerificationConsensusSettler) computeValidatorPayoutsInteger(
	recipients []crypto.AgentID,
	pool uint64,
	category string,
) map[crypto.AgentID]uint64 {
	if len(recipients) == 0 || pool == 0 {
		return map[crypto.AgentID]uint64{}
	}
	pm := make([]protocolmath.Recipient, 0, len(recipients))
	for _, v := range recipients {
		q := protocolmath.NeutralBP
		if s.qScoreFn != nil {
			q = s.qScoreFn(v, "", category)
		}
		if q < 0 {
			slog.Warn("settlement: qScoreFn returned negative; clamping to zero",
				"validator", v, "category", category, "returned_bp", q)
			q = 0
		}
		pm = append(pm, protocolmath.Recipient{CanonicalKey: []byte(v), Weight: q})
	}
	result, err := protocolmath.AllocateWithCeiling(pm, protocolmath.MicroAET(pool))
	if err != nil {
		slog.Error("settlement: protocolmath returned error; falling back to even-split",
			"err", err, "recipients", len(recipients), "pool", pool)
		return evenSplitFallback(recipients, pool)
	}
	out := make(map[crypto.AgentID]uint64, len(result))
	for k, v := range result {
		out[crypto.AgentID(k)] = uint64(v)
	}
	return out
}

// evenSplitFallback distributes pool evenly among recipients, routing any
// rounding remainder to the caller-slice-last recipient. Used by both the
// float path (when total Q is zero) and the integer path (when protocolmath
// returns an unexpected error; the latter should be unreachable in
// practice because negative Q is pre-clamped and recipient lists are built
// without duplicates by the settler).
func evenSplitFallback(recipients []crypto.AgentID, pool uint64) map[crypto.AgentID]uint64 {
	payouts := make(map[crypto.AgentID]uint64, len(recipients))
	if len(recipients) == 0 || pool == 0 {
		return payouts
	}
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

// logShadowDelta emits one line per computeValidatorPayouts invocation
// comparing the float and integer path outputs. Format documented in the
// canonical-distribution-integer-migration plan §4.5; Part F parses these
// lines from testnet logs to validate the cutover corpus.
//
// Conservation failures (sum mismatch) elevate to Warn; per-recipient
// differences stay Info because remainder-absorption-recipient drift is
// expected (up to the per-recipient rounding remainder) and is exactly
// the divergence the shadow pass is designed to surface before the
// cutover event flips the canonical path.
func (s *VerificationConsensusSettler) logShadowDelta(
	context, taskID string,
	recipients []crypto.AgentID,
	floatResult, intResult map[crypto.AgentID]uint64,
	pool uint64,
) {
	var sumFloat, sumInt uint64
	// safe: commutative sum; iteration order does not affect result
	for _, v := range floatResult {
		sumFloat += v
	}
	// safe: commutative sum; iteration order does not affect result
	for _, v := range intResult {
		sumInt += v
	}
	var maxAbs uint64
	for _, r := range recipients {
		f := floatResult[r]
		i := intResult[r]
		var d uint64
		if f > i {
			d = f - i
		} else {
			d = i - f
		}
		if d > maxAbs {
			maxAbs = d
		}
	}
	var sumDelta int64
	if sumFloat >= sumInt {
		sumDelta = int64(sumFloat - sumInt)
	} else {
		sumDelta = -int64(sumInt - sumFloat)
	}
	level := slog.LevelInfo
	if sumDelta != 0 {
		level = slog.LevelWarn
	}
	slog.Log(context_noop(), level, "shadow_delta",
		"context", context,
		"task_id", taskID,
		"recipient_count", len(recipients),
		"float_sum", sumFloat,
		"int_sum", sumInt,
		"sum_delta", sumDelta,
		"max_per_recipient_delta", maxAbs,
		"pool", pool,
	)
}

// context_noop returns a background context for slog.Log calls. Extracted
// so the helper's signature stays readable.
func context_noop() context.Context { return context.Background() }

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
