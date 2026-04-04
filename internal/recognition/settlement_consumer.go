package recognition

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// SettlementApplier is the minimal interface for applying settlement events.
// *settlement.Applicator satisfies this via its Apply method. Defined locally
// to avoid import cycles (recognition is Infrastructure, settlement is
// Infrastructure but we use a local interface for decoupling).
type SettlementApplier interface {
	// ApplyFromEvent applies a settlement from a raw DAG event. The
	// implementation must parse the payload and call the idempotent Apply.
	// Returns nil if already applied (idempotent).
	ApplyFromEvent(ev *event.Event) error
}

// settlementPayloadMini extracts the minimal fields needed from Settlement
// events for readiness checks without importing the settlement package.
type settlementPayloadMini struct {
	TargetEventID string `json:"target_event_id"`
	Verdict       string `json:"verdict"`
}

// SettlementConsumer is a CommitConsumer that recognizes Settlement DAG events
// and applies them to the ledger via the SettlementApplicator.
//
// This consumer runs in parallel with the existing syncHandler route.
// The applicator's Apply method is idempotent — duplicate calls from both
// paths are safe and produce no double-application.
//
// Readiness: a settlement event is immediately ready. The applicator handles
// deferred reconciliation internally (retries when target event is missing
// from the DAG).
type SettlementConsumer struct {
	applier SettlementApplier
}

// NewSettlementConsumer creates a consumer wired to the settlement applier.
func NewSettlementConsumer(applier SettlementApplier) *SettlementConsumer {
	return &SettlementConsumer{applier: applier}
}

// Name returns the unique consumer identifier.
func (c *SettlementConsumer) Name() string { return "settlement" }

// Interested returns true for Settlement events.
func (c *SettlementConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeSettlement
}

// Ready returns immediately ready. The applicator handles internal
// deferred reconciliation for settlements whose targets are not yet
// in the DAG.
func (c *SettlementConsumer) Ready(_ context.Context, _ *event.Event, _ ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume applies the settlement event. Idempotent: the applicator's
// applied set prevents double-application.
func (c *SettlementConsumer) Consume(_ context.Context, ev *event.Event) error {
	if err := c.applier.ApplyFromEvent(ev); err != nil {
		slog.Debug("recognition: settlement consume failed",
			"event_id", ev.ID, "err", err)
		return err
	}
	return nil
}

// Compile-time assertion.
var _ CommitConsumer = (*SettlementConsumer)(nil)

// SettlementApplierAdapter wraps a raw Apply function to satisfy
// the SettlementApplier interface. Used in wiring where the settlement
// applicator is available but the full interface is not imported.
type SettlementApplierAdapter struct {
	applyFn func(ev *event.Event) error
}

// NewSettlementApplierAdapter creates an adapter from a function that
// takes a raw event and applies the settlement.
func NewSettlementApplierAdapter(fn func(ev *event.Event) error) *SettlementApplierAdapter {
	return &SettlementApplierAdapter{applyFn: fn}
}

// ApplyFromEvent delegates to the wrapped function.
func (a *SettlementApplierAdapter) ApplyFromEvent(ev *event.Event) error {
	return a.applyFn(ev)
}

// ParseSettlementTarget extracts the target event ID from a Settlement
// event payload. Used for logging and diagnostics.
func ParseSettlementTarget(ev *event.Event) string {
	if ev.Payload == nil {
		return ""
	}
	var sp settlementPayloadMini
	if err := json.Unmarshal(ev.Payload, &sp); err != nil {
		return ""
	}
	return sp.TargetEventID
}
