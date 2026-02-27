package ocs_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aethernet/core/internal/crypto"
	"github.com/aethernet/core/internal/event"
	"github.com/aethernet/core/internal/identity"
	"github.com/aethernet/core/internal/ledger"
	"github.com/aethernet/core/internal/ocs"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testHarness holds all the wired-up components for a single test.
type testHarness struct {
	eng *ocs.Engine
	tl  *ledger.TransferLedger
	gl  *ledger.GenerationLedger
	reg *identity.Registry
}

// newHarness builds a fully wired engine with the given config.
// If cfg is nil, DefaultConfig is used. The engine is NOT started;
// each test starts and stops it as needed.
func newHarness(t *testing.T, cfg *ocs.EngineConfig) *testHarness {
	t.Helper()
	tl := ledger.NewTransferLedger()
	gl := ledger.NewGenerationLedger()
	reg := identity.NewRegistry()
	if cfg == nil {
		cfg = ocs.DefaultConfig()
	}
	return &testHarness{
		eng: ocs.NewEngine(cfg, tl, gl, reg),
		tl:  tl,
		gl:  gl,
		reg: reg,
	}
}

// fundAgent gives agentID an initial spendable balance in the transfer ledger.
func fundAgent(t *testing.T, h *testHarness, agentID string, amount uint64) {
	t.Helper()
	if err := h.tl.FundAgent(crypto.AgentID(agentID), amount); err != nil {
		t.Fatalf("FundAgent(%s, %d): %v", agentID, amount, err)
	}
}

// registerAgent adds a minimal CapabilityFingerprint for agentID to the registry.
// The fingerprint carries a 32-byte placeholder public key so the registry
// accepts it without requiring a real Ed25519 key.
func registerAgent(t *testing.T, reg *identity.Registry, agentID string) {
	t.Helper()
	fp, err := identity.NewFingerprint(
		crypto.AgentID(agentID),
		make([]byte, 32), // minimal non-empty public key
		nil,
	)
	if err != nil {
		t.Fatalf("NewFingerprint(%q): %v", agentID, err)
	}
	if err := reg.Register(fp); err != nil {
		t.Fatalf("Register(%q): %v", agentID, err)
	}
}

// newTransferEvent creates a Transfer event with the given stake amount.
// stake must be >= cfg.MinStakeRequired to pass Submit's stake check.
func newTransferEvent(t *testing.T, from, to string, amount, stake uint64) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeTransfer,
		nil,
		event.TransferPayload{FromAgent: from, ToAgent: to, Amount: amount, Currency: "AET"},
		from,
		nil,
		stake,
	)
	if err != nil {
		t.Fatalf("newTransferEvent: %v", err)
	}
	return e
}

// newGenerationEvent creates a Generation event with the given stake amount.
func newGenerationEvent(t *testing.T, generating, beneficiary string, claimed, stake uint64) *event.Event {
	t.Helper()
	e, err := event.New(
		event.EventTypeGeneration,
		nil,
		event.GenerationPayload{
			GeneratingAgent:  generating,
			BeneficiaryAgent: beneficiary,
			ClaimedValue:     claimed,
			EvidenceHash:     "sha256:test-evidence",
			TaskDescription:  "test task",
		},
		generating,
		nil,
		stake,
	)
	if err != nil {
		t.Fatalf("newGenerationEvent: %v", err)
	}
	return e
}

// waitFor polls condition every 5ms until it returns true or the timeout elapses.
// Used to synchronise with the engine's background goroutine without fixed sleeps.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not satisfied within timeout")
}

// verdict constructs a VerificationResult for an event, defaulting Timestamp to now.
func verdict(eventID event.EventID, v bool, verifiedValue uint64) ocs.VerificationResult {
	return ocs.VerificationResult{
		EventID:       eventID,
		Verdict:       v,
		VerifiedValue: verifiedValue,
		VerifierID:    "test-verifier",
		Reason:        "test",
		Timestamp:     time.Now(),
	}
}

// ---------------------------------------------------------------------------
// Engine lifecycle
// ---------------------------------------------------------------------------

func TestEngine_Start_ReturnsNil(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	h.eng.Stop()
}

func TestEngine_Start_AlreadyRunning(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Start(); err != nil {
		t.Fatalf("first Start() = %v, want nil", err)
	}
	defer h.eng.Stop()

	err := h.eng.Start()
	if !errors.Is(err, ocs.ErrAlreadyRunning) {
		t.Errorf("second Start() = %v, want ErrAlreadyRunning", err)
	}
}

func TestEngine_Stop_CleanShutdown(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	done := make(chan struct{})
	go func() {
		h.eng.Stop()
		close(done)
	}()

	select {
	case <-done:
		// clean shutdown
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s")
	}
}

func TestEngine_Stop_IdempotentWhenNotRunning(t *testing.T) {
	h := newHarness(t, nil)
	// Stop on a never-started engine must not block or panic.
	done := make(chan struct{})
	go func() {
		h.eng.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop() on unstarted engine did not return")
	}
}

func TestEngine_SubmitVerification_BeforeStart_Error(t *testing.T) {
	h := newHarness(t, nil)
	err := h.eng.SubmitVerification(verdict("some-id", true, 0))
	if !errors.Is(err, ocs.ErrNotRunning) {
		t.Errorf("SubmitVerification before Start = %v, want ErrNotRunning", err)
	}
}

// ---------------------------------------------------------------------------
// Submit
// ---------------------------------------------------------------------------

func TestEngine_Submit_Transfer_LandsInPending(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)
	e := newTransferEvent(t, "alice", "bob", 500, 1000)

	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit() = %v, want nil", err)
	}
	if !h.eng.IsPending(e.ID) {
		t.Error("IsPending() = false after Submit, want true")
	}
}

func TestEngine_Submit_Generation_LandsInPending(t *testing.T) {
	h := newHarness(t, nil)
	e := newGenerationEvent(t, "gen-agent", "ben-agent", 10_000, 1000)

	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit() = %v, want nil", err)
	}
	if !h.eng.IsPending(e.ID) {
		t.Error("IsPending() = false after Submit, want true")
	}
}

func TestEngine_Submit_InsufficientStake(t *testing.T) {
	h := newHarness(t, nil) // MinStakeRequired = 1000
	fundAgent(t, h, "alice", 100_000)
	e := newTransferEvent(t, "alice", "bob", 500, 999) // stake 999 < 1000

	err := h.eng.Submit(e)
	if !errors.Is(err, ocs.ErrInsufficientStake) {
		t.Errorf("Submit(stake=999) = %v, want ErrInsufficientStake", err)
	}
	if h.eng.IsPending(e.ID) {
		t.Error("rejected event must not appear in pending")
	}
}

func TestEngine_Submit_Duplicate_Error(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)
	e := newTransferEvent(t, "alice", "bob", 500, 1000)

	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("first Submit() = %v", err)
	}
	err := h.eng.Submit(e)
	if !errors.Is(err, ocs.ErrAlreadyPending) {
		t.Errorf("duplicate Submit() = %v, want ErrAlreadyPending", err)
	}
	// Pending count must be 1, not 2.
	if h.eng.PendingCount() != 1 {
		t.Errorf("PendingCount = %d after duplicate submit, want 1", h.eng.PendingCount())
	}
}

func TestEngine_Submit_UnsupportedEventType(t *testing.T) {
	h := newHarness(t, nil)

	// Attestation events are not handled by the OCS engine.
	e, err := event.New(
		event.EventTypeAttestation,
		nil,
		event.AttestationPayload{
			AttestingAgent:  "validator",
			TargetEventID:   "some-event",
			ClaimedAccuracy: 0.9,
			StakedAmount:    1000,
		},
		"validator",
		nil,
		1000,
	)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}

	if err := h.eng.Submit(e); !errors.Is(err, ocs.ErrUnsupportedEventType) {
		t.Errorf("Submit(Attestation) = %v, want ErrUnsupportedEventType", err)
	}
}

func TestEngine_PendingCount_Increments(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)

	if h.eng.PendingCount() != 0 {
		t.Fatalf("initial PendingCount = %d, want 0", h.eng.PendingCount())
	}

	e1 := newTransferEvent(t, "alice", "bob", 100, 1000)
	if err := h.eng.Submit(e1); err != nil {
		t.Fatalf("Submit e1: %v", err)
	}
	if h.eng.PendingCount() != 1 {
		t.Errorf("PendingCount = %d, want 1", h.eng.PendingCount())
	}

	e2 := newGenerationEvent(t, "gen", "ben", 500, 2000)
	if err := h.eng.Submit(e2); err != nil {
		t.Fatalf("Submit e2: %v", err)
	}
	if h.eng.PendingCount() != 2 {
		t.Errorf("PendingCount = %d, want 2", h.eng.PendingCount())
	}
}

func TestEngine_IsPending_TrueForSubmitted(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)
	e := newTransferEvent(t, "alice", "bob", 100, 1000)

	if h.eng.IsPending(e.ID) {
		t.Error("IsPending before Submit = true, want false")
	}
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !h.eng.IsPending(e.ID) {
		t.Error("IsPending after Submit = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Verification result processing (async via SubmitVerification)
// ---------------------------------------------------------------------------

func TestEngine_SubmitVerification_True_TransferSettled(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.eng.Stop()

	fundAgent(t, h, "alice", 100_000)
	e := newTransferEvent(t, "alice", "bob", 500, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := h.eng.SubmitVerification(verdict(e.ID, true, 0)); err != nil {
		t.Fatalf("SubmitVerification: %v", err)
	}

	waitFor(t, time.Second, func() bool { return !h.eng.IsPending(e.ID) })

	history, err := h.tl.History(crypto.AgentID("alice"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("History: err=%v len=%d", err, len(history))
	}
	if history[0].Settlement != event.SettlementSettled {
		t.Errorf("Settlement = %q, want Settled", history[0].Settlement)
	}
}

func TestEngine_SubmitVerification_True_GenerationSettled(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.eng.Stop()

	const claimed = uint64(10_000)
	const verified = uint64(8_000) // partial but still a positive verdict

	e := newGenerationEvent(t, "gen-agent", "ben-agent", claimed, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := h.eng.SubmitVerification(verdict(e.ID, true, verified)); err != nil {
		t.Fatalf("SubmitVerification: %v", err)
	}

	waitFor(t, time.Second, func() bool { return !h.eng.IsPending(e.ID) })

	history, err := h.gl.GenerationHistory(crypto.AgentID("gen-agent"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("GenerationHistory: err=%v len=%d", err, len(history))
	}
	if history[0].Settlement != event.SettlementSettled {
		t.Errorf("Settlement = %q, want Settled", history[0].Settlement)
	}
	if history[0].VerifiedValue != verified {
		t.Errorf("VerifiedValue = %d, want %d", history[0].VerifiedValue, verified)
	}
}

func TestEngine_SubmitVerification_False_Adjusted(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.eng.Stop()

	fundAgent(t, h, "alice", 100_000)
	e := newTransferEvent(t, "alice", "bob", 500, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := h.eng.SubmitVerification(verdict(e.ID, false, 0)); err != nil {
		t.Fatalf("SubmitVerification: %v", err)
	}

	waitFor(t, time.Second, func() bool { return !h.eng.IsPending(e.ID) })

	if h.eng.IsPending(e.ID) {
		t.Error("event still pending after false verdict")
	}

	history, err := h.tl.History(crypto.AgentID("alice"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("History: err=%v len=%d", err, len(history))
	}
	if history[0].Settlement != event.SettlementAdjusted {
		t.Errorf("Settlement = %q, want Adjusted", history[0].Settlement)
	}
}

// ---------------------------------------------------------------------------
// Verification result processing (synchronous via ProcessResult)
// These tests call ProcessResult directly to verify its exact effects on the
// ledger and identity registry without relying on the background goroutine.
// ---------------------------------------------------------------------------

func TestEngine_ProcessResult_True_RecordsTaskCompletion(t *testing.T) {
	h := newHarness(t, nil)
	registerAgent(t, h.reg, "alice")
	fundAgent(t, h, "alice", 100_000)

	e := newTransferEvent(t, "alice", "bob", 500, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	before, err := h.reg.Get(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}

	if err := h.eng.ProcessResult(verdict(e.ID, true, 0)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	after, err := h.reg.Get(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}

	if after.TasksCompleted != before.TasksCompleted+1 {
		t.Errorf("TasksCompleted: before=%d after=%d, want +1",
			before.TasksCompleted, after.TasksCompleted)
	}
	if after.FingerprintVersion != before.FingerprintVersion+1 {
		t.Errorf("FingerprintVersion: before=%d after=%d, want +1",
			before.FingerprintVersion, after.FingerprintVersion)
	}
}

func TestEngine_ProcessResult_False_RecordsTaskFailure(t *testing.T) {
	h := newHarness(t, nil)
	registerAgent(t, h.reg, "alice")
	fundAgent(t, h, "alice", 100_000)

	// First record a successful task to boost OptimisticTrustLimit above the
	// minimum floor (1000 micro-AET), so the subsequent 15% failure reduction
	// is visible rather than clamped back to the floor.
	e0 := newTransferEvent(t, "alice", "carol", 100, 1000)
	if err := h.eng.Submit(e0); err != nil {
		t.Fatalf("Submit e0: %v", err)
	}
	if err := h.eng.ProcessResult(verdict(e0.ID, true, 500)); err != nil {
		t.Fatalf("ProcessResult (success): %v", err)
	}

	boosted, err := h.reg.Get(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("Get after success: %v", err)
	}

	// Now submit and fail a second event.
	e := newTransferEvent(t, "alice", "bob", 500, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit e: %v", err)
	}
	if err := h.eng.ProcessResult(verdict(e.ID, false, 0)); err != nil {
		t.Fatalf("ProcessResult (failure): %v", err)
	}

	after, err := h.reg.Get(crypto.AgentID("alice"))
	if err != nil {
		t.Fatalf("Get after failure: %v", err)
	}

	if after.TasksFailed != boosted.TasksFailed+1 {
		t.Errorf("TasksFailed: before=%d after=%d, want +1",
			boosted.TasksFailed, after.TasksFailed)
	}
	// With OptimisticTrustLimit above the floor, the 15% reduction must be visible.
	if after.OptimisticTrustLimit >= boosted.OptimisticTrustLimit {
		t.Errorf("OptimisticTrustLimit: before=%d after=%d, want decrease",
			boosted.OptimisticTrustLimit, after.OptimisticTrustLimit)
	}
}

func TestEngine_ProcessResult_PartialVerification(t *testing.T) {
	h := newHarness(t, nil)
	registerAgent(t, h.reg, "gen-agent")

	const claimed = uint64(5_000)
	const verified = uint64(3_000) // 60% verified — overclaimer pattern

	e := newGenerationEvent(t, "gen-agent", "ben-agent", claimed, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Positive verdict with VerifiedValue < ClaimedValue: still settles.
	if err := h.eng.ProcessResult(verdict(e.ID, true, verified)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	history, err := h.gl.GenerationHistory(crypto.AgentID("gen-agent"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("GenerationHistory: err=%v len=%d", err, len(history))
	}
	entry := history[0]
	if entry.Settlement != event.SettlementSettled {
		t.Errorf("Settlement = %q, want Settled", entry.Settlement)
	}
	if entry.ClaimedValue != claimed {
		t.Errorf("ClaimedValue = %d, want %d (unchanged)", entry.ClaimedValue, claimed)
	}
	if entry.VerifiedValue != verified {
		t.Errorf("VerifiedValue = %d, want %d", entry.VerifiedValue, verified)
	}
}

func TestEngine_ProcessResult_NotPending_Error(t *testing.T) {
	h := newHarness(t, nil)
	err := h.eng.ProcessResult(verdict("unknown-event-id", true, 0))
	if !errors.Is(err, ocs.ErrNotPending) {
		t.Errorf("ProcessResult(unknown) = %v, want ErrNotPending", err)
	}
}

// ---------------------------------------------------------------------------
// Expiry
// ---------------------------------------------------------------------------

func TestEngine_Expiry_AdjustsLedger(t *testing.T) {
	cfg := &ocs.EngineConfig{
		VerificationTimeout: 50 * time.Millisecond,  // expire quickly
		CheckInterval:       20 * time.Millisecond,  // sweep frequently
		MaxPendingItems:     1000,
		AdjustmentPenalty:   500,
		MinStakeRequired:    1000,
	}
	h := newHarness(t, cfg)

	fundAgent(t, h, "alice", 100_000)
	e := newTransferEvent(t, "alice", "bob", 100, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.eng.Stop()

	// Sleep well past the deadline so the sweep is guaranteed to have fired.
	time.Sleep(200 * time.Millisecond)

	if h.eng.IsPending(e.ID) {
		t.Error("event still pending after timeout, want swept")
	}

	history, err := h.tl.History(crypto.AgentID("alice"), 1, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("History: err=%v len=%d", err, len(history))
	}
	if history[0].Settlement != event.SettlementAdjusted {
		t.Errorf("Settlement = %q, want Adjusted (expired)", history[0].Settlement)
	}
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestEngine_ConcurrentSubmit(t *testing.T) {
	h := newHarness(t, nil)
	const goroutines = 20

	// Fund each concurrent sender.
	for i := 0; i < goroutines; i++ {
		from := fmt.Sprintf("concurrent-sender-%d", i)
		fundAgent(t, h, from, 100_000)
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
				event.TransferPayload{
					FromAgent: from,
					ToAgent:   "sink",
					Amount:    uint64(n+1) * 100,
					Currency:  "AET",
				},
				from,
				nil,
				1000,
			)
			if err != nil {
				return
			}
			_ = h.eng.Submit(e)
		}(i)
	}
	wg.Wait()

	if h.eng.PendingCount() != goroutines {
		t.Errorf("PendingCount = %d, want %d after concurrent submits",
			h.eng.PendingCount(), goroutines)
	}
}

func TestEngine_ConcurrentVerification(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.eng.Stop()

	const goroutines = 20

	// Pre-submit all events sequentially so IDs are known.
	events := make([]*event.Event, goroutines)
	for i := 0; i < goroutines; i++ {
		from := fmt.Sprintf("verif-sender-%d", i)
		fundAgent(t, h, from, 100_000)
		e := newTransferEvent(t, from, "verif-sink", uint64(i+1)*100, 1000)
		if err := h.eng.Submit(e); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		events[i] = e
	}

	if h.eng.PendingCount() != goroutines {
		t.Fatalf("PendingCount = %d before verifications, want %d",
			h.eng.PendingCount(), goroutines)
	}

	// 20 goroutines submit verifications simultaneously.
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			_ = h.eng.SubmitVerification(verdict(events[n].ID, true, 0))
		}(i)
	}
	wg.Wait()

	// All items must reach zero pending without data races.
	waitFor(t, 2*time.Second, func() bool {
		return h.eng.PendingCount() == 0
	})
}

// ---------------------------------------------------------------------------
// Fix 2: Double settlement idempotency
// ---------------------------------------------------------------------------

func TestDoubleSettlement_IsIdempotent(t *testing.T) {
	// Verify that when both a real positive verdict and an expiry false verdict
	// race on the same event, the first one wins and the second is a no-op.
	// The event must end up Settled, not Adjusted.
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100_000)

	e := newTransferEvent(t, "alice", "bob", 500, 1000)
	if err := h.eng.Submit(e); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Process a positive verdict first.
	if err := h.eng.ProcessResult(verdict(e.ID, true, 0)); err != nil {
		t.Fatalf("ProcessResult (true): %v", err)
	}

	// Now simulate the expiry sweep also trying to process the same event.
	// This must not overwrite the Settled state with Adjusted.
	err := h.eng.ProcessResult(verdict(e.ID, false, 0))
	// The second call must be silently idempotent (no error, no state change).
	if err != nil {
		t.Fatalf("second ProcessResult should be idempotent, got: %v", err)
	}

	// The entry must be Settled, not Adjusted.
	history, err := h.tl.History(crypto.AgentID("alice"), 1, 0)
	if err != nil || len(history) == 0 {
		t.Fatalf("History: err=%v len=%d", err, len(history))
	}
	if history[0].Settlement != event.SettlementSettled {
		t.Errorf("Settlement = %q, want Settled (first-write-wins idempotency)", history[0].Settlement)
	}
}

// ---------------------------------------------------------------------------
// Fix 3: Balance validation
// ---------------------------------------------------------------------------

func TestEngine_Submit_ZeroBalance_Rejected(t *testing.T) {
	h := newHarness(t, nil)
	// Do NOT fund alice — she has zero balance.
	e := newTransferEvent(t, "alice", "bob", 500, 1000)

	err := h.eng.Submit(e)
	if err == nil {
		t.Fatal("Submit should reject transfer from zero-balance agent, got nil")
	}
}

func TestEngine_Submit_Overdraft_Rejected(t *testing.T) {
	h := newHarness(t, nil)
	fundAgent(t, h, "alice", 100) // only 100 micro-AET

	e := newTransferEvent(t, "alice", "bob", 500, 1000) // wants to send 500

	err := h.eng.Submit(e)
	if err == nil {
		t.Fatal("Submit should reject overdraft transfer, got nil")
	}
}
