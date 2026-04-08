package settlement

import (
	"log/slog"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// DAGAncestorReader provides causal ancestor traversal for the Generation
// Ledger calculator. Satisfied by *dag.DAG.
type DAGAncestorReader interface {
	Get(id event.EventID) (*event.Event, error)
}

// RoyaltyRecipient is one ancestor's share of the Generation Ledger pool.
type RoyaltyRecipient struct {
	EventID  event.EventID
	AgentID  string
	Depth    int
	Weight   float64
	Amount   uint64
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
	qualityFn func(event.EventID) float64
}

// NewGenerationLedgerCalculator creates a calculator.
// qualityFn returns the Quality Score Q for an ancestor event. In prompt 07,
// this always returns 1.0 (neutral).
//
// TODO prompt 08: replace neutral qualityFn with real Quality Score lookup from
// the reputation store. The Q formula from paper v4.1 is:
//   Q = (α₁·CVD_norm + α₂·ChallengeSurvival + α₃·ReplicationRate + α₄·Consistency) / Σα
// Prompt 07 uses Q=1.0 for all ancestors (neutral). Prompt 08 wires the Consistency term;
// CVD_norm, ChallengeSurvival, and ReplicationRate come online in later prompts.
func NewGenerationLedgerCalculator(
	dag DAGAncestorReader,
	qualityFn func(event.EventID) float64,
) *GenerationLedgerCalculator {
	return &GenerationLedgerCalculator{
		dag:       dag,
		qualityFn: qualityFn,
	}
}

// Calculate computes the royalty distribution for a 2% pool from the budget.
// poolMicroAET is the total royalty pool (2% of budget, computed by caller).
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

		q := g.qualityFn(cur.id)
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
		dist.Recipients = append(dist.Recipients, RoyaltyRecipient{
			EventID: a.id,
			AgentID: a.agentID,
			Depth:   a.depth,
			Weight:  a.weight / totalWeight,
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
