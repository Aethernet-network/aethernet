package derivation

import (
	"context"
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// DeriveSettlement is the F5 Phase 5B pure derivation function. Given
// a finalized TaskVerificationRound, the canonical TaskVerificationConsensus
// event ID being settled, the canonical terminal verdict, and a bundle
// of canonical-state primitives, returns the ordered slice of
// PayoutRecord values that fully settle the round (or a StatusDeferred
// signal if required canonical state is not yet locally materialized).
//
// Purity (plan §2.4): no time.Now, no math/rand, no crypto/rand, no
// mutation of inputs, no package-level mutable state read, no
// map-iteration without sort-or-commutative discipline. Every field of
// inputs must satisfy the §2.1 DerivationInputs contract.
//
// Determinism (plan §2.6, property D-1): every correct node computing
// DeriveSettlement on the same canonical state produces a byte-
// identical DerivationResult for the canonical fields (Records,
// TerminalStatus, ResolvedCutoffAnchor, ResolvedCutoffAnchorIsNil).
//
// Deferral safety (plan §2.6, property D-2): materialization lag may
// cause temporary divergence in progress (node A defers; node B
// proceeds) but NEVER divergence in derived meaning. Once missing
// canonical state is locally available, node A's retry converges to
// the byte-identical DerivationResult node B produced.
//
// **Shape 3 (founder direction 2026-04-25)**: canonical seal context
// and epoch-at-finalization are sourced from canonical DAG primitives
// at call time (consensusEventID + dagReader.CountAncestorsByType),
// NOT from recognition-fabric projections on the round struct (which
// race with dispatcher LK consumer dispatch). Round-struct
// CanonicalSealContext + EpochAtFinalization are preserved as
// observability/projection per sub-spec §8.2 v2.4.
//
// Preconditions (caller responsibility):
//   - round is a TaskVerificationRound with vote set populated by the
//     vote consumer (race-free: votes appended pre-terminal-transition).
//     round.State is NOT consulted; the verdict is supplied via
//     terminalVerdict (Outcome.Verdict from LK consumer DeriveOutcome).
//   - consensusEventID is the EventID of the TaskVerificationConsensus
//     event triggering this settlement. The dispatcher LK consumer
//     guarantees this is the canonical event whose causal position
//     defines the settlement's seal context.
//   - terminalVerdict ∈ {TerminalAccept, TerminalReject}.
//     TerminalDispute is unreachable through the LK-consumer-driven
//     settlement path (IsComplete seal-rule gate; see derive_dispute.go
//     doc for the dispute-path semantics).
//   - inputs.escrowMgr has a canonical escrow entry for round.TaskID
//     (settler guarantees this by only calling DeriveSettlement for
//     rounds whose escrow was registered).
func DeriveSettlement(
	_ context.Context,
	round *taskverification.TaskVerificationRound,
	consensusEventID event.EventID,
	terminalVerdict TerminalStatus,
	inputs DerivationInputs,
) (DerivationResult, error) {
	if round == nil {
		return DerivationResult{}, errors.New("derivation: nil round")
	}
	if consensusEventID == "" {
		return DerivationResult{}, errors.New("derivation: consensusEventID required (Shape 3 canonical seal context)")
	}

	// Step 3: V-1 activation check for W. Determines stub-W vs real-W
	// per the consensus event's canonical position relative to
	// inputs.reputationActivationEventID. Multi-AI Item 1 composite
	// (2026-04-25): performed via the canonical AnchorReader.IsAncestor
	// primitive directly through the `isActivated` helper — no function-
	// field surface, so no closure-captured runtime state can hide here.
	//
	// **Shape 3**: seal context is consensusEventID (the TVConsensus
	// event ID being settled), sourced directly from the dispatcher LK
	// consumer call site rather than from round.CanonicalSealContext
	// (which races with terminal-transition population).
	useRealW, err := isActivated(inputs.dagReader, inputs.reputationActivationEventID, consensusEventID)
	if err != nil {
		if errors.Is(err, dag.ErrEventNotFound) {
			return DerivationResult{Status: StatusDeferred, Cause: DeferredCauseV1AncestorCheck}, nil
		}
		return DerivationResult{}, fmt.Errorf("derivation: V-1 activation check: %w", err)
	}

	// Step 1: compute canonical cutoff (anchor + epoch).
	//
	// **Shape 3**: epoch_of(R) is computed at call time via
	// CountAncestorsByType(consensusEventID, EventTypeEpochBoundary)
	// rather than read from round.EpochAtFinalization. Same canonical
	// value (sub-spec §3); cluster-uniform via the canonical DAG
	// primitive; race-free dispatch under concurrent commit-bus dispatch
	// (the recognition fabric's terminal-transition population is no
	// longer in the critical path).
	//
	// CountAncestorsByType materialization-lag (ErrEventNotFound) is
	// surfaced as Cause=DeferredCauseDAGAncestorBFS — same semantic as
	// ReadAtAnchor failures since both signal "a canonical-DAG ancestor-
	// traversal could not complete due to local materialization lag."
	cutoff, err := computeCutoff(inputs.dagReader, consensusEventID, useRealW)
	if err != nil {
		if errors.Is(err, dag.ErrEventNotFound) {
			return DerivationResult{Status: StatusDeferred, Cause: DeferredCauseDAGAncestorBFS}, nil
		}
		return DerivationResult{}, fmt.Errorf("derivation: cutoff: %w", err)
	}

	// Step 4: select implementations.
	var wImpl CanonicalWProjection
	var wMode string
	if useRealW {
		wImpl = inputs.w.Real
		wMode = "real"
	} else {
		wImpl = inputs.w.Stub
		wMode = "stub"
	}
	if wImpl == nil {
		// Forward-work guard: useRealW is true but inputs.w.Real is nil
		// (locked workstream's real implementation hasn't been wired).
		// Today: reputationActivationEventID is empty, so useRealW is
		// always false and this branch is unreachable. Surface loudly
		// if it ever fires — implies a wiring bug.
		return DerivationResult{}, fmt.Errorf("derivation: V-1 selected real W but inputs.w.Real is nil (ReputationActivation has not yet shipped its W.Real implementation)")
	}

	// Quality is currently always stubbed (per Plan v3 §2.3 step 4 +
	// canonical-epoch sub-spec §5 / FORWARD_NOTES). The future V-1
	// quality activation pattern follows the same shape; for now,
	// inputs.qualityActivationEventID is the empty-string placeholder so
	// `isActivated` short-circuits to (false, nil) and we always select
	// Stub.
	useRealQuality, err := isActivated(inputs.dagReader, inputs.qualityActivationEventID, consensusEventID)
	if err != nil {
		if errors.Is(err, dag.ErrEventNotFound) {
			return DerivationResult{Status: StatusDeferred, Cause: DeferredCauseV1AncestorCheck}, nil
		}
		return DerivationResult{}, fmt.Errorf("derivation: V-1 activation check (quality): %w", err)
	}
	var qualityImpl CanonicalQualityProjection
	var qualityMode string
	if useRealQuality {
		qualityImpl = inputs.quality.Real
		qualityMode = "real"
	} else {
		qualityImpl = inputs.quality.Stub
		qualityMode = "stub"
	}
	if qualityImpl == nil {
		return DerivationResult{}, fmt.Errorf("derivation: V-1 selected real Quality but inputs.quality.Real is nil")
	}

	// Resolve escrow + funding canonical-frozen values. These reads
	// satisfy DerivationInputs contract clause (b) — canonical-frozen
	// at RegisterEscrow time.
	budget, err := inputs.escrowMgr.EscrowAmount(round.TaskID)
	if err != nil {
		return DerivationResult{}, fmt.Errorf("derivation: EscrowAmount(%s): %w", round.TaskID, err)
	}
	fundingRef, err := inputs.escrowMgr.FundingRef(round.TaskID)
	if err != nil {
		return DerivationResult{}, fmt.Errorf("derivation: FundingRef(%s): %w", round.TaskID, err)
	}
	posterID := round.PosterID

	// Step 5: route on the canonical terminalVerdict supplied by the LK
	// consumer (Outcome.Verdict from DeriveOutcome via IsComplete seal-
	// rule). Replaces the prior round.State switch which was a
	// recognition-fabric projection populated atomically with terminal
	// transition (Shape 3 race-resolution).
	//
	// TerminalDispute is NEVER reached through this path: the LK
	// consumer's IsComplete seal-rule guarantees Apply only fires on
	// passSealed || failSealed → Accept or Reject. Dispute settlements
	// route through the legacy escrow.ReleaseNet path via
	// autovalidator.processDisputedTasks per dispute-path tracing
	// (2026-04-25). derive_dispute.go is preserved as forward-compat
	// scaffolding (see its doc comment); the default branch errors out
	// per Item 2 V#4 enum-exhaustiveness pattern so any future
	// terminalVerdict extension that doesn't update this dispatch
	// surfaces at runtime.
	var (
		records  []PayoutRecord
		status   DerivationStatus
		cause    DeferredCause
		summary  partialSummary
		terminal TerminalStatus
		eerr     error
	)
	switch terminalVerdict {
	case TerminalAccept:
		records, status, cause, summary, eerr = deriveAccept(round, cutoff, wImpl, qualityImpl, inputs, budget, posterID, fundingRef, inputs.treasuryID)
		terminal = TerminalAccept
	case TerminalReject:
		records, status, cause, summary, eerr = deriveReject(round, cutoff, wImpl, inputs, budget, posterID, fundingRef, inputs.treasuryID)
		terminal = TerminalReject
	default:
		return DerivationResult{}, fmt.Errorf("derivation: unsupported terminalVerdict %v (LK-consumer-driven path produces TerminalAccept | TerminalReject only; TerminalDispute routes via legacy escrow.ReleaseNet)", terminalVerdict)
	}
	if eerr != nil {
		return DerivationResult{}, eerr
	}
	if status == StatusDeferred {
		return DerivationResult{Status: StatusDeferred, Cause: cause, Summary: buildSummary(summary, wMode, qualityMode)}, nil
	}

	// Step 7 (assembly): populate Provenance on each record + assign
	// canonical_id via SHA-256(JCS(record without canonical_id)).
	verdict := terminalToVerdict(terminal)
	for i := range records {
		records[i].Provenance = Provenance{
			RoundVerdict:               verdict,
			CanonicalCutoffAnchor:      cutoff.anchor,
			CanonicalCutoffAnchorIsNil: cutoff.anchorIsNil,
		}
	}

	// Step 7 (ordinal assignment per schema 4-step rule). Sorts records
	// into canonical order and assigns Purpose.Ordinal = monotone counter.
	records = AssignOrdinals(records)

	// Step 7 (canonical_id hash). Compute AFTER ordinals are assigned
	// because Purpose.Ordinal participates in the canonical_id preimage
	// (uniqueness invariant U-1).
	for i := range records {
		cid, hashErr := ComputeCanonicalID(records[i])
		if hashErr != nil {
			return DerivationResult{}, fmt.Errorf("derivation: ComputeCanonicalID for record %d: %w", i, hashErr)
		}
		records[i].CanonicalID = cid
	}

	// Step 8: build DerivationSummary.
	finalSummary := buildSummary(summary, wMode, qualityMode)
	for _, r := range records {
		if finalSummary.RecordCountByRole == nil {
			finalSummary.RecordCountByRole = make(map[string]uint32)
		}
		finalSummary.RecordCountByRole[string(r.Recipient.Role)]++
	}

	// Step 9: return the StatusDerived result.
	return DerivationResult{
		Status:                    StatusDerived,
		Records:                   records,
		TerminalStatus:            terminal,
		ResolvedCutoffAnchor:      cutoff.anchor,
		ResolvedCutoffAnchorIsNil: cutoff.anchorIsNil,
		Summary:                   finalSummary,
	}, nil
}

// terminalToVerdict maps the internal TerminalStatus enum to the
// schema-locked Verdict enum used in PayoutRecord.Provenance.RoundVerdict.
func terminalToVerdict(t TerminalStatus) Verdict {
	switch t {
	case TerminalAccept:
		return VerdictAccept
	case TerminalReject:
		return VerdictReject
	case TerminalDispute:
		return VerdictDispute
	}
	return ""
}

// buildSummary populates the DerivationSummary with the W + Quality
// mode labels from V-1 selection plus the partial counters from the
// route handler. RecordCountByRole is filled in by the caller after
// records are assembled.
func buildSummary(p partialSummary, wMode, qualityMode string) DerivationSummary {
	return DerivationSummary{
		SelectedWMode:          wMode,
		SelectedQualityMode:    qualityMode,
		GenLedgerTraversalRan:  p.GenLedgerTraversalRan,
		GenLedgerAncestorCount: p.GenLedgerAncestorCount,
		AgreeingValidatorCount: p.AgreeingValidatorCount,
	}
}
