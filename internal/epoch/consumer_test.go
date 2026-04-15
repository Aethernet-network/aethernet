package epoch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

func makeConsensusEvent(t *testing.T, roundID string) *event.Event {
	t.Helper()
	payload, err := json.Marshal(event.TaskVerificationConsensusPayload{
		Version: 1,
		RoundID: roundID,
		TaskID:  "task-abc",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &event.Event{
		Type:    event.EventTypeTaskVerificationConsensus,
		Payload: payload,
	}
}

func TestConsumer_InterestedOnlyInConsensus(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	rc := NewRoundCountConsumer(c)

	consensusEv := makeConsensusEvent(t, "r1")
	if !rc.Interested(consensusEv) {
		t.Fatalf("must be Interested in TaskVerificationConsensus")
	}

	otherEv := &event.Event{Type: event.EventTypeTaskSubmitted}
	if rc.Interested(otherEv) {
		t.Fatalf("must NOT be Interested in TaskSubmitted")
	}
}

func TestConsumer_ReadyAlwaysTrue(t *testing.T) {
	c, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	rc := NewRoundCountConsumer(c)
	ready, key, err := rc.Ready(context.Background(), makeConsensusEvent(t, "r1"), nil)
	if err != nil {
		t.Fatalf("Ready error: %v", err)
	}
	if !ready || key != "" {
		t.Fatalf("Ready must be (true, \"\"), got (%v, %q)", ready, key)
	}
}

func TestConsumer_ConsumeIncrements(t *testing.T) {
	counter, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	rc := NewRoundCountConsumer(counter)
	ev := makeConsensusEvent(t, "r1")

	if err := rc.Consume(context.Background(), ev); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if counter.Total() != 1 {
		t.Fatalf("total after consume: want 1, got %d", counter.Total())
	}
}

func TestConsumer_ConsumeIdempotent(t *testing.T) {
	counter, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	rc := NewRoundCountConsumer(counter)
	ev := makeConsensusEvent(t, "r1")

	for i := 0; i < 3; i++ {
		if err := rc.Consume(context.Background(), ev); err != nil {
			t.Fatalf("consume #%d: %v", i, err)
		}
	}
	if counter.Total() != 1 {
		t.Fatalf("idempotent consume: want total 1, got %d", counter.Total())
	}
}

func TestConsumer_MalformedPayloadErrors(t *testing.T) {
	counter, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	rc := NewRoundCountConsumer(counter)
	ev := &event.Event{
		Type:    event.EventTypeTaskVerificationConsensus,
		Payload: []byte("not json"),
	}
	if err := rc.Consume(context.Background(), ev); err == nil {
		t.Fatalf("malformed payload must error")
	}
}

func TestConsumer_EmptyRoundIDDropsButDoesNotError(t *testing.T) {
	counter, err := NewRoundCounter(newTestDB(t))
	if err != nil {
		t.Fatalf("NewRoundCounter: %v", err)
	}
	rc := NewRoundCountConsumer(counter)
	ev := makeConsensusEvent(t, "")
	if err := rc.Consume(context.Background(), ev); err != nil {
		t.Fatalf("empty roundID must be dropped, not errored: got %v", err)
	}
	if counter.Total() != 0 {
		t.Fatalf("empty roundID must not increment")
	}
}

func TestConsumer_NilCounterPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("NewRoundCountConsumer(nil) must panic")
		}
	}()
	NewRoundCountConsumer(nil)
}
