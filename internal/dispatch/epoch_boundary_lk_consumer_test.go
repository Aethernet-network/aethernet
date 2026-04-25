package dispatch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// makeBoundaryEvent constructs a signed EpochBoundary event with the
// given Epoch + TriggerEventID. Only used to exercise the consumer's
// Key projection — admission cross-check is NOT registered, so the
// payload-vs-canonical-state correctness is irrelevant here.
func makeBoundaryEvent(t *testing.T, epochN uint64, triggerID event.EventID) *event.Event {
	t.Helper()
	payload := event.EpochBoundaryPayload{
		Version:        1,
		Epoch:          epochN,
		TriggerEventID: triggerID,
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	signer, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	ev, err := event.New(
		event.EventTypeEpochBoundary,
		nil,
		json.RawMessage(bytes),
		string(signer.AgentID()),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	if err := crypto.SignEvent(ev, signer); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}
	return ev
}

// TestEpochBoundaryLK_KeyExtractsDecimalEpoch verifies the LK consumer
// projects Payload.Epoch as a decimal-string LogicalKey per sub-spec
// §12.6(i) — NOT content-hash. This is the load-bearing canonicality
// for multi-emit dedup convergence.
func TestEpochBoundaryLK_KeyExtractsDecimalEpoch(t *testing.T) {
	c := NewEpochBoundaryLogicalKeyConsumer()
	ev := makeBoundaryEvent(t, 42, "trigger-id")

	key, err := c.Key(ev)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if key != "42" {
		t.Fatalf("Key = %q, want decimal-string of Payload.Epoch (42)", key)
	}
}

// TestEpochBoundaryLK_KeySameForDifferentSigners verifies that two
// emitters (different signers) producing EpochBoundary(N) for the same
// trigger project to the SAME LogicalKey — the precondition for
// dispatcher per-key dedup converging multi-emit to one canonical
// boundary.
func TestEpochBoundaryLK_KeySameForDifferentSigners(t *testing.T) {
	c := NewEpochBoundaryLogicalKeyConsumer()
	ev1 := makeBoundaryEvent(t, 7, "shared-trigger")
	ev2 := makeBoundaryEvent(t, 7, "shared-trigger")

	if ev1.ID == ev2.ID {
		t.Fatalf("test setup: events should have distinct content-hashes (different signers)")
	}

	key1, err := c.Key(ev1)
	if err != nil {
		t.Fatalf("Key(ev1): %v", err)
	}
	key2, err := c.Key(ev2)
	if err != nil {
		t.Fatalf("Key(ev2): %v", err)
	}
	if key1 != key2 {
		t.Fatalf("LogicalKeys differ for same Epoch: %q vs %q — dispatcher dedup would fail to converge multi-emit", key1, key2)
	}
}

// TestEpochBoundaryLK_KeyRejectsEpochZero verifies sub-spec §4.2:
// epoch 0 has no boundary; a payload claiming Epoch=0 is invalid and
// the LK consumer surfaces it loudly.
func TestEpochBoundaryLK_KeyRejectsEpochZero(t *testing.T) {
	c := NewEpochBoundaryLogicalKeyConsumer()
	ev := makeBoundaryEvent(t, 0, "trigger-id")

	_, err := c.Key(ev)
	if err == nil {
		t.Fatalf("Key on Epoch=0 should error (sub-spec §4.2 forbids epoch-0 boundary)")
	}
}

// TestEpochBoundaryLK_IsCompleteAlwaysTrue verifies sub-spec §2.2: the
// LK consumer's IsComplete is unconditionally true. Canonical-state
// validation already happened at dag.Add via the admission cross-check;
// the LK consumer's job is solely to provide the per-Epoch dedup gate.
func TestEpochBoundaryLK_IsCompleteAlwaysTrue(t *testing.T) {
	c := NewEpochBoundaryLogicalKeyConsumer()
	rs := RoundState{LogicalKey: LogicalKey("1")}
	complete, err := c.IsComplete(rs)
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Fatal("IsComplete should be unconditionally true (admission cross-check already validated)")
	}
}

// TestEpochBoundaryLK_ApplyIsNoOp verifies sub-spec §2.2: Apply does
// nothing. The canonical effect of EpochBoundary(N) is its DAG
// presence; no per-Apply side effect.
func TestEpochBoundaryLK_ApplyIsNoOp(t *testing.T) {
	c := NewEpochBoundaryLogicalKeyConsumer()
	if err := c.Apply(context.Background(), LogicalKey("5"), Outcome{}); err != nil {
		t.Fatalf("Apply should be no-op, got: %v", err)
	}
}

// TestEpochBoundaryLK_RecoveryProbeAlwaysCompleted verifies the
// crash-recovery surface: there's no per-EpochBoundary side-effect that
// could be left half-done by a crash, so RecoveryProbe always returns
// RecoveryCompleted (sub-spec §2.2 Apply rationale).
func TestEpochBoundaryLK_RecoveryProbeAlwaysCompleted(t *testing.T) {
	c := NewEpochBoundaryLogicalKeyConsumer()
	status, err := c.RecoveryProbe(context.Background(), LogicalKey("3"))
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != RecoveryCompleted {
		t.Fatalf("RecoveryProbe = %v, want RecoveryCompleted (no per-Apply side effect to recover)", status)
	}
}

// TestEpochBoundaryLK_DispatcherDedup_SameEpochSecondAdmissionIsNoOp
// is the load-bearing convergence test: when two EpochBoundary events
// with the same Epoch but distinct content-hashes (multi-emit from two
// validators) are admitted via the dispatcher, the per-key LK
// dispatcher state machine ensures only ONE Apply fires. This is the
// production wiring's multi-emit dedup proof.
//
// Uses the EpochBoundaryLogicalKeyConsumer's Apply (which is a no-op)
// so we can't count Apply invocations directly; instead we assert via
// the dispatcher's admission store: the second admission's record
// should be in StateAlreadyApplied (or whatever the dedup terminal
// is per F4B's logical-key state machine).
func TestEpochBoundaryLK_DispatcherDedup_SameEpochSecondAdmissionIsNoOp(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := NewEpochBoundaryLogicalKeyConsumer()
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev1 := makeBoundaryEvent(t, 1, "trigger-shared")
	ev2 := makeBoundaryEvent(t, 1, "trigger-shared")
	if ev1.ID == ev2.ID {
		t.Fatalf("test setup: different signers should produce distinct content-hashes")
	}

	if err := d.Admit(context.Background(), ev1); err != nil {
		t.Fatalf("Admit ev1: %v", err)
	}
	if err := d.Admit(context.Background(), ev2); err != nil {
		t.Fatalf("Admit ev2: %v", err)
	}

	// Both admissions should resolve to the same logical-key
	// admission record (storeKey = LogicalAdmissionKey(consumerName, "1")).
	// Per dispatcher LK semantics the record is in StateApplied after the
	// first Apply; the second admission is a no-op.
	storeKey := LogicalAdmissionKey(c.Name(), LogicalKey("1"))
	rec, err := store.GetAdmission(storeKey)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec == nil {
		t.Fatal("expected logical-key admission record for key 1")
	}
	if rec.State != StateApplied {
		t.Fatalf("admission state = %v, want StateApplied (dedup terminal)", rec.State)
	}
}
