package cross_node

import (
	"context"
	"os"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/verification/cross_node/testnetwork"
)

// TestTiedWeightCorpus_ThroughDispatcherLK_Converges is the F4B §5.2.1
// GREEN gate. It re-runs the tied-weight corpus through the dispatcher
// with the NEW logical-key admission consumer (TVConsensusLogicalKeyConsumer)
// registered instead of the F3-B content-hash consumer. The cluster
// MUST converge on a single canonical verdict per round.
//
// Why this test MUST pass on F4B's fixed code path:
//
// Under logical-key admission, the dispatcher's admission-record key
// for two byte-distinct TVConsensus events with the same RoundID is
// IDENTICAL: LogicalAdmissionKey("tv_consensus_settlement_lk", RoundID).
// The per-(consumer, key) Apply guarantee means Apply fires exactly
// ONCE per RoundID per node. The consumer's DeriveOutcome computes
// the canonical verdict from round.Votes (cluster-uniform because all
// nodes share the same vote set via the harness's SetupTask). Apply
// settles with the derived verdict — not with the triggering event's
// advisory FinalVerdict. Per-node fastpath arrival order becomes
// irrelevant; verdicts match across nodes; per-validator payouts
// converge; cluster ledgers are byte-equal.
//
// If this test FAILS with a divergence, the F4B fix is not landing
// correctly. Per the task's halt-trigger, the founder has identified
// two likely causes:
//
//   (a) IsComplete firing before the outcome is mathematically
//       sealed (pass_weight / fail_weight threshold not met OR
//       gap closable by remaining votes).
//   (b) DeriveOutcome not using the full canonical vote set
//       (e.g., computing verdict from the triggering event's
//       FinalVerdict instead of from round.Votes).
//
// Both would produce per-node divergent Outcomes → per-node
// divergent settlements → the same ledger fork F4B exists to close.
//
// HOW THIS TEST IS USED:
//
//   - Capture mode (CROSS_NODE_HARNESS_CAPTURE=1) required: the
//     harness is expensive and the RED variants above this file
//     gate on the same env var for symmetry. Matches the F4B
//     completion-gate pattern.
//   - On GREEN: AssertByteEquality passes silently. Post-F4B-§5.2.1,
//     this test becomes a regression guard for the logical-key
//     admission path.
//   - On RED: the failure message names the diverging nodes and
//     attaches a per-node diff. See halt-trigger handling in the
//     task brief for what to do.
func TestTiedWeightCorpus_ThroughDispatcherLK_Converges(t *testing.T) {
	if os.Getenv("CROSS_NODE_HARNESS_CAPTURE") != "1" {
		t.Skip("run with CROSS_NODE_HARNESS_CAPTURE=1 to exercise the full tied-weight harness via the F4B logical-key admission path")
	}

	const nodeCount = 3
	cluster := NewCluster(t, nodeCount)

	// CRITICAL: register the logical-key consumer on every node
	// BEFORE driving events. installDispatcherLK replaces the
	// content-hash wiring installed by newNode with a fresh
	// dispatcher that has ONLY the LK consumer registered — the
	// F4B-exclusive migration shape per the cmd/node registration
	// audit §3.
	installDispatcherLK(t, cluster)

	// The LK consumer's IsComplete reads active validator weight
	// via the package-level activeWeightForHarness atomic; set it
	// to the cluster's total stake (3 validators * stake 100 each).
	// Without this, IsComplete returns false on every Admit → Apply
	// never fires → the test asserts byte-equality on untouched
	// ledgers (misleading PASS). SetActiveWeightForHarness makes
	// the dependency loud.
	SetActiveWeightForHarness(uint64(nodeCount) * 100)
	t.Cleanup(func() { SetActiveWeightForHarness(0) })

	scenarios := TiedWeightCorpus(cluster)
	if scenarios == nil {
		t.Fatal("TiedWeightCorpus: nil — cluster too small")
	}
	transport := testnetwork.New()
	ctx := context.Background()

	cluster.FundPoster(t, 1_000_000_000_000)

	for _, s := range scenarios {
		cluster.SetupTask(t, s)
	}
	for _, s := range scenarios {
		runScenarioViaDispatcherLK(t, cluster, transport, ctx, s)
	}

	inputs := snapshotInputsFor(cluster, scenarios)
	snaps := SnapshotCluster(cluster, inputs)

	// On F4B's fixed code path (logical-key admission), this MUST
	// pass. If it fails, halt per the task brief — do NOT iterate
	// silently.
	AssertByteEquality(t, snaps)
}

// runScenarioViaDispatcherLK mirrors runScenarioViaDispatcher
// (dispatcher_tied_weight_test.go) but is spelled separately so the
// GREEN test's call graph is trivially auditable side-by-side with
// the RED tests. Both call the SAME node.AdmitViaDispatcher — the
// dispatcher's internal routing decides content-hash vs logical-key
// based on which consumers are registered, and installDispatcherLK
// ensures only the LK consumer is on.
//
// Per-node delivery order is honored exactly as the RED tests
// specify; F4B's correctness property is precisely that arrival
// order becomes irrelevant because Apply collapses to the canonical
// verdict. If this helper were to reshape scenarios, it would weaken
// the test's validation of the F4B invariant.
func runScenarioViaDispatcherLK(
	t *testing.T,
	cluster *Cluster,
	transport *testnetwork.Transport,
	ctx context.Context,
	scenario *TaskScenario,
) {
	t.Helper()

	evs := make([]*event.Event, len(scenario.Emits))
	for i, em := range scenario.Emits {
		producer := cluster.Nodes[em.ProducerIdx]
		evs[i] = MakeConsensusEvent(t, producer, scenario.TaskID, scenario.RoundID, em.Verdict, em.ScoreBP)
	}

	for nodeIdx, node := range cluster.Nodes {
		order := scenario.effectiveDeliveryOrder(nodeIdx)
		ordered := make([]*event.Event, len(order))
		for i, idx := range order {
			ordered[i] = evs[idx]
		}
		transport.DeliverInOrderViaDispatcher(ctx, node, ordered)
	}
}
