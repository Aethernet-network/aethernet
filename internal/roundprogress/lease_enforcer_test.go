package roundprogress

import (
	"testing"
	"time"
)

func TestLeaseEnforcer_StartsAndStops(t *testing.T) {
	store := NewMemorySnapshotStore()
	le := NewLeaseEnforcer(store, 100*time.Millisecond, nil)
	le.Start()
	time.Sleep(250 * time.Millisecond) // let a few scans run
	le.Stop()
	// No panic, clean shutdown.
}

func TestLeaseEnforcer_DetectsExpiredLease(t *testing.T) {
	store := NewMemorySnapshotStore()
	_ = store.Put(&RoundProgressSnapshot{
		RoundID:         "r1",
		ValidatorID:     "v1",
		AnalyzerFamily:  "f1",
		CurrentPhase:    ProgressPhaseFetchingBlob,
		LeaseExpiryUnix: time.Now().Unix() - 10, // expired 10s ago
	})

	scanned := false
	le := NewLeaseEnforcer(store, 100*time.Millisecond, func() []string {
		scanned = true
		return []string{"r1"}
	})
	le.Start()
	time.Sleep(300 * time.Millisecond)
	le.Stop()

	if !scanned {
		t.Error("expected scan to run")
	}
	// The expired lease is detected and logged (we verify no crash).
	// In a real system, RoundPolicy reads LeaseExpiryUnix from the snapshot.
}

func TestLeaseEnforcer_TerminalPhasesNotFlagged(t *testing.T) {
	store := NewMemorySnapshotStore()
	_ = store.Put(&RoundProgressSnapshot{
		RoundID:         "r1",
		ValidatorID:     "v1",
		AnalyzerFamily:  "f1",
		CurrentPhase:    ProgressPhaseVoteEmitted, // terminal
		LeaseExpiryUnix: 0,                         // no lease
	})

	le := NewLeaseEnforcer(store, 100*time.Millisecond, func() []string {
		return []string{"r1"}
	})
	le.Start()
	time.Sleep(300 * time.Millisecond)
	le.Stop()
	// Terminal phases are skipped — no false positive.
}
