package network

import (
	"log/slog"
	"math/rand"
	"net"
	"sync"
	"time"
)

// maxReconnectJitter is the maximum random delay before each reconnection
// attempt. Prevents a thundering herd when all peers disconnect simultaneously
// (e.g. during a load test) — each reconnect is staggered by 0-5 seconds.
const maxReconnectJitter = 5 * time.Second

// PeerDiscovery periodically resolves a DNS name and connects to any peer
// whose IP is not already in the node's live peer set. It is designed for
// service-discovery environments such as AWS Cloud Map where peer IPs change
// on every container restart.
//
// Unlike a cache-based approach, PeerDiscovery checks the node's actual
// connected peers (by IP) on every cycle. This avoids stale-cache issues
// caused by ephemeral-port mismatches between inbound and outbound addresses
// or Cloud Map DNS propagation lag during rolling deploys.
//
// Lifecycle: call Start to begin periodic resolution, Stop to shut down.
// Stop is idempotent. PeerDiscovery is safe for concurrent use.
type PeerDiscovery struct {
	dnsName  string
	port     string
	node     *Node
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once

	// firstResolved tracks whether we've completed at least one DNS cycle.
	// Reconnection jitter is applied only after the first cycle so that
	// initial boot-time discovery is fast (no jitter).
	firstResolved bool
	mu            sync.Mutex

	// resolver performs host lookup. Defaults to net.LookupHost; may be
	// replaced in tests to inject a mock address list.
	resolver func(host string) ([]string, error)
}

// NewPeerDiscovery creates a PeerDiscovery that resolves dnsName every
// interval and dials any new IPs on port.
func NewPeerDiscovery(dnsName, port string, node *Node, interval time.Duration) *PeerDiscovery {
	return &PeerDiscovery{
		dnsName:  dnsName,
		port:     port,
		node:     node,
		interval: interval,
		stopCh:   make(chan struct{}),
		resolver: net.LookupHost,
	}
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

// resolve performs one DNS lookup and connects to any address whose IP is
// not already in the node's live peer set. This checks actual connected
// peers — not a cache — so ephemeral-port mismatches and stale entries
// cannot prevent reconnection.
func (pd *PeerDiscovery) resolve() {
	addrs, err := pd.resolver(pd.dnsName)
	if err != nil {
		slog.Warn("peer discovery: DNS lookup failed",
			"name", pd.dnsName, "err", err)
		return
	}
	slog.Debug("peer discovery: resolved", "name", pd.dnsName, "count", len(addrs))

	// Snapshot the node's currently connected peer IPs.
	connectedIPs := pd.node.PeerIPs()

	for _, ip := range addrs {
		addr := net.JoinHostPort(ip, pd.port)

		if pd.node.isSelfAddr(addr) {
			continue
		}

		// Check the live peer set by IP, not a cache. This handles the
		// inbound-vs-outbound port mismatch: an inbound peer from
		// 172.31.6.199:35014 is still recognized as "connected to 172.31.6.199".
		if connectedIPs[ip] {
			continue
		}

		// Stagger reconnection attempts after the first cycle to avoid
		// thundering herd. Initial boot discovery is jitter-free.
		pd.mu.Lock()
		needsJitter := pd.firstResolved
		pd.mu.Unlock()
		if needsJitter {
			jitter := time.Duration(rand.Int63n(int64(maxReconnectJitter)))
			if jitter > 0 {
				time.Sleep(jitter)
			}
		}

		slog.Info("peer discovery: connecting to peer", "addr", addr)
		if _, err := pd.node.Connect(addr); err != nil {
			slog.Warn("peer discovery: connect failed", "addr", addr, "err", err)
			continue
		}
		slog.Info("peer discovery: connected", "addr", addr)
	}

	pd.mu.Lock()
	pd.firstResolved = true
	pd.mu.Unlock()
}
