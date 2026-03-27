// materialize.go implements the Fast Path materialization stage.
//
// After validation (StageValidated), the materialization worker calls dag.Add
// to insert the reconstructed event into the canonical DAG. dag.Add enforces
// its own invariants (duplicate detection, causal ref checks, signature
// verification) — materialization does not weaken or bypass those checks.
//
// After successful materialization, the syncHandler is called (same as the
// V1 path) so consensus, settlement, and application-layer routing all fire.
package network

import (
	"context"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// materializeWorker drains validateQ, calls dag.Add for each validated event,
// fires the syncHandler, and advances to StageMaterialized. Runs as a
// goroutine started by Node.Start.
func (n *Node) materializeWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-n.ingest.ValidateQ():
			if !ok {
				return
			}
			n.materializeEvent(id)
		}
	}
}

// materializeEvent inserts a validated event into the DAG and fires side effects.
func (n *Node) materializeEvent(id event.EventID) {
	ev := n.ingest.GetReconstructedEvent(id)
	if ev == nil {
		slog.Debug("materialize: no reconstructed event", "event_id", id)
		return
	}

	tracking := n.ingest.GetTracking(id)
	if tracking == nil || tracking.Stage != StageValidated {
		return
	}

	// dag.Add enforces its own invariants:
	// - Duplicate detection (ErrDuplicateEvent)
	// - Causal ref existence (ErrMissingCausalRef)
	// - Signature verification for non-genesis events (ErrInvalidSignature)
	//
	// The fast-path validation stage already checked signature and EventID
	// consistency, but dag.Add re-checks independently. This is defense-in-depth.
	if err := n.dag.Add(ev); err != nil {
		slog.Debug("materialize: dag.Add failed",
			"event_id", id, "err", err)
		// Not necessarily an error — ErrDuplicateEvent means the V1 sync path
		// or another fast-path worker already materialized this event.
		n.ingest.Remove(id)
		return
	}

	// Fire the syncHandler (same as the V1 MsgEvent path).
	n.mu.RLock()
	sh := n.syncHandler
	n.mu.RUnlock()
	if sh != nil {
		sh(ev)
	}

	n.ingest.MarkMaterialized(id)

	slog.Debug("materialize: event materialized via fast path",
		"event_id", id, "type", ev.Type)
}
