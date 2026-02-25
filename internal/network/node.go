package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/dag"
	"github.com/aethernet/core/internal/event"
)

// Sentinel errors for programmatic handling by callers.
var (
	// ErrAlreadyRunning is returned by Start when the node is already active.
	ErrAlreadyRunning = errors.New("network: node already running")

	// ErrMaxPeers is returned by Connect and acceptLoop when the peers map is full.
	ErrMaxPeers = errors.New("network: max peers reached")
)

// NodeConfig holds the tunable parameters for a Node.
type NodeConfig struct {
	// ListenAddr is the TCP address the node listens on for incoming connections.
	ListenAddr string

	// AgentID is this node's identity, broadcast in the handshake.
	AgentID crypto.AgentID

	// MaxPeers is the maximum number of simultaneous peer connections.
	MaxPeers int

	// SyncInterval controls how often the node sends MsgRequestSync to all peers.
	SyncInterval time.Duration

	// Version is the protocol version string, included in every handshake.
	Version string
}

// DefaultNodeConfig returns a NodeConfig with production-ready defaults.
func DefaultNodeConfig(agentID crypto.AgentID) *NodeConfig {
	return &NodeConfig{
		ListenAddr:   "0.0.0.0:8337",
		AgentID:      agentID,
		MaxPeers:     50,
		SyncInterval: 10 * time.Second,
		Version:      "0.1.0",
	}
}

// Node is a running AetherNet participant. It manages peer connections, drives
// the DAG sync protocol, and routes incoming messages to the local DAG.
//
// Lifecycle: call Start to begin listening, Connect to add outbound peers, and
// Stop to shut down cleanly. Node is safe for concurrent use once started.
type Node struct {
	config *NodeConfig
	dag    *dag.DAG

	peers    map[crypto.AgentID]*Peer
	incoming chan Message // reserved for future external consumers

	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
	listener net.Listener
	wg       sync.WaitGroup
}

// NewNode constructs an idle Node backed by the given DAG. Call Start to begin
// accepting connections. config must not be nil.
func NewNode(config *NodeConfig, d *dag.DAG) *Node {
	return &Node{
		config:   config,
		dag:      d,
		peers:    make(map[crypto.AgentID]*Peer),
		incoming: make(chan Message, 256),
	}
}

// Start opens the TCP listener on config.ListenAddr, then launches the
// acceptLoop and syncLoop goroutines. Returns ErrAlreadyRunning if the node
// has already been started.
func (n *Node) Start() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.cancel != nil {
		return ErrAlreadyRunning
	}

	ln, err := net.Listen("tcp", n.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("network: listen %s: %w", n.config.ListenAddr, err)
	}
	n.listener = ln

	ctx, cancel := context.WithCancel(context.Background())
	n.ctx = ctx
	n.cancel = cancel

	n.wg.Add(2)
	go n.acceptLoop()
	go n.syncLoop()

	return nil
}

// Stop cancels the node's context, closes the listener and all peer connections,
// then blocks until every goroutine has exited. After Stop returns the node may
// be restarted with Start. It is a no-op if the node is not running.
func (n *Node) Stop() {
	n.mu.Lock()
	if n.cancel == nil {
		n.mu.Unlock()
		return
	}
	cancel := n.cancel
	listener := n.listener
	n.cancel = nil
	n.listener = nil

	// Snapshot and clear the peers map while the lock is held so that no new
	// peer can be added between the clear and the close calls below.
	peers := make([]*Peer, 0, len(n.peers))
	for _, p := range n.peers {
		peers = append(peers, p)
	}
	n.peers = make(map[crypto.AgentID]*Peer)
	n.mu.Unlock()

	cancel()         // unblocks select-based goroutines (writeLoop, syncLoop, dispatchers)
	listener.Close() // unblocks acceptLoop's Accept call
	for _, p := range peers {
		p.Close() // closes conn, unblocks each readLoop's Decode call
	}

	n.wg.Wait()
}

// Connect dials the node at address, performs the two-way handshake, registers
// the peer, and starts its read/write goroutines. Returns the connected Peer on
// success, or an error if the dial, handshake, or peer-limit check fails.
func (n *Node) Connect(address string) (*Peer, error) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("network: dial %s: %w", address, err)
	}

	peer := NewPeer("", address, conn)

	// Connecting side sends its handshake first.
	tips := n.dag.Tips()
	hsPayload, err := json.Marshal(HandshakePayload{
		AgentID:  n.config.AgentID,
		Version:  n.config.Version,
		TipCount: len(tips),
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("network: marshal handshake: %w", err)
	}
	if err := peer.enc.Encode(Message{Type: MsgHandshake, Payload: hsPayload}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("network: send handshake: %w", err)
	}

	// Then read the acceptor's handshake response.
	var reply Message
	if err := peer.dec.Decode(&reply); err != nil {
		conn.Close()
		return nil, fmt.Errorf("network: receive handshake: %w", err)
	}
	if reply.Type != MsgHandshake {
		conn.Close()
		return nil, errors.New("network: expected handshake response")
	}
	var theirHS HandshakePayload
	if err := json.Unmarshal(reply.Payload, &theirHS); err != nil {
		conn.Close()
		return nil, fmt.Errorf("network: decode handshake response: %w", err)
	}

	n.mu.Lock()
	if len(n.peers) >= n.config.MaxPeers {
		n.mu.Unlock()
		conn.Close()
		return nil, ErrMaxPeers
	}
	peer.mu.Lock()
	peer.AgentID = theirHS.AgentID
	peer.State = PeerConnected
	peer.mu.Unlock()
	n.peers[theirHS.AgentID] = peer
	n.mu.Unlock()

	n.startPeerLoops(peer)
	return peer, nil
}

// Broadcast sends ev to all currently connected peers. The event is serialised
// once and the same bytes are placed in every peer's send channel. Errors from
// individual peers (e.g. buffer full) are silently ignored — the caller drives
// the DAG and must handle per-peer reliability at a higher layer.
func (n *Node) Broadcast(e *event.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("network: marshal event: %w", err)
	}
	msg := Message{Type: MsgEvent, Payload: payload}

	n.mu.RLock()
	peers := make([]*Peer, 0, len(n.peers))
	for _, p := range n.peers {
		peers = append(peers, p)
	}
	n.mu.RUnlock()

	for _, p := range peers {
		_ = p.Send(msg)
	}
	return nil
}

// PeerCount returns the number of entries currently in the peers map.
// The value is a point-in-time snapshot and may change immediately after return.
func (n *Node) PeerCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.peers)
}

// startPeerLoops launches three goroutines for peer:
//
//  1. writeLoop — drains peer.send and writes to the TCP connection.
//  2. readLoop — reads from the TCP connection and forwards to peerIncoming.
//  3. dispatcher — calls handleMessage for each message on peerIncoming.
//
// When readLoop exits (remote close or error) it closes peerIncoming and removes
// the peer from the peers map, which causes the dispatcher to exit naturally.
func (n *Node) startPeerLoops(peer *Peer) {
	peerIncoming := make(chan Message, sendBufSize)

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		peer.writeLoop(n.ctx)
	}()

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		defer close(peerIncoming) // signals dispatcher to exit
		peer.readLoop(n.ctx, peerIncoming)
		peer.Close() // idempotent; ensures conn is closed if not already
		n.mu.Lock()
		delete(n.peers, peer.AgentID)
		n.mu.Unlock()
	}()

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		for {
			select {
			case msg, ok := <-peerIncoming:
				if !ok {
					return // readLoop exited; peerIncoming closed
				}
				n.handleMessage(peer, msg)
			case <-n.ctx.Done():
				return
			}
		}
	}()
}

// acceptLoop accepts incoming TCP connections and hands each off to
// handleIncomingConn in its own goroutine. It exits when the listener
// is closed (triggered by Stop).
func (n *Node) acceptLoop() {
	defer n.wg.Done()
	for {
		conn, err := n.listener.Accept()
		if err != nil {
			select {
			case <-n.ctx.Done():
				// Normal shutdown — listener was closed by Stop.
				return
			default:
				// Transient error (e.g. EMFILE) — keep accepting.
				continue
			}
		}

		n.wg.Add(1)
		go func(c net.Conn) {
			defer n.wg.Done()
			if err := n.handleIncomingConn(c); err != nil {
				c.Close()
			}
		}(conn)
	}
}

// handleIncomingConn performs the acceptor side of the handshake and, on
// success, registers the peer and starts its loops. The acceptor reads the
// connector's handshake first, then sends its own.
func (n *Node) handleIncomingConn(conn net.Conn) error {
	peer := NewPeer("", conn.RemoteAddr().String(), conn)

	// Read the connector's handshake.
	var msg Message
	if err := peer.dec.Decode(&msg); err != nil {
		return fmt.Errorf("network: read handshake: %w", err)
	}
	if msg.Type != MsgHandshake {
		return errors.New("network: expected handshake from connecting peer")
	}
	var theirHS HandshakePayload
	if err := json.Unmarshal(msg.Payload, &theirHS); err != nil {
		return fmt.Errorf("network: decode handshake: %w", err)
	}

	// Check capacity before committing to this peer.
	n.mu.RLock()
	full := len(n.peers) >= n.config.MaxPeers
	n.mu.RUnlock()
	if full {
		return ErrMaxPeers
	}

	// Send our handshake response.
	tips := n.dag.Tips()
	hsPayload, err := json.Marshal(HandshakePayload{
		AgentID:  n.config.AgentID,
		Version:  n.config.Version,
		TipCount: len(tips),
	})
	if err != nil {
		return fmt.Errorf("network: marshal handshake: %w", err)
	}
	if err := peer.enc.Encode(Message{Type: MsgHandshake, Payload: hsPayload}); err != nil {
		return fmt.Errorf("network: send handshake: %w", err)
	}

	peer.mu.Lock()
	peer.AgentID = theirHS.AgentID
	peer.State = PeerConnected
	peer.mu.Unlock()

	n.mu.Lock()
	// Re-check capacity under the write lock to close the TOCTOU window.
	if len(n.peers) >= n.config.MaxPeers {
		n.mu.Unlock()
		return ErrMaxPeers
	}
	n.peers[theirHS.AgentID] = peer
	n.mu.Unlock()

	n.startPeerLoops(peer)
	return nil
}

// syncLoop fires every SyncInterval and sends MsgRequestSync to all connected
// peers, asking them to reply with their current DAG tips. The local node adds
// whatever events it receives in the resulting MsgSyncBatch to its own DAG.
func (n *Node) syncLoop() {
	defer n.wg.Done()
	ticker := time.NewTicker(n.config.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.mu.RLock()
			peers := make([]*Peer, 0, len(n.peers))
			for _, p := range n.peers {
				peers = append(peers, p)
			}
			n.mu.RUnlock()

			for _, p := range peers {
				_ = p.Send(Message{Type: MsgRequestSync})
			}
		}
	}
}

// handleMessage routes an inbound message to the appropriate handler.
// It is called by the per-peer dispatcher goroutine with the originating peer
// so that responses can be sent back on the same connection.
func (n *Node) handleMessage(peer *Peer, msg Message) {
	peer.UpdateLastSeen()

	switch msg.Type {
	case MsgEvent:
		// Deserialise and add to local DAG. Duplicates and missing causal refs
		// are silently ignored — the DAG's sentinel errors handle these cases.
		var e event.Event
		if err := json.Unmarshal(msg.Payload, &e); err != nil {
			return
		}
		_ = n.dag.Add(&e)

	case MsgRequestSync:
		// Respond with a batch containing all current tip events.
		// In a full implementation this would be replaced by a bloom-filter
		// based reconciliation protocol; tips are a minimal first approximation.
		tips := n.dag.Tips()
		events := make([]*event.Event, 0, len(tips))
		for _, id := range tips {
			if e, err := n.dag.Get(id); err == nil {
				events = append(events, e)
			}
		}
		payload, err := json.Marshal(SyncBatchPayload{Events: events})
		if err != nil {
			return
		}
		_ = peer.Send(Message{Type: MsgSyncBatch, Payload: payload})

	case MsgSyncBatch:
		// Add each event in the batch to the local DAG. Events whose causal
		// refs are not yet known are silently skipped (ErrMissingCausalRef).
		var batch SyncBatchPayload
		if err := json.Unmarshal(msg.Payload, &batch); err != nil {
			return
		}
		for _, e := range batch.Events {
			_ = n.dag.Add(e)
		}

	case MsgPing:
		_ = peer.Send(Message{Type: MsgPong})

	case MsgPong:
		// lastSeen already updated above via UpdateLastSeen.
	}
}
