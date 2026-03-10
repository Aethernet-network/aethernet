package network

// Tests for DNS-based PeerDiscovery.
//
// DNS is mocked by replacing the resolver field on PeerDiscovery so the tests
// run without real network dependencies and execute quickly.

import (
	"net"
	"testing"
	"time"
)

// newDiscoveryTestNode starts a Node on a random port and registers cleanup.
func newDiscoveryTestNode(t *testing.T) *Node {
	t.Helper()
	n := newTestNode(t, false)
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
