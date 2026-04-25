package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// EpochBoundaryLogicalKeyConsumer is the F5 5B canonical-epoch
// sub-spec §2.2 Candidate A logical-key consumer for EpochBoundary
// admission dedup.
//
// **Logical key = Payload.Epoch (uint64), serialized as decimal string.**
// NOT content-hash. Per sub-spec §12.6(i) discovery-tax prediction:
// content-hash dedup is the natural default but it does NOT collapse
// multi-emit because each emitter's signature differs (per §1.5 the
// emitter's AgentID is in the content-hash preimage, so distinct
// validators produce distinct content-hashes for the same canonical
// EpochBoundary(N)). Keying on Epoch converges all emissions to ONE
// canonical EpochBoundary per epoch.
//
// Apply is a no-op: all canonical-state validation already happened at
// dag.Add admission time via the BoundaryAdmissionValidator (sub-spec
// v2.2 §1.4.1 admission-cross-check mechanism). The only purpose of
// this consumer is to provide the logical-key dedup gate; once the
// first EpochBoundary(N) is admitted, the dispatcher's per-(consumer,
// key) state machine ensures no second Apply fires.
type EpochBoundaryLogicalKeyConsumer struct{}

// NewEpochBoundaryLogicalKeyConsumer constructs the consumer.
// Stateless; one per node.
func NewEpochBoundaryLogicalKeyConsumer() *EpochBoundaryLogicalKeyConsumer {
	return &EpochBoundaryLogicalKeyConsumer{}
}

// Name implements LogicalKeyConsumer. Distinct from
// "tv_consensus_settlement_lk" so admission-store records for the two
// strategies never collide on name.
func (c *EpochBoundaryLogicalKeyConsumer) Name() string {
	return "epoch_boundary_lk"
}

// Interested implements LogicalKeyConsumer. Subscribes to
// EpochBoundary events.
func (c *EpochBoundaryLogicalKeyConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeEpochBoundary
}

// Key projects the event's Payload.Epoch as the logical admission key.
//
// Per sub-spec §12.6(i): Epoch (NOT content-hash) is the dedup key.
// Multiple validators emitting EpochBoundary(N) for the same trigger
// produce events with the same Epoch but different content-hashes
// (AgentID differs in preimage); keying on Epoch causes the dispatcher
// to admit only the first arrival per Epoch and silently drop the rest.
//
// An unparsable payload is a programming bug; surface to the dispatcher
// as an error so it logs loudly. Cannot happen for events that passed
// admission (BoundaryAdmissionValidator already validated payload
// shape) but kept defensive.
func (c *EpochBoundaryLogicalKeyConsumer) Key(ev *event.Event) (LogicalKey, error) {
	var payload event.EpochBoundaryPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return "", fmt.Errorf("epoch_boundary_lk: unmarshal payload: %w", err)
	}
	if payload.Epoch == 0 {
		return "", errors.New("epoch_boundary_lk: payload.Epoch == 0 (sub-spec §4.2 forbids epoch 0 boundary)")
	}
	// Decimal-string serialization of uint64. Stable canonical form for
	// the LogicalKey opaque-string contract.
	return LogicalKey(strconv.FormatUint(payload.Epoch, 10)), nil
}

// RoundState implements LogicalKeyConsumer. EpochBoundary admission has
// no underlying canonical state to query — the canonical-cross-check
// at dag.Add already established validity. Returns the empty
// RoundState; IsComplete uses only the LogicalKey field.
func (c *EpochBoundaryLogicalKeyConsumer) RoundState(_ context.Context, key LogicalKey) (RoundState, error) {
	return RoundState{LogicalKey: key}, nil
}

// IsComplete implements LogicalKeyConsumer. Always true: an
// EpochBoundary event admitted past dag.Add (and thus past the
// admission cross-check) is by definition canonically valid; no further
// underlying-state accumulation is required for canonical outcome
// derivation. The dispatcher's per-(consumer, key) state machine
// handles the dedup gate.
func (c *EpochBoundaryLogicalKeyConsumer) IsComplete(_ RoundState) (bool, error) {
	return true, nil
}

// DeriveOutcome implements LogicalKeyConsumer. Returns an empty
// Outcome — EpochBoundary is not a verdict-bearing event; its
// canonical effect is its presence in the DAG (counted by
// CountAncestorsByType). No verdict, no participants.
func (c *EpochBoundaryLogicalKeyConsumer) DeriveOutcome(_ RoundState) (Outcome, error) {
	return Outcome{}, nil
}

// Apply implements LogicalKeyConsumer. No-op: the canonical effect of
// EpochBoundary(N) is already in place once the event is admitted to
// the DAG (which happens before Apply is invoked). The
// LogicalKeyConsumer plumbing exists solely to provide the dedup gate
// keyed on Epoch.
//
// If a future workstream introduces a side-effect that should fire
// once per canonical EpochBoundary (e.g., snapshot emission per
// sub-spec §5.1), this is the hook to extend.
func (c *EpochBoundaryLogicalKeyConsumer) Apply(_ context.Context, _ event.EventID, _ LogicalKey, _ Outcome) error {
	return nil
}

// RecoveryProbe implements LogicalKeyConsumer. Returns
// RecoveryCompleted unconditionally for any logical key the dispatcher
// asks about: the canonical effect of EpochBoundary(N) is its DAG
// presence, and the DAG's durability layer (BadgerDB write-through)
// already recovers the event itself across crashes. There is no
// per-EpochBoundary side-effect that could be left half-done by a
// crash, so "Apply ran" and "Apply not yet started" are observationally
// identical for this consumer.
func (c *EpochBoundaryLogicalKeyConsumer) RecoveryProbe(_ context.Context, _ LogicalKey) (RecoveryStatus, error) {
	return RecoveryCompleted, nil
}

// Compile-time assertion.
var _ LogicalKeyConsumer = (*EpochBoundaryLogicalKeyConsumer)(nil)
