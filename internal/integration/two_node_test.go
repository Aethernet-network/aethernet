// Package integration_test contains end-to-end tests that exercise multiple
// packages working together across real TCP connections.
//
// Each test spins up two complete in-memory node stacks — DAG, dual ledger,
// identity registry, OCS engine, network node, and HTTP API server — connects
// them as TCP peers, and verifies that events submitted to one node propagate
// to the other.
package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethernet/core/internal/api"
	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/dag"
	"github.com/aethernet/core/internal/event"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/network"
	"github.com/aethernet/core/internal/ocs"
)

// nodeStack bundles a complete in-memory node for use in integration tests.
type nodeStack struct {
	dag        *dag.DAG
	transfer   *ledger.TransferLedger
	generation *ledger.GenerationLedger
	registry   *identity.Registry
	engine     *ocs.Engine
	supply     *ledger.SupplyManager
	kp         *crypto.KeyPair
	node       *network.Node
	apiURL     string // httptest server URL
}

// buildNodeStack constructs a complete in-memory node stack and registers
// cleanup functions with t so callers need not close anything explicitly.
func buildNodeStack(t *testing.T) *nodeStack {
	t.Helper()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	d := dag.New()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	eng := ocs.NewEngine(ocs.DefaultConfig(), tl, gl, reg)
	if err := eng.Start(); err != nil {
		t.Fatalf("start OCS engine: %v", err)
	}
	t.Cleanup(eng.Stop)
	sm := ledger.NewSupplyManager(tl, gl)

	// Network node: listen on 127.0.0.1:0 so the OS assigns a free port.
	// SyncInterval is short so periodic sync fires quickly during tests if
	// the broadcast path fails for any reason.
	cfg := &network.NodeConfig{
		ListenAddr:   "127.0.0.1:0",
		AgentID:      kp.AgentID(),
		MaxPeers:     10,
		SyncInterval: 500 * time.Millisecond,
		Version:      "0.1.0-test",
		// KeyPair intentionally omitted: challenge-response auth is skipped
		// when KeyPair is nil, which simplifies test setup without affecting
		// the correctness of the sync logic under test.
	}
	node := network.NewNode(cfg, d)
	if err := node.Start(); err != nil {
		t.Fatalf("start network node: %v", err)
	}
	t.Cleanup(node.Stop)

	// API server: mounted on httptest.NewServer so no real port is needed.
	apiSrv := api.NewServer("", d, tl, gl, reg, eng, sm, node, kp)
	ts := httptest.NewServer(apiSrv)
	t.Cleanup(ts.Close)

	return &nodeStack{
		dag:        d,
		transfer:   tl,
		generation: gl,
		registry:   reg,
		engine:     eng,
		supply:     sm,
		kp:         kp,
		node:       node,
		apiURL:     ts.URL,
	}
}

// postJSON is a test helper that sends a JSON POST and returns the response.
func postJSON(t *testing.T, url string, body map[string]any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// pollDAGSize polls d.Size() every 50 ms until it reaches at least want or
// the deadline elapses, then returns the final size.
func pollDAGSize(d *dag.DAG, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.Size() >= want {
			return d.Size()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return d.Size()
}

// waitPeerCount polls n.PeerCount() until it reaches at least want or
// the deadline elapses. Used to ensure both sides have completed the
// handshake before submitting events.
func waitPeerCount(n *network.Node, want int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n.PeerCount() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestTwoNode_EventSync verifies that an event submitted to node 1's API
// propagates to node 2's DAG via the broadcast path.
func TestTwoNode_EventSync(t *testing.T) {
	n1 := buildNodeStack(t)
	n2 := buildNodeStack(t)

	// Connect n2 → n1.
	if _, err := n2.node.Connect(n1.node.ListenAddr()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Wait for n1 to complete the acceptor side of the handshake.
	waitPeerCount(n1.node, 1, 2*time.Second)

	// Submit a Generation event on n1 via the HTTP API.
	resp := postJSON(t, n1.apiURL+"/v1/generation", map[string]any{
		"claimed_value": 5000,
		"evidence_hash": "sha256:integration-sync-test",
		"stake_amount":  1000,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/generation on n1: want 201, got %d", resp.StatusCode)
	}
	var genResult struct {
		EventID string `json:"event_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&genResult); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp.Body.Close()

	if genResult.EventID == "" {
		t.Fatal("event_id must not be empty")
	}

	// Poll n2's DAG: the broadcast from n1 should deliver the event quickly.
	if got := pollDAGSize(n2.dag, 1, 5*time.Second); got < 1 {
		t.Fatalf("event did not sync to node 2 within timeout: dag size = %d", got)
	}

	// Verify n2 has exactly the event that n1 produced.
	if _, err := n2.dag.Get(event.EventID(genResult.EventID)); err != nil {
		t.Errorf("event %s not found in node 2 DAG: %v", genResult.EventID[:8], err)
	}

	// Both DAGs must have identical sizes.
	if n1.dag.Size() != n2.dag.Size() {
		t.Errorf("DAG size mismatch: n1=%d, n2=%d", n1.dag.Size(), n2.dag.Size())
	}
}

// TestTwoNode_BidirectionalSync verifies that events submitted independently
// on both nodes converge: each node ends up with both events.
func TestTwoNode_BidirectionalSync(t *testing.T) {
	n1 := buildNodeStack(t)
	n2 := buildNodeStack(t)

	// Connect n2 → n1, then wait for both sides to register the peer.
	if _, err := n2.node.Connect(n1.node.ListenAddr()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitPeerCount(n1.node, 1, 2*time.Second)
	waitPeerCount(n2.node, 1, 2*time.Second)

	// Submit an event on n1.
	r1 := postJSON(t, n1.apiURL+"/v1/generation", map[string]any{
		"claimed_value": 3000,
		"evidence_hash": "sha256:bidir-from-n1",
		"stake_amount":  1000,
	})
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("n1 generation: want 201, got %d", r1.StatusCode)
	}
	r1.Body.Close()

	// Submit an event on n2.
	r2 := postJSON(t, n2.apiURL+"/v1/generation", map[string]any{
		"claimed_value": 4000,
		"evidence_hash": "sha256:bidir-from-n2",
		"stake_amount":  1000,
	})
	if r2.StatusCode != http.StatusCreated {
		t.Fatalf("n2 generation: want 201, got %d", r2.StatusCode)
	}
	r2.Body.Close()

	// Poll until both DAGs have at least 2 events.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n1.dag.Size() >= 2 && n2.dag.Size() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if n1.dag.Size() < 2 {
		t.Errorf("n1 DAG: want ≥2 events, got %d", n1.dag.Size())
	}
	if n2.dag.Size() < 2 {
		t.Errorf("n2 DAG: want ≥2 events, got %d", n2.dag.Size())
	}
	if n1.dag.Size() != n2.dag.Size() {
		t.Errorf("DAGs did not converge: n1=%d, n2=%d", n1.dag.Size(), n2.dag.Size())
	}
}

// TestTwoNode_TransferSync verifies that a Transfer event (which requires a
// funded agent) also propagates correctly across nodes.
func TestTwoNode_TransferSync(t *testing.T) {
	n1 := buildNodeStack(t)
	n2 := buildNodeStack(t)

	if _, err := n2.node.Connect(n1.node.ListenAddr()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitPeerCount(n1.node, 1, 2*time.Second)

	// Fund n1's agent directly via the ledger (no HTTP endpoint for funding).
	if err := n1.transfer.FundAgent(n1.kp.AgentID(), 1_000_000); err != nil {
		t.Fatalf("fund agent: %v", err)
	}

	// Submit a Transfer on n1.
	resp := postJSON(t, n1.apiURL+"/v1/transfer", map[string]any{
		"to_agent":     string(n1.kp.AgentID()), // self-transfer for demo
		"amount":       500,
		"currency":     "AET",
		"stake_amount": 1000,
	})
	if resp.StatusCode != http.StatusCreated {
		var e map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		t.Fatalf("POST /v1/transfer: want 201, got %d: %v", resp.StatusCode, e)
	}
	var txResult struct {
		EventID string `json:"event_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&txResult)
	resp.Body.Close()

	// Poll n2 for the transfer event.
	if got := pollDAGSize(n2.dag, 1, 5*time.Second); got < 1 {
		t.Fatalf("transfer event did not reach node 2: dag size = %d", got)
	}

	if _, err := n2.dag.Get(event.EventID(txResult.EventID)); err != nil {
		t.Errorf("transfer event not found in n2 DAG: %v", err)
	}
}
