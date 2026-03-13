package canary

import (
	"fmt"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// In-memory calibrationStore for tests
// ---------------------------------------------------------------------------

type memCalibrationStore struct {
	mu      sync.Mutex
	signals map[string]*CalibrationSignal
}

func newMemCalibrationStore() *memCalibrationStore {
	return &memCalibrationStore{signals: make(map[string]*CalibrationSignal)}
}

func (m *memCalibrationStore) PutSignal(sig *CalibrationSignal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Deep-copy maps to avoid sharing references.
	cp := *sig
	if sig.ExpectedCheckResults != nil {
		cp.ExpectedCheckResults = make(map[string]bool, len(sig.ExpectedCheckResults))
		for k, v := range sig.ExpectedCheckResults {
			cp.ExpectedCheckResults[k] = v
		}
	}
	if sig.ObservedCheckResults != nil {
		cp.ObservedCheckResults = make(map[string]bool, len(sig.ObservedCheckResults))
		for k, v := range sig.ObservedCheckResults {
			cp.ObservedCheckResults[k] = v
		}
	}
	m.signals[sig.ID] = &cp
	return nil
}

func (m *memCalibrationStore) GetSignal(id string) (*CalibrationSignal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sig, ok := m.signals[id]
	if !ok {
		return nil, fmt.Errorf("signal not found: %s", id)
	}
	cp := *sig
	return &cp, nil
}

func (m *memCalibrationStore) SignalsByActor(actorID string) ([]*CalibrationSignal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*CalibrationSignal
	for _, sig := range m.signals {
		if sig.ActorID == actorID {
			cp := *sig
			result = append(result, &cp)
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeCodeCanary(expectedPass bool, checks map[string]bool) *CanaryTask {
	c := NewCanaryTask("code", TypeKnownGood, expectedPass, checks, "sha256:ground-truth")
	c.ExpectedMinScore = 0.65
	c.ExpectedMaxScore = 1.0
	return c
}

// ---------------------------------------------------------------------------
// Evaluator tests
// ---------------------------------------------------------------------------

// TestEvaluate_CorrectOutcome verifies that when observed pass matches expected
// and all checks match, Correctness="correct" and Severity=0.0.
func TestEvaluate_CorrectOutcome(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	canary := makeCodeCanary(true, map[string]bool{"go_test": true, "lint": true})
	sig := ev.Evaluate(canary, "worker-1", RoleWorker, true, map[string]bool{"go_test": true, "lint": true})

	if sig.Correctness != CorrectnessCorrect {
		t.Errorf("Correctness = %q; want %q", sig.Correctness, CorrectnessCorrect)
	}
	if sig.Severity != 0.0 {
		t.Errorf("Severity = %v; want 0.0", sig.Severity)
	}
	if sig.ObservedPass != true {
		t.Error("ObservedPass must be true")
	}
	if sig.ExpectedPass != true {
		t.Error("ExpectedPass must be true")
	}
}

// TestEvaluate_MissedGoodWork verifies that when expected pass=true but
// observed pass=false, Correctness="incorrect" and Severity=0.7.
func TestEvaluate_MissedGoodWork(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	canary := makeCodeCanary(true, nil)
	sig := ev.Evaluate(canary, "worker-2", RoleWorker, false, nil)

	if sig.Correctness != CorrectnessIncorrect {
		t.Errorf("Correctness = %q; want %q", sig.Correctness, CorrectnessIncorrect)
	}
	if sig.Severity != 0.7 {
		t.Errorf("Severity = %v; want 0.7 (missed good work)", sig.Severity)
	}
}

// TestEvaluate_ApprovedBadWork verifies that when expected pass=false but
// observed pass=true, Correctness="incorrect" and Severity=1.0 (most dangerous).
func TestEvaluate_ApprovedBadWork(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	canary := makeCodeCanary(false, nil)
	canary.CanaryType = TypeKnownBad
	sig := ev.Evaluate(canary, "validator-1", RoleValidator, true, nil)

	if sig.Correctness != CorrectnessIncorrect {
		t.Errorf("Correctness = %q; want %q", sig.Correctness, CorrectnessIncorrect)
	}
	if sig.Severity != 1.0 {
		t.Errorf("Severity = %v; want 1.0 (approved bad work)", sig.Severity)
	}
}

// TestEvaluate_PartialMatch verifies that when the overall pass matches but one
// check differs, Correctness="partial" and Severity = 0.3 + 0.1*mismatches.
func TestEvaluate_PartialMatch(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	// Expect both checks to pass; observe go_test fail → 1 mismatch.
	canary := makeCodeCanary(true, map[string]bool{"go_test": true, "lint": true})
	sig := ev.Evaluate(canary, "worker-3", RoleWorker, true,
		map[string]bool{"go_test": false, "lint": true})

	if sig.Correctness != CorrectnessPartial {
		t.Errorf("Correctness = %q; want %q", sig.Correctness, CorrectnessPartial)
	}
	wantSeverity := 0.3 + 0.1*1 // 1 mismatch
	if sig.Severity != wantSeverity {
		t.Errorf("Severity = %v; want %v (1 mismatch)", sig.Severity, wantSeverity)
	}
}

// TestEvaluate_PartialMatch_TwoMismatches verifies severity scales with
// the number of mismatched checks: 2 mismatches → severity = 0.5.
func TestEvaluate_PartialMatch_TwoMismatches(t *testing.T) {
	store := newMemCalibrationStore()
	ev := NewEvaluator(store)

	canary := makeCodeCanary(true, map[string]bool{"go_test": true, "lint": true, "vet": true})
	// go_test and vet both mismatch; lint matches.
	sig := ev.Evaluate(canary, "worker-4", RoleWorker, true,
		map[string]bool{"go_test": false, "lint": true, "vet": false})

	if sig.Correctness != CorrectnessPartial {
		t.Errorf("Correctness = %q; want %q", sig.Correctness, CorrectnessPartial)
	}
	wantSeverity := 0.3 + 0.1*2 // 2 mismatches = 0.5
	if sig.Severity != wantSeverity {
		t.Errorf("Severity = %v; want %v (2 mismatches)", sig.Severity, wantSeverity)
	}
}

// TestSignalID_IsDeterministic verifies that signalID returns the same ID for
// the same (canaryID, actorID, role) triple regardless of call order.
func TestSignalID_IsDeterministic(t *testing.T) {
	id1 := signalID("cnr-abc123", "agent-7", RoleWorker)
	id2 := signalID("cnr-abc123", "agent-7", RoleWorker)

	if id1 != id2 {
		t.Errorf("signalID is not deterministic: %q != %q", id1, id2)
	}
	if id1 == "" {
		t.Error("signalID must not be empty")
	}
}

// TestSignalID_DiffersForDifferentInputs verifies that different inputs produce
// different IDs (no trivial collisions in the expected input space).
func TestSignalID_DiffersForDifferentInputs(t *testing.T) {
	cases := [][3]string{
		{"cnr-aaa", "worker-1", RoleWorker},
		{"cnr-aaa", "worker-1", RoleValidator},      // same canary+actor, different role
		{"cnr-aaa", "worker-2", RoleWorker},          // same canary+role, different actor
		{"cnr-bbb", "worker-1", RoleWorker},          // different canary
	}
	seen := map[string]struct{}{}
	for _, c := range cases {
		id := signalID(c[0], c[1], c[2])
		if _, ok := seen[id]; ok {
			t.Errorf("signalID collision for inputs %v: %q", c, id)
		}
		seen[id] = struct{}{}
	}
}

// TestSignalStoreRoundTrip verifies that PutSignal followed by GetSignal
// returns the original signal with all fields intact.
func TestSignalStoreRoundTrip(t *testing.T) {
	ms := newMemCalibrationStore()
	ev := NewEvaluator(ms)

	canary := makeCodeCanary(true, map[string]bool{"go_test": true})
	sig := ev.Evaluate(canary, "worker-rt", RoleWorker, true, map[string]bool{"go_test": true})

	got, err := ms.GetSignal(sig.ID)
	if err != nil {
		t.Fatalf("GetSignal: %v", err)
	}
	if got.ID != sig.ID {
		t.Errorf("ID: want %q, got %q", sig.ID, got.ID)
	}
	if got.Correctness != sig.Correctness {
		t.Errorf("Correctness: want %q, got %q", sig.Correctness, got.Correctness)
	}
	if got.Severity != sig.Severity {
		t.Errorf("Severity: want %v, got %v", sig.Severity, got.Severity)
	}
	if got.ActorID != "worker-rt" {
		t.Errorf("ActorID: want %q, got %q", "worker-rt", got.ActorID)
	}
	if got.ActorRole != RoleWorker {
		t.Errorf("ActorRole: want %q, got %q", RoleWorker, got.ActorRole)
	}
}

// TestSignalsByActor_ReturnsOnlyMatchingActor verifies that SignalsByActor
// filters to signals for the requested actor only.
func TestSignalsByActor_ReturnsOnlyMatchingActor(t *testing.T) {
	ms := newMemCalibrationStore()
	ev := NewEvaluator(ms)

	c1 := makeCodeCanary(true, nil)
	c2 := makeCodeCanary(false, nil)
	c3 := makeCodeCanary(true, nil)

	// Two signals for actor-A, one for actor-B.
	ev.Evaluate(c1, "actor-A", RoleWorker, true, nil)
	ev.Evaluate(c2, "actor-B", RoleValidator, true, nil) // approved bad work
	ev.Evaluate(c3, "actor-A", RoleWorker, true, nil)

	signsA, err := ms.SignalsByActor("actor-A")
	if err != nil {
		t.Fatalf("SignalsByActor actor-A: %v", err)
	}
	if len(signsA) != 2 {
		t.Errorf("actor-A signal count = %d; want 2", len(signsA))
	}
	for _, s := range signsA {
		if s.ActorID != "actor-A" {
			t.Errorf("unexpected ActorID %q in actor-A results", s.ActorID)
		}
	}

	signsB, err := ms.SignalsByActor("actor-B")
	if err != nil {
		t.Fatalf("SignalsByActor actor-B: %v", err)
	}
	if len(signsB) != 1 {
		t.Errorf("actor-B signal count = %d; want 1", len(signsB))
	}
}

// ---------------------------------------------------------------------------
// ComputeActorCalibration tests
// ---------------------------------------------------------------------------

// TestComputeActorCalibration_Aggregates verifies that ComputeActorCalibration
// correctly aggregates correct/partial/incorrect counts and computes Accuracy
// and AvgSeverity.
func TestComputeActorCalibration_Aggregates(t *testing.T) {
	signals := []*CalibrationSignal{
		{ActorID: "a1", Category: "code", Correctness: CorrectnessCorrect, Severity: 0.0},
		{ActorID: "a1", Category: "code", Correctness: CorrectnessPartial, Severity: 0.4},
		{ActorID: "a1", Category: "code", Correctness: CorrectnessIncorrect, Severity: 1.0},
		{ActorID: "a1", Category: "code", Correctness: CorrectnessCorrect, Severity: 0.0},
	}

	ac := ComputeActorCalibration(signals)

	if ac.ActorID != "a1" {
		t.Errorf("ActorID = %q; want %q", ac.ActorID, "a1")
	}
	if ac.TotalSignals != 4 {
		t.Errorf("TotalSignals = %d; want 4", ac.TotalSignals)
	}
	if ac.CorrectCount != 2 {
		t.Errorf("CorrectCount = %d; want 2", ac.CorrectCount)
	}
	if ac.PartialCount != 1 {
		t.Errorf("PartialCount = %d; want 1", ac.PartialCount)
	}
	if ac.IncorrectCount != 1 {
		t.Errorf("IncorrectCount = %d; want 1", ac.IncorrectCount)
	}

	wantAccuracy := 2.0 / 4.0 // 0.5
	if ac.Accuracy != wantAccuracy {
		t.Errorf("Accuracy = %v; want %v", ac.Accuracy, wantAccuracy)
	}

	wantAvgSeverity := (0.0 + 0.4 + 1.0 + 0.0) / 4.0
	if ac.AvgSeverity != wantAvgSeverity {
		t.Errorf("AvgSeverity = %v; want %v", ac.AvgSeverity, wantAvgSeverity)
	}
}

// TestComputeActorCalibration_PerCategoryBreakdown verifies that signals are
// bucketed into the correct ByCategory entries.
func TestComputeActorCalibration_PerCategoryBreakdown(t *testing.T) {
	signals := []*CalibrationSignal{
		{ActorID: "a2", Category: "code", Correctness: CorrectnessCorrect, Severity: 0.0},
		{ActorID: "a2", Category: "code", Correctness: CorrectnessIncorrect, Severity: 1.0},
		{ActorID: "a2", Category: "research", Correctness: CorrectnessCorrect, Severity: 0.0},
		{ActorID: "a2", Category: "writing", Correctness: CorrectnessPartial, Severity: 0.3},
	}

	ac := ComputeActorCalibration(signals)

	code, ok := ac.ByCategory["code"]
	if !ok {
		t.Fatal("ByCategory missing 'code' entry")
	}
	if code.TotalSignals != 2 {
		t.Errorf("code.TotalSignals = %d; want 2", code.TotalSignals)
	}
	if code.CorrectCount != 1 {
		t.Errorf("code.CorrectCount = %d; want 1", code.CorrectCount)
	}
	if code.IncorrectCount != 1 {
		t.Errorf("code.IncorrectCount = %d; want 1", code.IncorrectCount)
	}
	wantCodeAccuracy := 1.0 / 2.0
	if code.Accuracy != wantCodeAccuracy {
		t.Errorf("code.Accuracy = %v; want %v", code.Accuracy, wantCodeAccuracy)
	}

	research, ok := ac.ByCategory["research"]
	if !ok {
		t.Fatal("ByCategory missing 'research' entry")
	}
	if research.TotalSignals != 1 || research.CorrectCount != 1 {
		t.Errorf("research: TotalSignals=%d CorrectCount=%d; want 1/1",
			research.TotalSignals, research.CorrectCount)
	}
	if research.Accuracy != 1.0 {
		t.Errorf("research.Accuracy = %v; want 1.0", research.Accuracy)
	}

	writing, ok := ac.ByCategory["writing"]
	if !ok {
		t.Fatal("ByCategory missing 'writing' entry")
	}
	if writing.PartialCount != 1 {
		t.Errorf("writing.PartialCount = %d; want 1", writing.PartialCount)
	}
}

// TestComputeActorCalibration_EmptySignals verifies that an empty slice does
// not panic and returns a zero-value ActorCalibration.
func TestComputeActorCalibration_EmptySignals(t *testing.T) {
	ac := ComputeActorCalibration(nil)
	if ac.TotalSignals != 0 {
		t.Errorf("TotalSignals = %d; want 0", ac.TotalSignals)
	}
	if ac.Accuracy != 0.0 {
		t.Errorf("Accuracy = %v; want 0.0", ac.Accuracy)
	}
	if ac.ByCategory == nil {
		t.Error("ByCategory must not be nil")
	}
}
