// Package validator provides automatic settlement for AetherNet testnet nodes.
//
// On testnet, there are no external validator nodes, so transactions sit in
// Optimistic state forever unless something approves them. AutoValidator fills
// that role: it polls the OCS engine every interval and auto-approves every
// pending item, moving them from Optimistic → Settled within a single tick.
//
// This is TESTNET ONLY. On mainnet, real validator nodes earn fees by doing
// genuine verification work; auto-approval would defeat the trust model.
package validator

import (
	"log"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/ocs"
)

// AutoValidator periodically checks for pending OCS items and auto-approves
// them with a positive verdict. Intended for testnet use only.
type AutoValidator struct {
	engine      *ocs.Engine
	validatorID crypto.AgentID
	interval    time.Duration
	stop        chan struct{}
	once        sync.Once
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

// Start launches the background approval goroutine. It is safe to call once.
func (av *AutoValidator) Start() {
	go func() {
		ticker := time.NewTicker(av.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				av.processPending()
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
