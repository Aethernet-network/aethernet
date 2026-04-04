package recognition

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// EvidenceReadinessMarker is the minimal interface for marking a task's
// evidence as ready. *tasks.TaskManager satisfies this via MarkEvidenceReady.
type EvidenceReadinessMarker interface {
	MarkEvidenceReady(taskID string)
}

// BlobChecker is the minimal interface for checking blob availability.
type BlobChecker interface {
	Has(ctx context.Context, hash string) (bool, error)
}

// Deferred reason codes for evidence readiness.
const (
	DeferEvidenceBlobMissing = "evidence_blob_missing"
)

// PrerequisiteKeyEvidenceBlob returns the prerequisite key for a task
// waiting on its evidence blob to arrive.
func PrerequisiteKeyEvidenceBlob(blobHash string) string {
	return "evidence_blob:" + blobHash
}

// taskSubmittedPayload extracts the minimal fields needed from TaskSubmitted
// events without importing the tasks package.
type taskSubmittedPayload struct {
	TaskID           string `json:"task_id"`
	EvidenceBodyHash string `json:"evidence_body_hash,omitempty"`
}

// EvidenceReadinessConsumer is a CommitConsumer that recognizes TaskSubmitted
// events and marks evidence as ready when the evidence blob is locally available.
//
// If the evidence blob hash is empty (legacy submission), the task is
// immediately evidence-ready. If the blob hash is present but the blob
// is not yet locally available, the consumer defers with a prerequisite
// key so it can be activated when the blob arrives.
//
// This consumer works alongside the existing EvidenceReady gating in the
// autovalidator — both paths are idempotent.
type EvidenceReadinessConsumer struct {
	marker  EvidenceReadinessMarker
	checker BlobChecker // optional; nil = assume ready
}

// NewEvidenceReadinessConsumer creates a consumer wired to the TaskManager
// and optionally a blob store for availability checking.
func NewEvidenceReadinessConsumer(marker EvidenceReadinessMarker, checker BlobChecker) *EvidenceReadinessConsumer {
	return &EvidenceReadinessConsumer{marker: marker, checker: checker}
}

// Name returns the unique consumer identifier.
func (c *EvidenceReadinessConsumer) Name() string { return "evidence_readiness" }

// Interested returns true for TaskSubmitted events only.
func (c *EvidenceReadinessConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskSubmitted
}

// Ready checks whether the evidence blob is locally available.
func (c *EvidenceReadinessConsumer) Ready(ctx context.Context, ev *event.Event, _ ReadModel) (bool, string, error) {
	tp, err := c.parsePayload(ev)
	if err != nil {
		// Unparseable — mark ready as no-op to prevent infinite deferral.
		return true, "", nil
	}

	// No evidence hash = legacy submission, immediately ready.
	if tp.EvidenceBodyHash == "" {
		return true, "", nil
	}

	// Check blob availability.
	if c.checker != nil {
		has, checkErr := c.checker.Has(ctx, tp.EvidenceBodyHash)
		if checkErr != nil {
			slog.Debug("recognition: evidence blob check failed",
				"task_id", tp.TaskID, "hash", tp.EvidenceBodyHash, "err", checkErr)
			// Transient error — defer and retry.
			return false, PrerequisiteKeyEvidenceBlob(tp.EvidenceBodyHash), nil
		}
		if !has {
			return false, PrerequisiteKeyEvidenceBlob(tp.EvidenceBodyHash), nil
		}
	}

	return true, "", nil
}

// Consume marks the task's evidence as ready. Idempotent:
// MarkEvidenceReady is a no-op if already ready.
func (c *EvidenceReadinessConsumer) Consume(_ context.Context, ev *event.Event) error {
	tp, err := c.parsePayload(ev)
	if err != nil {
		return nil // no-op for unparseable events
	}
	c.marker.MarkEvidenceReady(tp.TaskID)
	return nil
}

func (c *EvidenceReadinessConsumer) parsePayload(ev *event.Event) (*taskSubmittedPayload, error) {
	if ev.Payload == nil {
		return nil, errNilPayload
	}
	var tp taskSubmittedPayload
	if err := json.Unmarshal(ev.Payload, &tp); err != nil {
		return nil, err
	}
	return &tp, nil
}

var errNilPayload = errorf("nil payload")

func errorf(msg string) error { return &simpleError{msg} }

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

// Compile-time assertion.
var _ CommitConsumer = (*EvidenceReadinessConsumer)(nil)
