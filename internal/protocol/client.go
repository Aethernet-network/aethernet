// Package protocol provides the canonical interface for submitting economically
// meaningful events to the AetherNet protocol. The marketplace and all
// application-layer code MUST use this interface for token movement. Direct
// ledger mutation is prohibited.
//
// Layer: Infrastructure — importable by any layer.
package protocol

import (
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// dagReader provides event lookup for constructing new events.
type dagReader interface {
	Get(event.EventID) (*event.Event, error)
}

// ocsSubmitter is the minimal OCS engine interface for event submission.
type ocsSubmitter interface {
	Submit(*event.Event) error
	MinEventStake() uint64
}

// localPublisher publishes locally-created events through the authoritative
// DAG-add + disseminate path. Satisfied by *localpub.Publisher.
type localPublisher interface {
	Publish(ev *event.Event) error
}

// Client is the canonical protocol interface for submitting economically
// meaningful events. Every token movement in the system flows through this
// interface, producing signed DAG events that propagate to all nodes and
// settle through the consensus pipeline.
type Client struct {
	dag       dagReader
	kp        *crypto.KeyPair
	engine    ocsSubmitter
	agentID   crypto.AgentID
	publisher localPublisher
}

// NewClient creates a protocol Client backed by the given DAG, keypair,
// and OCS engine. agentID is the node's identity used as the event signer.
func NewClient(dag dagReader, kp *crypto.KeyPair, engine ocsSubmitter, agentID crypto.AgentID) *Client {
	return &Client{
		dag:     dag,
		kp:      kp,
		engine:  engine,
		agentID: agentID,
	}
}

// SetPublisher wires the authoritative local event publisher. All locally-
// created protocol events flow through this publisher for DAG insertion and
// peer dissemination. Call before any Submit methods are used.
func (c *Client) SetPublisher(p localPublisher) {
	c.publisher = p
}

// SubmitTransfer creates a canonical Transfer event and submits it through
// the OCS engine into the DAG. The transfer settles through consensus →
// SettlementApplicator on every node.
func (c *Client) SubmitTransfer(from, to crypto.AgentID, amount uint64, reason, taskID string) (event.EventID, error) {
	payload := event.TransferPayload{
		Version:   1,
		FromAgent: string(from),
		ToAgent:   string(to),
		Amount:    amount,
		Currency:  "AET",
		Reason:    reason,
		TaskID:    taskID,
	}
	return c.submitTransferPayload(payload)
}

// SubmitEscrowLock creates a canonical Transfer from poster to the escrow
// bucket for a task. Reason is set to "escrow-lock".
func (c *Client) SubmitEscrowLock(posterID crypto.AgentID, taskID string, amount uint64) (event.EventID, error) {
	return c.SubmitTransfer(posterID, crypto.AgentID("escrow:"+taskID), amount, "escrow-lock", taskID)
}

// SubmitEscrowRelease creates a canonical Transfer from escrow bucket to
// recipient. Reason is set to "escrow-release".
func (c *Client) SubmitEscrowRelease(taskID string, recipientID crypto.AgentID, amount uint64) (event.EventID, error) {
	return c.SubmitTransfer(crypto.AgentID("escrow:"+taskID), recipientID, amount, "escrow-release", taskID)
}

// SubmitRefund creates a canonical Transfer from escrow bucket back to
// poster. Reason is set to "task-refund".
func (c *Client) SubmitRefund(taskID string, posterID crypto.AgentID, amount uint64) (event.EventID, error) {
	return c.SubmitTransfer(crypto.AgentID("escrow:"+taskID), posterID, amount, "task-refund", taskID)
}

// SubmitGrant creates a canonical Transfer from a genesis bucket to an
// agent. Reason is set to the provided reason (e.g. "onboarding-grant").
func (c *Client) SubmitGrant(fromBucket string, toAgent crypto.AgentID, amount uint64, reason string) (event.EventID, error) {
	return c.SubmitTransfer(crypto.AgentID(fromBucket), toAgent, amount, reason, "")
}

func (c *Client) submitTransferPayload(payload event.TransferPayload) (event.EventID, error) {
	// Protocol transfers (escrow locks, releases, grants, fee distributions)
	// use empty causal refs. This guarantees materialization on every remote
	// node without waiting for unrelated events. The transfer payload itself
	// contains the semantic context (from/to/reason/task_id).
	ev, err := event.New(
		event.EventTypeTransfer,
		nil,
		payload,
		string(c.agentID),
		nil,
		c.engine.MinEventStake(),
	)
	if err != nil {
		return "", fmt.Errorf("protocol: create transfer event: %w", err)
	}

	if err := crypto.SignEvent(ev, c.kp); err != nil {
		return "", fmt.Errorf("protocol: sign transfer event: %w", err)
	}

	// OCS submission must happen BEFORE publication so the pending queue
	// has the event when peers receive it and start voting.
	if err := c.engine.Submit(ev); err != nil {
		return "", fmt.Errorf("protocol: submit transfer: %w", err)
	}

	// Publish: dag.Add + Fast Path + legacy broadcast (single authoritative path).
	if c.publisher != nil {
		if err := c.publisher.Publish(ev); err != nil {
			// Submit succeeded but publication failed — event is in OCS
			// pending but not yet in DAG. Recoverable on next sync.
			return ev.ID, nil
		}
	}

	return ev.ID, nil
}
