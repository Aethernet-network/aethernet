package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
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

	// dagReader is the canonical AnchorReader passed to DeriveSettlement
	// (V-1 ActivationCheck closure + ReadAtAnchor + CountAncestorsByType
	// for cutoff_epoch). MUST be the same canonical view used by the
	// finalizing consumer's CountAncestorsByType call (sub-spec §8.2
	// canonical-DAG-view discipline). nil until set via SetDAGReader at
	// construction.
	dagReader derivation.AnchorReader

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

// SetDAGReader installs the canonical AnchorReader used by
// DeriveSettlement. Called by tests that wire a fake reader directly.
// Production code paths use SetDAG, which wraps *dag.DAG in the
// dagAnchorReaderAdapter.
//
// Per F5 5B canonical-epoch sub-spec §8.2 canonical-DAG-view discipline:
// the reader MUST be the same canonical view used by the finalizing
// consumer's CountAncestorsByType call. Shadow caches, stale wrappers,
// or local-only views are forbidden.
func (s *VerificationConsensusSettler) SetDAGReader(r derivation.AnchorReader) {
	s.dagReader = r
}

// SetDAG is the production wiring entry point: wraps *dag.DAG in the
// canonical dagAnchorReaderAdapter and stores it as the settler's
// AnchorReader. Per multi-AI Item 1 composite (2026-04-25), the
// explicit adapter struct documents the canonical-frozen-read intent
// at the wiring boundary instead of relying on implicit interface
// upcasting from the wide *dag.DAG surface.
func (s *VerificationConsensusSettler) SetDAG(d *dag.DAG) {
	s.SetDAGReader(dagAnchorReaderAdapter{dag: d})
}

// SettleResult captures the outcome of a settlement application.
type SettleResult struct {
	Applied              bool
	AlreadyApplied       bool
	Deferred             bool                    // F5 5B: DeriveSettlement returned StatusDeferred
	DeferredCause        derivation.DeferredCause // populated when Deferred == true
	Verdict              string
	WorkerPayout         uint64
	PosterRefund         uint64
	ValidatorPayouts     map[crypto.AgentID]uint64
	GenerationLedger     GenerationLedgerDistribution
	TreasuryAmount       uint64
	TotalDistributed     uint64

	// Records is the canonical PayoutRecord slice produced by
	// DeriveSettlement (F5 5B). Empty when Deferred=true or when the
	// legacy ReleaseSettlement path is taken (dagReader not wired).
	Records []derivation.PayoutRecord
}

// Settle applies the v4.1 economic distribution for a verification consensus.
// Idempotent: tasks already in a terminal state produce AlreadyApplied=true.
//
// F5 5B integration: when dagReader is wired, Settle routes through
// derivation.DeriveSettlement → escrow.ApplySettlementRecords (the
// canonical pure-derivation path). When dagReader is nil (legacy /
// pre-5B test environments), falls back to the imperative
// settleAccept/Reject/Dispute path (which calls escrow.ReleaseSettlement).
//
// The dagReader-nil fallback is dead code at 5B closure (cmd/node/main.go
// always wires dagReader); it remains for the float-path companion-PR's
// transitional window per Plan v3 §0.10. Companion-PR removal eliminates
// the legacy branch entirely.
func (s *VerificationConsensusSettler) Settle(
	ctx context.Context,
	payload *event.TaskVerificationConsensusPayload,
	round *taskverification.TaskVerificationRound,
) (SettleResult, error) {
	result := SettleResult{Verdict: payload.FinalVerdict}

	// Look up the task.
	task, err := s.taskMgr.Get(payload.TaskID)
	if err != nil {
		return result, fmt.Errorf("verification_settler: task %s not found: %w", payload.TaskID, err)
	}

	// Idempotent: already in terminal state. Per Gate 5A.1 §9.2 option-b
	// the task.Status read here is an EARLY-EXIT short-circuit, NOT a
	// payout-math dependency — the §4.4 reopen condition does not fire.
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

	// Suppress unused-variable warnings; entry/budget/etc. are read by
	// settleViaDerivation via the inputs bundle, not as locals here.
	_ = entry

	// F5 5B: route through DeriveSettlement. dagReader MUST be wired
	// at construction (cmd/node/main.go:tvSettler.SetDAGReader). The
	// legacy settleAccept/Reject/Dispute paths were removed in #133;
	// nil dagReader is now a precondition violation.
	if s.dagReader == nil {
		return result, fmt.Errorf("verification_settler: dagReader not wired — SetDAGReader must be called before Settle (post-F5 5B canonical path requires the DAG view for DeriveSettlement)")
	}
	return s.settleViaDerivation(ctx, payload, round, result)
}

// settleViaDerivation is the F5 5B canonical path: build
// DerivationInputs, call DeriveSettlement, route Status to apply or
// defer, then call ApplySettlementRecords + finalize task state.
//
// Per Plan v3 §3.5 caller-code shape:
//
//	result, err := deriveSettlement(ctx, round, inputs)
//	if err != nil { return err }
//	if result.Status == StatusDeferred { deferRound(round); return nil }
//	return e.escrowMgr.ApplySettlementRecords(taskID, result.Records)
func (s *VerificationConsensusSettler) settleViaDerivation(
	ctx context.Context,
	payload *event.TaskVerificationConsensusPayload,
	round *taskverification.TaskVerificationRound,
	result SettleResult,
) (SettleResult, error) {
	inputs, err := s.buildDerivationInputs()
	if err != nil {
		return result, fmt.Errorf("verification_settler: buildDerivationInputs: %w", err)
	}

	derived, err := derivation.DeriveSettlement(ctx, round, inputs)
	if err != nil {
		return result, fmt.Errorf("verification_settler: DeriveSettlement: %w", err)
	}

	// Exhaustive Status enum dispatch (multi-AI Item 2, V#4 hardening
	// 2026-04-25). Switch with explicit StatusDerived case + default
	// error catches future enum additions at runtime — without the
	// default branch, a new StatusXxx variant would silently fall
	// through to ApplySettlementRecords with empty Records, masking
	// the integration regression.
	switch derived.Status {
	case derivation.StatusDeferred:
		// Re-enqueue handled by the recognition fabric / dispatcher
		// retry mechanism (F3-B causal-prerequisite-gating). Caller
		// observes Deferred=true and does NOT mark the task as
		// terminal — it'll re-fire when canonical state advances.
		result.Deferred = true
		result.DeferredCause = derived.Cause
		slog.Info("verification_settler: deferred (canonical state not yet materialized)",
			"task_id", payload.TaskID,
			"round_id", payload.RoundID,
			"cause", derived.Cause.String(),
		)
		return result, nil
	case derivation.StatusDerived:
		// Fall through to the apply path below.
	default:
		return result, fmt.Errorf("verification_settler: DeriveSettlement returned unknown Status %v (enum extension without dispatch update)", derived.Status)
	}

	// Apply the canonical records.
	if applyErr := s.escrowMgr.ApplySettlementRecords(payload.TaskID, derived.Records); applyErr != nil {
		return result, fmt.Errorf("verification_settler: ApplySettlementRecords: %w", applyErr)
	}

	// Last step: terminal task-state transition. If Settle is interrupted
	// after ApplySettlementRecords but before this transition, retry's
	// pre-check (task.Status terminal) will short-circuit, AND the
	// escrow.ApplySettlementRecords paid-flag projection will skip-
	// optimize the records on retry. No double-pay risk per Plan v3
	// §3.4 obligation a/b/c + crash-position table.
	taskVerdictString := payload.FinalVerdict
	if transErr := s.taskMgr.ApplyVerificationConsensusResolution(
		payload.TaskID, taskVerdictString, payload.FinalScoreBP, payload.RoundID,
	); transErr != nil {
		return result, fmt.Errorf("verification_settler: task state: %w", transErr)
	}

	// Populate observability fields on SettleResult from the derived
	// records (back-compat with downstream observers that read these
	// fields; future-work to switch downstream observers to
	// result.Records directly).
	result.Records = derived.Records
	result.Applied = true
	for _, r := range derived.Records {
		switch r.Recipient.Role {
		case derivation.RoleWorker:
			result.WorkerPayout = r.Amount.Value
		case derivation.RolePosterRefund:
			result.PosterRefund = r.Amount.Value
		case derivation.RoleValidator:
			if result.ValidatorPayouts == nil {
				result.ValidatorPayouts = make(map[crypto.AgentID]uint64)
			}
			result.ValidatorPayouts[r.Recipient.ID] = r.Amount.Value
		case derivation.RoleTreasury:
			result.TreasuryAmount = r.Amount.Value
		}
		result.TotalDistributed += r.Amount.Value
	}

	slog.Info("verification_settler: settled via derivation",
		"task_id", payload.TaskID,
		"round_id", payload.RoundID,
		"verdict", payload.FinalVerdict,
		"records", len(derived.Records),
		"total_distributed", result.TotalDistributed,
	)
	return result, nil
}

// buildDerivationInputs assembles the DerivationInputs bundle via the
// derivation package's NewDerivationInputs constructor. Built per-call
// (lightweight; no expensive resource construction). Per F5 5B
// canonical-epoch sub-spec §1.4.1 + DerivationInputs §2.1 contract:
// every field is canonical-frozen or deterministic-replayable-lookup.
//
// **Multi-AI Item 1 composite (2026-04-25)**: the prior in-line
// `activationCheck` closure that captured `s.dagReader` was deleted.
// Activation now flows through the canonical-frozen
// {Reputation,Quality}ActivationEventID arguments (clause-(a) values
// pulled directly from the locked package-level constants);
// DeriveSettlement's `isActivated` helper combines them with the
// dagReader. The function-field surface that previously could have
// hidden runtime-flag capture is GONE.
//
// NewDerivationInputs validates required-non-nil services and the
// canonical TreasuryID at construction time; an error here indicates a
// wiring bug at start-of-settlement (not at first DeriveSettlement
// call). Adapter wrappers (escrowDerivationLookup, qScoreFnAsCanonicalW)
// satisfy the §2.1 contract by exposing only canonical-frozen reads of
// their underlying services.
func (s *VerificationConsensusSettler) buildDerivationInputs() (derivation.DerivationInputs, error) {
	wStub := derivation.CanonicalWProjection(qScoreFnAsCanonicalW{fn: s.qScoreFn})
	if s.qScoreFn == nil {
		// Pre-reputation-store wiring case: use the package's universal
		// NeutralBP stub to keep V-1 semantics intact (every validator
		// returns NeutralBP).
		wStub = derivation.NeutralBPStubW{}
	}

	return derivation.NewDerivationInputs(
		derivation.WProjections{
			Stub: wStub,
			// Real: nil — locked Reputation-and-Consensus-Integrity
			// workstream's real W ships separately. The V-1 isActivated
			// check returns false today (empty ReputationActivationEventID),
			// so the Real slot is never selected.
		},
		derivation.QualityProjections{
			Stub: derivation.NeutralQualityStub{},
			// Real: nil — quality canonicalization is deferred to a
			// future workstream per sub-spec §3.
		},
		s.dagReader,
		escrowDerivationLookup{escrow: s.escrowMgr},
		derivation.ReputationActivationEventID,
		derivation.QualityActivationEventID,
		s.treasuryID,
	)
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


