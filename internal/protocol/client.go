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

// dagWriter is the minimal DAG interface needed by the protocol client.
type dagWriter interface {
	Tips() []event.EventID
	Get(event.EventID) (*event.Event, error)
	Add(*event.Event) error
}

// ocsSubmitter is the minimal OCS engine interface for event submission.
type ocsSubmitter interface {
	Submit(*event.Event) error
	MinEventStake() uint64
}

// eventBroadcaster disseminates locally-created events to peer nodes.
// *network.Node satisfies this interface.
type eventBroadcaster interface {
	Broadcast(ev *event.Event) error
	SubmitLocalEvent(ev *event.Event) error
}

// Client is the canonical protocol interface for submitting economically
// meaningful events. Every token movement in the system flows through this
// interface, producing signed DAG events that propagate to all nodes and
// settle through the consensus pipeline.
type Client struct {
	dag         dagWriter
	kp          *crypto.KeyPair
	engine      ocsSubmitter
	agentID     crypto.AgentID
	broadcaster eventBroadcaster
}

// NewClient creates a protocol Client backed by the given DAG, keypair,
// and OCS engine. agentID is the node's identity used as the event signer.
func NewClient(dag dagWriter, kp *crypto.KeyPair, engine ocsSubmitter, agentID crypto.AgentID) *Client {
	return &Client{
		dag:     dag,
		kp:      kp,
		engine:  engine,
		agentID: agentID,
	}
}

// SetBroadcaster wires the network layer so protocol events are disseminated
// to peer nodes after DAG insertion. Without a broadcaster, events exist only
// in the local DAG and OCS pending queue — peers never learn about them.
func (c *Client) SetBroadcaster(b eventBroadcaster) {
	c.broadcaster = b
}

// SubmitTransfer creates a canonical Transfer event and submits it through
// the OCS engine into the DAG. The transfer settles through consensus →
// SettlementApplicator on every node.
func (c *Client) SubmitTransfer(from, to crypto.AgentID, amount uint64, reason, taskID string) (event.EventID, error) {
	payload := event.TransferPayload{
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
	tips := c.dag.Tips()
	priorTS := make(map[event.EventID]uint64, len(tips))
	for _, ref := range tips {
		if ev, err := c.dag.Get(ref); err == nil {
			priorTS[ref] = ev.CausalTimestamp
		}
	}

	ev, err := event.New(
		event.EventTypeTransfer,
		tips,
		payload,
		string(c.agentID),
		priorTS,
		c.engine.MinEventStake(),
	)
	if err != nil {
		return "", fmt.Errorf("protocol: create transfer event: %w", err)
	}

	if err := crypto.SignEvent(ev, c.kp); err != nil {
		return "", fmt.Errorf("protocol: sign transfer event: %w", err)
	}

	if err := c.engine.Submit(ev); err != nil {
		return "", fmt.Errorf("protocol: submit transfer: %w", err)
	}

	if err := c.dag.Add(ev); err != nil {
		// Submit succeeded but DAG add failed — event is in pending queue
		// but not yet in DAG. This is recoverable on next sync.
		return ev.ID, nil
	}

	// Broadcast to peers so they can add to their OCS pending queues and
	// vote on the event. Without this, the event exists only locally and
	// consensus can never finalize.
	if c.broadcaster != nil {
		_ = c.broadcaster.SubmitLocalEvent(ev)
		_ = c.broadcaster.Broadcast(ev)
	}

	return ev.ID, nil
}
