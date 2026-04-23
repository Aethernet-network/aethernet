package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/settlement"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// TVConsensusLogicalKeyConsumer is the F4B-era Type E consumer for
// TaskVerificationConsensus events. It replaces the F3-B content-hash
// TVConsensusConsumer for canonical-effect routing.
//
// Admission is keyed by the round's RoundID rather than the event's
// content hash (AdmissionStrategyLogicalKey, locked-invariant review
// §3.5). Multiple validators may emit TVConsensus events independently
// for the same round with potentially conflicting FinalVerdict values;
// per C-17 (Serialization-2), this consumer ignores the triggering
// event's FinalVerdict and derives the canonical verdict from the
// round's vote set, which is cluster-uniform.
//
// Key properties (F4 plan v2 §4.5, §4.7):
//
//   - Apply fires exactly once per RoundID regardless of how many
//     byte-distinct TVConsensus events the cluster emits for that round.
//   - IsComplete is deterministic on canonical state: pass_weight or
//     fail_weight has crossed supermajority AND the gap cannot be
//     closed by any possible remaining vote (mathematically sealed).
//   - DeriveOutcome computes Verdict + ScoreBP + ParticipatingIDs
//     from the canonical vote set; no wall-clock, no ephemeral state.
//   - RecoveryProbe consults the round's RoundState (finalized_accept
//     or finalized_reject) as positive evidence that Apply ran to
//     completion on a prior binary instance (round-state transition is
//     the last step inside ApplyVerificationConsensusResolution).
//
// Per-task serialization: the F3-B content-hash consumer used a
// sync.Map-of-mutexes keyed by TaskID to serialize concurrent Apply
// calls for the same task. Under logical-key admission, admission
// records serialize per (consumer, RoundID); because RoundID → TaskID
// is 1:1 and the dispatcher invokes Apply at most once per admission
// record, no per-task mutex is needed. The admission-record state
// machine IS the serialization primitive.
//
// Settlement invocation shape: Apply builds a synthetic
// TaskVerificationConsensusPayload from the derived Outcome + round
// metadata, then invokes settler.Settle. The synthetic payload keeps
// the existing settler contract unchanged; the settler continues to
// route all payouts through escrow.ReleaseSettlement with per-
// recipient paid-flag idempotency (C-11). The only difference from
// the content-hash path is the payload's FinalVerdict now reflects
// the canonical vote-derived Outcome, not the triggering event's
// advisory field.
type TVConsensusLogicalKeyConsumer struct {
	settler        *settlement.VerificationConsensusSettler
	rounds         taskverification.Store
	taskMgr        *tasks.TaskManager
	escrowMgr      *escrow.Escrow
	activeWeightFn func() uint64

	// nowFn returns the current Unix timestamp. Injected for test
	// determinism. Per C-17 the wall-clock is NOT consulted for
	// canonical state derivation (IsComplete / DeriveOutcome), only
	// for the synthetic payload's FinalizationTimeUnix field, which
	// is observability metadata and NOT consulted by the settler for
	// any canonical effect. Safe to vary per node.
	nowFn func() int64
}

// NewTVConsensusLogicalKeyConsumer constructs the Type E consumer.
// All parameters required. activeWeightFn must return the total
// active validator stake (supermajority denominator) derived from
// canonical DAG state — cluster-uniform at the moment IsComplete
// fires, per the registration audit §6 check.
func NewTVConsensusLogicalKeyConsumer(
	settler *settlement.VerificationConsensusSettler,
	rounds taskverification.Store,
	taskMgr *tasks.TaskManager,
	escrowMgr *escrow.Escrow,
	activeWeightFn func() uint64,
) *TVConsensusLogicalKeyConsumer {
	return &TVConsensusLogicalKeyConsumer{
		settler:        settler,
		rounds:         rounds,
		taskMgr:        taskMgr,
		escrowMgr:      escrowMgr,
		activeWeightFn: activeWeightFn,
		nowFn:          func() int64 { return time.Now().Unix() },
	}
}

// Name is the unique consumer identifier. Distinct from the old
// content-hash consumer's "tv_consensus_settlement" so admission-
// store records for the two strategies never collide on name.
func (c *TVConsensusLogicalKeyConsumer) Name() string {
	return "tv_consensus_settlement_lk"
}

// Interested reports whether the event is a TaskVerificationConsensus.
func (c *TVConsensusLogicalKeyConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskVerificationConsensus
}

// Key projects the event payload's RoundID as the logical admission
// key. An unparsable payload is a programming / routing bug — return
// an error so the dispatcher's admitOneLogicalKey surfaces it loudly.
func (c *TVConsensusLogicalKeyConsumer) Key(ev *event.Event) (LogicalKey, error) {
	payload, err := event.GetPayload[event.TaskVerificationConsensusPayload](ev)
	if err != nil {
		return "", fmt.Errorf("tv_consensus_lk: unmarshal payload: %w", err)
	}
	if payload.RoundID == "" {
		return "", errors.New("tv_consensus_lk: empty RoundID")
	}
	return LogicalKey(payload.RoundID), nil
}

// RoundState loads the canonical verification round for this logical
// key. The round's vote set is the cluster-uniform underlying state
// from which IsComplete and DeriveOutcome derive canonical Outcome.
//
// A round that does not exist on the local node yet returns an empty
// RoundState (no votes). IsComplete then returns false (no weight
// crosses threshold); the dispatcher persists StateProcessing and
// waits for either a re-Admit (after votes have been projected) or
// the next event arrival for this key.
func (c *TVConsensusLogicalKeyConsumer) RoundState(ctx context.Context, key LogicalKey) (RoundState, error) {
	roundID := taskverification.RoundID(key)
	_, err := c.rounds.LoadRound(ctx, roundID)
	if err != nil {
		// Not-found is not a fatal error — votes may not have been
		// projected yet on this node. Return an empty RoundState so
		// IsComplete returns false and the dispatcher defers.
		if errors.Is(err, taskverification.ErrRoundNotFound) {
			return RoundState{LogicalKey: key}, nil
		}
		return RoundState{}, fmt.Errorf("tv_consensus_lk: load round %s: %w", key, err)
	}
	return RoundState{
		LogicalKey: key,
		// Votes: synthesize event.Event stand-ins so the shared RoundState
		// struct contract is satisfied without leaking the
		// taskverification.TaskVerificationVoteRecord type into the
		// dispatch package. IsComplete and DeriveOutcome on this consumer
		// do NOT read rs.Votes — they load the round directly via a
		// per-method call path kept out of the RoundState plumbing.
		//
		// Rationale: RoundState is defined as the union of all logical-
		// key consumer needs (logical_key.go §3.4 design note). The
		// Votes slice's declared shape is []*event.Event (canonical Vote
		// events in the DAG); TaskVerificationVoteRecord is the in-
		// memory aggregated-vote form held by the round store. Rather
		// than couple RoundState's type to the in-memory form, this
		// consumer passes the round through a typed side-channel
		// (c.roundFor(key)) used by IsComplete and DeriveOutcome.
		Votes: nil,
	}, nil
}

// roundFor is the typed side-channel that IsComplete and
// DeriveOutcome use to reach the canonical round. Decoupled from
// RoundState so the dispatch package's cross-consumer RoundState
// struct does not need to import taskverification-specific vote
// types.
//
// Called lazily from IsComplete/DeriveOutcome via a context-less
// lookup because LogicalKeyConsumer.IsComplete and DeriveOutcome
// take RoundState, not ctx. The RoundState method above is the
// ONLY place that accesses the store with a caller-supplied ctx;
// the other two methods run on a background context for the store
// call. For Badger-backed store reads this is acceptable (no
// long-running I/O); if the store implementation changes to
// require cancellable reads, the LogicalKeyConsumer interface will
// need a ctx on IsComplete/DeriveOutcome.
func (c *TVConsensusLogicalKeyConsumer) roundFor(key LogicalKey) (*taskverification.TaskVerificationRound, error) {
	roundID := taskverification.RoundID(key)
	round, err := c.rounds.LoadRound(context.Background(), roundID)
	if err != nil {
		if errors.Is(err, taskverification.ErrRoundNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("tv_consensus_lk: load round %s: %w", key, err)
	}
	return round, nil
}

// IsComplete returns true when the round's outcome is mathematically
// sealed per F4 plan v2 §4.7:
//
//	passSealed = passWeight >= threshold && passWeight > failWeight + maxRemaining
//	failSealed = failWeight >= threshold && failWeight > passWeight + maxRemaining
//
// threshold is the supermajority floor (2/3 of total active weight,
// ceiling-divided). maxRemaining is the weight that has NOT yet
// voted and could still arrive. When either side is sealed, no
// possible future vote can change the winning verdict, so Apply is
// safe to fire.
//
// Cluster-uniformity: activeWeightFn + round.Votes are both derived
// from canonical DAG state; the computation is pure arithmetic.
// Given identical DAG-projected state on two nodes, IsComplete
// returns the same value — the load-bearing property F4B requires.
func (c *TVConsensusLogicalKeyConsumer) IsComplete(rs RoundState) (bool, error) {
	round, err := c.roundFor(rs.LogicalKey)
	if err != nil {
		return false, err
	}
	if round == nil {
		return false, nil
	}

	activeWeight := c.activeWeightFn()
	if activeWeight == 0 {
		// No validator set known → cannot determine completeness.
		// Defer rather than fail; a later Admit will re-check with a
		// populated validator snapshot.
		return false, nil
	}

	var passWeight, failWeight, castWeight uint64
	for _, v := range round.Votes {
		switch v.Verdict {
		case taskverification.VerdictPass:
			passWeight += v.Stake
		case taskverification.VerdictFail:
			failWeight += v.Stake
		}
		// Abstain weight contributes to cast weight but not to either
		// verdict's supermajority; it cannot change the winner so it
		// is lumped into the "already cast" bucket for maxRemaining.
		castWeight += v.Stake
	}

	// maxRemaining is the upper bound on weight that could still
	// arrive and swing either verdict. Clamp at zero in case the
	// round records more cast weight than the validator snapshot's
	// active weight (possible transiently during validator set
	// changes).
	var maxRemaining uint64
	if activeWeight > castWeight {
		maxRemaining = activeWeight - castWeight
	}

	// Supermajority threshold: ceil(2*active/3). Use integer math
	// without floating point to preserve cluster-uniform determinism.
	threshold := (2*activeWeight + 2) / 3

	passSealed := passWeight >= threshold && passWeight > failWeight+maxRemaining
	failSealed := failWeight >= threshold && failWeight > passWeight+maxRemaining

	return passSealed || failSealed, nil
}

// DeriveOutcome returns the canonical outcome for a completed round.
// Preconditions: IsComplete returned true for rs.LogicalKey.
//
// Verdict selection: whichever side has the larger weight wins. The
// F4 plan §4.7 seal rule guarantees one side is strictly greater
// when IsComplete=true; ties are impossible under the seal rule.
//
// ScoreBP on accept: weighted mean of agreeing (pass) voters'
// ScoreBP fields, rounded by integer division. On reject, ScoreBP
// is 0 (paper v4.1 does not attach a score to rejection).
//
// ParticipatingIDs: every validator whose vote agreed with the
// winning verdict, in lex order for determinism.
func (c *TVConsensusLogicalKeyConsumer) DeriveOutcome(rs RoundState) (Outcome, error) {
	round, err := c.roundFor(rs.LogicalKey)
	if err != nil {
		return Outcome{}, err
	}
	if round == nil {
		return Outcome{}, fmt.Errorf("tv_consensus_lk: DeriveOutcome called with no round for key %s", rs.LogicalKey)
	}

	var passWeight, failWeight uint64
	var passScoreWeighted uint64
	var passStakeTotal uint64
	passVoters := make(map[crypto.AgentID]struct{})
	failVoters := make(map[crypto.AgentID]struct{})
	for _, v := range round.Votes {
		switch v.Verdict {
		case taskverification.VerdictPass:
			passWeight += v.Stake
			passScoreWeighted += v.ScoreBP * v.Stake
			passStakeTotal += v.Stake
			passVoters[v.ValidatorID] = struct{}{}
		case taskverification.VerdictFail:
			failWeight += v.Stake
			failVoters[v.ValidatorID] = struct{}{}
		}
	}

	var verdict Verdict
	var scoreBP uint32
	var winnerSet map[crypto.AgentID]struct{}
	if passWeight > failWeight {
		verdict = VerdictAccept
		winnerSet = passVoters
		if passStakeTotal > 0 {
			avg := passScoreWeighted / passStakeTotal
			// ScoreBP is uint32 on Outcome; vote ScoreBP is uint64 but
			// canonically in [0, 10000].
			if avg > 0xFFFFFFFF {
				avg = 0xFFFFFFFF
			}
			scoreBP = uint32(avg)
		}
	} else if failWeight > passWeight {
		verdict = VerdictReject
		winnerSet = failVoters
		scoreBP = 0
	} else {
		// Defensive: under the seal rule IsComplete cannot have
		// returned true on a tie. Fail loudly if the caller reached
		// here anyway — a silent tiebreak would violate C-17.
		return Outcome{}, fmt.Errorf("tv_consensus_lk: DeriveOutcome on tied round %s (pass=%d fail=%d); IsComplete contract violated",
			rs.LogicalKey, passWeight, failWeight)
	}

	// Deterministic participant ordering — collect then sort the
	// deduped set of agreeing validator IDs.
	ids := make([]crypto.AgentID, 0, len(winnerSet))
	// safe: collected then sorted before return; no iteration-order leak
	for id := range winnerSet {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	return Outcome{
		Verdict:          verdict,
		ScoreBP:          scoreBP,
		ParticipatingIDs: ids,
	}, nil
}

// Apply invokes the v4.1 economic settlement for a verification
// round. Called exactly once per (consumer, RoundID) after
// IsComplete returned true.
//
// Pre-checks mirror the F3-B content-hash consumer's guards:
//
//  1. Task terminal status. If the task was already settled (e.g.,
//     the settler finished on a prior Apply that crashed after
//     escrow paid but before admission-record persist, and
//     RecoveryProbe has not yet run), short-circuit. This also
//     handles the defensive case where a same-node re-Admit fires
//     after a mid-Apply crash-then-restart.
//  2. escrowMgr.HasSettlementStarted. If any per-recipient paid
//     flag is set on the escrow entry, the settler is part-way
//     through an earlier Apply; short-circuit rather than start a
//     second interleaved drain with potentially different
//     recipients.
//
// Both pre-checks preserve the F3-B forward-only settlement
// invariant. Note: under logical-key admission, the per-key
// dispatcher guarantee already provides single-Apply semantics
// absent a crash; the pre-checks are defense-in-depth for the
// crash-recovery window.
//
// Synthetic payload construction: the settler's Settle method
// accepts a *TaskVerificationConsensusPayload. Under C-17 the
// triggering event's payload is advisory (may carry a stale or
// conflicting FinalVerdict); this consumer constructs a fresh
// payload from the Outcome + round state so the settler sees the
// canonical verdict. SubmissionEventID, PosterID, WorkerID come
// from the round (canonical). FinalVerdict reflects Outcome.
// FinalScoreBP reflects Outcome. FinalizationTimeUnix is the
// wall-clock moment Apply fires on this node — observability
// metadata, not canonical state (C-17 advisory).
func (c *TVConsensusLogicalKeyConsumer) Apply(ctx context.Context, key LogicalKey, outcome Outcome) error {
	round, err := c.roundFor(key)
	if err != nil {
		return fmt.Errorf("tv_consensus_lk: load round: %w", err)
	}
	if round == nil {
		return fmt.Errorf("tv_consensus_lk: round %s missing at Apply time", key)
	}

	taskID := round.TaskID

	// Pre-check 1: task already terminal. Covers the crash-restart
	// window before RecoveryProbe has reconciled the admission record.
	if task, taskErr := c.taskMgr.Get(taskID); taskErr == nil {
		switch task.Status {
		case tasks.TaskStatusCompleted, tasks.TaskStatusRejected,
			tasks.TaskStatusDisputedResolved, tasks.TaskStatusCancelled:
			return nil
		}
	}

	// Pre-check 2: mid-settlement partial paid flags.
	if c.escrowMgr.HasSettlementStarted(taskID) {
		return nil
	}

	// Build the synthetic payload from the canonical Outcome.
	verdictString := verdictToPayloadString(outcome.Verdict)
	if verdictString == "" {
		return fmt.Errorf("tv_consensus_lk: unknown Outcome.Verdict %q for round %s", outcome.Verdict, key)
	}

	payload := event.TaskVerificationConsensusPayload{
		Version:              1,
		RoundID:              string(key),
		TaskID:               taskID,
		SubmissionEventID:    string(round.SubmissionEventID),
		WorkerID:             string(round.WorkerID),
		PosterID:             string(round.PosterID),
		FinalVerdict:         verdictString,
		FinalScoreBP:         uint64(outcome.ScoreBP),
		FinalizationTimeUnix: c.nowFn(),
	}

	if _, err := c.settler.Settle(ctx, &payload, round); err != nil {
		return fmt.Errorf("tv_consensus_lk: settle round %s: %w", key, err)
	}
	return nil
}

// RecoveryProbe checks whether Apply completed for (consumer, key)
// during a prior binary instance. Per C-14: evidence-based,
// monotonic, replay-safe.
//
// The probe is: load the round; if the round is in a terminal
// state (finalized_accept / finalized_reject), Apply must have
// completed because round state transition is the last step inside
// ApplyVerificationConsensusResolution (which runs after every
// escrow payout). A terminal round is positive evidence that the
// full settlement ran.
//
// Disputed / expired rounds also count as positive evidence —
// those transitions also complete the settlement path (the
// deadline checker's dispute resolution is the terminal state for
// such rounds, and its applier is separate from this consumer).
// The probe returns RecoveryCompleted because the logical-key
// admission record should not continue to drive a TVConsensus
// Apply for a round that resolved some other way.
//
// Absence of a round (or an Open round) is RecoveryNotStarted —
// the next Admit will re-evaluate IsComplete and drive Apply if
// the round has since sealed.
func (c *TVConsensusLogicalKeyConsumer) RecoveryProbe(ctx context.Context, key LogicalKey) (RecoveryStatus, error) {
	roundID := taskverification.RoundID(key)
	round, err := c.rounds.LoadRound(ctx, roundID)
	if err != nil {
		if errors.Is(err, taskverification.ErrRoundNotFound) {
			return RecoveryNotStarted, nil
		}
		return RecoveryNotStarted, fmt.Errorf("tv_consensus_lk: recovery probe load round %s: %w", key, err)
	}
	switch round.State {
	case taskverification.RoundStateFinalizedAccept,
		taskverification.RoundStateFinalizedReject,
		taskverification.RoundStateDisputed,
		taskverification.RoundStateExpired:
		return RecoveryCompleted, nil
	}
	return RecoveryNotStarted, nil
}

// verdictToPayloadString maps the Outcome.Verdict enum back to the
// wire string shape the settler expects in the payload. The settler
// switches on "pass" | "fail" | "abstain"; Outcome carries
// VerdictAccept | VerdictReject.
//
// VerdictAccept → "pass", VerdictReject → "fail". The dispute path
// ("abstain") is not reachable through this consumer under the seal
// rule — disputes resolve via the deadline checker in a separate
// settlement path. Unknown verdicts return "" so Apply fails loudly
// rather than invoking the settler with an unroutable verdict.
func verdictToPayloadString(v Verdict) string {
	switch v {
	case VerdictAccept:
		return "pass"
	case VerdictReject:
		return "fail"
	}
	return ""
}
