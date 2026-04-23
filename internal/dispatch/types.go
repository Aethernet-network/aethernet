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
//
//	v2: F4B logical-key admission. Adds Strategy field
//	    (content-hash | logical-key) and LogicalKey field. v1 records
//	    dual-read: missing Strategy field defaults to
//	    AdmissionStrategyContentHash (zero value), missing LogicalKey
//	    defaults to "" (correct for content-hash). Backward-compat
//	    preserved — existing v1 records on disk decode into the v2
//	    struct with Strategy=AdmissionStrategyContentHash and
//	    LogicalKey="", which is the correct interpretation for the
//	    content-hash flow they describe.
const AdmissionCurrentVersion uint32 = 2

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

// AdmissionStrategy is the consumer-declared admission-key derivation
// strategy. Per locked-invariant review §3.1 (C-3'): every consumer
// declares its strategy at registration; the dispatcher routes events
// into the appropriate admission flow based on which consumers are
// interested.
//
// Strategy is part of the persisted AdmissionRecord shape so storage-
// layer reads can defensively assert the on-disk record matches the
// running binary's understanding of the strategy enum (analogous to
// the SchemaVersion / State gating from F4A FINDINGs #5/#6).
type AdmissionStrategy uint8

const (
	// AdmissionStrategyContentHash is the F3-B content-hash flow:
	// admission key is BLAKE3(canonical-bytes(ev)). Default value (0)
	// preserves zero-value semantics for v1 records that predate the
	// Strategy field.
	AdmissionStrategyContentHash AdmissionStrategy = 0
	// AdmissionStrategyLogicalKey is the F4B logical-key flow:
	// admission key is "lk:" + consumer_name + ":" + LogicalKey.
	// One admission record per (consumer, key); Apply fires exactly
	// once per (consumer, key), regardless of how many byte-distinct
	// canonical events project into that key.
	AdmissionStrategyLogicalKey AdmissionStrategy = 1
)

// IsKnownAdmissionStrategy returns true iff s is one of the enum values
// defined above. The store layer's decode path rejects records with
// unknown strategy values via ErrUnknownAdmissionStrategy, gating
// mixed-binary clusters in the same shape as IsKnownAdmissionState.
//
// New AdmissionStrategy values must be added to the const block AND to
// this function in the same commit. Both are caught by the no-bypass
// dispatch lint suite via TestUnknownAdmissionStrategy_Rejected.
func IsKnownAdmissionStrategy(s AdmissionStrategy) bool {
	switch s {
	case AdmissionStrategyContentHash,
		AdmissionStrategyLogicalKey:
		return true
	}
	return false
}

func (s AdmissionStrategy) String() string {
	switch s {
	case AdmissionStrategyContentHash:
		return "content-hash"
	case AdmissionStrategyLogicalKey:
		return "logical-key"
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
// dispatcher state. For content-hash admissions (the F3-B default),
// keyed by the BLAKE3 hash of the event's canonical bytes (invariant
// C-3). For logical-key admissions (F4B, locked-invariant review §3.5),
// keyed by "lk:" + consumer_name + ":" + LogicalKey. Persisted in
// BadgerDB under the "dispatch:" prefix. Non-canonical node-local
// machinery per C-15.
//
// Strategy and LogicalKey were introduced in v2 (F4B). v1 records on
// disk decode into the v2 struct shape with Strategy=0
// (AdmissionStrategyContentHash, the zero-value default) and
// LogicalKey="", which is the correct interpretation for the
// content-hash flow they describe.
type AdmissionRecord struct {
	SchemaVersion             uint32                       `json:"schema_version"`
	Key                       string                       `json:"key"`
	Strategy                  AdmissionStrategy            `json:"strategy"`
	LogicalKey                LogicalKey                   `json:"logical_key,omitempty"`
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
