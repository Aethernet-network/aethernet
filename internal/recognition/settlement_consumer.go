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

// SettlementConsumer is a CommitConsumer that recognizes Settlement DAG events.
//
// F4B §5.2.4 shape: this consumer routes Settlement events through the
// CanonicalEventDispatcher's logical-key admission path (keyed by the
// payload's TargetEventID). Under the dispatcher-routed shape, canonical
// ledger mutation flows through
// dispatch.SettlementLogicalKeyConsumer.Apply — which derives the
// verdict from the cluster-uniform VoteRecord rather than from the
// triggering event's (potentially advisory) payload. See
// internal/dispatch/settlement_lk_consumer.go for the canonical
// effect path.
//
// Backward-compat: when no dispatcher is wired via SetDispatcher (e.g.,
// unit tests that use the legacy wiring), the consumer falls back to
// the F3-B direct-applier path. The fallback is gated by the
// dispatch:lint pragma on the legacy branch so the no-bypass CI check
// treats it as an explicitly-authorized exemption.
//
// Idempotency at the recognition surface: identical to the pre-F4B
// shape — the recognition Index enforces per-consumer idempotency
// (MarkRecognizedOnce); the dispatcher enforces per-(consumer, key)
// exactly-once in the logical-key path; and the applicator's applied
// set is the final anchor for ledger mutation.
//
// Readiness: a settlement event is immediately ready. The applicator
// handles deferred reconciliation internally (retries when target
// event is missing from the DAG). Under dispatcher routing, the
// dispatcher's own reserved-pending-prereqs path handles the same
// concern at the admission layer.
type SettlementConsumer struct {
	applier    SettlementApplier  // legacy direct-applier path; used only when dispatcher is unset
	dispatcher DispatcherAdmitter // F4B §5.2.4: primary route; nil → fall back to applier
}

// NewSettlementConsumer creates a consumer wired to the settlement applier.
// The applier is the backward-compat fallback used only when no dispatcher
// is attached via SetDispatcher. Production cmd/node wiring attaches a
// dispatcher; legacy tests that exercise the recognition path without
// dispatcher wiring fall through to the applier transparently.
func NewSettlementConsumer(applier SettlementApplier) *SettlementConsumer {
	return &SettlementConsumer{applier: applier}
}

// SetDispatcher wires the dispatcher for logical-key admission of
// Settlement events. When set, Consume calls dispatcher.Admit(ctx, ev)
// instead of applier.ApplyFromEvent(ev). Mirrors the same injection
// pattern as TaskVerificationConsensusConsumer.SetDispatcher.
//
// Safe to call before or after bus.Start(); the write is protected
// only by the happens-before edge established by the Go runtime's
// goroutine scheduling (cmd/node wires SetDispatcher before
// bus.Start(), the production ordering). Tests may set/unset at any
// time; the consumer reads the field on every Consume call (no
// caching).
func (c *SettlementConsumer) SetDispatcher(d DispatcherAdmitter) {
	c.dispatcher = d
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

// Consume routes the settlement event. Under F4B §5.2.4:
//
//   - If a dispatcher is attached, route via dispatcher.Admit. The
//     dispatcher's SettlementLogicalKeyConsumer derives the canonical
//     verdict from the cluster-uniform VoteRecord and invokes the
//     applicator at most once per TargetEventID.
//
//   - Else (legacy / test-only): fall back to the applier's
//     ApplyFromEvent path. The admitter-unset branch is the pragma-
//     authorized bypass of the canonical-effect-through-dispatcher
//     invariant (C-10); the no-bypass lint recognizes it via the
//     nearby dispatch:lint comment.
func (c *SettlementConsumer) Consume(ctx context.Context, ev *event.Event) error {
	if c.dispatcher != nil {
		if err := c.dispatcher.Admit(ctx, ev); err != nil {
			slog.Debug("recognition: settlement dispatcher admission failed",
				"event_id", ev.ID, "err", err)
			return err
		}
		return nil
	}
	// dispatch:lint legacy-direct-applier "F4B §5.2.4 pre-dispatcher backward-compat path; only fires when SetDispatcher was never called, e.g. recognition unit tests with legacy wiring; production cmd/node always sets the dispatcher before bus.Start()"
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
