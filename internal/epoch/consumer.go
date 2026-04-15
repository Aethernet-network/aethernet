package epoch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// RoundCountConsumer subscribes to TaskVerificationConsensus events on the
// recognition fabric and applies each one to the RoundCounter. The
// counter's Apply is idempotent, so replay and cross-path double delivery
// are safe.
type RoundCountConsumer struct {
	counter *RoundCounter
}

// NewRoundCountConsumer constructs the consumer. counter must be non-nil.
func NewRoundCountConsumer(counter *RoundCounter) *RoundCountConsumer {
	if counter == nil {
		panic("epoch: NewRoundCountConsumer requires non-nil counter")
	}
	return &RoundCountConsumer{counter: counter}
}

// Name implements recognition.CommitConsumer.
func (c *RoundCountConsumer) Name() string { return "round_counter" }

// Interested implements recognition.CommitConsumer.
func (c *RoundCountConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskVerificationConsensus
}

// Ready implements recognition.CommitConsumer. Consensus events are always
// ready — they have no deferred prerequisites. Matches the pattern used by
// TaskVerificationConsensusConsumer (per lesson commit 0e6d8cc: deferred
// activation is not retroactive).
func (c *RoundCountConsumer) Ready(_ context.Context, _ *event.Event, _ recognition.ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume implements recognition.CommitConsumer. Idempotent via the
// counter's per-roundID marker.
func (c *RoundCountConsumer) Consume(ctx context.Context, ev *event.Event) error {
	var payload event.TaskVerificationConsensusPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("round_counter: unmarshal: %w", err)
	}
	if payload.RoundID == "" {
		// Defensive: a consensus event without a RoundID would be malformed
		// upstream; log and drop rather than error.
		slog.Warn("round_counter: consensus event missing RoundID", "event_id", ev.ID)
		return nil
	}
	epochChanged, err := c.counter.Apply(ctx, payload.RoundID)
	if err != nil {
		return fmt.Errorf("round_counter: apply: %w", err)
	}
	if epochChanged {
		slog.Info("round_counter: epoch advanced",
			"new_epoch", c.counter.CurrentEpoch(),
			"total_rounds", c.counter.Total(),
			"trigger_round_id", payload.RoundID,
		)
	}
	return nil
}

// Compile-time assertion.
var _ recognition.CommitConsumer = (*RoundCountConsumer)(nil)
