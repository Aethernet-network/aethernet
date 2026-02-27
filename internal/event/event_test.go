package event_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aethernet/core/internal/event"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mustNew creates an event or fails the test immediately.
func mustNew(
	t *testing.T,
	et event.EventType,
	refs []event.EventID,
	payload interface{},
	agentID string,
	prior map[event.EventID]uint64,
	stake uint64,
) *event.Event {
	t.Helper()
	e, err := event.New(et, refs, payload, agentID, prior, stake)
	if err != nil {
		t.Fatalf("event.New() unexpected error: %v", err)
	}
	return e
}

// ---------------------------------------------------------------------------
// Transfer events
// ---------------------------------------------------------------------------

func TestNew_Transfer(t *testing.T) {
	payload := event.TransferPayload{
		FromAgent: "agent-alpha",
		ToAgent:   "agent-beta",
		Amount:    1_000_000,
		Currency:  "AET",
		Memo:      "payment for inference",
	}

	e := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-alpha", nil, 500)

	if e.Type != event.EventTypeTransfer {
		t.Errorf("Type = %q, want %q", e.Type, event.EventTypeTransfer)
	}
	if e.AgentID != "agent-alpha" {
		t.Errorf("AgentID = %q, want %q", e.AgentID, "agent-alpha")
	}
	if e.StakeAmount != 500 {
		t.Errorf("StakeAmount = %d, want 500", e.StakeAmount)
	}
	if e.SettlementState != event.SettlementOptimistic {
		t.Errorf("SettlementState = %q, want Optimistic", e.SettlementState)
	}
	if e.ID == "" {
		t.Error("ID must not be empty")
	}
	if len(e.Signature) != 0 {
		t.Error("Signature should be empty until explicitly signed")
	}

	p, err := event.GetPayload[event.TransferPayload](e)
	ok := err == nil
	if !ok {
		t.Fatalf("Payload type assertion to TransferPayload failed")
	}
	if p.FromAgent != "agent-alpha" {
		t.Errorf("TransferPayload.FromAgent = %q, want %q", p.FromAgent, "agent-alpha")
	}
	if p.ToAgent != "agent-beta" {
		t.Errorf("TransferPayload.ToAgent = %q, want %q", p.ToAgent, "agent-beta")
	}
	if p.Amount != 1_000_000 {
		t.Errorf("TransferPayload.Amount = %d, want 1000000", p.Amount)
	}
	if p.Currency != "AET" {
		t.Errorf("TransferPayload.Currency = %q, want AET", p.Currency)
	}
	if p.Memo != "payment for inference" {
		t.Errorf("TransferPayload.Memo = %q, want %q", p.Memo, "payment for inference")
	}
}

// ---------------------------------------------------------------------------
// Generation events
// ---------------------------------------------------------------------------

func TestNew_Generation(t *testing.T) {
	payload := event.GenerationPayload{
		GeneratingAgent:  "agent-gpu-1",
		BeneficiaryAgent: "agent-client",
		ClaimedValue:     250_000,
		EvidenceHash:     "sha256:abc123",
		TaskDescription:  "GPT-4 summarisation of 10k-word document",
	}

	e := mustNew(t, event.EventTypeGeneration, nil, payload, "agent-gpu-1", nil, 1000)

	if e.Type != event.EventTypeGeneration {
		t.Errorf("Type = %q, want Generation", e.Type)
	}

	p, err := event.GetPayload[event.GenerationPayload](e)
	ok := err == nil
	if !ok {
		t.Fatalf("Payload type assertion to GenerationPayload failed")
	}
	if p.GeneratingAgent != "agent-gpu-1" {
		t.Errorf("GenerationPayload.GeneratingAgent = %q, want %q", p.GeneratingAgent, "agent-gpu-1")
	}
	if p.BeneficiaryAgent != "agent-client" {
		t.Errorf("GenerationPayload.BeneficiaryAgent = %q, want %q", p.BeneficiaryAgent, "agent-client")
	}
	if p.ClaimedValue != 250_000 {
		t.Errorf("GenerationPayload.ClaimedValue = %d, want 250000", p.ClaimedValue)
	}
	if p.EvidenceHash != "sha256:abc123" {
		t.Errorf("GenerationPayload.EvidenceHash = %q, want %q", p.EvidenceHash, "sha256:abc123")
	}
	if p.TaskDescription != "GPT-4 summarisation of 10k-word document" {
		t.Errorf("GenerationPayload.TaskDescription mismatch")
	}
}

// ---------------------------------------------------------------------------
// Attestation events
// ---------------------------------------------------------------------------

func TestNew_Attestation(t *testing.T) {
	// Create a target event to attest against.
	target := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 100, Currency: "AET"},
		"agent-a", nil, 0)

	payload := event.AttestationPayload{
		AttestingAgent:  "agent-validator",
		TargetEventID:   target.ID,
		ClaimedAccuracy: 0.97,
		StakedAmount:    5000,
	}

	e := mustNew(t, event.EventTypeAttestation, []event.EventID{target.ID}, payload, "agent-validator", nil, 5000)

	if e.Type != event.EventTypeAttestation {
		t.Errorf("Type = %q, want Attestation", e.Type)
	}

	p, err := event.GetPayload[event.AttestationPayload](e)
	ok := err == nil
	if !ok {
		t.Fatalf("Payload type assertion to AttestationPayload failed")
	}
	if p.AttestingAgent != "agent-validator" {
		t.Errorf("AttestationPayload.AttestingAgent = %q, want %q", p.AttestingAgent, "agent-validator")
	}
	if p.TargetEventID != target.ID {
		t.Errorf("AttestationPayload.TargetEventID = %q, want %q", p.TargetEventID, target.ID)
	}
	if p.ClaimedAccuracy != 0.97 {
		t.Errorf("AttestationPayload.ClaimedAccuracy = %f, want 0.97", p.ClaimedAccuracy)
	}
	if p.StakedAmount != 5000 {
		t.Errorf("AttestationPayload.StakedAmount = %d, want 5000", p.StakedAmount)
	}
}

// ---------------------------------------------------------------------------
// Verification events
// ---------------------------------------------------------------------------

func TestNew_Verification(t *testing.T) {
	target := mustNew(t, event.EventTypeGeneration, nil,
		event.GenerationPayload{
			GeneratingAgent:  "gen-agent",
			BeneficiaryAgent: "client",
			ClaimedValue:     100,
			EvidenceHash:     "sha256:deadbeef",
			TaskDescription:  "image classification",
		},
		"gen-agent", nil, 0)

	payload := event.VerificationPayload{
		VerifyingAgent: "validator-node-7",
		TargetEventID:  target.ID,
		Verdict:        true,
		EvidenceHash:   "sha256:proof99",
		StakedAmount:   10_000,
	}

	e := mustNew(t, event.EventTypeVerification, []event.EventID{target.ID}, payload, "validator-node-7", nil, 10_000)

	if e.Type != event.EventTypeVerification {
		t.Errorf("Type = %q, want Verification", e.Type)
	}

	p, err := event.GetPayload[event.VerificationPayload](e)
	ok := err == nil
	if !ok {
		t.Fatalf("Payload type assertion to VerificationPayload failed")
	}
	if p.VerifyingAgent != "validator-node-7" {
		t.Errorf("VerificationPayload.VerifyingAgent = %q, want %q", p.VerifyingAgent, "validator-node-7")
	}
	if p.TargetEventID != target.ID {
		t.Errorf("VerificationPayload.TargetEventID mismatch")
	}
	if !p.Verdict {
		t.Error("VerificationPayload.Verdict = false, want true")
	}
	if p.EvidenceHash != "sha256:proof99" {
		t.Errorf("VerificationPayload.EvidenceHash = %q, want %q", p.EvidenceHash, "sha256:proof99")
	}
	if p.StakedAmount != 10_000 {
		t.Errorf("VerificationPayload.StakedAmount = %d, want 10000", p.StakedAmount)
	}
}

func TestNew_Verification_NegativeVerdict(t *testing.T) {
	target := mustNew(t, event.EventTypeGeneration, nil,
		event.GenerationPayload{ClaimedValue: 999, EvidenceHash: "sha256:fake"},
		"bad-agent", nil, 0)

	payload := event.VerificationPayload{
		VerifyingAgent: "honest-validator",
		TargetEventID:  target.ID,
		Verdict:        false, // the claimed work is fraudulent
		EvidenceHash:   "sha256:counter-evidence",
		StakedAmount:   8000,
	}

	e := mustNew(t, event.EventTypeVerification, []event.EventID{target.ID}, payload, "honest-validator", nil, 8000)

	p, _ := event.GetPayload[event.VerificationPayload](e)
	if p.Verdict {
		t.Error("VerificationPayload.Verdict = true, want false for negative verdict")
	}
}

// ---------------------------------------------------------------------------
// Delegation events
// ---------------------------------------------------------------------------

func TestNew_Delegation(t *testing.T) {
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	payload := event.DelegationPayload{
		DelegatorAgent:      "orchestrator-agent",
		DelegateAgent:       "sub-agent-3",
		SpendingLimit:       500_000,
		PermittedCategories: []string{"inference", "storage"},
		ExpiresAt:           expiry,
	}

	e := mustNew(t, event.EventTypeDelegation, nil, payload, "orchestrator-agent", nil, 0)

	if e.Type != event.EventTypeDelegation {
		t.Errorf("Type = %q, want Delegation", e.Type)
	}

	p, err := event.GetPayload[event.DelegationPayload](e)
	ok := err == nil
	if !ok {
		t.Fatalf("Payload type assertion to DelegationPayload failed")
	}
	if p.DelegatorAgent != "orchestrator-agent" {
		t.Errorf("DelegationPayload.DelegatorAgent = %q, want %q", p.DelegatorAgent, "orchestrator-agent")
	}
	if p.DelegateAgent != "sub-agent-3" {
		t.Errorf("DelegationPayload.DelegateAgent = %q, want %q", p.DelegateAgent, "sub-agent-3")
	}
	if p.SpendingLimit != 500_000 {
		t.Errorf("DelegationPayload.SpendingLimit = %d, want 500000", p.SpendingLimit)
	}
	if len(p.PermittedCategories) != 2 {
		t.Errorf("DelegationPayload.PermittedCategories len = %d, want 2", len(p.PermittedCategories))
	}
	if p.PermittedCategories[0] != "inference" || p.PermittedCategories[1] != "storage" {
		t.Errorf("DelegationPayload.PermittedCategories = %v, want [inference storage]", p.PermittedCategories)
	}
	if !p.ExpiresAt.Equal(expiry) {
		t.Errorf("DelegationPayload.ExpiresAt = %v, want %v", p.ExpiresAt, expiry)
	}
}

// ---------------------------------------------------------------------------
// Causal timestamp (Lamport clock) derivation
// ---------------------------------------------------------------------------

func TestNew_GenesisEvent_CausalTimestamp(t *testing.T) {
	// A genesis event has no causal references; its timestamp must be 1 (the logical origin).
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)

	if e.CausalTimestamp != 1 {
		t.Errorf("genesis CausalTimestamp = %d, want 1", e.CausalTimestamp)
	}
	if len(e.CausalRefs) != 0 {
		t.Errorf("genesis CausalRefs len = %d, want 0", len(e.CausalRefs))
	}
}

func TestNew_CausalTimestamp_SingleRef(t *testing.T) {
	// Event A is a genesis (timestamp 1). Event B references A → B's timestamp = 2.
	a := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)

	prior := map[event.EventID]uint64{a.ID: a.CausalTimestamp}
	b := mustNew(t, event.EventTypeAttestation, []event.EventID{a.ID},
		event.AttestationPayload{AttestingAgent: "v", TargetEventID: a.ID, ClaimedAccuracy: 1.0},
		"agent-v", prior, 0)

	if b.CausalTimestamp != 2 {
		t.Errorf("CausalTimestamp = %d, want 2 (max(1) + 1)", b.CausalTimestamp)
	}
}

func TestNew_CausalTimestamp_MultipleRefs(t *testing.T) {
	// Events at timestamps 3 and 7. A new event referencing both must get timestamp 8.
	prior := map[event.EventID]uint64{
		"event-id-low":  3,
		"event-id-high": 7,
	}
	refs := []event.EventID{"event-id-low", "event-id-high"}
	e := mustNew(t, event.EventTypeTransfer, refs,
		event.TransferPayload{FromAgent: "x", ToAgent: "y", Amount: 10, Currency: "AET"},
		"agent-x", prior, 0)

	if e.CausalTimestamp != 8 {
		t.Errorf("CausalTimestamp = %d, want 8 (max(3,7) + 1)", e.CausalTimestamp)
	}
}

func TestComputeCausalTimestamp_UnknownRefs(t *testing.T) {
	// Refs not present in priorTimestamps are treated as 0 — result is still 1.
	ts := event.ComputeCausalTimestamp(
		[]event.EventID{"unknown-ref"},
		map[event.EventID]uint64{},
	)
	if ts != 1 {
		t.Errorf("ComputeCausalTimestamp with unknown refs = %d, want 1", ts)
	}
}

func TestComputeCausalTimestamp_EmptyRefs(t *testing.T) {
	ts := event.ComputeCausalTimestamp([]event.EventID{}, nil)
	if ts != 1 {
		t.Errorf("ComputeCausalTimestamp with empty refs = %d, want 1", ts)
	}
}

// ---------------------------------------------------------------------------
// Content-addressed ID properties
// ---------------------------------------------------------------------------

func TestComputeID_Determinism(t *testing.T) {
	// The same inputs must always produce the same ID — content-addressing guarantee.
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 42, Currency: "AET"}

	e1 := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-a", nil, 0)
	e2 := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-a", nil, 0)

	if e1.ID != e2.ID {
		t.Errorf("same inputs produced different IDs: %q vs %q", e1.ID, e2.ID)
	}
}

func TestComputeID_Sensitivity_AgentID(t *testing.T) {
	// Changing any canonical field must produce a different ID.
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}

	e1 := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-a", nil, 0)
	e2 := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-b", nil, 0)

	if e1.ID == e2.ID {
		t.Error("different agentIDs produced the same ID")
	}
}

func TestComputeID_Sensitivity_StakeAmount(t *testing.T) {
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}

	e1 := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-a", nil, 0)
	e2 := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-a", nil, 100)

	if e1.ID == e2.ID {
		t.Error("different stakeAmounts produced the same ID")
	}
}

func TestComputeID_Sensitivity_Payload(t *testing.T) {
	p1 := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}
	p2 := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 2, Currency: "AET"}

	e1 := mustNew(t, event.EventTypeTransfer, nil, p1, "agent-a", nil, 0)
	e2 := mustNew(t, event.EventTypeTransfer, nil, p2, "agent-a", nil, 0)

	if e1.ID == e2.ID {
		t.Error("different payloads produced the same ID")
	}
}

func TestComputeID_Sensitivity_Type(t *testing.T) {
	// Two events that differ only in Type must have different IDs.
	p1 := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}

	e1 := mustNew(t, event.EventTypeTransfer, nil, p1, "agent-a", nil, 0)
	// Use the same payload struct but a different EventType to isolate the type field.
	e2 := mustNew(t, event.EventTypeGeneration, nil, p1, "agent-a", nil, 0)

	if e1.ID == e2.ID {
		t.Error("different EventTypes produced the same ID")
	}
}

func TestComputeID_Sensitivity_CausalRefs(t *testing.T) {
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}

	// e1 has no causal refs; e2 references a fake event.
	e1 := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-a", nil, 0)

	refs := []event.EventID{"some-prior-event"}
	prior := map[event.EventID]uint64{"some-prior-event": 1}
	e2 := mustNew(t, event.EventTypeTransfer, refs, payload, "agent-a", prior, 0)

	if e1.ID == e2.ID {
		t.Error("different CausalRefs produced the same ID")
	}
}

func TestComputeID_IsHexSHA256(t *testing.T) {
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)

	id := string(e.ID)
	if len(id) != 64 {
		t.Errorf("ID length = %d, want 64 (hex-encoded SHA-256)", len(id))
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("ID %q contains non-hex character %q", id, c)
			break
		}
	}
}

func TestComputeID_SettlementStateExcluded(t *testing.T) {
	// Mutating SettlementState must NOT change the ID — it is excluded from the hash.
	payload := event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"}
	e := mustNew(t, event.EventTypeTransfer, nil, payload, "agent-a", nil, 0)
	originalID := e.ID

	_ = event.Transition(e, event.SettlementSettled)

	// Recompute ID — should still match original.
	recomputed, err := event.ComputeID(e)
	if err != nil {
		t.Fatalf("ComputeID error: %v", err)
	}
	if recomputed != originalID {
		t.Errorf("ID changed after SettlementState transition: %q → %q", originalID, recomputed)
	}
}

// ---------------------------------------------------------------------------
// Initial settlement state
// ---------------------------------------------------------------------------

func TestNew_InitialSettlementState(t *testing.T) {
	types := []event.EventType{
		event.EventTypeTransfer,
		event.EventTypeGeneration,
		event.EventTypeAttestation,
		event.EventTypeVerification,
		event.EventTypeDelegation,
	}
	for _, et := range types {
		e := mustNew(t, et, nil, nil, "agent-x", nil, 0)
		if e.SettlementState != event.SettlementOptimistic {
			t.Errorf("[%s] initial SettlementState = %q, want Optimistic", et, e.SettlementState)
		}
	}
}

// ---------------------------------------------------------------------------
// Settlement state transitions — valid paths
// ---------------------------------------------------------------------------

func TestTransition_Optimistic_to_Settled(t *testing.T) {
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)

	if err := event.Transition(e, event.SettlementSettled); err != nil {
		t.Fatalf("Transition Optimistic→Settled unexpected error: %v", err)
	}
	if e.SettlementState != event.SettlementSettled {
		t.Errorf("SettlementState = %q, want Settled", e.SettlementState)
	}
}

func TestTransition_Optimistic_to_Adjusted(t *testing.T) {
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)

	if err := event.Transition(e, event.SettlementAdjusted); err != nil {
		t.Fatalf("Transition Optimistic→Adjusted unexpected error: %v", err)
	}
	if e.SettlementState != event.SettlementAdjusted {
		t.Errorf("SettlementState = %q, want Adjusted", e.SettlementState)
	}
}

func TestTransition_Settled_to_Adjusted(t *testing.T) {
	// A supermajority late challenge can still adjust a Settled event.
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)

	_ = event.Transition(e, event.SettlementSettled)

	if err := event.Transition(e, event.SettlementAdjusted); err != nil {
		t.Fatalf("Transition Settled→Adjusted unexpected error: %v", err)
	}
	if e.SettlementState != event.SettlementAdjusted {
		t.Errorf("SettlementState = %q, want Adjusted", e.SettlementState)
	}
}

// ---------------------------------------------------------------------------
// Settlement state transitions — invalid paths
// ---------------------------------------------------------------------------

func TestTransition_Optimistic_to_Optimistic_Invalid(t *testing.T) {
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)

	err := event.Transition(e, event.SettlementOptimistic)
	if err == nil {
		t.Error("expected error for Optimistic→Optimistic, got nil")
	}
	// State must be unchanged.
	if e.SettlementState != event.SettlementOptimistic {
		t.Errorf("SettlementState changed on invalid transition: %q", e.SettlementState)
	}
}

func TestTransition_Settled_to_Optimistic_Invalid(t *testing.T) {
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)
	_ = event.Transition(e, event.SettlementSettled)

	err := event.Transition(e, event.SettlementOptimistic)
	if err == nil {
		t.Error("expected error for Settled→Optimistic, got nil")
	}
	if e.SettlementState != event.SettlementSettled {
		t.Errorf("SettlementState changed on invalid transition: %q", e.SettlementState)
	}
}

func TestTransition_Settled_to_Settled_Invalid(t *testing.T) {
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)
	_ = event.Transition(e, event.SettlementSettled)

	err := event.Transition(e, event.SettlementSettled)
	if err == nil {
		t.Error("expected error for Settled→Settled, got nil")
	}
}

func TestTransition_Adjusted_to_Any_Invalid(t *testing.T) {
	// Adjusted is a terminal state — no further transitions allowed.
	e := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET"},
		"agent-a", nil, 0)
	_ = event.Transition(e, event.SettlementAdjusted)

	for _, target := range []event.SettlementState{
		event.SettlementOptimistic,
		event.SettlementSettled,
		event.SettlementAdjusted,
	} {
		if err := event.Transition(e, target); err == nil {
			t.Errorf("expected error for Adjusted→%q, got nil", target)
		}
		if e.SettlementState != event.SettlementAdjusted {
			t.Errorf("SettlementState changed from Adjusted on invalid transition to %q", target)
		}
	}
}

// ---------------------------------------------------------------------------
// Input validation
// ---------------------------------------------------------------------------

func TestNew_EmptyType_Error(t *testing.T) {
	_, err := event.New("", nil, nil, "agent-a", nil, 0)
	if err == nil {
		t.Error("expected error for empty EventType, got nil")
	}
}

func TestNew_EmptyAgentID_Error(t *testing.T) {
	_, err := event.New(event.EventTypeTransfer, nil, nil, "", nil, 0)
	if err == nil {
		t.Error("expected error for empty agentID, got nil")
	}
}

// ---------------------------------------------------------------------------
// DAG chain: multi-event causal chain
// ---------------------------------------------------------------------------

func TestDAGChain_CausalTimestamps(t *testing.T) {
	// Build a simple three-event linear chain and verify Lamport timestamps propagate.
	//
	//   genesis (ts=1) → attestation (ts=2) → verification (ts=3)

	genesis := mustNew(t, event.EventTypeGeneration, nil,
		event.GenerationPayload{
			GeneratingAgent:  "ai-node",
			BeneficiaryAgent: "client",
			ClaimedValue:     100,
			EvidenceHash:     "sha256:genesis",
			TaskDescription:  "initial inference",
		},
		"ai-node", nil, 0)

	if genesis.CausalTimestamp != 1 {
		t.Errorf("genesis CausalTimestamp = %d, want 1", genesis.CausalTimestamp)
	}

	prior := map[event.EventID]uint64{genesis.ID: genesis.CausalTimestamp}
	attest := mustNew(t, event.EventTypeAttestation, []event.EventID{genesis.ID},
		event.AttestationPayload{
			AttestingAgent:  "peer-validator",
			TargetEventID:   genesis.ID,
			ClaimedAccuracy: 0.95,
			StakedAmount:    1000,
		},
		"peer-validator", prior, 1000)

	if attest.CausalTimestamp != 2 {
		t.Errorf("attestation CausalTimestamp = %d, want 2", attest.CausalTimestamp)
	}

	prior[attest.ID] = attest.CausalTimestamp
	verify := mustNew(t, event.EventTypeVerification, []event.EventID{genesis.ID, attest.ID},
		event.VerificationPayload{
			VerifyingAgent: "validator-node",
			TargetEventID:  genesis.ID,
			Verdict:        true,
			EvidenceHash:   "sha256:reexec-proof",
			StakedAmount:   5000,
		},
		"validator-node", prior, 5000)

	if verify.CausalTimestamp != 3 {
		t.Errorf("verification CausalTimestamp = %d, want 3 (max(1,2)+1)", verify.CausalTimestamp)
	}

	// All IDs must be distinct.
	ids := map[event.EventID]struct{}{genesis.ID: {}, attest.ID: {}, verify.ID: {}}
	if len(ids) != 3 {
		t.Error("chain events must have distinct IDs")
	}
}

func TestDAGChain_ForkThenMerge(t *testing.T) {
	// Two independent branches (ts=2 each) merge into one event (ts=3).
	//
	//   genesis (ts=1) ──→ branch-A (ts=2) ──┐
	//                  └─→ branch-B (ts=2) ──┴→ merge (ts=3)

	genesis := mustNew(t, event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: "root", ToAgent: "fork", Amount: 1000, Currency: "AET"},
		"root-agent", nil, 0)

	genesisMap := map[event.EventID]uint64{genesis.ID: genesis.CausalTimestamp}

	branchA := mustNew(t, event.EventTypeAttestation, []event.EventID{genesis.ID},
		event.AttestationPayload{AttestingAgent: "peer-a", TargetEventID: genesis.ID, ClaimedAccuracy: 1.0},
		"peer-a", genesisMap, 0)

	branchB := mustNew(t, event.EventTypeAttestation, []event.EventID{genesis.ID},
		event.AttestationPayload{AttestingAgent: "peer-b", TargetEventID: genesis.ID, ClaimedAccuracy: 1.0},
		"peer-b", genesisMap, 0)

	if branchA.CausalTimestamp != 2 || branchB.CausalTimestamp != 2 {
		t.Errorf("both branches must have CausalTimestamp=2, got A=%d B=%d",
			branchA.CausalTimestamp, branchB.CausalTimestamp)
	}

	mergeMap := map[event.EventID]uint64{
		genesis.ID: genesis.CausalTimestamp,
		branchA.ID: branchA.CausalTimestamp,
		branchB.ID: branchB.CausalTimestamp,
	}
	merge := mustNew(t, event.EventTypeVerification,
		[]event.EventID{branchA.ID, branchB.ID},
		event.VerificationPayload{
			VerifyingAgent: "merge-validator",
			TargetEventID:  genesis.ID,
			Verdict:        true,
			EvidenceHash:   "sha256:merged",
			StakedAmount:   999,
		},
		"merge-validator", mergeMap, 999)

	if merge.CausalTimestamp != 3 {
		t.Errorf("merge CausalTimestamp = %d, want 3 (max(2,2)+1)", merge.CausalTimestamp)
	}
}

// ---------------------------------------------------------------------------
// Fix 4B: json.RawMessage Payload deterministic round-trip
// ---------------------------------------------------------------------------

func TestPayload_RoundTrip_Deterministic(t *testing.T) {
	// Marshal an event to JSON, unmarshal it back, and verify the ID is
	// still correct. This was broken when Payload was interface{}: after
	// a JSON round-trip, the interface{} lost its concrete type and produced
	// different canonical bytes.
	payload := event.TransferPayload{
		FromAgent: "alice",
		ToAgent:   "bob",
		Amount:    42,
		Currency:  "AET",
	}
	e := mustNew(t, event.EventTypeTransfer, nil, payload, "alice", nil, 100)
	originalID := e.ID

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var roundTripped event.Event
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	recomputedID, err := event.ComputeID(&roundTripped)
	if err != nil {
		t.Fatalf("ComputeID after round-trip: %v", err)
	}
	if recomputedID != originalID {
		t.Errorf("ID changed after JSON round-trip:\n  original:    %s\n  recomputed:  %s",
			originalID, recomputedID)
	}
}

func TestGetPayload_Transfer(t *testing.T) {
	payload := event.TransferPayload{
		FromAgent: "alice",
		ToAgent:   "bob",
		Amount:    42,
		Currency:  "AET",
	}
	e := mustNew(t, event.EventTypeTransfer, nil, payload, "alice", nil, 0)

	got, err := event.GetPayload[event.TransferPayload](e)
	if err != nil {
		t.Fatalf("GetPayload: %v", err)
	}
	if got.FromAgent != "alice" || got.ToAgent != "bob" || got.Amount != 42 {
		t.Errorf("GetPayload returned %+v, want FromAgent=alice, ToAgent=bob, Amount=42", got)
	}
}

func TestGetPayload_Generation(t *testing.T) {
	payload := event.GenerationPayload{
		GeneratingAgent:  "gpu-agent",
		BeneficiaryAgent: "client",
		ClaimedValue:     1000,
		EvidenceHash:     "sha256:abc",
		TaskDescription:  "inference",
	}
	e := mustNew(t, event.EventTypeGeneration, nil, payload, "gpu-agent", nil, 0)

	got, err := event.GetPayload[event.GenerationPayload](e)
	if err != nil {
		t.Fatalf("GetPayload: %v", err)
	}
	if got.GeneratingAgent != "gpu-agent" || got.ClaimedValue != 1000 {
		t.Errorf("GetPayload returned %+v", got)
	}
}
