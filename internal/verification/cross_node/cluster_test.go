package cross_node

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/verification/cross_node/testnetwork"
)

// TestTrivialCorpus_UniformSettlement is the harness's positive sanity
// check: with a single TVConsensus event per round, identical on every
// emitter, delivered in identical order to every node, every node MUST
// project the same ledger state.
//
// If this test fails on the unfixed code path, the harness itself is
// broken — there is no per-node ordering variance and no payload
// variance to drive a divergence.
func TestTrivialCorpus_UniformSettlement(t *testing.T) {
	t.Parallel()

	const nodeCount = 3
	cluster := NewCluster(t, nodeCount)
	scenarios := TrivialCorpus(cluster)
	transport := testnetwork.New()
	ctx := context.Background()

	// One-shot poster funding (FundAgent is content-addressed; can only
	// be called once per (agent, amount) pair on a node).
	cluster.FundPoster(t, 1_000_000_000_000)

	// Provision tasks identically on every node.
	for _, s := range scenarios {
		cluster.SetupTask(t, s)
	}

	// Drive each scenario.
	for _, s := range scenarios {
		runScenario(t, cluster, transport, ctx, s)
	}

	// Snapshot every node and assert byte-equality across the cluster.
	inputs := snapshotInputsFor(cluster, scenarios)
	snaps := SnapshotCluster(cluster, inputs)
	AssertByteEquality(t, snaps)

	// Conservation sanity: every node's total supply should be non-zero
	// (the cluster has done some accounting) and identical across nodes
	// (which AssertByteEquality already verifies).
	for _, snap := range snaps {
		if snap.TotalSupply == 0 {
			t.Errorf("node %d: total supply is zero — settlement may not have run", snap.NodeIndex)
		}
	}
}

// runScenario executes a single TaskScenario end-to-end: builds the
// TVConsensus events from the scenario's Emits, then delivers them to
// each node in that node's specified order via the transport.
func runScenario(
	t *testing.T,
	cluster *Cluster,
	transport *testnetwork.Transport,
	ctx context.Context,
	scenario *TaskScenario,
) {
	t.Helper()

	// Construct each emitted event once. The emitter's ValidatorID is
	// recorded as event.AgentID; distinct emitters with distinct
	// (verdict, score) combinations produce distinct event IDs.
	evs := make([]*event.Event, len(scenario.Emits))
	for i, em := range scenario.Emits {
		producer := cluster.Nodes[em.ProducerIdx]
		evs[i] = MakeConsensusEvent(t, producer, scenario.TaskID, scenario.RoundID, em.Verdict, em.ScoreBP)
	}

	// Deliver per node in that node's specified order.
	for nodeIdx, node := range cluster.Nodes {
		order := scenario.effectiveDeliveryOrder(nodeIdx)
		ordered := make([]*event.Event, len(order))
		for i, idx := range order {
			ordered[i] = evs[idx]
		}
		transport.DeliverInOrder(ctx, node, ordered)
	}
}
