package settlement

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/protocolmath"
)

// DAGAncestorReader provides causal ancestor traversal for the Generation
// Ledger calculator. Satisfied by *dag.DAG.
type DAGAncestorReader interface {
	Get(id event.EventID) (*event.Event, error)
}

// RoyaltyRecipient is one ancestor's share of the Generation Ledger pool.
//
// Weight semantics migrated from float64 "normalized fraction in [0,1]" to
// protocolmath.BasisPoints "share of pool in basis points" by commit-4 of
// the canonical-distribution-integer-migration workstream. Integer
// representation is byte-stable across hardware; the prior float was not.
// GenerationLedgerDistribution is not persisted — it lives only in the
// in-memory SettleResult return type — so this change is not a schema
// migration.
type RoyaltyRecipient struct {
	EventID event.EventID
	AgentID string
	Depth   int
	Weight  protocolmath.BasisPoints
	Amount  uint64
}

// GenerationLedgerDistribution is the computed royalty distribution.
type GenerationLedgerDistribution struct {
	Recipients []RoyaltyRecipient
	Treasury   uint64 // amount routed to treasury (empty ancestor set or rounding)
	Total      uint64 // should equal the full 2% pool
}

const (
	// GenerationLedgerMaxDepth is the maximum causal hop depth for royalties.
	// Non-configurable per the v4.1 economic model.
	GenerationLedgerMaxDepth = 3
)

// GenerationLedgerCalculator computes the royalty distribution for an
// accepted task. Traverses causal ancestors up to depth 3, applies
// inverse-square decay weighted by Quality Score, and normalizes to
// the full royalty pool.
type GenerationLedgerCalculator struct {
	dag       DAGAncestorReader
	qualityFn func(event.EventID) protocolmath.BasisPoints

	// mu guards shadowMode. Reads happen on every Calculate call; writes
	// happen rarely (once per IntegerMigration activation). See Part E
	// of the canonical-distribution-integer-migration workstream.
	mu         sync.RWMutex
	shadowMode bool
}

// isShadowMode returns the current shadow-mode flag under RLock.
func (g *GenerationLedgerCalculator) isShadowMode() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.shadowMode
}

// SetShadowMode switches the calculator between shadow mode (legacy
// float path canonical, integer path logged) and integer-canonical mode.
// Driven by the IntegerMigrationActivationConsumer; see SetShadowMode
// on *VerificationConsensusSettler for the twin method and the
// activation semantics.
func (g *GenerationLedgerCalculator) SetShadowMode(shadow bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.shadowMode = shadow
}

// NewGenerationLedgerCalculator creates a calculator.
//
// qualityFn returns the Quality Score Q for an ancestor event in basis
// points. Callers today pass a closure that returns NeutralBP for all
// ancestors (the pre-migration neutral 1.0 converted to basis points).
//
// TODO prompt 08: replace neutral qualityFn with real Quality Score lookup
// from the reputation store. Q formula from paper v4.1:
//
//	Q = (α₁·CVD_norm + α₂·ChallengeSurvival + α₃·ReplicationRate + α₄·Consistency) / Σα
//
// Prompt 07 uses Q = NeutralBP for all ancestors (neutral). Prompt 08
// wires the Consistency term; CVD_norm, ChallengeSurvival, and
// ReplicationRate come online in later prompts.
//
// shadowMode: pass true for Part B's shadow-compare behavior (canonical
// float path, integer path runs and is logged). Pass false to make the
// integer path canonical (Part E territory).
func NewGenerationLedgerCalculator(
	dag DAGAncestorReader,
	qualityFn func(event.EventID) protocolmath.BasisPoints,
	shadowMode bool,
) *GenerationLedgerCalculator {
	return &GenerationLedgerCalculator{
		dag:        dag,
		qualityFn:  qualityFn,
		shadowMode: shadowMode,
	}
}

// Calculate computes the royalty distribution for a 2% pool from the budget.
// poolMicroAET is the total royalty pool (2% of budget, computed by caller).
//
// Shadow-gated: when shadowMode is true, the legacy float path
// (calculateFloat) is canonical and the integer path (calculateInteger)
// runs alongside for shadow_delta logging. When shadowMode is false the
// integer path is the only path that runs.
//
// TODO future prompt: cycle detection. The v4.1 economic model specifies that
// reciprocal reference patterns (A→B in task 1, B→A in task 2) should be excluded
// from royalty distribution. This prevents collusion rings where agents cite each
// other to game royalties. Deferred from prompt 07 because it requires nontrivial
// graph analysis on the DAG's causal ancestry.
func (g *GenerationLedgerCalculator) Calculate(
	acceptedTaskEventID event.EventID,
	poolMicroAET uint64,
) GenerationLedgerDistribution {
	if g.isShadowMode() {
		floatDist := g.calculateFloat(acceptedTaskEventID, poolMicroAET)
		intDist := g.calculateInteger(acceptedTaskEventID, poolMicroAET)
		logGenLedgerShadowDelta(string(acceptedTaskEventID), floatDist, intDist, poolMicroAET)
		return floatDist
	}
	return g.calculateInteger(acceptedTaskEventID, poolMicroAET)
}

// calculateFloat is the legacy float-arithmetic royalty distribution,
// preserved verbatim from the pre-migration codebase. Still canonical while
// shadowMode remains true on every node.
//
// Because qualityFn now returns BasisPoints (commit-4 signature change),
// this function converts to float64 at the callsite to keep the pre-
// migration arithmetic unchanged. Remainder absorption matches the pre-
// migration behavior: the last-in-traversal-order ancestor absorbs.
func (g *GenerationLedgerCalculator) calculateFloat(
	acceptedTaskEventID event.EventID,
	poolMicroAET uint64,
) GenerationLedgerDistribution {
	if poolMicroAET == 0 || g.dag == nil {
		return GenerationLedgerDistribution{Treasury: poolMicroAET, Total: poolMicroAET}
	}

	// BFS traversal up to GenerationLedgerMaxDepth.
	type entry struct {
		id    event.EventID
		depth int
	}
	visited := make(map[event.EventID]struct{})
	visited[acceptedTaskEventID] = struct{}{}

	queue := []entry{}
	// Seed with direct parents of the accepted task event.
	ev, err := g.dag.Get(acceptedTaskEventID)
	if err != nil {
		return GenerationLedgerDistribution{Treasury: poolMicroAET, Total: poolMicroAET}
	}
	for _, ref := range ev.CausalRefs {
		if _, seen := visited[ref]; !seen {
			queue = append(queue, entry{id: ref, depth: 1})
			visited[ref] = struct{}{}
		}
	}

	// Collect ancestors with weights.
	type ancestor struct {
		id      event.EventID
		agentID string
		depth   int
		weight  float64
	}
	var ancestors []ancestor
	var totalWeight float64

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.depth > GenerationLedgerMaxDepth {
			continue
		}

		ancestorEv, err := g.dag.Get(cur.id)
		if err != nil {
			continue
		}

		// Convert BasisPoints back to float for the pre-migration arithmetic;
		// removed when the float path itself is removed.
		q := float64(g.qualityFn(cur.id)) / 10000.0
		w := (1.0 / float64(cur.depth*cur.depth)) * q
		ancestors = append(ancestors, ancestor{
			id:      cur.id,
			agentID: ancestorEv.AgentID,
			depth:   cur.depth,
			weight:  w,
		})
		totalWeight += w

		// Enqueue parents for deeper traversal.
		if cur.depth < GenerationLedgerMaxDepth {
			for _, ref := range ancestorEv.CausalRefs {
				if _, seen := visited[ref]; !seen {
					queue = append(queue, entry{id: ref, depth: cur.depth + 1})
					visited[ref] = struct{}{}
				}
			}
		}
	}

	// Empty ancestor set: full pool to treasury.
	if len(ancestors) == 0 || totalWeight == 0 {
		return GenerationLedgerDistribution{Treasury: poolMicroAET, Total: poolMicroAET}
	}

	// Normalize and distribute.
	dist := GenerationLedgerDistribution{Total: poolMicroAET}
	var allocated uint64
	for i, a := range ancestors {
		share := uint64(float64(poolMicroAET) * (a.weight / totalWeight))
		// Give rounding remainder to the last recipient.
		if i == len(ancestors)-1 {
			share = poolMicroAET - allocated
		}
		// Weight field: prior float semantics were "normalized fraction";
		// preserve that by scaling to basis points at the boundary so the
		// returned RoyaltyRecipient.Weight matches the post-migration
		// schema. This is the only non-verbatim change in the float path;
		// it affects the returned field only, not the arithmetic.
		normalizedBP := protocolmath.BasisPoints(int64(((a.weight / totalWeight) * 10000) + 0.5))
		dist.Recipients = append(dist.Recipients, RoyaltyRecipient{
			EventID: a.id,
			AgentID: a.agentID,
			Depth:   a.depth,
			Weight:  normalizedBP,
			Amount:  share,
		})
		allocated += share
	}

	// Safety: if allocated < pool due to edge case, remainder to treasury.
	if allocated < poolMicroAET {
		dist.Treasury = poolMicroAET - allocated
		slog.Debug("generation_ledger: rounding remainder to treasury",
			"remainder", dist.Treasury)
	}

	return dist
}

// calculateInteger is the integer-arithmetic royalty distribution introduced
// by the canonical-distribution-integer-migration workstream. Routes the
// final per-ancestor split through protocolmath.Allocate for determinism
// across hardware and cross-node agreement on the remainder-absorber.
//
// Per-ancestor weight: Q (BasisPoints, clamped to [0, MaxBasisPoints]) is
// divided by depth*depth (BasisPoints). At depth 1 with NeutralBP Q, the
// weight is 10000 (NeutralBP). At depth 2: 2500. At depth 3: 1111.
// Overflow-safe because all quantities fit in int64 with many orders of
// magnitude to spare.
func (g *GenerationLedgerCalculator) calculateInteger(
	acceptedTaskEventID event.EventID,
	poolMicroAET uint64,
) GenerationLedgerDistribution {
	if poolMicroAET == 0 || g.dag == nil {
		return GenerationLedgerDistribution{Treasury: poolMicroAET, Total: poolMicroAET}
	}

	type entry struct {
		id    event.EventID
		depth int
	}
	visited := make(map[event.EventID]struct{})
	visited[acceptedTaskEventID] = struct{}{}

	queue := []entry{}
	ev, err := g.dag.Get(acceptedTaskEventID)
	if err != nil {
		return GenerationLedgerDistribution{Treasury: poolMicroAET, Total: poolMicroAET}
	}
	for _, ref := range ev.CausalRefs {
		if _, seen := visited[ref]; !seen {
			queue = append(queue, entry{id: ref, depth: 1})
			visited[ref] = struct{}{}
		}
	}

	// ancestorRec retains everything we need to build the final
	// RoyaltyRecipient after protocolmath.Allocate runs.
	type ancestorRec struct {
		id      event.EventID
		agentID string
		depth   int
		weight  protocolmath.BasisPoints
	}
	var ancestors []ancestorRec

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.depth > GenerationLedgerMaxDepth {
			continue
		}
		ancestorEv, err := g.dag.Get(cur.id)
		if err != nil {
			continue
		}

		q := g.qualityFn(cur.id)
		if q < 0 {
			slog.Warn("generation_ledger: qualityFn returned negative; clamping to zero",
				"event_id", cur.id, "returned_bp", q)
			q = 0
		}
		if q > protocolmath.MaxBasisPoints {
			q = protocolmath.MaxBasisPoints
		}
		w := q / protocolmath.BasisPoints(cur.depth*cur.depth)

		ancestors = append(ancestors, ancestorRec{
			id:      cur.id,
			agentID: ancestorEv.AgentID,
			depth:   cur.depth,
			weight:  w,
		})

		if cur.depth < GenerationLedgerMaxDepth {
			for _, ref := range ancestorEv.CausalRefs {
				if _, seen := visited[ref]; !seen {
					queue = append(queue, entry{id: ref, depth: cur.depth + 1})
					visited[ref] = struct{}{}
				}
			}
		}
	}

	if len(ancestors) == 0 {
		return GenerationLedgerDistribution{Treasury: poolMicroAET, Total: poolMicroAET}
	}

	// Build protocolmath recipients keyed by event ID bytes (stable across
	// nodes — event IDs are content-addressed).
	pm := make([]protocolmath.Recipient, 0, len(ancestors))
	for _, a := range ancestors {
		pm = append(pm, protocolmath.Recipient{
			CanonicalKey: []byte(a.id),
			Weight:       a.weight,
		})
	}
	result, err := protocolmath.Allocate(pm, protocolmath.MicroAET(poolMicroAET))
	if err != nil {
		// Impossible in practice: ancestors list is non-empty, weights are
		// pre-clamped non-negative, and DAG event IDs are unique. Log loudly
		// and route the full pool to treasury rather than stall.
		slog.Error("generation_ledger: protocolmath returned error; full pool to treasury",
			"err", err, "ancestor_count", len(ancestors), "pool", poolMicroAET)
		return GenerationLedgerDistribution{Treasury: poolMicroAET, Total: poolMicroAET}
	}

	// Total weight (for normalized-weight field computation).
	var totalWeight int64
	for _, a := range ancestors {
		totalWeight += int64(a.weight)
	}

	dist := GenerationLedgerDistribution{Total: poolMicroAET}
	var allocated uint64
	for _, a := range ancestors {
		amount := uint64(result[string(a.id)])
		var normalizedBP protocolmath.BasisPoints
		if totalWeight > 0 {
			normalizedBP = protocolmath.BasisPoints((int64(a.weight) * 10000) / totalWeight)
		}
		dist.Recipients = append(dist.Recipients, RoyaltyRecipient{
			EventID: a.id,
			AgentID: a.agentID,
			Depth:   a.depth,
			Weight:  normalizedBP,
			Amount:  amount,
		})
		allocated += amount
	}

	// Conservation: protocolmath.Allocate guarantees sum == pool, so
	// Treasury stays zero on the happy path. Safety net left in place.
	if allocated < poolMicroAET {
		dist.Treasury = poolMicroAET - allocated
	}
	return dist
}

// logGenLedgerShadowDelta emits one line comparing float and integer
// distributions for a generation-ledger Calculate invocation. Canonical
// format matches plan §4.5. Elevates to Warn on any conservation failure.
func logGenLedgerShadowDelta(
	taskEventID string,
	floatDist, intDist GenerationLedgerDistribution,
	pool uint64,
) {
	var sumFloat, sumInt uint64
	for _, r := range floatDist.Recipients {
		sumFloat += r.Amount
	}
	sumFloat += floatDist.Treasury
	for _, r := range intDist.Recipients {
		sumInt += r.Amount
	}
	sumInt += intDist.Treasury

	// Build a lookup to compute per-recipient deltas. Keyed by EventID.
	floatMap := make(map[event.EventID]uint64, len(floatDist.Recipients))
	for _, r := range floatDist.Recipients {
		floatMap[r.EventID] = r.Amount
	}
	var maxAbs uint64
	for _, r := range intDist.Recipients {
		f := floatMap[r.EventID]
		var d uint64
		if f > r.Amount {
			d = f - r.Amount
		} else {
			d = r.Amount - f
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
	slog.Log(context.Background(), level, "shadow_delta",
		"context", "generation_ledger",
		"task_id", taskEventID,
		"recipient_count", len(intDist.Recipients),
		"float_sum", sumFloat,
		"int_sum", sumInt,
		"sum_delta", sumDelta,
		"max_per_recipient_delta", maxAbs,
		"pool", pool,
	)
}
