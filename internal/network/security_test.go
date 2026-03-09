package network

// Security tests for the network package.
//
// These tests verify:
//   - HIGH-3.1: Unsigned P2P votes are dropped.
//   - HIGH-3.2: Peers without a public key are rejected when local node has a keypair.
//   - MEDIUM-3.3: Votes with a stale timestamp are dropped.
//
// Tests are in the internal `network` package (not `network_test`) so they can
// access unexported methods (handleMessage, handleIncomingConn).

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// newTestNode creates a minimal Node for unit testing.
func newTestNode(t *testing.T, withKeyPair bool) *Node {
	t.Helper()
	cfg := DefaultNodeConfig(crypto.AgentID("test-node-" + t.Name()))
	if withKeyPair {
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair: %v", err)
		}
		cfg.KeyPair = kp
		cfg.AgentID = kp.AgentID()
	}
	return NewNode(cfg, dag.New())
}

// TestHandleMessage_UnsignedVoteDropped verifies that a MsgVote without a
// PublicKey and Signature is dropped and the voteHandler is not invoked
// (HIGH-3.1: all remote votes must be authenticated).
func TestHandleMessage_UnsignedVoteDropped(t *testing.T) {
	n := newTestNode(t, false)

	handlerCalled := false
	n.SetVoteHandler(func(_ crypto.AgentID, _ event.EventID, _ bool) {
		handlerCalled = true
	})

	vp := VotePayload{
		EventID: event.EventID("ev-unsigned"),
		VoterID: crypto.AgentID("voter-1"),
		Verdict: true,
		// No PublicKey, no Signature — must be dropped.
	}
	payload, _ := json.Marshal(vp)
	peer := &Peer{AgentID: "remote-peer"}

	n.handleMessage(peer, Message{Type: MsgVote, Payload: payload})

	if handlerCalled {
		t.Error("voteHandler was called for an unsigned vote; SECURITY: unsigned votes must be dropped (HIGH-3.1)")
	}
}

// TestHandleMessage_StaleVoteDropped verifies that a validly signed vote with a
// timestamp older than 60 seconds is rejected to prevent replay attacks
// (MEDIUM-3.3).
func TestHandleMessage_StaleVoteDropped(t *testing.T) {
	n := newTestNode(t, true) // node with keypair so it can verify signatures

	handlerCalled := false
	n.SetVoteHandler(func(_ crypto.AgentID, _ event.EventID, _ bool) {
		handlerCalled = true
	})

	// Generate a keypair to sign the stale vote.
	voterKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	evID := event.EventID("ev-stale")
	voterID := voterKP.AgentID()
	verdict := true
	staleTimestamp := time.Now().Unix() - 120 // 2 minutes ago — well past the 60s limit

	sig, err := voterKP.Sign(voteBytes(evID, voterID, verdict, staleTimestamp))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	vp := VotePayload{
		EventID:   evID,
		VoterID:   voterID,
		Verdict:   verdict,
		Timestamp: staleTimestamp,
		PublicKey: voterKP.PublicKey,
		Signature: sig,
	}
	payload, _ := json.Marshal(vp)
	peer := &Peer{AgentID: "remote-peer"}

	n.handleMessage(peer, Message{Type: MsgVote, Payload: payload})

	if handlerCalled {
		t.Error("voteHandler was called for a stale vote; SECURITY: stale votes must be dropped (MEDIUM-3.3)")
	}
}

// TestHandleMessage_ValidSignedVoteAccepted verifies that a properly signed,
// fresh vote IS forwarded to the voteHandler (regression test for the fixes).
func TestHandleMessage_ValidSignedVoteAccepted(t *testing.T) {
	n := newTestNode(t, false)

	handlerCalled := false
	n.SetVoteHandler(func(_ crypto.AgentID, _ event.EventID, _ bool) {
		handlerCalled = true
	})

	voterKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	evID := event.EventID("ev-valid")
	voterID := voterKP.AgentID()
	verdict := true
	ts := time.Now().Unix()

	sig, err := voterKP.Sign(voteBytes(evID, voterID, verdict, ts))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	vp := VotePayload{
		EventID:   evID,
		VoterID:   voterID,
		Verdict:   verdict,
		Timestamp: ts,
		PublicKey: voterKP.PublicKey,
		Signature: sig,
	}
	payload, _ := json.Marshal(vp)
	peer := &Peer{AgentID: "remote-peer"}

	n.handleMessage(peer, Message{Type: MsgVote, Payload: payload})

	if !handlerCalled {
		t.Error("voteHandler was NOT called for a valid signed vote; regression — valid votes must be accepted")
	}
}

// TestHandshake_PeerWithoutPubkeyRejected verifies that when the local node
// has a keypair configured, a connecting peer that omits its public key in the
// handshake is rejected (HIGH-3.2).
func TestHandshake_PeerWithoutPubkeyRejected(t *testing.T) {
	// Build a node with a keypair — this enables mandatory peer authentication.
	n := newTestNode(t, true)

	// Use net.Pipe() to create a synchronous in-memory connection pair.
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()

	errCh := make(chan error, 1)
	go func() {
		err := n.handleIncomingConn(serverSide)
		serverSide.Close()
		errCh <- err
	}()

	// Client sends a handshake WITHOUT a PublicKey.
	hs := HandshakePayload{
		AgentID:  "evil-peer",
		Version:  "0.1.0",
		TipCount: 0,
		// No Challenge, no PublicKey — unauthenticated peer.
	}
	hsPayload, _ := json.Marshal(hs)
	enc := json.NewEncoder(clientSide)
	if err := enc.Encode(Message{Type: MsgHandshake, Payload: hsPayload}); err != nil {
		// If clientSide is already closed (server rejected fast), that's fine.
		t.Logf("encode handshake: %v", err)
	}

	// handleIncomingConn should return an error rejecting the unauthenticated peer.
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("handleIncomingConn accepted a peer without a public key; SECURITY: unauthenticated peers must be rejected (HIGH-3.2)")
		}
	case <-time.After(5 * time.Second):
		t.Error("timeout: handleIncomingConn did not reject the unauthenticated peer within 5s")
	}
}
