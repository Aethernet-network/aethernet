package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/settlement"
)

// TaskSettlementLogicalKeyConsumer is the F4B-era Type E consumer
// for TaskSettlement events (F4 plan v2 §5.2.3). It provides
// per-TaskID admission dedup so the dispatcher's admission-state
// machine sees a stable canonical record per task, regardless of
// how many autovalidators independently emit TaskSettlement events
// for that task.
//
// Important architectural note: TaskSettlement events are the
// TARGET of Settlement, not the settlement itself. The Settlement
// logical-key consumer (§5.2.2) handles the canonical verdict
// derivation and ledger mutation via settlementApp.Apply. This
// consumer's Apply is a **no-op**; its sole purpose is to
// establish per-TaskID admission dedup in the dispatcher so
// downstream consumers (including the recognition bus's
// ocsSubmitConsumer, which inserts TaskSettlement events into OCS
// pending) observe a canonical admission boundary.
//
// Content-determinism caveat: autovalidators on different nodes can
// in principle produce byte-distinct TaskSettlement events for the
// same TaskID, because the ScoreBP field is computed per-node from
// evidence-score computation. In the current codebase the score is
// deterministic on evidence bytes (the verification service uses a
// deterministic pass-through SubjectiveRater), so distinct TaskIDs
// produce distinct content and identical TaskIDs produce identical
// content. If the scoring pipeline is ever extended to use
// non-deterministic components (LLM sampling, ML inference without
// temperature=0), the admission-dedup shape below would need
// extending to resolve content divergence (e.g., "lexicographically
// smallest content-hash wins"). See the F4B completion report for
// the full caveat analysis.
//
// Key properties:
//
//   - Apply fires exactly once per TaskID regardless of how many
//     byte-distinct TaskSettlement events the cluster emits.
//   - IsComplete is trivially true: a TaskSettlement event's
//     arrival is itself the readiness signal. There is no per-task
//     underlying state to consult.
//   - DeriveOutcome returns an empty Outcome. No verdict derivation
//     happens here; the Settlement LK consumer (§5.2.2) owns that
//     responsibility keyed by TargetEventID.
//   - RecoveryProbe returns RecoveryCompleted always. No side
//     effects means nothing to recover — the admission record is
//     the only state, and it's recovered by the dispatcher's
//     normal Recover path.
type TaskSettlementLogicalKeyConsumer struct{}

// NewTaskSettlementLogicalKeyConsumer constructs the Type E
// TaskSettlement consumer. No dependencies: Apply is a no-op, so
// there is no applicator / store / settler to inject.
func NewTaskSettlementLogicalKeyConsumer() *TaskSettlementLogicalKeyConsumer {
	return &TaskSettlementLogicalKeyConsumer{}
}

// Name is the unique consumer identifier. Distinct from the
// Settlement LK consumer's name so admission records for the two
// consumer kinds never collide.
func (c *TaskSettlementLogicalKeyConsumer) Name() string {
	return "task_settlement_lk"
}

// Interested reports whether the event is a TaskSettlement event.
func (c *TaskSettlementLogicalKeyConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskSettlement
}

// Key projects the event payload's TaskID as the logical admission
// key. An unparsable payload or empty TaskID is a programming /
// routing bug — return an error so the dispatcher's
// admitOneLogicalKey surfaces it loudly.
func (c *TaskSettlementLogicalKeyConsumer) Key(ev *event.Event) (LogicalKey, error) {
	payload, err := event.GetPayload[settlement.TaskSettlementPayload](ev)
	if err != nil {
		return "", fmt.Errorf("task_settlement_lk: unmarshal payload: %w", err)
	}
	if payload.TaskID == "" {
		return "", errors.New("task_settlement_lk: empty TaskID")
	}
	return LogicalKey(payload.TaskID), nil
}

// RoundState populates only LogicalKey. No per-task canonical state
// to fetch at this stage — TaskSettlement events are readiness
// signals for their own admission record; verdict derivation
// happens in the Settlement LK consumer.
func (c *TaskSettlementLogicalKeyConsumer) RoundState(ctx context.Context, key LogicalKey) (RoundState, error) {
	_ = ctx
	return RoundState{LogicalKey: key}, nil
}

// IsComplete returns true unconditionally. A TaskSettlement event's
// arrival is itself the completion signal for its per-TaskID
// admission record. No vote set to wait on, no supermajority to
// compute — the event's existence is the readiness.
func (c *TaskSettlementLogicalKeyConsumer) IsComplete(rs RoundState) (bool, error) {
	_ = rs
	return true, nil
}

// DeriveOutcome returns an empty Outcome. This consumer does not
// produce a canonical verdict; verdict derivation is the
// Settlement LK consumer's responsibility (§5.2.2) keyed by
// TargetEventID.
func (c *TaskSettlementLogicalKeyConsumer) DeriveOutcome(rs RoundState) (Outcome, error) {
	_ = rs
	return Outcome{}, nil
}

// Apply is a no-op. The admission record itself is the durable
// effect of this consumer; there are no ledger mutations or
// side effects to invoke at this point. Downstream consumers
// (OCS pending insertion via recognition's ocsSubmitConsumer, and
// eventually the Settlement LK consumer when the Settlement event
// targeting this TaskSettlement arrives) proceed independently.
//
// The dispatcher's per-(consumer, key) state machine guarantees
// Apply is invoked at most once per TaskID; recording that fact in
// the admission record (StateApplied) is the only output this
// consumer needs.
func (c *TaskSettlementLogicalKeyConsumer) Apply(ctx context.Context, _ event.EventID, key LogicalKey, outcome Outcome) error {
	_ = ctx
	_ = key
	_ = outcome
	return nil
}

// RecoveryProbe returns RecoveryCompleted unconditionally. Apply
// is a no-op, so there is no durable-side-effect state to inspect
// for evidence of prior completion. The admission record itself
// suffices as the "was this key admitted?" answer, and the
// dispatcher's Recover already handles that layer; a positive
// RecoveryCompleted here signals "nothing more to do — retaining
// StateApplied is the correct resolution."
//
// This is semantically distinct from RecoveryNotStarted (which
// asks the dispatcher to re-drive Apply on next Admit). For a
// no-op Apply, either resolution produces the same observable
// behavior, but RecoveryCompleted is the honest signal: the
// effect (nothing) has already happened.
func (c *TaskSettlementLogicalKeyConsumer) RecoveryProbe(ctx context.Context, key LogicalKey) (RecoveryStatus, error) {
	_ = ctx
	_ = key
	return RecoveryCompleted, nil
}
