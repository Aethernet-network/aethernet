package settlement_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/escrow"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/identity"
	"github.com/Aethernet-network/aethernet/internal/ledger"
	"github.com/Aethernet-network/aethernet/internal/settlement"
)

// ── Helpers ──────────────────────────────────────────────────────────────────

func newTransferEvent(t *testing.T, from, to string, amount, stake uint64) *event.Event {
	t.Helper()
	e, err := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{FromAgent: from, ToAgent: to, Amount: amount, Currency: "AET"},
		from, nil, stake)
	if err != nil {
		t.Fatalf("newTransferEvent: %v", err)
	}
	return e
}

func newGenerationEvent(t *testing.T, agent string, claimed, stake uint64) *event.Event {
	t.Helper()
	e, err := event.New(event.EventTypeGeneration, nil,
		event.GenerationPayload{
			GeneratingAgent:  agent,
			BeneficiaryAgent: agent,
			ClaimedValue:     claimed,
			EvidenceHash:     "sha256:test",
			TaskDescription:  "test",
		}, agent, nil, stake)
	if err != nil {
		t.Fatalf("newGenerationEvent: %v", err)
	}
	return e
}

func newApplicator(t *testing.T, events map[event.EventID]*event.Event) (*settlement.Applicator, *ledger.TransferLedger, *ledger.GenerationLedger) {
	t.Helper()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()

	lookup := func(id event.EventID) (*event.Event, error) {
		ev, ok := events[id]
		if !ok {
			return nil, fmt.Errorf("not found: %s", id)
		}
		return ev, nil
	}

	a := settlement.NewApplicator(tl, gl, reg, lookup)
	return a, tl, gl
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestApplicator_Transfer_Accepted(t *testing.T) {
	ev := newTransferEvent(t, "alice", "bob", 500, 1000)
	events := map[event.EventID]*event.Event{ev.ID: ev}
	a, tl, _ := newApplicator(t, events)

	// Record the transfer in the ledger (simulates what OCS Submit does).
	if err := tl.FundAgent(crypto.AgentID("alice"), 10000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	if err := tl.Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	sp := &settlement.SettlementPayload{
		Version:       1,
		TargetEventID: string(ev.ID),
		Verdict:       string(settlement.VerdictAccepted),
		VerifiedValue: 500,
	}

	if err := a.Apply(sp); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	state, ok := tl.GetSettlement(ev.ID)
	if !ok || state != event.SettlementSettled {
		t.Errorf("transfer settlement: got (%v, %v); want (Settled, true)", state, ok)
	}
}

func TestApplicator_Transfer_Rejected(t *testing.T) {
	ev := newTransferEvent(t, "alice", "bob", 500, 1000)
	events := map[event.EventID]*event.Event{ev.ID: ev}
	a, tl, _ := newApplicator(t, events)

	if err := tl.FundAgent(crypto.AgentID("alice"), 10000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	if err := tl.Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	sp := &settlement.SettlementPayload{
		Version:       1,
		TargetEventID: string(ev.ID),
		Verdict:       string(settlement.VerdictRejected),
	}

	if err := a.Apply(sp); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	state, ok := tl.GetSettlement(ev.ID)
	if !ok || state != event.SettlementAdjusted {
		t.Errorf("transfer rejection: got (%v, %v); want (Adjusted, true)", state, ok)
	}
}

func TestApplicator_Generation_Accepted(t *testing.T) {
	ev := newGenerationEvent(t, "worker", 5000, 1000)
	events := map[event.EventID]*event.Event{ev.ID: ev}
	a, _, gl := newApplicator(t, events)

	if err := gl.Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	sp := &settlement.SettlementPayload{
		Version:       1,
		TargetEventID: string(ev.ID),
		Verdict:       string(settlement.VerdictAccepted),
		VerifiedValue: 5000,
	}

	if err := a.Apply(sp); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	state, ok := gl.GetSettlement(ev.ID)
	if !ok || state != event.SettlementSettled {
		t.Errorf("generation settlement: got (%v, %v); want (Settled, true)", state, ok)
	}
}

func TestApplicator_Idempotent(t *testing.T) {
	ev := newTransferEvent(t, "alice", "bob", 500, 1000)
	events := map[event.EventID]*event.Event{ev.ID: ev}
	a, tl, _ := newApplicator(t, events)

	if err := tl.FundAgent(crypto.AgentID("alice"), 10000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	if err := tl.Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	sp := &settlement.SettlementPayload{
		Version:       1,
		TargetEventID: string(ev.ID),
		Verdict:       string(settlement.VerdictAccepted),
		VerifiedValue: 500,
	}

	// First call succeeds.
	if err := a.Apply(sp); err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	// Second call is a no-op.
	if err := a.Apply(sp); err != nil {
		t.Fatalf("second Apply should be idempotent, got: %v", err)
	}

	if !a.IsApplied(ev.ID) {
		t.Error("IsApplied should return true after Apply")
	}
}

func TestApplicator_DeferredTarget(t *testing.T) {
	// Event NOT in the lookup map — simulates target not yet in DAG.
	events := make(map[event.EventID]*event.Event)
	a, _, _ := newApplicator(t, events)

	sp := &settlement.SettlementPayload{
		Version:       1,
		TargetEventID: "nonexistent-event-id",
		Verdict:       string(settlement.VerdictAccepted),
		VerifiedValue: 100,
	}

	// Apply should NOT return an error — it defers the settlement.
	if err := a.Apply(sp); err != nil {
		t.Fatalf("Apply should defer, not fail: %v", err)
	}

	// Should NOT be marked as applied.
	if a.IsApplied(event.EventID("nonexistent-event-id")) {
		t.Error("deferred settlement should not be marked as applied")
	}
}

func TestSettlementPayload_SortAttestations(t *testing.T) {
	sp := &settlement.SettlementPayload{
		Attestations: []settlement.VoterAttestation{
			{VoterID: "charlie", Verdict: "accepted", Weight: 100},
			{VoterID: "alice", Verdict: "accepted", Weight: 200},
			{VoterID: "bob", Verdict: "accepted", Weight: 150},
		},
	}

	sp.SortAttestations()

	expected := []string{"alice", "bob", "charlie"}
	for i, a := range sp.Attestations {
		if a.VoterID != expected[i] {
			t.Errorf("attestation[%d].VoterID = %q; want %q", i, a.VoterID, expected[i])
		}
	}
}

func TestDeterministicSerialization(t *testing.T) {
	// Create two SettlementPayloads with attestations in different order.
	// After sorting, JSON serialization must be byte-identical.
	makePayload := func(order []string) *settlement.SettlementPayload {
		sp := &settlement.SettlementPayload{
			Version:        1,
			TargetEventID:  "target-123",
			Verdict:        "accepted",
			VerifiedValue:  1000,
			ConsensusRound: 42,
		}
		for _, id := range order {
			sp.Attestations = append(sp.Attestations, settlement.VoterAttestation{
				VoterID: id, Verdict: "accepted", Weight: 100,
			})
		}
		sp.SortAttestations()
		return sp
	}

	sp1 := makePayload([]string{"charlie", "alice", "bob"})
	sp2 := makePayload([]string{"bob", "charlie", "alice"})

	// Verify they have the same order after sorting.
	for i := range sp1.Attestations {
		if sp1.Attestations[i].VoterID != sp2.Attestations[i].VoterID {
			t.Errorf("attestation order differs at index %d: %q vs %q",
				i, sp1.Attestations[i].VoterID, sp2.Attestations[i].VoterID)
		}
	}
}

func TestApplicator_Metrics(t *testing.T) {
	ev := newTransferEvent(t, "alice", "bob", 500, 1000)
	events := map[event.EventID]*event.Event{ev.ID: ev}
	a, tl, _ := newApplicator(t, events)

	if err := tl.FundAgent(crypto.AgentID("alice"), 10000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}
	if err := tl.Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	sp := &settlement.SettlementPayload{
		Version:       1,
		TargetEventID: string(ev.ID),
		Verdict:       string(settlement.VerdictAccepted),
		VerifiedValue: 500,
	}

	_ = a.Apply(sp)
	_ = a.Apply(sp) // duplicate

	applied, duplicated, _, _ := a.Metrics()
	if applied != 1 {
		t.Errorf("applied = %d; want 1", applied)
	}
	if duplicated != 1 {
		t.Errorf("duplicated = %d; want 1", duplicated)
	}
}

// Silence the sort import for deterministic test.
var _ = sort.Strings

// TestApplicator_EscrowLockTransfer_RegistersEntry verifies the F1 fix: when
// the applicator processes a canonical escrow-lock Transfer, it records the
// escrow metadata via RegisterEscrow (not Hold) and does NOT double-debit
// the poster's balance. The canonical Transfer (RecordFromSync above) has
// already moved funds from poster to the escrow bucket; the escrow entry
// is metadata only.
func TestApplicator_EscrowLockTransfer_RegistersEntry(t *testing.T) {
	e, err := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{
			Version:   1,
			FromAgent: "poster",
			ToAgent:   "escrow:task-1",
			Amount:    300,
			Currency:  "AET",
			Reason:    "escrow-lock",
			TaskID:    "task-1",
		},
		"poster", nil, 1000)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	events := map[event.EventID]*event.Event{e.ID: e}

	a, tl, _ := newApplicator(t, events)
	esc := escrow.New(tl)
	// Stub DAGReader returns the same event map the applicator lookup uses.
	esc.SetDAGReader(&applicatorDAGStub{events: events})
	a.SetEscrowManager(esc)

	if err := tl.FundAgent(crypto.AgentID("poster"), 1000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	sp := &settlement.SettlementPayload{
		Version:       1,
		TargetEventID: string(e.ID),
		Verdict:       string(settlement.VerdictAccepted),
		VerifiedValue: 300,
	}
	if err := a.Apply(sp); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The canonical Transfer moved 300 from poster to escrow bucket — exactly once.
	posterBal, _ := tl.Balance(crypto.AgentID("poster"))
	if posterBal != 700 {
		t.Errorf("poster balance: got %d want 700 (one debit not two)", posterBal)
	}
	bucketBal, _ := tl.Balance(crypto.AgentID("escrow:task-1"))
	if bucketBal != 300 {
		t.Errorf("escrow bucket balance: got %d want 300", bucketBal)
	}

	// Escrow metadata registered.
	if !esc.IsLocked("task-1") {
		t.Error("escrow entry not registered")
	}
	got, err := esc.Get("task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FundingTransferRef != e.ID {
		t.Errorf("FundingTransferRef: got %s want %s", got.FundingTransferRef, e.ID)
	}
}

type applicatorDAGStub struct {
	events map[event.EventID]*event.Event
}

func (s *applicatorDAGStub) Get(id event.EventID) (*event.Event, error) {
	if e, ok := s.events[id]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("applicator-stub: not found: %s", id)
}
