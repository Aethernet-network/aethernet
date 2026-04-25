package epoch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/recognition"
)

// BoundaryEmitterDAGReader is the narrow read surface BoundaryEmitter
// requires from the DAG. Satisfies the §2.1 canonical-trigger-condition
// contract: every read returns a deterministic function of canonical
// DAG state.
//
// *dag.DAG satisfies this interface structurally.
//
// The emitter MUST NOT receive a wrapper or alternative reader that
// returns local-counter or non-canonical-projection values — sub-spec
// v2.2 §12.1 primary hidden-error pattern. The grep-level test
// (boundary_emitter_purity_test.go) verifies the source contains no
// reads of RoundCounter, local counters, or non-canonical projections.
type BoundaryEmitterDAGReader interface {
	CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error)
	Get(id event.EventID) (*event.Event, error)
}

// BoundaryEmitterPublisher is the publication surface BoundaryEmitter
// uses. *localpub.Publisher satisfies this interface; the emitter does
// NOT call dag.Add directly per CLAUDE.md "localpub.Publisher.Publish
// is the only sanctioned local event creation path."
type BoundaryEmitterPublisher interface {
	Publish(ev *event.Event) error
}

// BoundaryEmitter is the F5 5B canonical-epoch sub-spec §2.2 Candidate A
// recognition consumer. On every committed TaskVerificationConsensus
// event, it computes canonical_tvc_rank via CountAncestorsByType and —
// if rank crosses an epoch threshold — emits an EpochBoundary.
//
// Symmetric across all nodes: every node running this consumer detects
// the canonical trigger condition and emits its own EpochBoundary(N).
// Cross-node convergence is achieved via the EpochBoundary
// LogicalKeyConsumer (keyed on Epoch per sub-spec §12.6(i)) which
// deduplicates multi-emit to one canonical EpochBoundary per epoch.
//
// V-1 / canonicality discipline: the trigger condition reads ONLY
// canonical DAG state via dagReader.CountAncestorsByType. NO reads of
// RoundCounter, local counters, or non-canonical projections — sub-spec
// §12.1 primary hidden-error pattern. The grep-level test enforces
// this at CI.
//
// Idempotency: emission of EpochBoundary(N) is logical-key-deduped by
// the LogicalKeyConsumer; the emitter is free to emit on every
// observation of a threshold-crossing TVConsensus event without
// concern for duplication. Replay-safe: re-observing the same canonical
// trigger condition produces the same emission, dedup'd at admission.
type BoundaryEmitter struct {
	dagReader BoundaryEmitterDAGReader
	publisher BoundaryEmitterPublisher
	signer    *crypto.KeyPair
}

// NewBoundaryEmitter constructs the emitter. All parameters required.
// signer is the local validator's signing key — every node signs its
// own EpochBoundary emission with its own key; per sub-spec §1.5,
// distinct signers produce distinct content-hashes; per §2.2 Candidate
// A, logical-key dedup on Epoch converges the cluster.
func NewBoundaryEmitter(
	dagReader BoundaryEmitterDAGReader,
	publisher BoundaryEmitterPublisher,
	signer *crypto.KeyPair,
) *BoundaryEmitter {
	if dagReader == nil {
		panic("epoch: NewBoundaryEmitter requires non-nil dagReader")
	}
	if publisher == nil {
		panic("epoch: NewBoundaryEmitter requires non-nil publisher")
	}
	if signer == nil {
		panic("epoch: NewBoundaryEmitter requires non-nil signer")
	}
	return &BoundaryEmitter{
		dagReader: dagReader,
		publisher: publisher,
		signer:    signer,
	}
}

// Name implements recognition.CommitConsumer. Distinct from
// "round_counter" (the existing EpochLength-tracking consumer) — the
// two consumers operate on the same event type but compute different
// canonical artifacts.
func (e *BoundaryEmitter) Name() string { return "epoch_boundary_emitter" }

// Interested implements recognition.CommitConsumer. Subscribes to
// TaskVerificationConsensus events — the canonical source of
// epoch-advancing cadence per sub-spec §2.1.
func (e *BoundaryEmitter) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskVerificationConsensus
}

// Ready implements recognition.CommitConsumer. Always true: the
// canonical trigger condition is evaluable as soon as the TVConsensus
// event is committed (its ancestors are already materialized per
// dag.Add's strict CausalRefs check).
func (e *BoundaryEmitter) Ready(_ context.Context, _ *event.Event, _ recognition.ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume implements recognition.CommitConsumer. Computes canonical_tvc_rank
// for the just-committed TVConsensus event; if rank == N * EpochLength for
// some N >= 1, constructs and publishes EpochBoundary(N).
//
// Errors from the publish path are logged but not returned — the
// recognition fabric's idempotency gate handles retry. Error-return
// would mark the (consumer, event) pair as failed and skip retry,
// which we don't want for transient publish failures.
//
// The emission is signed by the local validator's key. Per sub-spec
// §1.5, multiple validators emitting EpochBoundary(N) for the same
// trigger produce distinct content-hashes (AgentID differs in preimage);
// the LogicalKeyConsumer keyed on Epoch converges to one canonical
// boundary per epoch.
func (e *BoundaryEmitter) Consume(_ context.Context, ev *event.Event) error {
	// canonical_tvc_rank(ev) = CountAncestorsByType(ev, TVConsensus) + 1.
	// Pure canonical-DAG-state read.
	tvcAncestors, err := e.dagReader.CountAncestorsByType(ev.ID, event.EventTypeTaskVerificationConsensus)
	if err != nil {
		// ErrEventNotFound here means the event itself or one of its
		// ancestors isn't materialized — defensive: log and skip. The
		// recognition fabric will re-deliver the event later.
		slog.Debug("epoch_boundary_emitter: count TVConsensus ancestors failed",
			"event_id", ev.ID, "err", err)
		return nil
	}
	canonicalRank := tvcAncestors + 1

	// Threshold check: emit only when rank is exactly at an epoch
	// boundary multiple. Modulo zero AND rank > 0 (avoid edge case if
	// EpochLength is somehow 0 — defensive only).
	if EpochLength == 0 || canonicalRank == 0 || canonicalRank%EpochLength != 0 {
		return nil
	}
	epochN := canonicalRank / EpochLength

	if err := e.publishBoundary(epochN, ev); err != nil {
		// Log; do not propagate. Multi-emit is the design (Candidate A);
		// other nodes' emissions cover for any transient publish failure.
		slog.Warn("epoch_boundary_emitter: publish failed (other nodes' emissions will cover)",
			"epoch", epochN,
			"trigger_event_id", ev.ID,
			"err", err,
		)
	}
	return nil
}

// publishBoundary constructs, signs, and publishes EpochBoundary(epochN).
// CausalRefs = [trigger.ID] per sub-spec §1.3.
func (e *BoundaryEmitter) publishBoundary(epochN uint64, trigger *event.Event) error {
	payload := event.EpochBoundaryPayload{
		Version:        1,
		Epoch:          epochN,
		TriggerEventID: trigger.ID,
	}

	// Marshal payload explicitly so event.New uses the canonical bytes.
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	priorTimestamps := map[event.EventID]uint64{trigger.ID: trigger.CausalTimestamp}
	boundary, err := event.New(
		event.EventTypeEpochBoundary,
		[]event.EventID{trigger.ID},
		json.RawMessage(payloadBytes),
		string(e.signer.AgentID()),
		priorTimestamps,
		0,
	)
	if err != nil {
		return fmt.Errorf("event.New: %w", err)
	}

	if err := crypto.SignEvent(boundary, e.signer); err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	if err := e.publisher.Publish(boundary); err != nil {
		// Two expected error classes:
		// (a) duplicate — this node already admitted EpochBoundary(N) via
		//     a peer's earlier emission; logical-key dedup at the
		//     dispatcher layer collapsed it. Benign no-op.
		// (b) cross-check rejection — admission validator rejected this
		//     emission. Honest emitter math should never trigger this; if
		//     it does, log loudly because it indicates an emitter bug or
		//     racing canonical-state change.
		// Rather than parse error chains across package boundaries,
		// callers rely on the warn log to distinguish operational vs
		// design failures. Multi-emit (Candidate A) means transient
		// failures are absorbed by other validators' emissions; the
		// emitter does not retry locally.
		return fmt.Errorf("publish: %w", err)
	}

	slog.Info("epoch_boundary_emitter: emitted",
		"epoch", epochN,
		"trigger_event_id", trigger.ID,
		"boundary_event_id", boundary.ID,
	)
	return nil
}

// Compile-time assertion that BoundaryEmitter satisfies the recognition
// CommitConsumer contract.
var _ recognition.CommitConsumer = (*BoundaryEmitter)(nil)
