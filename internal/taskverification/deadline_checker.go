package taskverification

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// ConsensusPublisher is the minimal interface for publishing DAG events.
type ConsensusPublisher interface {
	Publish(ev *event.Event) error
}

// ActiveWeightFunc returns the total active validator weight.
type ActiveWeightFunc func() uint64

// DeadlineChecker periodically scans open rounds for deadline expiry.
// It extends convergent rounds once and finalizes non-convergent ones
// as disputed.
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
