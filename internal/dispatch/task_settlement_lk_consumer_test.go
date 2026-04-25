package dispatch_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/dispatch"
	"github.com/Aethernet-network/aethernet/internal/dispatch/conformance"
	"github.com/Aethernet-network/aethernet/internal/event"
	"github.com/Aethernet-network/aethernet/internal/settlement"
)

// makeTaskSettlementLKEvent constructs a TaskSettlement event with
// the given TaskID. Used to exercise Key() under known payload
// shapes.
func makeTaskSettlementLKEvent(t *testing.T, taskID string) *event.Event {
	t.Helper()
	ev, err := event.New(
		event.EventTypeTaskSettlement,
		nil,
		settlement.TaskSettlementPayload{
			Version:        1,
			TaskID:         taskID,
			PosterID:       "poster",
			ClaimerID:      "worker",
			Budget:         10_000,
			AcceptanceHash: "sha256:test",
			EvidenceHash:   "sha256:evidence",
			Category:       "research",
			ScoreBP:        7000,
			HoldGeneration: false,
		},
		"autovalidator-1",
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("construct task settlement event: %v", err)
	}
	return ev
}

// --- Shape tests ------------------------------------------------------------

func TestTaskSettlementLKConsumer_Name(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	if c.Name() != "task_settlement_lk" {
		t.Errorf("Name: got %q want task_settlement_lk", c.Name())
	}
}

func TestTaskSettlementLKConsumer_Interested(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	ev := makeTaskSettlementLKEvent(t, "task-1")
	if !c.Interested(ev) {
		t.Error("should be interested in TaskSettlement events")
	}
	other, _ := event.New(event.EventTypeTransfer, nil, event.TransferPayload{
		FromAgent: "a", ToAgent: "b", Amount: 1, Currency: "AET",
	}, "a", nil, 0)
	if c.Interested(other) {
		t.Error("should NOT be interested in Transfer events")
	}
}

func TestTaskSettlementLKConsumer_Key_ExtractsTaskID(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	ev := makeTaskSettlementLKEvent(t, "task-xyz")
	key, err := c.Key(ev)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if key != "task-xyz" {
		t.Errorf("Key: got %q want task-xyz", key)
	}
}

func TestTaskSettlementLKConsumer_Key_EmptyTaskIDErrors(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	ev := makeTaskSettlementLKEvent(t, "")
	if _, err := c.Key(ev); err == nil {
		t.Error("Key on empty TaskID should error")
	}
}

// --- IsComplete tests -------------------------------------------------------

// TestTaskSettlementLKConsumer_IsComplete_AlwaysTrue verifies the
// trivial-ready property: a TaskSettlement event's arrival is
// itself the readiness signal.
func TestTaskSettlementLKConsumer_IsComplete_AlwaysTrue(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	complete, err := c.IsComplete(dispatch.RoundState{LogicalKey: "any-task"})
	if err != nil {
		t.Fatalf("IsComplete: %v", err)
	}
	if !complete {
		t.Error("expected IsComplete=true unconditionally")
	}
}

// --- DeriveOutcome tests ----------------------------------------------------

// TestTaskSettlementLKConsumer_DeriveOutcome_Empty verifies that
// the consumer returns an empty Outcome (no verdict derivation).
// Verdict derivation for tasks flows through the Settlement LK
// consumer keyed by TargetEventID.
func TestTaskSettlementLKConsumer_DeriveOutcome_Empty(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	outcome, err := c.DeriveOutcome(dispatch.RoundState{LogicalKey: "task-1"})
	if err != nil {
		t.Fatalf("DeriveOutcome: %v", err)
	}
	if outcome.Verdict != "" {
		t.Errorf("Verdict: got %q want empty", outcome.Verdict)
	}
	if outcome.ScoreBP != 0 {
		t.Errorf("ScoreBP: got %d want 0", outcome.ScoreBP)
	}
	if len(outcome.ParticipatingIDs) != 0 {
		t.Errorf("ParticipatingIDs: got %v want empty", outcome.ParticipatingIDs)
	}
}

// --- Apply tests ------------------------------------------------------------

// TestTaskSettlementLKConsumer_Apply_NoOp verifies that Apply is a
// no-op. No error, no state mutation — the admission record is
// the sole durable artifact of this consumer.
func TestTaskSettlementLKConsumer_Apply_NoOp(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	err := c.Apply(context.Background(), event.EventID("trigger"), "task-1", dispatch.Outcome{})
	if err != nil {
		t.Errorf("Apply should be a no-op (no error); got %v", err)
	}
}

// --- RecoveryProbe tests ----------------------------------------------------

// TestTaskSettlementLKConsumer_RecoveryProbe_AlwaysCompleted verifies
// that RecoveryProbe signals completed unconditionally. Since Apply
// is a no-op there is no durable state to inspect for partial
// completion — the "effect" is trivially already done.
func TestTaskSettlementLKConsumer_RecoveryProbe_AlwaysCompleted(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()
	status, err := c.RecoveryProbe(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("RecoveryProbe: %v", err)
	}
	if status != dispatch.RecoveryCompleted {
		t.Errorf("RecoveryProbe: got %v want RecoveryCompleted", status)
	}
}

// --- End-to-end dispatcher tests --------------------------------------------

// TestTaskSettlementLKConsumer_EndToEnd_OneApplyPerTaskID is the
// admission-dedup property: multiple byte-distinct TaskSettlement
// events for the SAME TaskID produce exactly ONE admission record.
// This is the F4B value-add for TaskSettlement even under the
// Apply-is-no-op shape: it prevents the dispatcher from treating
// multiple emissions as distinct admission targets.
func TestTaskSettlementLKConsumer_EndToEnd_OneApplyPerTaskID(t *testing.T) {
	c := dispatch.NewTaskSettlementLogicalKeyConsumer()

	d, adm := newTestDispatcherForLK(t)
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	// Two byte-distinct events with DIFFERENT score values but SAME
	// TaskID. Both project to the same LogicalKey. Only one
	// admission record.
	ev1 := makeTaskSettlementLKEvent(t, "task-e2e")
	ev2, _ := event.New(
		event.EventTypeTaskSettlement,
		nil,
		settlement.TaskSettlementPayload{
			Version:        1,
			TaskID:         "task-e2e",
			PosterID:       "poster",
			ClaimerID:      "worker",
			Budget:         10_000,
			AcceptanceHash: "sha256:test",
			EvidenceHash:   "sha256:evidence",
			Category:       "research",
			ScoreBP:        8000, // different score — byte-distinct event
			HoldGeneration: false,
		},
		"autovalidator-2",
		nil,
		0,
	)
	if ev1.ID == ev2.ID {
		t.Fatalf("test setup: events should be byte-distinct")
	}

	if err := d.Admit(context.Background(), ev1); err != nil {
		t.Fatalf("Admit ev1: %v", err)
	}
	if err := d.Admit(context.Background(), ev2); err != nil {
		t.Fatalf("Admit ev2: %v", err)
	}

	storeKey := dispatch.LogicalAdmissionKey("task_settlement_lk", "task-e2e")
	rec, err := adm.GetAdmission(storeKey)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec.State != dispatch.StateApplied {
		t.Errorf("State: got %v want applied", rec.State)
	}
	if rec.Strategy != dispatch.AdmissionStrategyLogicalKey {
		t.Errorf("Strategy: got %v want logical-key", rec.Strategy)
	}
}

// --- Conformance ------------------------------------------------------------

// TestTaskSettlementLKConsumer_Conformance runs the baseline Type E
// behavioral suite. Since the conformance harness uses Transfer
// baseline events, `Interested` returns false and the suite's
// t.Skip paths fire — confirming structural shape without
// exercising TaskSettlement semantics. Semantics are covered by
// the unit tests above + end-to-end test.
func TestTaskSettlementLKConsumer_Conformance(t *testing.T) {
	conformance.RunLogicalKeyConformance(t, func() (dispatch.LogicalKeyConsumer, func()) {
		return dispatch.NewTaskSettlementLogicalKeyConsumer(), func() {}
	})
}
