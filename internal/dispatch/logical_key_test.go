package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// --- Synthetic logical-key consumer -----------------------------------------

// syntheticLogicalKeyConsumer is the analogue of syntheticConsumer for the
// LogicalKeyConsumer interface. Behavior is configurable per test:
//   - keyFn extracts a LogicalKey from the event (default: ev.ID-based).
//   - completeFn returns the IsComplete result (default: true).
//   - outcomeFn returns the DeriveOutcome result (default: VerdictAccept).
//
// The consumer records every Apply invocation: the LogicalKey it was
// invoked with, and the Outcome it received. Tests use these recordings
// to assert per-key Apply guarantees and the C-17 outcome-not-payload
// enforcement.
type syntheticLogicalKeyConsumer struct {
	name       string
	interested func(*event.Event) bool

	keyFn      func(*event.Event) (LogicalKey, error)
	stateFn    func(LogicalKey) (RoundState, error)
	completeFn func(RoundState) (bool, error)
	outcomeFn  func(RoundState) (Outcome, error)

	applyErr error

	mu          sync.Mutex
	applyCount  atomic.Int64
	appliedKeys []LogicalKey
	appliedOuts []Outcome
}

func newSyntheticLogicalKeyConsumer(name string) *syntheticLogicalKeyConsumer {
	return &syntheticLogicalKeyConsumer{
		name:       name,
		interested: func(_ *event.Event) bool { return true },
		keyFn: func(ev *event.Event) (LogicalKey, error) {
			return LogicalKey(ev.ID), nil
		},
		stateFn: func(k LogicalKey) (RoundState, error) {
			return RoundState{LogicalKey: k}, nil
		},
		completeFn: func(_ RoundState) (bool, error) { return true, nil },
		outcomeFn:  func(_ RoundState) (Outcome, error) { return Outcome{Verdict: VerdictAccept}, nil },
	}
}

func (c *syntheticLogicalKeyConsumer) Name() string                    { return c.name }
func (c *syntheticLogicalKeyConsumer) Interested(ev *event.Event) bool { return c.interested(ev) }
func (c *syntheticLogicalKeyConsumer) Key(ev *event.Event) (LogicalKey, error) {
	return c.keyFn(ev)
}
func (c *syntheticLogicalKeyConsumer) RoundState(_ context.Context, k LogicalKey) (RoundState, error) {
	return c.stateFn(k)
}
func (c *syntheticLogicalKeyConsumer) IsComplete(rs RoundState) (bool, error) {
	return c.completeFn(rs)
}
func (c *syntheticLogicalKeyConsumer) DeriveOutcome(rs RoundState) (Outcome, error) {
	return c.outcomeFn(rs)
}

func (c *syntheticLogicalKeyConsumer) Apply(_ context.Context, key LogicalKey, outcome Outcome) error {
	c.mu.Lock()
	c.appliedKeys = append(c.appliedKeys, key)
	c.appliedOuts = append(c.appliedOuts, outcome)
	c.mu.Unlock()
	c.applyCount.Add(1)
	return c.applyErr
}

func (c *syntheticLogicalKeyConsumer) snapshot() ([]LogicalKey, []Outcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := append([]LogicalKey(nil), c.appliedKeys...)
	outs := append([]Outcome(nil), c.appliedOuts...)
	return keys, outs
}

// --- Registration tests -----------------------------------------------------

func TestRegisterLogicalKey_Success(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("test-lk-consumer")
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}
	if d.LogicalKeyConsumerCount() != 1 {
		t.Errorf("logical-key consumer count: got %d want 1", d.LogicalKeyConsumerCount())
	}
	if d.ConsumerCount() != 0 {
		t.Errorf("content-hash consumer count: got %d want 0", d.ConsumerCount())
	}
}

func TestRegisterLogicalKey_DuplicateName(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c1 := newSyntheticLogicalKeyConsumer("dup")
	c2 := newSyntheticLogicalKeyConsumer("dup")
	if err := d.RegisterLogicalKey(c1); err != nil {
		t.Fatalf("RegisterLogicalKey c1: %v", err)
	}
	if err := d.RegisterLogicalKey(c2); err == nil {
		t.Fatal("expected duplicate-name error on second RegisterLogicalKey")
	}
}

func TestRegisterLogicalKey_DuplicateName_AcrossKinds_Rejected(t *testing.T) {
	d, _ := newTestDispatcher(t)

	// Register a content-hash consumer with name "shared".
	chc := newSyntheticConsumer("shared")
	if err := d.Register(chc); err != nil {
		t.Fatalf("Register content-hash: %v", err)
	}

	// Attempt to register a logical-key consumer with the same name.
	lkc := newSyntheticLogicalKeyConsumer("shared")
	err := d.RegisterLogicalKey(lkc)
	if err == nil {
		t.Fatal("expected error: name shared with existing content-hash consumer")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Errorf("error should mention conflicting name; got: %v", err)
	}

	// And the reverse: register lk first, then content-hash with the same name.
	d2, _ := newTestDispatcher(t)
	if err := d2.RegisterLogicalKey(newSyntheticLogicalKeyConsumer("shared")); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}
	err = d2.Register(newSyntheticConsumer("shared"))
	if err == nil {
		t.Fatal("expected error: name shared with existing logical-key consumer")
	}
}

func TestRegisterLogicalKey_EmptyName(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("")
	if err := d.RegisterLogicalKey(c); err == nil {
		t.Fatal("expected error for empty Name()")
	}
}

func TestRegisterLogicalKey_NilConsumer(t *testing.T) {
	d, _ := newTestDispatcher(t)
	if err := d.RegisterLogicalKey(nil); err == nil {
		t.Fatal("expected error for nil consumer")
	}
}

// --- Admission flow tests ---------------------------------------------------

func TestAdmit_LogicalKey_FreshKey_FiresApply(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("lkc")
	// Use a constant logical key so we can predict the storeKey.
	c.keyFn = func(_ *event.Event) (LogicalKey, error) { return "round-1", nil }
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev := makeTestEvent(t, "alice", "p1")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if c.applyCount.Load() != 1 {
		t.Errorf("apply count: got %d want 1", c.applyCount.Load())
	}

	storeKey := LogicalAdmissionKey("lkc", "round-1")
	rec, err := store.GetAdmission(storeKey)
	if err != nil {
		t.Fatalf("GetAdmission(%q): %v", storeKey, err)
	}
	if rec.Strategy != AdmissionStrategyLogicalKey {
		t.Errorf("Strategy: got %v want logical-key", rec.Strategy)
	}
	if rec.LogicalKey != "round-1" {
		t.Errorf("LogicalKey: got %q want round-1", rec.LogicalKey)
	}
	if rec.State != StateApplied {
		t.Errorf("State: got %v want applied", rec.State)
	}
	if rec.SchemaVersion != AdmissionCurrentVersion {
		t.Errorf("SchemaVersion: got %d want %d", rec.SchemaVersion, AdmissionCurrentVersion)
	}
}

func TestAdmit_LogicalKey_SecondEventSameKey_NoOp(t *testing.T) {
	d, _ := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("lkc")
	c.keyFn = func(_ *event.Event) (LogicalKey, error) { return "round-7", nil }
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev1 := makeTestEvent(t, "alice", "first")
	ev2 := makeTestEvent(t, "charlie", "second")
	// Different events (different agent → different canonical bytes →
	// different EventID) but the consumer's keyFn projects both to the
	// same LogicalKey.
	if ev1.ID == ev2.ID {
		t.Fatalf("test setup: events should have distinct EventIDs")
	}

	if err := d.Admit(context.Background(), ev1); err != nil {
		t.Fatalf("Admit ev1: %v", err)
	}
	if err := d.Admit(context.Background(), ev2); err != nil {
		t.Fatalf("Admit ev2: %v", err)
	}

	if got := c.applyCount.Load(); got != 1 {
		t.Errorf("apply count after two events same key: got %d want 1 (per-key Apply guarantee)", got)
	}
}

func TestAdmit_LogicalKey_NotComplete_DefersApply(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("lkc")
	c.keyFn = func(_ *event.Event) (LogicalKey, error) { return "round-not-yet", nil }
	c.completeFn = func(_ RoundState) (bool, error) { return false, nil }
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev := makeTestEvent(t, "alice", "p1")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if c.applyCount.Load() != 0 {
		t.Errorf("apply count: got %d want 0 (IsComplete returned false)", c.applyCount.Load())
	}

	storeKey := LogicalAdmissionKey("lkc", "round-not-yet")
	rec, err := store.GetAdmission(storeKey)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec.State != StateProcessing {
		t.Errorf("State: got %v want processing (deferred until IsComplete=true)", rec.State)
	}

	// Now flip IsComplete to true and re-admit.
	c.completeFn = func(_ RoundState) (bool, error) { return true, nil }
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("re-Admit: %v", err)
	}
	if c.applyCount.Load() != 1 {
		t.Errorf("apply count after completeness flip: got %d want 1", c.applyCount.Load())
	}
	rec, _ = store.GetAdmission(storeKey)
	if rec.State != StateApplied {
		t.Errorf("State after flip: got %v want applied", rec.State)
	}
}

func TestAdmit_LogicalKey_OutcomeReceived_NotPayload(t *testing.T) {
	// Verifies C-17: Apply receives the derived Outcome (from canonical
	// state via DeriveOutcome), NOT the triggering event's payload.
	d, _ := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("lkc")
	c.keyFn = func(_ *event.Event) (LogicalKey, error) { return "key-1", nil }
	// DeriveOutcome returns a synthetic Outcome shape that no event payload
	// could accidentally produce — this lets us assert with certainty the
	// value flowed through DeriveOutcome rather than ev.Payload.
	wantOutcome := Outcome{Verdict: VerdictReject, ScoreBP: 0xDEAD}
	c.outcomeFn = func(_ RoundState) (Outcome, error) { return wantOutcome, nil }
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev := makeTestEvent(t, "alice", "would-be-ignored-payload")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	keys, outs := c.snapshot()
	if len(keys) != 1 || len(outs) != 1 {
		t.Fatalf("expected exactly one Apply invocation; got keys=%v outs=%v", keys, outs)
	}
	if keys[0] != "key-1" {
		t.Errorf("Apply key: got %q want key-1", keys[0])
	}
	if outs[0].Verdict != wantOutcome.Verdict {
		t.Errorf("Apply outcome.Verdict: got %q want %q (must come from DeriveOutcome, not ev.Payload)",
			outs[0].Verdict, wantOutcome.Verdict)
	}
	if outs[0].ScoreBP != wantOutcome.ScoreBP {
		t.Errorf("Apply outcome.ScoreBP: got %d want %d (must come from DeriveOutcome, not ev.Payload)",
			outs[0].ScoreBP, wantOutcome.ScoreBP)
	}
}

func TestAdmit_LogicalKey_ContentHashAndLogicalKey_BothFire(t *testing.T) {
	// When both consumer kinds are registered for the same event type,
	// both paths run independently. This is the F4B coexistence
	// requirement: the logical-key path is additive, not replacing.
	d, _ := newTestDispatcher(t)

	chc := newSyntheticConsumer("ch-consumer")
	if err := d.Register(chc); err != nil {
		t.Fatalf("Register content-hash: %v", err)
	}

	lkc := newSyntheticLogicalKeyConsumer("lk-consumer")
	lkc.keyFn = func(_ *event.Event) (LogicalKey, error) { return "shared-round", nil }
	if err := d.RegisterLogicalKey(lkc); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev := makeTestEvent(t, "alice", "p1")
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	if chc.applyCount.Load() != 1 {
		t.Errorf("content-hash Apply count: got %d want 1", chc.applyCount.Load())
	}
	if lkc.applyCount.Load() != 1 {
		t.Errorf("logical-key Apply count: got %d want 1", lkc.applyCount.Load())
	}
}

func TestAdmit_LogicalKey_ApplyError_StateFailedRetryable(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("lkc")
	c.keyFn = func(_ *event.Event) (LogicalKey, error) { return "round-fail", nil }
	c.applyErr = errors.New("synthetic apply failure")
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev := makeTestEvent(t, "alice", "p1")
	if err := d.Admit(context.Background(), ev); err == nil {
		t.Fatal("expected Admit to surface the Apply failure")
	}

	storeKey := LogicalAdmissionKey("lkc", "round-fail")
	rec, err := store.GetAdmission(storeKey)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec.State != StateFailedRetryable {
		t.Errorf("State after Apply error: got %v want failed-retryable", rec.State)
	}

	// Clear the error and re-admit; the failed-retryable record drives a retry.
	c.applyErr = nil
	if err := d.Admit(context.Background(), ev); err != nil {
		t.Fatalf("re-Admit after fix: %v", err)
	}
	if c.applyCount.Load() != 2 {
		t.Errorf("apply count after retry: got %d want 2", c.applyCount.Load())
	}
	rec, _ = store.GetAdmission(storeKey)
	if rec.State != StateApplied {
		t.Errorf("State after successful retry: got %v want applied", rec.State)
	}
}

// --- Schema / strategy back-compat ------------------------------------------

func TestAdmissionRecord_v1Backcompat_DefaultsContentHash(t *testing.T) {
	// A pre-F4B v1 AdmissionRecord persisted on disk had no Strategy
	// field. JSON-decoding such a record into the v2 struct shape MUST
	// default Strategy to AdmissionStrategyContentHash (zero value),
	// which is the correct interpretation for the content-hash flow
	// the v1 record describes. Backward-compat preserved.
	v1JSON := `{
		"schema_version": 1,
		"key": "dispatch:abc123",
		"state": 3,
		"dag_anchor": "tip-1",
		"prerequisite_schema_version": 0,
		"consumers": {"some-consumer": 1},
		"event_id": "evt-1",
		"event_type": "Transfer",
		"created_at_epoch": 0
	}`
	var rec AdmissionRecord
	if err := json.Unmarshal([]byte(v1JSON), &rec); err != nil {
		t.Fatalf("unmarshal v1 record: %v", err)
	}
	if rec.SchemaVersion != 1 {
		t.Errorf("SchemaVersion: got %d want 1", rec.SchemaVersion)
	}
	if rec.Strategy != AdmissionStrategyContentHash {
		t.Errorf("Strategy: got %v want content-hash (zero-value default for v1 records)", rec.Strategy)
	}
	if rec.LogicalKey != "" {
		t.Errorf("LogicalKey: got %q want empty (correct for content-hash flow)", rec.LogicalKey)
	}
	if rec.State != StateApplied {
		t.Errorf("State: got %v want applied", rec.State)
	}
	// The dual-read defaults must still pass IsKnownAdmissionStrategy.
	if !IsKnownAdmissionStrategy(rec.Strategy) {
		t.Errorf("zero-value Strategy must be recognized as a known enum")
	}
}

func TestIsKnownAdmissionStrategy(t *testing.T) {
	cases := []struct {
		strategy AdmissionStrategy
		want     bool
	}{
		{AdmissionStrategyContentHash, true},
		{AdmissionStrategyLogicalKey, true},
		{AdmissionStrategy(99), false},
		{AdmissionStrategy(255), false},
	}
	for _, tc := range cases {
		if got := IsKnownAdmissionStrategy(tc.strategy); got != tc.want {
			t.Errorf("IsKnownAdmissionStrategy(%d) = %v, want %v", tc.strategy, got, tc.want)
		}
	}
}

// TestAdmit_LogicalKey_UnknownStrategy_Rejected verifies the runtime
// gating on rec.Strategy via the dispatcher-layer enum check
// (IsKnownAdmissionStrategy). The store-layer wrapper that surfaces
// ErrUnknownAdmissionStrategy on read is exercised in
// internal/store/store_extended_test.go::TestAdmission_UnknownStrategyValue_RejectedWithErr.
//
// The dispatcher's in-memory test store does not call
// validateAdmissionDecode, so this test asserts the dispatcher-side
// invariants the gate relies on:
//   - Strategy values outside the enum return false from IsKnownAdmissionStrategy.
//   - Known values (content-hash, logical-key) return true.
//   - The sentinel error exists and identifies the failure mode.
func TestAdmit_LogicalKey_UnknownStrategy_Rejected(t *testing.T) {
	if IsKnownAdmissionStrategy(AdmissionStrategy(99)) {
		t.Fatal("Strategy=99 must NOT be known per IsKnownAdmissionStrategy")
	}
	if IsKnownAdmissionStrategy(AdmissionStrategy(255)) {
		t.Fatal("Strategy=255 must NOT be known per IsKnownAdmissionStrategy")
	}
	if !IsKnownAdmissionStrategy(AdmissionStrategyContentHash) {
		t.Fatal("AdmissionStrategyContentHash must be a known strategy")
	}
	if !IsKnownAdmissionStrategy(AdmissionStrategyLogicalKey) {
		t.Fatal("AdmissionStrategyLogicalKey must be a known strategy")
	}
	if ErrUnknownAdmissionStrategy == nil {
		t.Fatal("ErrUnknownAdmissionStrategy must be a non-nil sentinel")
	}
	// The store-layer wrapper produces an error that wraps the sentinel;
	// errors.Is is the documented matching API.
	wrapped := errors.New("store: admission dispatch:weird-strategy: " + ErrUnknownAdmissionStrategy.Error() + ": strategy=99")
	if errors.Is(wrapped, ErrUnknownAdmissionStrategy) {
		// errors.Is on a manually-constructed string error returns false
		// because the wrapping is text-only; that is fine, the real
		// store wrapper uses fmt.Errorf("...%w...", ErrUnknownAdmissionStrategy)
		// which IS comparable. We don't re-test that here — it lives in
		// the store-package test cited in the comment above.
		_ = wrapped
	}
}

// --- Helper: byte-distinct events project to same logical key ---------------

// TestAdmit_LogicalKey_DistinctCanonicalEvents_SameKey is the F4B
// scenario that motivates logical-key admission: two byte-distinct
// canonical events for the same logical key MUST produce exactly ONE
// Apply invocation.
func TestAdmit_LogicalKey_DistinctCanonicalEvents_SameKey(t *testing.T) {
	d, store := newTestDispatcher(t)
	c := newSyntheticLogicalKeyConsumer("lkc")
	c.keyFn = func(_ *event.Event) (LogicalKey, error) { return "round-A", nil }
	if err := d.RegisterLogicalKey(c); err != nil {
		t.Fatalf("RegisterLogicalKey: %v", err)
	}

	ev1 := makeTestEvent(t, "alice", "verdict-pass")
	ev2 := makeTestEvent(t, "charlie", "verdict-fail")

	// Sanity: the two canonical events are byte-distinct (different
	// content hash) but project to the same logical key.
	k1, _ := AdmissionKey(ev1)
	k2, _ := AdmissionKey(ev2)
	if k1 == k2 {
		t.Fatal("test setup: events should have distinct content-hash admission keys")
	}

	if err := d.Admit(context.Background(), ev1); err != nil {
		t.Fatalf("Admit ev1: %v", err)
	}
	if err := d.Admit(context.Background(), ev2); err != nil {
		t.Fatalf("Admit ev2: %v", err)
	}

	if got := c.applyCount.Load(); got != 1 {
		t.Errorf("apply count: got %d want 1 (per-(consumer, key) guarantee)", got)
	}

	storeKey := LogicalAdmissionKey("lkc", "round-A")
	rec, err := store.GetAdmission(storeKey)
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if rec.State != StateApplied {
		t.Errorf("State: got %v want applied", rec.State)
	}
	// The record's EventID is whichever event was the first to hit
	// the dispatcher; both projections reach the same storeKey.
	if rec.EventID != ev1.ID && rec.EventID != ev2.ID {
		t.Errorf("EventID: got %q want one of %q / %q", rec.EventID, ev1.ID, ev2.ID)
	}
}

// --- Keys --------------------------------------------------------------------

func TestLogicalAdmissionKey_DeterministicAndPrefixed(t *testing.T) {
	k1 := LogicalAdmissionKey("foo", "round-1")
	k2 := LogicalAdmissionKey("foo", "round-1")
	if k1 != k2 {
		t.Errorf("LogicalAdmissionKey not deterministic: %q != %q", k1, k2)
	}
	if !strings.HasPrefix(k1, admissionKeyPrefix+logicalAdmissionKeyPrefix) {
		t.Errorf("LogicalAdmissionKey missing %q+%q prefix: %q",
			admissionKeyPrefix, logicalAdmissionKeyPrefix, k1)
	}
	// Different consumer name → different key.
	if k1 == LogicalAdmissionKey("bar", "round-1") {
		t.Error("LogicalAdmissionKey collision across consumer names")
	}
	// Different logical key → different key.
	if k1 == LogicalAdmissionKey("foo", "round-2") {
		t.Error("LogicalAdmissionKey collision across logical keys")
	}
}

func TestLogicalAdmissionKey_DoesNotCollideWithContentHash(t *testing.T) {
	// Content-hash keys are 64 hex characters; logical keys live under
	// the "lk:" sub-prefix. They must not collide even when an attacker
	// crafts an exotic LogicalKey.
	ev := makeTestEvent(t, "alice", "p1")
	chKey, err := AdmissionKey(ev)
	if err != nil {
		t.Fatalf("AdmissionKey: %v", err)
	}
	lkKey := LogicalAdmissionKey("anything", LogicalKey(strings.TrimPrefix(chKey, admissionKeyPrefix)))
	if chKey == lkKey {
		t.Errorf("content-hash key %q collides with logical-key key %q", chKey, lkKey)
	}
}
