package dispatch

import (
	"context"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/settlement"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// TVConsensusConsumer is the Type A dispatcher consumer for
// TaskVerificationConsensus events. It mediates the v4.1 economic
// settlement through the dispatcher's exactly-once admission
// guarantee (C-1), replacing the prior direct-invocation path from
// the recognition fabric to the settler.
//
// Only the settlement invocation goes through the dispatcher. Round
// state finalization, calibration counters, and slashing evaluation
// remain in the recognition consumer because they do not require
// cross-node ledger convergence — they are local-node replay-safe
// state or best-effort side effects.
type TVConsensusConsumer struct {
	settler *settlement.VerificationConsensusSettler
	rounds  taskverification.Store
	taskMgr *tasks.TaskManager
}

// NewTVConsensusConsumer constructs a consumer. All parameters required.
func NewTVConsensusConsumer(
	settler *settlement.VerificationConsensusSettler,
	rounds taskverification.Store,
	taskMgr *tasks.TaskManager,
) *TVConsensusConsumer {
	return &TVConsensusConsumer{
		settler: settler,
		rounds:  rounds,
		taskMgr: taskMgr,
	}
}

func (c *TVConsensusConsumer) Name() string { return "tv_consensus_settlement" }

func (c *TVConsensusConsumer) Type() ConsumerType { return TypeA }

func (c *TVConsensusConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskVerificationConsensus
}

// Prerequisites returns nil (no explicit prerequisites declared).
//
// The correctness of this choice depends on DAG strict enforcement: dag.Add
// requires all CausalRefs to be present before an event can be added. Because
// TaskVerificationConsensus events reference the full round chain through
// votes → TaskSubmitted → TaskClaimed → TaskPosted, the arrival of a
// TaskVerificationConsensus event in the local DAG guarantees TaskPosted is
// also present. The narrow race between DAG commit and task-lifecycle-consumer
// processing of TaskPosted (which populates taskMgr) is handled by the
// dispatcher's failed-retryable retry mechanism.
//
// If a future DAG-layer change weakens strict CausalRefs enforcement, this
// consumer's Prerequisites must be revisited — a consumer with empty
// prerequisites would silently break on out-of-order arrivals.
func (c *TVConsensusConsumer) Prerequisites(_ *event.Event) []event.EventID {
	return nil
}

func (c *TVConsensusConsumer) PrerequisiteSchemaVersion() uint32 { return 1 }

// Apply executes the v4.1 economic settlement for a TaskVerificationConsensus
// event. Delegates to the existing settler logic, which now routes all payouts
// through escrow.ReleaseSettlement with per-recipient paid-flag idempotency.
func (c *TVConsensusConsumer) Apply(ctx context.Context, ev *event.Event) error {
	payload, err := event.GetPayload[event.TaskVerificationConsensusPayload](ev)
	if err != nil {
		return fmt.Errorf("tv_consensus_settlement: unmarshal: %w", err)
	}

	roundID := taskverification.RoundID(payload.RoundID)
	round, err := c.rounds.LoadRound(ctx, roundID)
	if err != nil {
		return fmt.Errorf("tv_consensus_settlement: load round %s: %w", payload.RoundID, err)
	}

	result, err := c.settler.Settle(ctx, &payload, round)
	if err != nil {
		return fmt.Errorf("tv_consensus_settlement: settle: %w", err)
	}
	if result.AlreadyApplied {
		return nil
	}
	return nil
}

// RecoveryProbe checks whether settlement completed for a
// TaskVerificationConsensus event during a prior invocation interrupted
// by a crash. Per C-14: evidence-based, monotonic, replay-safe.
//
// The probe checks the task's terminal status (Completed, Rejected,
// DisputedResolved) as positive evidence of settlement completion.
// Terminal status is set by ApplyVerificationConsensusResolution as the
// last step of settlement (verification_consensus_settler.go:203/256/314).
// If the task is terminal, all payouts completed because terminal-status
// transition happens after all ReleaseSettlement transfers.
//
// Load-bearing assumption: only ApplyVerificationConsensusResolution sets
// terminal status on multi-validator-pipeline tasks. Six terminal-status
// assignment sites exist in tasks.go; the other three are legacy paths
// requiring different event types or explicit HTTP API calls. See
// docs/lessons.md entry for this assumption.
func (c *TVConsensusConsumer) RecoveryProbe(ctx context.Context, ev *event.Event) (RecoveryStatus, error) {
	payload, err := event.GetPayload[event.TaskVerificationConsensusPayload](ev)
	if err != nil {
		return RecoveryNotStarted, nil
	}
	task, taskErr := c.taskMgr.Get(payload.TaskID)
	if taskErr != nil {
		return RecoveryNotStarted, nil
	}
	switch task.Status {
	case tasks.TaskStatusCompleted, tasks.TaskStatusRejected,
		tasks.TaskStatusDisputedResolved:
		return RecoveryCompleted, nil
	}
	return RecoveryNotStarted, nil
}
