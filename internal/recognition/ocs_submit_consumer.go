package recognition

import (
	"context"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// OCSSubmitter is the minimal interface needed by OCSSubmitConsumer to add
// events to the OCS pending queue. *ocs.Engine satisfies this interface
// via its SubmitFromSync method.
type OCSSubmitter interface {
	SubmitFromSync(ev *event.Event) error
}

// OCSSubmitConsumer is a CommitConsumer that recognizes optimistic economic
// events (Transfer, Generation, TaskSettlement) and adds them to the OCS
// pending queue for verification and eventual settlement.
//
// This consumer runs in parallel with the existing syncHandler route in
// cmd/node/main.go. Both paths call engine.SubmitFromSync, which is
// idempotent — duplicate calls are safe and produce no side effects.
//
// Readiness: an event is ready immediately if it is one of the three OCS
// types and has SettlementState == Optimistic. No prerequisite deferral.
type OCSSubmitConsumer struct {
	submitter OCSSubmitter
}

// NewOCSSubmitConsumer creates a consumer wired to the given OCS engine.
func NewOCSSubmitConsumer(submitter OCSSubmitter) *OCSSubmitConsumer {
	return &OCSSubmitConsumer{submitter: submitter}
}

// Name returns the unique consumer identifier.
func (c *OCSSubmitConsumer) Name() string { return "ocs_submit" }

// Interested returns true for Transfer, Generation, and TaskSettlement events.
func (c *OCSSubmitConsumer) Interested(ev *event.Event) bool {
	switch ev.Type {
	case event.EventTypeTransfer, event.EventTypeGeneration, event.EventTypeTaskSettlement:
		return true
	}
	return false
}

// Ready checks whether the event should enter the OCS pending queue.
// Returns immediately ready for optimistic events. Non-optimistic events
// (already settled/adjusted) are not re-pended.
func (c *OCSSubmitConsumer) Ready(_ context.Context, ev *event.Event, _ ReadModel) (bool, string, error) {
	if ev.SettlementState != event.SettlementOptimistic {
		// Already settled — mark as ready (recognized but no action needed).
		// This prevents the event from being deferred and retried.
		return true, "", nil
	}
	return true, "", nil
}

// Consume adds the event to the OCS pending queue via SubmitFromSync.
// Idempotent: SubmitFromSync returns nil for already-pending or
// already-processed events.
func (c *OCSSubmitConsumer) Consume(_ context.Context, ev *event.Event) error {
	if ev.SettlementState != event.SettlementOptimistic {
		// Non-optimistic events don't need OCS pending tracking.
		// Mark as consumed without action.
		return nil
	}
	if err := c.submitter.SubmitFromSync(ev); err != nil {
		slog.Debug("recognition: ocs_submit consume failed (non-fatal)",
			"event_id", ev.ID, "type", ev.Type, "err", err)
		// SubmitFromSync returns ErrQueueFull when at capacity.
		// This is transient — the event will be retried on next activation.
		return err
	}
	return nil
}

// Compile-time assertion.
var _ CommitConsumer = (*OCSSubmitConsumer)(nil)
