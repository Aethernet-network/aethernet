// tracking.go defines the explicit event lifecycle state machine for
// the Fast Path pre-materialization network layer.
//
// Event lifecycle: Announced → Completed → Validated → Materialized.
//
//   - Announced: header received, causal structure known, body not yet fetched.
//     Relay happens at this stage — peers forward headers before bodies.
//   - Completed: body received and verified against BodyCommitment.
//     The event is now a full event.Event ready for validation.
//   - Validated: signature and causal ref checks passed. The event is ready
//     for materialization (dag.Add).
//   - Materialized: dag.Add succeeded. The event is in the canonical DAG.
//     Ledger side effects (settlement, OCS) fire after this.
//
// Each stage is forward-only. An event never moves backward.
package network

import (
	"encoding/json"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// EventStage is the lifecycle position of an event in the fast-path pipeline.
type EventStage uint8

const (
	// StageAnnounced means the header has been received but the body has not.
	// The event is eligible for header relay to other V2 peers.
	StageAnnounced EventStage = iota + 1

	// StageCompleted means the body has been received and matches the
	// BodyCommitment. The full event.Event can be reconstructed.
	StageCompleted

	// StageValidated means signature verification and causal-ref checks
	// have passed. The event is ready for dag.Add.
	StageValidated

	// StageMaterialized means dag.Add succeeded. The event is in the
	// canonical DAG and ledger side effects may fire.
	StageMaterialized
)

// String returns a human-readable stage name for logging.
func (s EventStage) String() string {
	switch s {
	case StageAnnounced:
		return "Announced"
	case StageCompleted:
		return "Completed"
	case StageValidated:
		return "Validated"
	case StageMaterialized:
		return "Materialized"
	default:
		return "Unknown"
	}
}

// EventTracking holds the mutable state of an event as it progresses through
// the fast-path pipeline. One tracking entry exists per event ID from the
// moment a header is admitted until materialization or TTL expiry.
type EventTracking struct {
	// Header is the causality-plane projection received at announcement.
	Header EventHeader

	// Stage is the current lifecycle position.
	Stage EventStage

	// ReceivedAt is the wall-clock time the header was first admitted.
	// Used for TTL expiry of stale announced items.
	ReceivedAt time.Time

	// CompletedAt is when the body was received. Zero until StageCompleted.
	CompletedAt time.Time

	// SourcePeer is the AgentID of the peer that first announced this event.
	// Used for peer scoring — if an announced header is never completed,
	// the source peer's reliability score decreases.
	SourcePeer crypto.AgentID

	// BodyRequested tracks whether a body fetch has been initiated.
	// Prevents duplicate fetch requests for the same event.
	BodyRequested bool

	// BodyAvailable is true when the body payload is present locally.
	// For locally-created events this is true from construction.
	// For remote events it becomes true after a verified body response.
	BodyAvailable bool

	// Body holds the verified payload bytes. Non-nil only after body
	// completion (BodyAvailable == true). Used to reconstruct the full
	// event.Event for validation and materialization.
	Body json.RawMessage

	// Reconstructed holds the full event.Event rebuilt from header + body
	// after validation passes. Non-nil only at StageValidated or later.
	Reconstructed *event.Event

	// MissingParents tracks causal refs that are not yet in the local DAG.
	// Populated when materialization fails with ErrMissingCausalRef.
	// Cleared when the missing parents are received and materialized.
	MissingParents map[event.EventID]struct{}

	// RepairRequested is true when a repair request has been sent for this
	// event's missing parents. Prevents duplicate repair requests.
	RepairRequested bool

	// RelayCount tracks how many peers this header has been relayed to.
	RelayCount int
}

// CanAdvanceTo returns true if the event can transition from its current
// stage to the target stage. Stages are strictly forward-only.
func (t *EventTracking) CanAdvanceTo(target EventStage) bool {
	return target > t.Stage
}

// AdvanceTo transitions the event to the target stage if valid.
// Returns true if the transition occurred, false if rejected.
func (t *EventTracking) AdvanceTo(target EventStage) bool {
	if !t.CanAdvanceTo(target) {
		return false
	}
	t.Stage = target
	if target == StageCompleted {
		t.CompletedAt = time.Now()
	}
	return true
}

// IsExpired returns true if the event has been in its current stage longer
// than ttl without advancing. Used by the GC to reclaim stale entries.
func (t *EventTracking) IsExpired(ttl time.Duration) bool {
	ref := t.ReceivedAt
	if !t.CompletedAt.IsZero() {
		ref = t.CompletedAt
	}
	return time.Since(ref) > ttl
}

// EventID returns the tracked event's ID for convenience.
func (t *EventTracking) EventID() event.EventID {
	return t.Header.EventID
}

// LatencyMS returns the time in milliseconds from header admission to now.
// Useful for logging materialization latency.
func (t *EventTracking) LatencyMS() int64 {
	return time.Since(t.ReceivedAt).Milliseconds()
}
