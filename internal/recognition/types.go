// Package recognition implements the Causal Commit Bus (CCB) and Recognition
// Index for AetherNet's event lifecycle.
//
// The recognition fabric provides source-agnostic, replay-safe post-commit
// notification for every event that enters the causal DAG. It replaces ad-hoc
// caller-side notification with a uniform dispatch model:
//
//	dag.Add(ev) → CCB dispatch → per-consumer recognition → domain readiness
//
// Key properties:
//   - Fires after every durable dag.Add, regardless of source (local, remote,
//     repair, replay).
//   - Dispatch is asynchronous and bounded — never blocks the DAG write path.
//   - Recognition is idempotent — safe to replay on restart.
//   - Readiness is separated from recognition — a consumer may acknowledge an
//     event but defer action until prerequisites are met.
//
// Layer: Infrastructure — importable by any layer.
package recognition

import (
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// CommitSource identifies where an event entered the local DAG.
type CommitSource string

const (
	SourceLocal         CommitSource = "local"
	SourceRemoteFastPath CommitSource = "remote_fastpath"
	SourceRemoteLegacy  CommitSource = "remote_legacy"
	SourceRepair        CommitSource = "repair"
	SourceReplay        CommitSource = "replay"
)

// CommitRecord carries the metadata for a single DAG commit event.
// It is immutable after construction and safe for concurrent reads.
type CommitRecord struct {
	EventID   event.EventID   `json:"event_id"`
	EventType event.EventType `json:"event_type"`
	Source    CommitSource     `json:"source"`
	Replay   bool             `json:"replay"`

	// CommittedAt is the wall-clock time the event was committed to the DAG.
	CommittedAt time.Time `json:"committed_at"`
}

// RecognitionState tracks a single consumer's processing state for an event.
// Keyed by (ConsumerName, EventID) in the Recognition Index.
type RecognitionState struct {
	ConsumerName string        `json:"consumer_name"`
	EventID      event.EventID `json:"event_id"`

	// Recognized is true once the consumer has acknowledged the event.
	// This is set on the first Consume call, regardless of outcome.
	Recognized bool `json:"recognized"`

	// Ready is true when all prerequisites for domain action are met.
	// An event can be recognized but not ready (deferred).
	Ready bool `json:"ready"`

	// DeferredReason describes why the event is not yet ready.
	// Empty when Ready is true.
	DeferredReason string `json:"deferred_reason,omitempty"`

	// PrerequisiteKey is an opaque key identifying what the consumer is
	// waiting for. When the prerequisite is satisfied, targeted activation
	// can wake this specific deferred item without a global scan.
	// Example: "pending:evt123" for a vote waiting for the target event
	// to appear in OCS pending.
	PrerequisiteKey string `json:"prerequisite_key,omitempty"`

	// LastAttemptUnix is the Unix timestamp of the most recent Consume attempt.
	LastAttemptUnix int64 `json:"last_attempt_unix,omitempty"`

	// Attempts counts how many times Consume has been called for this item.
	Attempts int `json:"attempts,omitempty"`
}
