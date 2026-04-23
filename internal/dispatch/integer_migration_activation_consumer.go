package dispatch

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// IntegerMigrationActivator is the subset of settler / generation-ledger
// behavior the consumer calls to flip mode. Both
// *settlement.VerificationConsensusSettler and
// *settlement.GenerationLedgerCalculator satisfy this via
// SetShadowMode(bool) methods added in Part E of the canonical-
// distribution-integer-migration workstream.
//
// Narrow interface so the consumer package does not import the
// settlement package; the production wiring in cmd/node/main.go passes
// concrete types through this interface.
type IntegerMigrationActivator interface {
	SetShadowMode(shadow bool)
}

// MigrationStateStore persists the integer-migration activation state so
// a node restart after activation restores the canonical flag state before
// any settlement runs. The adapter in cmd/node/main.go wraps *store.Store's
// generic PutMeta/GetMeta to satisfy this interface without adding
// migration-specific methods to the store package.
type MigrationStateStore interface {
	PutIntegerMigrationActivated(eventID event.EventID, emittedAtUnix int64) error
	GetIntegerMigrationActivated() (activated bool, eventID event.EventID, emittedAtUnix int64, err error)
}

// IntegerMigrationActivationConsumer is the Type A dispatcher consumer
// for EventTypeIntegerMigrationActivation events. On Apply it durably
// records the transition (persist first, per Principle 9) and then flips
// the settler and generation-ledger calculator out of shadow mode so
// subsequent settlements are integer-canonical.
//
// Idempotent: Apply on an already-activated system is a no-op that
// returns nil. Duplicate activation events after the first are safely
// ignored — activation is one-way and one-shot.
type IntegerMigrationActivationConsumer struct {
	settler   IntegerMigrationActivator
	genLedger IntegerMigrationActivator
	store     MigrationStateStore
}

// NewIntegerMigrationActivationConsumer constructs a consumer. All
// arguments required.
func NewIntegerMigrationActivationConsumer(
	settler IntegerMigrationActivator,
	genLedger IntegerMigrationActivator,
	store MigrationStateStore,
) *IntegerMigrationActivationConsumer {
	return &IntegerMigrationActivationConsumer{
		settler:   settler,
		genLedger: genLedger,
		store:     store,
	}
}

// Name returns the consumer identifier used as the per-consumer key in
// the dispatcher's admission record and registry.
func (c *IntegerMigrationActivationConsumer) Name() string {
	return "integer_migration_activation"
}

// Type reports the consumer taxonomy. Integer migration activation is a
// single-event authoritative local state transition — Type A.
func (c *IntegerMigrationActivationConsumer) Type() ConsumerType { return TypeA }

// Interested returns true only for EventTypeIntegerMigrationActivation.
// Other event types are ignored.
func (c *IntegerMigrationActivationConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeIntegerMigrationActivation
}

// Prerequisites returns nil — the activation event is self-contained and
// depends on no prior specific event.
func (c *IntegerMigrationActivationConsumer) Prerequisites(_ *event.Event) []event.EventID {
	return nil
}

// PrerequisiteSchemaVersion returns 1.
func (c *IntegerMigrationActivationConsumer) PrerequisiteSchemaVersion() uint32 { return 1 }

// Apply processes an activation event. Sequence:
//
//  1. Early idempotency: if the store reports already-activated, return
//     nil immediately without re-writing or re-flipping.
//  2. Unmarshal the payload. Malformed payload → error without mutation.
//  3. Persist the activation state (PutIntegerMigrationActivated). This
//     comes before flag mutation per Principle 9 (persist before publish /
//     act). A persist failure leaves the flags untouched so a retry can
//     recover.
//  4. Flip the settler and generation-ledger flags via SetShadowMode(false).
//  5. Log at Info level — activation is a significant protocol-state change
//     and operators should see it.
func (c *IntegerMigrationActivationConsumer) Apply(_ context.Context, ev *event.Event) error {
	// Step 1: early idempotency.
	if activated, _, _, _ := c.store.GetIntegerMigrationActivated(); activated {
		return nil
	}
	// Step 2: parse payload.
	payload, err := event.GetPayload[event.IntegerMigrationActivationPayload](ev)
	if err != nil {
		return fmt.Errorf("integer_migration_activation: unmarshal: %w", err)
	}
	// Step 3: persist before mutate.
	if err := c.store.PutIntegerMigrationActivated(ev.ID, payload.EmittedAtUnix); err != nil {
		return fmt.Errorf("integer_migration_activation: persist: %w", err)
	}
	// Step 4: flip flags.
	c.settler.SetShadowMode(false)
	c.genLedger.SetShadowMode(false)
	// Step 5: log the transition.
	slog.Info("integer_migration_activation: activated",
		"event_id", ev.ID,
		"emitting_agent", payload.EmittingAgent,
		"reason", payload.ActivationReason,
	)
	return nil
}

// RecoveryProbe returns RecoveryCompleted when positive evidence in the
// store shows this specific event has already been applied. Per C-14:
// evidence-based, monotonic, replay-safe; no wall-clock, no ephemeral
// memory, no external systems.
func (c *IntegerMigrationActivationConsumer) RecoveryProbe(_ context.Context, ev *event.Event) (RecoveryStatus, error) {
	activated, storedEventID, _, err := c.store.GetIntegerMigrationActivated()
	if err != nil {
		// Treat store errors as "not started" rather than failure —
		// absence of evidence is not evidence of absence (C-14).
		return RecoveryNotStarted, nil
	}
	if activated && storedEventID == ev.ID {
		return RecoveryCompleted, nil
	}
	return RecoveryNotStarted, nil
}
