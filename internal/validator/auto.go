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
	"github.com/Aethernet-network/aethernet/internal/ocs"
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
	taskMgr   *tasks.TaskManager
	escrowMgr *escrow.Escrow
}

// NewAutoValidator creates an AutoValidator that polls engine every interval
// and approves all pending items as validatorID.
func NewAutoValidator(engine *ocs.Engine, validatorID crypto.AgentID, interval time.Duration) *AutoValidator {
	return &AutoValidator{
		engine:      engine,
		validatorID: validatorID,
		interval:    interval,
		stop:        make(chan struct{}),
	}
}

// SetTaskManager wires optional task marketplace components. When set, the
// auto-validator auto-approves submitted tasks older than 10 seconds.
func (av *AutoValidator) SetTaskManager(tm *tasks.TaskManager, e *escrow.Escrow) {
	av.taskMgr = tm
	av.escrowMgr = e
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

// processSubmittedTasks auto-approves marketplace tasks that have been in
// Submitted state for more than 10 seconds. This prevents tasks from sitting
// unapproved forever when the poster is offline or unresponsive.
func (av *AutoValidator) processSubmittedTasks() {
	submitted := av.taskMgr.Search(tasks.TaskStatusSubmitted, "", 0)
	cutoff := time.Now().UnixNano() - int64(10*time.Second)
	for _, task := range submitted {
		if task.SubmittedAt > cutoff {
			continue // submitted too recently
		}
		if err := av.taskMgr.ApproveTask(task.ID, av.validatorID); err != nil {
			log.Printf("auto-validator: could not approve task %s: %v", task.ID, err)
			continue
		}
		if av.escrowMgr != nil && task.ClaimerID != "" {
			if err := av.escrowMgr.Release(task.ID, crypto.AgentID(task.ClaimerID)); err != nil {
				log.Printf("auto-validator: could not release escrow for task %s: %v", task.ID, err)
			}
		}
		log.Printf("auto-validator: auto-approved task %s (claimer: %s)", task.ID, task.ClaimerID)
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
