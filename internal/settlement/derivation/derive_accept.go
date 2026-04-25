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
		ancestors, readErr := ReadAtAnchor(inputs.dagReader, cutoff.anchor, round.SubmissionEventID, MaxGenLedgerDepth)
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
		//
		// **Defensive seed-inclusion assertion (Grok Fix #3, 2026-04-25)**:
		// the calculator's [1:] strip below is canonical only if BFS
		// returned the seed at index 0. If a future BFS regression returns
		// seed-exclusive output, this panic fires before silently-wrong
		// gen-ledger payouts ship.
		if len(ancestors) > 0 && ancestors[0].EventID != round.SubmissionEventID {
			panic(fmt.Sprintf("derivation: seed-inclusion contract violation — ReadAtAnchor returned [0]=%s, expected SubmissionEventID=%s",
				ancestors[0].EventID, round.SubmissionEventID))
		}

		// Strip the seed (index 0) — the gen-ledger calculator does NOT
		// pay the submission event's author as a gen-ledger ancestor of
		// their own submission. Per pre-5B convention, gen-ledger payouts
		// begin at depth=1.
		var ancestorList []AncestorWithDepth
		if len(ancestors) > 1 {
			ancestorList = ancestors[1:]
		}

		// Multi-AI Fix #1 (2026-04-25): use the BFS-tracked depth from
		// ReadAtAnchor's anchor-scoped traversal directly. Replaces the
		// prior `computeDepths` helper that ran an UNRESTRICTED BFS over
		// all CausalRefs — which produced shorter out-of-scope-path
		// depths and broke D-1 across nodes whose canonical state
		// included DAG topologies with both in-scope and out-of-scope
		// paths to the same ancestor. Single source of truth: BFS that
		// produced membership also produced the depth.

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
			for _, anc := range ancestorList {
				ev, getErr := inputs.dagReader.Get(anc.EventID)
				if getErr != nil {
					if errors.Is(getErr, dag.ErrEventNotFound) {
						return nil, StatusDeferred, DeferredCauseDAGAncestorBFS, summary, nil
					}
					return nil, 0, 0, summary, fmt.Errorf("derivation: ancestor Get %s: %w", anc.EventID, getErr)
				}
				q, qErr := qualityImpl.Lookup(anc.EventID, cutoff.epoch)
				if qErr != nil {
					if errors.Is(qErr, dag.ErrEventNotFound) {
						return nil, StatusDeferred, DeferredCauseQualityLookup, summary, nil
					}
					return nil, 0, 0, summary, fmt.Errorf("derivation: Quality.Lookup %s: %w", anc.EventID, qErr)
				}
				if q < 0 {
					q = 0
				}
				if q > protocolmath.MaxBasisPoints {
					q = protocolmath.MaxBasisPoints
				}
				depth := anc.Depth
				if depth <= 0 {
					depth = 1 // defensive — seed-strip should ensure depth >= 1
				}
				w := q / protocolmath.BasisPoints(depth*depth)
				ws = append(ws, weighted{
					id:      anc.EventID,
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
