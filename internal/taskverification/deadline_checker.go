package taskverification

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/roundpolicy"
	"github.com/Aethernet-network/aethernet/internal/roundprogress"
)

// ConsensusPublisher is the minimal interface for publishing DAG events.
type ConsensusPublisher interface {
	Publish(ev *event.Event) error
}

// ActiveWeightFunc returns the total active validator weight.
type ActiveWeightFunc func() uint64

// ActiveValidatorCountFunc returns the total number of active validators.
type ActiveValidatorCountFunc func() int

// ProgressSnapshotStore is the read interface for round progress snapshots.
// Satisfied by roundprogress.SnapshotStore implementations.
type ProgressSnapshotStore interface {
	GetAllForRound(roundID string) ([]*roundprogress.RoundProgressSnapshot, error)
}

// DeadlineChecker periodically scans open rounds for deadline expiry.
// When a RoundPolicyEvaluator and ProgressSnapshotStore are wired, it uses
// the 5-step progress-aware algorithm from §6.4 of the BlobSync design.
// Otherwise falls back to the original deadline-based behavior.
type DeadlineChecker struct {
	rounds           Store
	finalizer        *Finalizer
	publisher        ConsensusPublisher
	kp               *crypto.KeyPair
	validatorID      crypto.AgentID
	activeWeightFn   ActiveWeightFunc
	interval         time.Duration
	extensionSeconds int64
	clock            func() int64

	// Progress-aware finalization (optional, from BlobSync prompt 06).
	policyEvaluator     *roundpolicy.RoundPolicyEvaluator
	progressStore       ProgressSnapshotStore
	activeCountFn       ActiveValidatorCountFunc
	backstopSeconds     int64 // absolute backstop from round open, default 300 (5 min)

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewDeadlineChecker creates a checker that scans open rounds for expiry.
func NewDeadlineChecker(
	rounds Store,
	finalizer *Finalizer,
	publisher ConsensusPublisher,
	kp *crypto.KeyPair,
	validatorID crypto.AgentID,
	activeWeightFn ActiveWeightFunc,
	interval time.Duration,
	extensionSeconds int64,
	clock func() int64,
) *DeadlineChecker {
	return &DeadlineChecker{
		rounds:           rounds,
		finalizer:        finalizer,
		publisher:        publisher,
		kp:               kp,
		validatorID:      validatorID,
		activeWeightFn:   activeWeightFn,
		interval:         interval,
		extensionSeconds: extensionSeconds,
		clock:            clock,
		stop:             make(chan struct{}),
	}
}

// SetProgressAware wires the progress-aware finalization components.
// When set, the checker uses the 5-step algorithm instead of static deadlines.
func (d *DeadlineChecker) SetProgressAware(
	evaluator *roundpolicy.RoundPolicyEvaluator,
	progressStore ProgressSnapshotStore,
	activeCountFn ActiveValidatorCountFunc,
	backstopSeconds int64,
) {
	d.policyEvaluator = evaluator
	d.progressStore = progressStore
	d.activeCountFn = activeCountFn
	if backstopSeconds <= 0 {
		backstopSeconds = 300 // 5 minutes
	}
	d.backstopSeconds = backstopSeconds
}

// Start launches the periodic deadline check goroutine.
func (d *DeadlineChecker) Start() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-ticker.C:
				d.check()
			}
		}
	}()
}

// Stop signals the checker to exit and waits for it to finish.
func (d *DeadlineChecker) Stop() {
	close(d.stop)
	d.wg.Wait()
}

func (d *DeadlineChecker) check() {
	if d.policyEvaluator != nil && d.progressStore != nil {
		d.checkProgressAware()
		return
	}
	d.checkLegacy()
}

// checkLegacy is the original deadline-based finalization path.
// Used when the progress-aware components are not wired.
func (d *DeadlineChecker) checkLegacy() {
	ctx := context.Background()
	open, err := d.rounds.ListOpenRounds(ctx)
	if err != nil {
		slog.Warn("deadline_checker: failed to list open rounds", "err", err)
		return
	}

	now := d.clock()
	totalWeight := d.activeWeightFn()

	for _, round := range open {
		deadline := round.DeadlineForCurrentPhase()
		if now <= deadline {
			continue // not expired yet
		}

		decision := d.finalizer.Evaluate(round, totalWeight, now)

		if decision.ShouldFinalize {
			d.applyFinalization(ctx, round, decision, now)
			continue
		}

		// Deadline expired but finalizer says don't finalize — convergence
		// is plausible. Extend once if not already extended.
		if round.ExtendedUntilUnix == 0 {
			round.ExtendedUntilUnix = round.DeadlineUnix + d.extensionSeconds
			if err := d.rounds.SaveRound(ctx, round); err != nil {
				slog.Warn("deadline_checker: failed to save extended round",
					"round_id", round.RoundID, "err", err)
				continue
			}
			slog.Info("deadline_checker: round extended",
				"round_id", round.RoundID,
				"task_id", round.TaskID,
				"extended_until", round.ExtendedUntilUnix,
				"pass_weight", round.PassWeight,
				"fail_weight", round.FailWeight,
			)
		}
	}
}

// checkProgressAware is the 5-step progress-aware finalization path.
// Scans ALL open rounds (not just expired ones) and uses the RoundPolicy
// evaluator to decide what to do based on validator progress state.
func (d *DeadlineChecker) checkProgressAware() {
	ctx := context.Background()
	open, err := d.rounds.ListOpenRounds(ctx)
	if err != nil {
		slog.Warn("deadline_checker: failed to list open rounds", "err", err)
		return
	}

	now := d.clock()
	totalWeight := d.activeWeightFn()

	for _, round := range open {
		state := d.buildRoundState(round, totalWeight, now)
		decision := d.policyEvaluator.Evaluate(state, now)

		switch decision {
		case roundpolicy.WaitDecisionFinalizeNow:
			// Evaluate verdict using the existing finalizer (durable votes only).
			verdictDecision := d.finalizer.Evaluate(round, totalWeight, now)
			if verdictDecision.ShouldFinalize {
				d.applyFinalization(ctx, round, verdictDecision, now)
			} else {
				// Policy says finalize but the finalizer disagrees (e.g., no
				// BFT threshold met despite all being terminal). This means
				// all validators are done but votes are insufficient → dispute.
				disputeDecision := FinalizationDecision{
					ShouldFinalize: true,
					Verdict:        VerdictAbstain,
					Reason:         ReasonDeadlineExpiredDispute,
				}
				d.applyFinalization(ctx, round, disputeDecision, now)
			}

		case roundpolicy.WaitDecisionWaitForActive:
			// Active leases — do nothing, check again next tick.
			slog.Debug("deadline_checker: waiting for active validators",
				"round_id", round.RoundID,
				"active_leases", state.ActiveLeaseCount,
				"max_expiry", state.MaxLeaseExpiryUnix,
			)

		case roundpolicy.WaitDecisionWaitForBackstop:
			// No active progress, votes insufficient. Wait for backstop.
			slog.Debug("deadline_checker: waiting for backstop",
				"round_id", round.RoundID,
				"backstop", state.BackstopUnix,
				"voted", state.VotedCount,
				"total", state.TotalValidators,
			)

		case roundpolicy.WaitDecisionExpired:
			// Backstop reached. Try the finalizer — if it can finalize with
			// existing votes, do so. Otherwise → dispute.
			verdictDecision := d.finalizer.Evaluate(round, totalWeight, now)
			if verdictDecision.ShouldFinalize {
				d.applyFinalization(ctx, round, verdictDecision, now)
			} else {
				disputeDecision := FinalizationDecision{
					ShouldFinalize: true,
					Verdict:        VerdictAbstain,
					Reason:         ReasonDeadlineExpiredDispute,
				}
				d.applyFinalization(ctx, round, disputeDecision, now)
			}
		}
	}
}

// buildRoundState constructs a RoundState from the round's durable votes
// and progress snapshots for the policy evaluator.
//
// Authority boundary: OutcomeSecured is computed from durable votes only.
// Signed-abstain in progress counts as terminal (don't wait) but does NOT
// add vote weight. Stale/silent validators are uncounted.
func (d *DeadlineChecker) buildRoundState(
	round *TaskVerificationRound,
	totalWeight uint64,
	nowUnix int64,
) roundpolicy.RoundState {
	totalValidators := 0
	if d.activeCountFn != nil {
		totalValidators = d.activeCountFn()
	}
	if totalValidators == 0 {
		totalValidators = 5 // fallback for testnet
	}

	// Count distinct validators who have voted (durable votes on DAG).
	votedValidators := make(map[string]bool)
	for _, v := range round.Votes {
		votedValidators[string(v.ValidatorID)] = true
	}
	votedCount := len(votedValidators)

	// Get progress snapshots for this round.
	var snapshots []*roundprogress.RoundProgressSnapshot
	if d.progressStore != nil {
		var err error
		snapshots, err = d.progressStore.GetAllForRound(string(round.RoundID))
		if err != nil {
			slog.Debug("deadline_checker: progress store error",
				"round_id", round.RoundID, "err", err)
		}
	}

	// Categorize validators based on progress snapshots.
	terminalFromProgress := 0
	activeLeaseCount := 0
	var maxLeaseExpiry int64

	for _, snap := range snapshots {
		// Skip validators who already have durable votes.
		if votedValidators[snap.ValidatorID] {
			continue
		}

		if snap.CurrentPhase.IsTerminal() {
			// Signed-abstain or signed-failed in progress.
			// Terminal: don't wait. Does NOT add vote weight.
			terminalFromProgress++
		} else if snap.LeaseExpiryUnix > nowUnix {
			// Active lease — validator is working.
			activeLeaseCount++
			if snap.LeaseExpiryUnix > maxLeaseExpiry {
				maxLeaseExpiry = snap.LeaseExpiryUnix
			}
		}
		// Else: stale (non-terminal, expired lease). Counted as stale below.
	}

	terminalCount := votedCount + terminalFromProgress
	staleCount := totalValidators - terminalCount - activeLeaseCount
	if staleCount < 0 {
		staleCount = 0
	}

	// OutcomeSecured: check if current durable votes satisfy BFT threshold
	// + diversity floor + median score, such that remaining votes cannot
	// change the outcome. Computed from durable votes only (authority boundary).
	outcomeSecured := d.isOutcomeSecured(round, totalWeight)

	backstopUnix := round.OpenedAtUnix + d.backstopSeconds

	return roundpolicy.RoundState{
		TotalValidators:    totalValidators,
		VotedCount:         votedCount,
		TerminalCount:      terminalCount,
		ActiveLeaseCount:   activeLeaseCount,
		StaleCount:         staleCount,
		MaxLeaseExpiryUnix: maxLeaseExpiry,
		OutcomeSecured:     outcomeSecured,
		BackstopUnix:       backstopUnix,
	}
}

// isOutcomeSecured checks if the current durable votes make the outcome
// mathematically certain regardless of remaining votes. Uses the existing
// finalizer's BFT threshold, diversity floor, and median score checks.
//
// This is conservative: it only returns true when the existing votes ALREADY
// satisfy all finalization criteria. It does not speculate about future votes.
func (d *DeadlineChecker) isOutcomeSecured(round *TaskVerificationRound, totalWeight uint64) bool {
	// Check if the finalizer would finalize right now.
	decision := d.finalizer.Evaluate(round, totalWeight, d.clock())
	return decision.ShouldFinalize && decision.Reason != ReasonDeadlineExpiredDispute
}

func (d *DeadlineChecker) applyFinalization(ctx context.Context, round *TaskVerificationRound, decision FinalizationDecision, now int64) {
	targetState := VerdictToState(decision.Verdict)
	if err := round.Transition(targetState, now); err != nil {
		return // already finalized
	}
	round.FinalVerdict = decision.Verdict
	round.FinalScoreBP = decision.FinalScoreBP

	// Persist BEFORE publish (ordering invariant).
	if err := d.rounds.SaveRound(ctx, round); err != nil {
		slog.Warn("deadline_checker: failed to save finalized round",
			"round_id", round.RoundID, "err", err)
		return
	}

	EmitConsensusEvent(round, d.publisher, d.kp, d.validatorID, decision.Reason)

	slog.Info("deadline_checker: round finalized",
		"round_id", round.RoundID,
		"task_id", round.TaskID,
		"verdict", decision.Verdict,
		"reason", decision.Reason,
		"score_bp", decision.FinalScoreBP,
	)
}

// EmitConsensusEvent builds and publishes a TaskVerificationConsensus DAG
// event for a finalized round. Shared by the vote consumer and deadline
// checker. The semantic parent is the TaskSubmitted event.
func EmitConsensusEvent(
	round *TaskVerificationRound,
	publisher ConsensusPublisher,
	kp *crypto.KeyPair,
	validatorID crypto.AgentID,
	reason FinalizationReason,
) {
	if publisher == nil || kp == nil {
		return
	}

	// Build participating families list.
	var families []string
	for f, w := range round.ParticipatingFamilies {
		if w > 0 {
			families = append(families, f)
		}
	}

	payload := event.TaskVerificationConsensusPayload{
		Version:               1,
		RoundID:               string(round.RoundID),
		TaskID:                round.TaskID,
		SubmissionEventID:     string(round.SubmissionEventID),
		WorkerID:              string(round.WorkerID),
		PosterID:              string(round.PosterID),
		FinalVerdict:          round.FinalVerdict.String(),
		FinalScoreBP:          round.FinalScoreBP,
		PassWeight:            round.PassWeight,
		FailWeight:            round.FailWeight,
		AbstainWeight:         round.AbstainWeight,
		TotalActiveWeight:     0, // filled below if available
		ParticipatingFamilies: families,
		DiversityFloorMet:     round.DistinctPassFamilies() >= round.DiversityFloor,
		VoteCount:             len(round.Votes),
		FinalizationTimeUnix:  round.FinalizationTime,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("consensus_event: marshal failed", "round_id", round.RoundID, "err", err)
		return
	}

	// Semantic parent: the TaskSubmitted event.
	refs := []event.EventID{round.SubmissionEventID}
	ev, err := event.New(
		event.EventTypeTaskVerificationConsensus,
		refs,
		json.RawMessage(payloadBytes),
		string(validatorID),
		nil,
		0,
	)
	if err != nil {
		slog.Warn("consensus_event: create failed", "round_id", round.RoundID, "err", err)
		return
	}

	if err := crypto.SignEvent(ev, kp); err != nil {
		slog.Warn("consensus_event: sign failed", "round_id", round.RoundID, "err", err)
		return
	}

	if err := publisher.Publish(ev); err != nil {
		slog.Debug("consensus_event: publish failed (may be duplicate)",
			"round_id", round.RoundID, "err", err)
	} else {
		slog.Info("consensus_event: TaskVerificationConsensus emitted",
			"round_id", round.RoundID,
			"task_id", round.TaskID,
			"verdict", round.FinalVerdict,
			"event_id", ev.ID,
		)
	}
}

// VerdictToState maps a Verdict to the appropriate terminal RoundState.
func VerdictToState(v Verdict) RoundState {
	switch v {
	case VerdictPass:
		return RoundStateFinalizedAccept
	case VerdictFail:
		return RoundStateFinalizedReject
	default:
		return RoundStateDisputed
	}
}
