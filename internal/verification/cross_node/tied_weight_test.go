package cross_node

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification/cross_node/testnetwork"
)

// TestTiedWeightCorpus_ReproducesDivergence is the harness's CRITICAL
// validation against the F4 selection-race bug class. It must FAIL on
// the current (unfixed) code path with cross-node ledger divergence,
// proving the harness can detect the bug F4B exists to fix.
//
// Bug mechanism (from
// docs/plans/implementation/selection-race-characterization.md §1-§3):
//
//  1. Multiple validators independently emit TaskVerificationConsensus
//     events with potentially different FinalVerdict for the same
//     RoundID, based on each node's local view of accumulated votes
//     at the moment of finalization.
//  2. All such events propagate cluster-wide; the DAG converges
//     byte-equally on every node.
//  3. On each node, TVConsensusConsumer.Apply (per-task mutex at
//     internal/dispatch/tv_consensus_consumer.go:63-66) lets the FIRST
//     event past the mutex win locally. Subsequent events for the same
//     task short-circuit on the task-status-terminal pre-check.
//  4. The "first event" is determined by per-node fastpath arrival
//     order. Different nodes admit different events first → different
//     verdicts settle → different per-validator payouts → ledger forks.
//
// The harness reproduces this by:
//   - Provisioning the same task on every node with a half-pass /
//     half-fail vote split (vote-weight tie).
//   - Emitting a "pass"-verdict TVConsensus event from validator 0 and
//     a "fail"-verdict TVConsensus event from validator 1.
//   - Delivering the events to even-indexed nodes in [pass, fail] order
//     and to odd-indexed nodes in [fail, pass] order.
//
// Result on the unfixed code path:
//   - Even nodes: settle "pass" → worker paid, validator 0 paid,
//     treasury gets accept-share.
//   - Odd nodes: settle "fail" → poster refunded, validators 1 and 2
//     paid, treasury gets reject-share.
//
// AssertByteEquality must fail. Per the F4 plan §2.1.1, this failure
// is the harness's self-test.
//
// HOW THIS TEST IS USED:
//
//   - Until F4B's logical-key admission fix lands, this test is SKIPPED
//     in CI (see t.Skip at the top of the function). The skip message
//     references the captured baseline divergence in
//     testdata/tied_weight_divergence_baseline.txt — that file IS the
//     harness validation evidence.
//   - The capture mode is enabled by setting the env var
//     CROSS_NODE_HARNESS_CAPTURE=1, which lifts the skip and writes a
//     fresh baseline. Used by the F4A author once at harness creation,
//     and again by the F4B fix author to confirm the fix collapses
//     divergence to zero.
//   - When F4B lands, this test will be unskipped permanently and is
//     expected to PASS — divergence will be gone.
func TestTiedWeightCorpus_ReproducesDivergence(t *testing.T) {
	t.Parallel()

	captureMode := os.Getenv("CROSS_NODE_HARNESS_CAPTURE") == "1"

	if !captureMode {
		t.Skip("pending F4B logical-key admission fix — harness validated to reproduce bug; " +
			"baseline divergence captured in testdata/tied_weight_divergence_baseline.txt. " +
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
		runScenario(t, cluster, transport, ctx, s)
	}

	inputs := snapshotInputsFor(cluster, scenarios)
	snaps := SnapshotCluster(cluster, inputs)

	// Capture mode: write the divergence diff to the baseline fixture
	// before asserting (so the fixture survives even if AssertByteEquality
	// triggers t.Fatalf). The fixture is the concrete evidence that the
	// harness reproduces the documented bug class.
	diff := formatDivergenceDiff(snaps)
	baseline := filepath.Join("testdata", "tied_weight_divergence_baseline.txt")
	if err := os.MkdirAll(filepath.Dir(baseline), 0o755); err != nil {
		t.Fatalf("create testdata dir: %v", err)
	}
	if err := os.WriteFile(baseline, []byte(diff), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	t.Logf("baseline divergence captured to %s", baseline)

	// On the unfixed code path, this MUST fail.
	AssertByteEquality(t, snaps)
}
