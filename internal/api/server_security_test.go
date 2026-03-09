package api_test

// Security tests for the API server.
//
// These tests verify:
//   - CRITICAL-1.1: from_agent override in POST /v1/transfer is ignored; the
//     sender is always the node's own keypair identity.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// TestHandleTransfer_FromAgentOverrideIgnored verifies that a transfer request
// containing a from_agent field pointing to a different agent is silently
// overridden to use the node's own identity (CRITICAL-1.1).
//
// Without this fix, an attacker could set from_agent=victim to create a
// TransferPayload that debits the victim's balance.
func TestHandleTransfer_FromAgentOverrideIgnored(t *testing.T) {
	setup := newTestSetup(t)

	// Fund the node's own agentID so the OCS engine has non-zero supply data.
	nodeAgentID := setup.kp.AgentID()
	if err := setup.tl.FundAgent(nodeAgentID, 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	// POST a transfer that attempts to spoof from_agent.
	body := map[string]any{
		"to_agent":   "innocent-recipient",
		"from_agent": "attacker-controlled-victim", // must be ignored
		"amount":     1,
		"currency":   "AET",
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(setup.ts.URL+"/v1/transfer", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /v1/transfer: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	var evResp struct {
		EventID string `json:"event_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&evResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if evResp.EventID == "" {
		t.Fatal("empty event_id in response")
	}

	// Retrieve the created event from the DAG and verify FromAgent.
	all := setup.d.All()
	var found *event.Event
	for _, e := range all {
		if string(e.ID) == evResp.EventID {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatalf("event %q not found in DAG", evResp.EventID)
	}

	tp, err := event.GetPayload[event.TransferPayload](found)
	if err != nil {
		t.Fatalf("GetPayload: %v", err)
	}

	// The FromAgent in the event must be the node's own identity.
	if tp.FromAgent == "attacker-controlled-victim" {
		t.Error("from_agent override was applied; CRITICAL vulnerability still present (CRITICAL-1.1)")
	}
	if tp.FromAgent != string(nodeAgentID) {
		t.Errorf("FromAgent = %q, want node identity %q", tp.FromAgent, nodeAgentID)
	}
}
