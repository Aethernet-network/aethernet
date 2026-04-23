// Package dispatch provides the CanonicalEventDispatcher primitive —
// the single architectural choke point between the recognition fabric
// and all canonical-event consumers. The dispatcher guarantees exactly-
// once successful application per (event, consumer) pair regardless of
// how many delivery paths the fabric uses.
//
// See docs/plans/2026-04-15-settlement-consensus-integrity-fix.md §4
// for the locked design (invariants C-1 through C-16) and
// docs/plans/2026-04-15-f3b-part-c-canonical-event-dispatcher.md for
// the implementation plan.
package dispatch

import "github.com/Aethernet-network/aethernet/internal/event"

// AdmissionCurrentVersion is the current schema version of AdmissionRecord
// recognized by this binary. The store layer's GetAdmission and
// AllAdmissions reject records with SchemaVersion > this value with
// ErrAdmissionSchemaTooNew, gating mixed-binary clusters per F4A FINDINGs
// #5 and #6 (admission-schema-no-gate / admission-state-no-gate).
//
// Bump rules: increment by 1 for any field addition, removal, or shape
// change. Document the bump in docs/architecture/schema-migration-discipline.md
// §2.1 dispatch: row, then implement dual-read (or forward-only cutover)
// in the same commit.
//
// History:
//
//	v1: initial F3-B shape (SchemaVersion, Key, State, DAGAnchor,
//	    PrerequisiteSchemaVersion, Consumers map, EventID, EventType,
//	    CreatedAtEpoch, MissingPrerequisites, EvidenceEmitted)
const AdmissionCurrentVersion uint32 = 1

// AdmissionState represents the lifecycle state of a canonical event's
// admission record in the dispatcher. Five states per invariant C-4;
// boolean-only state is forbidden.
type AdmissionState uint8

const (
	StateAbsent                 AdmissionState = 0 // no record in DB
	StateReservedPendingPrereqs AdmissionState = 1
	StateProcessing             AdmissionState = 2
	StateApplied                AdmissionState = 3
	StateFailedRetryable        AdmissionState = 4
)

// IsKnownAdmissionState returns true iff s is one of the enum values
// defined above. The store layer's decode path rejects records with
// unknown state values via ErrUnknownAdmissionState, gating mixed-
// binary clusters per F4A FINDING #6.
//
// New AdmissionState values must be added to the const block AND to this
// function in the same commit. Both are caught by the no-bypass dispatch
// lint suite via TestUnknownAdmissionState_Rejected.
func IsKnownAdmissionState(s AdmissionState) bool {
	switch s {
	case StateAbsent,
		StateReservedPendingPrereqs,
		StateProcessing,
		StateApplied,
		StateFailedRetryable:
		return true
	}
	return false
}

func (s AdmissionState) String() string {
	switch s {
	case StateAbsent:
		return "absent"
	case StateReservedPendingPrereqs:
		return "reserved-pending-prerequisites"
	case StateProcessing:
		return "processing"
	case StateApplied:
		return "applied"
	case StateFailedRetryable:
		return "failed-retryable"
	default:
		return "unknown"
	}
}

// PerConsumerStatus tracks the disposition of a single consumer for a
// single event within the admission record.
type PerConsumerStatus uint8

const (
	ConsumerPending         PerConsumerStatus = 0
	ConsumerApplied         PerConsumerStatus = 1
	ConsumerFailedRetryable PerConsumerStatus = 2
)

func (s PerConsumerStatus) String() string {
	switch s {
	case ConsumerPending:
		return "pending"
	case ConsumerApplied:
		return "applied"
	case ConsumerFailedRetryable:
		return "failed-retryable"
	default:
		return "unknown"
	}
}

// ConsumerType is the taxonomy category from §12 of the locked design.
// Determines which conformance template applies and which additional
// invariants (Invariant 8.1, 8.2) the consumer must satisfy.
type ConsumerType uint8

const (
	TypeA ConsumerType = iota + 1 // Single-event projection (e.g., settlement, reputation evidence)
	TypeB                         // Multi-event state-machine (e.g., challenge path)
	TypeC                         // Externalization (e.g., trajectory integration, data ingestion)
	TypeD                         // Deadline/deferred (e.g., dispute deadline)
)

func (t ConsumerType) String() string {
	switch t {
	case TypeA:
		return "TypeA(single-event-projection)"
	case TypeB:
		return "TypeB(multi-event-state-machine)"
	case TypeC:
		return "TypeC(externalization)"
	case TypeD:
		return "TypeD(deadline-deferred)"
	default:
		return "unknown"
	}
}

// RecoveryStatus is returned by Consumer.RecoveryProbe to indicate
// whether Apply completed for an event during a prior invocation
// interrupted by a crash.
type RecoveryStatus uint8

const (
	RecoveryNotStarted RecoveryStatus = iota
	RecoveryCompleted
)

// AdmissionRecord is the on-disk representation of a canonical event's
// dispatcher state. Keyed by the BLAKE3 hash of the event's canonical
// bytes (invariant C-3). Persisted in BadgerDB under the "dispatch:"
// prefix. Non-canonical node-local machinery per C-15.
type AdmissionRecord struct {
	SchemaVersion             uint32                       `json:"schema_version"`
	Key                       string                       `json:"key"`
	State                     AdmissionState               `json:"state"`
	DAGAnchor                 event.EventID                `json:"dag_anchor"`
	PrerequisiteSchemaVersion uint32                       `json:"prerequisite_schema_version"`
	Consumers                 map[string]PerConsumerStatus `json:"consumers"`
	EventID                   event.EventID                `json:"event_id"`
	EventType                 string                       `json:"event_type"`
	CreatedAtEpoch            uint64                       `json:"created_at_epoch"`

	// MissingPrerequisites lists EventIDs that are DAG-reachable from the
	// triggering event but not yet projected locally. Updated on each re-check.
	// Empty means all prerequisites are satisfied.
	MissingPrerequisites []event.EventID `json:"missing_prerequisites,omitempty"`

	// EvidenceEmitted is true once a PrerequisiteWithholding canonical evidence
	// event has been emitted for this admission record. Prevents duplicate
	// evidence emission per D-5.
	EvidenceEmitted bool `json:"evidence_emitted,omitempty"`
}

// computeTopLevelState derives the admission record's top-level state
// from per-consumer statuses. Per C-12: applied only when every consumer
// is applied; failed-retryable if any consumer is failed-retryable.
func computeTopLevelState(consumers map[string]PerConsumerStatus) AdmissionState {
	allApplied := true
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, status := range consumers {
		switch status {
		case ConsumerFailedRetryable:
			return StateFailedRetryable
		case ConsumerPending:
			allApplied = false
		}
	}
	if allApplied && len(consumers) > 0 {
		return StateApplied
	}
	return StateProcessing
}
