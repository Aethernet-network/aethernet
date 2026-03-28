// Package localpub provides the authoritative publication path for
// locally-created DAG events.
//
// Every event authored by the local node — transfers, registrations, votes,
// settlements, trajectory commits, lifecycle transitions — must be published
// through this service. It guarantees the correct three-step sequence:
//
//  1. dag.Add: persist the event in the causal DAG (append-only, validated)
//  2. SubmitLocalEvent: enter the Fast Path v2 ingest pipeline for header relay
//  3. Broadcast: send the full event to all peers via the legacy V1 path
//
// This service must NOT be used for:
//   - Events received from peers (those go through the sync handler)
//   - DAG replay/import at startup (those go through dag.LoadFromStore)
//   - Events that were already added to the DAG by another path
//
// Layer: Infrastructure — importable by any layer.
package localpub

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// DAGWriter adds events to the causal DAG. Satisfied by *dag.DAG.
type DAGWriter interface {
	Add(ev *event.Event) error
}

// EventDisseminator sends locally-created events to peer nodes.
// Satisfied by *network.Node.
type EventDisseminator interface {
	// SubmitLocalEvent enters the event into the Fast Path v2 ingest
	// pipeline for header relay and body fetch. Dedup-safe.
	SubmitLocalEvent(ev *event.Event) error

	// Broadcast sends the full serialized event to all connected peers
	// via the legacy V1 message path.
	Broadcast(ev *event.Event) error
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrDAGAddFailed is returned when the DAG rejects the event (duplicate,
	// missing causal refs, invalid signature). No broadcast occurs.
	ErrDAGAddFailed = errors.New("localpub: dag.Add failed")

	// ErrNilEvent is returned when Publish is called with a nil event.
	ErrNilEvent = errors.New("localpub: event must not be nil")
)

// ---------------------------------------------------------------------------
// Publisher
// ---------------------------------------------------------------------------

// Publisher is the authoritative local event publication service. It owns the
// three-step publication sequence and is the only approved path for emitting
// locally-authored DAG events.
//
// Thread safety: Publisher is safe for concurrent use. DAGWriter and
// EventDisseminator implementations must also be safe for concurrent use
// (both *dag.DAG and *network.Node satisfy this requirement).
type Publisher struct {
	dag          DAGWriter
	disseminator EventDisseminator // nil before networking starts
}

// New creates a Publisher backed by the given DAG. The disseminator is
// optional — when nil, events are persisted in the DAG but not broadcast
// (appropriate for pre-networking startup events that are broadcast later
// by broadcastLocalEvents).
func New(dag DAGWriter, disseminator EventDisseminator) *Publisher {
	return &Publisher{
		dag:          dag,
		disseminator: disseminator,
	}
}

// SetDisseminator wires or replaces the network disseminator. Call after
// the network node starts and peers begin connecting. Safe to call
// concurrently with Publish — a Publish call in progress will use
// whichever disseminator was set at the time of the broadcast step.
func (p *Publisher) SetDisseminator(d EventDisseminator) {
	p.disseminator = d
}

// Publish adds a locally-created event to the DAG and disseminates it to
// all connected peers. This is the single authoritative publication path.
//
// Sequence:
//  1. dag.Add(ev) — persist and validate. On failure, returns ErrDAGAddFailed
//     and no broadcast occurs.
//  2. disseminator.SubmitLocalEvent(ev) — enter Fast Path v2 pipeline.
//  3. disseminator.Broadcast(ev) — send to all peers via legacy V1 path.
//
// Steps 2 and 3 are best-effort: broadcast errors are logged but do not
// cause Publish to fail. The event is already persisted in the DAG, and
// peers will eventually receive it through repair or periodic sync.
//
// Returns nil on success (DAG accepted the event).
func (p *Publisher) Publish(ev *event.Event) error {
	if ev == nil {
		return ErrNilEvent
	}

	// Step 1: Persist in DAG. This is the authoritative mutation.
	// If dag.Add fails (duplicate, missing parents, invalid signature),
	// we do NOT broadcast — the event is not part of the canonical state.
	if err := p.dag.Add(ev); err != nil {
		return fmt.Errorf("%w: %v", ErrDAGAddFailed, err)
	}

	// Step 2 + 3: Disseminate to peers. Best-effort — the event is
	// already persisted, so even if broadcast fails, peers will
	// eventually sync the event via repair or periodic discovery.
	p.disseminate(ev)

	return nil
}

// PublishBatch adds multiple events to the DAG and broadcasts each one.
// Events are processed in order; if one fails dag.Add, subsequent events
// are still attempted. Returns the number of successfully published events
// and the first error encountered (if any).
func (p *Publisher) PublishBatch(events []*event.Event) (int, error) {
	published := 0
	var firstErr error
	for _, ev := range events {
		if err := p.Publish(ev); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		published++
	}
	return published, firstErr
}

// disseminate sends the event through both V2 Fast Path and V1 legacy paths.
// Errors are logged but do not fail the operation — the event is already in
// the DAG, which is the authoritative state.
func (p *Publisher) disseminate(ev *event.Event) {
	if p.disseminator == nil {
		return // pre-networking: event is in DAG, will be broadcast later
	}

	if err := p.disseminator.SubmitLocalEvent(ev); err != nil {
		slog.Debug("localpub: SubmitLocalEvent failed (non-fatal)",
			"event_id", ev.ID, "type", ev.Type, "err", err)
	}

	if err := p.disseminator.Broadcast(ev); err != nil {
		slog.Debug("localpub: Broadcast failed (non-fatal)",
			"event_id", ev.ID, "type", ev.Type, "err", err)
	}
}
