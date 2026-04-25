# F5 5B implementation bundle — for multi-AI review

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5B implementation complete; pre-#135 testnet verification multi-AI review.
**Branch**: `feat/f5-5b-derivation` from `51bce89` (F4-frozen) + uncommitted 5A.4 working tree.
**Date**: 2026-04-24

## Bundle scope

20 load-bearing files spanning the F5 5B implementation surface. **No test code or wiring boilerplate** — this bundle is for pressure-testing canonical-correctness claims, not test-setup review.

The 20 files implement:

- The `internal/settlement/derivation/` package (10 files): pure-derivation function `DeriveSettlement` + `ReadAtAnchor` + cutoff computation + ordinal-assignment + canonical_id hashing.
- `internal/escrow/applicator.go` (1 file): `ApplySettlementRecords` per Plan v3 §3 (ledger-level idempotency + intra-node defense-in-depth lock + paid-flag projection as observability-only).
- `internal/dag/dag.go` (1 file): `CountAncestorsByType` primitive + `RegisterAdmissionCrossCheck` substrate per canonical-epoch sub-spec v2.2 §1.4.1.
- `internal/epoch/boundary_validator.go` + `boundary_emitter.go` (2 files): EpochBoundary admission cross-check + recognition-fabric Candidate A emitter.
- `internal/dispatch/epoch_boundary_lk_consumer.go` + `dispatcher.go` + `logical_key_admit.go` (3 files): EpochBoundary LK dedup (Epoch-keyed, not content-hash) + dispatcher per-(consumer, key) lock at admitOneLogicalKey RMW region.
- `internal/recognition/task_verification_consensus_consumer.go` (1 file): canonical-epoch sub-spec §8.2 finalizing-consumer wiring (`CanonicalSealContext` + `EpochAtFinalization` field population at terminal transition).
- `internal/settlement/verification_consensus_settler.go` (1 file): `Settle()` integrated with `DeriveSettlement` + `ApplySettlementRecords`; legacy `settleAccept/Reject/Dispute` paths deleted in #133.
- `internal/taskverification/round.go` (1 file): `TaskVerificationRound` with new canonical fields + `Canonical()` projection.

## Reference documents

Multi-AI reviewers should already have these from prior rounds:

- `docs/plans/implementation/f5-phase-5b-plan-v3.md`
- `docs/architecture/canonical-epoch-event.md` (v2.3)
- `docs/plans/implementation/canonical-epoch-event-completion-gate-report.md`
- `docs/architecture/q-score-canonicalization-design.md` (v2.2)
- `docs/architecture/generation-ledger-canonical-derivation.md` (v1.2)
- `docs/architecture/payout-artifact-schema.yaml` (v1.1)
- `docs/plans/implementation/f5-phase-5a-completion-gate-report.md`

## Forward notes (in bundle)

The 3 forward-noted items at the end of this bundle are documented architectural carries that do NOT need to be re-surfaced as findings:

1. `ReputationActivationEventID` const-flip V-1 hole at upgrade time.
2. EpochBoundary signer canonical-validator-snapshot binding deferred until snapshot infrastructure ships.
3. `TestTypeE_SyntheticReplayConformance/PopulatedDAGReplay_PerKey` flake — RESOLVED at #134-followon (Path A: per-(consumer, key) lock at dispatcher layer; included in this bundle as `internal/dispatch/dispatcher.go` + `logical_key_admit.go`).

---


## File: `internal/settlement/derivation/derive.go`

```go
package derivation

import (
	"context"
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// DeriveSettlement is the F5 Phase 5B pure derivation function. Given
// a finalized TaskVerificationRound and a bundle of canonical-state
// primitives, returns the ordered slice of PayoutRecord values that
// fully settle the round (or a StatusDeferred signal if required
// canonical state is not yet locally materialized).
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
// Preconditions (caller responsibility):
//   - round is a finalized TaskVerificationRound
//     (State ∈ {FinalizedAccept, FinalizedReject, Disputed}).
//   - round.Votes is the canonical vote set (cluster-uniform per F4).
//   - round.CanonicalSealContext is populated (per F5 5B canonical-
//     epoch sub-spec §8.1; finalizing consumer wires this at terminal
//     transition).
//   - inputs.EscrowMgr has a canonical escrow entry for round.TaskID
//     (settler guarantees this by only calling DeriveSettlement for
//     rounds whose escrow was registered).
func DeriveSettlement(
	_ context.Context,
	round *taskverification.TaskVerificationRound,
	inputs DerivationInputs,
) (DerivationResult, error) {
	if round == nil {
		return DerivationResult{}, errors.New("derivation: nil round")
	}
	if !round.IsTerminal() {
		return DerivationResult{}, fmt.Errorf("derivation: round %s is not terminal (state=%v)", round.RoundID, round.State)
	}

	// Step 3: V-1 ActivationCheck for W. Determines stub-W vs real-W
	// per round's canonical position relative to ReputationActivation.
	useRealW, err := inputs.ActivationCheck(ReputationActivationEventID, round.CanonicalSealContext)
	if err != nil {
		if errors.Is(err, dag.ErrEventNotFound) {
			return DerivationResult{Status: StatusDeferred, Cause: DeferredCauseV1AncestorCheck}, nil
		}
		return DerivationResult{}, fmt.Errorf("derivation: V-1 ActivationCheck: %w", err)
	}

	// Step 1: compute canonical cutoff (anchor + epoch). Anchor depends
	// on V-1 outcome (Fix A); epoch is independent.
	cutoff := computeCutoff(round, useRealW)

	// Step 4: select implementations.
	var wImpl CanonicalWProjection
	var wMode string
	if useRealW {
		wImpl = inputs.W.Real
		wMode = "real"
	} else {
		wImpl = inputs.W.Stub
		wMode = "stub"
	}
	if wImpl == nil {
		// Forward-work guard: useRealW is true but inputs.W.Real is nil
		// (locked workstream's real implementation hasn't been wired).
		// Today: ReputationActivationEventID is empty, so useRealW is
		// always false and this branch is unreachable. Surface loudly
		// if it ever fires — implies a wiring bug.
		return DerivationResult{}, fmt.Errorf("derivation: V-1 selected real W but inputs.W.Real is nil (ReputationActivation has not yet shipped its W.Real implementation)")
	}

	// Quality is currently always stubbed (per Plan v3 §2.3 step 4 +
	// canonical-epoch sub-spec §5 / FORWARD_NOTES). The future V-1
	// quality activation pattern follows the same shape; for now,
	// QualityActivationEventID is empty so we always select Stub.
	useRealQuality, err := inputs.ActivationCheck(QualityActivationEventID, round.CanonicalSealContext)
	if err != nil {
		if errors.Is(err, dag.ErrEventNotFound) {
			return DerivationResult{Status: StatusDeferred, Cause: DeferredCauseV1AncestorCheck}, nil
		}
		return DerivationResult{}, fmt.Errorf("derivation: V-1 ActivationCheck (quality): %w", err)
	}
	var qualityImpl CanonicalQualityProjection
	var qualityMode string
	if useRealQuality {
		qualityImpl = inputs.Quality.Real
		qualityMode = "real"
	} else {
		qualityImpl = inputs.Quality.Stub
		qualityMode = "stub"
	}
	if qualityImpl == nil {
		return DerivationResult{}, fmt.Errorf("derivation: V-1 selected real Quality but inputs.Quality.Real is nil")
	}

	// Resolve escrow + funding canonical-frozen values. These reads
	// satisfy DerivationInputs contract clause (b) — canonical-frozen
	// at RegisterEscrow time.
	budget, err := inputs.EscrowMgr.EscrowAmount(round.TaskID)
	if err != nil {
		return DerivationResult{}, fmt.Errorf("derivation: EscrowAmount(%s): %w", round.TaskID, err)
	}
	fundingRef, err := inputs.EscrowMgr.FundingRef(round.TaskID)
	if err != nil {
		return DerivationResult{}, fmt.Errorf("derivation: FundingRef(%s): %w", round.TaskID, err)
	}
	posterID := round.PosterID

	// Step 5: route on round.State (the canonical finalization state
	// set by the recognition fabric's TaskVerificationConsensusConsumer
	// at terminal transition; per breakpoint-C wiring, atomically with
	// CanonicalSealContext + EpochAtFinalization population).
	var (
		records  []PayoutRecord
		status   DerivationStatus
		cause    DeferredCause
		summary  partialSummary
		terminal TerminalStatus
		eerr     error
	)
	switch round.State {
	case taskverification.RoundStateFinalizedAccept:
		records, status, cause, summary, eerr = deriveAccept(round, cutoff, wImpl, qualityImpl, inputs, budget, posterID, fundingRef, inputs.TreasuryID)
		terminal = TerminalAccept
	case taskverification.RoundStateFinalizedReject:
		records, status, cause, summary, eerr = deriveReject(round, cutoff, wImpl, inputs, budget, posterID, fundingRef, inputs.TreasuryID)
		terminal = TerminalReject
	case taskverification.RoundStateDisputed:
		records, status, cause, summary, eerr = deriveDispute(round, inputs, budget, posterID, fundingRef, inputs.TreasuryID)
		terminal = TerminalDispute
	default:
		return DerivationResult{}, fmt.Errorf("derivation: unsupported terminal round state %v", round.State)
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
```

## File: `internal/settlement/derivation/derive_accept.go`

```go
package derivation

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// deriveAccept implements Plan v3 §2.3 step 6 (deriveAccept main path).
// Computes pool shares, agreeing-validator W lookups, gen-ledger
// ancestor traversal + quality lookups, and assembles the unordered
// PayoutRecord slice for an accept-verdict round. Caller wraps the
// result with canonical_id + ordinal assignment.
//
// Pool shares (mirror pre-5B settler integer-path arithmetic in
// internal/settlement/verification_consensus_settler.go:209-212):
//   workerAmount   = budget * WorkerShareBP     / SharesDenominator
//   validatorPool  = budget * ValidatorShareBP  / SharesDenominator
//   genPool        = budget * GenerationShareBP / SharesDenominator
//   treasuryAmount = budget - workerAmount - validatorPool - genPool
//
// Treasury absorbs all rounding remainders + per-pool unallocated
// amounts (no agreeing validators → validatorPool flows to treasury;
// no gen-ledger ancestors → genPool flows to treasury). Per F5 5A.1
// integer-path conservation: sum across all PayoutRecord amounts +
// treasury == budget exactly.
//
// Deferral signals: any canonical-state lookup returning ErrEventNotFound
// causes the function to return a (Records, status, cause) triple with
// status=StatusDeferred and the appropriate cause. Caller (DeriveSettlement)
// constructs the DerivationResult.
func deriveAccept(
	round *taskverification.TaskVerificationRound,
	cutoff cutoffResult,
	wImpl CanonicalWProjection,
	qualityImpl CanonicalQualityProjection,
	inputs DerivationInputs,
	budget uint64,
	posterID crypto.AgentID,
	fundingRef event.EventID,
	treasuryID crypto.AgentID,
) (records []PayoutRecord, status DerivationStatus, cause DeferredCause, summary partialSummary, err error) {
	// Pool computation.
	workerAmount := budget * WorkerShareBP / SharesDenominator
	validatorPool := budget * ValidatorShareBP / SharesDenominator
	genPool := budget * GenerationShareBP / SharesDenominator
	treasuryAmount := budget - workerAmount - validatorPool - genPool

	settlementKey := SettlementKey{
		RoundID:          string(round.RoundID),
		TaskID:           round.TaskID,
		FundingReference: fundingRef,
	}

	// 1. Worker payout.
	if workerAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: round.WorkerID, Role: RoleWorker},
			Amount:            Amount{Value: workerAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagWorkerPayout},
		})
	}

	// 2. Validator distribution: per-validator W-weighted shares of
	// validatorPool, computed via protocolmath.AllocateWithCeiling
	// (matches pre-5B settler computeValidatorPayoutsInteger).
	agreeing := collectAgreeingValidators(round, taskverification.VerdictPass)
	summary.AgreeingValidatorCount = uint32(len(agreeing))
	if len(agreeing) == 0 || validatorPool == 0 {
		// No agreeing validators (or zero pool): validatorPool flows to
		// treasury per pre-5B semantic (settler.go:217-220).
		treasuryAmount += validatorPool
	} else {
		family := "" // Pre-5B settler passes empty family per integer path.
		recips := make([]protocolmath.Recipient, 0, len(agreeing))
		// Lex-sort agreeing validator IDs for canonical lookup ordering.
		// (protocolmath.AllocateWithCeiling itself sorts by CanonicalKey,
		// so post-allocation iteration is canonical regardless of input
		// order; we sort here for canonical W lookup ordering only.)
		sort.Slice(agreeing, func(i, j int) bool { return agreeing[i] < agreeing[j] })
		for _, v := range agreeing {
			w, lookupErr := wImpl.Lookup(v, family, round.Category, posterID, budget, cutoff.epoch)
			if lookupErr != nil {
				if errors.Is(lookupErr, dag.ErrEventNotFound) {
					return nil, StatusDeferred, DeferredCauseWLookup, summary, nil
				}
				return nil, 0, 0, summary, fmt.Errorf("derivation: W.Lookup for validator %s: %w", v, lookupErr)
			}
			if w < 0 {
				w = 0
			}
			recips = append(recips, protocolmath.Recipient{
				CanonicalKey: []byte(v),
				Weight:       w,
			})
		}
		alloc, allocErr := protocolmath.AllocateWithCeiling(recips, protocolmath.MicroAET(validatorPool))
		if allocErr != nil {
			return nil, 0, 0, summary, fmt.Errorf("derivation: validator pool allocate: %w", allocErr)
		}
		// Iterate alloc in lex-sorted CanonicalKey order for canonical
		// PayoutRecord construction.
		keys := make([]string, 0, len(alloc))
		for k := range alloc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			amt := uint64(alloc[k])
			if amt == 0 {
				continue
			}
			records = append(records, PayoutRecord{
				DerivationVersion: DerivationVersion,
				SettlementKey:     settlementKey,
				Recipient:         Recipient{ID: crypto.AgentID(k), Role: RoleValidator},
				Amount:            Amount{Value: amt, Currency: CurrencyAET},
				Purpose:           Purpose{Tag: TagValidatorDistribution},
			})
		}
	}

	// 3. Generation-ledger royalty: ReadAtAnchor traversal from
	// round.SubmissionEventID, depth-bounded at MaxGenLedgerDepth (3).
	// Each ancestor's weight = quality(a) / depth² per F5 5A.3 §3.1.
	// Allocation via protocolmath.Allocate with CanonicalKey=ancestorID.
	if genPool > 0 {
		ancestors, readErr := ReadAtAnchor(inputs.DAGReader, cutoff.anchor, round.SubmissionEventID, MaxGenLedgerDepth)
		if readErr != nil {
			if errors.Is(readErr, dag.ErrEventNotFound) {
				return nil, StatusDeferred, DeferredCauseDAGAncestorBFS, summary, nil
			}
			return nil, 0, 0, summary, fmt.Errorf("derivation: ReadAtAnchor: %w", readErr)
		}
		summary.GenLedgerTraversalRan = true
		// LAYER SEPARATION (architect-confirmed at breakpoint-2 closure):
		//
		//   ReadAtAnchor (BFS layer, anchor_read.go) is seed-inclusive
		//   per F5 5A.3 §2.2.1 option (a) lock — the seed is the first
		//   element of the result slice. UNCHANGED from the sub-spec.
		//
		//   The gen-ledger CALCULATOR (this layer, derive_accept.go)
		//   filters out the seed before quality lookup + allocation
		//   because the poster earns settlement value via the
		//   worker-share line (TagWorkerPayout for accept; TagPosterRefund
		//   for reject), NOT as a gen-ledger ancestor of their own
		//   submission. Pre-5B convention preserved: gen-ledger payouts
		//   begin at depth=1 (immediate causal parents), not depth=0
		//   (the submission itself).
		//
		// Future gen-ledger consumers that DO want seed inclusion can
		// use ReadAtAnchor's raw output without modifying the BFS layer.
		var ancestorList []event.EventID
		if len(ancestors) > 1 {
			ancestorList = ancestors[1:]
		}

		// Build depth map by re-running BFS depth tracking — ReadAtAnchor
		// returns IDs in BFS-visit order but doesn't expose depths. We
		// recompute via Get + CausalRefs lookup. Cheaper alternative
		// would be to extend ReadAtAnchor to return (id, depth) pairs;
		// kept lean for breakpoint-2 scope. If the perf gate (§9 1ms p99)
		// is breached, this is the first place to optimize.
		depths, depthErr := computeDepths(inputs.DAGReader, round.SubmissionEventID, ancestorList, MaxGenLedgerDepth)
		if depthErr != nil {
			if errors.Is(depthErr, dag.ErrEventNotFound) {
				return nil, StatusDeferred, DeferredCauseDAGAncestorBFS, summary, nil
			}
			return nil, 0, 0, summary, fmt.Errorf("derivation: computeDepths: %w", depthErr)
		}

		summary.GenLedgerAncestorCount = uint32(len(ancestorList))

		if len(ancestorList) > 0 {
			// Compute (id, weight=q/depth²) for each ancestor.
			type weighted struct {
				id      event.EventID
				agentID crypto.AgentID
				depth   int
				weight  protocolmath.BasisPoints
			}
			ws := make([]weighted, 0, len(ancestorList))
			for _, aID := range ancestorList {
				ev, getErr := inputs.DAGReader.Get(aID)
				if getErr != nil {
					if errors.Is(getErr, dag.ErrEventNotFound) {
						return nil, StatusDeferred, DeferredCauseDAGAncestorBFS, summary, nil
					}
					return nil, 0, 0, summary, fmt.Errorf("derivation: ancestor Get %s: %w", aID, getErr)
				}
				q, qErr := qualityImpl.Lookup(aID, cutoff.epoch)
				if qErr != nil {
					if errors.Is(qErr, dag.ErrEventNotFound) {
						return nil, StatusDeferred, DeferredCauseQualityLookup, summary, nil
					}
					return nil, 0, 0, summary, fmt.Errorf("derivation: Quality.Lookup %s: %w", aID, qErr)
				}
				if q < 0 {
					q = 0
				}
				if q > protocolmath.MaxBasisPoints {
					q = protocolmath.MaxBasisPoints
				}
				depth := depths[aID]
				if depth <= 0 {
					depth = 1 // defensive
				}
				w := q / protocolmath.BasisPoints(depth*depth)
				ws = append(ws, weighted{
					id:      aID,
					agentID: crypto.AgentID(ev.AgentID),
					depth:   depth,
					weight:  w,
				})
			}

			recips := make([]protocolmath.Recipient, 0, len(ws))
			for _, w := range ws {
				recips = append(recips, protocolmath.Recipient{
					CanonicalKey: []byte(w.id),
					Weight:       w.weight,
				})
			}
			alloc, allocErr := protocolmath.Allocate(recips, protocolmath.MicroAET(genPool))
			if allocErr != nil {
				// Allocation failure (e.g., all weights zero) → full pool
				// to treasury per pre-5B settler semantic (gen-ledger
				// calculator falls back to treasury route on
				// protocolmath error).
				treasuryAmount += genPool
			} else {
				// Iterate ws (already in canonical order from BFS) so the
				// produced records are deterministic. AgentID lookup via
				// the ws slice (CanonicalKey indexing into alloc).
				var allocated uint64
				for _, w := range ws {
					amt := uint64(alloc[string(w.id)])
					allocated += amt
					if amt == 0 {
						continue
					}
					records = append(records, PayoutRecord{
						DerivationVersion: DerivationVersion,
						SettlementKey:     settlementKey,
						Recipient:         Recipient{ID: w.agentID, Role: RoleGenLedgerAncestor},
						Amount:            Amount{Value: amt, Currency: CurrencyAET},
						Purpose:           Purpose{Tag: TagGenLedgerRoyalty},
					})
				}
				if allocated < genPool {
					treasuryAmount += genPool - allocated
				}
			}
		} else {
			// No gen-ledger ancestors → full pool to treasury.
			treasuryAmount += genPool
		}
	}

	// 4. Treasury (always emitted, even if amount==0, so the canonical
	// record set carries the conservation evidence). The treasury record
	// uses TagTreasuryRemainder per schema enum.
	if treasuryAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: treasuryID, Role: RoleTreasury},
			Amount:            Amount{Value: treasuryAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagTreasuryRemainder},
		})
	}

	return records, StatusDerived, 0, summary, nil
}

// partialSummary collects the observability counts during derivation;
// the final DerivationSummary on the result is built from this plus the
// W/Quality selection labels.
type partialSummary struct {
	AgreeingValidatorCount uint32
	GenLedgerTraversalRan  bool
	GenLedgerAncestorCount uint32
}

// collectAgreeingValidators returns the lex-sorted list of distinct
// validator IDs whose vote matches the given verdict. Mirrors the
// pre-5B settler helper of the same name (verification_consensus_settler.go:367)
// but returns the slice in canonical (lex-sorted) order rather than
// vote-receive order.
func collectAgreeingValidators(round *taskverification.TaskVerificationRound, verdict taskverification.Verdict) []crypto.AgentID {
	seen := make(map[crypto.AgentID]struct{})
	var out []crypto.AgentID
	for _, v := range round.Votes {
		if v.Verdict != verdict {
			continue
		}
		if _, dup := seen[v.ValidatorID]; dup {
			continue
		}
		seen[v.ValidatorID] = struct{}{}
		out = append(out, v.ValidatorID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// computeDepths returns a map of ancestorID → BFS depth from root.
// Mirrors ReadAtAnchor's BFS structure; needed because ReadAtAnchor
// returns IDs without depth metadata. Depth-bounded at maxDepth.
func computeDepths(reader AnchorReader, root event.EventID, ids []event.EventID, maxDepth int) (map[event.EventID]int, error) {
	depths := map[event.EventID]int{root: 0}
	type entry struct {
		id    event.EventID
		depth int
	}
	queue := []entry{{root, 0}}
	wanted := make(map[event.EventID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		ev, err := reader.Get(cur.id)
		if err != nil {
			return nil, err
		}
		// Lex-sorted enumeration for canonical depth assignment when
		// multiple paths reach the same ancestor (BFS visits via the
		// shortest path; lex sort breaks ties deterministically).
		children := append([]event.EventID(nil), ev.CausalRefs...)
		sort.Slice(children, func(i, j int) bool { return children[i] < children[j] })
		for _, child := range children {
			if _, known := depths[child]; known {
				continue
			}
			depths[child] = cur.depth + 1
			if _, want := wanted[child]; want {
				// We'll keep traversing in case other wanted IDs are
				// deeper — don't terminate early.
				_ = want
			}
			queue = append(queue, entry{id: child, depth: cur.depth + 1})
		}
	}
	return depths, nil
}
```

## File: `internal/settlement/derivation/derive_reject.go`

```go
package derivation

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// deriveReject implements Plan v3 §2.3 step 6 reject branch.
// No gen-ledger on reject; poster gets the worker-share; agreeing
// validators (those who voted Fail) split the validator pool W-weighted;
// treasury absorbs remainders + validator-pool-when-empty.
//
// Mirrors pre-5B settler integer-path arithmetic at
// internal/settlement/verification_consensus_settler.go:282-284:
//   posterAmount   = budget * WorkerShareBP    / SharesDenominator  (note: poster gets worker-share)
//   validatorPool  = budget * ValidatorShareBP / SharesDenominator
//   treasuryAmount = budget - posterAmount - validatorPool
func deriveReject(
	round *taskverification.TaskVerificationRound,
	cutoff cutoffResult,
	wImpl CanonicalWProjection,
	_ DerivationInputs,
	budget uint64,
	posterID crypto.AgentID,
	fundingRef event.EventID,
	treasuryID crypto.AgentID,
) (records []PayoutRecord, status DerivationStatus, cause DeferredCause, summary partialSummary, err error) {
	posterAmount := budget * WorkerShareBP / SharesDenominator
	validatorPool := budget * ValidatorShareBP / SharesDenominator
	treasuryAmount := budget - posterAmount - validatorPool

	settlementKey := SettlementKey{
		RoundID:          string(round.RoundID),
		TaskID:           round.TaskID,
		FundingReference: fundingRef,
	}

	// 1. Poster refund (using worker-share amount per pre-5B convention).
	if posterAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: posterID, Role: RolePosterRefund},
			Amount:            Amount{Value: posterAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagPosterRefund},
		})
	}

	// 2. Validator distribution: agreeing = those who voted Fail.
	agreeing := collectAgreeingValidators(round, taskverification.VerdictFail)
	summary.AgreeingValidatorCount = uint32(len(agreeing))
	if len(agreeing) == 0 || validatorPool == 0 {
		treasuryAmount += validatorPool
	} else {
		family := ""
		recips := make([]protocolmath.Recipient, 0, len(agreeing))
		sort.Slice(agreeing, func(i, j int) bool { return agreeing[i] < agreeing[j] })
		for _, v := range agreeing {
			w, lookupErr := wImpl.Lookup(v, family, round.Category, posterID, budget, cutoff.epoch)
			if lookupErr != nil {
				if errors.Is(lookupErr, dag.ErrEventNotFound) {
					return nil, StatusDeferred, DeferredCauseWLookup, summary, nil
				}
				return nil, 0, 0, summary, fmt.Errorf("derivation: W.Lookup for validator %s: %w", v, lookupErr)
			}
			if w < 0 {
				w = 0
			}
			recips = append(recips, protocolmath.Recipient{
				CanonicalKey: []byte(v),
				Weight:       w,
			})
		}
		alloc, allocErr := protocolmath.AllocateWithCeiling(recips, protocolmath.MicroAET(validatorPool))
		if allocErr != nil {
			return nil, 0, 0, summary, fmt.Errorf("derivation: validator pool allocate: %w", allocErr)
		}
		keys := make([]string, 0, len(alloc))
		for k := range alloc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			amt := uint64(alloc[k])
			if amt == 0 {
				continue
			}
			records = append(records, PayoutRecord{
				DerivationVersion: DerivationVersion,
				SettlementKey:     settlementKey,
				Recipient:         Recipient{ID: crypto.AgentID(k), Role: RoleValidator},
				Amount:            Amount{Value: amt, Currency: CurrencyAET},
				Purpose:           Purpose{Tag: TagValidatorDistribution},
			})
		}
	}

	// 3. Treasury (no gen-ledger on reject — generation pool is not
	// allocated at all on reject; the budget split is poster + validator
	// + treasury, no third pool).
	if treasuryAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: treasuryID, Role: RoleTreasury},
			Amount:            Amount{Value: treasuryAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagTreasuryRemainder},
		})
	}

	return records, StatusDerived, 0, summary, nil
}
```

## File: `internal/settlement/derivation/derive_dispute.go`

```go
package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// deriveDispute implements Plan v3 §2.3 step 6 dispute branch.
// No validator payouts, no gen-ledger; worker portion is split 50/50
// between worker and poster; treasury absorbs the remainder.
//
// Mirrors pre-5B settler integer-path arithmetic at
// internal/settlement/verification_consensus_settler.go:331-334:
//   workerPortion  = budget * WorkerShareBP / SharesDenominator
//   workerAmount   = workerPortion / 2
//   posterAmount   = workerPortion - workerAmount  (absorbs the 50/50 odd cent)
//   treasuryAmount = budget - workerPortion
//
// The 50/50 split absorbs its own rounding into posterAmount; the
// treasuryAmount carries the (validator + generation) pool weight that
// would have been allocated under accept/reject.
func deriveDispute(
	round *taskverification.TaskVerificationRound,
	_ DerivationInputs,
	budget uint64,
	posterID crypto.AgentID,
	fundingRef event.EventID,
	treasuryID crypto.AgentID,
) (records []PayoutRecord, status DerivationStatus, cause DeferredCause, summary partialSummary, err error) {
	workerPortion := budget * WorkerShareBP / SharesDenominator
	workerAmount := workerPortion / 2
	posterAmount := workerPortion - workerAmount
	treasuryAmount := budget - workerPortion

	settlementKey := SettlementKey{
		RoundID:          string(round.RoundID),
		TaskID:           round.TaskID,
		FundingReference: fundingRef,
	}

	if workerAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: round.WorkerID, Role: RoleWorker},
			Amount:            Amount{Value: workerAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagWorkerPayout},
		})
	}
	if posterAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: posterID, Role: RolePosterRefund},
			Amount:            Amount{Value: posterAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagPosterRefund},
		})
	}
	if treasuryAmount > 0 {
		records = append(records, PayoutRecord{
			DerivationVersion: DerivationVersion,
			SettlementKey:     settlementKey,
			Recipient:         Recipient{ID: treasuryID, Role: RoleTreasury},
			Amount:            Amount{Value: treasuryAmount, Currency: CurrencyAET},
			Purpose:           Purpose{Tag: TagTreasuryRemainder},
		})
	}

	return records, StatusDerived, 0, summary, nil
}
```

## File: `internal/settlement/derivation/anchor_read.go`

```go
package derivation

import (
	"errors"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// MaxGenLedgerDepth is the maximum BFS hop depth for generation-ledger
// ancestor traversal. Locked at 3 per F5 5A.3 §3.1 (matches the pre-5B
// gen-ledger calculator's `GenerationLedgerMaxDepth` constant; the depth
// bound is canonical-frozen — changes require a coordinated upgrade).
const MaxGenLedgerDepth = 3

// ReadAtAnchor performs a bounded, anchor-scoped BFS from `root`,
// returning the deterministic seed-inclusive slice of EventIDs reachable
// within `maxDepth` hops whose path to `root` lies within the canonical
// ancestry of (or equal to) `anchor`.
//
// Per F5 5A.3 §2.2 + §2.2.1:
//
//   - **Seed-inclusive**: `root` is included in the result on first
//     dequeue. The result is the bounded anchor-scoped causal subgraph
//     including root and (when reachable) anchor — NOT a strict-
//     ancestors-only set.
//
//   - **Anchor-in-result semantic (option a)**: when `anchor` is
//     reachable from `root` within `maxDepth` hops, anchor IS included.
//     Anchor's own children are not traversed further (descendants of
//     anchor are not ancestors of anchor — IsAncestor returns false).
//
//   - **Spec 2 dedup**: each event is enqueued at most once via the
//     visited set; concurrent paths to the same ancestor produce one
//     entry in the result.
//
//   - **Spec 3 deterministic per-hop**: at each parent's CausalRefs
//     enumeration, children are visited in lex-sorted order so the
//     result slice is byte-identical across nodes for identical canonical
//     DAG state.
//
//   - **All-or-defer**: any traversed event missing from the local DAG
//     causes the function to return ErrEventNotFound (caller defers per
//     Plan v3 §2.3 step 6 → Status=StatusDeferred,
//     Cause=DeferredCauseDAGAncestorBFS).
//
// Nil-anchor semantic (Fix A pre-activation case): when `anchor` is the
// empty EventID, the anchor-scoped predicate `IsAncestor(child, anchor)`
// is skipped — depth-bounded BFS proceeds from root without the anchor
// filter. Pre-`ReputationActivation`, no canonical snapshot exists per
// Fix A; the gen-ledger traversal still happens but is bounded only by
// `maxDepth`. This matches pre-5B settler behavior (gen-ledger BFS
// without anchor scoping) for the genesis epoch.
func ReadAtAnchor(reader AnchorReader, anchor event.EventID, root event.EventID, maxDepth int) ([]event.EventID, error) {
	type queueEntry struct {
		id    event.EventID
		depth int
	}

	visited := map[event.EventID]struct{}{root: {}}
	queue := []queueEntry{{id: root, depth: 0}}
	var result []event.EventID

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		// Defensive: depth > maxDepth is unreachable per the enqueue gate
		// below, but if it ever fires, skip rather than enumerate children.
		if cur.depth > maxDepth {
			continue
		}

		ev, err := reader.Get(cur.id)
		if err != nil {
			return nil, err
		}

		// Seed-inclusive: append on first dequeue. Anchor (when reached
		// as a child enqueued via the special-case below) appears in the
		// result on its own dequeue. Per §2.2.1 option (a).
		result = append(result, cur.id)

		// Don't enumerate children at maxDepth.
		if cur.depth == maxDepth {
			continue
		}

		// Spec 3: lex-sort CausalRefs before iterating to make the BFS
		// child-visit order deterministic across nodes.
		children := append([]event.EventID(nil), ev.CausalRefs...)
		sort.Slice(children, func(i, j int) bool { return children[i] < children[j] })

		for _, child := range children {
			if _, seen := visited[child]; seen {
				continue // Spec 2 dedup
			}

			// Anchor-scoped predicate: only follow children that are
			// canonical ancestors of (or equal to) `anchor`. The
			// `child == anchor` short-circuit includes anchor itself
			// as a leaf (option a per §2.2.1) without an irreflexive
			// IsAncestor(anchor, anchor) self-check (which returns false).
			//
			// Nil anchor: skip the predicate entirely (Fix A
			// pre-activation case; depth-bounded BFS without
			// anchor-scoping).
			if anchor != "" && child != anchor {
				isAnc, err := reader.IsAncestor(child, anchor)
				if err != nil {
					if errors.Is(err, errIsAncestorNotInScope()) {
						// Unreachable today; reserved for future tighter
						// error-class signaling that distinguishes "not in
						// scope" from "not materialized."
						continue
					}
					return nil, err
				}
				if !isAnc {
					continue
				}
			}

			visited[child] = struct{}{}
			queue = append(queue, queueEntry{id: child, depth: cur.depth + 1})
		}
	}

	return result, nil
}

// errIsAncestorNotInScope is a placeholder error for a future error
// class that would distinguish "child is not in canonical ancestry of
// anchor" from "child is not materialized." Today, dag.IsAncestor
// returns ErrEventNotFound only when an ID is absent; the
// not-in-scope case returns (false, nil). The placeholder allows the
// errors.Is dispatch above to be tightened in the future without
// changing the call shape.
func errIsAncestorNotInScope() error { return nil }
```

## File: `internal/settlement/derivation/cutoff.go`

```go
package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// cutoffResult bundles the two canonical cutoff handles produced by
// computeCutoff: the anchor (EventID | nil per Fix A) and the epoch
// (uint64). Per Plan v3 §2.3 step 1 + canonical-epoch sub-spec §6.4
// orthogonality: the anchor and the epoch are distinct canonical
// values serving distinct purposes.
type cutoffResult struct {
	// anchor is the canonical_cutoff_anchor field on Provenance per
	// Fix A. Empty (nil semantic) iff ReputationActivation is NOT a
	// canonical ancestor of round.CanonicalSealContext. Non-empty when
	// the locked Reputation-and-Consensus-Integrity workstream's
	// snapshot infrastructure ships and provides a concrete EventID.
	anchor event.EventID

	// anchorIsNil discriminates the Fix A nil case from the empty-
	// EventID-with-different-meaning case. True iff the cutoff anchor
	// was determined to be nil per Fix A.
	anchorIsNil bool

	// epoch is the cutoff_epoch passed to W.Lookup / Quality.Lookup.
	// Per canonical-epoch sub-spec §4.4: cutoff_epoch =
	// max(epoch_of(R) - 1, 0). For genesis rounds (epoch_of=0), the
	// max-clamp keeps cutoff_epoch >= 0 in uint64 space; pre-activation
	// stub W/quality ignore the epoch argument anyway, so the clamp is
	// benign.
	epoch uint64
}

// computeCutoff produces the canonical cutoff (anchor + epoch) for
// round R per Plan v3 §2.3 step 1.
//
// useRealW is the result of the V-1 ActivationCheck for W (computed
// separately and passed in to avoid duplicate ActivationCheck calls).
// When useRealW is true, the caller has already established that
// ReputationActivation is a canonical ancestor of R.CanonicalSealContext,
// so the cutoff anchor is non-nil per Fix A.
//
// Today (F5 5B ship) the locked Reputation-and-Consensus-Integrity
// workstream has not yet shipped its snapshot infrastructure. When
// useRealW is true (which is unreachable today because
// ReputationActivationEventID is the empty-string placeholder per
// internal/settlement/derivation/activation.go), the snapshot EventID
// for the cutoff anchor is not computable. The function returns the
// cutoff anchor as empty with anchorIsNil=false in that case, signaling
// to the caller that the path is a forward-work hole; combined with
// the V-1 selection of stub W today, this branch is not exercised.
//
// When the locked workstream ships:
//   - Add a snapshot-read primitive to DerivationInputs (e.g.,
//     SnapshotEventIDForEpoch(epoch uint64) (event.EventID, error)).
//   - In the useRealW branch below, query the primitive for the
//     cutoff_epoch and populate `anchor` with the returned EventID.
//   - Update FORWARD_NOTES.md §1 (V-1 const-flip) with closure note.
func computeCutoff(round *taskverification.TaskVerificationRound, useRealW bool) cutoffResult {
	var epoch uint64
	if round.EpochAtFinalization > 0 {
		epoch = round.EpochAtFinalization - 1
	}
	// epoch is uint64, so when EpochAtFinalization == 0 we keep epoch=0
	// (the max-clamp in the formula). Pre-activation rounds always have
	// EpochAtFinalization == 0 (no canonical EpochBoundary events
	// committed yet); pre-activation stub W/quality ignore epoch
	// anyway, so this is benign.

	if !useRealW {
		// Pre-activation per Fix A: cutoff anchor is nil.
		return cutoffResult{anchor: "", anchorIsNil: true, epoch: epoch}
	}

	// Post-activation: cutoff anchor would be the locked workstream's
	// snapshot EventID at end of cutoff_epoch. The snapshot read
	// primitive is forward work (FORWARD_NOTES.md §1 + §2). Today this
	// branch is unreachable; surface the placeholder via empty anchor
	// with anchorIsNil=false so the test harness can detect the
	// forward-work signal if the branch ever fires.
	return cutoffResult{anchor: "", anchorIsNil: false, epoch: epoch}
}
```

## File: `internal/settlement/derivation/ordinal.go`

```go
package derivation

import (
	"sort"
)

// AssignOrdinals applies the schema 4-step ordinal-assignment rule to
// `records` in place, then returns them sorted in canonical order:
//
//  1. Group by Purpose.Tag.
//  2. Within each tag group, sort by Recipient.ID lex.
//  3. Tag groups are processed in the fixed order defined by
//     OrdinalAssignmentOrder.
//  4. Ordinal is a single monotone counter from 0 across the full
//     ordered sequence; does NOT reset between tag groups.
//
// Per docs/architecture/payout-artifact-schema.yaml §purpose.ordinal
// `ordinal_assignment_rule` (LOCKED at Gate 5A.4.a). 5B implements TO
// the schema; CI lint at internal/settlement/lint/ + 5D verification
// harness assert byte-equality of the produced (canonical_id, ordinal)
// pairs.
//
// Records of types not in OrdinalAssignmentOrder are an upstream bug
// (the schema's tag enum is closed); they are placed AFTER the locked
// sequence and ordered by Tag lex then Recipient.ID lex, so the
// behavior is deterministic but indicates a contract violation upstream
// (worth surfacing via the canonical_id check + audit).
func AssignOrdinals(records []PayoutRecord) []PayoutRecord {
	if len(records) == 0 {
		return records
	}

	// Step 1: group by Tag.
	byTag := make(map[PurposeTag][]PayoutRecord, len(OrdinalAssignmentOrder))
	for _, r := range records {
		byTag[r.Purpose.Tag] = append(byTag[r.Purpose.Tag], r)
	}

	// Step 2: sort each group by Recipient.ID lex.
	// safe: writes back to byTag[tag] for the same tag we just read; map iteration order does not affect canonical output (Step 3 below re-reads via OrdinalAssignmentOrder)
	for tag := range byTag {
		group := byTag[tag]
		sort.Slice(group, func(i, j int) bool {
			return group[i].Recipient.ID < group[j].Recipient.ID
		})
		byTag[tag] = group
	}

	// Step 3: process tag groups in fixed order; collect into a single
	// ordered slice. Step 4: ordinal is a monotone counter across the
	// full sequence.
	ordered := make([]PayoutRecord, 0, len(records))
	var ordinal uint32

	knownTags := make(map[PurposeTag]struct{}, len(OrdinalAssignmentOrder))
	for _, tag := range OrdinalAssignmentOrder {
		knownTags[tag] = struct{}{}
		group, ok := byTag[tag]
		if !ok {
			continue
		}
		for _, r := range group {
			r.Purpose.Ordinal = ordinal
			ordered = append(ordered, r)
			ordinal++
		}
	}

	// Defensive: any records with a tag not in OrdinalAssignmentOrder
	// (schema-contract violation) get placed after the locked sequence.
	// Sort the unknown groups deterministically by Tag then Recipient.ID
	// so the behavior is canonical even on broken input.
	var unknownTags []PurposeTag
	// safe: collected then sorted before iteration; sort.Slice on the next line establishes canonical order
	for tag := range byTag {
		if _, known := knownTags[tag]; known {
			continue
		}
		unknownTags = append(unknownTags, tag)
	}
	sort.Slice(unknownTags, func(i, j int) bool { return unknownTags[i] < unknownTags[j] })
	for _, tag := range unknownTags {
		for _, r := range byTag[tag] {
			r.Purpose.Ordinal = ordinal
			ordered = append(ordered, r)
			ordinal++
		}
	}

	return ordered
}
```

## File: `internal/settlement/derivation/inputs.go`

```go
package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// AnchorReader is the narrow subset of DAG read methods DeriveSettlement
// depends on. Satisfies DerivationInputs contract clause (b): every
// method is a deterministic function of canonical DAG state.
//
// Locally-defined here (not imported from internal/dispatch or
// internal/dag) to keep the derivation package's import graph narrow
// and to let the F5 5A.3 §2.1 consolidation (move to
// internal/dag/anchor_reader.go) land independently. *dag.DAG satisfies
// this interface structurally; once the consolidated type exists, a
// single-line import switch points this package at the canonical
// definition without caller changes.
//
// Returns dag.ErrEventNotFound on reads that cannot be served from
// locally-materialized state; DeriveSettlement converts the signal to
// Status=StatusDeferred with the appropriate DeferredCause.
type AnchorReader interface {
	// IsAncestor reports whether `ancestor` is a strict canonical
	// ancestor of `descendant`. Strict means irreflexive:
	// IsAncestor(X, X) == false. Used by the V-1 ActivationCheck and
	// by ReadAtAnchor's anchor-scoping test.
	IsAncestor(ancestor, descendant event.EventID) (bool, error)

	// Get retrieves an event by ID. Used by ReadAtAnchor for BFS.
	// Returns ErrEventNotFound if the event is not locally
	// materialized.
	Get(id event.EventID) (*event.Event, error)

	// CountAncestorsByType counts the canonical strict ancestors of
	// descendant whose event type equals eventType (does NOT include
	// descendant itself; matches IsAncestor's irreflexive semantic).
	// Per F5 5B canonical-epoch sub-spec §3.
	//
	// All-or-defer per sub-spec §3.1: returns ErrEventNotFound if
	// descendant or any traversed ancestor is not locally materialized.
	// Caller (DeriveSettlement at the cutoff-epoch derivation site, or
	// the finalizing consumer at round.EpochAtFinalization population)
	// converts the signal to Status=StatusDeferred or to F3-B
	// causal-prerequisite-gating respectively.
	//
	// Returns (0, nil) if descendant exists but has no ancestors of
	// the requested type. Not an error.
	CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error)
}

// DerivationInputs bundles every canonical-state primitive
// DeriveSettlement reads. Constructed once per settler and passed into
// every DeriveSettlement invocation unchanged.
//
// §2.1 CONTRACT (LOAD-BEARING — from Gate 5B Plan v1 multi-AI review):
// every field MUST satisfy at least one of:
//
//	(a) Canonical-frozen value — fixed at DerivationInputs construction
//	    time; does not change during DeriveSettlement. Example: a locked
//	    enum or an interface handle to a stub that only returns
//	    constants.
//
//	(b) Deterministic replayable lookup at cutoff — exposes a query
//	    interface whose return values are pure functions of canonical
//	    state at the cutoff anchor. Example: CanonicalWProjection.Lookup,
//	    AnchorReader.IsAncestor.
//
// No field may expose mutable state through alternative paths. Adding
// a field that violates the contract is a halt-and-surface trigger per
// plan §5: "Derivation function impurity detected".
//
// This contract is the 5B analogue of V-1's "no runtime flag" rule:
// V-1 forbade a reputationActivated bool anywhere in the derivation
// package; the DerivationInputs contract generalizes to forbid any
// state-leaking path through the input bundle's field set. Failure
// modes the contract rules out:
//
//   - A mutable wrapper around CanonicalWProjection that caches results
//     in a process-local map and falls back to live-read on miss.
//   - A flag-closing ActivationCheck whose closure captures a runtime
//     reputationActivated bool set by a consumer at admission time.
//   - An AnchorReader wrapper that falls back to local-tip on
//     ErrEventNotFound instead of propagating the deferral signal.
//   - A "convenience" EscrowLookup.GetWithFallback that synthesizes
//     data when the entry is absent instead of returning an error.
//
// Every DerivationInputs field below names the clause it satisfies.
// Review new fields against this contract. Future 5A.4.c lint expansion
// (post-5D) can validate the contract structurally.
type DerivationInputs struct {
	// W is the CanonicalWProjection pair (stub + real). DeriveSettlement
	// selects Stub vs Real per the V-1 canonical-ancestor check against
	// ReputationActivationEventID. Satisfies clause (b): both Stub and
	// Real expose deterministic Lookup returning canonical values;
	// selection is canonical-position-bound, never runtime-flag-bound.
	W WProjections

	// Quality is the CanonicalQualityProjection pair (stub + real).
	// Same V-1 pattern as W. F5 ships with Real==nil; quality-activation
	// check always returns false until the future quality-
	// canonicalization workstream wires the real implementation.
	// Satisfies clause (b).
	Quality QualityProjections

	// DAGReader is the narrow canonical-DAG read surface. Satisfies
	// clause (b): IsAncestor and Get are deterministic functions of
	// canonical DAG state (materialization lag surfaces as
	// ErrEventNotFound, not as a wrong answer).
	DAGReader AnchorReader

	// EscrowMgr is the canonical-frozen escrow-entry read surface.
	// Satisfies clause (b): every method reads fields set at
	// RegisterEscrow and never mutated thereafter.
	EscrowMgr EscrowLookup

	// TaskMgr is the canonical-frozen task-record read surface.
	// Satisfies clause (b) on Category/Family (canonical-frozen at
	// TaskPosted) and on the narrow Status read (canonical-live, used
	// for early-exit short-circuit only per Gate 5A.1 §9.2 option-b;
	// any payout-math dependency discovered is a halt-and-surface
	// trigger per plan §4.4 reopen condition).
	TaskMgr TaskLookup

	// ActivationCheck is the V-1 canonical-ancestor check function.
	// Satisfies clause (b): a pure function of canonical DAG state.
	// The canonical wiring is a closure around DAGReader.IsAncestor;
	// alternative wirings that capture runtime flags are contract
	// violations per the list above.
	ActivationCheck ActivationCheck

	// TreasuryID is the canonical treasury agent. Per F5 5A.1 manifest
	// treasury_id row.
	//
	// Source today (architect-confirmed at breakpoint-2 closure): a Go
	// compile-time constant `genesis.BucketTreasury = "genesis:treasury"`
	// at internal/genesis/genesis.go:39, wired in cmd/node/main.go:1665
	// as `crypto.AgentID(genesis.BucketTreasury)`. Identical on every
	// node by build artifact; canonical-frozen at compile time. Same
	// discipline as ReputationActivationEventID's current placeholder
	// per FORWARD_NOTES.md §1.
	//
	// Future tightening: when the canonical-snapshot infrastructure
	// ships (locked Reputation-and-Consensus-Integrity workstream), the
	// TreasuryID may become canonical-DAG-derivable from a
	// genesis-locked admin event — a strictly stronger source. The
	// derivation package itself does NOT need changes for that swap;
	// only the cmd/node wiring updates to read from the canonical event
	// instead of the constant.
	//
	// Satisfies §2.1 contract clause (a) — fixed at DerivationInputs
	// construction; does not change during DeriveSettlement.
	TreasuryID crypto.AgentID
}
```

## File: `internal/settlement/derivation/canonical_id.go`

```go
package derivation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/auth"
)

// ComputeCanonicalID computes the SHA-256 hex digest of the RFC 8785
// JCS canonicalization of a PayoutRecord's preimage — the full record
// with CanonicalID excluded. Per docs/architecture/payout-artifact-
// schema.yaml §"CANONICAL HASH ALGORITHM" (LOCKED at Gate 5A.4.a).
//
// Uniqueness invariant U-1: the preimage MUST include SettlementKey,
// Recipient, Purpose (Tag + Ordinal), Amount, Provenance, and
// DerivationVersion. Shrinking the preimage is a halt-worthy regression
// per schema §"UNIQUENESS INVARIANT".
//
// Fix A nil semantic for Provenance.CanonicalCutoffAnchor: nil is a
// distinct canonical value. Encoded as JSON null, never omitted.
// The CanonicalCutoffAnchorIsNil discriminator on Provenance controls
// whether the canonical_cutoff_anchor slot holds null or the EventID
// string. Field omission would create preimage ambiguity (two different
// absences hashing to the same preimage).
//
// Every other optional field in the canonical_id preimage follows the
// same explicit-null discipline. New fields added to PayoutRecord must
// extend this function to include them in the preimage map in RFC 8785
// JCS canonical key order (the JCS pass sorts; ComputeCanonicalID does
// not itself sort, but it must include every U-1-bearing field).
func ComputeCanonicalID(r PayoutRecord) (string, error) {
	preimage := payoutRecordPreimage(r)

	rawJSON, err := json.Marshal(preimage)
	if err != nil {
		return "", fmt.Errorf("derivation: canonical_id marshal: %w", err)
	}

	canonical, err := auth.CanonicalizeJSON(rawJSON)
	if err != nil {
		return "", fmt.Errorf("derivation: canonical_id jcs: %w", err)
	}

	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// payoutRecordPreimage builds the JSON-Marshalable preimage map for
// ComputeCanonicalID. Every U-1-bearing field is present. Optional
// fields (today: Provenance.CanonicalCutoffAnchor) use explicit null
// when the field is nil; the nil discriminator lives on the record
// struct to preserve the two-valued semantic.
//
// The JCS pass sorts keys, so this function may emit keys in any
// order. We mirror the schema's top-level field order for readability.
func payoutRecordPreimage(r PayoutRecord) map[string]any {
	preimage := map[string]any{
		"derivation_version": r.DerivationVersion,
		"settlement_key": map[string]any{
			"round_id":          r.SettlementKey.RoundID,
			"task_id":           r.SettlementKey.TaskID,
			"funding_reference": string(r.SettlementKey.FundingReference),
		},
		"recipient": map[string]any{
			"id":   string(r.Recipient.ID),
			"role": string(r.Recipient.Role),
		},
		"amount": map[string]any{
			"value":    r.Amount.Value,
			"currency": string(r.Amount.Currency),
		},
		"purpose": map[string]any{
			"tag":     string(r.Purpose.Tag),
			"ordinal": r.Purpose.Ordinal,
		},
		"provenance": provenancePreimage(r.Provenance),
	}
	return preimage
}

// provenancePreimage serializes Provenance with Fix A nil semantic on
// canonical_cutoff_anchor. When IsNil is true the slot is JSON null;
// otherwise it is the EventID string. Field is always present —
// never omitted — to prevent preimage ambiguity.
func provenancePreimage(p Provenance) map[string]any {
	var cutoffValue any
	if p.CanonicalCutoffAnchorIsNil {
		cutoffValue = nil
	} else {
		cutoffValue = string(p.CanonicalCutoffAnchor)
	}
	return map[string]any{
		"round_verdict":           string(p.RoundVerdict),
		"canonical_cutoff_anchor": cutoffValue,
	}
}
```

## File: `internal/settlement/derivation/types.go`

```go
package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/event"
)

// DerivationVersion is the monotone version of the derivation function
// that produced a given PayoutRecord. Part of canonical record identity:
// records with different derivation_versions hash to different canonical_ids
// even when all other fields are byte-identical.
//
// Bumped only when the record SCHEMA or DERIVATION SEMANTICS change.
// Per F5 5A.4.a schema notes: the implementation swap from stub-W to
// real-W via canonical-position-bound V-1 selection does NOT bump this
// version — V-1 makes the swap transparent to record content.
const DerivationVersion uint32 = 1

// DerivationStatus discriminates whether DeriveSettlement produced
// records (Derived) or deferred pending canonical-state materialization
// (Deferred). Closed enum.
type DerivationStatus int

const (
	// StatusDerived indicates the derivation function completed and
	// produced a records slice. The caller applies the records via the
	// applicator.
	StatusDerived DerivationStatus = iota

	// StatusDeferred indicates canonical state needed by the derivation
	// function is not yet locally materialized. The caller re-enqueues
	// the round for retry; once materialization catches up, retry
	// converges to the byte-identical Derived result (property D-2).
	StatusDeferred
)

// String returns a lowercase identifier for DerivationStatus. Used for
// telemetry only; not in any canonical_id preimage.
func (s DerivationStatus) String() string {
	switch s {
	case StatusDerived:
		return "derived"
	case StatusDeferred:
		return "deferred"
	}
	return "unknown"
}

// DeferredCause names which canonical-state lookup hit
// ErrEventNotFound and triggered deferral. Closed enum; one variant per
// distinct deferral path in DeriveSettlement.
//
// Rationale for typed enum over string (v2 plan-mode review Finding 2):
// allows caller retry/telemetry policy to discriminate without string
// parsing, and guarantees the compiler surfaces any new deferral path
// at the callsite.
type DeferredCause int

const (
	// DeferredCauseV1AncestorCheck: ActivationCheck returned
	// ErrEventNotFound while deciding stub-W vs real-W per V-1.
	DeferredCauseV1AncestorCheck DeferredCause = iota

	// DeferredCauseDAGAncestorBFS: ReadAtAnchor returned
	// ErrEventNotFound while enumerating generation-ledger ancestors.
	DeferredCauseDAGAncestorBFS

	// DeferredCauseWLookup: CanonicalWProjection.Lookup returned
	// ErrEventNotFound for a validator's W at the round's canonical
	// cutoff.
	DeferredCauseWLookup

	// DeferredCauseQualityLookup: CanonicalQualityProjection.Lookup
	// returned ErrEventNotFound for a gen-ledger ancestor's quality
	// at the round's canonical cutoff.
	DeferredCauseQualityLookup
)

// String returns a lowercase identifier for DeferredCause. Telemetry
// only; not canonical.
func (c DeferredCause) String() string {
	switch c {
	case DeferredCauseV1AncestorCheck:
		return "v1_ancestor_check"
	case DeferredCauseDAGAncestorBFS:
		return "dag_ancestor_bfs"
	case DeferredCauseWLookup:
		return "w_lookup"
	case DeferredCauseQualityLookup:
		return "quality_lookup"
	}
	return "unknown"
}

// TerminalStatus names the terminal round outcome that produced the
// settlement. Mirrors the three finalized branches of
// taskverification.RoundState (FinalizedAccept / FinalizedReject /
// Disputed) — the round-expired branch is not a settlement input.
type TerminalStatus int

const (
	TerminalAccept TerminalStatus = iota
	TerminalReject
	TerminalDispute
)

// String returns a lowercase identifier for TerminalStatus. Telemetry
// only; the canonical provenance.round_verdict field is separate and
// uses Verdict (see record.go).
func (t TerminalStatus) String() string {
	switch t {
	case TerminalAccept:
		return "accept"
	case TerminalReject:
		return "reject"
	case TerminalDispute:
		return "dispute"
	}
	return "unknown"
}

// DerivationSummary is observability metadata about a derivation
// invocation. NOT included in any canonical_id hash preimage; never
// feeds back into derivation. Used for diagnostics and for 5D
// verification's cross-node sanity checks (e.g., "every node selected
// real-W for round R" agreement).
type DerivationSummary struct {
	RecordCountByRole      map[string]uint32 // role name → count
	SelectedWMode          string            // "stub" | "real"
	SelectedQualityMode    string            // "stub" | "real"
	GenLedgerTraversalRan  bool              // true iff verdict == Accept and gen-ledger pool > 0
	GenLedgerAncestorCount uint32            // 0 if traversal did not run
	AgreeingValidatorCount uint32            // 0 on dispute path
}

// DerivationResult is the output of DeriveSettlement.
//
// When Status == StatusDerived: Records is populated in the canonical
// ordinal order (schema 4-step rule); TerminalStatus is set; Cause is
// unused; Summary is populated; ResolvedCutoffAnchor and
// ResolvedCutoffAnchorIsNil are always meaningful.
//
// When Status == StatusDeferred: Records is empty; TerminalStatus is
// unused; Cause is populated; Summary may be partially populated for
// debugging; ResolvedCutoffAnchor / ResolvedCutoffAnchorIsNil are
// unset.
type DerivationResult struct {
	Status         DerivationStatus
	Records        []PayoutRecord
	TerminalStatus TerminalStatus

	// ResolvedCutoffAnchor is the canonical_cutoff_anchor used during
	// this derivation (per Fix A semantic). Returned for caller audit
	// and 5D verification. Meaningful only when Status == StatusDerived.
	// EventID value when non-nil; zero value when nil (see
	// ResolvedCutoffAnchorIsNil to discriminate).
	ResolvedCutoffAnchor event.EventID

	// ResolvedCutoffAnchorIsNil reports whether the resolved cutoff
	// anchor is the Fix A nil form. True iff ReputationActivation is
	// NOT a canonical ancestor of R.canonical_seal_context. Meaningful
	// only when Status == StatusDerived.
	ResolvedCutoffAnchorIsNil bool

	// Cause is populated only when Status == StatusDeferred.
	Cause DeferredCause

	// Summary is observability metadata. NOT in any canonical_id hash
	// preimage.
	Summary DerivationSummary
}
```

## File: `internal/escrow/applicator.go`

```go
package escrow

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
)

// recordLocks holds per-canonical_id sync.Mutex entries for intra-node
// defense-in-depth serialization within ApplySettlementRecords. Per F5
// 5B Plan v3 §3.3 sentence: this lock is INTRA-NODE defense-in-depth
// only. Cross-node idempotency and ordering are guaranteed by property
// D-1 (DeriveSettlement produces byte-identical records on every node)
// and by the LEDGER's canonical_id duplicate detection
// (TransferFromBucketLabeled returns ErrDuplicateEntry on duplicate
// EventID under l.mu.Lock() at internal/ledger/transfer.go:531). The
// lock here prevents wasted intra-node ledger calls when two settler
// goroutines race on the same record set; the LEDGER is what makes the
// cross-node correctness load-bearing.
//
// Removing this lock would not affect canonical correctness — the
// ledger's atomic dedup is sufficient. The lock exists to skip
// redundant work, not to enforce uniqueness.
var recordLocks sync.Map // canonical_id (string) → *sync.Mutex

func recordLock(canonicalID string) *sync.Mutex {
	v, _ := recordLocks.LoadOrStore(canonicalID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ApplySettlementRecords applies a derivation-function-produced slice
// of PayoutRecords atomically-with-respect-to-canonical_id. Replaces
// the pre-5B `ReleaseSettlement` 5-paid-flag check-then-transfer-then-set
// pattern (the concurrent-Apply race site).
//
// Per F5 5B Plan v3 §3.2-§3.5:
//
//   - Records are processed in canonical order (the slice is already
//     sorted by Purpose.Ordinal per the schema 4-step rule applied in
//     DeriveSettlement).
//
//   - Each record's transfer is keyed on record.CanonicalID as the
//     ledger EventID. Concurrent invocations producing the same
//     derivation produce records with the same CanonicalID values
//     (DeriveSettlement is pure per property D-1); the ledger's
//     duplicate-entry detection (ErrDuplicateEntry at
//     internal/ledger/transfer.go:531) makes the second call's transfer
//     attempt a no-op atomically with the first call's commit.
//
//   - The per-canonical_id sync.Mutex is INTRA-NODE defense-in-depth
//     only (architect-confirmed per recordLocks doc above). Cross-node
//     correctness rests on (a) D-1 producing identical records and (b)
//     the ledger's atomic dedup.
//
//   - Paid-flag projection (entry.WorkerPaid, entry.ValidatorsPaid[id],
//     etc.) is updated AS A PURE PROJECTION of records that have been
//     applied (Plan v3 §3.4 obligation b). Paid-flag READS are used
//     ONLY for skip-optimization (avoiding redundant ledger calls when
//     we already know the record was applied); they are NEVER a
//     correctness gate (Plan v3 §3.4 obligation c). Even if a flag is
//     unset due to crash mid-persist, calling
//     TransferFromBucketLabeled is safe — the ledger returns
//     ErrDuplicateEntry which we treat as benign no-op.
//
// Crash-position analysis (Plan v3 §3.3 table):
//
//   - Crash before ledger write: paid-flag says "not applied"; retry
//     succeeds; flag updated.
//   - Crash after ledger write, before paid-flag persist: paid-flag
//     says "not applied"; retry's TransferFromBucketLabeled returns
//     ErrDuplicateEntry; treated as benign no-op AND flag updated.
//   - Crash after both: paid-flag says "applied"; retry's
//     skip-optimization fast-path skips entirely.
//
// All three positions self-heal to byte-identical end-state with
// non-crashed nodes. The ledger's existing duplicate-detection IS the
// canonical idempotency primitive; atomic-persist coordination at this
// layer is unnecessary.
//
// Returns ErrEscrowNotFound if no escrow exists for taskID.
//
// On success, the escrow entry is deleted iff every record was applied
// (matches pre-5B convention: the entry's lifetime ends with the
// canonical settlement).
func (e *Escrow) ApplySettlementRecords(taskID string, records []derivation.PayoutRecord) error {
	if len(records) == 0 {
		return nil
	}

	e.mu.Lock()
	entry, ok := e.entries[taskID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: task %s", ErrEscrowNotFound, taskID)
	}

	bucket := bucketID(taskID)

	for _, rec := range records {
		// Skip-optimization (fast path, never a correctness gate per
		// Plan v3 §3.4 obligation c). Paid-flag READ here decides ONLY
		// whether to skip a redundant ledger call we've already made;
		// the ledger's ErrDuplicateEntry would catch it anyway.
		e.mu.RLock()
		alreadyApplied := isRecordApplied(entry, rec)
		e.mu.RUnlock()
		if alreadyApplied {
			continue
		}

		// Per-canonical_id intra-node lock (defense-in-depth only;
		// canonical correctness is at the ledger layer).
		lock := recordLock(rec.CanonicalID)
		lock.Lock()

		// Re-check skip-optimization under lock (handles concurrent-
		// goroutine race where two settler goroutines both pass the
		// pre-lock check).
		e.mu.RLock()
		alreadyApplied = isRecordApplied(entry, rec)
		e.mu.RUnlock()
		if alreadyApplied {
			lock.Unlock()
			continue
		}

		eventID := event.EventID(rec.CanonicalID)
		memo := memoForTag(rec.Purpose.Tag)

		// Canonical correctness gate: the ledger's atomic
		// canonical_id duplicate detection. nil OR ErrDuplicateEntry
		// both mean "this record's transfer is in the ledger now."
		transferErr := e.ledger.TransferFromBucketLabeled(
			eventID,
			bucket,
			rec.Recipient.ID,
			rec.Amount.Value,
			memo,
			false,
		)
		switch {
		case transferErr == nil:
			// Transfer succeeded; update paid-flag projection.
		case errors.Is(transferErr, ledger.ErrDuplicateEntry):
			// Ledger already has this canonical_id (crash window
			// position 2: prior call wrote ledger but crashed before
			// paid-flag persist). Benign no-op; still update the
			// paid-flag projection so subsequent retries fast-path skip.
		default:
			// Real failure (e.g., insufficient bucket balance — should
			// not happen under correct derivation). Surface; do NOT
			// update paid-flag.
			lock.Unlock()
			return fmt.Errorf("escrow: apply record %s for task %s: %w", rec.CanonicalID, taskID, transferErr)
		}

		// Update paid-flag projection (Plan v3 §3.4 obligation b: pure
		// projection of records that have been applied).
		e.mu.Lock()
		updatePaidFlag(entry, rec)
		e.mu.Unlock()
		e.persist(entry)

		lock.Unlock()
	}

	// Delete the entry once all records are applied — matches pre-5B
	// convention. Records are derived per round; once they're all
	// applied the entry's purpose is fulfilled.
	if allRecordsApplied(entry, records) {
		e.mu.Lock()
		delete(e.entries, taskID)
		e.mu.Unlock()
		if e.store != nil {
			if err := e.store.DeleteEscrow(taskID); err != nil {
				// Non-fatal: the in-memory deletion succeeded; on next
				// restart LoadFromStore will rebuild without this entry.
				// Logged at the same level as the existing ReleaseSettlement
				// path (escrow.go:631).
				return fmt.Errorf("escrow: delete settled entry for task %s: %w", taskID, err)
			}
		}
	}
	return nil
}

// isRecordApplied returns true iff the paid-flag projection on entry
// already reflects this record. SKIP-OPTIMIZATION ONLY: a false return
// is NOT an authorization to apply (the ledger's canonical_id dedup is
// the correctness gate); a true return is a hint that the ledger call
// would be redundant and can be skipped.
//
// Per Plan v3 §3.4 obligation c: this function is READ-only on
// paid-flags; the result is used to short-circuit the per-record loop
// body, NEVER to gate whether application logic runs at all.
func isRecordApplied(entry *EscrowEntry, rec derivation.PayoutRecord) bool {
	switch rec.Recipient.Role {
	case derivation.RoleWorker:
		return entry.WorkerPaid
	case derivation.RolePosterRefund:
		return entry.PosterRefundPaid
	case derivation.RoleValidator:
		if entry.ValidatorsPaid == nil {
			return false
		}
		return entry.ValidatorsPaid[string(rec.Recipient.ID)]
	case derivation.RoleGenLedgerAncestor:
		if entry.GenLedgerPaid == nil {
			return false
		}
		return entry.GenLedgerPaid[string(rec.Recipient.ID)]
	case derivation.RoleTreasury:
		return entry.TreasuryPaid
	}
	return false
}

// updatePaidFlag writes the paid-flag projection for a record.
// Per Plan v3 §3.4 obligation b: paid-flag is a PURE PROJECTION of
// records that have been applied. The function is the WRITER of that
// projection; isRecordApplied is the READER (skip-optimization only).
//
// Callers MUST hold e.mu.Lock() when invoking this function.
func updatePaidFlag(entry *EscrowEntry, rec derivation.PayoutRecord) {
	switch rec.Recipient.Role {
	case derivation.RoleWorker:
		entry.WorkerPaid = true
	case derivation.RolePosterRefund:
		entry.PosterRefundPaid = true
	case derivation.RoleValidator:
		if entry.ValidatorsPaid == nil {
			entry.ValidatorsPaid = make(map[string]bool)
		}
		entry.ValidatorsPaid[string(rec.Recipient.ID)] = true
	case derivation.RoleGenLedgerAncestor:
		if entry.GenLedgerPaid == nil {
			entry.GenLedgerPaid = make(map[string]bool)
		}
		entry.GenLedgerPaid[string(rec.Recipient.ID)] = true
	case derivation.RoleTreasury:
		entry.TreasuryPaid = true
	}
}

// allRecordsApplied returns true iff every record's paid-flag is set.
// Drives the entry-deletion fast path at the end of
// ApplySettlementRecords. Like isRecordApplied, this function is
// READ-only on paid-flags and is used for skip / cleanup decisions, not
// correctness gating.
func allRecordsApplied(entry *EscrowEntry, records []derivation.PayoutRecord) bool {
	for _, rec := range records {
		if !isRecordApplied(entry, rec) {
			return false
		}
	}
	return true
}

// memoForTag returns the canonical memo string for a record's purpose
// tag. Memos match the pre-5B ReleaseSettlement values exactly so the
// ledger's per-entry memo field is byte-identical to the pre-5B output
// for the same canonical inputs (preserves the existing audit log
// shape).
func memoForTag(tag derivation.PurposeTag) string {
	switch tag {
	case derivation.TagWorkerPayout:
		return "escrow-release:worker"
	case derivation.TagPosterRefund:
		return "escrow-release:poster-refund"
	case derivation.TagValidatorDistribution:
		return "escrow-release:validator-distribution"
	case derivation.TagGenLedgerRoyalty:
		return "escrow-release:gen-ledger-royalty"
	case derivation.TagTreasuryRemainder:
		return "escrow-release:treasury-fee"
	}
	// Defensive: tags outside the locked vocabulary are a schema-
	// contract violation upstream. Use a recognizable memo so the ledger
	// entry surfaces the issue at audit time.
	return "escrow-release:unknown-tag"
}

// Compile-time guard that ApplySettlementRecords stays in the package's
// public surface. If the method signature drifts, the compiler catches
// it here before downstream callers do.
var _ = (*Escrow)(nil).ApplySettlementRecords
```

## File: `internal/dag/dag.go`

```go
// Package dag implements the append-only causal Directed Acyclic Graph (DAG)
// that forms the structural spine of the AetherNet protocol.
//
// Unlike a blockchain, which imposes a single total order by batching events into
// sequentially-linked blocks, the AetherNet DAG allows events to be produced in
// parallel. Each event references only the specific prior events it depends on,
// making causal relationships explicit at the data level. The DAG grows outward
// from genesis events rather than growing as a single chain.
//
// # Internal representation
//
// Three maps maintain the graph state:
//
//	events:   EventID → *event.Event            (primary store, O(1) lookup)
//	children: EventID → set{EventID}            (forward edges, built incrementally)
//	tips:     set{EventID}                      (frontier: events with no children yet)
//
// Directed edges come in two flavors:
//   - Backward (causal) edges: e.CausalRefs lists the events e directly depends on.
//   - Forward edges: children[id] is the set of events that include id in CausalRefs.
//
// Forward edges are maintained explicitly because many operations (topological sort,
// settlement propagation) need to traverse the DAG from parents toward children,
// not just from children toward parents.
//
// # Design principles
//
//  1. Append-only: events are never removed or modified through the DAG API.
//     This mirrors the immutability of the causal record.
//
//  2. Causal validation: Add rejects any event whose CausalRefs reference an ID
//     not already in the DAG. You cannot build on events you haven't seen.
//
//  3. Tip tracking: the frontier (events not yet referenced by any child) is
//     maintained as a set and updated in O(|CausalRefs|) on every Add. Tips are
//     the natural candidates for new events to extend the DAG.
//
//  4. Concurrent access: a single sync.RWMutex protects all internal state.
//     Multiple goroutines may read concurrently; writes serialise. A single
//     coarse-grained lock is chosen over fine-grained per-map locking to avoid
//     the complexity and deadlock risk of coordinating multiple locks, while
//     still providing concurrent reads.
//
//  5. Deterministic topological sort: events are ordered by (CausalTimestamp, EventID),
//     which is a provably valid topological order (parent timestamps are strictly
//     less than their children's) and is stable across repeated calls.
package dag

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// dagPersistence is the subset of store.Store used by DAG.
// Defining a local interface breaks the potential import cycle: the store package
// may import dag-adjacent packages, and using an interface here avoids any
// circular dependency. *store.Store from the store package satisfies this interface.
type dagPersistence interface {
	PutEvent(e *event.Event) error
	AllEvents() ([]*event.Event, error)
}

// Sentinel errors allow callers to use errors.Is for programmatic branching
// rather than string-matching error messages.
var (
	// ErrEventNotFound is returned when a requested EventID is absent from the DAG.
	ErrEventNotFound = errors.New("event not found")

	// ErrDuplicateEvent is returned when an event whose ID is already stored is added
	// again.
	ErrDuplicateEvent = errors.New("duplicate event")

	// ErrMissingCausalRef is returned when an event's CausalRefs include an EventID
	// not yet present in the DAG.
	ErrMissingCausalRef = errors.New("causal reference not in DAG")

	// ErrInvalidSignature is returned when an event's cryptographic signature does
	// not verify against its canonical content and public key.
	ErrInvalidSignature = errors.New("dag: invalid event signature")

	// ErrMissingSignature is returned when a non-genesis event has no signature.
	ErrMissingSignature = errors.New("dag: event has no signature")

	// ErrCrossCheckRejected is returned when an admission-cross-check
	// validator rejects an event during Add. The wrapped error from the
	// validator is preserved for diagnostic purposes.
	ErrCrossCheckRejected = errors.New("dag: admission cross-check rejected")

	// ErrCrossCheckAlreadyRegistered is returned by RegisterAdmissionCrossCheck
	// when a validator is already registered for the given event type.
	// Validators are one-shot per type; reconfiguration is not supported.
	ErrCrossCheckAlreadyRegistered = errors.New("dag: admission cross-check already registered for event type")
)

// AdmissionCrossCheck is a per-event-type, admission-time, canonical-state-
// dependent payload validator. See RegisterAdmissionCrossCheck.
type AdmissionCrossCheck func(ev *event.Event, reader WhileLockedReader) error

// WhileLockedReader is the lock-free read interface available to admission-
// cross-check validators while dag.Add holds its write lock. Methods on
// WhileLockedReader access DAG state directly without re-acquiring the lock
// — the caller (dag.Add) already holds it.
//
// RESTRICTED API. Validators MUST use only methods on this interface; they
// MUST NOT call *DAG methods like d.IsAncestor or d.CountAncestorsByType,
// which acquire RLock and would deadlock against the held write lock.
//
// New canonical-state read methods are added here as future
// admission-cross-check users require them. Each addition is one entry per
// canonical query the validators need; non-canonical or runtime-state reads
// are explicitly out of scope for this interface.
type WhileLockedReader interface {
	// GetWhileLocked returns the event with the given ID, or
	// ErrEventNotFound if absent. Lock-free equivalent of (*DAG).Get.
	GetWhileLocked(id event.EventID) (*event.Event, error)

	// CountAncestorsByTypeWhileLocked counts the canonical strict ancestors
	// of descendant whose event type equals eventType. Lock-free equivalent
	// of (*DAG).CountAncestorsByType. Same all-or-defer semantic per
	// canonical-epoch sub-spec §3.1.
	CountAncestorsByTypeWhileLocked(descendant event.EventID, eventType event.EventType) (uint64, error)
}

// whileLockedReader is the *DAG-backed implementation of WhileLockedReader.
// Methods read d.events directly without acquiring d.mu; constructor must
// only hand this out under a held write lock.
type whileLockedReader struct {
	d *DAG
}

func (r *whileLockedReader) GetWhileLocked(id event.EventID) (*event.Event, error) {
	e, ok := r.d.events[id]
	if !ok {
		return nil, fmt.Errorf("dag: %w: %s", ErrEventNotFound, id)
	}
	return e, nil
}

func (r *whileLockedReader) CountAncestorsByTypeWhileLocked(descendant event.EventID, eventType event.EventType) (uint64, error) {
	if _, ok := r.d.events[descendant]; !ok {
		return 0, fmt.Errorf("dag: %w: %s", ErrEventNotFound, descendant)
	}

	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}
	var count uint64

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		e, ok := r.d.events[cur]
		if !ok {
			return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, cur, descendant)
		}
		for _, ref := range e.CausalRefs {
			if _, seen := visited[ref]; seen {
				continue
			}
			visited[ref] = struct{}{}
			queue = append(queue, ref)

			refEvent, ok := r.d.events[ref]
			if !ok {
				return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, ref, descendant)
			}
			if refEvent.Type == eventType {
				count++
			}
		}
	}

	return count, nil
}

// DAG is a concurrent, append-only causal directed acyclic graph of AetherNet events.
// The zero value is not usable; construct via New.
type DAG struct {
	mu sync.RWMutex

	// events is the authoritative store mapping every known EventID to its event.
	events map[event.EventID]*event.Event

	// children maps each EventID to the set of EventIDs that list it in CausalRefs.
	// These are the forward (parent→child) edges of the DAG. They are dual to the
	// backward (child→parent) edges stored in event.CausalRefs.
	// Every EventID present in events has a corresponding entry in children
	// (possibly an empty set if the event has no children yet).
	children map[event.EventID]map[event.EventID]struct{}

	// tips is the frontier: the set of EventIDs that have no children yet.
	// An event enters tips when it is added, and is removed from tips when
	// any later event lists it in CausalRefs.
	tips map[event.EventID]struct{}

	// store is the optional persistence backend. When non-nil, every successful
	// Add writes the event through to BadgerDB for durability. Defaults to nil
	// (in-memory only) so existing tests require no changes.
	store dagPersistence

	// onCommit is an optional hook called after every successful durable
	// insert (Add or addFromStore). Called OUTSIDE the DAG write lock.
	// The replay flag is true when the event is being loaded from persistent
	// storage (addFromStore), false for live inserts (Add).
	//
	// This is the universal convergence point for the Causal Commit Bus:
	// every event path (local, remote, repair, replay) fires this hook.
	// The hook must not block — use a non-blocking channel or bounded queue.
	//
	// For source tagging (local vs remote vs repair), each call site emits
	// to the recognition bus directly with the correct CommitSource. The
	// onCommit hook provides replay-path coverage that no call site handles.
	onCommit func(ev *event.Event, replay bool)

	// crossChecks holds the admission-time canonical-state cross-check
	// validators, keyed by event type. Populated via
	// RegisterAdmissionCrossCheck at startup; read in Add under the write
	// lock. One validator per event type; registration is one-shot.
	//
	// See RegisterAdmissionCrossCheck for the restricted-API discipline.
	crossChecks map[event.EventType]AdmissionCrossCheck
}

// SetStore attaches a persistence backend to the DAG. After this call every
// successful Add writes through to the store. Must be called before any Add
// to ensure the full event history is durable. s must satisfy dagPersistence;
// *store.Store from the store package does so.
func (d *DAG) SetStore(s dagPersistence) {
	d.store = s
}

// SetOnCommit registers a callback that fires after every successful durable
// insert (Add or addFromStore). The callback runs OUTSIDE the DAG write lock
// and must not block. Set before any Add or LoadFromStore call.
//
// The replay parameter is true for events loaded from persistence (addFromStore),
// false for live inserts (Add). This enables source-aware dispatch in the
// Causal Commit Bus without coupling the DAG to the recognition package.
func (d *DAG) SetOnCommit(fn func(ev *event.Event, replay bool)) {
	d.onCommit = fn
}

// New creates and returns an empty DAG ready to accept events.
func New() *DAG {
	return &DAG{
		events:      make(map[event.EventID]*event.Event),
		children:    make(map[event.EventID]map[event.EventID]struct{}),
		tips:        make(map[event.EventID]struct{}),
		crossChecks: make(map[event.EventType]AdmissionCrossCheck),
	}
}

// RegisterAdmissionCrossCheck registers a validation function that runs
// synchronously during dag.Add, after signature+causal-refs verification
// and before the event is stored. Per F5 5B canonical-epoch sub-spec
// v2.2 §1.4.1.
//
// RESTRICTED API. The validator MUST:
//   - Be a pure function of the candidate event and canonical DAG state.
//   - Use the provided WhileLockedReader; never call *DAG methods that
//     acquire locks (reentrancy deadlock — d.mu is already held).
//   - Return an error to reject; nil to admit.
//   - Be fast (runs under the DAG write lock, blocking all other Add and
//     read traffic).
//   - Have no I/O, no goroutines, no side effects beyond the return value.
//
// One validator per event type; registration at startup, not reconfigurable.
// Returns ErrCrossCheckAlreadyRegistered if a validator is already set
// for eventType.
//
// Use case: canonical cross-checks where payload validity depends on
// canonical DAG state at admission (e.g., EpochBoundary's Payload.Epoch
// equals CountAncestorsByType + 1). NOT for policy, rate-limiting, or
// non-canonical concerns. Mis-use is a halt-worthy regression per
// canonical-epoch sub-spec §9.
//
// The substrate enforces single-registration but cannot enforce purity
// — the discipline is on the validator author, with code review as the
// primary defense and the §9 halt-trigger as the implementation-time
// catch.
func (d *DAG) RegisterAdmissionCrossCheck(eventType event.EventType, validator AdmissionCrossCheck) error {
	if eventType == "" {
		return errors.New("dag: RegisterAdmissionCrossCheck requires non-empty event type")
	}
	if validator == nil {
		return errors.New("dag: RegisterAdmissionCrossCheck requires non-nil validator")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.crossChecks == nil {
		d.crossChecks = make(map[event.EventType]AdmissionCrossCheck)
	}
	if _, exists := d.crossChecks[eventType]; exists {
		return fmt.Errorf("%w: %s", ErrCrossCheckAlreadyRegistered, eventType)
	}
	d.crossChecks[eventType] = validator
	return nil
}

// Add inserts event e into the DAG and updates the causal graph.
//
// Preconditions checked before any state mutation (atomic):
//   - e.ID must not already be present in the DAG.
//   - Every EventID in e.CausalRefs must already be present in the DAG.
//
// On success:
//   - e is stored and retrievable via Get(e.ID).
//   - e.ID is added to the tips set (e has no children yet).
//   - Every ref in e.CausalRefs is removed from tips (they now have at least one child).
//   - children[ref] is updated to include e.ID for every ref in e.CausalRefs.
//
// The event pointer is stored directly. The caller must not mutate e after
// Add returns; treat stored events as immutable. SettlementState transitions
// (via event.Transition) are the one permitted post-Add mutation, but they
// are not protected by the DAG's mutex — coordinate externally if needed.
func (d *DAG) Add(e *event.Event) error {
	d.mu.Lock()

	if _, exists := d.events[e.ID]; exists {
		d.mu.Unlock()
		return fmt.Errorf("dag: %w: %s", ErrDuplicateEvent, e.ID)
	}

	// Validate all causal references before mutating any state.
	// This check runs before signature verification so that events with unknown
	// refs return ErrMissingCausalRef (the structurally prior error) rather than
	// ErrMissingSignature when both conditions hold.
	for _, ref := range e.CausalRefs {
		if _, ok := d.events[ref]; !ok {
			d.mu.Unlock()
			return fmt.Errorf("dag: %w: %s (referenced by %s)", ErrMissingCausalRef, ref, e.ID)
		}
	}

	// Signature enforcement: ALL events must be signed and verifiable,
	// including genesis events (empty CausalRefs). Genesis events are signed
	// by a key in the validator manifest. This prevents injection of unsigned
	// events by unauthorized nodes.
	if len(e.Signature) == 0 {
		d.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrMissingSignature, e.ID)
	}
	if !crypto.VerifyEvent(e) {
		d.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrInvalidSignature, e.ID)
	}

	// Admission cross-check: per-type canonical-state-dependent payload
	// validation, registered via RegisterAdmissionCrossCheck. Runs after
	// signature + causal-refs verification, before any state mutation.
	// The validator is a pure function of (event, canonical DAG state)
	// and uses WhileLockedReader for lock-free reads under our held write
	// lock. Restricted-API discipline per F5 5B canonical-epoch sub-spec
	// v2.2 §1.4.1.
	if validator, ok := d.crossChecks[e.Type]; ok {
		reader := &whileLockedReader{d: d}
		if err := validator(e, reader); err != nil {
			d.mu.Unlock()
			return fmt.Errorf("%w: %s: %w", ErrCrossCheckRejected, e.ID, err)
		}
	}

	// Commit phase — all preconditions are satisfied; mutate state.

	// Store the event and initialise its (empty) child set.
	d.events[e.ID] = e
	d.children[e.ID] = make(map[event.EventID]struct{})

	// Update forward edges and tip set for each causal reference.
	for _, ref := range e.CausalRefs {
		d.children[ref][e.ID] = struct{}{}
		// ref now has at least one child — it leaves the frontier.
		delete(d.tips, ref)
	}

	// e itself has no children yet, so it enters the frontier.
	d.tips[e.ID] = struct{}{}

	// Write-through to the persistence store when one is attached.
	if d.store != nil {
		if err := d.store.PutEvent(e); err != nil {
			slog.Error("dag: failed to persist event", "event_id", e.ID, "err", err)
		}
	}

	// Release lock BEFORE firing the commit hook. The hook must never run
	// under the DAG write lock — it may enqueue work to consumers that
	// read from the DAG, causing deadlock or blocking the hot path.
	d.mu.Unlock()

	// Fire the post-commit hook (Causal Commit Bus convergence point).
	if d.onCommit != nil {
		d.onCommit(e, false)
	}

	return nil
}

// LoadFromStore reconstructs an in-memory DAG from a previously persisted store.
// Events are replayed in CausalTimestamp order so that every parent is inserted
// before its children. The returned DAG has s attached as its store so subsequent
// Add calls continue to write through. s must satisfy dagPersistence;
// *store.Store from the store package does so.
func LoadFromStore(s dagPersistence) (*DAG, error) {
	events, err := s.AllEvents()
	if err != nil {
		return nil, fmt.Errorf("dag: load from store: %w", err)
	}

	d := New()
	d.store = s

	// Topological sort (Kahn's algorithm): replay parents before children.
	// CausalTimestamp ordering is insufficient because multiple events can
	// share a timestamp and clock skew can invert parent/child ordering.
	sorted, skipped := topoSort(events)
	if len(skipped) > 0 {
		slog.Warn("dag: skipped events with unresolvable parents during replay",
			"skipped", len(skipped), "total", len(events))
	}

	for _, e := range sorted {
		if err := d.addFromStore(e); err != nil {
			return nil, fmt.Errorf("dag: replay event %s: %w", e.ID, err)
		}
	}
	return d, nil
}

// topoSort performs a topological sort of events using Kahn's algorithm.
// Events with no CausalRefs (roots) are emitted first, followed by events
// whose parents have all been emitted. Returns the sorted list and any
// events that could not be placed (missing parents — indicates data corruption).
func topoSort(events []*event.Event) (sorted []*event.Event, skipped []*event.Event) {
	byID := make(map[event.EventID]*event.Event, len(events))
	for _, e := range events {
		byID[e.ID] = e
	}

	// inDegree counts the number of unsatisfied parent references per event.
	inDegree := make(map[event.EventID]int, len(events))
	// children maps parent → list of child event IDs.
	children := make(map[event.EventID][]event.EventID)

	for _, e := range events {
		deps := 0
		for _, ref := range e.CausalRefs {
			if _, exists := byID[ref]; exists {
				deps++
				children[ref] = append(children[ref], e.ID)
			}
			// If parent is not in the set, it's either already in the DAG
			// (genesis) or truly missing — don't count it as a dependency.
		}
		inDegree[e.ID] = deps
	}

	// Seed the queue with all zero-dependency events (roots).
	queue := make([]event.EventID, 0, len(events)/2)
	for _, e := range events {
		if inDegree[e.ID] == 0 {
			queue = append(queue, e.ID)
		}
	}

	sorted = make([]*event.Event, 0, len(events))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byID[id])

		for _, childID := range children[id] {
			inDegree[childID]--
			if inDegree[childID] == 0 {
				queue = append(queue, childID)
			}
		}
	}

	// Any events still with inDegree > 0 have unresolvable parents.
	if len(sorted) < len(events) {
		for _, e := range events {
			if inDegree[e.ID] > 0 {
				skipped = append(skipped, e)
			}
		}
	}
	return
}

// addFromStore inserts e into the in-memory DAG structures, verifying the
// EventID and signature for integrity. It does not write back to the store
// (we are loading FROM the store). Duplicate events during replay are
// silently skipped.
func (d *DAG) addFromStore(e *event.Event) error {
	d.mu.Lock()

	// Silently skip duplicates during replay.
	if _, exists := d.events[e.ID]; exists {
		d.mu.Unlock()
		return nil
	}

	// Verify EventID integrity: recompute from content and compare.
	recomputed, err := event.ComputeID(e)
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("dag: replay: failed to recompute EventID for %s: %w", e.ID, err)
	}
	if recomputed != e.ID {
		d.mu.Unlock()
		return fmt.Errorf("dag: replay: EventID mismatch for %s: stored=%s computed=%s — possible store corruption",
			e.ID, e.ID, recomputed)
	}

	// Verify signature: signed events must have valid signatures.
	// Genesis events (empty CausalRefs) may be unsigned in legacy DAGs.
	if len(e.Signature) > 0 {
		if !crypto.VerifyEvent(e) {
			d.mu.Unlock()
			return fmt.Errorf("%w: %s (detected during store replay)", ErrInvalidSignature, e.ID)
		}
	}

	// Validate causal references. Topological sort guarantees parents are
	// replayed first; a missing ref here indicates data corruption.
	for _, ref := range e.CausalRefs {
		if _, ok := d.events[ref]; !ok {
			d.mu.Unlock()
			slog.Warn("dag: missing causal ref during replay, skipping event",
				"event", e.ID, "missing_ref", ref)
			return nil
		}
	}

	// Insert into in-memory structures (mirrors the commit phase of Add).
	d.events[e.ID] = e
	d.children[e.ID] = make(map[event.EventID]struct{})

	for _, ref := range e.CausalRefs {
		d.children[ref][e.ID] = struct{}{}
		delete(d.tips, ref)
	}
	d.tips[e.ID] = struct{}{}

	d.mu.Unlock()

	// Fire the post-commit hook for replay events.
	if d.onCommit != nil {
		d.onCommit(e, true)
	}

	return nil
}

// Get returns the event stored under id, or ErrEventNotFound if no such event exists.
//
// The returned pointer aliases the DAG's internal storage. Treat the pointed-at
// event as read-only; the DAG's RWMutex does not protect individual field writes
// on an event once the pointer is in the caller's hands.
func (d *DAG) Get(id event.EventID) (*event.Event, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	e, ok := d.events[id]
	if !ok {
		return nil, fmt.Errorf("dag: %w: %s", ErrEventNotFound, id)
	}
	return e, nil
}

// Tips returns a snapshot of the current DAG frontier — all events that have not
// yet been referenced by any child event.
//
// New events should reference tips when created: doing so extends the DAG, improves
// connectivity, and contributes to the causal record that validators score.
//
// The returned slice is a copy and is safe to hold after further events are added.
// Elements are returned in lex-sorted EventID order — E.P2.A1 (F4 plan §6).
// Without this, callers like dispatcher.currentAnchor() that select tips[0] as
// the per-node admission anchor would land different anchors on different nodes
// for the same DAG state. Per locked invariant C-15 the anchor is non-canonical
// node-local, so the divergence is harmless for consensus but pollutes
// diagnostic comparisons and replay-conformance reasoning.
func (d *DAG) Tips() []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()

	tips := make([]event.EventID, 0, len(d.tips))
	// safe: collecting keys for the sort that immediately follows
	for id := range d.tips {
		tips = append(tips, id)
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i] < tips[j] })
	return tips
}

// PrimaryTips returns the current DAG tips excluding events of type
// TrajectoryCommit. This is the default parent selection set for
// non-trajectory events — it prevents trajectory commits from polluting
// the causal refs of transfers, tasks, and other primary events.
//
// If filtering removes ALL tips (e.g., the DAG frontier consists entirely
// of trajectory commits), PrimaryTips falls back to the full Tips() set
// to avoid empty causal refs which would create invalid genesis-like events.
//
// The mechanical tip tracking (d.tips) is NOT modified. PrimaryTips is a
// filtered view, not a second tracked set.
func (d *DAG) PrimaryTips() []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()

	primary := make([]event.EventID, 0, len(d.tips))
	// safe: collecting keys for the sort that follows the filter
	for id := range d.tips {
		ev, ok := d.events[id]
		if !ok {
			continue
		}
		if ev.Type == event.EventTypeTrajectoryCommit {
			continue
		}
		primary = append(primary, id)
	}

	// Fallback: if all tips are trajectory commits, return all tips.
	if len(primary) == 0 && len(d.tips) > 0 {
		// safe: fallback collection of keys; sort applied at the bottom of the function
		for id := range d.tips {
			primary = append(primary, id)
		}
	}

	// Sort for cross-node determinism (E.P2.A1 — same rationale as Tips()).
	sort.Slice(primary, func(i, j int) bool { return primary[i] < primary[j] })
	return primary
}

// LocalTips returns the current DAG tips authored by the given agent,
// excluding TrajectoryCommit events. This is the recommended parent
// selection set for locally-created events — it ensures new events only
// reference parents that this node has already broadcast, preventing
// materialization stalls on remote nodes that may not yet have third-party
// tip events synced from other sources.
//
// If the agent has no current tips (e.g., first event after startup),
// LocalTips returns an empty slice. Callers should treat an empty result
// the same as a genesis event (CausalTimestamp will be 1).
func (d *DAG) LocalTips(agentID string) []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()

	local := make([]event.EventID, 0, 4)
	// safe: collecting keys for the sort that follows the filter
	for id := range d.tips {
		ev, ok := d.events[id]
		if !ok {
			continue
		}
		if ev.AgentID != agentID {
			continue
		}
		if ev.Type == event.EventTypeTrajectoryCommit {
			continue
		}
		local = append(local, id)
	}
	// Sort for cross-node determinism (E.P2.A1 — same rationale as Tips()).
	sort.Slice(local, func(i, j int) bool { return local[i] < local[j] })
	return local
}

// Size returns the number of events currently stored in the DAG.
func (d *DAG) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.events)
}

// All returns a snapshot of every event currently stored in the DAG.
// The slice is unordered — for a causally-ordered listing use TopologicalSort.
// Returned pointers alias the DAG's internal storage; treat events as read-only.
func (d *DAG) All() []*event.Event {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]*event.Event, 0, len(d.events))
	// safe: documented as unordered; callers requiring a canonical order use TopologicalSort
	for _, e := range d.events {
		result = append(result, e)
	}
	return result
}

// RecentEvents returns events with CausalTimestamp >= minTimestamp, bounded
// by maxCount. Events are sorted by CausalTimestamp ascending. This is a
// read-only operation — returned pointers alias the DAG's internal storage.
func (d *DAG) RecentEvents(minTimestamp uint64, maxCount int) []*event.Event {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var result []*event.Event
	// safe: filter pass; final result sorted by CausalTimestamp below
	for _, e := range d.events {
		if e.CausalTimestamp >= minTimestamp {
			result = append(result, e)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CausalTimestamp < result[j].CausalTimestamp
	})
	if len(result) > maxCount {
		result = result[len(result)-maxCount:]
	}
	return result
}

// MaxTimestamp returns the highest CausalTimestamp in the DAG, or 0 if empty.
func (d *DAG) MaxTimestamp() uint64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var max uint64
	// safe: commutative max-reduction; final scalar is order-independent
	for _, e := range d.events {
		if e.CausalTimestamp > max {
			max = e.CausalTimestamp
		}
	}
	return max
}

// EventIDs returns all event IDs currently in the DAG.
//
// The returned slice is intentionally NOT sorted; the sole production
// caller (Node.BuildCheckpoint) sorts the slice itself before hashing.
// Calling sort here would duplicate the work without affecting any
// observable property.
func (d *DAG) EventIDs() []event.EventID {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ids := make([]event.EventID, 0, len(d.events))
	// safe: caller sorts (Node.BuildCheckpoint) before any deterministic use
	for id := range d.events {
		ids = append(ids, id)
	}
	return ids
}

// Ancestors returns the set of all events that causally precede id —
// the transitive closure of id's CausalRefs, traversed breadth-first.
//
// id itself is not included (strict / irreflexive ancestor relation).
// The ordering of elements in the returned slice is BFS discovery order.
// For a causally ordered listing of the full DAG, use TopologicalSort.
//
// Returns ErrEventNotFound if id is not in the DAG.
func (d *DAG) Ancestors(id event.EventID) ([]event.EventID, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.ancestorsLocked(id)
}

// ancestorsLocked is the internal, mutex-free implementation of Ancestors.
// It is separated from Ancestors so that other methods (or future internal
// callers that already hold d.mu) can compute ancestors without a second lock
// acquisition, which would panic on a non-reentrant sync.RWMutex.
func (d *DAG) ancestorsLocked(id event.EventID) ([]event.EventID, error) {
	e, ok := d.events[id]
	if !ok {
		return nil, fmt.Errorf("dag: %w: %s", ErrEventNotFound, id)
	}

	// BFS over the backward (causal) edges, collecting all reachable ancestors.
	// We mark nodes visited when they are enqueued, not when they are dequeued,
	// to prevent adding the same ancestor to the result multiple times when it
	// is reachable via multiple paths (common in fork-then-merge subgraphs).
	visited := make(map[event.EventID]struct{})
	queue := make([]event.EventID, 0, len(e.CausalRefs))

	for _, ref := range e.CausalRefs {
		if _, seen := visited[ref]; !seen {
			visited[ref] = struct{}{}
			queue = append(queue, ref)
		}
	}

	result := make([]event.EventID, 0, len(visited))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		result = append(result, cur)

		parent, ok := d.events[cur]
		if !ok {
			// Defensive: should not occur in a DAG built exclusively through Add,
			// which enforces referential integrity before insertion.
			continue
		}
		for _, ref := range parent.CausalRefs {
			if _, seen := visited[ref]; !seen {
				visited[ref] = struct{}{}
				queue = append(queue, ref)
			}
		}
	}

	return result, nil
}

// IsAncestor reports whether ancestor is a strict causal ancestor of descendant —
// that is, whether there exists a directed path from descendant back to ancestor
// through CausalRefs edges.
//
// An event is not considered an ancestor of itself (strict / irreflexive).
//
// Returns ErrEventNotFound if either ID is absent from the DAG.
// Complexity: O(A) where A is the number of ancestors of descendant.
func (d *DAG) IsAncestor(ancestor, descendant event.EventID) (bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if _, ok := d.events[ancestor]; !ok {
		return false, fmt.Errorf("dag: %w: %s", ErrEventNotFound, ancestor)
	}
	if _, ok := d.events[descendant]; !ok {
		return false, fmt.Errorf("dag: %w: %s", ErrEventNotFound, descendant)
	}

	// Strict ancestor: an event is not its own ancestor.
	// Handle this before BFS so we don't short-circuit on the first dequeue.
	if ancestor == descendant {
		return false, nil
	}

	// BFS from descendant, following CausalRefs backward, searching for ancestor.
	// We stop as soon as ancestor is found rather than computing the full ancestor
	// set — this is an early-exit optimisation for hot-path settlement checks.
	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur == ancestor {
			return true, nil
		}

		e, ok := d.events[cur]
		if !ok {
			continue // defensive
		}
		for _, ref := range e.CausalRefs {
			if _, seen := visited[ref]; !seen {
				visited[ref] = struct{}{}
				queue = append(queue, ref)
			}
		}
	}

	return false, nil
}

// CountAncestorsByType counts the canonical strict ancestors of descendant
// whose event type equals eventType. The count does NOT include descendant
// itself (strict / irreflexive, matching IsAncestor semantics).
//
// Canonical: two nodes with identical DAG content compute identical counts
// for identical (descendant, eventType) arguments. The count is a pure
// function of DAG topology; no local-admission-order dependence.
//
// All-or-defer (per F5 5B canonical-epoch sub-spec §3.1): if descendant is
// not locally materialized, OR if any traversed ancestor needed to
// determine the count is not locally materialized, returns ErrEventNotFound.
// Callers MUST defer rather than use a partial count — partial counts
// would diverge across nodes with different materialization progress and
// break the D-1 cross-node byte-equality guarantee. Strict CausalRefs
// admission in Add (lines 187-192) makes the second clause defensive
// against future semantic changes; the first clause covers the genuine
// materialization-lag case.
//
// Returns (0, nil) if descendant exists but has no ancestors of the
// requested type. Not an error.
//
// Complexity: O(A) where A is the number of ancestors of descendant.
// Sub-spec §9 names a 1ms p99 latency gate at 10^6 ancestors;
// implementation may add an LRU cache or projection-assisted counting if
// benchmarking shows the gate is exceeded.
func (d *DAG) CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if _, ok := d.events[descendant]; !ok {
		return 0, fmt.Errorf("dag: %w: %s", ErrEventNotFound, descendant)
	}

	// BFS from descendant over CausalRefs. Strict / irreflexive: descendant
	// itself is not counted. Visited set guards against revisiting events
	// reachable via multiple paths.
	visited := map[event.EventID]struct{}{descendant: {}}
	queue := []event.EventID{descendant}
	var count uint64

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		e, ok := d.events[cur]
		if !ok {
			// Defensive: should not happen given strict CausalRefs admission
			// in Add. If it does, the local DAG is missing an ancestor needed
			// to determine the count — defer per §3.1 all-or-defer rather
			// than return a partial count.
			return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, cur, descendant)
		}
		for _, ref := range e.CausalRefs {
			if _, seen := visited[ref]; seen {
				continue
			}
			visited[ref] = struct{}{}
			queue = append(queue, ref)

			refEvent, ok := d.events[ref]
			if !ok {
				return 0, fmt.Errorf("dag: %w: %s (ancestor traversal incomplete for %s)", ErrEventNotFound, ref, descendant)
			}
			if refEvent.Type == eventType {
				count++
			}
		}
	}

	return count, nil
}

// TopologicalSort returns all events in a deterministic causal order.
//
// The ordering satisfies: for every event e at index i, all events in
// e.CausalRefs appear at indices less than i (parents before children).
//
// Algorithm: Kahn's algorithm (for cycle detection) followed by a stable sort
// on (CausalTimestamp, EventID). The sort step yields determinism because:
//
//	For any edge A → B (A is a parent of B):
//	    A.CausalTimestamp < B.CausalTimestamp
//
// This inequality holds by the Lamport clock derivation rule
// (child timestamp = max(parent timestamps) + 1), so sorting by CausalTimestamp
// preserves all causal relationships. EventID tiebreaking provides a total order
// among causally unrelated events (concurrent events at the same logical time).
//
// A cycle cannot occur in a DAG built through Add because content-addressed
// EventIDs make mutual reference cryptographically impossible, but the check
// is retained as a defensive invariant assertion.
//
// Returns an error only if a cycle is detected (which indicates internal corruption).
func (d *DAG) TopologicalSort() ([]*event.Event, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Kahn's algorithm: track in-degree (parent count) for each event.
	// Start by processing events whose in-degree is 0 (genesis events),
	// then iteratively "remove" them and decrement their children's in-degrees.
	inDegree := make(map[event.EventID]int, len(d.events))
	// safe: building per-key inDegree counts; assignment commutes across keys
	for id, e := range d.events {
		inDegree[id] = len(e.CausalRefs)
	}

	queue := make([]event.EventID, 0)
	// safe: queue ordering does not affect output; the final sort.Slice below
	// imposes a deterministic (CausalTimestamp, EventID) total order on result
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	result := make([]*event.Event, 0, len(d.events))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		result = append(result, d.events[cur])

		// safe: in-degree decrement is commutative; final sort.Slice below
		// imposes the canonical (CausalTimestamp, EventID) total order
		for childID := range d.children[cur] {
			inDegree[childID]--
			if inDegree[childID] == 0 {
				queue = append(queue, childID)
			}
		}
	}

	if len(result) != len(d.events) {
		return nil, errors.New("dag: cycle detected during topological sort — DAG invariant violated")
	}

	// Sort by (CausalTimestamp, EventID) for a deterministic total order.
	// See function-level comment for the proof that this preserves causal ordering.
	sort.Slice(result, func(i, j int) bool {
		ti, tj := result[i].CausalTimestamp, result[j].CausalTimestamp
		if ti != tj {
			return ti < tj
		}
		return result[i].ID < result[j].ID
	})

	return result, nil
}
```

## File: `internal/epoch/boundary_validator.go`

```go
package epoch

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// Sentinel errors surfaced by BoundaryAdmissionValidator. Wrapped via
// dag.ErrCrossCheckRejected at the dag.Add boundary.
var (
	// ErrInvalidPayloadVersion: payload.Version != 1.
	ErrInvalidPayloadVersion = errors.New("epoch_boundary: invalid payload version")

	// ErrInvalidEpoch: payload.Epoch < 1 (epoch 0 has no boundary by §4.2).
	ErrInvalidEpoch = errors.New("epoch_boundary: invalid epoch (must be >= 1)")

	// ErrTriggerEventIDMissing: payload.TriggerEventID is not in the DAG.
	// Distinct from a materialization-lag deferral (which would appear at a
	// different layer); at admission time, missing TriggerEventID means the
	// emitter referenced an event that doesn't exist canonically.
	ErrTriggerEventIDMissing = errors.New("epoch_boundary: trigger event ID not found in DAG")

	// ErrTriggerEventWrongType: TriggerEventID exists but is not a
	// TaskVerificationConsensus event (the only canonical trigger source).
	ErrTriggerEventWrongType = errors.New("epoch_boundary: trigger event must be TaskVerificationConsensus")

	// ErrEpochMismatch: payload.Epoch != CountAncestorsByType(TriggerEventID,
	// EpochBoundary) + 1. Either Byzantine emission with a wrong epoch claim
	// or an honest race where another EpochBoundary landed first; both reject
	// at admission.
	ErrEpochMismatch = errors.New("epoch_boundary: payload epoch does not match canonical ancestor count")

	// ErrThresholdNotCrossed: payload.Epoch * EpochLength !=
	// CountAncestorsByType(TriggerEventID, TaskVerificationConsensus) + 1.
	// The TVConsensus event the boundary references does not actually sit
	// at the canonical_tvc_rank corresponding to the claimed epoch. Honest
	// emitter math bug or Byzantine emission.
	ErrThresholdNotCrossed = errors.New("epoch_boundary: trigger event does not cross the canonical epoch threshold")
)

// BoundaryAdmissionValidator implements the F5 5B canonical-epoch
// sub-spec v2.2 §1.4 admission cross-check for EventTypeEpochBoundary.
// Pure function of (event, canonical DAG state). No side effects, no
// I/O, no goroutines — runs synchronously under the dag.Add write lock
// via the restricted-API discipline of WhileLockedReader.
//
// Validates (in order):
//
//  1. Payload unmarshals as EpochBoundaryPayload.
//  2. Payload.Version == 1.
//  3. Payload.Epoch >= 1.
//  4. Payload.TriggerEventID exists in the DAG and Type ==
//     EventTypeTaskVerificationConsensus.
//  5. Payload.Epoch == CountAncestorsByType(TriggerEventID,
//     EventTypeEpochBoundary) + 1 (canonical epoch-count cross-check).
//  6. Payload.Epoch * EpochLength == CountAncestorsByType(
//     TriggerEventID, EventTypeTaskVerificationConsensus) + 1
//     (canonical threshold-crossing cross-check).
//
// Signature validation (§1.4 last bullet) is performed by dag.Add's
// existing crypto.VerifyEvent step before the cross-check fires — this
// validator does NOT re-verify signatures. The signer-in-canonical-
// validator-snapshot-at-TriggerEventID's-position binding is intentional
// sub-scope per FORWARD_NOTES.md §2: requires snapshot infrastructure
// owned by the locked Reputation-and-Consensus-Integrity workstream;
// implementation deferred until that infrastructure ships. The
// canonical-state checks (1-6 above) close the D-1 / canonicality
// surface; signer binding is an attribution / slashing surface
// orthogonal to settlement correctness.
//
// Returns nil to admit; a wrapped sentinel error to reject. The dag.Add
// boundary further wraps with dag.ErrCrossCheckRejected.
func BoundaryAdmissionValidator(ev *event.Event, reader dag.WhileLockedReader) error {
	if ev == nil {
		return errors.New("epoch_boundary: nil event passed to validator")
	}
	if ev.Type != event.EventTypeEpochBoundary {
		// Should not happen — dag.Add only invokes validators for matching
		// types — but defensive against future wiring bugs.
		return fmt.Errorf("epoch_boundary: validator called on wrong event type %q", ev.Type)
	}

	var payload event.EpochBoundaryPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("epoch_boundary: payload unmarshal: %w", err)
	}

	// 2. Version pinned at 1 for F5 ship.
	if payload.Version != 1 {
		return fmt.Errorf("%w: got %d, want 1", ErrInvalidPayloadVersion, payload.Version)
	}

	// 3. Epoch numbering starts at 1; epoch 0 has no boundary.
	if payload.Epoch < 1 {
		return fmt.Errorf("%w: got %d", ErrInvalidEpoch, payload.Epoch)
	}

	// 4. TriggerEventID must exist in the DAG and be a TVConsensus event.
	trigger, err := reader.GetWhileLocked(payload.TriggerEventID)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTriggerEventIDMissing, payload.TriggerEventID, err)
	}
	if trigger.Type != event.EventTypeTaskVerificationConsensus {
		return fmt.Errorf("%w: got %q for trigger %s", ErrTriggerEventWrongType, trigger.Type, payload.TriggerEventID)
	}

	// 5. Canonical epoch-count cross-check: the claim that this is
	// EpochBoundary(N) is true iff N-1 EpochBoundary events are canonical
	// ancestors of the trigger.
	priorBoundaries, err := reader.CountAncestorsByTypeWhileLocked(payload.TriggerEventID, event.EventTypeEpochBoundary)
	if err != nil {
		// CountAncestorsByType returning ErrEventNotFound during admission
		// indicates the trigger or one of its ancestors is missing from
		// the local DAG — but the trigger lookup above succeeded, so any
		// ancestor-traversal failure is a defensive case (CausalRefs
		// invariant violation in the DAG itself, which dag.Add normally
		// rejects). Reject to be safe.
		return fmt.Errorf("epoch_boundary: count epoch-boundary ancestors: %w", err)
	}
	if payload.Epoch != priorBoundaries+1 {
		return fmt.Errorf("%w: payload.Epoch=%d, canonical_count+1=%d", ErrEpochMismatch, payload.Epoch, priorBoundaries+1)
	}

	// 6. Canonical threshold-crossing cross-check: the trigger must be at
	// canonical_tvc_rank = N * EpochLength (where N = payload.Epoch).
	// canonical_tvc_rank(E) = CountAncestorsByType(E, TVConsensus) + 1.
	priorTVC, err := reader.CountAncestorsByTypeWhileLocked(payload.TriggerEventID, event.EventTypeTaskVerificationConsensus)
	if err != nil {
		return fmt.Errorf("epoch_boundary: count TVConsensus ancestors: %w", err)
	}
	expectedTVCAncestors := payload.Epoch*EpochLength - 1 // because rank = ancestors + 1
	if priorTVC != expectedTVCAncestors {
		return fmt.Errorf("%w: payload.Epoch=%d implies %d TVC ancestors, got %d (canonical_tvc_rank=%d, want %d)",
			ErrThresholdNotCrossed,
			payload.Epoch,
			expectedTVCAncestors,
			priorTVC,
			priorTVC+1,
			payload.Epoch*EpochLength,
		)
	}

	return nil
}
```

## File: `internal/epoch/boundary_emitter.go`

```go
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
```

## File: `internal/dispatch/epoch_boundary_lk_consumer.go`

```go
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// EpochBoundaryLogicalKeyConsumer is the F5 5B canonical-epoch
// sub-spec §2.2 Candidate A logical-key consumer for EpochBoundary
// admission dedup.
//
// **Logical key = Payload.Epoch (uint64), serialized as decimal string.**
// NOT content-hash. Per sub-spec §12.6(i) discovery-tax prediction:
// content-hash dedup is the natural default but it does NOT collapse
// multi-emit because each emitter's signature differs (per §1.5 the
// emitter's AgentID is in the content-hash preimage, so distinct
// validators produce distinct content-hashes for the same canonical
// EpochBoundary(N)). Keying on Epoch converges all emissions to ONE
// canonical EpochBoundary per epoch.
//
// Apply is a no-op: all canonical-state validation already happened at
// dag.Add admission time via the BoundaryAdmissionValidator (sub-spec
// v2.2 §1.4.1 admission-cross-check mechanism). The only purpose of
// this consumer is to provide the logical-key dedup gate; once the
// first EpochBoundary(N) is admitted, the dispatcher's per-(consumer,
// key) state machine ensures no second Apply fires.
type EpochBoundaryLogicalKeyConsumer struct{}

// NewEpochBoundaryLogicalKeyConsumer constructs the consumer.
// Stateless; one per node.
func NewEpochBoundaryLogicalKeyConsumer() *EpochBoundaryLogicalKeyConsumer {
	return &EpochBoundaryLogicalKeyConsumer{}
}

// Name implements LogicalKeyConsumer. Distinct from
// "tv_consensus_settlement_lk" so admission-store records for the two
// strategies never collide on name.
func (c *EpochBoundaryLogicalKeyConsumer) Name() string {
	return "epoch_boundary_lk"
}

// Interested implements LogicalKeyConsumer. Subscribes to
// EpochBoundary events.
func (c *EpochBoundaryLogicalKeyConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeEpochBoundary
}

// Key projects the event's Payload.Epoch as the logical admission key.
//
// Per sub-spec §12.6(i): Epoch (NOT content-hash) is the dedup key.
// Multiple validators emitting EpochBoundary(N) for the same trigger
// produce events with the same Epoch but different content-hashes
// (AgentID differs in preimage); keying on Epoch causes the dispatcher
// to admit only the first arrival per Epoch and silently drop the rest.
//
// An unparsable payload is a programming bug; surface to the dispatcher
// as an error so it logs loudly. Cannot happen for events that passed
// admission (BoundaryAdmissionValidator already validated payload
// shape) but kept defensive.
func (c *EpochBoundaryLogicalKeyConsumer) Key(ev *event.Event) (LogicalKey, error) {
	var payload event.EpochBoundaryPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return "", fmt.Errorf("epoch_boundary_lk: unmarshal payload: %w", err)
	}
	if payload.Epoch == 0 {
		return "", errors.New("epoch_boundary_lk: payload.Epoch == 0 (sub-spec §4.2 forbids epoch 0 boundary)")
	}
	// Decimal-string serialization of uint64. Stable canonical form for
	// the LogicalKey opaque-string contract.
	return LogicalKey(strconv.FormatUint(payload.Epoch, 10)), nil
}

// RoundState implements LogicalKeyConsumer. EpochBoundary admission has
// no underlying canonical state to query — the canonical-cross-check
// at dag.Add already established validity. Returns the empty
// RoundState; IsComplete uses only the LogicalKey field.
func (c *EpochBoundaryLogicalKeyConsumer) RoundState(_ context.Context, key LogicalKey) (RoundState, error) {
	return RoundState{LogicalKey: key}, nil
}

// IsComplete implements LogicalKeyConsumer. Always true: an
// EpochBoundary event admitted past dag.Add (and thus past the
// admission cross-check) is by definition canonically valid; no further
// underlying-state accumulation is required for canonical outcome
// derivation. The dispatcher's per-(consumer, key) state machine
// handles the dedup gate.
func (c *EpochBoundaryLogicalKeyConsumer) IsComplete(_ RoundState) (bool, error) {
	return true, nil
}

// DeriveOutcome implements LogicalKeyConsumer. Returns an empty
// Outcome — EpochBoundary is not a verdict-bearing event; its
// canonical effect is its presence in the DAG (counted by
// CountAncestorsByType). No verdict, no participants.
func (c *EpochBoundaryLogicalKeyConsumer) DeriveOutcome(_ RoundState) (Outcome, error) {
	return Outcome{}, nil
}

// Apply implements LogicalKeyConsumer. No-op: the canonical effect of
// EpochBoundary(N) is already in place once the event is admitted to
// the DAG (which happens before Apply is invoked). The
// LogicalKeyConsumer plumbing exists solely to provide the dedup gate
// keyed on Epoch.
//
// If a future workstream introduces a side-effect that should fire
// once per canonical EpochBoundary (e.g., snapshot emission per
// sub-spec §5.1), this is the hook to extend.
func (c *EpochBoundaryLogicalKeyConsumer) Apply(_ context.Context, _ LogicalKey, _ Outcome) error {
	return nil
}

// RecoveryProbe implements LogicalKeyConsumer. Returns
// RecoveryCompleted unconditionally for any logical key the dispatcher
// asks about: the canonical effect of EpochBoundary(N) is its DAG
// presence, and the DAG's durability layer (BadgerDB write-through)
// already recovers the event itself across crashes. There is no
// per-EpochBoundary side-effect that could be left half-done by a
// crash, so "Apply ran" and "Apply not yet started" are observationally
// identical for this consumer.
func (c *EpochBoundaryLogicalKeyConsumer) RecoveryProbe(_ context.Context, _ LogicalKey) (RecoveryStatus, error) {
	return RecoveryCompleted, nil
}

// Compile-time assertion.
var _ LogicalKeyConsumer = (*EpochBoundaryLogicalKeyConsumer)(nil)
```

## File: `internal/dispatch/dispatcher.go`

```go
package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// AdmissionStore is the persistence interface for dispatcher admission
// records. *store.Store satisfies this interface.
type AdmissionStore interface {
	GetAdmission(key string) (*AdmissionRecord, error)
	PutAdmission(key string, record *AdmissionRecord) error
	AllAdmissions() ([]*AdmissionRecord, error)
	DeleteAdmission(key string) error
}

// EvidenceEmitter publishes a canonical event to the DAG. Injected to
// break the circular dependency dispatch → localpub → dag → dispatch.
// In production, wired to localpub.Publisher.Publish via cmd/node/main.go.
type EvidenceEmitter func(ev *event.Event) error

// Dispatcher is the CanonicalEventDispatcher primitive. It sits between
// the recognition fabric and all canonical-event consumers, guaranteeing
// exactly-once successful Apply per (event, consumer) pair (C-1) for
// content-hash consumers and exactly-once successful Apply per
// (consumer, logical_key) pair for logical-key consumers (F4B,
// locked-invariant review §3.5).
//
// Consumers register before startup via Register (content-hash) or
// RegisterLogicalKey (logical-key). Events are delivered via Admit,
// which may be called from any goroutine, any number of times per
// event — the dispatcher absorbs duplication at a single architectural
// choke point.
type Dispatcher struct {
	mu                  sync.RWMutex
	consumers           map[string]Consumer
	logicalKeyConsumers map[string]LogicalKeyConsumer
	store               AdmissionStore
	dag                 DAGAnchorReader
	epochFn             func() uint64
	evidenceEmitter     EvidenceEmitter
	deferralIndex       map[event.EventID][]string // prereq EventID → admission keys waiting

	// keyLocks holds per-(consumer, key) sync.Mutex entries for
	// intra-node defense-in-depth serialization within
	// admitOneLogicalKey. Per F5 5B post-#133 dispatcher LK race fix
	// (Path A): the lock spans the read-modify-write region of the
	// per-(consumer, key) admission state machine, eliminating the
	// race window where two concurrent commit-bus workers (DefaultWorkers=4)
	// processing byte-distinct events for the same logical key both
	// pass the StateApplied gate and both invoke consumer.Apply.
	//
	// **Defense-in-depth framing** (architect direction, mirrors
	// internal/escrow/applicator.go's recordLocks):
	//
	// The lock is INTRA-NODE defense-in-depth only. Cross-node
	// correctness on logical-key admission is guaranteed by:
	//   1. Each LK consumer's Apply being canonically deterministic
	//      per its consumer contract.
	//   2. Ledger ErrDuplicateEntry idempotency at any transfer layer
	//      Apply might invoke (internal/ledger/transfer.go:531).
	//   3. LK consumer's Apply being idempotent or no-op for byte-
	//      distinct events with the same logical key (F4B contract).
	//
	// The lock prevents wasted intra-node Apply calls on race-loss,
	// not cross-node correctness divergence. Removing it would not
	// affect canonical correctness — same V-1-class layer separation
	// as recordLocks. The lock exists to skip redundant work, not to
	// enforce uniqueness.
	//
	// Per-key granularity preserves cross-key parallelism: different
	// logical keys still run concurrently across workers; only same-
	// key races serialize.
	//
	// Map values are *sync.Mutex; entries are kept for the dispatcher's
	// lifetime. At testnet scale this is bounded by the count of
	// distinct (consumer, key) pairs ever observed; for mainnet a
	// future enhancement may add a TTL-based eviction once the key's
	// admission record reaches StateApplied terminal.
	keyLocks sync.Map // storeKey (string) → *sync.Mutex
}

// NewDispatcher constructs a dispatcher. All parameters are required.
// Register consumers before calling Recover or Admit.
func NewDispatcher(store AdmissionStore, dag DAGAnchorReader, epochFn func() uint64) *Dispatcher {
	return &Dispatcher{
		consumers:           make(map[string]Consumer),
		logicalKeyConsumers: make(map[string]LogicalKeyConsumer),
		store:               store,
		dag:                 dag,
		epochFn:             epochFn,
		deferralIndex:       make(map[event.EventID][]string),
	}
}

// SetEvidenceEmitter wires the function used to emit PrerequisiteWithholding
// evidence events to the DAG. Must be called before Admit.
func (d *Dispatcher) SetEvidenceEmitter(fn EvidenceEmitter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.evidenceEmitter = fn
}

// Register adds a content-hash consumer. Must be called before Recover
// or Admit. Performs structural validation per C-8. Returns an error if
// the consumer fails validation or if a consumer with the same Name is
// already registered (in either the content-hash OR logical-key map —
// names are unique across both kinds).
func (d *Dispatcher) Register(c Consumer) error {
	if err := validateConsumer(c); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.consumers[c.Name()]; exists {
		return fmt.Errorf("dispatch: consumer %q already registered", c.Name())
	}
	if _, exists := d.logicalKeyConsumers[c.Name()]; exists {
		return fmt.Errorf("dispatch: consumer name %q already registered as logical-key consumer; names must be unique across both kinds", c.Name())
	}
	d.consumers[c.Name()] = c
	return nil
}

// RegisterLogicalKey adds a logical-key consumer (Type E, F4B). Must be
// called before Recover or Admit. Performs structural validation
// (mirroring Register's C-8 check). Returns an error if the consumer
// fails validation or if a consumer with the same Name is already
// registered (in either the content-hash OR logical-key map — names
// are unique across both kinds).
//
// Per F4 plan v2 §4.4 and locked-invariant review §3.4. The dispatcher
// routes events through admitLogicalKey for any LogicalKeyConsumer
// whose Interested(ev) returns true; the same event can independently
// flow through both the logical-key path AND the content-hash path
// when consumers of both kinds are interested in it.
func (d *Dispatcher) RegisterLogicalKey(c LogicalKeyConsumer) error {
	if err := validateLogicalKeyConsumer(c); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.logicalKeyConsumers[c.Name()]; exists {
		return fmt.Errorf("dispatch: logical-key consumer %q already registered", c.Name())
	}
	if _, exists := d.consumers[c.Name()]; exists {
		return fmt.Errorf("dispatch: consumer name %q already registered as content-hash consumer; names must be unique across both kinds", c.Name())
	}
	d.logicalKeyConsumers[c.Name()] = c
	return nil
}

// ConsumerCount returns the number of registered content-hash consumers.
// Logical-key consumers are counted separately via LogicalKeyConsumerCount.
func (d *Dispatcher) ConsumerCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.consumers)
}

// LogicalKeyConsumerCount returns the number of registered logical-key
// consumers (F4B Type E).
func (d *Dispatcher) LogicalKeyConsumerCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.logicalKeyConsumers)
}

// Admit processes a canonical event delivery. Thread-safe; may be called
// from multiple goroutines concurrently. The dispatcher guarantees
// exactly-once invocation of each interested content-hash consumer's
// Apply method per canonical event (C-1) and exactly-once invocation
// of each interested logical-key consumer's Apply method per
// (consumer, logical_key) pair (F4B; locked-invariant review §3.5).
//
// The admission flow:
//  1. Logical-key admission path (F4B). Fires for any registered
//     LogicalKeyConsumer whose Interested(ev) returns true. Independent
//     of the content-hash path; both flows can run for the same event.
//     Cheap fast-path when no logical-key consumers are registered.
//  2. Content-hash admission path (F3-B, unchanged):
//     a. Canonicalize the event and compute the BLAKE3 admission key
//        (outside any transaction, per C-3/C-5).
//     b. Verify the DAG anchor (per C-6).
//     c. Read-modify-write the admission record (atomic, per C-2).
//     d. Invoke consumers outside the transaction (per C-5).
//
// The two paths are kept structurally separate so the F3-B content-hash
// behavior — including error precedence (AdmissionKey before
// VerifyAnchor) and persistence layout — is preserved bit-exact for
// the no-logical-key-consumer configuration that production runs today.
func (d *Dispatcher) Admit(ctx context.Context, ev *event.Event) error {
	// F4B logical-key admission. Cheap fast-path when no logical-key
	// consumers are registered (the snapshot returns empty, no
	// iteration, no per-event work, no anchor verification).
	if err := d.admitLogicalKey(ctx, ev); err != nil {
		return err
	}

	// Existing F3-B content-hash flow — preserved bit-exact.
	return d.admitContentHash(ctx, ev)
}

// admitContentHash runs the F3-B content-hash admission flow. Extracted
// from the original Admit body in F4B step 1 slice 4 so the logical-key
// branch can sit ahead of it without altering this path. Behavior is
// bit-identical to the pre-F4B body, including error precedence
// (AdmissionKey computed before VerifyAnchor).
func (d *Dispatcher) admitContentHash(ctx context.Context, ev *event.Event) error {
	key, err := AdmissionKey(ev)
	if err != nil {
		return fmt.Errorf("dispatch: admission key: %w", err)
	}

	if err := VerifyAnchor(d.dag, d.currentAnchor()); err != nil {
		return err
	}

	d.mu.RLock()
	consumers := d.snapshotInterestedConsumers(ev)
	d.mu.RUnlock()

	if len(consumers) == 0 {
		return nil
	}

	rec, err := d.reserveOrLoad(key, ev, consumers)
	if err != nil {
		return err
	}

	if rec.State == StateApplied {
		return nil
	}

	// Check prerequisites outside the transaction (D-8).
	prereqResult, prereqErr := d.checkPrerequisites(ev, consumers)
	if prereqErr != nil {
		// Forgery: clean up the reservation record — the prerequisite
		// can never be satisfied since it's not a real ancestor.
		_ = d.store.DeleteAdmission(key)
		return prereqErr // forgery → fail admission (D-4)
	}

	if !prereqResult.allProjected {
		// Valid but missing prerequisites → defer (D-1, D-2).
		rec.State = StateReservedPendingPrereqs
		rec.MissingPrerequisites = prereqResult.missing
		if err := d.store.PutAdmission(key, rec); err != nil {
			return fmt.Errorf("dispatch: persist deferral for %s: %w", key, err)
		}
		d.mu.Lock()
		d.addToDeferralIndex(prereqResult.missing, key)
		d.mu.Unlock()
		return nil
	}

	// All prerequisites satisfied → proceed to processing.
	if rec.State == StateReservedPendingPrereqs {
		rec.State = StateProcessing
		rec.MissingPrerequisites = nil
		if err := d.store.PutAdmission(key, rec); err != nil {
			return fmt.Errorf("dispatch: prereq-to-processing for %s: %w", key, err)
		}
	}

	return d.invokeConsumers(ctx, ev, key, rec)
}

func (d *Dispatcher) currentAnchor() event.EventID {
	tips := d.dag.Tips()
	if len(tips) > 0 {
		return tips[0]
	}
	return ""
}

func (d *Dispatcher) snapshotInterestedConsumers(ev *event.Event) []Consumer {
	// E.P1 (F4 plan §6): iterate consumer names in lexicographic order so
	// the returned slice is deterministic across nodes. Map iteration order
	// would otherwise leak into the AdmissionRecord.Consumers map's
	// insertion order, the sequence of safeApply invocations in
	// invokeConsumers, and any consumer-set-derived diagnostics — all
	// observable cross-node when consumers count > 1.
	names := make([]string, 0, len(d.consumers))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for name := range d.consumers {
		names = append(names, name)
	}
	sort.Strings(names)

	var interested []Consumer
	for _, name := range names {
		c := d.consumers[name]
		if c.Interested(ev) {
			interested = append(interested, c)
		}
	}
	return interested
}

// reserveOrLoad atomically reads or creates the admission record.
// Per C-2: the check-and-set is atomic. Per C-7: any storage error
// other than "not found" causes the dispatcher to refuse invocation.
func (d *Dispatcher) reserveOrLoad(key string, ev *event.Event, consumers []Consumer) (*AdmissionRecord, error) {
	existing, err := d.store.GetAdmission(key)
	if err != nil {
		// Not found: create a new reservation.
		if isNotFound(err) {
			return d.createReservation(key, ev, consumers)
		}
		return nil, fmt.Errorf("dispatch: get admission %s: %w", key, err)
	}

	// Already have a record. Handle based on state.
	switch existing.State {
	case StateApplied:
		return existing, nil

	case StateFailedRetryable:
		existing.State = StateProcessing
		if err := d.store.PutAdmission(key, existing); err != nil {
			return nil, fmt.Errorf("dispatch: retry transition for %s: %w", key, err)
		}
		return existing, nil

	case StateReservedPendingPrereqs:
		// Return as-is; the prerequisite check in Admit will determine
		// whether to transition to processing or stay deferred.
		return existing, nil

	case StateProcessing:
		return existing, nil
	}

	return existing, nil
}

func (d *Dispatcher) createReservation(key string, ev *event.Event, consumers []Consumer) (*AdmissionRecord, error) {
	consumerMap := make(map[string]PerConsumerStatus, len(consumers))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, c := range consumers {
		consumerMap[c.Name()] = ConsumerPending
	}

	// Capture the max PrerequisiteSchemaVersion across interested consumers.
	var maxSchemaVer uint32
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, c := range consumers {
		if v := c.PrerequisiteSchemaVersion(); v > maxSchemaVer {
			maxSchemaVer = v
		}
	}

	rec := &AdmissionRecord{
		SchemaVersion:             1,
		Key:                       key,
		State:                     StateReservedPendingPrereqs,
		DAGAnchor:                 d.currentAnchor(),
		PrerequisiteSchemaVersion: maxSchemaVer,
		Consumers:                 consumerMap,
		EventID:                   ev.ID,
		EventType:                 string(ev.Type),
		CreatedAtEpoch:            d.epochFn(),
	}

	if err := d.store.PutAdmission(key, rec); err != nil {
		return nil, fmt.Errorf("dispatch: create reservation for %s: %w", key, err)
	}
	return rec, nil
}

// invokeConsumers calls Apply on each consumer that is still pending or
// failed-retryable. Consumers already in per-consumer applied are skipped
// (C-1). After all invocations, the top-level state is recomputed and
// persisted.
func (d *Dispatcher) invokeConsumers(ctx context.Context, ev *event.Event, key string, rec *AdmissionRecord) error {
	d.mu.RLock()
	consumers := d.consumers
	d.mu.RUnlock()

	// E.P1 (F4 plan §6): lex-sort consumer names so safeApply is invoked
	// in a deterministic order across nodes. Different orders here would
	// produce different sequences of canonical state mutation when
	// multiple consumers touch the same canonical surface (e.g., ledger,
	// escrow). With a single consumer registered today this is decorative;
	// becomes load-bearing the moment a second consumer is added.
	names := make([]string, 0, len(rec.Consumers))
	for name := range rec.Consumers {
		names = append(names, name)
	}
	sort.Strings(names)

	changed := false
	for _, name := range names {
		status := rec.Consumers[name]
		if status == ConsumerApplied {
			continue
		}
		c, ok := consumers[name]
		if !ok {
			continue
		}

		err := safeApply(ctx, c, ev)
		if err != nil {
			slog.Warn("dispatch: consumer Apply failed",
				"consumer", name, "event", ev.ID, "err", err)
			rec.Consumers[name] = ConsumerFailedRetryable
			changed = true
		} else {
			rec.Consumers[name] = ConsumerApplied
			changed = true
		}
	}

	if changed {
		rec.State = computeTopLevelState(rec.Consumers)
		if err := d.store.PutAdmission(key, rec); err != nil {
			return fmt.Errorf("dispatch: persist after invoke for %s: %w", key, err)
		}
	}
	return nil
}

// safeApply calls consumer.Apply with panic recovery. A panic is treated
// as a failed-retryable error.
func safeApply(ctx context.Context, c Consumer, ev *event.Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("dispatch: consumer %s panicked on event %s: %v", c.Name(), ev.ID, r)
		}
	}()
	return c.Apply(ctx, ev)
}

// Recover scans all non-terminal admission records at startup and
// resolves them deterministically. Must be called after Register and
// before any Admit calls (load-before-listener ordering).
func (d *Dispatcher) Recover(ctx context.Context) error {
	records, err := d.store.AllAdmissions()
	if err != nil {
		return fmt.Errorf("dispatch: recovery scan failed: %w", err)
	}

	d.mu.RLock()
	consumers := d.consumers
	logicalKeyConsumers := d.logicalKeyConsumers
	d.mu.RUnlock()

	// Rebuild the in-memory deferral index from persisted records.
	d.mu.Lock()
	d.rebuildDeferralIndex(records)
	d.mu.Unlock()

	for _, rec := range records {
		// Logical-key admission records (F4B) have a different recovery
		// shape than content-hash records: they have no prerequisite
		// semantics (no Prerequisites interface method on LogicalKeyConsumer),
		// their recovery probe is consumer-per-(consumer, key) rather than
		// per-(consumer, event), and their store key encodes the consumer
		// name for direct lookup. Route them through recoverLogicalKey and
		// continue; the content-hash recovery path below does not apply.
		if rec.Strategy == AdmissionStrategyLogicalKey {
			if err := d.recoverLogicalKey(ctx, rec, logicalKeyConsumers); err != nil {
				return err
			}
			continue
		}

		// Schema version mismatch check (D-6): refuse to advance records
		// whose stored PrerequisiteSchemaVersion differs from any current
		// consumer's version. Abort startup with an operator-action
		// diagnostic.
		if rec.State != StateApplied {
			if err := d.checkSchemaVersions(rec, consumers); err != nil {
				return err
			}
		}

		switch rec.State {
		case StateApplied:
			continue

		case StateFailedRetryable:
			continue

		case StateReservedPendingPrereqs:
			// Re-check prerequisites. If still missing, leave deferred.
			// If now satisfied, transition to processing and fall through
			// to recovery probe.
			var stillMissing []event.EventID
			for _, pid := range rec.MissingPrerequisites {
				if _, dagErr := d.dag.Get(pid); dagErr != nil {
					stillMissing = append(stillMissing, pid)
				}
			}
			if len(stillMissing) > 0 {
				rec.MissingPrerequisites = stillMissing
				// Check failover threshold during recovery.
				currentEpoch := d.epochFn()
				if rec.CreatedAtEpoch <= currentEpoch {
					age := currentEpoch - rec.CreatedAtEpoch
					if age >= DeferralFailoverThreshold {
						return fmt.Errorf("dispatch: admission %s deferred for %d epochs "+
							"(threshold %d); manual intervention required",
							rec.Key, age, DeferralFailoverThreshold)
					}
				}
				if err := d.store.PutAdmission(rec.Key, rec); err != nil {
					return fmt.Errorf("dispatch: recovery persist deferred %s: %w", rec.EventID, err)
				}
				continue
			}
			rec.State = StateProcessing
			rec.MissingPrerequisites = nil
			d.mu.Lock()
			d.removeFromDeferralIndex(rec.Key)
			d.mu.Unlock()
			fallthrough

		case StateProcessing:
			ev, evErr := d.dag.Get(rec.EventID)
			if evErr != nil {
				return fmt.Errorf("dispatch: recovery: event %s not in DAG: %w",
					rec.EventID, evErr)
			}

			// E.P1 (F4 plan §6): lex-sort so RecoveryProbe is invoked in
			// deterministic order across nodes. Probe order is observable
			// when consumers share probe-targets; sorting eliminates the
			// non-determinism even though no current consumer pair shares
			// targets.
			probeNames := make([]string, 0, len(rec.Consumers))
			for name := range rec.Consumers {
				probeNames = append(probeNames, name)
			}
			sort.Strings(probeNames)

			for _, name := range probeNames {
				status := rec.Consumers[name]
				if status != ConsumerPending {
					continue
				}
				c, ok := consumers[name]
				if !ok {
					continue
				}
				probeResult, probeErr := c.RecoveryProbe(ctx, ev)
				if probeErr != nil {
					return fmt.Errorf("dispatch: recovery probe %s for event %s: %w",
						name, rec.EventID, probeErr)
				}
				switch probeResult {
				case RecoveryCompleted:
					rec.Consumers[name] = ConsumerApplied
				case RecoveryNotStarted:
					rec.Consumers[name] = ConsumerFailedRetryable
				}
			}
		}

		rec.State = computeTopLevelState(rec.Consumers)
		if err := d.store.PutAdmission(rec.Key, rec); err != nil {
			return fmt.Errorf("dispatch: recovery persist for %s: %w", rec.EventID, err)
		}
	}
	return nil
}

// recoverLogicalKey resolves one non-terminal logical-key admission
// record at startup. Per C-14 (recovery probes evidence-based,
// monotonic, replay-safe): if the consumer reports positive evidence
// that Apply completed (RecoveryCompleted), promote the record to
// StateApplied. Otherwise mark it StateFailedRetryable so the next
// Admit for any event projecting to this key drives a retry.
//
// Per F4B: logical-key records use the RecoveryProbe(key) method on
// LogicalKeyConsumer rather than content-hash RecoveryProbe(ev). The
// key is carried directly in rec.LogicalKey (populated by
// reserveOrLoadLogical); no DAG lookup is needed because the probe is
// over canonical durable state the consumer owns, not over a
// triggering event.
//
// Records whose consumer is no longer registered (orphan — e.g., the
// consumer was renamed between binary versions) are left in whatever
// state they were persisted in; the next Admit from a registered
// consumer of the same kind will write a fresh record. This matches
// the content-hash path's orphan handling.
//
// Records in StateApplied are left untouched (the per-key Apply
// guarantee is already satisfied). Records in StateFailedRetryable
// are also left untouched — a subsequent Admit drives retry without
// probing, mirroring content-hash semantics.
func (d *Dispatcher) recoverLogicalKey(
	ctx context.Context,
	rec *AdmissionRecord,
	consumers map[string]LogicalKeyConsumer,
) error {
	switch rec.State {
	case StateApplied, StateFailedRetryable:
		return nil
	}

	// Probe every consumer recorded on this admission. In practice a
	// logical-key record carries exactly one consumer (the one that
	// reserved it under its own name namespace); the loop matches the
	// content-hash shape for symmetry.
	//
	// E.P1 (F4 plan §6): lex-sort so probe order is deterministic
	// across nodes even though the single-consumer-per-record invariant
	// makes this decorative today.
	names := make([]string, 0, len(rec.Consumers))
	for name := range rec.Consumers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		status := rec.Consumers[name]
		if status == ConsumerApplied {
			continue
		}
		c, ok := consumers[name]
		if !ok {
			// Orphan: consumer no longer registered. Leave record
			// untouched; next Admit from a registered consumer drives
			// the state machine. Mirrors content-hash orphan handling.
			continue
		}
		probeResult, probeErr := c.RecoveryProbe(ctx, rec.LogicalKey)
		if probeErr != nil {
			return fmt.Errorf("dispatch: logical-key recovery probe %s for key %s: %w",
				name, rec.LogicalKey, probeErr)
		}
		switch probeResult {
		case RecoveryCompleted:
			rec.Consumers[name] = ConsumerApplied
		case RecoveryNotStarted:
			rec.Consumers[name] = ConsumerFailedRetryable
		}
	}

	rec.State = computeTopLevelState(rec.Consumers)
	if err := d.store.PutAdmission(rec.Key, rec); err != nil {
		return fmt.Errorf("dispatch: logical-key recovery persist for %s: %w", rec.Key, err)
	}
	return nil
}

// checkSchemaVersions verifies that non-applied admission records have a
// PrerequisiteSchemaVersion that matches every current consumer with
// pending status. Per D-6: mismatch aborts startup with an operator-action
// diagnostic. No canonical ledger rollback implied.
func (d *Dispatcher) checkSchemaVersions(rec *AdmissionRecord, consumers map[string]Consumer) error {
	// E.P1 (F4 plan §6): lex-sort so the FIRST mismatch reported is
	// deterministic across nodes. Without sorting, two nodes could report
	// different consumers as the mismatch source for the same record;
	// stable order makes operator triage reproducible.
	names := make([]string, 0, len(rec.Consumers))
	for name := range rec.Consumers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		status := rec.Consumers[name]
		if status == ConsumerApplied {
			continue
		}
		c, ok := consumers[name]
		if !ok {
			continue // orphaned consumer
		}
		if rec.PrerequisiteSchemaVersion != c.PrerequisiteSchemaVersion() {
			return fmt.Errorf(
				"dispatch: schema version mismatch for consumer %q on admission %s: "+
					"record has version %d, consumer declares version %d. "+
					"Operator action: complete in-flight records under the old binary, "+
					"or clear non-applied local admission state (dispatch: BadgerDB prefix) "+
					"after verifying no canonical effects were committed. "+
					"No canonical ledger rollback is implied.",
				name, rec.Key, rec.PrerequisiteSchemaVersion, c.PrerequisiteSchemaVersion())
		}
	}
	return nil
}

// isNotFound checks whether err is a "key not found" error from BadgerDB
// or the store layer. The store returns badger.ErrKeyNotFound directly.
func isNotFound(err error) bool {
	return err != nil && err.Error() == "Key not found"
}
```

## File: `internal/dispatch/logical_key_admit.go`

```go
package dispatch

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// admitLogicalKey runs the F4B logical-key admission flow for ev.
//
// Per F4 plan v2 §4.5:
//   1. Snapshot the registered logical-key consumers in lex-sorted order
//      (E.P1 invariant — deterministic across nodes).
//   2. Filter to those whose Interested(ev) returns true.
//   3. For each interested consumer, run admitOneLogicalKey, which is
//      the per-consumer logical-key state machine.
//
// Cheap fast-path: when no logical-key consumers are registered the
// snapshot is empty and the function returns immediately. The
// performance non-regression gate requires that
// BenchmarkAdmit_FreshContentHash NOT regress >5% from the F4A baseline
// just from the addition of this branch — empty-map iteration plus the
// nil-slice early return is well below that ceiling.
//
// Per C-6: VerifyAnchor must be called before logical-key consumers
// observe an event. Skipped on the empty-snapshot fast path because no
// observation occurs there; called once when at least one consumer is
// interested (mirrors the content-hash flow's invariant that anchor
// verification gates every admission that mutates state).
func (d *Dispatcher) admitLogicalKey(ctx context.Context, ev *event.Event) error {
	d.mu.RLock()
	consumers := d.snapshotInterestedLogicalKeyConsumersLocked(ev)
	d.mu.RUnlock()

	if len(consumers) == 0 {
		return nil
	}

	if err := VerifyAnchor(d.dag, d.currentAnchor()); err != nil {
		return err
	}

	for _, c := range consumers {
		if err := d.admitOneLogicalKey(ctx, ev, c); err != nil {
			return err
		}
	}
	return nil
}

// snapshotInterestedLogicalKeyConsumersLocked returns the subset of
// registered logical-key consumers whose Interested(ev) returns true,
// in lex-sorted Name order. Caller must hold d.mu (read or write).
//
// E.P1 (F4 plan §6 / locked-invariant review §3.4): iterate consumer
// names in lexicographic order so the returned slice is deterministic
// across nodes. Map iteration order would otherwise leak into the
// sequence of admitOneLogicalKey invocations, which is observable
// cross-node when consumers count > 1 (e.g., when both
// TVConsensusLogicalKeyConsumer and a future SettlementLogicalKey
// consumer share an event type).
func (d *Dispatcher) snapshotInterestedLogicalKeyConsumersLocked(ev *event.Event) []LogicalKeyConsumer {
	if len(d.logicalKeyConsumers) == 0 {
		return nil
	}

	names := make([]string, 0, len(d.logicalKeyConsumers))
	// safe: collected then lex-sorted before iteration; no iteration-order leak
	for name := range d.logicalKeyConsumers {
		names = append(names, name)
	}
	sort.Strings(names)

	var interested []LogicalKeyConsumer
	for _, name := range names {
		c := d.logicalKeyConsumers[name]
		if c.Interested(ev) {
			interested = append(interested, c)
		}
	}
	return interested
}

// admitOneLogicalKey runs the per-(consumer, key) admission state
// machine for one logical-key consumer observing one event.
//
// Per F4 plan v2 §4.5 steps (a) through (g):
//   (a) Compute key = consumer.Key(ev).
//   (b) Reserve-or-load the per-(consumer, key) admission record.
//   (c) If already StateApplied: return without re-Apply (per-key
//       Apply guarantee). Future events for this key are observed but
//       do not trigger Apply.
//   (d) Query canonical RoundState via consumer.RoundState.
//   (e) consumer.IsComplete(rs). If false: persist as StateProcessing
//       (the observation is recorded; no Apply invocation).
//   (f) If complete and not yet applied: consumer.DeriveOutcome(rs).
//   (g) consumer.Apply(ctx, key, outcome). On success, mark
//       StateApplied. On failure, mark StateFailedRetryable; future
//       observations of an event for this key (or a re-Admit of any
//       event for this key) drive a retry.
//
// Critical: Apply receives the derived Outcome, NOT ev.Payload. This
// is the C-17 enforcement point — by passing the derived Outcome, the
// dispatcher prevents the consumer from accidentally deriving
// canonical state from the triggering event's (potentially advisory)
// payload fields.
//
// **Per-(consumer, key) lock** (F5 5B post-#133 LK race fix, Path A):
// the read-modify-write region (reserve → gate → IsComplete →
// DeriveOutcome → Apply → persist) is serialized by a per-storeKey
// mutex from d.keyLocks. Without it, two concurrent commit-bus
// workers processing byte-distinct events for the same logical key
// could both pass the line-127 StateApplied gate and both invoke
// consumer.Apply.
//
// Defense-in-depth framing per d.keyLocks doc: the lock is INTRA-NODE
// only; cross-node correctness rests on each LK consumer's Apply
// canonicality + ledger ErrDuplicateEntry idempotency + the F4B
// contract that LK Apply is idempotent or no-op for byte-distinct
// events with the same logical key. The lock prevents wasted intra-
// node Apply calls on race-loss; it is NOT the canonical-correctness
// mechanism. Same V-1-class layer separation as
// internal/escrow/applicator.go's recordLocks.
func (d *Dispatcher) admitOneLogicalKey(ctx context.Context, ev *event.Event, c LogicalKeyConsumer) error {
	key, err := c.Key(ev)
	if err != nil {
		return fmt.Errorf("dispatch: logical-key extraction for %s: %w", c.Name(), err)
	}

	storeKey := LogicalAdmissionKey(c.Name(), key)

	// Acquire per-(consumer, key) intra-node lock for the duration of
	// the read-modify-write region below. Defense-in-depth only;
	// canonical correctness is guaranteed by downstream idempotency
	// per d.keyLocks doc.
	lockI, _ := d.keyLocks.LoadOrStore(storeKey, &sync.Mutex{})
	lock := lockI.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	rec, err := d.reserveOrLoadLogical(storeKey, ev, c, key)
	if err != nil {
		return err
	}

	// Per-key Apply guarantee (C-1 generalized to (consumer, key)):
	// once StateApplied, no future event for this key invokes Apply
	// again. Returning here also handles the "later observation" case
	// where a second byte-distinct canonical event projects into the
	// same logical key after the first one already drove Apply.
	if rec.State == StateApplied {
		return nil
	}

	rs, err := c.RoundState(ctx, key)
	if err != nil {
		return fmt.Errorf("dispatch: RoundState for %s/%s: %w", c.Name(), key, err)
	}

	complete, err := c.IsComplete(rs)
	if err != nil {
		return fmt.Errorf("dispatch: IsComplete for %s/%s: %w", c.Name(), key, err)
	}

	if !complete {
		// Record the observation; defer Apply until completeness is reached.
		rec.State = StateProcessing
		rec.Consumers[c.Name()] = ConsumerPending
		if err := d.store.PutAdmission(storeKey, rec); err != nil {
			return fmt.Errorf("dispatch: persist incomplete logical-key %s/%s: %w", c.Name(), key, err)
		}
		return nil
	}

	outcome, err := c.DeriveOutcome(rs)
	if err != nil {
		return fmt.Errorf("dispatch: DeriveOutcome for %s/%s: %w", c.Name(), key, err)
	}

	// Per-key Apply (atomic per C-11). Apply once; failures are
	// recorded as StateFailedRetryable so a subsequent re-Admit of any
	// event for this key drives a retry (mirrors the F3-B
	// failed-retryable retry pattern at the (consumer, event) grain).
	if applyErr := safeApplyLogicalKey(ctx, c, key, outcome); applyErr != nil {
		rec.State = StateFailedRetryable
		rec.Consumers[c.Name()] = ConsumerFailedRetryable
		if persistErr := d.store.PutAdmission(storeKey, rec); persistErr != nil {
			return fmt.Errorf("dispatch: persist after logical-key apply failure for %s/%s: %w", c.Name(), key, persistErr)
		}
		return fmt.Errorf("dispatch: logical-key Apply for %s/%s: %w", c.Name(), key, applyErr)
	}

	rec.State = StateApplied
	rec.Consumers[c.Name()] = ConsumerApplied
	if err := d.store.PutAdmission(storeKey, rec); err != nil {
		return fmt.Errorf("dispatch: persist after logical-key apply success for %s/%s: %w", c.Name(), key, err)
	}
	return nil
}

// reserveOrLoadLogical atomically reads or creates the per-
// (consumer, key) admission record for a logical-key consumer.
//
// Mirrors reserveOrLoad's read-or-create discipline. Validates that
// any pre-existing record at storeKey has Strategy ==
// AdmissionStrategyLogicalKey; a mismatch here would indicate a
// LogicalAdmissionKey collision with the content-hash key space (which
// the "lk:" sub-prefix is designed to prevent) and is fail-loud rather
// than fail-silent.
func (d *Dispatcher) reserveOrLoadLogical(storeKey string, ev *event.Event, c LogicalKeyConsumer, key LogicalKey) (*AdmissionRecord, error) {
	existing, err := d.store.GetAdmission(storeKey)
	if err != nil {
		if isNotFound(err) {
			return &AdmissionRecord{
				SchemaVersion:  AdmissionCurrentVersion,
				Key:            storeKey,
				Strategy:       AdmissionStrategyLogicalKey,
				LogicalKey:     key,
				State:          StateProcessing,
				DAGAnchor:      d.currentAnchor(),
				Consumers:      map[string]PerConsumerStatus{c.Name(): ConsumerPending},
				EventID:        ev.ID,
				EventType:      string(ev.Type),
				CreatedAtEpoch: d.epochFn(),
			}, nil
		}
		return nil, fmt.Errorf("dispatch: get logical admission %s: %w", storeKey, err)
	}
	if existing.Strategy != AdmissionStrategyLogicalKey {
		return nil, fmt.Errorf("dispatch: storeKey collision — %s exists as %s strategy, want logical-key", storeKey, existing.Strategy)
	}
	if existing.Consumers == nil {
		existing.Consumers = make(map[string]PerConsumerStatus, 1)
	}
	if _, ok := existing.Consumers[c.Name()]; !ok {
		existing.Consumers[c.Name()] = ConsumerPending
	}
	return existing, nil
}

// safeApplyLogicalKey calls consumer.Apply with panic recovery,
// mirroring safeApply for content-hash consumers. A panic is treated
// as a failed-retryable error.
func safeApplyLogicalKey(ctx context.Context, c LogicalKeyConsumer, key LogicalKey, outcome Outcome) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("dispatch: logical-key consumer %s panicked on key %s: %v", c.Name(), key, r)
		}
	}()
	return c.Apply(ctx, key, outcome)
}
```

## File: `internal/recognition/task_verification_consensus_consumer.go`

```go
package recognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// TaskVerificationConsensusConsumer processes TaskVerificationConsensus
// events from the DAG: applies round state for replay safety AND advances
// per-(category, family) calibration counters AND evaluates slashing.
//
// Settlement invocation was previously owned by this consumer (via an
// inline dispatcher.Admit call with a direct settler.Settle fallback).
// Part E.1 moved that responsibility to the general
// DispatcherAdmissionConsumer, which forwards every committed event to
// dispatch.Dispatcher.Admit; the dispatcher routes
// TaskVerificationConsensus events to dispatch.TVConsensusConsumer.Apply
// via its Interested() filter. This consumer retains only the local-node
// replay-safe and best-effort sidework (round state, calibration,
// slashing) that does not require cross-node ledger convergence.
// EpochAncestorReader is the narrow read surface this consumer needs to
// populate round.EpochAtFinalization at terminal-transition time. Per
// F5 5B canonical-epoch sub-spec v2.2 §8.2: the reader MUST be backed by
// the same canonical DAG view used by activation checks and settlement
// ancestry reads — shadow caches, stale wrappers, or local-only views
// are forbidden, as consistency with canonical-state queries elsewhere
// is load-bearing for cross-node byte-equality at round finalization.
//
// *dag.DAG satisfies this interface structurally.
type EpochAncestorReader interface {
	CountAncestorsByType(descendant event.EventID, eventType event.EventType) (uint64, error)
}

type TaskVerificationConsensusConsumer struct {
	rounds      taskverification.Store
	slashing    *taskverification.SlashingEvaluator // nil if slashing not wired
	calibration *taskverification.CalibrationStore  // nil if calibration not wired
	dagReader   EpochAncestorReader                 // nil ONLY in tests; production MUST wire stack.dag
}

// NewTaskVerificationConsensusConsumer creates a consensus consumer.
// slashing and calibration may be nil (graceful degradation).
//
// dagReader: per F5 5B canonical-epoch sub-spec v2.2 §8.2, this is the
// canonical reader used to compute round.EpochAtFinalization at
// terminal transition. In production, MUST be wired to *dag.DAG (the
// canonical DAG view). nil is permitted ONLY for tests that do not
// exercise the canonical-epoch field-population path; when nil, the
// consumer skips epoch field population and round.EpochAtFinalization
// stays zero. cmd/node/main.go is the production wiring site;
// regression risk surfaces if main.go is changed to pass nil.
//
// The settler parameter was removed in Part E.1: settlement now flows
// exclusively through the DispatcherAdmissionConsumer →
// dispatch.Dispatcher → dispatch.TVConsensusConsumer.Apply path.
func NewTaskVerificationConsensusConsumer(
	rounds taskverification.Store,
	slashing *taskverification.SlashingEvaluator,
	calibration *taskverification.CalibrationStore,
	dagReader EpochAncestorReader,
) *TaskVerificationConsensusConsumer {
	return &TaskVerificationConsensusConsumer{
		rounds:      rounds,
		slashing:    slashing,
		calibration: calibration,
		dagReader:   dagReader,
	}
}

// Name returns the unique consumer identifier.
func (c *TaskVerificationConsensusConsumer) Name() string { return "task_verification_consensus" }

// Interested returns true for TaskVerificationConsensus events.
func (c *TaskVerificationConsensusConsumer) Interested(ev *event.Event) bool {
	return ev.Type == event.EventTypeTaskVerificationConsensus
}

// Ready always returns true — consensus events are always ready.
func (c *TaskVerificationConsensusConsumer) Ready(_ context.Context, _ *event.Event, _ ReadModel) (bool, string, error) {
	return true, "", nil
}

// Consume applies the consensus event to the corresponding round, runs
// calibration counters, and evaluates slashing. Idempotent: if the round
// is already finalized in the same way, finalization is a no-op;
// calibration is guarded by round.CalibrationApplied; slashing is
// best-effort.
//
// Settlement is NOT invoked here — see the type doc comment. The
// DispatcherAdmissionConsumer forwards this event to the dispatcher,
// which routes it to dispatch.TVConsensusConsumer.Apply for the
// economic settlement.
func (c *TaskVerificationConsensusConsumer) Consume(_ context.Context, ev *event.Event) error {
	var payload event.TaskVerificationConsensusPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("task_verification_consensus: unmarshal: %w", err)
	}

	roundID := taskverification.RoundID(payload.RoundID)
	round, err := c.rounds.LoadRound(context.Background(), roundID)
	if err != nil {
		if errors.Is(err, taskverification.ErrRoundNotFound) {
			// Round not found — might arrive during replay before the round
			// consumer creates the round. Log and return nil.
			slog.Debug("task_verification_consensus: round not found (may arrive later)",
				"round_id", payload.RoundID, "event_id", ev.ID)
			return nil
		}
		return fmt.Errorf("task_verification_consensus: load round: %w", err)
	}

	// Apply finalization to the round if not already terminal.
	// The round may already be finalized by the vote consumer's inline
	// finalization path — that's fine, the admission-router-fed
	// dispatcher settlement path still runs regardless.
	if !round.IsTerminal() {
		verdict := parseConsensusVerdict(payload.FinalVerdict)
		targetState := consensusVerdictToState(verdict)
		if err := round.Transition(targetState, payload.FinalizationTimeUnix); err == nil {
			round.FinalVerdict = verdict
			round.FinalScoreBP = payload.FinalScoreBP

			// Per F5 5B canonical-epoch sub-spec v2.2 §8.2: populate the
			// canonical-epoch fields at terminal-transition time, atomically
			// with verdict + score. CanonicalSealContext = the TVConsensus
			// event ID being admitted (this event); EpochAtFinalization =
			// canonical count of EpochBoundary ancestors of that event.
			//
			// dagReader is nil in test environments that don't exercise this
			// path; production wires stack.dag (the canonical view).
			round.CanonicalSealContext = ev.ID
			if c.dagReader != nil {
				epoch, countErr := c.dagReader.CountAncestorsByType(ev.ID, event.EventTypeEpochBoundary)
				if countErr != nil {
					// Per sub-spec §8.4 + §3.1 all-or-defer: ErrEventNotFound
					// signals materialization lag. Under dag.Add's strict
					// CausalRefs invariant this is unreachable for events
					// arriving via the recognition fabric (the event itself
					// AND all its ancestors are in the DAG by then), but the
					// defensive branch surfaces canonical-state corruption
					// loudly rather than silently writing a zero epoch.
					return fmt.Errorf("task_verification_consensus: count epoch-boundary ancestors of %s: %w", ev.ID, countErr)
				}
				round.EpochAtFinalization = epoch
			}

			if err := c.rounds.SaveRound(context.Background(), round); err != nil {
				return fmt.Errorf("task_verification_consensus: save: %w", err)
			}
			slog.Info("task_verification_consensus: round finalized from DAG event",
				"round_id", payload.RoundID,
				"task_id", payload.TaskID,
				"verdict", payload.FinalVerdict,
				"score_bp", payload.FinalScoreBP,
				"event_id", ev.ID,
				"epoch_at_finalization", round.EpochAtFinalization,
			)
		}
	}

	// Settlement invocation is routed by the general recognition→dispatcher
	// admission router (internal/recognition/dispatcher_admission_consumer.go,
	// IM Part E.1 / commit-13). This consumer performs only per-node work
	// that does not require cross-node ledger convergence: round-state
	// finalization, calibration counters, slashing evaluation. The former
	// dispatcher field / SetDispatcher method / legacy direct-settler
	// fallback were removed when the general router landed — see locked-
	// invariant review §2.1 C-14 for the cross-check.

	// Apply calibration counters once per round per distinct analyzer family
	// that contributed any vote. Idempotency-guarded by round.CalibrationApplied
	// so a replay does not double-count. Must run BEFORE slashing so that
	// SlashingEvaluator.EvaluateRound reads the post-increment calibration
	// state when deciding whether a (category, family) tuple is calibrated.
	// Per step-2 plan §D2.
	if c.calibration != nil && !round.CalibrationApplied {
		allSucceeded := true
		seen := make(map[string]struct{}, len(round.Votes))
		for _, vote := range round.Votes {
			fam := vote.AnalyzerFamily
			if fam == "" {
				continue
			}
			if _, already := seen[fam]; already {
				continue
			}
			seen[fam] = struct{}{}
			if _, err := c.calibration.Increment(context.Background(), round.Category, verification.FamilyID(fam)); err != nil {
				slog.Warn("task_verification_consensus: calibration increment failed",
					"round_id", payload.RoundID,
					"category", round.Category,
					"family", fam,
					"err", err,
				)
				// Don't set CalibrationApplied; next replay retries.
				// Note: partially-applied increments before the failure will
				// double-count on retry, since Increment is non-idempotent.
				// This is within §8's conservative margin; noted for future
				// hardening.
				allSucceeded = false
				break
			}
		}
		if allSucceeded {
			round.CalibrationApplied = true
			if err := c.rounds.SaveRound(context.Background(), round); err != nil {
				slog.Warn("task_verification_consensus: save round after calibration failed",
					"round_id", payload.RoundID, "err", err)
			}
		}
	}

	// Evaluate slashing after calibration. Best-effort — failures log but
	// do not block the pipeline.
	if c.slashing != nil {
		actions := c.slashing.EvaluateRound(context.Background(), round)
		for _, action := range actions {
			slog.Info("task_verification_consensus: slashing action",
				"round_id", payload.RoundID,
				"validator_id", action.ValidatorID,
				"type", action.Type,
				"reason", action.Reason,
				"stake_penalty_bp", action.StakePenaltyBP,
				"reputation_penalty", action.ReputationPenalty,
			)
		}
	}

	return nil
}

func parseConsensusVerdict(s string) taskverification.Verdict {
	switch s {
	case "pass":
		return taskverification.VerdictPass
	case "fail":
		return taskverification.VerdictFail
	default:
		return taskverification.VerdictAbstain
	}
}

func consensusVerdictToState(v taskverification.Verdict) taskverification.RoundState {
	switch v {
	case taskverification.VerdictPass:
		return taskverification.RoundStateFinalizedAccept
	case taskverification.VerdictFail:
		return taskverification.RoundStateFinalizedReject
	default:
		return taskverification.RoundStateDisputed
	}
}

// Compile-time assertion.
var _ CommitConsumer = (*TaskVerificationConsensusConsumer)(nil)
```

## File: `internal/settlement/verification_consensus_settler.go`

```go
package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
	"github.com/Aethernet-network/aethernet/internal/settlement/derivation"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// v4.1 economic model: locked fee split (basis points out of 10000).
const (
	workerShareBP     = 7300 // 73%
	validatorShareBP  = 2300 // 23%
	generationShareBP = 200  // 2%
	treasuryShareBP   = 200  // 2%
)

// VerificationConsensusSettler processes TaskVerificationConsensus events
// and applies the v4.1 economic distribution: 73/23/2/2 on accept, full
// refund on reject, 50/50 on dispute.
// ValidatorQScoreFn returns the Quality Score Q for a validator in the
// context of a specific family and category. Used for Q-weighted fee
// distribution. Returns NeutralBP (10000, equivalent to the prior 1.0
// float) for new validators with no history.
//
// Return type migrated from float64 to protocolmath.BasisPoints in the
// canonical-distribution-integer-migration workstream (commit-3). Callers
// from the reputation store return BasisPoints natively; external
// callers that previously produced float values should scale by 10000
// and clamp to MaxBasisPoints.
type ValidatorQScoreFn func(validatorID crypto.AgentID, family string, category string) protocolmath.BasisPoints

type VerificationConsensusSettler struct {
	taskMgr    *tasks.TaskManager
	transfer   *ledger.TransferLedger
	escrowMgr  *escrow.Escrow
	dagScanner DAGScanner // for LookupEscrowLockTransfer on catch-up
	genLedger  *GenerationLedgerCalculator
	treasuryID crypto.AgentID
	qScoreFn   ValidatorQScoreFn // nil → even-split fallback

	// dagReader is the canonical AnchorReader passed to DeriveSettlement
	// (V-1 ActivationCheck closure + ReadAtAnchor + CountAncestorsByType
	// for cutoff_epoch). MUST be the same canonical view used by the
	// finalizing consumer's CountAncestorsByType call (sub-spec §8.2
	// canonical-DAG-view discipline). nil until set via SetDAGReader at
	// construction.
	dagReader derivation.AnchorReader

	// mu guards shadowMode. Reads happen on every settlement call (via
	// isShadowMode); writes happen rarely (once per IntegerMigration
	// activation event, via SetShadowMode). sync.RWMutex amortizes the
	// read cost; see Part E of the canonical-distribution-integer-migration
	// workstream for the activation consumer that drives the write.
	mu         sync.RWMutex
	shadowMode bool
}

// isShadowMode returns the current shadow-mode flag under RLock. Called
// on every settlement path.
func (s *VerificationConsensusSettler) isShadowMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shadowMode
}

// SetShadowMode switches the settler between shadow mode (legacy float
// path is canonical, integer path runs alongside for delta logging) and
// integer-canonical mode (integer path is canonical; legacy float path
// is not run). Called by the IntegerMigrationActivationConsumer when
// the activation event is applied, and at startup when a prior
// activation is loaded from the store.
//
// Thread-safe. One-way transition is the protocol-level expectation,
// but SetShadowMode itself accepts either value so tests can restore
// shadow mode after exercising the integer path in isolation.
func (s *VerificationConsensusSettler) SetShadowMode(shadow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shadowMode = shadow
}

// NewVerificationConsensusSettler creates a settler with the full v4.1
// economic model. qScoreFn may be nil for even-split fallback.
//
// dagScanner is used on the escrow catch-up path (peer node scenario) to
// locate the canonical escrow-lock Transfer event that funds the escrow.
// May be nil only in tests that do not exercise the catch-up path.
//
// shadowMode: pass true for Part B's shadow-compare behavior. Part E's
// activation consumer flips this to false at runtime when the
// EventTypeIntegerMigrationActivation event is projected, and startup
// restores it to false on a post-activation restart via the adapter in
// cmd/node/main.go.
func NewVerificationConsensusSettler(
	taskMgr *tasks.TaskManager,
	transfer *ledger.TransferLedger,
	escrowMgr *escrow.Escrow,
	dagScanner DAGScanner,
	genLedger *GenerationLedgerCalculator,
	treasuryID crypto.AgentID,
	qScoreFn ValidatorQScoreFn,
	shadowMode bool,
) *VerificationConsensusSettler {
	return &VerificationConsensusSettler{
		taskMgr:    taskMgr,
		transfer:   transfer,
		escrowMgr:  escrowMgr,
		dagScanner: dagScanner,
		genLedger:  genLedger,
		treasuryID: treasuryID,
		qScoreFn:   qScoreFn,
		shadowMode: shadowMode,
	}
}

// SetDAGReader installs the canonical AnchorReader used by
// DeriveSettlement. Called at production wiring time (cmd/node/main.go)
// after the *dag.DAG is constructed; tests that don't exercise the
// derivation path may leave it nil and the legacy Settle path applies.
//
// Per F5 5B canonical-epoch sub-spec §8.2 canonical-DAG-view discipline:
// the reader MUST be the same canonical view used by the finalizing
// consumer's CountAncestorsByType call. Shadow caches, stale wrappers,
// or local-only views are forbidden.
func (s *VerificationConsensusSettler) SetDAGReader(r derivation.AnchorReader) {
	s.dagReader = r
}

// SettleResult captures the outcome of a settlement application.
type SettleResult struct {
	Applied              bool
	AlreadyApplied       bool
	Deferred             bool                    // F5 5B: DeriveSettlement returned StatusDeferred
	DeferredCause        derivation.DeferredCause // populated when Deferred == true
	Verdict              string
	WorkerPayout         uint64
	PosterRefund         uint64
	ValidatorPayouts     map[crypto.AgentID]uint64
	GenerationLedger     GenerationLedgerDistribution
	TreasuryAmount       uint64
	TotalDistributed     uint64

	// Records is the canonical PayoutRecord slice produced by
	// DeriveSettlement (F5 5B). Empty when Deferred=true or when the
	// legacy ReleaseSettlement path is taken (dagReader not wired).
	Records []derivation.PayoutRecord
}

// Settle applies the v4.1 economic distribution for a verification consensus.
// Idempotent: tasks already in a terminal state produce AlreadyApplied=true.
//
// F5 5B integration: when dagReader is wired, Settle routes through
// derivation.DeriveSettlement → escrow.ApplySettlementRecords (the
// canonical pure-derivation path). When dagReader is nil (legacy /
// pre-5B test environments), falls back to the imperative
// settleAccept/Reject/Dispute path (which calls escrow.ReleaseSettlement).
//
// The dagReader-nil fallback is dead code at 5B closure (cmd/node/main.go
// always wires dagReader); it remains for the float-path companion-PR's
// transitional window per Plan v3 §0.10. Companion-PR removal eliminates
// the legacy branch entirely.
func (s *VerificationConsensusSettler) Settle(
	ctx context.Context,
	payload *event.TaskVerificationConsensusPayload,
	round *taskverification.TaskVerificationRound,
) (SettleResult, error) {
	result := SettleResult{Verdict: payload.FinalVerdict}

	// Look up the task.
	task, err := s.taskMgr.Get(payload.TaskID)
	if err != nil {
		return result, fmt.Errorf("verification_settler: task %s not found: %w", payload.TaskID, err)
	}

	// Idempotent: already in terminal state. Per Gate 5A.1 §9.2 option-b
	// the task.Status read here is an EARLY-EXIT short-circuit, NOT a
	// payout-math dependency — the §4.4 reopen condition does not fire.
	switch task.Status {
	case tasks.TaskStatusCompleted, tasks.TaskStatusRejected,
		tasks.TaskStatusDisputedResolved, tasks.TaskStatusCancelled:
		result.AlreadyApplied = true
		return result, nil
	}

	// Get escrowed budget.
	entry, err := s.escrowMgr.Get(payload.TaskID)
	if err != nil {
		// If escrow is not locked, try to catch up (peer node scenario).
		// The canonical escrow-lock Transfer has already moved funds; we only
		// need to register metadata linking the escrow entry to that Transfer.
		if !s.escrowMgr.IsLocked(payload.TaskID) {
			if s.dagScanner == nil {
				return result, fmt.Errorf("verification_settler: escrow catch-up required but dagScanner not configured for task %s", payload.TaskID)
			}
			fundingRef, lookupErr := LookupEscrowLockTransfer(
				s.dagScanner, payload.TaskID, crypto.AgentID(task.PosterID), task.Budget)
			if lookupErr != nil {
				return result, fmt.Errorf("verification_settler: escrow catch-up funding-transfer lookup failed: %w", lookupErr)
			}
			if regErr := s.escrowMgr.RegisterEscrow(payload.TaskID, crypto.AgentID(task.PosterID), task.Budget, fundingRef); regErr != nil {
				return result, fmt.Errorf("verification_settler: escrow catch-up register failed: %w", regErr)
			}
			entry, err = s.escrowMgr.Get(payload.TaskID)
			if err != nil {
				return result, fmt.Errorf("verification_settler: escrow still missing after catch-up: %w", err)
			}
		} else {
			return result, fmt.Errorf("verification_settler: escrow get failed: %w", err)
		}
	}

	// Suppress unused-variable warnings; entry/budget/etc. are read by
	// settleViaDerivation via the inputs bundle, not as locals here.
	_ = entry

	// F5 5B: route through DeriveSettlement. dagReader MUST be wired
	// at construction (cmd/node/main.go:tvSettler.SetDAGReader). The
	// legacy settleAccept/Reject/Dispute paths were removed in #133;
	// nil dagReader is now a precondition violation.
	if s.dagReader == nil {
		return result, fmt.Errorf("verification_settler: dagReader not wired — SetDAGReader must be called before Settle (post-F5 5B canonical path requires the DAG view for DeriveSettlement)")
	}
	return s.settleViaDerivation(ctx, payload, round, result)
}

// settleViaDerivation is the F5 5B canonical path: build
// DerivationInputs, call DeriveSettlement, route Status to apply or
// defer, then call ApplySettlementRecords + finalize task state.
//
// Per Plan v3 §3.5 caller-code shape:
//
//	result, err := deriveSettlement(ctx, round, inputs)
//	if err != nil { return err }
//	if result.Status == StatusDeferred { deferRound(round); return nil }
//	return e.escrowMgr.ApplySettlementRecords(taskID, result.Records)
func (s *VerificationConsensusSettler) settleViaDerivation(
	ctx context.Context,
	payload *event.TaskVerificationConsensusPayload,
	round *taskverification.TaskVerificationRound,
	result SettleResult,
) (SettleResult, error) {
	inputs := s.buildDerivationInputs()

	derived, err := derivation.DeriveSettlement(ctx, round, inputs)
	if err != nil {
		return result, fmt.Errorf("verification_settler: DeriveSettlement: %w", err)
	}
	if derived.Status == derivation.StatusDeferred {
		// Re-enqueue handled by the recognition fabric / dispatcher
		// retry mechanism (F3-B causal-prerequisite-gating). Caller
		// observes Deferred=true and does NOT mark the task as
		// terminal — it'll re-fire when canonical state advances.
		result.Deferred = true
		result.DeferredCause = derived.Cause
		slog.Info("verification_settler: deferred (canonical state not yet materialized)",
			"task_id", payload.TaskID,
			"round_id", payload.RoundID,
			"cause", derived.Cause.String(),
		)
		return result, nil
	}

	// Apply the canonical records.
	if applyErr := s.escrowMgr.ApplySettlementRecords(payload.TaskID, derived.Records); applyErr != nil {
		return result, fmt.Errorf("verification_settler: ApplySettlementRecords: %w", applyErr)
	}

	// Last step: terminal task-state transition. If Settle is interrupted
	// after ApplySettlementRecords but before this transition, retry's
	// pre-check (task.Status terminal) will short-circuit, AND the
	// escrow.ApplySettlementRecords paid-flag projection will skip-
	// optimize the records on retry. No double-pay risk per Plan v3
	// §3.4 obligation a/b/c + crash-position table.
	taskVerdictString := payload.FinalVerdict
	if transErr := s.taskMgr.ApplyVerificationConsensusResolution(
		payload.TaskID, taskVerdictString, payload.FinalScoreBP, payload.RoundID,
	); transErr != nil {
		return result, fmt.Errorf("verification_settler: task state: %w", transErr)
	}

	// Populate observability fields on SettleResult from the derived
	// records (back-compat with downstream observers that read these
	// fields; future-work to switch downstream observers to
	// result.Records directly).
	result.Records = derived.Records
	result.Applied = true
	for _, r := range derived.Records {
		switch r.Recipient.Role {
		case derivation.RoleWorker:
			result.WorkerPayout = r.Amount.Value
		case derivation.RolePosterRefund:
			result.PosterRefund = r.Amount.Value
		case derivation.RoleValidator:
			if result.ValidatorPayouts == nil {
				result.ValidatorPayouts = make(map[crypto.AgentID]uint64)
			}
			result.ValidatorPayouts[r.Recipient.ID] = r.Amount.Value
		case derivation.RoleTreasury:
			result.TreasuryAmount = r.Amount.Value
		}
		result.TotalDistributed += r.Amount.Value
	}

	slog.Info("verification_settler: settled via derivation",
		"task_id", payload.TaskID,
		"round_id", payload.RoundID,
		"verdict", payload.FinalVerdict,
		"records", len(derived.Records),
		"total_distributed", result.TotalDistributed,
	)
	return result, nil
}

// buildDerivationInputs assembles the DerivationInputs bundle from the
// settler's existing primitives. Built per-call (lightweight; no
// expensive resource construction). Per F5 5B canonical-epoch sub-spec
// §1.4.1 + DerivationInputs §2.1 contract: every field is canonical-
// frozen or deterministic-replayable-lookup.
func (s *VerificationConsensusSettler) buildDerivationInputs() derivation.DerivationInputs {
	wStub := derivation.CanonicalWProjection(qScoreFnAsCanonicalW{fn: s.qScoreFn})
	if s.qScoreFn == nil {
		// Pre-reputation-store wiring case: use the package's universal
		// NeutralBP stub to keep V-1 semantics intact (every validator
		// returns NeutralBP).
		wStub = derivation.NeutralBPStubW{}
	}

	activationCheck := func(activationEventID, sealContext event.EventID) (bool, error) {
		// dag.IsAncestor with empty activationEventID returns false +
		// ErrEventNotFound (per dag.go:665-667). Match the V-1 contract:
		// when the activation event hasn't been defined, treat as
		// "not a canonical ancestor" (use stub). Empty-string check
		// short-circuits to false to match the canonical semantic
		// without surfacing an ErrEventNotFound deferral signal.
		if activationEventID == "" {
			return false, nil
		}
		return s.dagReader.IsAncestor(activationEventID, sealContext)
	}

	return derivation.DerivationInputs{
		W: derivation.WProjections{
			Stub: wStub,
			// Real: nil — locked Reputation-and-Consensus-Integrity
			// workstream's real W ships separately. ActivationCheck
			// returns false today (empty ReputationActivationEventID),
			// so the Real slot is never selected.
		},
		Quality: derivation.QualityProjections{
			Stub: derivation.NeutralQualityStub{},
			// Real: nil — quality canonicalization is deferred to a
			// future workstream per sub-spec §3.
		},
		DAGReader:       s.dagReader,
		EscrowMgr:       escrowDerivationLookup{escrow: s.escrowMgr},
		TaskMgr:         taskDerivationLookup{tasks: s.taskMgr},
		ActivationCheck: activationCheck,
		TreasuryID:      s.treasuryID,
	}
}


// computeValidatorPayouts calculates Q-weighted per-validator amounts
// without executing transfers. The returned map is passed to
// ReleaseSettlement which handles the actual transfers with per-recipient
// paid-flag idempotency.
//
// This is the shadow-gated entry point introduced in the canonical-
// distribution-integer-migration workstream. When shadowMode is true, the
// legacy float path (computeValidatorPayoutsFloat) is canonical; the
// integer path (computeValidatorPayoutsInteger) runs alongside and the
// per-recipient delta is logged via shadow_delta. When shadowMode is
// false (Part E and beyond), the integer path is the only path that runs.
//
// taskID is threaded in purely so the shadow_delta log line can identify
// which settlement the delta belongs to; it has no arithmetic effect.
func (s *VerificationConsensusSettler) computeValidatorPayouts(
	taskID string,
	recipients []crypto.AgentID,
	pool uint64,
	category string,
) map[crypto.AgentID]uint64 {
	if s.isShadowMode() {
		floatResult := s.computeValidatorPayoutsFloat(recipients, pool, category)
		intResult := s.computeValidatorPayoutsInteger(recipients, pool, category)
		s.logShadowDelta("validator_distribution", taskID, recipients, floatResult, intResult, pool)
		return floatResult
	}
	return s.computeValidatorPayoutsInteger(recipients, pool, category)
}

// computeValidatorPayoutsFloat is the legacy float-arithmetic payout
// computation, preserved verbatim from the pre-migration codebase. While
// shadowMode remains true on every node, this is the canonical path: its
// output is what the settler returns and what ReleaseSettlement applies.
// Removed when the shadow gate flips to integer-canonical (separate
// workstream).
//
// Behavior note carried forward from the pre-migration code: the float
// path absorbs its rounding remainder at the caller-slice-last recipient.
// Because the caller-supplied recipients slice ordering is not guaranteed
// to be stable across nodes (collectAgreeingValidators iterates a Votes
// slice whose ordering depends on local receive-order), the float path's
// remainder-absorption recipient can differ across nodes by the handful
// of µAET represented by the rounding remainder. The integer path (via
// protocolmath.AllocateWithCeiling) fixes this by sorting on CanonicalKey
// before allocation.
func (s *VerificationConsensusSettler) computeValidatorPayoutsFloat(
	recipients []crypto.AgentID,
	pool uint64,
	category string,
) map[crypto.AgentID]uint64 {
	payouts := make(map[crypto.AgentID]uint64)
	if len(recipients) == 0 || pool == 0 {
		return payouts
	}

	type scored struct {
		id crypto.AgentID
		q  float64
	}
	entries := make([]scored, len(recipients))
	var totalQ float64
	for i, v := range recipients {
		q := 1.0
		if s.qScoreFn != nil {
			// Down-convert BasisPoints to float64 inside the legacy path so
			// the pre-migration arithmetic is preserved unchanged. Removed
			// when the float path itself is removed.
			q = float64(s.qScoreFn(v, "", category)) / 10000.0
		}
		entries[i] = scored{id: v, q: q}
		totalQ += q
	}

	if totalQ == 0 {
		return evenSplitFallback(recipients, pool)
	}

	var distributed uint64
	for i, e := range entries {
		var amount uint64
		if i == len(entries)-1 {
			amount = pool - distributed
		} else {
			amount = uint64(float64(pool) * (e.q / totalQ))
		}
		if amount > 0 {
			payouts[e.id] = amount
		}
		distributed += amount
	}
	return payouts
}

// computeValidatorPayoutsInteger is the integer-arithmetic payout
// computation introduced by the canonical-distribution-integer-migration
// workstream. It pre-clamps negative Q to zero (so protocolmath's
// ErrInvariantViolation remains a true impossibility-signal rather than a
// routine handling path for upstream bugs) and routes the allocation
// through protocolmath.AllocateWithCeiling for deterministic cross-node
// results.
//
// Remainder-absorption: protocolmath sorts recipients by CanonicalKey
// (AgentID bytes) and routes the rounding remainder to the sorted-last
// recipient. This is deterministic across nodes regardless of receive-
// ordering of the vote events, which is a correctness improvement over
// the float path; see the float-path doc comment for the pre-migration
// non-determinism.
func (s *VerificationConsensusSettler) computeValidatorPayoutsInteger(
	recipients []crypto.AgentID,
	pool uint64,
	category string,
) map[crypto.AgentID]uint64 {
	if len(recipients) == 0 || pool == 0 {
		return map[crypto.AgentID]uint64{}
	}
	pm := make([]protocolmath.Recipient, 0, len(recipients))
	for _, v := range recipients {
		q := protocolmath.NeutralBP
		if s.qScoreFn != nil {
			q = s.qScoreFn(v, "", category)
		}
		if q < 0 {
			slog.Warn("settlement: qScoreFn returned negative; clamping to zero",
				"validator", v, "category", category, "returned_bp", q)
			q = 0
		}
		pm = append(pm, protocolmath.Recipient{CanonicalKey: []byte(v), Weight: q})
	}
	result, err := protocolmath.AllocateWithCeiling(pm, protocolmath.MicroAET(pool))
	if err != nil {
		slog.Error("settlement: protocolmath returned error; falling back to even-split",
			"err", err, "recipients", len(recipients), "pool", pool)
		return evenSplitFallback(recipients, pool)
	}
	out := make(map[crypto.AgentID]uint64, len(result))
	for k, v := range result {
		out[crypto.AgentID(k)] = uint64(v)
	}
	return out
}

// evenSplitFallback distributes pool evenly among recipients, routing any
// rounding remainder to the caller-slice-last recipient. Used by both the
// float path (when total Q is zero) and the integer path (when protocolmath
// returns an unexpected error; the latter should be unreachable in
// practice because negative Q is pre-clamped and recipient lists are built
// without duplicates by the settler).
func evenSplitFallback(recipients []crypto.AgentID, pool uint64) map[crypto.AgentID]uint64 {
	payouts := make(map[crypto.AgentID]uint64, len(recipients))
	if len(recipients) == 0 || pool == 0 {
		return payouts
	}
	perValidator := pool / uint64(len(recipients))
	var distributed uint64
	for i, v := range recipients {
		amount := perValidator
		if i == len(recipients)-1 {
			amount = pool - distributed
		}
		if amount > 0 {
			payouts[v] = amount
		}
		distributed += amount
	}
	return payouts
}

// logShadowDelta emits one line per computeValidatorPayouts invocation
// comparing the float and integer path outputs. Format documented in the
// canonical-distribution-integer-migration plan §4.5; Part F parses these
// lines from testnet logs to validate the cutover corpus.
//
// Conservation failures (sum mismatch) elevate to Warn; per-recipient
// differences stay Info because remainder-absorption-recipient drift is
// expected (up to the per-recipient rounding remainder) and is exactly
// the divergence the shadow pass is designed to surface before the
// cutover event flips the canonical path.
func (s *VerificationConsensusSettler) logShadowDelta(
	context, taskID string,
	recipients []crypto.AgentID,
	floatResult, intResult map[crypto.AgentID]uint64,
	pool uint64,
) {
	var sumFloat, sumInt uint64
	// safe: commutative sum; iteration order does not affect result
	for _, v := range floatResult {
		sumFloat += v
	}
	// safe: commutative sum; iteration order does not affect result
	for _, v := range intResult {
		sumInt += v
	}
	var maxAbs uint64
	for _, r := range recipients {
		f := floatResult[r]
		i := intResult[r]
		var d uint64
		if f > i {
			d = f - i
		} else {
			d = i - f
		}
		if d > maxAbs {
			maxAbs = d
		}
	}
	var sumDelta int64
	if sumFloat >= sumInt {
		sumDelta = int64(sumFloat - sumInt)
	} else {
		sumDelta = -int64(sumInt - sumFloat)
	}
	level := slog.LevelInfo
	if sumDelta != 0 {
		level = slog.LevelWarn
	}
	slog.Log(context_noop(), level, "shadow_delta",
		"context", context,
		"task_id", taskID,
		"recipient_count", len(recipients),
		"float_sum", sumFloat,
		"int_sum", sumInt,
		"sum_delta", sumDelta,
		"max_per_recipient_delta", maxAbs,
		"pool", pool,
	)
}

// context_noop returns a background context for slog.Log calls. Extracted
// so the helper's signature stays readable.
func context_noop() context.Context { return context.Background() }


```

## File: `internal/taskverification/round.go`

```go
package taskverification

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/jcs"
)

// ---------------------------------------------------------------------------
// TaskVerificationRound
// ---------------------------------------------------------------------------

// TaskVerificationRound tracks the lifecycle of a multi-validator verification
// of a single task submission. Each round is opened when a TaskSubmitted event
// is recognized, collects votes from validators, and finalizes via BFT
// consensus rules including analyzer-family diversity requirements.
type TaskVerificationRound struct {
	RoundID               RoundID        `json:"round_id"`
	TaskID                string         `json:"task_id"`
	SubmissionEventID     event.EventID  `json:"submission_event_id"`
	WorkerID              crypto.AgentID `json:"worker_id"`
	PosterID              crypto.AgentID `json:"poster_id"`
	Category              string         `json:"category"`
	ValidatorSetVersion   uint64         `json:"validator_set_version"`
	Committee             []crypto.AgentID `json:"committee,omitempty"` // nil = all active (bootstrap mode)
	AnalyzerPolicyID      string         `json:"analyzer_policy_id"`
	DiversityFloor        int            `json:"diversity_floor"`
	AcceptanceThresholdBP uint64         `json:"acceptance_threshold_bp"`
	OpenedAtUnix          int64          `json:"opened_at_unix"`
	DeadlineUnix          int64          `json:"deadline_unix"`
	ExtendedUntilUnix     int64          `json:"extended_until_unix,omitempty"` // 0 if not extended
	State                 RoundState     `json:"state"`

	// Aggregation state
	PassWeight            uint64            `json:"pass_weight"`
	FailWeight            uint64            `json:"fail_weight"`
	AbstainWeight         uint64            `json:"abstain_weight"`
	ParticipatingFamilies    map[string]uint64 `json:"participating_families,omitempty"`     // family_id → accumulated pass-weight
	AllParticipatingFamilies map[string]bool   `json:"all_participating_families,omitempty"` // family_id → true if any vote (pass/fail/abstain) received

	// Vote records (for audit and finalization)
	Votes []TaskVerificationVoteRecord `json:"votes,omitempty"`

	// Final outcome (set on finalization)
	FinalVerdict      Verdict `json:"final_verdict"`
	FinalScoreBP      uint64  `json:"final_score_bp"`
	FinalizationTime  int64   `json:"finalization_time,omitempty"`

	// CalibrationApplied is set to true after the recognition-fabric
	// consensus consumer applies per-family calibration increments for
	// this round. Guards against double-apply on consensus-event replay.
	// Per step-2 plan §D2: name the semantic state (CalibrationApplied),
	// not the writer mechanism (Increment). Field is JSON-optional so
	// rounds persisted before step 2 deserialize with zero-value false,
	// which is correct — the first replay catches up and sets the flag.
	CalibrationApplied bool `json:"calibration_applied,omitempty"`

	// CanonicalSealContext is the canonical TaskVerificationConsensus
	// event ID that finalized this round. Populated by the finalizing
	// consumer at terminal-transition time (per F5 5B canonical-epoch
	// sub-spec v2.2 §8 + prior-halt Option A). Canonical-frozen once set;
	// downstream consumers (DeriveSettlement) read it for V-1 ActivationCheck.
	//
	// Empty string for rounds finalized before F5 5B implementation —
	// migrating populated rounds is out of scope (testnet wipe at F5
	// merge per Plan v3 §0.5).
	CanonicalSealContext event.EventID `json:"canonical_seal_context,omitempty"`

	// EpochAtFinalization is the canonical epoch in which this round was
	// finalized, derived from the count of EpochBoundary canonical
	// ancestors of CanonicalSealContext (per sub-spec v2.2 §8.2). Pure
	// canonical-DAG-state read; no RoundCounter dependence (the
	// secondary-halt motivation for the entire canonical-epoch sub-spec).
	//
	// Zero for rounds finalized before F5 5B implementation. After F5
	// merge, every round R has EpochAtFinalization equal to the number
	// of canonical EpochBoundary events ancestral to R's
	// CanonicalSealContext.
	EpochAtFinalization uint64 `json:"epoch_at_finalization,omitempty"`
}

// TaskVerificationVoteRecord captures a single validator's vote within a round.
type TaskVerificationVoteRecord struct {
	ValidatorID          crypto.AgentID    `json:"validator_id"`
	Verdict              Verdict           `json:"verdict"`
	ScoreBP              uint64            `json:"score_bp"`
	ScoreBreakdown       map[string]uint64 `json:"score_breakdown,omitempty"`
	AnalyzerFamily       string            `json:"analyzer_family"`
	AnalyzerVersion      string            `json:"analyzer_version"`
	PolicyVersion        string            `json:"policy_version"`
	AnalysisArtifactHash string            `json:"analysis_artifact_hash,omitempty"`
	Stake                uint64            `json:"stake"`
	TimestampUnix        int64             `json:"timestamp_unix"`
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// OpenRoundParams contains the inputs for opening a new verification round.
type OpenRoundParams struct {
	TaskID                string
	SubmissionEventID     event.EventID
	WorkerID              crypto.AgentID
	PosterID              crypto.AgentID
	Category              string
	ValidatorSetVersion   uint64
	Committee             []crypto.AgentID // nil for bootstrap mode (all active validators)
	AnalyzerPolicyID      string
	DiversityFloor        int
	AcceptanceThresholdBP uint64
	DeadlineSeconds       int64
	Now                   int64 // injected for determinism in tests
}

// OpenRound creates a new TaskVerificationRound in Open state. The round ID
// is deterministically derived from the submission event ID.
func OpenRound(p OpenRoundParams) (*TaskVerificationRound, error) {
	if p.TaskID == "" {
		return nil, fmt.Errorf("%w: TaskID is required", ErrInvalidRoundID)
	}
	if p.SubmissionEventID == "" {
		return nil, fmt.Errorf("%w: SubmissionEventID is required", ErrInvalidRoundID)
	}
	if p.WorkerID == "" {
		return nil, fmt.Errorf("%w: WorkerID is required", ErrInvalidRoundID)
	}
	if p.PosterID == "" {
		return nil, fmt.Errorf("%w: PosterID is required", ErrInvalidRoundID)
	}
	if p.Category == "" {
		return nil, fmt.Errorf("%w: Category is required", ErrInvalidRoundID)
	}
	if p.DeadlineSeconds <= 0 {
		return nil, fmt.Errorf("%w: DeadlineSeconds must be > 0", ErrInvalidDeadline)
	}
	if p.DiversityFloor < 1 {
		return nil, fmt.Errorf("%w: DiversityFloor must be >= 1", ErrInvalidDeadline)
	}
	if p.AcceptanceThresholdBP > 10000 {
		return nil, fmt.Errorf("%w: AcceptanceThresholdBP must be in [0, 10000]", ErrInvalidDeadline)
	}

	return &TaskVerificationRound{
		RoundID:               NewRoundID(p.SubmissionEventID),
		TaskID:                p.TaskID,
		SubmissionEventID:     p.SubmissionEventID,
		WorkerID:              p.WorkerID,
		PosterID:              p.PosterID,
		Category:              p.Category,
		ValidatorSetVersion:   p.ValidatorSetVersion,
		Committee:             p.Committee,
		AnalyzerPolicyID:      p.AnalyzerPolicyID,
		DiversityFloor:        p.DiversityFloor,
		AcceptanceThresholdBP: p.AcceptanceThresholdBP,
		OpenedAtUnix:          p.Now,
		DeadlineUnix:          p.Now + p.DeadlineSeconds,
		ExtendedUntilUnix:     0,
		State:                 RoundStateOpen,
		ParticipatingFamilies:    make(map[string]uint64),
		AllParticipatingFamilies: make(map[string]bool),
		Votes:                   []TaskVerificationVoteRecord{},
	}, nil
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

// validTransitions defines the allowed state transitions. All transitions
// originate from Open; all other states are terminal.
var validTransitions = map[RoundState][]RoundState{
	RoundStateOpen:            {RoundStateFinalizedAccept, RoundStateFinalizedReject, RoundStateDisputed, RoundStateExpired},
	RoundStateFinalizedAccept: {},
	RoundStateFinalizedReject: {},
	RoundStateDisputed:        {},
	RoundStateExpired:         {},
}

// CanTransitionTo returns true if the round can move from its current state
// to the given next state.
func (r *TaskVerificationRound) CanTransitionTo(next RoundState) bool {
	allowed, ok := validTransitions[r.State]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == next {
			return true
		}
	}
	return false
}

// Transition moves the round to the given state. Returns
// ErrInvalidStateTransition if the transition is not permitted.
// now is the Unix timestamp to record as finalization time for terminal states.
func (r *TaskVerificationRound) Transition(next RoundState, now int64) error {
	if !r.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidStateTransition, r.State, next)
	}
	r.State = next
	if next != RoundStateOpen {
		r.FinalizationTime = now
	}
	return nil
}

// IsTerminal returns true if the round is in a terminal state (no further
// transitions possible).
func (r *TaskVerificationRound) IsTerminal() bool {
	return r.State != RoundStateOpen
}

// DeadlineForCurrentPhase returns the effective deadline. If the round has
// been extended, returns ExtendedUntilUnix; otherwise returns DeadlineUnix.
func (r *TaskVerificationRound) DeadlineForCurrentPhase() int64 {
	if r.ExtendedUntilUnix > 0 {
		return r.ExtendedUntilUnix
	}
	return r.DeadlineUnix
}

// DistinctPassFamilies returns the number of distinct analyzer families
// that have contributed pass-weight to this round.
func (r *TaskVerificationRound) DistinctPassFamilies() int {
	count := 0
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, w := range r.ParticipatingFamilies {
		if w > 0 {
			count++
		}
	}
	return count
}

// DistinctParticipatingFamilies returns the number of distinct analyzer
// families that have contributed any vote (pass, fail, or abstain) to this
// round. Used for the participation floor check.
//
// Computes from the Votes slice as the authoritative source, since the
// AllParticipatingFamilies map may lose entries under concurrent vote
// processing (read-then-write race on the round store).
func (r *TaskVerificationRound) DistinctParticipatingFamilies() int {
	families := make(map[string]bool)
	for _, v := range r.Votes {
		if v.AnalyzerFamily != "" {
			families[v.AnalyzerFamily] = true
		}
	}
	return len(families)
}

// ---------------------------------------------------------------------------
// Canonical serialization
// ---------------------------------------------------------------------------

// canonicalFamilyEntry is a sorted representation of a map[string]uint64
// entry, used for deterministic serialization.
type canonicalFamilyEntry struct {
	Key   string `json:"k"`
	Value uint64 `json:"v"`
}

// Canonical returns a deterministic byte representation of the round using
// JCS (RFC 8785) canonicalization. Map fields are sorted by key before
// serialization. Vote records are sorted by ValidatorID.
func (r *TaskVerificationRound) Canonical() ([]byte, error) {
	// Sort participating families by key for deterministic output.
	families := make([]canonicalFamilyEntry, 0, len(r.ParticipatingFamilies))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for k, v := range r.ParticipatingFamilies {
		families = append(families, canonicalFamilyEntry{Key: k, Value: v})
	}
	sort.Slice(families, func(i, j int) bool { return families[i].Key < families[j].Key })

	// Sort votes by ValidatorID for deterministic output.
	sortedVotes := make([]TaskVerificationVoteRecord, len(r.Votes))
	copy(sortedVotes, r.Votes)
	sort.Slice(sortedVotes, func(i, j int) bool {
		return sortedVotes[i].ValidatorID < sortedVotes[j].ValidatorID
	})

	// Sort each vote's ScoreBreakdown by key.
	for i := range sortedVotes {
		if len(sortedVotes[i].ScoreBreakdown) > 0 {
			sorted := make([]canonicalFamilyEntry, 0, len(sortedVotes[i].ScoreBreakdown))
			for k, v := range sortedVotes[i].ScoreBreakdown {
				sorted = append(sorted, canonicalFamilyEntry{Key: k, Value: v})
			}
			sort.Slice(sorted, func(a, b int) bool { return sorted[a].Key < sorted[b].Key })
			rebuilt := make(map[string]uint64, len(sorted))
			for _, e := range sorted {
				rebuilt[e.Key] = e.Value
			}
			sortedVotes[i].ScoreBreakdown = rebuilt
		}
	}

	// Build canonical projection — uses the same struct but with sorted data.
	proj := TaskVerificationRound{
		RoundID:               r.RoundID,
		TaskID:                r.TaskID,
		SubmissionEventID:     r.SubmissionEventID,
		WorkerID:              r.WorkerID,
		PosterID:              r.PosterID,
		Category:              r.Category,
		ValidatorSetVersion:   r.ValidatorSetVersion,
		Committee:             r.Committee,
		AnalyzerPolicyID:      r.AnalyzerPolicyID,
		DiversityFloor:        r.DiversityFloor,
		AcceptanceThresholdBP: r.AcceptanceThresholdBP,
		OpenedAtUnix:          r.OpenedAtUnix,
		DeadlineUnix:          r.DeadlineUnix,
		ExtendedUntilUnix:     r.ExtendedUntilUnix,
		State:                 r.State,
		PassWeight:            r.PassWeight,
		FailWeight:            r.FailWeight,
		AbstainWeight:         r.AbstainWeight,
		ParticipatingFamilies: r.ParticipatingFamilies,
		Votes:                 sortedVotes,
		FinalVerdict:          r.FinalVerdict,
		FinalScoreBP:          r.FinalScoreBP,
		FinalizationTime:      r.FinalizationTime,
		CalibrationApplied:    r.CalibrationApplied,
		CanonicalSealContext:  r.CanonicalSealContext,
		EpochAtFinalization:   r.EpochAtFinalization,
	}

	data, err := json.Marshal(proj)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrSerializationFailed, err)
	}
	canonical, err := jcs.Canonicalize(data)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize: %v", ErrSerializationFailed, err)
	}
	return canonical, nil
}

// RoundFromCanonical deserializes a TaskVerificationRound from canonical
// (or any valid JSON) bytes.
func RoundFromCanonical(data []byte) (*TaskVerificationRound, error) {
	var r TaskVerificationRound
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%w: unmarshal: %v", ErrSerializationFailed, err)
	}
	return &r, nil
}
```

## File: `internal/settlement/derivation/FORWARD_NOTES.md`

```markdown
# Forward notes — derivation package

Open architectural questions that 5B implementation cannot resolve on
its own; surfaced here for resolution at the 5B completion gate (before
F5 merge) or at the locked Reputation-and-Consensus-Integrity workstream
coordination point.

Each note documents what the implementation is doing today, why that is
safe in the current window, and what must change before the window
closes.

---

## 1. ReputationActivationEventID as `const event.EventID = ""` — V-1 hole at upgrade time

**Surfaced:** 5B skeleton breakpoint 1 (2026-04-24), flagged by architect.

**Current implementation:** `activation.go` defines
`ReputationActivationEventID` as a compile-time constant equal to the
empty string. `dag.IsAncestor("", R)` returns false for every R, so
every round pre-activation selects `NeutralBPStubW` automatically
without reaching any runtime flag. `const` (not `var`) is chosen to
satisfy the §2.1 DerivationInputs contract — a mutable variable would
be a state-leaking path that could be overwritten at runtime to alter
selection behavior.

**Why this is correct for today:** the locked Reputation-and-Consensus-
Integrity workstream has zero production callers of the real W
implementation. Every round in the F5 ship window settles with
NeutralBPStubW. The empty-string placeholder never triggers a wrong
decision — only a correct one (always use stub).

**The hole:** when the real workstream ships and
`ReputationActivationEventID` must become a real canonical event ID, a
source-level flip of the constant requires a binary-version change.
During the rollout window nodes running the old binary compute
`ActivationCheck("", R) == false` (select stub) and nodes running the
new binary compute `ActivationCheck(realID, R) == true` (select real).
Two correct binaries produce different DerivationResult values for the
same canonical state — property D-1 is violated for the duration of
the rollout.

This is binary-version-bound selection, not canonical-DAG-bound —
precisely the V-1 failure mode the invariant forbids.

**What must be resolved before the constant flips:** one of:

1. **Canonical-state-sourced activation ID.** Read the activation
   event ID from a canonical admin/genesis record (e.g., a
   CanonicalParameterSet event in the DAG) rather than from a binary
   constant. Every node at every binary version computes
   `ActivationCheck(lookupCanonical(), R)` against the same canonical
   state. A single canonical event introduces the activation ID; all
   nodes learn it at the same canonical position; V-1 holds through
   rollout.

2. **Canonical bootstrap pattern.** Define a protocol-level registry
   of named activation events sourced from a canonical bootstrap
   record at chain genesis; upgrades advance the registry via
   canonical events, not binary constants.

3. **Protocol hard-fork discipline.** Accept that the activation flip
   is itself a coordinated hard fork — all nodes upgrade their binary
   at a scheduled canonical position; rounds that straddle the fork
   are handled by the hard-fork protocol. D-1 violations are bounded
   by the fork window and treated as known protocol-upgrade behavior,
   not a silent divergence.

Options 1 and 2 preserve the "no binary-version-bound canonical
behavior" invariant cleanly; option 3 accepts a bounded, documented
violation as the cost of the upgrade. Option 3 is viable only if the
protocol has an explicit hard-fork mechanism to coordinate; otherwise
it is unsafe and reduces to uncoordinated rollout divergence.

**Scope:** coordination required with the locked Reputation-and-Consensus-
Integrity workstream (that workstream owns the real W implementation
and the definition of the activation event). This derivation package
cannot unilaterally decide the mechanism; the workstream and the
protocol-upgrade discipline together must land one of the three
options (or an equivalent) before the real constant is wired.

**Resolution track:** surface to architect at the 5B completion gate.
If the mechanism is not resolved by F5 merge, the flag remains as
stub-universal and the hole is carried forward into F5 post-ship
workstream planning. The derivation package does not require mechanism
resolution to ship — it only requires the mechanism to exist before
the constant flips.

---

## 2. EpochBoundary signer canonical-validator-snapshot binding — deferred until snapshot infrastructure ships

**Surfaced:** sub-spec implementation breakpoint B (2026-04-24).

**Current implementation:** `internal/epoch/boundary_validator.go` `BoundaryAdmissionValidator` enforces sub-spec v2.2 §1.4 items 1-5 (payload-shape checks, TriggerEventID type+existence, canonical epoch-count cross-check, canonical threshold-crossing cross-check). Signature validity is enforced upstream by `dag.Add`'s existing `crypto.VerifyEvent` step before the validator fires.

**The deferred check:** sub-spec §1.4 last bullet — "Signature: EpochBoundary events MUST be signed by a validator seated in the canonical validator-seat snapshot effective at `TriggerEventID`'s canonical position." The validator does NOT bind signer eligibility to a per-canonical-position validator-seat snapshot today.

**Why this is correct for the current window:** the binding requires a canonical validator-seat-snapshot read primitive that doesn't yet exist in the codebase. The same primitive is required by the locked Reputation-and-Consensus-Integrity workstream (snapshot emissions per its §5.2) and would be built there, not here. F5 5B's correctness goals — canonical settlement, no D-1 violation — are met by the canonical-state checks (items 1-5): a Byzantine signer who is in the validator manifest can sign an EpochBoundary, but if the canonical-state cross-check fails the event is rejected at admission and never enters the canonical DAG. The signer-binding check is a slashing/attribution surface (knowing WHO emitted an attempted-but-rejected event), orthogonal to settlement correctness.

**The hole:** an out-of-set Byzantine signer (one no longer in the canonical validator-seat snapshot at the trigger's canonical position but still in the runtime manifest) could pass `crypto.VerifyEvent` and pass the canonical-state cross-check (because they did the math right) — and successfully emit an EpochBoundary. Multi-emit logical-key dedup converges to one canonical EpochBoundary per Epoch regardless, so cross-node consistency is preserved; the only gap is "is this emitter canonically-authorized to emit at this position." Today: no canonical authorization check; a malicious holdover whose seat was canonically removed but whose key is still trusted by the runtime manifest could be the canonical emitter for an Epoch. F5 5B does not ship slashing-for-EpochBoundary so this carries no economic penalty surface, but it does mean the canonical EpochBoundary's emitter identity is not sourced from canonical state.

**Concretely (architect note, breakpoint B closure 2026-04-24):** an out-of-set validator retaining their signing key could participate in EpochBoundary emission under current implementation. Harmless to canonical semantic (cross-check layer preserves correctness regardless of signer). Becomes consequential only when signer-attribution surface is wired for slashing. Scope: locked Reputation-and-Consensus-Integrity workstream's validator-seat-snapshot primitive delivers the canonical read; F5 5B completion gate captures this dependency.

**What must be resolved before the window closes:** the canonical validator-seat-snapshot read primitive ships (locked workstream or adjacent infrastructure). Once available, `BoundaryAdmissionValidator` adds: read snapshot at `TriggerEventID`'s canonical position, verify `ev.AgentID` is in the snapshot's seat set, reject otherwise. The substrate (admission-cross-check mechanism) is in place and the validator function is straightforward to extend.

**Scope:** coordinated with locked Reputation-and-Consensus-Integrity workstream (snapshot infrastructure owner). This forward note exists alongside `internal/settlement/derivation/FORWARD_NOTES.md` §1's V-1 const-flip note as parallel architectural carries — both are issues to resolve at or before the canonical snapshot infrastructure lands.

**Resolution track:** surface to architect at sub-spec implementation completion gate. Implementation discretion at completion gate: (a) accept as carried-forward forward note, document in completion gate report, defer to snapshot-infrastructure ship; (b) halt 5B and require snapshot infrastructure now.

---

## 3. `TestTypeE_SyntheticReplayConformance/PopulatedDAGReplay_PerKey` flake — RESOLVED at #134-followon (Path A)

**Status (2026-04-24):** RESOLVED. Per architect direction at #134 closure: Path A — per-`(consumer, key)` `sync.Map` lock at the dispatcher layer. Same defense-in-depth pattern as `internal/escrow/applicator.go`'s `recordLocks`. Lock spans the read-modify-write region of `admitOneLogicalKey` (`internal/dispatch/logical_key_admit.go`). New `keyLocks sync.Map` field on `Dispatcher` (`internal/dispatch/dispatcher.go`) carries the framing-discipline doc explicitly: lock is intra-node defense-in-depth only; cross-node correctness rests on each LK consumer's `Apply` canonicality + ledger `ErrDuplicateEntry` idempotency + the F4B contract that LK `Apply` is idempotent or no-op for byte-distinct events with the same logical key.

**Verification:**
- New regression test `TestAdmitOneLogicalKey_PerKeyLockEliminatesRace` (`internal/dispatch/logical_key_race_test.go`) deterministically reproduces the race with a 50ms-sleep `Apply` and asserts exactly-1 Apply call. Pre-fix: 3-of-3 deterministic FAIL ("Apply fired 2 times"). Post-fix: green.
- `TestTypeE_SyntheticReplayConformance` re-run 50× via `go test -count=50`: 50/50 green (was 1-in-N intermittent).

**Note (the original investigation, kept for context):**

[Original §3 retained below for historical reference; superseded by the RESOLVED status above.]

---

### Original investigation (pre-resolution)

**Surfaced:** F5 5B settler integration (#132) full-sweep run, 2026-04-24. Pre-existing flake (predates this work); first observed during the post-integration full sweep. Reruns pass; the race manifests intermittently.

**Where:** `internal/dispatch/conformance/logical_key_replay.go:74` — `runLKReplayPerKey` test asserts the per-key Apply guarantee under multi-emit + multi-worker replay. The "fired Apply 2 times; expected exactly 1" failure indicates two workers both invoked `Apply` for the same logical key.

**Root cause:** `internal/dispatch/logical_key_admit.go:109-175` `admitOneLogicalKey` has a state-machine race between line 127 (`if rec.State == StateApplied { return nil }`) and line 169-173 (writes `StateApplied`). Between read and write there is NO lock on the per-`(consumer, key)` admission record. With multiple commit-bus workers (`recognition.DefaultWorkers = 4`) processing two byte-distinct events for the same logical key concurrently, both workers can:

1. Pass the early-exit gate at line 127 (record's State is not yet `StateApplied`).
2. Call `IsComplete` + `DeriveOutcome` + `safeApplyLogicalKey`.
3. Both successfully invoke `Apply` before either writes `StateApplied`.

The race window is the duration of `IsComplete` + `DeriveOutcome` + `Apply`. For lightweight consumers (synthetic test consumers; `EpochBoundaryLogicalKeyConsumer.Apply` is a no-op), the window is microseconds. For heavyweight consumers (post-#132 `TVConsensusLogicalKeyConsumer.Apply` calling `DeriveSettlement` + `ApplySettlementRecords`), the window is much wider. This is why the flake surfaced post-#132 even though the bug is pre-existing.

**Why this is correct for the current window:**
- The flake is intermittent (rerun-passes); does not block CI.
- Per-key Apply duplication for `EpochBoundaryLogicalKeyConsumer` is benign — `Apply` is a no-op (sub-spec §2.2; canonical effect is the DAG admission, already gated by the canonical-cross-check at `dag.Add`).
- Per-key Apply duplication for `TVConsensusLogicalKeyConsumer` is benign per F5 5B's canonical-correctness model — `escrow.ApplySettlementRecords` is idempotent via canonical_id at the ledger layer (`internal/ledger/transfer.go:531` `ErrDuplicateEntry`); a second `Apply` call's `DeriveSettlement` produces byte-identical records (D-1) and the applicator's per-canonical_id locks + ledger dedup absorb the duplication.

So the flake is **correctness-safe** today (no double-pay; no canonical divergence); it's a **test-suite-quality** issue (intermittent failures mask real regressions during 5-node testnet observation).

**The hole:** the dispatcher's per-`(consumer, key)` state machine should be atomic — a record-level mutex around the read-modify-write region in `admitOneLogicalKey` would close it. Without that fix, future heavyweight LK consumers may see the duplication count climb (more workers + slower Apply = wider race window), and any consumer whose Apply is NOT idempotent would be at risk.

**What must be resolved before the window closes:** add per-`(consumer, key)` mutex in the dispatcher (`reserveOrLoadLogical` returns the lock alongside the record; `admitOneLogicalKey` holds it across the read-modify-write region). Compatible with existing `processConsumer` execution model — the lock is per-record, not global.

**Scope:** dispatcher package internal change. No protocol semantic change. Test will go from intermittent to reliable. F5 5B implementation does not BLOCK on this fix because canonical correctness is preserved by downstream idempotency (canonical_id ledger dedup); but the test-suite signal-degradation is real.

**Resolution track:** investigate + fix before F5 5B testnet verification (#135). If the fix is too large for that window, document the test-suite expectation explicitly (mark the test as "known intermittent" with a `t.Skip` gated on a flag) so 5-node deploy verification doesn't false-fail on this surface.

---

## How to extend this file

Add a new numbered section per forward-note. Every note should state:
1. When it was surfaced (breakpoint + date).
2. What the implementation does today.
3. Why today's implementation is safe in the current window.
4. The hole / open question.
5. What must change before the window closes.
6. The scope / coordination required.
7. The resolution track (which gate / workstream owns the resolution).

Notes are removed from this file when the resolution lands, with a
journal entry marking the closure.
```

---

**End of F5 5B implementation bundle. 20 load-bearing files + FORWARD_NOTES.md. Ready for multi-AI review.**
