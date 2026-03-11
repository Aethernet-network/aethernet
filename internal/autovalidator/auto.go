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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/fees"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/reputation"
	"github.com/Aethernet-network/aethernet/internal/tasks"
)

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

	// dag and kp are optional. When both are set, settleTask creates a Transfer
	// DAG event recording the worker payment so the activity feed reflects task
	// payouts alongside regular peer transfers.
	dag *dag.DAG
	kp  *crypto.KeyPair

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

// verifyEvidence dispatches to the VerifierRegistry when wired, or falls back
// to the default keyword verifier when not. This is the single call-site for
// all evidence assessment in the auto-validator.
func (av *AutoValidator) verifyEvidence(ev *evidence.Evidence, title, description string, budget uint64, category string) (*evidence.Score, bool) {
	if av.verifierRegistry != nil {
		return av.verifierRegistry.Verify(ev, title, description, budget, category)
	}
	return evidence.NewVerifier().Verify(ev, title, description, budget)
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
		score, passed := av.verifyEvidence(ev, task.Title, task.Description, task.Budget, task.Category)
		_ = av.taskMgr.SetVerificationScore(task.ID, score)
		if !passed {
			slog.Info("auto-validator: task held below threshold",
				"task_id", task.ID, "score", score.Overall, "threshold", evidence.PassThreshold)
			continue
		}
		av.settleTask(task, score)
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
			s, _ = av.verifyEvidence(ev, task.Title, task.Description, task.Budget, task.Category)
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
			if av.generationLedger != nil && task.ClaimerID != "" && score != nil {
				verifiedValue := uint64(float64(task.Budget) * score.Overall)
				_ = av.generationLedger.RecordTaskGeneration(
					crypto.AgentID(task.ClaimerID), task.ResultHash, task.Title, verifiedValue, task.ID,
				)
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

// settleTask is the shared approval path used by both processSubmittedTasks and
// processDisputedTasks (approve branch). It:
//  1. Marks the task Completed via taskMgr.ApproveTask
//  2. Releases (budget - fee) to the worker via escrow.ReleaseNet
//  3. Distributes the fee to validator + treasury via feeCollector.CollectFee
//  4. Records verified productive output in the generation ledger
//  5. Records a reputation completion for the worker
func (av *AutoValidator) settleTask(task *tasks.Task, score *evidence.Score) {
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
	if av.generationLedger != nil && task.ClaimerID != "" && score != nil {
		verifiedValue := uint64(float64(task.Budget) * score.Overall)
		if err := av.generationLedger.RecordTaskGeneration(
			crypto.AgentID(task.ClaimerID),
			task.ResultHash,
			task.Title,
			verifiedValue,
			task.ID,
		); err != nil {
			slog.Error("auto-validator: generation ledger record failed", "task_id", task.ID, "err", err)
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
