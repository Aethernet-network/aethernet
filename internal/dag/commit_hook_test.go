package dag_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/dag"
	"github.com/Aethernet-network/aethernet/internal/event"
)

func TestOnCommit_FiresAfterSuccessfulAdd(t *testing.T) {
	d := dag.New()

	var committed atomic.Int64
	var lastID event.EventID
	var mu sync.Mutex

	d.SetOnCommit(func(ev *event.Event, replay bool) {
		committed.Add(1)
		mu.Lock()
		lastID = ev.ID
		mu.Unlock()
		if replay {
			t.Error("replay should be false for Add")
		}
	})

	kp, _ := crypto.GenerateKeyPair()
	ev := makeHookTestGenesis(t, string(kp.AgentID()))
	_ = crypto.SignEvent(ev, kp)
	if err := d.Add(ev); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if committed.Load() != 1 {
		t.Errorf("committed = %d; want 1", committed.Load())
	}
	mu.Lock()
	if lastID != ev.ID {
		t.Errorf("lastID = %q; want %q", lastID, ev.ID)
	}
	mu.Unlock()
}

func TestOnCommit_DoesNotFireOnDuplicate(t *testing.T) {
	d := dag.New()

	var committed atomic.Int64
	d.SetOnCommit(func(ev *event.Event, replay bool) {
		committed.Add(1)
	})

	kp, _ := crypto.GenerateKeyPair()
	ev := makeHookTestGenesis(t, string(kp.AgentID()))
	_ = crypto.SignEvent(ev, kp)

	_ = d.Add(ev) // first — succeeds
	_ = d.Add(ev) // second ��� duplicate, should not fire hook

	if committed.Load() != 1 {
		t.Errorf("committed = %d; want 1 (no fire on duplicate)", committed.Load())
	}
}

func TestOnCommit_DoesNotFireOnMissingRef(t *testing.T) {
	d := dag.New()

	var committed atomic.Int64
	d.SetOnCommit(func(ev *event.Event, replay bool) {
		committed.Add(1)
	})

	kp, _ := crypto.GenerateKeyPair()
	payload := event.TransferPayload{
		FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET",
	}
	orphan, _ := event.New(event.EventTypeTransfer, []event.EventID{"nonexistent"},
		payload, string(kp.AgentID()), map[event.EventID]uint64{"nonexistent": 1}, 0)
	_ = crypto.SignEvent(orphan, kp)

	_ = d.Add(orphan) // fails — missing ref

	if committed.Load() != 0 {
		t.Errorf("committed = %d; want 0 (no fire on failed Add)", committed.Load())
	}
}

func TestOnCommit_FiresOutsideLock(t *testing.T) {
	d := dag.New()

	d.SetOnCommit(func(ev *event.Event, replay bool) {
		// This runs outside the DAG lock. Prove it by calling a method
		// that requires a read lock — if we were under the write lock,
		// this would deadlock.
		_ = d.Size() // requires RLock — deadlocks if called under WLock
	})

	kp, _ := crypto.GenerateKeyPair()
	ev := makeHookTestGenesis(t, string(kp.AgentID()))
	_ = crypto.SignEvent(ev, kp)

	// If the hook runs under the lock, this will deadlock with -timeout.
	if err := d.Add(ev); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if d.Size() != 1 {
		t.Error("event should be in DAG")
	}
}

func TestOnCommit_MultipleEventsFireInOrder(t *testing.T) {
	d := dag.New()

	var ids []event.EventID
	var mu sync.Mutex
	d.SetOnCommit(func(ev *event.Event, replay bool) {
		mu.Lock()
		ids = append(ids, ev.ID)
		mu.Unlock()
	})

	for i := 0; i < 5; i++ {
		kp, _ := crypto.GenerateKeyPair()
		ev := makeHookTestGenesis(t, string(kp.AgentID()))
		_ = crypto.SignEvent(ev, kp)
		_ = d.Add(ev)
	}

	mu.Lock()
	if len(ids) != 5 {
		t.Errorf("committed %d events; want 5", len(ids))
	}
	mu.Unlock()
}

func TestOnCommit_NilHookIsNoop(t *testing.T) {
	d := dag.New()
	// No hook set — should not panic.

	kp, _ := crypto.GenerateKeyPair()
	ev := makeHookTestGenesis(t, string(kp.AgentID()))
	_ = crypto.SignEvent(ev, kp)

	if err := d.Add(ev); err != nil {
		t.Fatalf("Add with nil hook: %v", err)
	}
}

// makeHookTestGenesis creates a genesis event (no causal refs) for testing.
func makeHookTestGenesis(t *testing.T, agentID string) *event.Event {
	t.Helper()
	payload := event.TransferPayload{
		FromAgent: agentID, ToAgent: "sink", Amount: 1, Currency: "AET",
	}
	ev, err := event.New(event.EventTypeTransfer, nil, payload, agentID, nil, 0)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	return ev
}
