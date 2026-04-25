package epoch

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// Sentinel errors surfaced by BoundaryAdmissionValidator. Wrapped via
// dag.ErrCrossCheckRejected at the dag.Add boundary.
var (
	// ErrInvalidPayloadVersion: payload.Version != 1.
	ErrInvalidPayloadVersion = errors.New("epoch_boundary: invalid payload version")

	// ErrInvalidEpoch: payload.Epoch < 1 (epoch 0 has no boundary by §4.2).
	ErrInvalidEpoch = errors.New("epoch_boundary: invalid epoch (must be >= 1)")

	// ErrTriggerEventIDMissing: payload.TriggerEventID is not in the DAG.
	// Distinct from a materialization-lag deferral (which would appear at a
	// different layer); at admission time, missing TriggerEventID means the
	// emitter referenced an event that doesn't exist canonically.
	ErrTriggerEventIDMissing = errors.New("epoch_boundary: trigger event ID not found in DAG")

	// ErrTriggerEventWrongType: TriggerEventID exists but is not a
	// TaskVerificationConsensus event (the only canonical trigger source).
	ErrTriggerEventWrongType = errors.New("epoch_boundary: trigger event must be TaskVerificationConsensus")

	// ErrEpochMismatch: payload.Epoch != CountAncestorsByType(TriggerEventID,
	// EpochBoundary) + 1. Either Byzantine emission with a wrong epoch claim
	// or an honest race where another EpochBoundary landed first; both reject
	// at admission.
	ErrEpochMismatch = errors.New("epoch_boundary: payload epoch does not match canonical ancestor count")

	// ErrThresholdNotCrossed: payload.Epoch * EpochLength !=
	// CountAncestorsByType(TriggerEventID, TaskVerificationConsensus) + 1.
	// The TVConsensus event the boundary references does not actually sit
	// at the canonical_tvc_rank corresponding to the claimed epoch. Honest
	// emitter math bug or Byzantine emission.
	ErrThresholdNotCrossed = errors.New("epoch_boundary: trigger event does not cross the canonical epoch threshold")
)

// BoundaryAdmissionValidator implements the F5 5B canonical-epoch
// sub-spec v2.2 §1.4 admission cross-check for EventTypeEpochBoundary.
// Pure function of (event, canonical DAG state). No side effects, no
// I/O, no goroutines — runs synchronously under the dag.Add write lock
// via the restricted-API discipline of WhileLockedReader.
//
// Validates (in order):
//
//  1. Payload unmarshals as EpochBoundaryPayload.
//  2. Payload.Version == 1.
//  3. Payload.Epoch >= 1.
//  4. Payload.TriggerEventID exists in the DAG and Type ==
//     EventTypeTaskVerificationConsensus.
//  5. Payload.Epoch == CountAncestorsByType(TriggerEventID,
//     EventTypeEpochBoundary) + 1 (canonical epoch-count cross-check).
//  6. Payload.Epoch * EpochLength == CountAncestorsByType(
//     TriggerEventID, EventTypeTaskVerificationConsensus) + 1
//     (canonical threshold-crossing cross-check).
//
// Signature validation (§1.4 last bullet) is performed by dag.Add's
// existing crypto.VerifyEvent step before the cross-check fires — this
// validator does NOT re-verify signatures. The signer-in-canonical-
// validator-snapshot-at-TriggerEventID's-position binding is intentional
// sub-scope per FORWARD_NOTES.md §2: requires snapshot infrastructure
// owned by the locked Reputation-and-Consensus-Integrity workstream;
// implementation deferred until that infrastructure ships. The
// canonical-state checks (1-6 above) close the D-1 / canonicality
// surface; signer binding is an attribution / slashing surface
// orthogonal to settlement correctness.
//
// Returns nil to admit; a wrapped sentinel error to reject. The dag.Add
// boundary further wraps with dag.ErrCrossCheckRejected.
func BoundaryAdmissionValidator(ev *event.Event, reader dag.WhileLockedReader) error {
	if ev == nil {
		return errors.New("epoch_boundary: nil event passed to validator")
	}
	if ev.Type != event.EventTypeEpochBoundary {
		// Should not happen — dag.Add only invokes validators for matching
		// types — but defensive against future wiring bugs.
		return fmt.Errorf("epoch_boundary: validator called on wrong event type %q", ev.Type)
	}

	var payload event.EpochBoundaryPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("epoch_boundary: payload unmarshal: %w", err)
	}

	// 2. Version pinned at 1 for F5 ship.
	if payload.Version != 1 {
		return fmt.Errorf("%w: got %d, want 1", ErrInvalidPayloadVersion, payload.Version)
	}

	// 3. Epoch numbering starts at 1; epoch 0 has no boundary.
	if payload.Epoch < 1 {
		return fmt.Errorf("%w: got %d", ErrInvalidEpoch, payload.Epoch)
	}

	// 4. TriggerEventID must exist in the DAG and be a TVConsensus event.
	trigger, err := reader.GetWhileLocked(payload.TriggerEventID)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTriggerEventIDMissing, payload.TriggerEventID, err)
	}
	if trigger.Type != event.EventTypeTaskVerificationConsensus {
		return fmt.Errorf("%w: got %q for trigger %s", ErrTriggerEventWrongType, trigger.Type, payload.TriggerEventID)
	}

	// 5. Canonical epoch-count cross-check: the claim that this is
	// EpochBoundary(N) is true iff N-1 EpochBoundary events are canonical
	// ancestors of the trigger.
	priorBoundaries, err := reader.CountAncestorsByTypeWhileLocked(payload.TriggerEventID, event.EventTypeEpochBoundary)
	if err != nil {
		// CountAncestorsByType returning ErrEventNotFound during admission
		// indicates the trigger or one of its ancestors is missing from
		// the local DAG — but the trigger lookup above succeeded, so any
		// ancestor-traversal failure is a defensive case (CausalRefs
		// invariant violation in the DAG itself, which dag.Add normally
		// rejects). Reject to be safe.
		return fmt.Errorf("epoch_boundary: count epoch-boundary ancestors: %w", err)
	}
	if payload.Epoch != priorBoundaries+1 {
		return fmt.Errorf("%w: payload.Epoch=%d, canonical_count+1=%d", ErrEpochMismatch, payload.Epoch, priorBoundaries+1)
	}

	// 6. Canonical threshold-crossing cross-check: the trigger must be at
	// canonical_tvc_rank = N * EpochLength (where N = payload.Epoch).
	// canonical_tvc_rank(E) = CountAncestorsByType(E, TVConsensus) + 1.
	priorTVC, err := reader.CountAncestorsByTypeWhileLocked(payload.TriggerEventID, event.EventTypeTaskVerificationConsensus)
	if err != nil {
		return fmt.Errorf("epoch_boundary: count TVConsensus ancestors: %w", err)
	}
	expectedTVCAncestors := payload.Epoch*EpochLength - 1 // because rank = ancestors + 1
	if priorTVC != expectedTVCAncestors {
		return fmt.Errorf("%w: payload.Epoch=%d implies %d TVC ancestors, got %d (canonical_tvc_rank=%d, want %d)",
			ErrThresholdNotCrossed,
			payload.Epoch,
			expectedTVCAncestors,
			priorTVC,
			priorTVC+1,
			payload.Epoch*EpochLength,
		)
	}

	return nil
}
