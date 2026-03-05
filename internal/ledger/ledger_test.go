package ledger_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/ledger"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTransferEvent constructs a Transfer event. refs/prior control the Lamport
// timestamp; pass nil for genesis events.
func newTransferEvent(
	t *testing.T,
	from, to string,
	amount uint64,
	refs []event.EventID,
	prior map[event.EventID]uint64,
) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeTransfer,
		refs,
		event.TransferPayload{FromAgent: from, ToAgent: to, Amount: amount, Currency: "AET"},
		from,
		prior,
		0,
	)
	if err != nil {
		t.Fatalf("newTransferEvent: %v", err)
	}
	return e
}

// newGenerationEvent constructs a Generation event.
func newGenerationEvent(
	t *testing.T,
	generating, beneficiary string,
	claimedValue uint64,
	refs []event.EventID,
	prior map[event.EventID]uint64,
) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeGeneration,
		refs,
		event.GenerationPayload{
			GeneratingAgent:  generating,
			BeneficiaryAgent: beneficiary,
			ClaimedValue:     claimedValue,
			EvidenceHash:     "sha256:test-evidence",
			TaskDescription:  "test generation task",
		},
		generating,
		prior,
		0,
	)
	if err != nil {
		t.Fatalf("newGenerationEvent: %v", err)
	}
	return e
}

// chainPrior builds the refs slice and priorTimestamps map needed to make the
// next event in a causal chain inherit the correct Lamport timestamp.
func chainPrior(events ...*event.Event) ([]event.EventID, map[event.EventID]uint64) {
	refs := make([]event.EventID, len(events))
	prior := make(map[event.EventID]uint64, len(events))
	for i, e := range events {
		refs[i] = e.ID
		prior[e.ID] = e.CausalTimestamp
	}
	return refs, prior
}

// ---------------------------------------------------------------------------
// Transfer Ledger
// ---------------------------------------------------------------------------

func TestTransferLedger_RecordAndHistory(t *testing.T) {
	tl := ledger.NewTransferLedger()

	// Fund alice so the balance check passes.
	if err := tl.FundAgent("alice", 10_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	e := newTransferEvent(t, "alice", "bob", 1_000, nil, nil)
	if err := tl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	history, err := tl.History(crypto.AgentID("alice"), 10, 0)
	if err != nil {
		t.Fatalf("History() error: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("History() len = %d, want 1", len(history))
	}
	entry := history[0]
	if entry.EventID != e.ID {
		t.Errorf("entry.EventID = %q, want %q", entry.EventID, e.ID)
	}
	if entry.FromAgent != crypto.AgentID("alice") {
		t.Errorf("entry.FromAgent = %q, want alice", entry.FromAgent)
	}
	if entry.ToAgent != crypto.AgentID("bob") {
		t.Errorf("entry.ToAgent = %q, want bob", entry.ToAgent)
	}
	if entry.Amount != 1_000 {
		t.Errorf("entry.Amount = %d, want 1000", entry.Amount)
	}
	if entry.Currency != "AET" {
		t.Errorf("entry.Currency = %q, want AET", entry.Currency)
	}
	if entry.Settlement != event.SettlementOptimistic {
		t.Errorf("entry.Settlement = %q, want Optimistic", entry.Settlement)
	}
	if entry.Timestamp != e.CausalTimestamp {
		t.Errorf("entry.Timestamp = %d, want %d", entry.Timestamp, e.CausalTimestamp)
	}
}

func TestTransferLedger_Balance_ZeroForNewAgent(t *testing.T) {
	tl := ledger.NewTransferLedger()

	bal, err := tl.Balance(crypto.AgentID("nobody"))
	if err != nil {
		t.Fatalf("Balance() error: %v", err)
	}
	if bal != 0 {
		t.Errorf("Balance(new agent) = %d, want 0", bal)
	}
}

func TestTransferLedger_Balance_SettledIncoming(t *testing.T) {
	tl := ledger.NewTransferLedger()

	// Fund alice so she can send to bob.
	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	e := newTransferEvent(t, "alice", "bob", 5_000, nil, nil)
	if err := tl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	// Optimistic inflow must not count as spendable yet.
	bal, err := tl.Balance(crypto.AgentID("bob"))
	if err != nil {
		t.Fatalf("Balance() error: %v", err)
	}
	if bal != 0 {
		t.Errorf("Balance before settle = %d, want 0 (optimistic inflow not spendable)", bal)
	}

	if err := tl.Settle(e.ID, event.SettlementSettled); err != nil {
		t.Fatalf("Settle() error: %v", err)
	}

	bal, err = tl.Balance(crypto.AgentID("bob"))
	if err != nil {
		t.Fatalf("Balance() error: %v", err)
	}
	if bal != 5_000 {
		t.Errorf("Balance after settle = %d, want 5000", bal)
	}
}

func TestTransferLedger_Balance_ReservesOptimisticOutgoing(t *testing.T) {
	tl := ledger.NewTransferLedger()

	// Fund carol so she can send to alice.
	if err := tl.FundAgent("carol", 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	// carol → alice: 3000, settled immediately.
	inbound := newTransferEvent(t, "carol", "alice", 3_000, nil, nil)
	if err := tl.Record(inbound); err != nil {
		t.Fatalf("Record inbound: %v", err)
	}
	if err := tl.Settle(inbound.ID, event.SettlementSettled); err != nil {
		t.Fatalf("Settle inbound: %v", err)
	}

	// alice → bob: 1000, optimistic (not yet settled).
	refs, prior := chainPrior(inbound)
	outbound := newTransferEvent(t, "alice", "bob", 1_000, refs, prior)
	if err := tl.Record(outbound); err != nil {
		t.Fatalf("Record outbound: %v", err)
	}

	// alice: 3000 settled in − 1000 optimistic out reserved = 2000 spendable.
	bal, err := tl.Balance(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("Balance() error: %v", err)
	}
	if bal != 2_000 {
		t.Errorf("Balance = %d, want 2000 (3000 in - 1000 optimistic reserved)", bal)
	}
}

func TestTransferLedger_PendingOutgoing_OptimisticOnly(t *testing.T) {
	tl := ledger.NewTransferLedger()

	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	// e1: alice → bob 1000, stays Optimistic.
	e1 := newTransferEvent(t, "alice", "bob", 1_000, nil, nil)
	if err := tl.Record(e1); err != nil {
		t.Fatalf("Record e1: %v", err)
	}

	// e2: alice → carol 2000, settled.
	refs, prior := chainPrior(e1)
	e2 := newTransferEvent(t, "alice", "carol", 2_000, refs, prior)
	if err := tl.Record(e2); err != nil {
		t.Fatalf("Record e2: %v", err)
	}
	if err := tl.Settle(e2.ID, event.SettlementSettled); err != nil {
		t.Fatalf("Settle e2: %v", err)
	}

	// Only the optimistic transfer counts.
	pending, err := tl.PendingOutgoing(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("PendingOutgoing() error: %v", err)
	}
	if pending != 1_000 {
		t.Errorf("PendingOutgoing = %d, want 1000 (only optimistic outgoing)", pending)
	}
}

func TestTransferLedger_DuplicateRecord_Error(t *testing.T) {
	tl := ledger.NewTransferLedger()

	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	e := newTransferEvent(t, "alice", "bob", 500, nil, nil)
	if err := tl.Record(e); err != nil {
		t.Fatalf("first Record() error: %v", err)
	}

	err := tl.Record(e)
	if !errors.Is(err, ledger.ErrDuplicateEntry) {
		t.Fatalf("duplicate Record() error = %v, want ErrDuplicateEntry", err)
	}

	// Original entry must be uncorrupted.
	history, err := tl.History(crypto.AgentID("alice"), 10, 0)
	if err != nil {
		t.Fatalf("History() error: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("History() len = %d after duplicate record, want 1", len(history))
	}
}

func TestTransferLedger_Settle_AdvancesState(t *testing.T) {
	tl := ledger.NewTransferLedger()

	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	e := newTransferEvent(t, "alice", "bob", 100, nil, nil)
	if err := tl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	// Optimistic → Settled.
	if err := tl.Settle(e.ID, event.SettlementSettled); err != nil {
		t.Fatalf("Settle(Settled) error: %v", err)
	}

	// Settled → Adjusted (supermajority late challenge).
	if err := tl.Settle(e.ID, event.SettlementAdjusted); err != nil {
		t.Fatalf("Settle(Adjusted) error: %v", err)
	}

	// Adjusted entries are excluded from balance on both sides.
	bal, err := tl.Balance(crypto.AgentID("bob"))
	if err != nil {
		t.Fatalf("Balance() error: %v", err)
	}
	if bal != 0 {
		t.Errorf("Balance = %d after Adjusted, want 0 (adjusted entries excluded)", bal)
	}
}

func TestTransferLedger_Settle_InvalidTransition_Error(t *testing.T) {
	tl := ledger.NewTransferLedger()

	if err := tl.FundAgent("alice", 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	e := newTransferEvent(t, "alice", "bob", 100, nil, nil)
	if err := tl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := tl.Settle(e.ID, event.SettlementSettled); err != nil {
		t.Fatalf("Settle(Settled) error: %v", err)
	}

	// Settled → Settled is not permitted.
	if err := tl.Settle(e.ID, event.SettlementSettled); !errors.Is(err, ledger.ErrInvalidTransition) {
		t.Errorf("Settle(Settled→Settled) = %v, want ErrInvalidTransition", err)
	}

	// Advance to the terminal state.
	if err := tl.Settle(e.ID, event.SettlementAdjusted); err != nil {
		t.Fatalf("Settle(Adjusted) error: %v", err)
	}

	// Adjusted is terminal — any further transition must fail.
	if err := tl.Settle(e.ID, event.SettlementSettled); !errors.Is(err, ledger.ErrInvalidTransition) {
		t.Errorf("Settle(Adjusted→Settled) = %v, want ErrInvalidTransition", err)
	}

	// Unknown event ID.
	if err := tl.Settle("does-not-exist", event.SettlementSettled); !errors.Is(err, ledger.ErrEntryNotFound) {
		t.Errorf("Settle(unknown) = %v, want ErrEntryNotFound", err)
	}
}

func TestTransferLedger_History_Pagination(t *testing.T) {
	tl := ledger.NewTransferLedger()
	const agent = "pager"
	const n = 5

	if err := tl.FundAgent(crypto.AgentID(agent), 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	// Build a causal chain so each of the 5 events gets a distinct Lamport
	// timestamp (1–5), making the sort order fully deterministic.
	var prev *event.Event
	for i := 0; i < n; i++ {
		var refs []event.EventID
		var prior map[event.EventID]uint64
		if prev != nil {
			refs, prior = chainPrior(prev)
		}
		e := newTransferEvent(t, agent, fmt.Sprintf("dst-%d", i), uint64(100*(i+1)), refs, prior)
		if err := tl.Record(e); err != nil {
			t.Fatalf("Record(%d) error: %v", i, err)
		}
		prev = e
	}

	// Page 1: highest 2 timestamps.
	page1, err := tl.History(crypto.AgentID(agent), 2, 0)
	if err != nil {
		t.Fatalf("History(2,0) error: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("History(2,0) len = %d, want 2", len(page1))
	}

	// Page 2: middle 2.
	page2, err := tl.History(crypto.AgentID(agent), 2, 2)
	if err != nil {
		t.Fatalf("History(2,2) error: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("History(2,2) len = %d, want 2", len(page2))
	}

	// Page 3: last 1 (partial page).
	page3, err := tl.History(crypto.AgentID(agent), 2, 4)
	if err != nil {
		t.Fatalf("History(2,4) error: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("History(2,4) len = %d, want 1", len(page3))
	}

	// No limit: all 5.
	all, err := tl.History(crypto.AgentID(agent), 0, 0)
	if err != nil {
		t.Fatalf("History(0,0) error: %v", err)
	}
	if len(all) != n {
		t.Fatalf("History(0,0) len = %d, want %d", len(all), n)
	}

	// Offset past end: empty non-nil slice.
	beyond, err := tl.History(crypto.AgentID(agent), 10, n)
	if err != nil {
		t.Fatalf("History beyond end error: %v", err)
	}
	if len(beyond) != 0 {
		t.Errorf("History beyond end len = %d, want 0", len(beyond))
	}

	// All entries across the three pages are distinct.
	seen := make(map[event.EventID]bool)
	for _, page := range [][]*ledger.TransferEntry{page1, page2, page3} {
		for _, entry := range page {
			if seen[entry.EventID] {
				t.Errorf("duplicate EventID %q across pages", entry.EventID)
			}
			seen[entry.EventID] = true
		}
	}
	if len(seen) != n {
		t.Errorf("expected %d distinct events across pages, got %d", n, len(seen))
	}
}

func TestTransferLedger_History_OrderedByTimestampDesc(t *testing.T) {
	tl := ledger.NewTransferLedger()
	const agent = "orderer"

	if err := tl.FundAgent(crypto.AgentID(agent), 100_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	// Three-event causal chain: ts=1, ts=2, ts=3.
	e1 := newTransferEvent(t, agent, "dst-1", 100, nil, nil)
	if err := tl.Record(e1); err != nil {
		t.Fatalf("Record e1: %v", err)
	}
	refs2, prior2 := chainPrior(e1)
	e2 := newTransferEvent(t, agent, "dst-2", 200, refs2, prior2)
	if err := tl.Record(e2); err != nil {
		t.Fatalf("Record e2: %v", err)
	}
	refs3, prior3 := chainPrior(e2)
	e3 := newTransferEvent(t, agent, "dst-3", 300, refs3, prior3)
	if err := tl.Record(e3); err != nil {
		t.Fatalf("Record e3: %v", err)
	}

	history, err := tl.History(crypto.AgentID(agent), 0, 0)
	if err != nil {
		t.Fatalf("History() error: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("History() len = %d, want 3", len(history))
	}

	// Expected order: e3 (ts=3), e2 (ts=2), e1 (ts=1).
	if history[0].EventID != e3.ID {
		t.Errorf("history[0] EventID = %q (ts=%d), want e3 (ts=3)", history[0].EventID, history[0].Timestamp)
	}
	if history[1].EventID != e2.ID {
		t.Errorf("history[1] EventID = %q (ts=%d), want e2 (ts=2)", history[1].EventID, history[1].Timestamp)
	}
	if history[2].EventID != e1.ID {
		t.Errorf("history[2] EventID = %q (ts=%d), want e1 (ts=1)", history[2].EventID, history[2].Timestamp)
	}
	// Verify timestamps are strictly descending.
	for i := 1; i < len(history); i++ {
		if history[i].Timestamp >= history[i-1].Timestamp {
			t.Errorf("history[%d].Timestamp %d >= history[%d].Timestamp %d (not descending)",
				i, history[i].Timestamp, i-1, history[i-1].Timestamp)
		}
	}
}

// ---------------------------------------------------------------------------
// Generation Ledger
// ---------------------------------------------------------------------------

func TestGenerationLedger_Record(t *testing.T) {
	gl := ledger.NewGenerationLedger()

	e := newGenerationEvent(t, "gen-agent", "ben-agent", 10_000, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	history, err := gl.GenerationHistory(crypto.AgentID("gen-agent"), 10, 0)
	if err != nil {
		t.Fatalf("GenerationHistory() error: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("GenerationHistory() len = %d, want 1", len(history))
	}
	entry := history[0]
	if entry.EventID != e.ID {
		t.Errorf("entry.EventID = %q, want %q", entry.EventID, e.ID)
	}
	if entry.GeneratingAgent != crypto.AgentID("gen-agent") {
		t.Errorf("entry.GeneratingAgent = %q, want gen-agent", entry.GeneratingAgent)
	}
	if entry.BeneficiaryAgent != crypto.AgentID("ben-agent") {
		t.Errorf("entry.BeneficiaryAgent = %q, want ben-agent", entry.BeneficiaryAgent)
	}
	if entry.ClaimedValue != 10_000 {
		t.Errorf("entry.ClaimedValue = %d, want 10000", entry.ClaimedValue)
	}
	if entry.VerifiedValue != 0 {
		t.Errorf("entry.VerifiedValue = %d, want 0 (not yet verified)", entry.VerifiedValue)
	}
	if entry.Settlement != event.SettlementOptimistic {
		t.Errorf("entry.Settlement = %q, want Optimistic", entry.Settlement)
	}
}

func TestGenerationLedger_Verify_SetsVerifiedValue(t *testing.T) {
	gl := ledger.NewGenerationLedger()

	e := newGenerationEvent(t, "gen-agent", "ben-agent", 10_000, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Verify(e.ID, 10_000); err != nil {
		t.Fatalf("Verify() error: %v", err)
	}

	history, err := gl.GenerationHistory(crypto.AgentID("gen-agent"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("GenerationHistory() error: %v", err)
	}
	if history[0].VerifiedValue != 10_000 {
		t.Errorf("VerifiedValue = %d, want 10000", history[0].VerifiedValue)
	}
	if history[0].Settlement != event.SettlementSettled {
		t.Errorf("Settlement = %q, want Settled", history[0].Settlement)
	}
}

func TestGenerationLedger_Verify_PartialVerification(t *testing.T) {
	gl := ledger.NewGenerationLedger()

	// Claim 1000 but validator confirms only 600.
	e := newGenerationEvent(t, "optimist", "ben-agent", 1_000, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Verify(e.ID, 600); err != nil {
		t.Fatalf("Verify(600) error: %v", err)
	}

	history, err := gl.GenerationHistory(crypto.AgentID("optimist"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("GenerationHistory() error: %v", err)
	}
	entry := history[0]
	if entry.ClaimedValue != 1_000 {
		t.Errorf("ClaimedValue = %d, want 1000 (unchanged)", entry.ClaimedValue)
	}
	if entry.VerifiedValue != 600 {
		t.Errorf("VerifiedValue = %d, want 600 (partial verification)", entry.VerifiedValue)
	}
	if entry.Settlement != event.SettlementSettled {
		t.Errorf("Settlement = %q, want Settled", entry.Settlement)
	}
}

func TestGenerationLedger_Reject_SetsAdjustedKeepsZeroVerified(t *testing.T) {
	gl := ledger.NewGenerationLedger()

	e := newGenerationEvent(t, "fraudster", "fraudster", 9_999_999, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Reject(e.ID); err != nil {
		t.Fatalf("Reject() error: %v", err)
	}

	history, err := gl.GenerationHistory(crypto.AgentID("fraudster"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("GenerationHistory() error: %v", err)
	}
	entry := history[0]
	if entry.Settlement != event.SettlementAdjusted {
		t.Errorf("Settlement = %q, want Adjusted", entry.Settlement)
	}
	if entry.VerifiedValue != 0 {
		t.Errorf("VerifiedValue = %d, want 0 (rejected claims carry no verified value)", entry.VerifiedValue)
	}

	// Adjusted is terminal; a second Reject must fail.
	if err := gl.Reject(e.ID); !errors.Is(err, ledger.ErrInvalidTransition) {
		t.Errorf("double Reject() = %v, want ErrInvalidTransition", err)
	}
}

func TestGenerationLedger_TotalVerifiedValue_SettledOnly(t *testing.T) {
	gl := ledger.NewGenerationLedger()
	gen := "multi-gen"

	// e1: Settled → should count.
	e1 := newGenerationEvent(t, gen, gen, 3_000, nil, nil)
	if err := gl.Record(e1); err != nil {
		t.Fatalf("Record e1: %v", err)
	}
	if err := gl.Verify(e1.ID, 3_000); err != nil {
		t.Fatalf("Verify e1: %v", err)
	}

	// e2: Optimistic (unverified) → must not count.
	refs2, prior2 := chainPrior(e1)
	e2 := newGenerationEvent(t, gen, gen, 7_000, refs2, prior2)
	if err := gl.Record(e2); err != nil {
		t.Fatalf("Record e2: %v", err)
	}

	// e3: Adjusted (rejected) → must not count.
	refs3, prior3 := chainPrior(e2)
	e3 := newGenerationEvent(t, gen, gen, 5_000, refs3, prior3)
	if err := gl.Record(e3); err != nil {
		t.Fatalf("Record e3: %v", err)
	}
	if err := gl.Reject(e3.ID); err != nil {
		t.Fatalf("Reject e3: %v", err)
	}

	total, err := gl.TotalVerifiedValue(24 * time.Hour)
	if err != nil {
		t.Fatalf("TotalVerifiedValue() error: %v", err)
	}
	if total != 3_000 {
		t.Errorf("TotalVerifiedValue = %d, want 3000 (only settled entry counted)", total)
	}
}

func TestGenerationLedger_TotalVerifiedValue_WindowExcludes(t *testing.T) {
	gl := ledger.NewGenerationLedger()

	e := newGenerationEvent(t, "gen-agent", "gen-agent", 5_000, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Verify(e.ID, 5_000); err != nil {
		t.Fatalf("Verify() error: %v", err)
	}

	// A 24-hour window covers the entry.
	total, err := gl.TotalVerifiedValue(24 * time.Hour)
	if err != nil {
		t.Fatalf("TotalVerifiedValue(24h) error: %v", err)
	}
	if total != 5_000 {
		t.Errorf("TotalVerifiedValue(24h) = %d, want 5000", total)
	}

	// Advance time so RecordedAt is strictly before the zero-window cutoff.
	time.Sleep(time.Millisecond)

	// A zero-duration window sets cutoff = now; RecordedAt < now → excluded.
	total, err = gl.TotalVerifiedValue(0)
	if err != nil {
		t.Fatalf("TotalVerifiedValue(0) error: %v", err)
	}
	if total != 0 {
		t.Errorf("TotalVerifiedValue(0) = %d, want 0 (entry outside zero window)", total)
	}
}

func TestGenerationLedger_ContributionScore_NoHistory(t *testing.T) {
	gl := ledger.NewGenerationLedger()

	score, err := gl.ContributionScore(crypto.AgentID("nobody"))
	if err != nil {
		t.Fatalf("ContributionScore() error: %v", err)
	}
	if score != 5000 {
		t.Errorf("ContributionScore (no history) = %d, want 5000 (neutral default)", score)
	}
}

func TestGenerationLedger_ContributionScore_Overclaiming(t *testing.T) {
	gl := ledger.NewGenerationLedger()
	gen := "overclaimer"

	// Claim 1000, only 400 verified → score = 400*10000/1000 = 4000.
	e := newGenerationEvent(t, gen, gen, 1_000, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Verify(e.ID, 400); err != nil {
		t.Fatalf("Verify() error: %v", err)
	}

	score, err := gl.ContributionScore(crypto.AgentID(gen))
	if err != nil {
		t.Fatalf("ContributionScore() error: %v", err)
	}
	if score != 4000 {
		t.Errorf("ContributionScore = %d, want 4000 (400/1000 × 10000)", score)
	}
}

func TestGenerationLedger_ContributionScore_Perfect(t *testing.T) {
	gl := ledger.NewGenerationLedger()
	gen := "accurate-agent"

	// e1: verified exactly matches claimed.
	e1 := newGenerationEvent(t, gen, gen, 2_000, nil, nil)
	if err := gl.Record(e1); err != nil {
		t.Fatalf("Record e1: %v", err)
	}
	if err := gl.Verify(e1.ID, 2_000); err != nil {
		t.Fatalf("Verify e1: %v", err)
	}

	score, err := gl.ContributionScore(crypto.AgentID(gen))
	if err != nil {
		t.Fatalf("ContributionScore() error: %v", err)
	}
	if score != 10000 {
		t.Errorf("ContributionScore = %d, want 10000 (perfect match)", score)
	}

	// e2: validator awards more than claimed — score must still be capped at 10000.
	refs, prior := chainPrior(e1)
	e2 := newGenerationEvent(t, gen, gen, 1_000, refs, prior)
	if err := gl.Record(e2); err != nil {
		t.Fatalf("Record e2: %v", err)
	}
	if err := gl.Verify(e2.ID, 1_500); err != nil { // 50% over-award
		t.Fatalf("Verify e2: %v", err)
	}

	// sumVerified=3500 > sumClaimed=3000 → ratio >1 → capped at 10000.
	score, err = gl.ContributionScore(crypto.AgentID(gen))
	if err != nil {
		t.Fatalf("ContributionScore() error: %v", err)
	}
	if score != 10000 {
		t.Errorf("ContributionScore (over-award) = %d, want 10000 (capped)", score)
	}
}

func TestGenerationLedger_DuplicateRecord_Error(t *testing.T) {
	gl := ledger.NewGenerationLedger()

	e := newGenerationEvent(t, "gen-agent", "ben-agent", 1_000, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("first Record() error: %v", err)
	}

	err := gl.Record(e)
	if !errors.Is(err, ledger.ErrDuplicateEntry) {
		t.Errorf("duplicate Record() = %v, want ErrDuplicateEntry", err)
	}

	// State must be uncorrupted: exactly one entry present.
	history, err := gl.GenerationHistory(crypto.AgentID("gen-agent"), 10, 0)
	if err != nil {
		t.Fatalf("GenerationHistory() error: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("GenerationHistory() len = %d after duplicate, want 1", len(history))
	}
}

// ---------------------------------------------------------------------------
// Supply Manager
// ---------------------------------------------------------------------------

func TestSupplyManager_CurrentSupply_NoGeneration(t *testing.T) {
	sm := ledger.NewSupplyManager(ledger.NewTransferLedger(), ledger.NewGenerationLedger())

	supply, err := sm.CurrentSupply()
	if err != nil {
		t.Fatalf("CurrentSupply() error: %v", err)
	}
	if supply != ledger.BaseSupply {
		t.Errorf("CurrentSupply = %d, want BaseSupply %d", supply, ledger.BaseSupply)
	}
}

func TestSupplyManager_CurrentSupply_WithGeneration(t *testing.T) {
	gl := ledger.NewGenerationLedger()
	sm := ledger.NewSupplyManager(ledger.NewTransferLedger(), gl)

	const verified = uint64(500_000_000) // 500 AET worth of verified work

	e := newGenerationEvent(t, "gen-agent", "ben-agent", verified, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Verify(e.ID, verified); err != nil {
		t.Fatalf("Verify() error: %v", err)
	}

	supply, err := sm.CurrentSupply()
	if err != nil {
		t.Fatalf("CurrentSupply() error: %v", err)
	}
	want := ledger.BaseSupply + verified
	if supply != want {
		t.Errorf("CurrentSupply = %d, want %d (base + verified generation)", supply, want)
	}
}

func TestSupplyManager_CurrentSupply_NeverExceedsCap(t *testing.T) {
	gl := ledger.NewGenerationLedger()
	sm := ledger.NewSupplyManager(ledger.NewTransferLedger(), gl)

	// Verified generation large enough to saturate the expansion cap.
	huge := ledger.BaseSupply * ledger.MaxSupplyMultiplier
	e := newGenerationEvent(t, "gen-agent", "ben-agent", huge, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Verify(e.ID, huge); err != nil {
		t.Fatalf("Verify() error: %v", err)
	}

	supply, err := sm.CurrentSupply()
	if err != nil {
		t.Fatalf("CurrentSupply() error: %v", err)
	}
	maxSupply := ledger.BaseSupply * ledger.MaxSupplyMultiplier
	if supply != maxSupply {
		t.Errorf("CurrentSupply = %d, want capped value %d", supply, maxSupply)
	}
	if supply > maxSupply {
		t.Errorf("CurrentSupply %d exceeds hard cap %d", supply, maxSupply)
	}
}

func TestSupplyManager_SupplyRatio_AtBase(t *testing.T) {
	sm := ledger.NewSupplyManager(ledger.NewTransferLedger(), ledger.NewGenerationLedger())

	ratio, err := sm.SupplyRatio()
	if err != nil {
		t.Fatalf("SupplyRatio() error: %v", err)
	}
	if ratio != 1.0 {
		t.Errorf("SupplyRatio = %f, want 1.0 (no expansion)", ratio)
	}
}

func TestSupplyManager_HealthMetrics_NotAtCap(t *testing.T) {
	sm := ledger.NewSupplyManager(ledger.NewTransferLedger(), ledger.NewGenerationLedger())

	h, err := sm.HealthMetrics()
	if err != nil {
		t.Fatalf("HealthMetrics() error: %v", err)
	}
	if h.AtCap {
		t.Error("HealthMetrics.AtCap = true, want false (no generation)")
	}
	if h.CurrentSupply != ledger.BaseSupply {
		t.Errorf("CurrentSupply = %d, want %d", h.CurrentSupply, ledger.BaseSupply)
	}
	if h.BaseSupply != ledger.BaseSupply {
		t.Errorf("BaseSupply = %d, want %d", h.BaseSupply, ledger.BaseSupply)
	}
	if h.ExpansionAmount != 0 {
		t.Errorf("ExpansionAmount = %d, want 0", h.ExpansionAmount)
	}
	if h.SupplyRatio != 1.0 {
		t.Errorf("SupplyRatio = %f, want 1.0", h.SupplyRatio)
	}
	if h.VerifiedGeneration != 0 {
		t.Errorf("VerifiedGeneration = %d, want 0", h.VerifiedGeneration)
	}
	if h.MeasurementWindow != ledger.MeasurementWindow {
		t.Errorf("MeasurementWindow = %v, want %v", h.MeasurementWindow, ledger.MeasurementWindow)
	}
	if h.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero")
	}
}

func TestSupplyManager_HealthMetrics_AtCap(t *testing.T) {
	gl := ledger.NewGenerationLedger()
	sm := ledger.NewSupplyManager(ledger.NewTransferLedger(), gl)

	// BaseSupply × (MaxSupplyMultiplier−1) of verified generation is exactly
	// enough to push CurrentSupply to BaseSupply × MaxSupplyMultiplier.
	capValue := ledger.BaseSupply * (ledger.MaxSupplyMultiplier - 1)
	e := newGenerationEvent(t, "gen-agent", "ben-agent", capValue, nil, nil)
	if err := gl.Record(e); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := gl.Verify(e.ID, capValue); err != nil {
		t.Fatalf("Verify() error: %v", err)
	}

	h, err := sm.HealthMetrics()
	if err != nil {
		t.Fatalf("HealthMetrics() error: %v", err)
	}
	if !h.AtCap {
		t.Error("HealthMetrics.AtCap = false, want true")
	}
	maxSupply := ledger.BaseSupply * ledger.MaxSupplyMultiplier
	if h.CurrentSupply != maxSupply {
		t.Errorf("CurrentSupply = %d, want %d", h.CurrentSupply, maxSupply)
	}
	if h.SupplyRatio != float64(ledger.MaxSupplyMultiplier) {
		t.Errorf("SupplyRatio = %f, want %v", h.SupplyRatio, float64(ledger.MaxSupplyMultiplier))
	}
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestTransferLedger_ConcurrentRecord(t *testing.T) {
	tl := ledger.NewTransferLedger()
	const goroutines = 50

	// Fund each concurrent sender so the balance check passes.
	for i := 0; i < goroutines; i++ {
		from := crypto.AgentID(fmt.Sprintf("concurrent-sender-%d", i))
		if err := tl.FundAgent(from, 100_000); err != nil {
			t.Fatalf("FundAgent %s: %v", from, err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			from := fmt.Sprintf("concurrent-sender-%d", n)
			e, err := event.New(
				event.EventTypeTransfer,
				nil,
				event.TransferPayload{FromAgent: from, ToAgent: "sink", Amount: uint64(n+1) * 100, Currency: "AET"},
				from,
				nil,
				0,
			)
			if err != nil {
				return // event.New with valid args never fails
			}
			_ = tl.Record(e)
		}(i)
	}
	wg.Wait()

	// All 50 unique events (distinct sender per goroutine) must be present in sink's history.
	all, err := tl.History(crypto.AgentID("sink"), goroutines+10, 0)
	if err != nil {
		t.Fatalf("History() error: %v", err)
	}
	if len(all) != goroutines {
		t.Errorf("History() len = %d, want %d after concurrent records", len(all), goroutines)
	}
}

func TestGenerationLedger_ConcurrentRecord(t *testing.T) {
	gl := ledger.NewGenerationLedger()
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			agentID := fmt.Sprintf("concurrent-gen-%d", n)
			e, err := event.New(
				event.EventTypeGeneration,
				nil,
				event.GenerationPayload{
					GeneratingAgent:  agentID,
					BeneficiaryAgent: agentID,
					ClaimedValue:     uint64(n+1) * 1_000,
					EvidenceHash:     fmt.Sprintf("sha256:evidence-%d", n),
					TaskDescription:  "concurrent test task",
				},
				agentID,
				nil,
				0,
			)
			if err != nil {
				return
			}
			_ = gl.Record(e)
		}(i)
	}
	wg.Wait()

	// Spot-check: goroutine 0's agent should have exactly one entry.
	history, err := gl.GenerationHistory(crypto.AgentID("concurrent-gen-0"), 10, 0)
	if err != nil {
		t.Fatalf("GenerationHistory() error: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("GenerationHistory(concurrent-gen-0) len = %d, want 1", len(history))
	}
}

// ---------------------------------------------------------------------------
// Fix 3: Balance validation and FundAgent
// ---------------------------------------------------------------------------

func TestTransferLedger_Record_ZeroBalance_Rejected(t *testing.T) {
	tl := ledger.NewTransferLedger()

	// alice has no balance — the transfer must be rejected.
	e := newTransferEvent(t, "alice", "bob", 1_000, nil, nil)
	err := tl.Record(e)
	if err == nil {
		t.Fatal("Record should reject transfer from zero-balance sender")
	}
	if !errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Errorf("want ErrInsufficientBalance, got %v", err)
	}
}

func TestTransferLedger_Record_Overdraft_Rejected(t *testing.T) {
	tl := ledger.NewTransferLedger()

	if err := tl.FundAgent("alice", 500); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	// alice has 500 but tries to send 1000.
	e := newTransferEvent(t, "alice", "bob", 1_000, nil, nil)
	err := tl.Record(e)
	if err == nil {
		t.Fatal("Record should reject overdraft transfer")
	}
	if !errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Errorf("want ErrInsufficientBalance, got %v", err)
	}
}

func TestTransferLedger_FundAgent_CreatesSettledEntry(t *testing.T) {
	tl := ledger.NewTransferLedger()

	if err := tl.FundAgent("bob", 5_000); err != nil {
		t.Fatalf("FundAgent: %v", err)
	}

	bal, err := tl.Balance(crypto.AgentID("bob"))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 5_000 {
		t.Errorf("Balance = %d, want 5000 after FundAgent", bal)
	}
}
