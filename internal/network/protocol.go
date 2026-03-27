// protocol.go defines the Fast Path v1 wire protocol primitives for AetherNet.
//
// These structs represent the three-plane message model:
//   - Causality plane: EventHeader, FrontierSummary, WindowDigest, CheckpointSummary
//   - Body plane: EventBody, BodyRef
//   - Repair/proof plane: RepairRequest, RepairResponse
//   - Control plane: HelloV2, PeerStatus, AckHint, NackMissingParent, Overloaded
//
// Event lifecycle: Announced → Completed → Validated → Materialized.
// Relay occurs from Announced state (header only, before body is fetched).
//
// This file adds V2 message types alongside the existing V1 JSON envelope.
// No behavior change yet — these are compile-time definitions only.
package network

import (
	"encoding/json"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// ── V2 Message Types ─────────────────────────────────────────────────────────
//
// These extend the existing MessageType constants. V1 and V2 coexist on the
// same connection; version negotiation determines which set a peer supports.

const (
	// Causality plane.
	MsgEventHeader     MessageType = "v2_event_header"
	MsgFrontier        MessageType = "v2_frontier"
	MsgWindowDigest    MessageType = "v2_window_digest"
	MsgCheckpoint      MessageType = "v2_checkpoint"

	// Body plane.
	MsgEventBody       MessageType = "v2_event_body"
	MsgBodyRequest     MessageType = "v2_body_request"

	// Repair/proof plane.
	MsgRepairRequest   MessageType = "v2_repair_request"
	MsgRepairResponse  MessageType = "v2_repair_response"

	// Control plane.
	MsgHelloV2         MessageType = "v2_hello"
	MsgPeerStatus      MessageType = "v2_peer_status"
	MsgAckHint         MessageType = "v2_ack"
	MsgNackMissing     MessageType = "v2_nack_missing"
	MsgOverloaded      MessageType = "v2_overloaded"
)

// ── Causality Plane ──────────────────────────────────────────────────────────

// EventHeader is the causality-plane projection of an event. It contains all
// fields needed to establish causal ordering and verify the DAG structure
// without the full payload body. Peers relay headers at Announced state.
type EventHeader struct {
	EventID        event.EventID   `json:"event_id"`
	Type           event.EventType `json:"type"`
	CausalRefs     []event.EventID `json:"causal_refs"`
	AgentID        string          `json:"agent_id"`
	CausalTimestamp uint64         `json:"causal_timestamp"`
	StakeAmount    uint64          `json:"stake_amount"`
	BodyCommitment string          `json:"body_commitment"` // SHA-256 of canonical payload bytes
	Signature      []byte          `json:"signature,omitempty"`
}

// BodyRef identifies an event body for receiver-driven fetch. Peers include
// BodyRefs in AckHint messages to request specific bodies they are missing.
type BodyRef struct {
	EventID        event.EventID `json:"event_id"`
	BodyCommitment string        `json:"body_commitment"`
}

// FrontierSummary is a compact representation of a peer's current DAG tips.
// Exchanged during synchronization to identify divergence without full state
// comparison. Replaces the role of MsgRequestSync in V2 protocol.
type FrontierSummary struct {
	Tips           []event.EventID `json:"tips"`
	MaxTimestamp   uint64          `json:"max_timestamp"`
	EventCount     uint64          `json:"event_count"`
}

// WindowDigest summarizes a range of the DAG for repair negotiation. The
// window covers events with CausalTimestamp in [FromTimestamp, ToTimestamp].
// EventIDs is the sorted list of known event IDs in the window.
type WindowDigest struct {
	FromTimestamp  uint64          `json:"from_timestamp"`
	ToTimestamp    uint64          `json:"to_timestamp"`
	EventIDs       []event.EventID `json:"event_ids"`
}

// CheckpointSummary is a periodic, signed assertion of a peer's DAG state
// at a specific causal timestamp. Used for state-diff repair and consistency
// proof exchange.
type CheckpointSummary struct {
	Timestamp      uint64          `json:"timestamp"`
	Tips           []event.EventID `json:"tips"`
	EventCount     uint64          `json:"event_count"`
	StateHash      string          `json:"state_hash"` // SHA-256 of sorted event IDs
	NodeID         string          `json:"node_id"`
	Signature      []byte          `json:"signature,omitempty"`
}

// ── Body Plane ───────────────────────────────────────────────────────────────

// EventBody carries the payload for a previously announced event header.
// Body transfer is receiver-driven: the recipient sends a BodyRequest after
// receiving the header, and the sender responds with EventBody.
type EventBody struct {
	EventID event.EventID   `json:"event_id"`
	Payload json.RawMessage `json:"payload"`
}

// ── Repair/Proof Plane ───────────────────────────────────────────────────────

// RepairRequest asks a peer to send specific events or windows that the
// requester is missing. Bounded: at most MaxIDs event IDs per request.
type RepairRequest struct {
	// MissingIDs are specific event IDs the requester needs.
	MissingIDs     []event.EventID `json:"missing_ids,omitempty"`
	// Window requests all events in a causal timestamp range.
	FromTimestamp  uint64          `json:"from_timestamp,omitempty"`
	ToTimestamp    uint64          `json:"to_timestamp,omitempty"`
}

// MaxRepairIDs is the maximum number of event IDs in a single RepairRequest.
// Prevents a malicious peer from requesting the entire DAG in one message.
const MaxRepairIDs = 256

// RepairResponse carries requested events (full events for materialization).
type RepairResponse struct {
	Events []*event.Event `json:"events"`
	// Truncated is true if the response was capped by MaxRepairIDs.
	Truncated bool `json:"truncated,omitempty"`
}

// ── Control Plane ────────────────────────────────────────────────────────────

// HelloV2 is the V2 capability negotiation payload. Sent after (or alongside)
// the V1 handshake to advertise V2 support. If a peer does not send HelloV2
// within the handshake window, the connection falls back to V1-only.
type HelloV2 struct {
	ProtocolVersion uint32          `json:"protocol_version"` // 2 for Fast Path v1
	Features        []string        `json:"features"`         // e.g. ["fast_path", "body_split", "repair"]
	MaxBodySize     uint64          `json:"max_body_size"`    // max bytes the peer will accept for a body
	FrontierTips    []event.EventID `json:"frontier_tips,omitempty"` // optional: current tips for immediate sync
}

// PeerStatus is a periodic health signal. Peers exchange status to inform
// relay decisions (e.g., don't relay to an overloaded peer).
type PeerStatus struct {
	QueueDepth     int    `json:"queue_depth"`     // outbound message queue length
	IngestRate     int    `json:"ingest_rate"`      // events/sec ingested in the last window
	DAGEventCount  uint64 `json:"dag_event_count"`
	Healthy        bool   `json:"healthy"`
}

// AckHint acknowledges receipt of event headers and optionally requests
// bodies. This drives the receiver-initiated body fetch.
type AckHint struct {
	// Received lists event IDs whose headers were successfully received.
	Received []event.EventID `json:"received,omitempty"`
	// NeedBodies lists event IDs whose bodies the receiver wants fetched.
	NeedBodies []BodyRef `json:"need_bodies,omitempty"`
}

// NackMissingParent signals that the receiver cannot process an event header
// because one or more of its CausalRefs are unknown. The sender should
// provide repair data for the missing parents.
type NackMissingParent struct {
	EventID        event.EventID   `json:"event_id"`
	MissingParents []event.EventID `json:"missing_parents"`
}

// Overloaded signals that the sender is under load and requests the peer
// to reduce relay rate. RetryAfterMs is a suggested backoff duration.
type Overloaded struct {
	RetryAfterMs int `json:"retry_after_ms"`
}
