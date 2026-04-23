package cross_node

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/verification/cross_node/testnetwork"
)

// TestTiedWeightCorpus_ThroughDispatcher_ReproducesDivergence is the
// dispatcher-integrated companion to TestTiedWeightCorpus_ReproducesDivergence
// (see tied_weight_test.go). It exists to validate that the harness can
// reproduce the cross-node selection-race divergence with the production
// dispatch.Dispatcher in the middle, not just at the bare consumer.Apply
// boundary.
//
// Logic chain (from F4A completion gate report §6):
//
//   - F4B's protocol fix is logical-key admission, which operates AT the
//     dispatcher layer.
//   - F4A's harness invokes TVConsensusConsumer.Apply directly. That
//     proves the bug exists and reproduces it deterministically, but
//     bypasses the surface F4B's fix will modify.
//   - Therefore: a dispatcher-integrated harness extension is required
//     to validate F4B's fix end-to-end. It must FIRST reproduce the
//     SAME divergence on the current (unfixed) code path — otherwise
//     F4B's fix at the dispatcher layer wouldn't actually be addressing
//     a surface we can observe the bug at.
//
// Why this test is expected to fail on current code:
//
// The dispatcher today admits events by content-hash. Two byte-distinct
// TaskVerificationConsensus events for the same RoundID (e.g., one with
// FinalVerdict="pass" authored by validator-0 and one with
// FinalVerdict="fail" authored by validator-1) compute DIFFERENT
// admission keys, get DIFFERENT admission records, and produce TWO
// invocations of TVConsensusConsumer.Apply per node. The dispatcher's
// existing admission state machine does not dedupe by RoundID. So the
// per-task selection race in TVConsensusConsumer.Apply (the per-task
// mutex at tv_consensus_consumer.go:63-66 — first event past the mutex
// wins; subsequent events for the same task short-circuit on the
// task-status-terminal pre-check) still fires PER NODE. Per-node
// fastpath arrival order — modeled here by per-node DeliveryOrders —
// determines which event wins on each node. Different verdicts settle on
// different nodes. Per-validator payouts diverge. Cluster ledger forks.
//
// What F4B's fix does to make this test PASS:
//
// Logical-key admission. Two TaskVerificationConsensus events for the
// same RoundID will compute the SAME admission key (the logical key
// derived from RoundID, not the content hash). The dispatcher's
// exactly-once guarantee per admission key means the SECOND event
// finds a StateApplied (or in-flight) record and short-circuits at the
// admission layer — Apply is invoked exactly once per RoundID across
// all nodes, with the canonical winning verdict (selected by F4B's
// canonical-tiebreak rule, not per-node arrival order). Cluster
// converges; this test passes.
//
// HOW THIS TEST IS USED:
//
//   - In default mode (CROSS_NODE_HARNESS_CAPTURE unset), this test is
//     SKIPPED. The skip message references the captured baseline
//     divergence in testdata/tied_weight_divergence_through_dispatcher_baseline.txt
//     — that file IS the harness validation evidence for the
//     dispatcher-integrated path.
//   - The capture mode (CROSS_NODE_HARNESS_CAPTURE=1) lifts the skip and
//     writes a fresh baseline. The F4B step 0.1 author runs this once at
//     harness extension to produce the baseline; the F4B fix author
//     re-runs it to confirm the fix collapses divergence to zero (at
//     which point this test will be permanently unskipped and assert
//     PASS).
//
// CRITICAL: if this test does NOT reproduce divergence on the current
// code path, that is a HALT trigger for F4B per the F4A gate report §6 —
// it would mean the bug is not actually at the dispatcher layer (which
// contradicts F4 plan v2 §5's premise) or that some other surface is
// silently deduping. Either way, F4B's logical-key admission fix would
// NOT be fixing the production bug, and the F4 plan needs revision.
func TestTiedWeightCorpus_ThroughDispatcher_ReproducesDivergence(t *testing.T) {
	t.Parallel()

	captureMode := os.Getenv("CROSS_NODE_HARNESS_CAPTURE") == "1"

	if !captureMode {
		t.Skip("pending F4B logical-key admission fix; this gate flips RED → GREEN when F4B lands. " +
			"Harness validated to reproduce bug through dispatcher; baseline divergence captured in " +
			"testdata/tied_weight_divergence_through_dispatcher_baseline.txt. " +
			"Re-run with CROSS_NODE_HARNESS_CAPTURE=1 to regenerate the baseline.")
	}

	const nodeCount = 3
	cluster := NewCluster(t, nodeCount)
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
		runScenarioViaDispatcher(t, cluster, transport, ctx, s)
	}

	inputs := snapshotInputsFor(cluster, scenarios)
	snaps := SnapshotCluster(cluster, inputs)

	// Capture mode: write the divergence diff to the dispatcher-variant
	// baseline fixture before asserting (so the fixture survives even if
	// AssertByteEquality triggers t.Fatalf). The fixture is the concrete
	// evidence that the harness reproduces the documented bug class via
	// the dispatcher — the gate F4B's fix must close.
	diff := formatDivergenceDiff(snaps)
	baseline := filepath.Join("testdata", "tied_weight_divergence_through_dispatcher_baseline.txt")
	if err := os.MkdirAll(filepath.Dir(baseline), 0o755); err != nil {
		t.Fatalf("create testdata dir: %v", err)
	}
	if err := os.WriteFile(baseline, []byte(diff), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	t.Logf("dispatcher-integrated baseline divergence captured to %s", baseline)

	// On the unfixed code path (content-hash admission), this MUST fail.
	// On F4B's fixed code path (logical-key admission), this MUST pass.
	AssertByteEquality(t, snaps)
}

// runScenarioViaDispatcher is the dispatcher-integrated parallel of
// runScenario in cluster_test.go. Constructs the same TVConsensus events
// from the scenario's Emits, then delivers them to each node in that
// node's specified order via Transport.DeliverInOrderViaDispatcher.
//
// Kept separate from runScenario so the existing direct-Apply tests are
// unaffected. The two helpers share the event-construction loop verbatim
// (deliberate duplication rather than shared helper to keep both paths
// trivially auditable side-by-side).
func runScenarioViaDispatcher(
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
