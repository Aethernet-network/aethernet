package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aethernet-network/aethernet/internal/epoch"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// TestRoundCounter_IncrementsOnConsensus drives a TaskVerificationConsensus
// event through the RoundCountConsumer and asserts the round counter's
// Empty probe flips from true to false. Also asserts the projection
// entry's IntegrationTestRef matches the fully-qualified name of this
// test symbol (plan §D8 meta-assertion).
func TestRoundCounter_IncrementsOnConsensus(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions(t.TempDir()).WithLogger(nil))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	counter, err := epoch.NewRoundCounter(db)
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}

	ctx := context.Background()
	empty, err := counter.Empty(ctx)
	if err != nil || !empty {
		t.Fatalf("pre-condition: counter must be empty (got empty=%v, err=%v)", empty, err)
	}

	// Drive a real TaskVerificationConsensus event through the consumer.
	payload, _ := json.Marshal(event.TaskVerificationConsensusPayload{
		Version: 1,
		RoundID: "round-integration-1",
		TaskID:  "task-integration-1",
	})
	ev := &event.Event{
		Type:    event.EventTypeTaskVerificationConsensus,
		Payload: payload,
	}
	consumer := epoch.NewRoundCountConsumer(counter)
	if err := consumer.Consume(ctx, ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	empty, err = counter.Empty(ctx)
	if err != nil {
		t.Fatalf("Empty after consume: %v", err)
	}
	if empty {
		t.Fatalf("counter must not be empty after consensus event")
	}

	// Meta-assertion (plan §D8): IntegrationTestRef names this test.
	entry := epoch.RoundCounterProjection(counter)
	want := "github.com/Aethernet-network/aethernet/internal/integration.TestRoundCounter_IncrementsOnConsensus"
	if entry.IntegrationTestRef != want {
		t.Fatalf("IntegrationTestRef mismatch:\n  want: %s\n  got:  %s", want, entry.IntegrationTestRef)
	}
}
