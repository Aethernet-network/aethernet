// validation.go implements the Fast Path async validation stage.
//
// After body completion (StageCompleted), the validation worker reconstructs
// a full event.Event from the header + body, verifies the Ed25519 signature,
// and checks the EventID matches the content hash. If all checks pass, the
// event advances to StageValidated and is enqueued for materialization.
//
// This is a pre-screening step. dag.Add still enforces its own duplicate,
// causal-ref, and signature checks independently — validation does not
// replace the DAG's invariants.
package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// Validation errors.
var (
	ErrValidationNotCompleted      = errors.New("validation: event not at Completed stage")
	ErrValidationNoBody            = errors.New("validation: body not available")
	ErrValidationIDMismatch        = errors.New("validation: reconstructed EventID does not match header")
	ErrValidationBadSignature      = errors.New("validation: signature verification failed")
	ErrTrajectoryInvalidPayload    = errors.New("validation: trajectory commit has invalid payload")
	ErrTrajectoryInvalidCausalRefs = errors.New("validation: trajectory commit has invalid causal ref shape")
)

// ReconstructEvent rebuilds a full event.Event from an EventTracking entry
// that has reached StageCompleted with a verified body. The reconstructed
// event's ID is verified against the header's EventID to ensure consistency.
//
// The event is reconstructed with SettlementState = Optimistic (the default
// for newly received events). ID and Signature are set from the header.
func ReconstructEvent(t *EventTracking) (*event.Event, error) {
	if !t.BodyAvailable || t.Body == nil {
		return nil, ErrValidationNoBody
	}

	e := &event.Event{
		ID:              t.Header.EventID,
		Type:            t.Header.Type,
		CausalRefs:      t.Header.CausalRefs,
		Payload:         t.Body,
		AgentID:         t.Header.AgentID,
		CausalTimestamp: t.Header.CausalTimestamp,
		StakeAmount:     t.Header.StakeAmount,
		Signature:       t.Header.Signature,
		SettlementState: event.SettlementOptimistic,
	}

	// Verify the EventID matches what ComputeID would produce from the
	// canonical fields. This catches any tampering of the header's EventID.
	computedID, err := event.ComputeID(e)
	if err != nil {
		return nil, fmt.Errorf("validation: compute ID: %w", err)
	}
	if computedID != t.Header.EventID {
		return nil, fmt.Errorf("%w: header=%s computed=%s", ErrValidationIDMismatch, t.Header.EventID, computedID)
	}

	return e, nil
}

// ValidateEvent performs pre-materialization checks on a reconstructed event:
// signature verification, EventID consistency, and type-specific validation.
// This is advisory — dag.Add still enforces its own checks independently.
func ValidateEvent(e *event.Event) error {
	// Signature verification. Genesis events (empty CausalRefs) may be unsigned.
	isGenesis := len(e.CausalRefs) == 0
	if !isGenesis {
		if len(e.Signature) == 0 {
			return fmt.Errorf("%w: missing signature on non-genesis event %s", ErrValidationBadSignature, e.ID)
		}
		if !crypto.VerifyEvent(e) {
			return fmt.Errorf("%w: event %s", ErrValidationBadSignature, e.ID)
		}
	}

	// Type-specific validation.
	if e.Type == event.EventTypeTrajectoryCommit {
		if err := validateTrajectoryCommit(e); err != nil {
			return err
		}
	}

	return nil
}

// validateTrajectoryCommit performs trajectory-specific validation:
//   - Payload must parse as TrajectoryCommitPayload with valid fields
//   - Causal ref shape must be task-local:
//   - Root commit: exactly 1 ref (task claim event)
//   - Child commit: exactly 2 refs (task claim event + parent commit)
//   - Checkpoint blob presence is NOT required for Phase 1 validation.
//     The blob is an external artifact fetched by the trajectory service,
//     not by the Fast Path completion pipeline.
func validateTrajectoryCommit(e *event.Event) error {
	var payload event.TrajectoryCommitPayload
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return fmt.Errorf("%w: unmarshal failed: %v", ErrTrajectoryInvalidPayload, err)
	}

	if err := payload.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrTrajectoryInvalidPayload, err)
	}

	// Causal ref shape validation.
	if payload.IsRootCommit() {
		// Root commit: CausalRefs = [task_claim_event_id]
		if len(e.CausalRefs) != 1 {
			return fmt.Errorf("%w: root commit must have exactly 1 causal ref (task claim), got %d",
				ErrTrajectoryInvalidCausalRefs, len(e.CausalRefs))
		}
	} else {
		// Child commit: CausalRefs = [task_claim_event_id, parent_commit_id]
		if len(e.CausalRefs) != 2 {
			return fmt.Errorf("%w: child commit must have exactly 2 causal refs (claim + parent), got %d",
				ErrTrajectoryInvalidCausalRefs, len(e.CausalRefs))
		}
	}

	return nil
}

// validationWorker drains completeQ, reconstructs events, validates them,
// and advances to StageValidated. Runs as a goroutine started by Node.Start.
func (n *Node) validationWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-n.ingest.CompleteQ():
			if !ok {
				return
			}
			n.validateEvent(id)
		}
	}
}

// validateEvent performs reconstruction and validation for a single event.
func (n *Node) validateEvent(id event.EventID) {
	tracking := n.ingest.GetTracking(id)
	if tracking == nil {
		return
	}
	if tracking.Stage != StageCompleted {
		return
	}

	reconstructed, err := ReconstructEvent(tracking)
	if err != nil {
		slog.Warn("validation: reconstruction failed",
			"event_id", id, "err", err)
		return
	}

	if err := ValidateEvent(reconstructed); err != nil {
		slog.Warn("validation: event rejected",
			"event_id", id, "err", err)
		return
	}

	// Store the reconstructed event on the tracking entry for materialization.
	n.ingest.SetReconstructedEvent(id, reconstructed)

	if !n.ingest.MarkValidated(id) {
		slog.Debug("validation: could not advance to Validated", "event_id", id)
	}
}
