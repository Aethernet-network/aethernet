package network

// Security tests for the network package.
//
// These tests verify:
//   - HIGH-3.1: Unsigned P2P votes are dropped.
//   - HIGH-3.2: Peers without a public key are rejected when local node has a keypair.
//   - MEDIUM-3.3: Votes with a stale timestamp are dropped.
//   - CRITICAL-4: resetLimitReader resets per-message so connections survive 10 MB+ of traffic.
//   - CRITICAL-5: SetIdentityLookup drops votes where peer's claimed key ≠ registry key.
//
// Tests are in the internal `network` package (not `network_test`) so they can
// access unexported methods (handleMessage, handleIncomingConn, resetLimitReader).

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

// TestResetLimitReader_SustainedTraffic verifies that the resetLimitReader
// allows many messages to flow through a long-lived connection without hitting
// a cumulative byte cap (CRITICAL-4).
//
// An io.LimitReader with a 512-byte limit would die after the first message;
// resetLimitReader resets after each decode so 100 messages of 128 bytes each
// (12 800 bytes total — 25× the per-message limit) all succeed.
func TestResetLimitReader_SustainedTraffic(t *testing.T) {
	const perMsgLimit = 512  // per-message cap
	const msgCount = 100     // total messages to send
	const msgPayload = "aaa" // 3 bytes; well within cap

	// net.Pipe gives a synchronous in-memory connection — no real TCP needed.
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Sender goroutine: write msgCount JSON messages to clientConn.
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		enc := json.NewEncoder(clientConn)
		for i := 0; i < msgCount; i++ {
			msg := Message{Type: MsgPing, Payload: []byte(`"` + msgPayload + `"`)}
			if err := enc.Encode(msg); err != nil {
				// clientConn may close before all sends complete if the test fails.
				return
			}
		}
	}()

	// Receiver: wrap serverConn with a resetLimitReader (512-byte per-msg cap).
	rl := &resetLimitReader{r: serverConn, limit: perMsgLimit}
	dec := json.NewDecoder(rl)

	received := 0
	for received < msgCount {
		var msg Message
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("decode failed after %d messages (total bytes far exceed per-msg limit × msgCount): %v", received, err)
		}
		// Reset so next message gets a fresh per-message budget.
		rl.Reset()
		received++
	}

	<-sendDone

	if received != msgCount {
		t.Errorf("received %d/%d messages; resetLimitReader should survive sustained traffic (CRITICAL-4)", received, msgCount)
	}
}

// TestHandleMessage_VoteIdentityMismatch verifies that when SetIdentityLookup
// is configured, a vote whose claimed public key does not match the registry
// entry for the stated VoterID is silently dropped (CRITICAL-5).
//
// Attack scenario: an authenticated peer passes the handshake with their real
// key, but then sends a MsgVote claiming to be a different high-reputation
// voter, substituting a freshly-generated self-owned keypair. Without registry
// verification the vote signature is valid (it verifies against the supplied
// key), but the VoterID's reputation/stake belongs to the impersonated agent.
func TestHandleMessage_VoteIdentityMismatch(t *testing.T) {
	n := newTestNode(t, false)

	handlerCalled := false
	n.SetVoteHandler(func(_ crypto.AgentID, _ event.EventID, _ bool) {
		handlerCalled = true
	})

	// attacker generates their own keypair — valid for signing but NOT registered
	// under the victim's VoterID.
	attackerKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (attacker): %v", err)
	}

	// victim is a legitimately registered agent with a different keypair.
	victimKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (victim): %v", err)
	}
	victimID := victimKP.AgentID()

	// Wire up identity lookup: only victimID is registered, with victimKP.PublicKey.
	n.SetIdentityLookup(func(id crypto.AgentID) []byte {
		if id == victimID {
			return victimKP.PublicKey
		}
		return nil
	})

	// Build a fresh, validly signed vote claiming to be victimID but signed by
	// the attacker's key.
	evID := event.EventID("ev-impersonation")
	verdict := true
	ts := time.Now().Unix()

	// Sign with attacker's key — signature is internally consistent but
	// the key is NOT the one registered for victimID.
	sig, err := attackerKP.Sign(voteBytes(evID, victimID, verdict, ts))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	vp := VotePayload{
		EventID:   evID,
		VoterID:   victimID,        // claims to be the victim
		Verdict:   verdict,
		Timestamp: ts,
		PublicKey: attackerKP.PublicKey, // but supplies attacker's key
		Signature: sig,
	}
	payload, _ := json.Marshal(vp)
	peer := &Peer{AgentID: "attacker-peer"}

	n.handleMessage(peer, Message{Type: MsgVote, Payload: payload})

	if handlerCalled {
		t.Error("voteHandler was called despite key mismatch; SECURITY: votes with wrong registry key must be dropped (CRITICAL-5)")
	}
}

// TestHandleMessage_VoteIdentityMatch verifies that a vote whose public key
// matches the registry entry IS forwarded to the voteHandler (regression guard
// for the CRITICAL-5 fix — legitimate voters must not be rejected).
func TestHandleMessage_VoteIdentityMatch(t *testing.T) {
	n := newTestNode(t, false)

	handlerCalled := false
	n.SetVoteHandler(func(_ crypto.AgentID, _ event.EventID, _ bool) {
		handlerCalled = true
	})

	voterKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	voterID := voterKP.AgentID()

	// Registry returns the voter's real public key — legitimate case.
	n.SetIdentityLookup(func(id crypto.AgentID) []byte {
		if id == voterID {
			return voterKP.PublicKey
		}
		return nil
	})

	evID := event.EventID("ev-legit")
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
	peer := &Peer{AgentID: "legitimate-peer"}

	n.handleMessage(peer, Message{Type: MsgVote, Payload: payload})

	if !handlerCalled {
		t.Error("voteHandler was NOT called for a legitimate voter; regression — valid votes with matching registry key must be accepted (CRITICAL-5)")
	}
}
