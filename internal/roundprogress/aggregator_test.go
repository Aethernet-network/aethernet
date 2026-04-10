package roundprogress

import (
	"sync"
	"testing"
)

func newTestAggregator() (*ProgressAggregator, *MemorySnapshotStore) {
	store := NewMemorySnapshotStore()
	rl := NewRateLimiter(10)
	agg := NewProgressAggregator(store, rl)
	return agg, store
}

func TestAggregator_FirstUpdateCreatesSnapshot(t *testing.T) {
	agg, store := newTestAggregator()

	update := &ProgressUpdate{
		RoundID:            "r1",
		ValidatorID:        "v1",
		AnalyzerFamily:     "fam-a",
		Phase:              ProgressPhaseAcknowledged,
		ProgressGeneration: 1,
		ReasonCode:         ReasonCodeStartingRound,
	}
	if err := agg.Apply(update, 1000); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	snap, _ := store.Get("r1", "v1", "fam-a")
	if snap == nil {
		t.Fatal("expected snapshot to be created")
	}
	if snap.CurrentPhase != ProgressPhaseAcknowledged {
		t.Errorf("phase = %v, want Acknowledged", snap.CurrentPhase)
	}
}

func TestAggregator_ValidForwardTransition(t *testing.T) {
	agg, store := newTestAggregator()

	// Initial.
	_ = agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseAcknowledged, ProgressGeneration: 1,
	}, 1000)

	// Forward transition.
	err := agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseFetchingBlob, ProgressGeneration: 2,
		ProgressEvidence: [32]byte{1},
	}, 1020) // 20s later, within rate limit

	if err != nil {
		t.Fatalf("Apply forward transition: %v", err)
	}

	snap, _ := store.Get("r1", "v1", "f")
	if snap.CurrentPhase != ProgressPhaseFetchingBlob {
		t.Errorf("phase = %v, want FetchingBlob", snap.CurrentPhase)
	}
	if snap.ProgressGeneration != 2 {
		t.Errorf("generation = %d, want 2", snap.ProgressGeneration)
	}
}

func TestAggregator_BackwardsTransitionRejected(t *testing.T) {
	agg, _ := newTestAggregator()

	_ = agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseAnalyzing, ProgressGeneration: 1,
	}, 1000)

	err := agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseFetchingBlob, ProgressGeneration: 2, // backwards phase
	}, 1020)

	if err == nil {
		t.Error("expected error for backwards transition")
	}
}

func TestAggregator_StaleGenerationRejected(t *testing.T) {
	agg, _ := newTestAggregator()

	_ = agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseFetchingBlob, ProgressGeneration: 5,
	}, 1000)

	err := agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseAnalyzing, ProgressGeneration: 3, // stale
	}, 1020)

	if err == nil {
		t.Error("expected error for stale generation")
	}
}

func TestAggregator_RateLimited(t *testing.T) {
	agg, _ := newTestAggregator()

	_ = agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseAcknowledged, ProgressGeneration: 1,
	}, 1000)

	// Second update within 10s rate limit window.
	err := agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseFetchingBlob, ProgressGeneration: 2,
	}, 1005) // only 5s later

	if err == nil {
		t.Error("expected rate limit rejection")
	}
}

func TestAggregator_ETAClamped(t *testing.T) {
	agg, store := newTestAggregator()

	_ = agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseFetchingBlob, ProgressGeneration: 1,
		EstimatedReadyUnix: 2000, // way beyond now+30s
	}, 1000)

	snap, _ := store.Get("r1", "v1", "f")
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	// ETA should be clamped to now+30 = 1030
	if snap.EstimatedReadyUnix != 1030 {
		t.Errorf("ETA = %d, want 1030 (clamped)", snap.EstimatedReadyUnix)
	}
}

func TestAggregator_AnomalyCounterIncrements(t *testing.T) {
	agg, _ := newTestAggregator()

	_ = agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseAnalyzing, ProgressGeneration: 5,
	}, 1000)

	// Backwards transition — should increment anomaly.
	_ = agg.Apply(&ProgressUpdate{
		RoundID: "r1", ValidatorID: "v1", AnalyzerFamily: "f",
		Phase: ProgressPhaseFetchingBlob, ProgressGeneration: 6,
	}, 1020)

	if count := agg.AnomalyCount("v1"); count != 1 {
		t.Errorf("anomaly count = %d, want 1", count)
	}
}

func TestAggregator_ConcurrentApply(t *testing.T) {
	agg, _ := newTestAggregator()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = agg.Apply(&ProgressUpdate{
				RoundID:            "r1",
				ValidatorID:        "v1",
				AnalyzerFamily:     "f",
				Phase:              ProgressPhaseAcknowledged,
				ProgressGeneration: uint64(i),
			}, int64(1000+i*20))
		}(i)
	}
	wg.Wait()
}
