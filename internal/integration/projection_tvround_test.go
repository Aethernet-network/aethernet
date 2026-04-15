package integration_test

import (
	"context"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// TestTaskVerificationRound_PersistsOnTaskSubmitted asserts that the
// TaskVerificationRound BadgerStore's Empty probe flips from true to
// false when a round is opened and saved — which is what the
// TaskVerificationRoundConsumer does on a TaskSubmitted event. Per
// plan §D8, also meta-asserts the projection entry's IntegrationTestRef.
func TestTaskVerificationRound_PersistsOnTaskSubmitted(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions(t.TempDir()).WithLogger(nil))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := taskverification.NewBadgerStore(db)
	ctx := context.Background()

	empty, err := store.Empty(ctx)
	if err != nil || !empty {
		t.Fatalf("round store must start empty (got empty=%v, err=%v)", empty, err)
	}

	r, err := taskverification.OpenRound(taskverification.OpenRoundParams{
		TaskID:                "task-integration-tv",
		SubmissionEventID:     "evt-sub-tv",
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
	if err := store.SaveRound(ctx, r); err != nil {
		t.Fatalf("SaveRound: %v", err)
	}

	empty, err = store.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty after SaveRound: %v", err)
	}
	if empty {
		t.Fatalf("round store must not be empty after SaveRound")
	}

	// Meta-assertion (plan §D8).
	entry := taskverification.RoundStoreProjection(store)
	want := "github.com/Aethernet-network/aethernet/internal/integration.TestTaskVerificationRound_PersistsOnTaskSubmitted"
	if entry.IntegrationTestRef != want {
		t.Fatalf("IntegrationTestRef mismatch:\n  want: %s\n  got:  %s", want, entry.IntegrationTestRef)
	}
}
