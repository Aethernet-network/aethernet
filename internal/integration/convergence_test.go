package integration_test

import (
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/network"
)

// TestFiveNode_DAGConvergence creates 5 nodes with divergent DAGs and
// verifies they converge to the same state through the periodic sync
// mechanism. This reproduces the exact testnet failure mode: concurrent
// startup with events created before peers connect.
func TestFiveNode_DAGConvergence(t *testing.T) {
	const numNodes = 5
	const eventsPerNode = 10

	// Create 5 nodes with keypairs.
	type nodeInfo struct {
		kp   *crypto.KeyPair
		d    *dag.DAG
		node *network.Node
	}
	nodes := make([]nodeInfo, numNodes)

	for i := range nodes {
		kp, _ := crypto.GenerateKeyPair()
		d := dag.New()
		cfg := &network.NodeConfig{
			ListenAddr:   "127.0.0.1:0",
			AgentID:      kp.AgentID(),
			MaxPeers:     10,
			SyncInterval: 500 * time.Millisecond, // fast sync for testing
			Version:      "test",
		}
		n := network.NewNode(cfg, d)
		if err := n.Start(); err != nil {
			t.Fatalf("node %d start: %v", i, err)
		}
		t.Cleanup(n.Stop)
		nodes[i] = nodeInfo{kp: kp, d: d, node: n}
	}

	// Add DIFFERENT events to each node BEFORE connecting peers.
	// This simulates the concurrent startup scenario where each node
	// creates genesis/registration events independently.
	totalEvents := 0
	for i, ni := range nodes {
		for j := 0; j < eventsPerNode; j++ {
			payload := event.TransferPayload{
				Version:   1,
				FromAgent: "system",
				ToAgent:   string(ni.kp.AgentID()),
				Amount:    uint64(i*100 + j),
				Currency:  "AET",
				Reason:    "test-divergence",
			}
			// Use DAG tips as causal refs (first event has none = genesis).
			tips := ni.d.Tips()
			priorTS := make(map[event.EventID]uint64, len(tips))
			for _, ref := range tips {
				if ev, err := ni.d.Get(ref); err == nil {
					priorTS[ref] = ev.CausalTimestamp
				}
			}
			ev, err := event.New(event.EventTypeTransfer, tips, payload,
				string(ni.kp.AgentID()), priorTS, 1000)
			if err != nil {
				t.Fatalf("node %d event %d: %v", i, j, err)
			}
			_ = crypto.SignEvent(ev, ni.kp)
			if err := ni.d.Add(ev); err != nil {
				t.Fatalf("node %d dag.Add: %v", i, err)
			}
			totalEvents++
		}
	}

	// Verify divergent state: each node has only its own events.
	for i, ni := range nodes {
		if ni.d.Size() != eventsPerNode {
			t.Fatalf("node %d: expected %d events, got %d", i, eventsPerNode, ni.d.Size())
		}
	}
	t.Logf("pre-connect: %d total events across %d nodes (each has %d)", totalEvents, numNodes, eventsPerNode)

	// Connect all nodes in a full mesh.
	for i := 0; i < numNodes; i++ {
		for j := i + 1; j < numNodes; j++ {
			if _, err := nodes[i].node.Connect(nodes[j].node.ListenAddr()); err != nil {
				t.Fatalf("connect %d→%d: %v", i, j, err)
			}
		}
	}

	// Wait for convergence. With SyncInterval=500ms and multi-pass batch
	// handling, all nodes should converge within a few seconds.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		converged := true
		sizes := make([]int, numNodes)
		for i, ni := range nodes {
			sizes[i] = ni.d.Size()
			if sizes[i] != totalEvents {
				converged = false
			}
		}
		if converged {
			t.Logf("CONVERGED: all %d nodes have %d events", numNodes, totalEvents)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Failed — report divergent state.
	for i, ni := range nodes {
		t.Logf("node %d: dag_size=%d (want %d)", i, ni.d.Size(), totalEvents)
	}
	t.Fatalf("DAG did not converge after 15 seconds")
}
