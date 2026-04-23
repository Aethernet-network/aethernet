// Package cross_node implements the F4A cross-node ledger byte-equality
// test harness for the AetherNet protocol. It spins up N in-process node
// stacks (TaskManager + TransferLedger + Escrow + verification settler +
// dispatcher TVConsensus consumer + per-node taskverification round
// store), deterministically delivers canonical TaskVerificationConsensus
// events through a test transport, and asserts byte-equality of projected
// ledger state across all nodes.
//
// The harness is the verification-discipline foundation for F4 (Plan v2
// §2.1.1). Its purpose is twofold:
//
//  1. Detect cross-node settlement divergence on every PR via CI.
//  2. Reproduce the multi-emit selection-race bug class (characterized in
//     docs/plans/implementation/selection-race-characterization.md) on the
//     CURRENT unfixed code path, so that the harness's own correctness is
//     established before F4B's protocol fix is implemented.
//
// Determinism strategy: the harness does not use TCP, fastpath, or the
// real recognition fabric. Each node is a self-contained in-process stack;
// transport is a function that delivers an event to a chosen subset of
// nodes in a chosen order via direct consumer.Apply invocation. This
// removes timing variance, so the only divergence the harness can produce
// is structural — exactly the divergence the F4 fix is meant to close.
//
// Scope notes:
//
//   - The harness models the post-DAG-converged state. Every node's
//     TaskManager, escrow, and round store are pre-populated identically;
//     this corresponds to all nodes having ingested the same task lifecycle
//     events from the DAG. The race the harness reproduces is purely the
//     downstream consumer-selection race.
//   - The harness does not exercise OCS, autovalidator, fastpath, or the
//     TaskSubmitted → TVConsensus emission path. Those layers are tested
//     elsewhere; this harness's job is to validate that, given an agreed
//     DAG of canonical events, all nodes project the same ledger state.
//   - The harness intentionally does NOT spin up the full Dispatcher
//     wrapper. The TVConsensusConsumer's per-task mutex is the locus of
//     the bug; calling Apply directly exercises that exact code path
//     without the dispatcher's content-hash deduplication confusing the
//     reproduction (each emit has a distinct hash anyway).
package cross_node

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement"
	"github.com/Aethernet-network/aethernet/internal/tasks"
	"github.com/Aethernet-network/aethernet/internal/taskverification"

	badger "github.com/dgraph-io/badger/v4"
)

// TreasuryID is the canonical treasury agent ID used by the harness.
// Settlement payouts route the treasury share here so balance assertions
// can compare cluster-wide.
const TreasuryID crypto.AgentID = "genesis:treasury"

// PosterID and WorkerID are the canonical poster/worker identities used
// across all corpus tasks. Fixing them simplifies cross-node ledger
// comparison: every node's poster/worker balance must equal the same
// value.
const (
	PosterID crypto.AgentID = "poster"
	WorkerID crypto.AgentID = "worker"
)

// Node is a single in-process node stack. It owns the local copy of
// every state mutation surface that the TVConsensus settlement path
// touches: the task manager, the transfer ledger, the escrow, the
// round store, and the consumer that runs Apply.
//
// Each node's ValidatorID is distinct; this matches the production
// shape where every validator is a node with its own identity. Validator
// IDs participate in payouts (validators that voted for the winning
// verdict get a share of the validator pool), so the cluster's set of
// validator IDs is part of the canonical state every node must agree on.
type Node struct {
	Index       int
	ValidatorID crypto.AgentID
	KeyPair     *crypto.KeyPair

	TaskManager *tasks.TaskManager
	Ledger      *ledger.TransferLedger
	Escrow      *escrow.Escrow
	RoundStore  taskverification.Store
	Settler     *settlement.VerificationConsensusSettler
	Consumer    *dispatch.TVConsensusConsumer

	// badgerDB is the in-memory BadgerDB backing the round store; held
	// so it can be closed on cleanup.
	badgerDB *badger.DB
}

// Cluster is a set of N in-process nodes plus shared metadata.
//
// Validators is the deterministic set of validator agent IDs in the
// cluster; every node knows the full set so that round.Votes can list
// validator participation correctly. Index i in Validators is the
// validator ID of Nodes[i].
type Cluster struct {
	Nodes      []*Node
	Validators []crypto.AgentID
}

// NewCluster constructs an n-node in-process cluster. Each node gets a
// deterministic ed25519 keypair seeded from its index so test runs are
// reproducible. The cluster is not "started" — there is no goroutine,
// no listener, no background task. All mutation happens synchronously
// via direct Apply calls from the test transport.
func NewCluster(t *testing.T, n int) *Cluster {
	t.Helper()
	if n < 2 {
		t.Fatalf("cross_node: cluster size must be >= 2 (got %d)", n)
	}

	c := &Cluster{
		Nodes:      make([]*Node, n),
		Validators: make([]crypto.AgentID, n),
	}
	for i := 0; i < n; i++ {
		c.Nodes[i] = newNode(t, i)
		c.Validators[i] = c.Nodes[i].ValidatorID
	}
	return c
}

// newNode constructs a single node stack. All state is in-memory; the
// round store uses an in-memory BadgerDB so rounds persist across Apply
// calls within the test (matching production semantics) without touching
// disk.
func newNode(t *testing.T, idx int) *Node {
	t.Helper()

	kp := deterministicKeyPair(idx)
	validatorID := kp.AgentID()

	tl := ledger.NewTransferLedger()
	tm := tasks.NewTaskManager()
	em := escrow.New(tl)

	opts := badger.DefaultOptions("").WithInMemory(true).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("cross_node: open badger for node %d: %v", idx, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	roundStore := taskverification.NewBadgerStore(db)

	// Even-split validator distribution (qScoreFn=nil falls back to
	// even-split). Treasury is fixed across all nodes.
	settler := settlement.NewVerificationConsensusSettler(
		tm, tl, em, &nilDAGScanner{}, nil, TreasuryID, nil)
	consumer := dispatch.NewTVConsensusConsumer(settler, roundStore, tm, em)

	return &Node{
		Index:       idx,
		ValidatorID: validatorID,
		KeyPair:     kp,
		TaskManager: tm,
		Ledger:      tl,
		Escrow:      em,
		RoundStore:  roundStore,
		Settler:     settler,
		Consumer:    consumer,
		badgerDB:    db,
	}
}

// deterministicKeyPair builds an ed25519 keypair from a fixed seed
// derived from the node index. This makes validator IDs stable across
// test runs so divergence diffs are reproducible.
func deterministicKeyPair(idx int) *crypto.KeyPair {
	var seed [ed25519.SeedSize]byte
	binary.BigEndian.PutUint64(seed[0:8], uint64(0xae7e_4e72_0000_0000)) // "aether"
	binary.BigEndian.PutUint64(seed[8:16], uint64(idx)+1)
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	return &crypto.KeyPair{
		PublicKey:  pub,
		PrivateKey: priv,
	}
}

// FundPoster mints a one-time genesis credit for the poster on every
// node. Called once per cluster before SetupTask. The amount must be
// large enough to cover every scenario's escrow lock; the harness
// chooses 1e12 micro-AET (1 million AET) which is comfortably above
// the corpus aggregate.
//
// FundAgent is content-addressed by (agentID, amount), so repeated
// calls with the same arguments fail with ErrDuplicateEntry. This is
// why funding is hoisted out of SetupTask — every scenario shares one
// poster, and the funding event must be created exactly once per node.
func (c *Cluster) FundPoster(t *testing.T, amount uint64) {
	t.Helper()
	for _, node := range c.Nodes {
		if err := node.Ledger.FundAgent(PosterID, amount); err != nil {
			t.Fatalf("node %d: fund poster: %v", node.Index, err)
		}
	}
}

// SetupTask provisions a single task identically on every node in the
// cluster. After this call:
//
//   - Every node's TaskManager has the task in TaskStatusSubmitted (the
//     state required for the TVConsensus consumer to Apply settlement).
//   - Every node's Escrow has the budget locked under the task's
//     escrow:<taskID> bucket and an EscrowEntry registered.
//   - Every node's RoundStore has the verification round saved with the
//     given vote set.
//
// This corresponds to the post-DAG-converged state where every node has
// successfully ingested the TaskPosted, TaskClaimed, TaskSubmitted,
// escrow-lock Transfer, and per-validator vote events. The harness then
// drives the divergence-prone TVConsensus admission and asserts ledger
// equality.
//
// The cluster's poster must already be funded (see FundPoster).
func (c *Cluster) SetupTask(t *testing.T, scenario *TaskScenario) {
	t.Helper()
	for _, node := range c.Nodes {
		node.setupTask(t, scenario)
	}
}

// setupTask is the per-node implementation of Cluster.SetupTask.
func (n *Node) setupTask(t *testing.T, scenario *TaskScenario) {
	t.Helper()

	task, err := n.TaskManager.PostTask(
		string(PosterID),
		"task-"+scenario.TaskID,
		"cross-node harness scenario",
		"research",
		scenario.Budget,
		tasks.PostTaskOpts{TaskID: scenario.TaskID},
	)
	if err != nil {
		t.Fatalf("node %d: post task %s: %v", n.Index, scenario.TaskID, err)
	}
	if err := n.TaskManager.ClaimTask(task.ID, WorkerID); err != nil {
		t.Fatalf("node %d: claim task %s: %v", n.Index, scenario.TaskID, err)
	}
	if err := n.TaskManager.SubmitResult(task.ID, WorkerID, "sha256:harness", "harness", ""); err != nil {
		t.Fatalf("node %d: submit task %s: %v", n.Index, scenario.TaskID, err)
	}

	// Move budget into the escrow bucket and register the entry. Mirrors
	// the production "escrow-lock Transfer + RegisterEscrow" pair, but
	// without requiring a DAG-projected funding event (the
	// RegisterEscrowForTest helper is the same one used by the existing
	// dispatch consumer tests for this purpose).
	bucket := crypto.AgentID("escrow:" + scenario.TaskID)
	if err := n.Ledger.TransferFromBucket(PosterID, bucket, scenario.Budget); err != nil {
		t.Fatalf("node %d: escrow-lock transfer for %s: %v", n.Index, scenario.TaskID, err)
	}
	if err := n.Escrow.RegisterEscrowForTest(
		scenario.TaskID, PosterID, scenario.Budget,
		event.EventID("test-funding:"+scenario.TaskID),
	); err != nil {
		t.Fatalf("node %d: register escrow for %s: %v", n.Index, scenario.TaskID, err)
	}

	// Build and save the round. Same vote set on every node — divergence
	// in the harness comes from the TVConsensus event payload, not from
	// per-node round content.
	round := &taskverification.TaskVerificationRound{
		RoundID:               taskverification.RoundID(scenario.RoundID),
		TaskID:                scenario.TaskID,
		WorkerID:              WorkerID,
		PosterID:              PosterID,
		Category:              "research",
		ParticipatingFamilies: map[string]uint64{},
		Votes:                 scenario.Votes,
	}
	if err := n.RoundStore.SaveRound(context.Background(), round); err != nil {
		t.Fatalf("node %d: save round for %s: %v", n.Index, scenario.TaskID, err)
	}
}

// Apply invokes the node's TVConsensusConsumer.Apply for ev, using the
// in-process per-task mutex that is the locus of the selection race.
// This is the production-equivalent invocation site (see
// internal/dispatch/dispatcher.go invokeConsumers); the dispatcher
// itself is omitted because each event in the harness has a distinct
// canonical hash and would be admitted independently anyway, so the
// dispatcher would just call this method.
func (n *Node) Apply(ctx context.Context, ev *event.Event) error {
	return n.Consumer.Apply(ctx, ev)
}

// nilDAGScanner is a stub DAGScanner satisfying the settler's interface
// for the catch-up path. The harness pre-registers escrow on every node
// via RegisterEscrowForTest, so the catch-up path is never invoked.
type nilDAGScanner struct{}

func (n *nilDAGScanner) All() []*event.Event { return nil }

// MakeConsensusEvent constructs a signed TaskVerificationConsensus event
// authored by the given validator. This mirrors EmitConsensusEvent in
// internal/taskverification/deadline_checker.go but does not require a
// publisher or DAG — the harness delivers events directly to consumers.
//
// The event is signed so that the canonical event ID is determined
// (signing is part of the canonical bytes). Distinct validator authors
// or distinct verdicts produce distinct event IDs, which mirrors the
// production multi-emit pattern.
func MakeConsensusEvent(
	t *testing.T,
	validator *Node,
	taskID string,
	roundID string,
	verdict string,
	scoreBP uint64,
) *event.Event {
	t.Helper()

	payload := event.TaskVerificationConsensusPayload{
		Version:      1,
		RoundID:      roundID,
		TaskID:       taskID,
		WorkerID:     string(WorkerID),
		PosterID:     string(PosterID),
		FinalVerdict: verdict,
		FinalScoreBP: scoreBP,
	}

	ev, err := event.New(
		event.EventTypeTaskVerificationConsensus,
		nil,
		payload,
		string(validator.ValidatorID),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("cross_node: construct consensus event: %v", err)
	}
	if err := crypto.SignEvent(ev, validator.KeyPair); err != nil {
		t.Fatalf("cross_node: sign consensus event: %v", err)
	}
	return ev
}

// AllValidators returns a copy of the cluster's full validator-ID set.
// Used by corpus generators that need to construct vote sets covering
// every node in the cluster.
func (c *Cluster) AllValidators() []crypto.AgentID {
	out := make([]crypto.AgentID, len(c.Validators))
	copy(out, c.Validators)
	return out
}

// String renders a short cluster description for debug output.
func (c *Cluster) String() string {
	return fmt.Sprintf("Cluster{nodes=%d}", len(c.Nodes))
}
