package network

import (
	"log/slog"
	"net"
	"sync"
	"time"
)

// PeerDiscovery periodically resolves a DNS name and connects to any newly
// discovered IP addresses. It is designed for service-discovery environments
// such as AWS Cloud Map where peer IPs change when containers restart.
//
// On each resolution cycle, addresses already present in knownPeers are
// skipped. When a peer disconnects its address is removed from knownPeers so
// the next cycle can reconnect if the same IP reappears in DNS.
//
// Lifecycle: call Start to begin periodic resolution, Stop to shut down.
// Stop is idempotent. PeerDiscovery is safe for concurrent use.
type PeerDiscovery struct {
	dnsName    string
	port       string
	node       *Node
	interval   time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
	knownPeers map[string]bool
	mu         sync.Mutex

	// resolver performs host lookup. Defaults to net.LookupHost; may be
	// replaced in tests to inject a mock address list.
	resolver func(host string) ([]string, error)
}

// NewPeerDiscovery creates a PeerDiscovery that resolves dnsName every
// interval and dials any new IPs on port. It registers a disconnect handler
// on node so that stale entries are cleared and reconnection is attempted
// automatically on the next cycle.
func NewPeerDiscovery(dnsName, port string, node *Node, interval time.Duration) *PeerDiscovery {
	pd := &PeerDiscovery{
		dnsName:    dnsName,
		port:       port,
		node:       node,
		interval:   interval,
		stopCh:     make(chan struct{}),
		knownPeers: make(map[string]bool),
		resolver:   net.LookupHost,
	}

	// When a peer disconnects, remove its address from knownPeers so the next
	// DNS cycle can reconnect if the IP reappears (e.g. ECS task restart with
	// the same subnet IP, or Cloud Map propagation lag during a rolling deploy).
	node.SetDisconnectHandler(func(address string) {
		pd.mu.Lock()
		delete(pd.knownPeers, address)
		pd.mu.Unlock()
		slog.Debug("peer discovery: cleared disconnected peer", "address", address)
	})

	return pd
}

// Start launches the background resolution goroutine. The first DNS lookup
// fires immediately so peers are connected before the first tick.
func (pd *PeerDiscovery) Start() {
	go pd.run()
}

// Stop signals the discovery goroutine to exit. It returns immediately; the
// goroutine will observe the stop signal on its next select. Stop is idempotent.
func (pd *PeerDiscovery) Stop() {
	pd.stopOnce.Do(func() { close(pd.stopCh) })
}

func (pd *PeerDiscovery) run() {
	pd.resolve()
	ticker := time.NewTicker(pd.interval)
	defer ticker.Stop()
	for {
		select {
		case <-pd.stopCh:
			return
		case <-ticker.C:
			pd.resolve()
		}
	}
}

// resolve performs one DNS lookup and connects to any address not yet in
// knownPeers. Failed connections are not added to knownPeers so they are
// retried on the next cycle.
func (pd *PeerDiscovery) resolve() {
	addrs, err := pd.resolver(pd.dnsName)
	if err != nil {
		slog.Warn("peer discovery: DNS lookup failed",
			"name", pd.dnsName, "err", err)
		return
	}
	slog.Debug("peer discovery: resolved", "name", pd.dnsName, "count", len(addrs))

	for _, ip := range addrs {
		addr := net.JoinHostPort(ip, pd.port)

		pd.mu.Lock()
		known := pd.knownPeers[addr]
		pd.mu.Unlock()
		if known {
			continue
		}

		slog.Info("peer discovery: connecting to new peer", "addr", addr)
		if _, err := pd.node.Connect(addr); err != nil {
			slog.Warn("peer discovery: connect failed", "addr", addr, "err", err)
			continue
		}
		pd.mu.Lock()
		pd.knownPeers[addr] = true
		pd.mu.Unlock()
		slog.Info("peer discovery: connected", "addr", addr)
	}
}
