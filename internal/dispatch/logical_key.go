package dispatch

import (
	"context"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// LogicalKey is the consumer-declared projection of an event payload.
// For TVConsensus: RoundID. For Settlement: TargetEventID. For
// TaskSettlement: TaskID. The dispatcher treats it as opaque — the
// consumer is the only party that knows its semantics.
//
// Per F4 plan v2 §4.4 and locked-invariant review §3.4. Type E in the
// consumer taxonomy.
type LogicalKey string

// Verdict is the canonical outcome verdict for verdict-bearing
// outcomes (verification consensus, settlement). Logical-key consumers
// whose outcome is not a verdict (e.g., distribution-based) leave it
// empty.
type Verdict string

const (
	// VerdictAccept indicates the round / target was accepted by
	// supermajority of canonical underlying state.
	VerdictAccept Verdict = "accept"
	// VerdictReject indicates the round / target was rejected by
	// supermajority of canonical underlying state.
	VerdictReject Verdict = "reject"
)

// RoundState holds the canonical state a logical-key consumer needs to
// compute completeness and outcome. Per F4 plan v2 §4.4: "Additional
// fields added as consumer types require, with explicit typed shape —
// never interface{}." Each consumer reads only the fields relevant to it.
//
// The struct is the UNION of all logical-key consumer needs. New fields
// are added in the same commit as the consumer that requires them.
type RoundState struct {
	// LogicalKey is the consumer-declared projection (e.g., RoundID).
	LogicalKey LogicalKey

	// Votes holds canonical Vote events for this round (TVConsensus).
	Votes []*event.Event

	// Attestations holds canonical Attestations (Settlement).
	Attestations []*event.Event

	// ObservedEvents is every logical-key-matched event observed by
	// the dispatcher so far. Diagnostic; not used to derive canonical
	// state per Serialization-2 / C-17.
	ObservedEvents []*event.Event

	// Epoch is the current dispatcher epoch at which RoundState was
	// queried. Used by IsComplete rules that consider time-bounded
	// completeness (e.g., deadline-based).
	Epoch uint64
}

// Outcome is the derived canonical result. Per F4 plan v2 §4.4:
// "Concrete type per consumer; shared type for consumers with similar
// outcome shapes." Verdict-bearing consumers populate Verdict;
// distribution-bearing consumers populate ParticipatingIDs. Consumer-
// specific fields added with explicit shape (no interface{}).
type Outcome struct {
	// Verdict is the canonical accept/reject decision for verdict-
	// bearing logical-key consumers (TVConsensus, Settlement).
	Verdict Verdict

	// ScoreBP is an optional basis-point score derived from canonical
	// underlying state (e.g., vote-weighted average for TVConsensus).
	ScoreBP uint32

	// ParticipatingIDs lists the validators / attesters whose votes
	// or attestations counted toward Verdict.
	ParticipatingIDs []crypto.AgentID
}

// LogicalKeyConsumer is a dispatcher consumer that handles events
// admitted by logical-key (e.g., RoundID, TargetEventID) rather than
// content-hash. Multiple byte-distinct canonical events can exist in
// the DAG for the same logical key; this consumer treats them as
// readiness signals and derives canonical outcome from underlying
// state that is cluster-uniform.
//
// Type E in the consumer taxonomy (F4 plan v2 §13). Distinguished from
// Type A/B/C/D by admission strategy: content-hash for A-D, logical-
// key for E.
//
// Critical properties (locked-invariant review §3.4):
//   - Apply is per-key, not per-event. Fires exactly once per logical-
//     key value, regardless of how many events for that key exist.
//   - IsComplete is deterministic on canonical state. Same votes on
//     all nodes → same completeness determination.
//   - No "superseded" transitions. IsComplete is defined so further
//     events cannot change Outcome; there is no case where a post-
//     Apply event would change the outcome.
//   - Forward-only settlement preserved. Atomic-batch per C-11
//     unchanged.
type LogicalKeyConsumer interface {
	// Name returns the unique consumer identifier. Same shape as
	// Consumer.Name. Used as the per-(consumer, key) namespace in the
	// admission store.
	Name() string

	// Interested returns true iff this consumer cares about the event
	// type. Same shape as Consumer.Interested. Logical-key admission
	// only fires for events the consumer is interested in.
	Interested(ev *event.Event) bool

	// Key extracts the logical-key value from an event. Pure
	// deterministic function of the event payload. Returns an error
	// iff the payload cannot be parsed (which would indicate a
	// misrouted event — caller treats as a bug, not a runtime
	// condition).
	Key(ev *event.Event) (LogicalKey, error)

	// RoundState queries canonical state needed for IsComplete and
	// DeriveOutcome. The consumer is responsible for fetching from
	// its own data sources (round store, attestation store, DAG
	// queries via injected accessors). Pure read; no side effects.
	//
	// Rationale for putting this on the consumer rather than the
	// dispatcher: the dispatcher does not have universal DAG-query
	// knowledge of every event-type-to-state mapping, and the plan's
	// §4.4 RoundState struct is a UNION of all consumer-needed
	// fields. Centralizing the query in the consumer keeps the
	// dispatcher generic and the consumer self-contained. Documented
	// deviation from F4 plan v2 §4.5 step (c) which speaks of the
	// dispatcher "querying canonical round state" — mechanism is the
	// consumer's method, orchestration is still the dispatcher's.
	RoundState(ctx context.Context, key LogicalKey) (RoundState, error)

	// IsComplete returns true when enough evidence exists in the
	// round state to derive a canonical outcome that no future event
	// arrival can change. Must be a pure deterministic function of
	// canonical state. Must not consult wall-clock, ephemeral memory,
	// or external systems.
	//
	// Cluster-uniformity: same RoundState on all nodes → same
	// IsComplete result. This is the load-bearing property for F4B's
	// correctness.
	IsComplete(rs RoundState) (bool, error)

	// DeriveOutcome computes the canonical Outcome from RoundState.
	// Pure deterministic. Same RoundState on all nodes → same
	// Outcome.
	DeriveOutcome(rs RoundState) (Outcome, error)

	// Apply invokes the consumer's authoritative local state
	// transition. Called once per logical key, after IsComplete
	// returns true. Receives the derived Outcome, NOT the triggering
	// event's payload.
	//
	// This is the C-17 enforcement point: by passing Outcome (derived
	// from canonical state) rather than ev.Payload (potentially
	// advisory), the dispatcher prevents the consumer from
	// accidentally deriving canonical state from advisory fields.
	//
	// triggerEventID is the canonical EventID of the event that drove
	// this Apply invocation (the event whose admission satisfied the
	// IsComplete gate). The consumer MAY use it for:
	//   - canonical seal context where downstream code requires the
	//     specific event identity (e.g., F5 5B Shape 3: TVConsensus
	//     event ID as DeriveSettlement's CanonicalSealContext source,
	//     replacing recognition-fabric-projected round.CanonicalSealContext)
	//   - audit / observability (which canonical event was the trigger)
	// Consumers that don't need it ignore the parameter.
	Apply(ctx context.Context, triggerEventID event.EventID, key LogicalKey, outcome Outcome) error

	// RecoveryProbe checks whether Apply completed for the
	// (consumer, key) pair during a prior invocation interrupted by a
	// crash. Per C-14: evidence-based, monotonic, replay-safe. Must
	// derive its answer from canonical durable state (not ephemeral
	// memory, wall-clock, or external systems).
	//
	// Returns RecoveryCompleted iff positive evidence of Apply
	// completion exists in canonical state; RecoveryNotStarted
	// otherwise. Absence of evidence is not evidence of absence —
	// return RecoveryNotStarted when uncertain. Errors are fatal —
	// Dispatcher.Recover aborts startup.
	//
	// Dispatcher.Recover invokes this for every non-terminal logical-
	// key admission record at startup. If a record was left in
	// StateProcessing or StateFailedRetryable because the node
	// crashed mid-Apply, RecoveryCompleted promotes it to
	// StateApplied (Apply ran; no retry needed); RecoveryNotStarted
	// promotes it to StateFailedRetryable (the next Admit for any
	// event projecting to this key will drive a retry).
	RecoveryProbe(ctx context.Context, key LogicalKey) (RecoveryStatus, error)
}

// validateLogicalKeyConsumer performs structural validation at
// registration time, mirroring validateConsumer for Type A/B/C/D
// consumers. Behavioral conformance is enforced in CI via the
// conformance test suite; startup checks structure only.
func validateLogicalKeyConsumer(c LogicalKeyConsumer) error {
	if c == nil {
		return errLogicalKeyConsumerNil
	}
	if c.Name() == "" {
		return errLogicalKeyConsumerEmptyName
	}
	return nil
}
