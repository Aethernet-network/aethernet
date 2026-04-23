package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// TestCalibration_AppliesOnRoundFinalization drives a
// TaskVerificationConsensus event through TaskVerificationConsensusConsumer
// and asserts (a) calibration counters for each distinct analyzer family
// in the round's votes are incremented, (b) a replay of the same event
// does not double-increment, and (c) the projection entry's
// IntegrationTestRef matches this test's fully-qualified symbol.
func TestCalibration_AppliesOnRoundFinalization(t *testing.T) {
	dbOpts := badger.DefaultOptions(t.TempDir()).WithLogger(nil)
	db, err := badger.Open(dbOpts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	tvStore := taskverification.NewBadgerStore(db)
	calibration := taskverification.NewCalibrationStore(db, taskverification.CalibrationConfig{DefaultThreshold: 100})
	ctx := context.Background()

	// Pre-condition.
	empty, err := calibration.Empty(ctx)
	if err != nil || !empty {
		t.Fatalf("calibration must start empty (got empty=%v, err=%v)", empty, err)
	}

	// Build an open round with votes across two distinct families plus a
	// duplicate to exercise the distinct-family guard.
	r, err := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID:                "task-integration-cal",
		SubmissionEventID:     "evt-sub-cal",
		WorkerID:              "worker-a",
		PosterID:              "poster-a",
		Category:              "research",
		DiversityFloor:        2,
		AcceptanceThresholdBP: 6000,
		DeadlineSeconds:       60,
		Now:                   time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("OpenRound: %v", err)
	}
	r.Votes = []taskverification.TaskVerificationVoteRecord{
		{ValidatorID: "v1", AnalyzerFamily: string(verification.FamilyDeterministicHeuristic), Verdict: taskverification.VerdictPass},
		{ValidatorID: "v2", AnalyzerFamily: string(verification.FamilyStatisticalStructural), Verdict: taskverification.VerdictPass},
		{ValidatorID: "v3", AnalyzerFamily: string(verification.FamilyDeterministicHeuristic), Verdict: taskverification.VerdictPass},
	}
	if err := tvStore.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	// Build the consumer with calibration wired; slashing nil.
	consumer := recognition.NewTaskVerificationConsensusConsumer(tvStore, nil, calibration)

	payload, _ := json.Marshal(event.TaskVerificationConsensusPayload{
		Version:              1,
		RoundID:              string(r.RoundID),
		TaskID:               r.TaskID,
		FinalVerdict:         "pass",
		FinalScoreBP:         7500,
		FinalizationTimeUnix: time.Now().Unix(),
	})
	ev := &event.Event{
		ID:      event.EventID("evt-integration-cal"),
		Type:    event.EventTypeTaskVerificationConsensus,
		Payload: payload,
	}

	// First apply.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("first Consume: %v", err)
	}

	empty, err = calibration.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty after apply: %v", err)
	}
	if empty {
		t.Fatalf("calibration must not be empty after apply")
	}

	// Per-family counts.
	gotDH, _ := calibration.Get(ctx, "research", verification.FamilyDeterministicHeuristic)
	gotSS, _ := calibration.Get(ctx, "research", verification.FamilyStatisticalStructural)
	if gotDH != 1 {
		t.Fatalf("det_heuristic count: want 1, got %d", gotDH)
	}
	if gotSS != 1 {
		t.Fatalf("stat_structural count: want 1, got %d", gotSS)
	}

	// Replay must be idempotent.
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("replay Consume: %v", err)
	}
	gotDH2, _ := calibration.Get(ctx, "research", verification.FamilyDeterministicHeuristic)
	gotSS2, _ := calibration.Get(ctx, "research", verification.FamilyStatisticalStructural)
	if gotDH2 != 1 {
		t.Fatalf("det_heuristic replay count: want 1, got %d", gotDH2)
	}
	if gotSS2 != 1 {
		t.Fatalf("stat_structural replay count: want 1, got %d", gotSS2)
	}

	// Meta-assertion (plan §D8).
	entry := taskverification.CalibrationProjection(calibration)
	want := "github.com/Aethernet-network/aethernet/internal/integration.TestCalibration_AppliesOnRoundFinalization"
	if entry.IntegrationTestRef != want {
		t.Fatalf("IntegrationTestRef mismatch:\n  want: %s\n  got:  %s", want, entry.IntegrationTestRef)
	}
}
