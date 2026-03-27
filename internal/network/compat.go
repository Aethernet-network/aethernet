// compat.go implements V1/V2 capability negotiation for AetherNet peers.
//
// After the V1 handshake completes, both sides optionally exchange HelloV2
// messages. If the remote peer does not send HelloV2 within the negotiation
// window (or sends an incompatible version), the peer is classified as
// legacy V1 and only V1 messages are sent to it.
//
// This file does NOT change sync behavior. It provides the classification
// and gating primitives that subsequent prompts use to route messages.
package network

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// v2NegotiationTimeout is how long after the V1 handshake completes we wait
// for a HelloV2 from the remote peer before classifying it as legacy.
const v2NegotiationTimeout = 5 * time.Second

// FastPathFeature constants used in HelloV2.Features.
const (
	FeatureFastPath = "fast_path"
	FeatureBodySplit = "body_split"
	FeatureRepair    = "repair"
)

// SupportsFastPath returns true if this peer has completed V2 negotiation
// and advertises the fast_path feature.
func (p *Peer) SupportsFastPath() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.v2Negotiated {
		return false
	}
	for _, f := range p.v2Features {
		if f == FeatureFastPath {
			return true
		}
	}
	return false
}

// IsLegacySyncOnly returns true if this peer does not support V2 fast path
// and should use MsgRequestSync/MsgSyncBatch exclusively.
func (p *Peer) IsLegacySyncOnly() bool {
	return !p.SupportsFastPath()
}

// ProtocolVersion returns the negotiated protocol version for this peer.
// 0 means V1-only (legacy), 2 means Fast Path v1.
func (p *Peer) ProtocolVersion() uint32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.protocolVersion
}

// SetV2Capabilities records a successful V2 negotiation result on the peer.
// Called after HelloV2 exchange completes successfully.
func (p *Peer) SetV2Capabilities(hello HelloV2) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.protocolVersion = hello.ProtocolVersion
	p.v2Features = hello.Features
	p.v2Negotiated = true
	if p.score == nil {
		p.score = NewPeerScore()
	}
}

// PeerScore returns the peer's quality score, or nil if not initialized.
func (p *Peer) PeerScore() *PeerScore {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.score
}

// EnsureScore initializes the score if not already set.
func (p *Peer) EnsureScore() *PeerScore {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.score == nil {
		p.score = NewPeerScore()
	}
	return p.score
}

// Quota returns the peer's rate quota, or nil if not initialized.
func (p *Peer) Quota() *PeerQuota {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.quota
}

// EnsureQuota initializes the quota if not already set.
func (p *Peer) EnsureQuota(cfg QuotaConfig) *PeerQuota {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.quota == nil {
		p.quota = NewPeerQuota(cfg)
	}
	return p.quota
}

// UpdateStatus records the most recent PeerStatus from this peer.
func (p *Peer) UpdateStatus(status PeerStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastStatus = &status
}

// LastStatus returns the most recent PeerStatus, or nil if none received.
func (p *Peer) LastStatus() *PeerStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastStatus
}

// BuildHelloV2 constructs a HelloV2 message for this node, advertising
// the full set of V2 features and the current frontier tips.
func BuildHelloV2(tips []event.EventID) HelloV2 {
	return HelloV2{
		ProtocolVersion: 2,
		Features:        []string{FeatureFastPath, FeatureBodySplit, FeatureRepair},
		MaxBodySize:     4 * 1024 * 1024, // 4 MiB
		FrontierTips:    tips,
	}
}

// SendHelloV2 sends a HelloV2 message to a peer. Returns an error if the
// message cannot be sent. Called after the V1 handshake completes.
func SendHelloV2(p *Peer, hello HelloV2) error {
	payload, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	return p.Send(Message{Type: MsgHelloV2, Payload: payload})
}

// HandleHelloV2 processes an incoming HelloV2 message from a peer. If the
// version is compatible, the peer's V2 capabilities are recorded.
// Returns true if negotiation succeeded, false if the peer is incompatible.
func HandleHelloV2(p *Peer, payload []byte) bool {
	var hello HelloV2
	if err := json.Unmarshal(payload, &hello); err != nil {
		slog.Warn("network: failed to decode HelloV2", "peer", p.AgentID, "err", err)
		return false
	}
	if hello.ProtocolVersion < 2 {
		slog.Info("network: peer advertised unsupported protocol version",
			"peer", p.AgentID, "version", hello.ProtocolVersion)
		return false
	}
	p.SetV2Capabilities(hello)
	slog.Info("network: V2 negotiation succeeded",
		"peer", p.AgentID,
		"version", hello.ProtocolVersion,
		"features", hello.Features)
	return true
}

// IsV2Message returns true if the message type belongs to the V2 protocol.
// V2 messages must NOT be sent to peers that have not completed V2 negotiation.
func IsV2Message(msgType MessageType) bool {
	switch msgType {
	case MsgEventHeader, MsgFrontier, MsgWindowDigest, MsgCheckpoint,
		MsgEventBody, MsgBodyRequest,
		MsgRepairRequest, MsgRepairResponse,
		MsgHelloV2, MsgPeerStatus, MsgAckHint, MsgNackMissing, MsgOverloaded:
		return true
	default:
		return false
	}
}

// SafeSend sends a message to a peer, checking V2 gating. If the message is
// a V2 type and the peer has not negotiated V2, the message is silently
// dropped and false is returned. V1 messages are always sent.
func SafeSend(p *Peer, msg Message) bool {
	if IsV2Message(msg.Type) && !p.SupportsFastPath() {
		return false
	}
	_ = p.Send(msg)
	return true
}
