// Package network implements the AetherNet peer-to-peer transport layer.
//
// Two design principles guide this package:
//
//  1. Newline-delimited JSON framing — each Message is written as a single
//     JSON object followed by a newline. json.Encoder and json.Decoder handle
//     the framing automatically, giving a self-delimiting, human-readable
//     wire format that is trivial to inspect with standard tools.
//
//  2. Lock discipline — the Peer mutex guards only exported state fields
//     (AgentID, Address, State, lastSeen). The encoder and decoder are never
//     accessed concurrently: enc is owned exclusively by writeLoop and the
//     handshake, dec is owned exclusively by readLoop and the handshake.
//     The send channel is the boundary between goroutines.
//
//  3. Per-message size limiting — the resetLimitReader wraps the connection
//     with a per-message byte cap that resets after each successful JSON decode.
//     Unlike io.LimitReader (which caps cumulative lifetime bytes and kills
//     long-lived connections after 4 MiB of legitimate traffic), this approach
//     limits each individual message independently (CRITICAL-4).
package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// sendBufSize is the capacity of each Peer's outbound message channel.
// Sized to absorb a burst of outgoing messages without blocking the caller
// while writeLoop drains them to the network.
const sendBufSize = 64

// PeerState is the lifecycle state of a Peer connection.
type PeerState int

const (
	// PeerConnecting is the state from construction until handshake completes.
	PeerConnecting PeerState = iota
	// PeerConnected is the normal operating state after a successful handshake.
	PeerConnected
	// PeerDisconnected is the terminal state after Close is called.
	PeerDisconnected
)

// MessageType identifies the purpose of a wire message.
type MessageType string

const (
	// MsgHandshake is the first message exchanged; carries AgentID and version.
	MsgHandshake MessageType = "handshake"
	// MsgEvent carries a single serialised event.Event for DAG ingestion.
	MsgEvent MessageType = "event"
	// MsgRequestSync asks the peer to send its current DAG tips.
	MsgRequestSync MessageType = "sync_request"
	// MsgSyncBatch carries a batch of events in response to MsgRequestSync.
	MsgSyncBatch MessageType = "sync_batch"
	// MsgPing is a keepalive probe.
	MsgPing MessageType = "ping"
	// MsgPong is the reply to MsgPing; updates the sender's lastSeen timestamp.
	MsgPong MessageType = "pong"
	// MsgVote carries a single validator vote for use in the OCS consensus round.
	MsgVote MessageType = "vote"
)

// maxMsgBytes is the per-message read limit applied to the P2P decoder.
// Messages exceeding this bound cause the decoder to return io.EOF for that
// message, which terminates the connection (CRITICAL-4: prevent memory
// exhaustion from oversized messages). Unlike the old io.LimitReader approach,
// resetLimitReader resets after each decode so the limit is per-message, not
// cumulative over the connection lifetime.
const maxMsgBytes = 4 * 1024 * 1024 // 4 MiB

// resetLimitReader wraps an io.Reader with a per-message byte limit. After each
// successful json.Decode, the caller calls Reset() to give the next message a
// fresh budget. This prevents both oversized-message DoS (limit is enforced per
// message) and the connection-kill bug of io.LimitReader (cumulative limit).
type resetLimitReader struct {
	r     io.Reader
	limit int64
	n     int64 // bytes consumed since last Reset
}

func (rl *resetLimitReader) Read(p []byte) (int, error) {
	if rl.n >= rl.limit {
		return 0, io.EOF
	}
	if int64(len(p)) > rl.limit-rl.n {
		p = p[:rl.limit-rl.n]
	}
	n, err := rl.r.Read(p)
	rl.n += int64(n)
	return n, err
}

// Reset clears the byte counter so the next message starts with a fresh budget.
// Call after each successful json.Decode to enforce per-message limits.
func (rl *resetLimitReader) Reset() {
	rl.n = 0
}

// VotePayload is the body of a MsgVote wire message. It carries the vote data
// and an Ed25519 signature over the canonical fields so that receiving nodes
// can authenticate the vote before feeding it into their local consensus round.
//
// The canonical signed byte sequence is:
//
//	[event_id bytes] | [voter_id bytes] | [0x01 if verdict=true else 0x00] | [timestamp big-endian int64]
type VotePayload struct {
	// EventID identifies the event being voted on.
	EventID event.EventID `json:"event_id"`

	// VoterID is the AgentID of the node casting the vote.
	VoterID crypto.AgentID `json:"voter_id"`

	// Verdict is true for an accept vote, false for a reject vote.
	Verdict bool `json:"verdict"`

	// Timestamp is the Unix second at which the vote was created.
	// Included in the signed byte sequence to prevent vote replay attacks
	// (MEDIUM-3.3). Receivers reject votes older than 60 seconds.
	Timestamp int64 `json:"timestamp,omitempty"`

	// PublicKey is the sender's Ed25519 public key for signature verification.
	// May be omitted when the receiver can look up the key by VoterID.
	PublicKey []byte `json:"public_key,omitempty"`

	// Signature is the Ed25519 signature over the canonical byte sequence.
	// When empty, the receiving node accepts the vote without authentication
	// (useful for testing and trusted local networks).
	Signature []byte `json:"signature,omitempty"`
}

// Message is the envelope for all wire traffic between peers.
// Type identifies the message kind; Payload carries the JSON-encoded body.
// When Payload is absent (e.g. MsgPing, MsgPong) it is nil.
type Message struct {
	Type    MessageType `json:"type"`
	Payload []byte      `json:"payload,omitempty"`
}

// HandshakePayload is exchanged immediately after a TCP connection is established.
// Both sides send their own HandshakePayload, in sequence: the connecting side
// sends first, the accepting side sends second.
//
// Challenge-response authentication: each side includes a random Challenge for the
// other to sign. The other side signs it and includes the signature in
// ChallengeResponse. Both sides also include their PublicKey so the peer can
// verify the response.
type HandshakePayload struct {
	// AgentID identifies the local node.
	AgentID crypto.AgentID `json:"agent_id"`
	// Version is the protocol version string for compatibility gating.
	Version string `json:"version"`
	// TipCount is the number of DAG tips the sender currently holds.
	TipCount int `json:"tip_count"`
	// Challenge is a random 32-byte nonce the sender wants the peer to sign.
	Challenge []byte `json:"challenge,omitempty"`
	// ChallengeResponse is this side's signature over the peer's Challenge.
	ChallengeResponse []byte `json:"challenge_response,omitempty"`
	// PublicKey is the Ed25519 public key corresponding to AgentID.
	PublicKey []byte `json:"public_key,omitempty"`
}

// SyncBatchPayload carries a set of events sent in response to MsgRequestSync.
// The receiving node attempts to add each event to its local DAG; events whose
// causal references are missing are silently skipped (the DAG enforces referential
// integrity and returns ErrMissingCausalRef in those cases).
type SyncBatchPayload struct {
	Events []*event.Event `json:"events"`
}

// Peer represents a single remote node connection. It is safe for concurrent use
// by multiple goroutines once constructed. The exported fields AgentID, Address,
// and State are readable without a lock; mutations go through Send and Close.
type Peer struct {
	// AgentID is set to the remote node's ID after a successful handshake.
	AgentID crypto.AgentID
	// Address is the TCP address of the remote end.
	Address string
	// State tracks the lifecycle stage of this connection.
	State PeerState

	conn net.Conn
	enc  *json.Encoder     // owned by writeLoop after handshake
	dec  *json.Decoder     // owned by readLoop after handshake
	rl   *resetLimitReader // per-message size limiter; Reset() called after each decode

	send        chan Message
	lastSeen    time.Time
	lastSyncReq time.Time // tracks last MsgRequestSync for per-peer rate limiting (NEW-6)
	mu          sync.RWMutex
}

// NewPeer constructs a Peer for the given connection. agentID may be empty
// if the remote identity is not yet known (pre-handshake); it is filled in
// after the handshake completes. The Peer starts in PeerConnecting state.
//
// The decoder is wrapped with io.LimitReader(maxMsgBytes) to prevent a
// malicious peer from exhausting memory by sending an oversized message
// (MEDIUM-9.1).
func NewPeer(agentID crypto.AgentID, address string, conn net.Conn) *Peer {
	return newPeerWithLimit(agentID, address, conn, maxMsgBytes)
}

// newPeerWithLimit is like NewPeer but uses a custom per-message read limit
// instead of the package-level maxMsgBytes constant. Values ≤ 0 fall back
// to maxMsgBytes.
//
// The connection is wrapped with a resetLimitReader rather than io.LimitReader.
// io.LimitReader caps TOTAL lifetime bytes across the connection and kills it
// after 4 MiB of legitimate traffic; resetLimitReader applies the cap per-message
// and resets after each decode, so long-lived connections work correctly (CRITICAL-4).
func newPeerWithLimit(agentID crypto.AgentID, address string, conn net.Conn, limitBytes int64) *Peer {
	if limitBytes <= 0 {
		limitBytes = maxMsgBytes
	}
	rl := &resetLimitReader{r: conn, limit: limitBytes}
	return &Peer{
		AgentID:  agentID,
		Address:  address,
		State:    PeerConnecting,
		conn:     conn,
		enc:      json.NewEncoder(conn),
		dec:      json.NewDecoder(rl),
		rl:       rl,
		send:     make(chan Message, sendBufSize),
		lastSeen: time.Now(),
	}
}

// Send enqueues msg for asynchronous delivery by writeLoop.
// It is non-blocking: if the send channel is full, it returns an error rather
// than blocking the caller. Returns an error if the peer is not connected.
func (p *Peer) Send(msg Message) error {
	p.mu.RLock()
	state := p.State
	p.mu.RUnlock()

	if state != PeerConnected {
		return errors.New("network: peer not connected")
	}
	select {
	case p.send <- msg:
		return nil
	default:
		return errors.New("network: peer send buffer full")
	}
}

// Close transitions the peer to PeerDisconnected and closes the underlying
// TCP connection, which unblocks any readLoop goroutine waiting on Decode.
// Close is idempotent; subsequent calls are no-ops.
func (p *Peer) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.State == PeerDisconnected {
		return
	}
	p.State = PeerDisconnected
	p.conn.Close()
}

// IsConnected reports whether the peer is in the PeerConnected state.
func (p *Peer) IsConnected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.State == PeerConnected
}

// LastSeen returns the wall-clock time of the most recent message received
// from this peer.
func (p *Peer) LastSeen() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastSeen
}

// UpdateLastSeen records the current wall-clock time as the latest activity
// timestamp for this peer. Called by the message dispatcher on every received
// message.
func (p *Peer) UpdateLastSeen() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSeen = time.Now()
}

// AllowSyncRequest reports whether a MsgRequestSync from this peer should be
// processed. Enforces a 10-second per-peer rate limit to prevent sync spam
// (NEW-6: a peer triggering continuous full DAG serialisations could cause
// significant CPU and memory pressure). Returns true and updates the timestamp
// when the request is allowed; returns false without updating when the peer has
// sent a sync request within the last 10 seconds.
func (p *Peer) AllowSyncRequest() bool {
	const syncMinInterval = 10 * time.Second
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.lastSyncReq) < syncMinInterval {
		return false
	}
	p.lastSyncReq = time.Now()
	return true
}

// writeLoop drains the send channel and writes each Message as a JSON line to
// the connection. It exits when ctx is cancelled or the send channel is closed.
// Called as a dedicated goroutine per peer; the encoder is not shared.
func (p *Peer) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-p.send:
			if !ok {
				return
			}
			if err := p.enc.Encode(msg); err != nil {
				// Write error — the connection is broken; exit and let
				// readLoop discover the same error independently.
				return
			}
		}
	}
}

// readLoop continuously decodes JSON messages from the connection and forwards
// them to incoming. It exits on any decode error (including EOF when the remote
// side closes the connection) or when ctx is cancelled. The caller is expected
// to close the connection to unblock Decode when ctx fires.
//
// After each successful decode, p.rl.Reset() is called to give the next message
// a fresh per-message byte budget (CRITICAL-4: per-message size limiting).
func (p *Peer) readLoop(ctx context.Context, incoming chan<- Message) {
	for {
		var msg Message
		if err := p.dec.Decode(&msg); err != nil {
			// EOF, connection reset, or peer closed — exit cleanly.
			return
		}
		// Reset per-message byte counter so the next message gets a fresh limit.
		p.rl.Reset()
		select {
		case incoming <- msg:
		case <-ctx.Done():
			return
		}
	}
}
