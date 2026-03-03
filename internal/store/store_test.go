package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/event"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/ocs"
	"github.com/aethernet/core/internal/store"
)

// openStore creates a Store backed by a fresh temp directory and registers
// t.Cleanup to close it automatically.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNewStore_CreatesDatabase(t *testing.T) {
	s, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPutGetEvent_Roundtrip(t *testing.T) {
	s := openStore(t)

	e, err := event.New(event.EventTypeTransfer, nil,
		event.TransferPayload{
			FromAgent: "system",
			ToAgent:   "alice",
			Amount:    1000,
			Currency:  "AET",
		},
		"testagent", nil, 500)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	if err := s.PutEvent(e); err != nil {
		t.Fatalf("PutEvent: %v", err)
	}

	got, err := s.GetEvent(e.ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("ID mismatch: got %s, want %s", got.ID, e.ID)
	}
	if got.Type != e.Type {
		t.Errorf("Type mismatch: got %s, want %s", got.Type, e.Type)
	}
	if got.AgentID != e.AgentID {
		t.Errorf("AgentID mismatch: got %s, want %s", got.AgentID, e.AgentID)
	}
}

func TestAllEvents_ReturnsAll(t *testing.T) {
	s := openStore(t)

	for i := range 3 {
		e, err := event.New(event.EventTypeDelegation, nil, nil,
			"agent"+string(rune('A'+i)), nil, 100)
		if err != nil {
			t.Fatalf("event.New[%d]: %v", i, err)
		}
		if err := s.PutEvent(e); err != nil {
			t.Fatalf("PutEvent[%d]: %v", i, err)
		}
	}

	all, err := s.AllEvents()
	if err != nil {
		t.Fatalf("AllEvents: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("AllEvents: got %d, want 3", len(all))
	}
}

func TestPutGetTransfer_Roundtrip(t *testing.T) {
	s := openStore(t)

	entry := &ledger.TransferEntry{
		EventID:    event.EventID("txf-test-001"),
		FromAgent:  crypto.AgentID("alice"),
		ToAgent:    crypto.AgentID("bob"),
		Amount:     5000,
		Currency:   "AET",
		Memo:       "test payment",
		Timestamp:  42,
		Settlement: event.SettlementOptimistic,
		RecordedAt: time.Now().UTC().Truncate(time.Second),
	}

	if err := s.PutTransfer(entry); err != nil {
		t.Fatalf("PutTransfer: %v", err)
	}

	got, err := s.GetTransfer(entry.EventID)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if got.EventID != entry.EventID {
		t.Errorf("EventID mismatch: got %s, want %s", got.EventID, entry.EventID)
	}
	if got.Amount != entry.Amount {
		t.Errorf("Amount mismatch: got %d, want %d", got.Amount, entry.Amount)
	}
	if got.FromAgent != entry.FromAgent {
		t.Errorf("FromAgent mismatch: got %s, want %s", got.FromAgent, entry.FromAgent)
	}
	if got.ToAgent != entry.ToAgent {
		t.Errorf("ToAgent mismatch: got %s, want %s", got.ToAgent, entry.ToAgent)
	}
	if got.Settlement != entry.Settlement {
		t.Errorf("Settlement mismatch: got %s, want %s", got.Settlement, entry.Settlement)
	}
}

func TestAllTransfers_ReturnsAll(t *testing.T) {
	s := openStore(t)

	for i := range 2 {
		entry := &ledger.TransferEntry{
			EventID:    event.EventID("txf-" + string(rune('0'+i))),
			FromAgent:  crypto.AgentID("alice"),
			ToAgent:    crypto.AgentID("bob"),
			Amount:     uint64(1000 * (i + 1)),
			Currency:   "AET",
			Settlement: event.SettlementOptimistic,
			RecordedAt: time.Now().UTC(),
		}
		if err := s.PutTransfer(entry); err != nil {
			t.Fatalf("PutTransfer[%d]: %v", i, err)
		}
	}

	all, err := s.AllTransfers()
	if err != nil {
		t.Fatalf("AllTransfers: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("AllTransfers: got %d, want 2", len(all))
	}
}

func TestPutGetGeneration_Roundtrip(t *testing.T) {
	s := openStore(t)

	entry := &ledger.GenerationEntry{
		EventID:          event.EventID("gen-test-001"),
		GeneratingAgent:  crypto.AgentID("worker"),
		BeneficiaryAgent: crypto.AgentID("owner"),
		ClaimedValue:     9000,
		VerifiedValue:    0,
		EvidenceHash:     "deadbeef",
		TaskDescription:  "summarise document",
		Timestamp:        10,
		Settlement:       event.SettlementOptimistic,
		RecordedAt:       time.Now().UTC().Truncate(time.Second),
	}

	if err := s.PutGeneration(entry); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}

	got, err := s.GetGeneration(entry.EventID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.EventID != entry.EventID {
		t.Errorf("EventID: got %s, want %s", got.EventID, entry.EventID)
	}
	if got.ClaimedValue != entry.ClaimedValue {
		t.Errorf("ClaimedValue: got %d, want %d", got.ClaimedValue, entry.ClaimedValue)
	}
	if got.TaskDescription != entry.TaskDescription {
		t.Errorf("TaskDescription: got %q, want %q", got.TaskDescription, entry.TaskDescription)
	}
}

func TestPutDeletePending_Roundtrip(t *testing.T) {
	s := openStore(t)

	item := &ocs.PendingItem{
		EventID:      event.EventID("ocs-test-001"),
		EventType:    event.EventTypeTransfer,
		AgentID:      crypto.AgentID("alice"),
		Amount:       2000,
		OptimisticAt: time.Now().UTC(),
		Deadline:     30 * time.Second,
	}

	if err := s.PutPending(item); err != nil {
		t.Fatalf("PutPending: %v", err)
	}

	all, err := s.AllPending()
	if err != nil {
		t.Fatalf("AllPending after put: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("AllPending after put: got %d, want 1", len(all))
	}

	if err := s.DeletePending(item.EventID); err != nil {
		t.Fatalf("DeletePending: %v", err)
	}

	all, err = s.AllPending()
	if err != nil {
		t.Fatalf("AllPending after delete: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("AllPending after delete: got %d, want 0", len(all))
	}
}

func TestPutGetIdentity_Roundtrip(t *testing.T) {
	s := openStore(t)

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	agentID := kp.AgentID()

	fp, err := identity.NewFingerprint(agentID, kp.PublicKey, nil)
	if err != nil {
		t.Fatalf("NewFingerprint: %v", err)
	}

	if err := s.PutIdentity(fp); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}

	got, err := s.GetIdentity(agentID)
	if err != nil {
		t.Fatalf("GetIdentity: %v", err)
	}
	if got.AgentID != fp.AgentID {
		t.Errorf("AgentID: got %s, want %s", got.AgentID, fp.AgentID)
	}
}

func TestStore_SurvivesCloseAndReopen(t *testing.T) {
	dir := t.TempDir()

	// Write an event to the first store instance.
	s1, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (first): %v", err)
	}
	e, err := event.New(event.EventTypeVerification, nil, nil, "node1", nil, 1000)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	if err := s1.PutEvent(e); err != nil {
		t.Fatalf("PutEvent: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (first): %v", err)
	}

	// Re-open at the same path and verify the event is still there.
	s2, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (second): %v", err)
	}
	defer s2.Close()

	got, err := s2.GetEvent(e.ID)
	if err != nil {
		t.Fatalf("GetEvent after reopen: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("ID mismatch after reopen: got %s, want %s", got.ID, e.ID)
	}
}

func TestConcurrentPuts_Safe(t *testing.T) {
	s := openStore(t)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)

	for i := range n {
		go func(i int) {
			defer wg.Done()
			e, err := event.New(event.EventTypeAttestation, nil, nil,
				"agent-concurrent-"+string(rune('A'+i%26)), nil, 100)
			if err != nil {
				t.Errorf("event.New[%d]: %v", i, err)
				return
			}
			if err := s.PutEvent(e); err != nil {
				t.Errorf("PutEvent[%d]: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	all, err := s.AllEvents()
	if err != nil {
		t.Fatalf("AllEvents: %v", err)
	}
	if len(all) != n {
		t.Errorf("AllEvents: got %d, want %d", len(all), n)
	}
}
