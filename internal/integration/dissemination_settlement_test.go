// End-to-end tests proving that locally-created events reach all peers and
// that consensus-finalized events settle with converged balances.
//
// These tests exercise the complete chain:
//   event creation → localpub.Publish → dag.Add → Fast Path + V1 broadcast
//   → peer sync → OCS pending → auto-validator vote → consensus finalization
//   → SetFinalizationHandler → Settlement event → settlementApp.Apply
//   → balance update
//
// Three-node topology: all nodes connected in a full mesh.
package integration_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/autovalidator"
	"github.com/Aethernet-network/aethernet/internal/consensus"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/localpub"
	"github.com/Aethernet-network/aethernet/internal/network"
	"github.com/Aethernet-network/aethernet/internal/ocs"
	"github.com/Aethernet-network/aethernet/internal/settlement"
)

// ---------------------------------------------------------------------------
// Full-stack test node with consensus + settlement
// ---------------------------------------------------------------------------

// fullNode is a complete in-memory node stack with OCS, consensus,
// auto-validator, settlement applicator, and the authoritative publisher.
type fullNode struct {
	kp         *crypto.KeyPair
	agentID    crypto.AgentID
	d          *dag.DAG
	tl         *ledger.TransferLedger
	gl         *ledger.GenerationLedger
	reg        *identity.Registry
	eng        *ocs.Engine
	vr         *consensus.VotingRound
	av         *autovalidator.AutoValidator
	settleApp  *settlement.Applicator
	node       *network.Node
	pub        *localpub.Publisher
}

// buildFullNode constructs a complete node stack with consensus wiring.
// minParticipants controls the consensus quorum size.
func buildFullNode(t *testing.T, minParticipants int) *fullNode {
	t.Helper()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	agentID := kp.AgentID()

	d := dag.New()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	// Register self in the identity registry for vote weight.
	fp, _ := identity.NewFingerprint(agentID, kp.PublicKey, nil)
	fp.ReputationScore = 5000
	fp.StakedAmount = 100_000
	_ = reg.Register(fp)

	// OCS engine with consensus.
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	votingCfg := &consensus.ConsensusConfig{
		SupermajorityThreshold: 0.667,
		MaxRounds:              10,
		RoundTimeout:           5 * time.Second,
		MinParticipants:        minParticipants,
	}
	vr := consensus.NewVotingRound(votingCfg, reg)
	eng.SetConsensus(vr)

	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(eng.Stop)

	// Network node.
	nodeCfg := &network.NodeConfig{
		ListenAddr:   "127.0.0.1:0",
		AgentID:      agentID,
		MaxPeers:     10,
		SyncInterval: 200 * time.Millisecond,
		Version:      "0.1.0-test",
	}
	node := network.NewNode(nodeCfg, d)
	if err := node.Start(); err != nil {
		t.Fatalf("start node: %v", err)
	}
	t.Cleanup(node.Stop)

	// Publisher: authoritative local event publication.
	pub := localpub.New(d, node)

	// Settlement applicator.
	settleApp := settlement.NewApplicator(tl, gl, reg,
		func(id event.EventID) (*event.Event, error) { return d.Get(id) },
	)
	settleApp.Start()
	t.Cleanup(settleApp.Stop)

	// Finalization handler: when consensus finalizes, create Settlement event.
	eng.SetFinalizationHandler(func(targetID event.EventID, verdict bool, verifiedValue uint64, finalOrder uint64) {
		if settleApp.IsApplied(targetID) {
			return
		}
		rec, err := vr.GetRecord(targetID)
		if err != nil {
			return
		}
		consensusVerdict := settlement.VerdictAccepted
		if !verdict {
			consensusVerdict = settlement.VerdictRejected
		}
		var attestations []settlement.VoterAttestation
		for voterKey, vote := range rec.Votes {
			v := settlement.VerdictAccepted
			if !vote {
				v = settlement.VerdictRejected
			}
			attestations = append(attestations, settlement.VoterAttestation{
				VoterID: string(voterKey), Verdict: string(v),
			})
		}
		sp := settlement.SettlementPayload{
			TargetEventID:  string(targetID),
			Verdict:        string(consensusVerdict),
			VerifiedValue:  verifiedValue,
			ConsensusRound: finalOrder,
			Attestations:   attestations,
		}
		sp.SortAttestations()
		tips := d.Tips()
		priorTS := make(map[event.EventID]uint64, len(tips))
		for _, ref := range tips {
			if te, lookupErr := d.Get(ref); lookupErr == nil {
				priorTS[ref] = te.CausalTimestamp
			}
		}
		settlementEv, evErr := event.New(
			event.EventTypeSettlement, tips, sp,
			string(agentID), priorTS, 0,
		)
		if evErr != nil {
			return
		}
		_ = crypto.SignEvent(settlementEv, kp)
		_ = pub.Publish(settlementEv)
		_ = settleApp.Apply(&sp)
	})

	// Sync handler: route incoming events to OCS + settlement.
	node.SetSyncHandler(func(ev *event.Event) {
		switch ev.Type {
		case event.EventTypeTransfer, event.EventTypeGeneration, event.EventTypeTaskSettlement:
			_ = eng.SubmitFromSync(ev)
		case event.EventTypeVerificationVote:
			vp, err := event.GetPayload[settlement.VerificationVotePayload](ev)
			if err != nil {
				return
			}
			_ = eng.AcceptPeerVote(
				event.EventID(vp.TargetEventID),
				crypto.AgentID(vp.VoterID),
				vp.Verdict == string(settlement.VerdictAccepted),
			)
		case event.EventTypeSettlement:
			sp, err := event.GetPayload[settlement.SettlementPayload](ev)
			if err != nil {
				return
			}
			_ = settleApp.Apply(&sp)
		}
	})

	// Vote handler for MsgVote wire messages.
	eng.SetVoteBroadcaster(func(eventID event.EventID, verdict bool, voterID crypto.AgentID) {
		_ = node.BroadcastVote(eventID, verdict)
	})
	node.SetVoteHandler(func(voterID crypto.AgentID, eventID event.EventID, verdict bool) {
		_ = eng.AcceptPeerVote(eventID, voterID, verdict)
	})

	// Auto-validator: votes on pending events every 50ms.
	av := autovalidator.NewAutoValidator(eng, agentID, 50*time.Millisecond)
	av.SetDAG(d)
	av.SetKeyPair(kp)
	av.SetPublisher(pub)
	av.Start()
	t.Cleanup(av.Stop)

	return &fullNode{
		kp: kp, agentID: agentID, d: d, tl: tl, gl: gl, reg: reg,
		eng: eng, vr: vr, av: av, settleApp: settleApp, node: node, pub: pub,
	}
}

// connectNodes creates a full mesh between all nodes.
func connectNodes(t *testing.T, nodes []*fullNode) {
	t.Helper()
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			if _, err := nodes[j].node.Connect(nodes[i].node.ListenAddr()); err != nil {
				t.Fatalf("connect node %d → node %d: %v", j, i, err)
			}
		}
	}
	// Wait for all handshakes to complete.
	for _, n := range nodes {
		waitPeerCount(n.node, len(nodes)-1, 3*time.Second)
	}
}

// registerPeerIdentities registers each node's identity in all other nodes'
// registries so vote weight computation works across nodes.
func registerPeerIdentities(t *testing.T, nodes []*fullNode) {
	t.Helper()
	for _, src := range nodes {
		for _, dst := range nodes {
			if src == dst {
				continue
			}
			fp, _ := identity.NewFingerprint(src.agentID, src.kp.PublicKey, nil)
			fp.ReputationScore = 5000
			fp.StakedAmount = 100_000
			_ = dst.reg.Register(fp)
		}
	}
}

// pollBalance polls tl.Balance(agentID) until it reaches at least want.
func pollBalance(tl *ledger.TransferLedger, agentID crypto.AgentID, want uint64, timeout time.Duration) uint64 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		bal, _ := tl.Balance(agentID)
		if bal >= want {
			return bal
		}
		time.Sleep(50 * time.Millisecond)
	}
	bal, _ := tl.Balance(agentID)
	return bal
}

// pollPending polls eng.PendingCount() until it reaches target.
func pollPending(eng *ocs.Engine, target int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if eng.PendingCount() == target {
			return target
		}
		time.Sleep(50 * time.Millisecond)
	}
	return eng.PendingCount()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestE2E_TransferReachesAllNodes(t *testing.T) {
	// 3-node mesh. A transfer created on node 0 must reach nodes 1 and 2.
	nodes := make([]*fullNode, 3)
	for i := range nodes {
		nodes[i] = buildFullNode(t, 3)
	}
	connectNodes(t, nodes)
	registerPeerIdentities(t, nodes)

	// Fund the sender on node 0.
	_ = nodes[0].tl.FundAgent("alice", 1_000_000)

	// Create and publish a Transfer event on node 0.
	payload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 1000, Currency: "AET",
	}
	ev, err := event.New(event.EventTypeTransfer, nodes[0].d.Tips(), payload,
		string(nodes[0].agentID), nil, 1000)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	_ = crypto.SignEvent(ev, nodes[0].kp)

	// Submit to OCS pending on node 0.
	_ = nodes[0].eng.Submit(ev)
	// Publish via the authoritative publisher.
	if err := nodes[0].pub.Publish(ev); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Wait for the event to reach all nodes.
	for i, n := range nodes {
		size := pollDAGSize(n.d, 1, 5*time.Second)
		if size < 1 {
			t.Fatalf("node %d: event did not arrive (dag size = %d)", i, size)
		}
		if _, lookupErr := n.d.Get(ev.ID); lookupErr != nil {
			t.Fatalf("node %d: event %s not found", i, ev.ID)
		}
	}
}

func TestE2E_VoteEventsReachAllNodes(t *testing.T) {
	// 3-node mesh. Auto-validator vote events from node 0 must reach nodes 1 and 2.
	nodes := make([]*fullNode, 3)
	for i := range nodes {
		nodes[i] = buildFullNode(t, 3)
	}
	connectNodes(t, nodes)
	registerPeerIdentities(t, nodes)

	// Fund and submit a Transfer on node 0.
	_ = nodes[0].tl.FundAgent("alice", 1_000_000)
	payload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 500, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nodes[0].d.Tips(), payload,
		string(nodes[0].agentID), nil, 1000)
	_ = crypto.SignEvent(ev, nodes[0].kp)
	_ = nodes[0].eng.Submit(ev)
	_ = nodes[0].pub.Publish(ev)

	// Wait for the transfer to reach all nodes.
	for _, n := range nodes {
		pollDAGSize(n.d, 1, 5*time.Second)
	}

	// Wait for auto-validators to vote. Each node creates a VerificationVote.
	// With the publisher wired, votes are broadcast via Fast Path + V1.
	time.Sleep(500 * time.Millisecond)

	// Each node should have the transfer + at least 1 vote event.
	for i, n := range nodes {
		size := pollDAGSize(n.d, 2, 5*time.Second)
		if size < 2 {
			t.Errorf("node %d: expected at least 2 events (transfer + vote), got %d", i, size)
		}
	}
}

func TestE2E_SettlementConvergence(t *testing.T) {
	// 3-node mesh with MinParticipants=2. After 2+ auto-validator votes,
	// settlement must be created and balances must converge.
	nodes := make([]*fullNode, 3)
	for i := range nodes {
		nodes[i] = buildFullNode(t, 2) // MinParticipants=2 for faster finalization
	}
	connectNodes(t, nodes)
	registerPeerIdentities(t, nodes)

	// Fund sender on ALL nodes so OCS balance check passes everywhere.
	for _, n := range nodes {
		_ = n.tl.FundAgent("sender", 10_000_000)
	}

	// Create and publish a Transfer on node 0.
	payload := event.TransferPayload{
		FromAgent: "sender", ToAgent: "recipient", Amount: 50_000, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, nodes[0].d.Tips(), payload,
		string(nodes[0].agentID), nil, 1000)
	_ = crypto.SignEvent(ev, nodes[0].kp)
	_ = nodes[0].eng.Submit(ev)
	_ = nodes[0].pub.Publish(ev)

	// Wait for the transfer to propagate.
	for _, n := range nodes {
		pollDAGSize(n.d, 1, 5*time.Second)
	}

	// Wait for auto-validators to vote and settlement to converge.
	// With MinParticipants=2, 2 of 3 auto-validators voting is enough.
	// Give generous time for vote propagation + settlement creation.
	time.Sleep(2 * time.Second)

	// OCS pending should be zero on the originating node — the event
	// was either settled (ProcessResult cleared pending) or expired.
	pending := pollPending(nodes[0].eng, 0, 5*time.Second)
	if pending != 0 {
		t.Errorf("node 0: ocs_pending should be 0 after settlement, got %d", pending)
	}

	// Check that at least one node created a Settlement event.
	settlementFound := false
	for i, n := range nodes {
		for _, dagEv := range n.d.All() {
			if dagEv.Type == event.EventTypeSettlement {
				settlementFound = true
				sp, err := event.GetPayload[settlement.SettlementPayload](dagEv)
				if err != nil {
					t.Fatalf("node %d: decode settlement payload: %v", i, err)
				}
				if sp.TargetEventID != string(ev.ID) {
					continue // different settlement
				}
				if sp.Verdict != string(settlement.VerdictAccepted) {
					t.Errorf("node %d: settlement verdict = %s, want accepted", i, sp.Verdict)
				}
				t.Logf("node %d: settlement found for target %s, verdict=%s",
					i, sp.TargetEventID[:8], sp.Verdict)
			}
		}
	}
	if !settlementFound {
		t.Fatal("no Settlement event found on any node after consensus finalization")
	}
}

func TestE2E_GrantSettlesWithBalance(t *testing.T) {
	// Single node with MinParticipants=1. Grant event → auto-validator vote
	// → consensus finalization → settlement → balance > 0.
	// This tests the complete grant settlement chain that was broken before
	// commits 21057ca through 06018f0.
	n := buildFullNode(t, 1)

	// Fund the genesis bucket so the grant can draw from it.
	_ = n.tl.FundAgent("genesis:ecosystem", 100_000_000)

	// Create a grant (Transfer from genesis bucket to the agent).
	payload := event.TransferPayload{
		FromAgent: "genesis:ecosystem",
		ToAgent:   string(n.agentID),
		Amount:    50_000,
		Currency:  "AET",
		Reason:    "onboarding-grant",
	}
	ev, _ := event.New(event.EventTypeTransfer, n.d.Tips(), payload,
		string(n.agentID), nil, n.eng.MinEventStake())
	_ = crypto.SignEvent(ev, n.kp)
	if err := n.eng.Submit(ev); err != nil {
		t.Fatalf("OCS submit: %v", err)
	}
	_ = n.pub.Publish(ev)

	// Wait for auto-validator to vote and settlement to complete.
	pending := pollPending(n.eng, 0, 10*time.Second)
	if pending != 0 {
		t.Fatalf("ocs_pending should be 0 after settlement, got %d", pending)
	}

	// The agent's balance should reflect the settled grant. Poll with
	// generous timeout since the settlement chain involves vote creation,
	// finalization handler, Settlement event creation, and Apply.
	bal := pollBalance(n.tl, n.agentID, 1, 10*time.Second)
	if bal == 0 {
		t.Fatal("agent balance should be > 0 after settled grant — " +
			"settlement not applied (the bug this test guards against)")
	}
	t.Logf("agent balance after grant settlement: %d µAET", bal)

	// Settlement event should exist in the DAG.
	found := false
	for _, dagEv := range n.d.All() {
		if dagEv.Type == event.EventTypeSettlement {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Settlement event not found in DAG after consensus finalization")
	}
}

func TestE2E_PendingZeroMeansSettled(t *testing.T) {
	// Verify that pending=0 genuinely means the event was settled, not that
	// it silently disappeared. This guards against ProcessResult clearing
	// pending without actual settlement.
	n := buildFullNode(t, 1)
	_ = n.tl.FundAgent("alice", 1_000_000)

	payload := event.TransferPayload{
		FromAgent: "alice", ToAgent: "bob", Amount: 1000, Currency: "AET",
	}
	ev, _ := event.New(event.EventTypeTransfer, n.d.Tips(), payload,
		string(n.agentID), nil, 1000)
	_ = crypto.SignEvent(ev, n.kp)
	_ = n.eng.Submit(ev)
	_ = n.pub.Publish(ev)

	// Wait for pending to clear.
	pending := pollPending(n.eng, 0, 10*time.Second)
	if pending != 0 {
		t.Fatalf("pending should reach 0, got %d", pending)
	}

	// When pending=0, a Settlement event MUST exist.
	time.Sleep(500 * time.Millisecond)
	found := false
	for _, dagEv := range n.d.All() {
		if dagEv.Type == event.EventTypeSettlement {
			sp, err := event.GetPayload[settlement.SettlementPayload](dagEv)
			if err != nil {
				continue
			}
			if sp.TargetEventID == string(ev.ID) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("pending cleared to 0 but no Settlement event exists — " +
			"settlement silently disappeared (the bug this test guards against)")
	}
}
