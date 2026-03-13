// Package autovalidator provides automatic settlement for AetherNet testnet nodes.
//
// On testnet, there are no external validator nodes, so transactions sit in
// Optimistic state forever unless something approves them. AutoValidator fills
// that role: it polls the OCS engine every interval and auto-approves every
// pending item, moving them from Optimistic → Settled within a single tick.
//
// It also auto-settles task marketplace submissions: any task in Submitted
// state for more than taskStaleness is automatically approved so the explorer
// shows completed tasks without manual operator intervention.
//
// Dispute resolution: disputed tasks are auto-resolved after disputeReviewTimeout
// based on the stored evidence score — score ≥ 0.60 releases funds to the
// worker, score < 0.60 refunds the poster and penalises the worker's reputation.
//
// Claim timeout: claimed tasks where the claimer hasn't submitted within
// claimTimeout are released back to Open so another agent can take them; the
// abandoning claimer's reputation takes a failure hit.
//
// This is TESTNET ONLY. On mainnet, real validator nodes earn fees by doing
// genuine verification work; auto-approval would defeat the trust model.
//
// This package is L3 (Application layer) — it drives the task marketplace
// lifecycle and should not be embedded in the L2 validator infrastructure.
package autovalidator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/canary"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/replay"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// canarySource is the interface satisfied by *canary.Injector. Defined locally
// to avoid import cycles and to keep the dependency surface minimal.
type canarySource interface {
	ShouldInject() bool
	NextCanary(category string) *canary.CanaryTask
	LinkTask(c *canary.CanaryTask, taskID string) error
	IsCanary(taskID string) bool
}

// canaryEvalSource is the interface satisfied by *canary.CanaryManager for
// looking up the canary record associated with a given protocol task ID.
type canaryEvalSource interface {
	GetCanaryByTaskID(taskID string) (*canary.CanaryTask, error)
}

// canaryEvalRecorder is the interface satisfied by *canary.Evaluator for
// computing and persisting a calibration signal. observedOutput is the raw
// work text submitted by the actor (ResultNote or similar); empty string is
// accepted when not available (falls back to verifier-only scoring).
type canaryEvalRecorder interface {
	Evaluate(c *canary.CanaryTask, actorID, role string, observedPass bool, observedChecks map[string]bool, observedOutput string) *canary.CalibrationSignal
}

// AutoValidator periodically checks for pending OCS items and auto-approves
// them with a positive verdict. Intended for testnet use only.
type AutoValidator struct {
	engine      *ocs.Engine
	validatorID crypto.AgentID
	interval    time.Duration
	stop        chan struct{}
	once        sync.Once

	// Task marketplace — optional. When set, auto-settles submitted tasks.
	taskMgr       *tasks.TaskManager
	escrowMgr     *escrow.Escrow
	reputationMgr *reputation.ReputationManager

	// Identity registry — optional. When set, auto-settlement syncs the
	// capability fingerprint's TasksCompleted counter so trust limits improve.
	identityRegistry *identity.Registry

	// Fee collection — optional. When set, deducts a settlement fee from each
	// approved task budget before releasing funds to the worker.
	feeCollector *fees.Collector
	treasuryID   crypto.AgentID

	// Generation ledger — optional. When set, records a verified production
	// entry for every settled task so GET /v1/economics tracks total output.
	generationLedger *ledger.GenerationLedger

	// verifierRegistry routes tasks to the appropriate deterministic verifier by
	// category. When nil, the default KeywordVerifier is used (backward-compatible).
	verifierRegistry *evidence.VerifierRegistry

	// verificationService is the optional higher-level evidence service. When set,
	// it takes priority over verifierRegistry in verifyEvidence(). Falls back to
	// verifierRegistry (then keyword verifier) when nil — backward compatible.
	verificationService verification.VerificationService

	// replayCoordinator is optional. When set, every settled task is evaluated
	// for replay eligibility. Generation-eligible tasks selected for replay
	// have their generation ledger entry held until the replay confirms the
	// original work.
	replayCoordinator *replay.ReplayCoordinator

	// dag and kp are optional. When both are set, settleTask creates a Transfer
	// DAG event recording the worker payment so the activity feed reflects task
	// payouts alongside regular peer transfers.
	dag *dag.DAG
	kp  *crypto.KeyPair

	// canaryInjector is optional. When set, IsCanary is checked for each
	// submitted task to detect protocol-internal measurement tasks injected
	// into the live stream. Used by SetCanaryInjector; nil = skip all injection
	// logic (backward compatible).
	canaryInjector canarySource

	// canaryEvalStore and canaryEval are optional. When both are non-nil,
	// every submitted task is checked for a canary record; if found, a
	// CalibrationSignal is emitted. Evaluation is observational — it does not
	// alter task settlement or reputation.
	canaryEvalStore canaryEvalSource
	canaryEval      canaryEvalRecorder

	// taskStaleness is the minimum age a submitted task must reach before the
	// auto-validator processes it. Defaults to 10 seconds so the task poster
	// has a window to approve manually first. Set to 0 in tests.
	taskStaleness time.Duration

	// claimTimeout controls claim-expiry detection.
	// When > 0, a claimed task is considered expired if
	//   task.ClaimedAt + int64(claimTimeout) < time.Now().UnixNano()
	// When 0, task.ClaimDeadline is used instead (set by TaskManager on claim).
	// Defaults to 0 so the deadline written at claim time governs production.
	// Set to a short duration in tests to exercise expiry quickly.
	claimTimeout time.Duration

	// disputeReviewTimeout is how long after DisputedAt before the auto-validator
	// auto-resolves a dispute. Defaults to 10 minutes (testnet).
	disputeReviewTimeout time.Duration
}

// NewAutoValidator creates an AutoValidator that polls engine every interval
// and approves all pending items as validatorID.
func NewAutoValidator(engine *ocs.Engine, validatorID crypto.AgentID, interval time.Duration) *AutoValidator {
	return &AutoValidator{
		engine:               engine,
		validatorID:          validatorID,
		interval:             interval,
		stop:                 make(chan struct{}),
		taskStaleness:        10 * time.Second,
		disputeReviewTimeout: 10 * time.Minute,
	}
}

// SetTaskManager wires optional task marketplace components. When set, the
// auto-validator auto-approves submitted tasks older than taskStaleness.
func (av *AutoValidator) SetTaskManager(tm *tasks.TaskManager, e *escrow.Escrow) {
	av.taskMgr = tm
	av.escrowMgr = e
}

// SetReputationManager wires optional reputation tracking. When set, task
// completions and failures are recorded for category-level reputation scoring.
func (av *AutoValidator) SetReputationManager(rm *reputation.ReputationManager) {
	av.reputationMgr = rm
}

// SetRegistry wires the identity registry into the auto-validator. When set,
// every auto-settled task increments the claimer's TasksCompleted counter in
// their CapabilityFingerprint, ensuring trust limits grow with task history.
func (av *AutoValidator) SetRegistry(reg *identity.Registry) {
	av.identityRegistry = reg
}

// SetFeeCollector wires optional fee collection. When set, a 0.1% settlement
// fee is deducted from each approved task budget: the worker receives
// (budget - fee), and the fee is split 80/20 between the validator and treasury.
func (av *AutoValidator) SetFeeCollector(fc *fees.Collector, treasuryID crypto.AgentID) {
	av.feeCollector = fc
	av.treasuryID = treasuryID
}

// SetGenerationLedger wires optional generation ledger recording. When set,
// every approved task creates a Settled entry in the generation ledger so
// GET /v1/economics can report total verified productive AI computation.
func (av *AutoValidator) SetGenerationLedger(gl *ledger.GenerationLedger) {
	av.generationLedger = gl
}

// SetTaskStalenessThreshold overrides the minimum age a submitted task must
// reach before the auto-validator processes it. The default is 10 seconds.
// Set to 0 in tests to process tasks immediately.
func (av *AutoValidator) SetTaskStalenessThreshold(d time.Duration) {
	av.taskStaleness = d
}

// SetClaimTimeout overrides how long a claimed task can stay unsubmitted before
// the auto-validator releases it. When 0 (default), task.ClaimDeadline governs.
// Set to a short duration in tests to exercise claim expiry quickly.
func (av *AutoValidator) SetClaimTimeout(d time.Duration) {
	av.claimTimeout = d
}

// SetDisputeReviewTimeout overrides how long after a dispute before auto-resolution.
// Default is 10 minutes. Set to 0 in tests to resolve immediately.
func (av *AutoValidator) SetDisputeReviewTimeout(d time.Duration) {
	av.disputeReviewTimeout = d
}

// SetVerifierRegistry wires a VerifierRegistry into the auto-validator so that
// each task category is assessed by the appropriate deterministic verifier
// (CodeVerifier, DataVerifier, ContentVerifier) rather than the default
// keyword-overlap heuristic. When not called, backward-compatible behaviour is
// preserved: all tasks use evidence.NewVerifier().
func (av *AutoValidator) SetVerifierRegistry(r *evidence.VerifierRegistry) {
	av.verifierRegistry = r
}

// SetVerificationService wires a VerificationService into the auto-validator.
// When set, it takes priority over the direct VerifierRegistry call in
// verifyEvidence(). If not called, the existing VerifierRegistry / keyword-verifier
// fallback path is used unchanged.
func (av *AutoValidator) SetVerificationService(svc verification.VerificationService) {
	av.verificationService = svc
}

// SetCanaryInjector wires a canary Injector for detecting canary tasks in the
// live task stream. When set, SetCanaryEvaluator should also be called so that
// detected canary tasks emit calibration signals. When not called (default),
// all canary logic is bypassed — backward compatible.
func (av *AutoValidator) SetCanaryInjector(src canarySource) {
	av.canaryInjector = src
}

// SetCanaryEvaluator wires the canary evaluation components. When both src and
// rec are non-nil, each submitted task is checked for a canary record; if
// found, a CalibrationSignal is emitted recording how the worker's result
// compared to the ground truth. Evaluation is observational — settlement and
// reputation proceed unchanged. When not called (default), no evaluation
// occurs — backward compatible.
func (av *AutoValidator) SetCanaryEvaluator(src canaryEvalSource, rec canaryEvalRecorder) {
	av.canaryEvalStore = src
	av.canaryEval = rec
}

// SetReplayCoordinator wires an optional ReplayCoordinator. When set, every
// successfully verified task is evaluated for replay eligibility. Tasks
// selected for replay have their ReplayStatus set to "replay_pending".
// Generation-eligible tasks additionally have their generation ledger entry
// withheld until the ReplayEnforcer confirms the original work via
// ProcessReplayOutcome.
func (av *AutoValidator) SetReplayCoordinator(c *replay.ReplayCoordinator) {
	av.replayCoordinator = c
}

// SetDAG wires the causal DAG so that settled task payments are recorded as
// Transfer events in the DAG, making them visible in the activity feed and
// network statistics. Optional; settlement proceeds without DAG recording if
// not called.
func (av *AutoValidator) SetDAG(d *dag.DAG) {
	av.dag = d
}

// SetKeyPair sets the signing key used to author DAG events created by the
// auto-validator. Optional; unsigned events are still added to the DAG when
// not set, but they will fail signature verification by strict validators.
func (av *AutoValidator) SetKeyPair(kp *crypto.KeyPair) {
	av.kp = kp
}

// verifyEvidence dispatches to the VerificationService when wired, falls back
// to the VerifierRegistry, and finally falls back to the default keyword
// verifier. This is the single call-site for all evidence assessment in the
// auto-validator.
func (av *AutoValidator) verifyEvidence(ev *evidence.Evidence, title, description string, budget uint64, category string) (*evidence.Score, bool, map[string]bool) {
	if av.verificationService != nil {
		req := verification.VerificationRequest{
			Category:    category,
			Title:       title,
			Description: description,
			Budget:      budget,
			Evidence:    ev,
		}
		if result, err := av.verificationService.Verify(context.Background(), req); err == nil && result != nil {
			score := &evidence.Score{
				Relevance:    result.SubjectiveReport.Relevance,
				Completeness: result.SubjectiveReport.Completeness,
				Quality:      result.SubjectiveReport.Quality,
				Overall:      result.SubjectiveReport.Overall,
			}
			passed := len(result.DeterministicReport.HardGates) > 0 && result.DeterministicReport.HardGates[0].Pass
			// Extract named structural gates (indices 1+) for canary check comparison.
			// Gate 0 is always "threshold" and is captured by passed; skip it.
			var gates map[string]bool
			if len(result.DeterministicReport.HardGates) > 1 {
				gates = make(map[string]bool, len(result.DeterministicReport.HardGates)-1)
				for _, g := range result.DeterministicReport.HardGates[1:] {
					gates[g.Name] = g.Pass
				}
			}
			return score, passed, gates
		}
	}
	if av.verifierRegistry != nil {
		score, passed := av.verifierRegistry.Verify(ev, title, description, budget, category)
		return score, passed, nil
	}
	score, passed := evidence.NewVerifier().Verify(ev, title, description, budget)
	return score, passed, nil
}

// Start launches the background approval goroutine. It is safe to call once.
func (av *AutoValidator) Start() {
	go func() {
		ticker := time.NewTicker(av.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				av.processPending()
				if av.taskMgr != nil {
					av.processSubmittedTasks()
					av.processExpiredClaims()
					av.processDisputedTasks()
					av.processStuckHeld()
				}
			case <-av.stop:
				return
			}
		}
	}()
	slog.Info("auto-validator started", "interval", av.interval, "validator_id", av.validatorID)
}

// Stop shuts down the background goroutine. Safe to call multiple times.
func (av *AutoValidator) Stop() {
	av.once.Do(func() { close(av.stop) })
}

// processSubmittedTasks assesses submitted marketplace tasks using the evidence
// Verifier. Tasks that pass the quality threshold are auto-approved; those below
// are held for manual review with a log message. Only tasks older than taskStaleness
// are evaluated to give the poster time to approve manually first.
func (av *AutoValidator) processSubmittedTasks() {
	submitted := av.taskMgr.Search(tasks.TaskStatusSubmitted, "", 0)
	cutoff := time.Now().UnixNano() - int64(av.taskStaleness)
	for _, task := range submitted {
		if task.SubmittedAt > cutoff {
			continue // submitted too recently
		}
		// Use the structured evidence stored at submission time when available —
		// it carries OutputPreview, Metrics, and the correct OutputType/OutputSize
		// that the auto-validator's verifiers rely on for quality scoring.
		// Fall back to the individual stored fields for tasks submitted before
		// this feature was added (SubmittedEvidence == nil).
		ev := task.SubmittedEvidence
		if ev == nil {
			ev = &evidence.Evidence{
				Hash:       task.ResultHash,
				Summary:    task.ResultNote,
				OutputType: "text",
				OutputSize: uint64(len(task.ResultNote)),
				OutputURL:  task.ResultURI,
			}
		}
		score, passed, observedGates := av.verifyEvidence(ev, task.Title, task.Description, task.Budget, task.Category)
		_ = av.taskMgr.SetVerificationScore(task.ID, score)

		// Canary evaluation: check whether this task is a protocol-internal
		// measurement canary. When a canary record is found, emit a
		// CalibrationSignal comparing the worker's result to ground truth.
		// Evaluation is observational — settlement proceeds normally regardless.
		if av.canaryEvalStore != nil && av.canaryEval != nil {
			if c, err := av.canaryEvalStore.GetCanaryByTaskID(task.ID); err == nil {
				// Use the ResultNote as observed output for truth model evaluation.
				// If structured evidence is available, prefer its Summary (it may
				// be richer than the raw note).
				observedOutput := task.ResultNote
				if ev != nil && ev.Summary != "" {
					observedOutput = ev.Summary
				}
				// observedGates carries named structural gate results from the
				// VerificationService (e.g. "has_output", "min_length", "hash_valid").
				// Nil when the legacy verifierRegistry / keyword-verifier path was used.
				sig := av.canaryEval.Evaluate(c, task.ClaimerID, canary.RoleWorker, passed, observedGates, observedOutput)
				slog.Info("canary: calibration signal emitted",
					"signal_id", sig.ID,
					"task_id", task.ID,
					"canary_id", c.ID,
					"actor_id", task.ClaimerID,
					"correctness", sig.Correctness,
					"severity", sig.Severity,
					"computed_by", sig.ComputedBy,
					"truth_model_score", sig.TruthModelScore,
				)
			}
		}

		if !passed {
			slog.Info("auto-validator: task held below threshold",
				"task_id", task.ID, "score", score.Overall, "threshold", evidence.PassThreshold)
			continue
		}

		// Determine whether the replay coordinator wants to verify this task
		// and, if so, whether to hold the generation ledger entry.
		holdGeneration := false
		var replayReason string
		if av.replayCoordinator != nil {
			// Fetch the real task count for the claimer so the probationary
			// sampling rate is applied correctly to new agents.
			agentTaskCount := 0
			if av.identityRegistry != nil && task.ClaimerID != "" {
				if fp, err := av.identityRegistry.Get(crypto.AgentID(task.ClaimerID)); err == nil {
					agentTaskCount = int(fp.TasksCompleted)
				}
			}

			shouldReplay, reason := av.replayCoordinator.ShouldReplay(
				task.ID, task.ClaimerID, task.Category,
				score.Overall,
				task.Contract.GenerationEligible,
				false,           // challenged — not supported at settle time
				nil,             // anomalyFlags — populated by executor, not yet available
				agentTaskCount,  // real count from identity registry
			)
			if shouldReplay {
				replayReason = reason
				if task.Contract.GenerationEligible {
					holdGeneration = true
				} else {
					slog.Info("auto-validator: replay scheduled (non-generation task)",
						"task_id", task.ID, "reason", reason)
				}
			}
		}

		av.settleTask(task, score, holdGeneration)

		// Schedule the replay job after settlement so the task is in Completed
		// state before the replay executor can query it.
		if replayReason != "" && av.replayCoordinator != nil {
			// Build minimal ReplayRequirements from the task's AcceptanceContract.
			// These fields give the replay executor the contract commitment hash and
			// the required gate list so it knows what to re-run.
			var reqs *verification.ReplayRequirements
			if task.Contract.SpecHash != "" || len(task.Contract.RequiredChecks) > 0 {
				reqs = &verification.ReplayRequirements{
					AcceptanceContractHash: task.Contract.SpecHash,
					RequiredChecks:         task.Contract.RequiredChecks,
				}
			}

			// Assess the replayability of the submitted material so operators
			// can monitor how many replay jobs have full execution packs vs.
			// structural-only material. This is informational; it does not
			// block scheduling.
			assessment := verification.AssessReplayability(reqs, task.Category, task.Contract.PolicyVersion)
			slog.Debug("auto-validator: replay material assessment",
				"task_id", task.ID,
				"category", task.Category,
				"replayable", assessment.Replayable,
				"replay_level", assessment.ReplayLevel,
				"missing_fields_count", len(assessment.MissingFields),
			)

			job, err := av.replayCoordinator.ScheduleReplay(
				task.ID, task.ResultHash, task.Category,
				task.Contract.PolicyVersion,
				reqs, replayReason, task.ClaimerID,
			)
			if err != nil {
				slog.Error("auto-validator: failed to schedule replay",
					"task_id", task.ID, "reason", replayReason, "err", err)
			} else {
				if err := av.taskMgr.SetReplayStatus(task.ID, "replay_pending", job.ID); err != nil {
					slog.Error("auto-validator: failed to set replay status",
						"task_id", task.ID, "job_id", job.ID, "err", err)
				}
				slog.Info("auto-validator: replay scheduled",
					"task_id", task.ID, "job_id", job.ID, "reason", replayReason,
					"hold_generation", holdGeneration)
			}
		}
	}
}

// processExpiredClaims finds claimed tasks whose deadline has passed and
// releases them back to Open, penalising the claimer's reputation.
//
// The effective deadline is:
//   - task.ClaimedAt + claimTimeout  when av.claimTimeout > 0 (test override)
//   - task.ClaimDeadline             otherwise (set by TaskManager at claim time)
func (av *AutoValidator) processExpiredClaims() {
	claimed := av.taskMgr.Search(tasks.TaskStatusClaimed, "", 0)
	now := time.Now().UnixNano()
	for _, task := range claimed {
		var deadline int64
		if av.claimTimeout > 0 {
			// Test override: compute deadline from ClaimedAt + configurable timeout.
			deadline = task.ClaimedAt + int64(av.claimTimeout)
		} else {
			deadline = task.ClaimDeadline
		}
		if deadline <= 0 || deadline > now {
			continue // not yet expired
		}

		formerClaimer, err := av.taskMgr.ReleaseTask(task.ID)
		if err != nil {
			slog.Warn("auto-validator: could not release expired claim", "task_id", task.ID, "err", err)
			continue
		}
		slog.Info("auto-validator: claim expired", "task_id", task.ID, "claimer", formerClaimer)

		// Penalise the claimer's reputation for abandoning the task.
		if av.reputationMgr != nil && formerClaimer != "" {
			av.reputationMgr.RecordFailure(crypto.AgentID(formerClaimer), task.Category)
			slog.Debug("auto-validator: recorded failure for claimer", "claimer", formerClaimer, "task_id", task.ID)
		}
	}
}

// processDisputedTasks auto-resolves disputes that have exceeded the review
// window. Resolution is deterministic, based on the stored verification score:
//
//   - score >= evidence.PassThreshold (0.25): work was adequate → release to worker
//   - score  < evidence.PassThreshold:        work was inadequate → refund poster, penalise worker
//
// If no score is stored (e.g. dispute raised before auto-validation ran), the
// verifier is called again with the task's evidence fields.
func (av *AutoValidator) processDisputedTasks() {
	disputed := av.taskMgr.Search(tasks.TaskStatusDisputed, "", 0)
	now := time.Now().UnixNano()
	reviewCutoff := now - int64(av.disputeReviewTimeout)

	for _, task := range disputed {
		if task.DisputedAt > reviewCutoff {
			continue // still within the review window
		}

		// Determine the verification score.
		score := task.VerificationScore
		if score == nil {
			// Score not yet set — run the verifier now.
			ev := &evidence.Evidence{
				Hash:       task.ResultHash,
				Summary:    task.ResultNote,
				OutputType: "text",
				OutputSize: uint64(len(task.ResultNote)),
				OutputURL:  task.ResultURI,
			}
			var s *evidence.Score
			s, _, _ = av.verifyEvidence(ev, task.Title, task.Description, task.Budget, task.Category)
			_ = av.taskMgr.SetVerificationScore(task.ID, s)
			score = s
		}

		if score != nil && score.Overall >= evidence.PassThreshold {
			// Work was adequate: approve and release to worker.
			slog.Info("auto-validator: dispute resolved (approve)", "task_id", task.ID, "score", score.Overall)
			// Change task status to Completed before calling settleTask (which
			// calls ApproveTask that requires Submitted status — bypassed here
			// via ResolveDispute which accepts Disputed state).
			if err := av.taskMgr.ResolveDispute(task.ID, av.validatorID, true); err != nil {
				slog.Warn("auto-validator: could not resolve dispute (approve)", "task_id", task.ID, "err", err)
				continue
			}
			// Release escrow and distribute fee from the escrow bucket (C1/C2 fix).
			if av.escrowMgr != nil && task.ClaimerID != "" {
				fee := fees.CalculateFee(task.Budget)
				netAmount := task.Budget - fee
				validatorAmount := fee * fees.ValidatorShare / 100
				treasuryAmount := fee * fees.TreasuryShare / 100
				burned := fee - validatorAmount - treasuryAmount
				if err := av.escrowMgr.ReleaseNet(
					task.ID,
					crypto.AgentID(task.ClaimerID), netAmount,
					av.validatorID, validatorAmount,
					av.treasuryID, treasuryAmount,
				); err != nil {
					slog.Error("auto-validator: could not release escrow (dispute approve)", "task_id", task.ID, "err", err)
				} else if av.feeCollector != nil && fee > 0 {
					av.feeCollector.TrackFee(fee, burned, treasuryAmount)
				}
			}
			if av.generationLedger != nil && task.ClaimerID != "" && score != nil && task.Contract.GenerationEligible {
				verifiedValue := uint64(float64(task.Budget) * score.Overall)
				if err := av.generationLedger.RecordTaskGeneration(
					crypto.AgentID(task.ClaimerID), task.ResultHash, task.Title, verifiedValue, task.ID,
				); err != nil {
					slog.Error("auto-validator: generation ledger record failed (dispute resolved)",
						"task_id", task.ID, "err", err)
				}
			}
			if av.reputationMgr != nil && task.ClaimerID != "" {
				approvedAt := time.Now().UnixNano()
				deliverySecs := float64(approvedAt-task.ClaimedAt) / 1e9
				av.reputationMgr.RecordCompletion(
					crypto.AgentID(task.ClaimerID), task.Category, task.Budget, score.Overall, deliverySecs,
				)
			}
			if av.identityRegistry != nil && task.ClaimerID != "" {
				_ = av.identityRegistry.RecordTaskCompletion(
					crypto.AgentID(task.ClaimerID), task.Budget, task.Category,
				)
			}
		} else {
			// Work was inadequate: refund poster, penalise worker.
			overall := 0.0
			if score != nil {
				overall = score.Overall
			}
			slog.Info("auto-validator: dispute resolved (reject)", "task_id", task.ID, "score", overall)

			if err := av.taskMgr.ResolveDispute(task.ID, av.validatorID, false); err != nil {
				slog.Warn("auto-validator: could not resolve dispute (reject)", "task_id", task.ID, "err", err)
				continue
			}
			if av.escrowMgr != nil {
				if err := av.escrowMgr.Refund(task.ID); err != nil {
					slog.Error("auto-validator: could not refund escrow", "task_id", task.ID, "err", err)
				}
			}
			if av.reputationMgr != nil && task.ClaimerID != "" {
				av.reputationMgr.RecordFailure(crypto.AgentID(task.ClaimerID), task.Category)
			}
		}
	}
}

// settleTask is the shared approval path used by processSubmittedTasks. It:
//  1. Marks the task Completed via taskMgr.ApproveTask
//  2. Releases (budget - fee) to the worker via escrow.ReleaseNet
//  3. Distributes the fee to validator + treasury via feeCollector.CollectFee
//  4. Records verified productive output in the generation ledger (unless holdGeneration is true)
//  5. Records a reputation completion for the worker
//
// holdGeneration should be true when the replay coordinator has selected this
// task for replay and the task is generation-eligible. In that case the
// generation ledger entry is withheld until ReplayEnforcer.ProcessReplayOutcome
// confirms the original work.
func (av *AutoValidator) settleTask(task *tasks.Task, score *evidence.Score, holdGeneration bool) {
	approvedAt := time.Now().UnixNano()
	if err := av.taskMgr.ApproveTask(task.ID, av.validatorID); err != nil {
		slog.Warn("auto-validator: could not approve task", "task_id", task.ID, "err", err)
		return
	}
	if av.escrowMgr != nil && task.ClaimerID != "" {
		fee := fees.CalculateFee(task.Budget)
		netAmount := task.Budget - fee
		validatorAmount := fee * fees.ValidatorShare / 100
		treasuryAmount := fee * fees.TreasuryShare / 100
		burned := fee - validatorAmount - treasuryAmount
		// C1/C2: Distribute all fee splits from the escrow bucket (no minting).
		if err := av.escrowMgr.ReleaseNet(
			task.ID,
			crypto.AgentID(task.ClaimerID), netAmount,
			av.validatorID, validatorAmount,
			av.treasuryID, treasuryAmount,
		); err != nil {
			slog.Error("auto-validator: could not release escrow for task", "task_id", task.ID, "err", err)
		} else {
			if av.feeCollector != nil && fee > 0 {
				av.feeCollector.TrackFee(fee, burned, treasuryAmount)
				slog.Info("auto-validator: collected fee", "fee", fee, "task_id", task.ID, "net_to_worker", netAmount)
			}
			// Record the payout as a Transfer event in the DAG so the activity
			// feed shows task settlements alongside peer-to-peer transfers.
			if av.dag != nil {
				tips := av.dag.Tips()
				priorTS := make(map[event.EventID]uint64, len(tips))
				for _, ref := range tips {
					if ev, err := av.dag.Get(ref); err == nil {
						priorTS[ref] = ev.CausalTimestamp
					}
				}
				e, err := event.New(
					event.EventTypeTransfer,
					tips,
					event.TransferPayload{
						FromAgent: "escrow:" + task.ID,
						ToAgent:   string(task.ClaimerID),
						Amount:    netAmount,
						Currency:  "AET",
						Memo:      fmt.Sprintf("task-settlement:%s", task.ID),
					},
					string(av.validatorID),
					priorTS,
					0,
				)
				if err == nil {
					if av.kp != nil {
						_ = crypto.SignEvent(e, av.kp)
					}
					_ = av.dag.Add(e)
				}
			}
		}
	}

	// Record verified productive AI computation in the generation ledger.
	// Only for generation-eligible tasks; non-eligible tasks have their payout
	// settled but must never write generation ledger entries.
	// Skipped when holdGeneration is true: the replay coordinator has selected
	// this task for verification replay and the generation credit will be
	// released by ReplayEnforcer.ProcessReplayOutcome once the replay confirms
	// the original work.
	if !holdGeneration && av.generationLedger != nil && task.ClaimerID != "" && score != nil && task.Contract.GenerationEligible {
		verifiedValue := uint64(float64(task.Budget) * score.Overall)
		if err := av.generationLedger.RecordTaskGeneration(
			crypto.AgentID(task.ClaimerID),
			task.ResultHash,
			task.Title,
			verifiedValue,
			task.ID,
		); err != nil {
			slog.Error("auto-validator: generation ledger record failed", "task_id", task.ID, "err", err)
		} else {
			if err := av.taskMgr.SetGenerationStatus(task.ID, "recognized"); err != nil {
				slog.Error("auto-validator: set generation status recognized failed", "task_id", task.ID, "err", err)
			}
		}
	} else if holdGeneration {
		// Generation credit is withheld pending replay outcome.
		if err := av.taskMgr.SetGenerationStatus(task.ID, "held"); err != nil {
			slog.Error("auto-validator: set generation status held failed", "task_id", task.ID, "err", err)
		}
	}

	if av.reputationMgr != nil && task.ClaimerID != "" && score != nil {
		deliverySecs := float64(approvedAt-task.ClaimedAt) / 1e9
		av.reputationMgr.RecordCompletion(
			crypto.AgentID(task.ClaimerID),
			task.Category,
			task.Budget,
			score.Overall,
			deliverySecs,
		)
	}
	// Sync the identity registry TasksCompleted so trust limits grow with history.
	if av.identityRegistry != nil && task.ClaimerID != "" {
		_ = av.identityRegistry.RecordTaskCompletion(
			crypto.AgentID(task.ClaimerID),
			task.Budget,
			task.Category,
		)
	}
	slog.Info("auto-validator: task approved", "task_id", task.ID, "score", score.Overall, "claimer", task.ClaimerID)
}

// processStuckHeld scans completed tasks for those with GenerationStatus="held"
// but no associated ReplayJobID. This condition indicates that settleTask set
// the generation status to "held" but the subsequent ScheduleReplay call
// failed, leaving the task in a state the ReplayEnforcer can never resolve.
//
// Recovery: the GenerationStatus is reset to "" so the task is no longer stuck
// in an unresolvable hold. The lost generation credit is logged at ERROR
// severity for operator review.
func (av *AutoValidator) processStuckHeld() {
	completed := av.taskMgr.Search(tasks.TaskStatusCompleted, "", 0)
	for _, task := range completed {
		if task.GenerationStatus != "held" || task.ReplayJobID != "" {
			continue
		}
		slog.Error("auto-validator: stuck-held task detected — generation credit held with no replay job",
			"task_id", task.ID,
			"claimer_id", task.ClaimerID,
			"remedy", "generation_status_reset_to_empty",
		)
		if err := av.taskMgr.SetGenerationStatus(task.ID, ""); err != nil {
			slog.Error("auto-validator: failed to reset stuck-held generation status",
				"task_id", task.ID, "err", err)
		}
	}
}

// processPending fetches all pending OCS items and submits a positive verdict
// for each. Items that fail (e.g. self-dealing checks) are skipped with a log.
func (av *AutoValidator) processPending() {
	pending := av.engine.Pending()
	for _, item := range pending {
		err := av.engine.ProcessResult(ocs.VerificationResult{
			EventID:       item.EventID,
			Verdict:       true,
			VerifiedValue: item.Amount,
			VerifierID:    av.validatorID,
			Reason:        "auto-validator: testnet settlement",
			Timestamp:     time.Now(),
		})
		if err != nil {
			slog.Warn("auto-validator: could not settle", "event_id", item.EventID, "err", err)
			continue
		}
		slog.Debug("auto-validator: settled", "event_id", item.EventID, "value", item.Amount)
	}
}
