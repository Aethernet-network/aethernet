package network

// Tests for DNS-based PeerDiscovery and self-connection prevention.
//
// DNS is mocked by replacing the resolver field on PeerDiscovery so the tests
// run without real network dependencies and execute quickly.

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
)

// newDiscoveryTestNode starts a Node on a random port and registers cleanup.
// Each node gets a unique AgentID (derived from a fresh keypair) so that
// the self-connection AgentID guard does not fire when two test nodes connect
// to each other within the same test function.
func newDiscoveryTestNode(t *testing.T) *Node {
	t.Helper()
	n := newTestNode(t, true) // true = keypair → unique AgentID per node
	n.config.ListenAddr = "127.0.0.1:0"
	if err := n.Start(); err != nil {
		t.Fatalf("node Start: %v", err)
	}
	t.Cleanup(n.Stop)
	return n
}

// TestPeerDiscovery_ConnectsToResolvedPeer verifies that PeerDiscovery dials
// the IP returned by the mock resolver and the target node sees the connection.
func TestPeerDiscovery_ConnectsToResolvedPeer(t *testing.T) {
	// target is the node we want to discover and connect to.
	target := newDiscoveryTestNode(t)

	// Extract target's port so the discovery can dial it.
	_, port, err := net.SplitHostPort(target.ListenAddr())
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", target.ListenAddr(), err)
	}

	// discoverer is the node that will run peer discovery.
	discoverer := newDiscoveryTestNode(t)

	pd := NewPeerDiscovery("nodes.test.local", port, discoverer, 50*time.Millisecond)
	// Override resolver to return the target's loopback IP.
	pd.resolver = func(host string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	}
	pd.Start()
	defer pd.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if discoverer.PeerCount() >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if discoverer.PeerCount() < 1 {
		t.Fatal("PeerDiscovery did not connect to the target peer within 2 s")
	}
}

// TestPeerDiscovery_SkipsKnownPeers verifies that an already-connected address
// is not dialled a second time (no duplicate connections).
func TestPeerDiscovery_SkipsKnownPeers(t *testing.T) {
	target := newDiscoveryTestNode(t)
	_, port, _ := net.SplitHostPort(target.ListenAddr())

	discoverer := newDiscoveryTestNode(t)

	callCount := 0
	pd := NewPeerDiscovery("nodes.test.local", port, discoverer, 20*time.Millisecond)
	pd.resolver = func(host string) ([]string, error) {
		callCount++
		return []string{"127.0.0.1"}, nil
	}
	pd.Start()
	defer pd.Stop()

	// Let at least 3 resolution cycles pass.
	time.Sleep(120 * time.Millisecond)

	if discoverer.PeerCount() < 1 {
		t.Fatal("expected at least one peer connection")
	}
	if discoverer.PeerCount() > 1 {
		t.Errorf("duplicate connections: want 1 peer, got %d", discoverer.PeerCount())
	}
}

// TestPeerDiscovery_ReconnectsAfterDisconnect verifies that after a peer
// disconnects its address is cleared from knownPeers so the next cycle
// reconnects to it.
func TestPeerDiscovery_ReconnectsAfterDisconnect(t *testing.T) {
	target := newDiscoveryTestNode(t)
	_, port, _ := net.SplitHostPort(target.ListenAddr())

	discoverer := newDiscoveryTestNode(t)

	pd := NewPeerDiscovery("nodes.test.local", port, discoverer, 50*time.Millisecond)
	pd.resolver = func(host string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	}
	pd.Start()
	defer pd.Stop()

	// Wait for initial connection.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if discoverer.PeerCount() >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if discoverer.PeerCount() < 1 {
		t.Fatal("initial connection did not establish within 2 s")
	}

	// Stop the target node to simulate a peer disconnect.
	target.Stop()

	// Wait for the peer count to drop to zero.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if discoverer.PeerCount() == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if discoverer.PeerCount() != 0 {
		t.Fatal("peer count did not drop to 0 after target stopped")
	}

	// knownPeers should now be empty (address cleared by disconnect handler).
	pd.mu.Lock()
	knownCount := len(pd.knownPeers)
	pd.mu.Unlock()
	if knownCount != 0 {
		t.Errorf("knownPeers should be empty after disconnect, got %d entries", knownCount)
	}
}

// TestPeerDiscovery_SkipsSelf verifies that when the DNS resolver returns an
// address that matches the node's own listener (same IP and port), the
// discovery loop skips it without attempting a connection.
//
// Production scenario: all 3 ECS nodes share the same DNS name. Each node's
// own private IP appears in the resolved list; connecting to it would create
// a broken self-peer or fail noisily on every discovery cycle.
func TestPeerDiscovery_SkipsSelf(t *testing.T) {
	// Start a node and learn its actual listen address.
	self := newDiscoveryTestNode(t)
	selfHost, selfPort, err := net.SplitHostPort(self.ListenAddr())
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", self.ListenAddr(), err)
	}
	_ = selfHost // may be "127.0.0.1" or "::" depending on OS

	// Create discovery using self's own port. The resolver returns only self's
	// IP, so every resolved address should be recognised as a self-connection
	// and skipped without incrementing PeerCount.
	pd := NewPeerDiscovery("nodes.test.local", selfPort, self, 30*time.Millisecond)
	pd.resolver = func(host string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	}
	pd.Start()
	defer pd.Stop()

	// Run several resolution cycles.
	time.Sleep(120 * time.Millisecond)

	if self.PeerCount() != 0 {
		t.Errorf("self-connection was established: want 0 peers, got %d", self.PeerCount())
	}
}

// TestConnect_SelfConnectionByAgentID verifies the defense-in-depth AgentID
// check in Connect(). Two nodes are configured with the same AgentID (simulating
// a node that dials itself through a NAT or load balancer, where the IP check
// cannot catch the self-connection). Connect must return ErrSelfConnection.
func TestConnect_SelfConnectionByAgentID(t *testing.T) {
	sharedID := crypto.AgentID("shared-agent-" + t.Name())

	// nodeA listens and accepts connections.
	cfgA := DefaultNodeConfig(sharedID)
	cfgA.ListenAddr = "127.0.0.1:0"
	nodeA := NewNode(cfgA, dag.New())
	if err := nodeA.Start(); err != nil {
		t.Fatalf("nodeA Start: %v", err)
	}
	t.Cleanup(nodeA.Stop)

	// nodeB has the SAME AgentID — it is dialling "itself" from nodeA's perspective.
	cfgB := DefaultNodeConfig(sharedID)
	cfgB.ListenAddr = "127.0.0.1:0"
	nodeB := NewNode(cfgB, dag.New())

	// nodeB dials nodeA. After the handshake, nodeA responds with sharedID which
	// matches nodeB's own AgentID — triggering ErrSelfConnection.
	_, err := nodeB.Connect(nodeA.ListenAddr())
	if err == nil {
		t.Fatal("Connect succeeded but should have returned ErrSelfConnection (same AgentID)")
	}
	if !errors.Is(err, ErrSelfConnection) {
		t.Errorf("Connect returned %v; want ErrSelfConnection", err)
	}
}

// TestConnect_SelfConnectionByIP verifies the IP-level self-connection guard
// in Connect(). When a node calls Connect with its own listen address (same IP
// and port), Connect must return ErrSelfConnection before dialling.
func TestConnect_SelfConnectionByIP(t *testing.T) {
	self := newDiscoveryTestNode(t)

	_, err := self.Connect(self.ListenAddr())
	if err == nil {
		t.Fatal("Connect succeeded but should have returned ErrSelfConnection (own IP:port)")
	}
	if !errors.Is(err, ErrSelfConnection) {
		t.Errorf("Connect returned %v; want ErrSelfConnection", err)
	}
}
