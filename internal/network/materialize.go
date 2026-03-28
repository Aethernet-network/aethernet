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
	"errors"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/dag"
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
		slog.Warn("fastpath: materialize skipped — no reconstructed event",
			"event_id", id, "reason", "no_reconstructed_event")
		return
	}

	tracking := n.ingest.GetTracking(id)
	if tracking == nil || tracking.Stage != StageValidated {
		return
	}

	if err := n.dag.Add(ev); err != nil {
		if errors.Is(err, dag.ErrMissingCausalRef) {
			missing := make(map[event.EventID]struct{})
			for _, ref := range ev.CausalRefs {
				if _, getErr := n.dag.Get(ref); getErr != nil {
					missing[ref] = struct{}{}
				}
			}
			if len(missing) > 0 {
				n.ingest.SetMissingParents(id, missing)
				n.ingest.EnqueueRepair(id)
				slog.Info("fastpath: materialize blocked — missing parents, repair requested",
					"event_id", id,
					"type", ev.Type,
					"missing_parents", len(missing),
					"source_peer", tracking.SourcePeer,
					"reason", "missing_causal_refs",
				)
				return
			}
		}
		if errors.Is(err, dag.ErrDuplicateEvent) {
			// Duplicate — silently skip (normal during concurrent sync).
			n.ingest.Remove(id)
			return
		}
		slog.Warn("fastpath: materialize failed — dag.Add rejected",
			"event_id", id,
			"type", ev.Type,
			"source_peer", tracking.SourcePeer,
			"reason", "dag_add_failed",
			"err", err,
		)
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

	slog.Info("fastpath: event materialized",
		"event_id", id,
		"type", ev.Type,
		"source_peer", tracking.SourcePeer,
		"stage", "materialized",
		"latency_ms", tracking.LatencyMS(),
	)
}
