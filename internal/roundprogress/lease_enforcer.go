package roundprogress

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const defaultScanInterval = 5 * time.Second

// LeaseEnforcer periodically scans the snapshot store for expired leases.
// Expired leases remain in the store (for RoundPolicy to read) but are
// logged for observability.
//
// Lifecycle must be managed by the caller's signal wait loop, NOT by defer
// in a setup function (per lesson from commit 1cfb8ed).
type LeaseEnforcer struct {
	store        SnapshotStore
	scanInterval time.Duration
	roundIDs     func() []string // returns active round IDs to scan

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewLeaseEnforcer creates an enforcer. roundIDsFn returns the set of active
// round IDs to scan on each interval. If nil, no scanning occurs (useful for
// testing the enforcer lifecycle without active rounds).
func NewLeaseEnforcer(store SnapshotStore, scanInterval time.Duration, roundIDsFn func() []string) *LeaseEnforcer {
	if scanInterval <= 0 {
		scanInterval = defaultScanInterval
	}
	return &LeaseEnforcer{
		store:        store,
		scanInterval: scanInterval,
		roundIDs:     roundIDsFn,
	}
}

// Start launches the background scan goroutine.
func (le *LeaseEnforcer) Start() {
	le.ctx, le.cancel = context.WithCancel(context.Background())
	le.wg.Add(1)
	go le.scanLoop()
	slog.Info("roundprogress: lease enforcer started", "scan_interval", le.scanInterval)
}

// Stop cancels the context and waits for the scan goroutine to exit.
func (le *LeaseEnforcer) Stop() {
	if le.cancel != nil {
		le.cancel()
	}
	le.wg.Wait()
	slog.Info("roundprogress: lease enforcer stopped")
}

func (le *LeaseEnforcer) scanLoop() {
	defer le.wg.Done()
	ticker := time.NewTicker(le.scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-le.ctx.Done():
			return
		case <-ticker.C:
			le.scan()
		}
	}
}

func (le *LeaseEnforcer) scan() {
	if le.roundIDs == nil {
		return
	}

	nowUnix := time.Now().Unix()
	roundIDs := le.roundIDs()

	for _, roundID := range roundIDs {
		snaps, err := le.store.GetAllForRound(roundID)
		if err != nil {
			slog.Warn("roundprogress: lease scan error",
				"round", roundID, "err", err)
			continue
		}

		for _, snap := range snaps {
			if snap.CurrentPhase.IsTerminal() {
				continue // terminal phases have no lease
			}
			if snap.LeaseExpiryUnix > 0 && snap.LeaseExpiryUnix < nowUnix {
				slog.Info("roundprogress: stale lease detected",
					"round", snap.RoundID,
					"validator", snap.ValidatorID,
					"family", snap.AnalyzerFamily,
					"phase", snap.CurrentPhase,
					"expired_at", snap.LeaseExpiryUnix,
					"now", nowUnix,
				)
			}
		}
	}
}
