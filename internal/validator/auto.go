// Package validator provides automatic settlement for AetherNet testnet nodes.
//
// On testnet, there are no external validator nodes, so transactions sit in
// Optimistic state forever unless something approves them. AutoValidator fills
// that role: it polls the OCS engine every interval and auto-approves every
// pending item, moving them from Optimistic → Settled within a single tick.
//
// It also auto-settles task marketplace submissions: any task in Submitted
// state for more than 10 seconds is automatically approved so the explorer
// shows completed tasks without manual operator intervention.
//
// This is TESTNET ONLY. On mainnet, real validator nodes earn fees by doing
// genuine verification work; auto-approval would defeat the trust model.
package validator

import (
	"log"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/fees"
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

	// Fee collection — optional. When set, deducts a settlement fee from each
	// approved task budget before releasing funds to the worker.
	feeCollector *fees.Collector
	treasuryID   crypto.AgentID

	// taskStaleness is the minimum age a submitted task must reach before the
	// auto-validator processes it. Defaults to 10 seconds so the task poster
	// has a window to approve manually first. Set to 0 in tests.
	taskStaleness time.Duration
}

// NewAutoValidator creates an AutoValidator that polls engine every interval
// and approves all pending items as validatorID.
func NewAutoValidator(engine *ocs.Engine, validatorID crypto.AgentID, interval time.Duration) *AutoValidator {
	return &AutoValidator{
		engine:        engine,
		validatorID:   validatorID,
		interval:      interval,
		stop:          make(chan struct{}),
		taskStaleness: 10 * time.Second,
	}
}

// SetTaskManager wires optional task marketplace components. When set, the
// auto-validator auto-approves submitted tasks older than 10 seconds.
func (av *AutoValidator) SetTaskManager(tm *tasks.TaskManager, e *escrow.Escrow) {
	av.taskMgr = tm
	av.escrowMgr = e
}

// SetReputationManager wires optional reputation tracking. When set, task
// completions and failures are recorded for category-level reputation scoring.
func (av *AutoValidator) SetReputationManager(rm *reputation.ReputationManager) {
	av.reputationMgr = rm
}

// SetFeeCollector wires optional fee collection. When set, a 0.1% settlement
// fee is deducted from each approved task budget: the worker receives
// (budget - fee), and the fee is split 80/20 between the validator and treasury.
func (av *AutoValidator) SetFeeCollector(fc *fees.Collector, treasuryID crypto.AgentID) {
	av.feeCollector = fc
	av.treasuryID = treasuryID
}

// SetTaskStalenessThreshold overrides the minimum age a submitted task must
// reach before the auto-validator processes it. The default is 10 seconds.
// Set to 0 in tests to process tasks immediately.
func (av *AutoValidator) SetTaskStalenessThreshold(d time.Duration) {
	av.taskStaleness = d
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
				}
			case <-av.stop:
				return
			}
		}
	}()
	log.Printf("auto-validator started (interval: %v, validator_id: %s)", av.interval, av.validatorID)
}

// Stop shuts down the background goroutine. Safe to call multiple times.
func (av *AutoValidator) Stop() {
	av.once.Do(func() { close(av.stop) })
}

// processSubmittedTasks assesses submitted marketplace tasks using the evidence
// Verifier. Tasks that pass the quality threshold are auto-approved; those below
// are held for manual review with a log message. Only tasks older than 10 seconds
// are evaluated to give the poster time to approve manually first.
func (av *AutoValidator) processSubmittedTasks() {
	submitted := av.taskMgr.Search(tasks.TaskStatusSubmitted, "", 0)
	cutoff := time.Now().UnixNano() - int64(av.taskStaleness)
	verifier := evidence.NewVerifier()
	for _, task := range submitted {
		if task.SubmittedAt > cutoff {
			continue // submitted too recently
		}
		ev := &evidence.Evidence{
			Hash:       task.ResultHash,
			Summary:    task.ResultNote,
			OutputType: "text",
			OutputSize: uint64(len(task.ResultNote)),
			OutputURL:  task.ResultURI,
		}
		score, passed := verifier.Verify(ev, task.Title, task.Description, task.Budget)
		_ = av.taskMgr.SetVerificationScore(task.ID, score)
		if !passed {
			log.Printf("auto-validator: HELD task %s (score: %.2f, below threshold %.2f)",
				task.ID, score.Overall, evidence.PassThreshold)
			continue
		}
		approvedAt := time.Now().UnixNano()
		if err := av.taskMgr.ApproveTask(task.ID, av.validatorID); err != nil {
			log.Printf("auto-validator: could not approve task %s: %v", task.ID, err)
			continue
		}
		if av.escrowMgr != nil && task.ClaimerID != "" {
			fee := fees.CalculateFee(task.Budget)
			netAmount := task.Budget - fee
			if err := av.escrowMgr.ReleaseNet(task.ID, crypto.AgentID(task.ClaimerID), netAmount); err != nil {
				log.Printf("auto-validator: could not release escrow for task %s: %v", task.ID, err)
			} else if av.feeCollector != nil && fee > 0 {
				// CollectFee expects the full settlement amount and computes the
				// 0.1% fee internally — pass task.Budget, not the pre-computed fee.
				av.feeCollector.CollectFee(task.Budget, av.validatorID, av.treasuryID)
				log.Printf("auto-validator: collected fee %d for task %s (net to worker: %d)",
					fee, task.ID, netAmount)
			}
		}
		if av.reputationMgr != nil && task.ClaimerID != "" {
			deliverySecs := float64(approvedAt-task.ClaimedAt) / 1e9
			av.reputationMgr.RecordCompletion(
				crypto.AgentID(task.ClaimerID),
				task.Category,
				task.Budget,
				score.Overall,
				deliverySecs,
			)
		}
		log.Printf("auto-validator: APPROVED task %s (score: %.2f, claimer: %s)",
			task.ID, score.Overall, task.ClaimerID)
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
			log.Printf("auto-validator: could not settle %s: %v", item.EventID, err)
			continue
		}
		log.Printf("auto-validator: settled %s (value: %d)", item.EventID, item.Amount)
	}
}
