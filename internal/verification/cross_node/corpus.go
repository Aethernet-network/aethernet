// Corpus generators for the cross-node byte-equality harness.
//
// A corpus is a set of TaskScenarios that the harness drives end-to-end
// through the cluster. Each scenario describes:
//
//   - The task identity (TaskID, Budget, RoundID).
//   - The vote set the round will record (Votes), shared across all
//     nodes — votes are canonical events; in production they would have
//     been ingested DAG-converged before TVConsensus is emitted.
//   - The TVConsensus events to inject (Emits) and the per-node delivery
//     order (DeliveryOrders) that drives the dispatcher.
//
// The harness ships two corpora:
//
//   - TrivialCorpus: every emitter agrees; one verdict per round; uniform
//     delivery order. The cluster MUST converge.
//   - TiedWeightCorpus: validators emit conflicting verdicts; per-node
//     delivery order is chosen so different nodes see different events
//     first. On the unfixed code path, this MUST diverge — the harness's
//     own self-test that it can detect the bug F4B exists to fix.

package cross_node

import (
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/taskverification"
)

// EmitSpec describes one TaskVerificationConsensus event to be created
// for a scenario. The producer is the index of the validator (and thus
// the node) authoring the event; the verdict and score are written into
// the event payload.
//
// In production, every node's deadline checker and vote consumer can
// independently emit a TVConsensus event for the same round with that
// node's local view of FinalVerdict. Multiple emits per round are the
// norm; this struct is the harness's explicit model of one such emit.
type EmitSpec struct {
	ProducerIdx int    // index in Cluster.Nodes of the authoring validator
	Verdict     string // "pass" | "fail" | "abstain"
	ScoreBP     uint64 // FinalScoreBP recorded in payload
}

// TaskScenario captures everything the harness needs to drive a single
// task end-to-end. SetupTask uses TaskID/Budget/RoundID/Votes; the
// transport uses Emits and DeliveryOrders.
type TaskScenario struct {
	// TaskID is the canonical task identity. Used as the escrow bucket
	// key and as the consumer's per-task mutex key.
	TaskID string

	// RoundID is the canonical verification-round identity. Multiple
	// TVConsensus events for the same round have the same RoundID; the
	// per-task selection race observes this through round.Votes lookup.
	RoundID string

	// Budget is the task's escrowed budget in micro-AET.
	Budget uint64

	// Votes is the canonical vote set saved into every node's round
	// store. In production this set is ingested via DAG votes and is
	// cluster-uniform; the harness mirrors that.
	Votes []taskverification.TaskVerificationVoteRecord

	// Emits is the set of TVConsensus events that will be created and
	// delivered. The order in this slice is logical (which validator
	// produced which event); per-node delivery order is given by
	// DeliveryOrders.
	Emits []EmitSpec

	// DeliveryOrders[nodeIdx] is the order in which Emits indices are
	// delivered to node nodeIdx. The length of each inner slice equals
	// len(Emits). Distinct DeliveryOrders[i] across nodes is what
	// reproduces the per-node first-past-the-mutex selection race.
	//
	// If nil, every node receives Emits in declaration order.
	DeliveryOrders [][]int
}

// effectiveDeliveryOrder returns the order in which node nodeIdx should
// receive Emits. Falls back to declaration order when DeliveryOrders is
// nil or short.
func (s *TaskScenario) effectiveDeliveryOrder(nodeIdx int) []int {
	if s.DeliveryOrders == nil || nodeIdx >= len(s.DeliveryOrders) {
		out := make([]int, len(s.Emits))
		for i := range out {
			out[i] = i
		}
		return out
	}
	return s.DeliveryOrders[nodeIdx]
}

// uniformVotes constructs a vote set where each cluster validator
// records the same verdict and score. Used by TrivialCorpus.
func uniformVotes(validators []crypto.AgentID, verdict taskverification.Verdict, scoreBP uint64) []taskverification.TaskVerificationVoteRecord {
	out := make([]taskverification.TaskVerificationVoteRecord, 0, len(validators))
	for _, v := range validators {
		out = append(out, taskverification.TaskVerificationVoteRecord{
			ValidatorID:    v,
			Verdict:        verdict,
			ScoreBP:        scoreBP,
			AnalyzerFamily: "heuristic",
			Stake:          100,
		})
	}
	return out
}

// splitVotes constructs a vote set where the first half of validators
// vote pass and the second half vote fail. Used by TiedWeightCorpus —
// when the round has a mix of pass/fail votes, the agreeing-validator
// pool diverges between settling for "pass" (pays the pass-voters) and
// settling for "fail" (pays the fail-voters), making cross-node
// divergence visible in the per-validator balance comparison.
//
// Stakes are equal (100 each), so two halves of equal size produce
// numerically tied vote weights. The harness does not implement a
// real supermajority finalizer; it just builds the round state the
// settler will read.
func splitVotes(validators []crypto.AgentID) []taskverification.TaskVerificationVoteRecord {
	out := make([]taskverification.TaskVerificationVoteRecord, 0, len(validators))
	half := len(validators) / 2
	for i, v := range validators {
		verdict := taskverification.VerdictPass
		family := "heuristic"
		if i >= half {
			verdict = taskverification.VerdictFail
			family = "embedding"
		}
		out = append(out, taskverification.TaskVerificationVoteRecord{
			ValidatorID:    v,
			Verdict:        verdict,
			ScoreBP:        7000,
			AnalyzerFamily: family,
			Stake:          100,
		})
	}
	return out
}

// TrivialCorpus returns a 10-task scenario set where every emit carries
// the same verdict ("pass") and every node receives them in the same
// order. The cluster MUST converge. Used as a harness sanity test.
func TrivialCorpus(c *Cluster) []*TaskScenario {
	validators := c.AllValidators()
	scenarios := make([]*TaskScenario, 10)
	for i := 0; i < 10; i++ {
		taskID := fmt.Sprintf("trivial-%02d", i)
		roundID := fmt.Sprintf("trivial-round-%02d", i)
		scenarios[i] = &TaskScenario{
			TaskID:  taskID,
			RoundID: roundID,
			Budget:  100_000,
			Votes:   uniformVotes(validators, taskverification.VerdictPass, 7000),
			Emits: []EmitSpec{
				{ProducerIdx: 0, Verdict: "pass", ScoreBP: 7000},
			},
		}
	}
	return scenarios
}

// TiedWeightCorpus constructs a 10-task scenario set designed to
// reproduce the cross-node selection race on the current unfixed code
// path. Per scenario:
//
//   - The round has half pass-votes and half fail-votes (equal stake →
//     a vote-weight tie at the moment of finalization).
//   - Two TVConsensus events are emitted: one with FinalVerdict="pass"
//     authored by the first validator, one with FinalVerdict="fail"
//     authored by the second.
//   - Per-node delivery order alternates: even-indexed nodes see "pass"
//     first, odd-indexed nodes see "fail" first.
//
// On each node, TVConsensusConsumer.Apply takes the per-task mutex and
// settles using the FIRST event past the mutex — meaning even and odd
// nodes settle different verdicts for the same task, and per-validator
// payouts diverge across the cluster. The cross-node ledger snapshot
// MUST diverge.
//
// This is the bug class characterized in
// docs/plans/implementation/selection-race-characterization.md §1-§3.
// If this corpus does NOT produce divergence, either the bug has been
// fixed (in which case F4B is done and this corpus should be retired)
// or the harness is broken (in which case F4 cannot proceed). Either
// outcome is load-bearing on harness validation.
func TiedWeightCorpus(c *Cluster) []*TaskScenario {
	validators := c.AllValidators()
	if len(validators) < 2 {
		// Need at least 2 validators to construct conflicting authors.
		return nil
	}

	scenarios := make([]*TaskScenario, 10)
	for i := 0; i < 10; i++ {
		taskID := fmt.Sprintf("tied-%02d", i)
		roundID := fmt.Sprintf("tied-round-%02d", i)

		emits := []EmitSpec{
			{ProducerIdx: 0, Verdict: "pass", ScoreBP: 7000},
			{ProducerIdx: 1, Verdict: "fail", ScoreBP: 7000},
		}

		// Alternate delivery order: even nodes see pass-first, odd nodes
		// see fail-first. With ≥2 nodes this guarantees at least one
		// node settles "pass" and at least one settles "fail".
		deliveryOrders := make([][]int, len(c.Nodes))
		for n := range c.Nodes {
			if n%2 == 0 {
				deliveryOrders[n] = []int{0, 1}
			} else {
				deliveryOrders[n] = []int{1, 0}
			}
		}

		scenarios[i] = &TaskScenario{
			TaskID:         taskID,
			RoundID:        roundID,
			Budget:         100_000,
			Votes:          splitVotes(validators),
			Emits:          emits,
			DeliveryOrders: deliveryOrders,
		}
	}
	return scenarios
}
