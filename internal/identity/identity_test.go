package identity_test

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/identity"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// agentID generates a deterministic fake AgentID for testing. Using real
// Ed25519 keypairs is unnecessary here — the identity package operates on
// AgentID as an opaque string.
func agentID(n int) crypto.AgentID {
	// 64 hex chars = 32 bytes = valid Ed25519 pubkey length format.
	return crypto.AgentID(fmt.Sprintf("%064x", n))
}

func pubKey(n int) []byte {
	b := make([]byte, 32)
	b[0] = byte(n)
	return b
}

func mustNewFingerprint(t *testing.T, id crypto.AgentID) *identity.CapabilityFingerprint {
	t.Helper()
	fp, err := identity.NewFingerprint(id, pubKey(1), nil)
	if err != nil {
		t.Fatalf("NewFingerprint(%s): %v", id, err)
	}
	return fp
}

// mustRegister creates a fresh fingerprint, registers it, and returns it.
func mustRegister(t *testing.T, r *identity.Registry, id crypto.AgentID) *identity.CapabilityFingerprint {
	t.Helper()
	fp := mustNewFingerprint(t, id)
	if err := r.Register(fp); err != nil {
		t.Fatalf("Register(%s): %v", id, err)
	}
	return fp
}

// mustGet retrieves a fingerprint or fails the test.
func mustGet(t *testing.T, r *identity.Registry, id crypto.AgentID) *identity.CapabilityFingerprint {
	t.Helper()
	fp, err := r.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return fp
}

// ---------------------------------------------------------------------------
// NewFingerprint
// ---------------------------------------------------------------------------

func TestNewFingerprint_FieldsInitialised(t *testing.T) {
	id := agentID(1)
	pk := pubKey(1)
	fp, err := identity.NewFingerprint(id, pk, []identity.Capability{
		{Domain: "nlp.summarization", Confidence: 5000, EvidenceCount: 0},
	})
	if err != nil {
		t.Fatalf("NewFingerprint: %v", err)
	}

	if fp.AgentID != id {
		t.Errorf("AgentID = %q, want %q", fp.AgentID, id)
	}
	if string(fp.PublicKey) != string(pk) {
		t.Error("PublicKey mismatch")
	}
	if fp.TasksCompleted != 0 {
		t.Errorf("TasksCompleted = %d, want 0", fp.TasksCompleted)
	}
	if fp.TasksFailed != 0 {
		t.Errorf("TasksFailed = %d, want 0", fp.TasksFailed)
	}
	if fp.ReputationScore != 0 {
		t.Errorf("ReputationScore = %d, want 0 for new agent", fp.ReputationScore)
	}
	if fp.OptimisticTrustLimit != 1000 {
		t.Errorf("OptimisticTrustLimit = %d, want 1000 (minTrustLimit)", fp.OptimisticTrustLimit)
	}
	if fp.FingerprintVersion != 1 {
		t.Errorf("FingerprintVersion = %d, want 1", fp.FingerprintVersion)
	}
	if len(fp.Capabilities) != 1 {
		t.Errorf("Capabilities len = %d, want 1", len(fp.Capabilities))
	}
}

func TestNewFingerprint_NilCapabilitiesNormalised(t *testing.T) {
	fp, err := identity.NewFingerprint(agentID(1), pubKey(1), nil)
	if err != nil {
		t.Fatalf("NewFingerprint: %v", err)
	}
	// nil capabilities should be normalised to an empty (non-nil) slice
	// for consistent hashing, same as event.New normalises CausalRefs.
	if fp.Capabilities == nil {
		t.Error("Capabilities should not be nil after NewFingerprint with nil input")
	}
}

func TestNewFingerprint_HashIsSet(t *testing.T) {
	fp, _ := identity.NewFingerprint(agentID(1), pubKey(1), nil)
	if fp.FingerprintHash == "" {
		t.Error("FingerprintHash must be non-empty after NewFingerprint")
	}
	if len(fp.FingerprintHash) != 64 {
		t.Errorf("FingerprintHash len = %d, want 64 (hex SHA-256)", len(fp.FingerprintHash))
	}
}

func TestNewFingerprint_EmptyAgentID_Error(t *testing.T) {
	_, err := identity.NewFingerprint("", pubKey(1), nil)
	if err == nil {
		t.Error("expected error for empty AgentID, got nil")
	}
}

func TestNewFingerprint_EmptyPublicKey_Error(t *testing.T) {
	_, err := identity.NewFingerprint(agentID(1), nil, nil)
	if err == nil {
		t.Error("expected error for nil PublicKey, got nil")
	}
	_, err = identity.NewFingerprint(agentID(1), []byte{}, nil)
	if err == nil {
		t.Error("expected error for empty PublicKey, got nil")
	}
}

// ---------------------------------------------------------------------------
// FingerprintHash
// ---------------------------------------------------------------------------

func TestComputeHash_Deterministic(t *testing.T) {
	fp, _ := identity.NewFingerprint(agentID(1), pubKey(1), nil)

	h1, err := fp.ComputeHash()
	if err != nil {
		t.Fatalf("ComputeHash first call: %v", err)
	}
	h2, err := fp.ComputeHash()
	if err != nil {
		t.Fatalf("ComputeHash second call: %v", err)
	}
	if h1 != h2 {
		t.Error("ComputeHash returned different values on consecutive calls for the same fingerprint")
	}
}

func TestFingerprintHash_ChangesAfterMutation(t *testing.T) {
	// We exercise mutation via the Registry and verify the hash stored changes.
	r := identity.NewRegistry()
	id := agentID(1)
	fp := mustRegister(t, r, id)
	hashBefore := fp.FingerprintHash

	if err := r.RecordTaskCompletion(id, 500, "nlp.summarization"); err != nil {
		t.Fatalf("RecordTaskCompletion: %v", err)
	}

	updated := mustGet(t, r, id)
	if updated.FingerprintHash == hashBefore {
		t.Error("FingerprintHash did not change after task completion — hash not refreshed")
	}
}

func TestFingerprintHash_DifferentForDifferentAgents(t *testing.T) {
	fp1, _ := identity.NewFingerprint(agentID(1), pubKey(1), nil)
	fp2, _ := identity.NewFingerprint(agentID(2), pubKey(2), nil)
	if fp1.FingerprintHash == fp2.FingerprintHash {
		t.Error("different agents produced identical FingerprintHash values")
	}
}

// ---------------------------------------------------------------------------
// Registry: Register / Get
// ---------------------------------------------------------------------------

func TestRegistry_Register_Get_Roundtrip(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(42)
	pk := pubKey(42)

	fp, err := identity.NewFingerprint(id, pk, []identity.Capability{
		{Domain: "code.generation", Confidence: 7500, EvidenceCount: 10},
	})
	if err != nil {
		t.Fatalf("NewFingerprint: %v", err)
	}
	if err := r.Register(fp); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentID != id {
		t.Errorf("AgentID = %q, want %q", got.AgentID, id)
	}
	if string(got.PublicKey) != string(pk) {
		t.Error("PublicKey mismatch after roundtrip")
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0].Domain != "code.generation" {
		t.Error("Capabilities not preserved through Register/Get roundtrip")
	}
}

func TestRegistry_Get_ReturnsClone(t *testing.T) {
	// Mutating the returned fingerprint must not affect the stored version.
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	got1 := mustGet(t, r, id)
	got1.TasksCompleted = 9999
	got1.ReputationScore = 9999

	got2 := mustGet(t, r, id)
	if got2.TasksCompleted == 9999 {
		t.Error("TasksCompleted was mutated on the stored fingerprint via Get's return value")
	}
	if got2.ReputationScore == 9999 {
		t.Error("ReputationScore was mutated on the stored fingerprint via Get's return value")
	}
}

func TestRegistry_Get_ClonesCapabilities(t *testing.T) {
	// Mutating a capability in the returned clone must not affect the stored entry.
	r := identity.NewRegistry()
	id := agentID(1)
	fp, _ := identity.NewFingerprint(id, pubKey(1), []identity.Capability{
		{Domain: "data.analysis", Confidence: 3000, EvidenceCount: 5},
	})
	_ = r.Register(fp)

	got := mustGet(t, r, id)
	got.Capabilities[0].EvidenceCount = 99999

	got2 := mustGet(t, r, id)
	if got2.Capabilities[0].EvidenceCount == 99999 {
		t.Error("Capability slice was not deep-copied — mutation leaked into registry state")
	}
}

func TestRegistry_Register_DuplicateReturnsError(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	fp2, _ := identity.NewFingerprint(id, pubKey(1), nil)
	err := r.Register(fp2)
	if err == nil {
		t.Fatal("second Register with same AgentID should return error, got nil")
	}
	if !errors.Is(err, identity.ErrAgentAlreadyExists) {
		t.Errorf("want ErrAgentAlreadyExists, got %v", err)
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := identity.NewRegistry()
	_, err := r.Get(agentID(99))
	if err == nil {
		t.Fatal("Get on unregistered agent should return error, got nil")
	}
	if !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("want ErrAgentNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registry: Update
// ---------------------------------------------------------------------------

func TestRegistry_Update_ValidVersionIncrement(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	// Get current, bump version, update.
	got := mustGet(t, r, id)
	got.FingerprintVersion++ // 1 → 2
	got.StakedAmount = 5000

	if err := r.Update(id, got); err != nil {
		t.Fatalf("Update with correct version increment: %v", err)
	}

	stored := mustGet(t, r, id)
	if stored.FingerprintVersion != 2 {
		t.Errorf("FingerprintVersion = %d, want 2", stored.FingerprintVersion)
	}
	if stored.StakedAmount != 5000 {
		t.Errorf("StakedAmount = %d, want 5000", stored.StakedAmount)
	}
}

func TestRegistry_Update_RejectsVersionDowngrade(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	got := mustGet(t, r, id)
	// Do NOT increment version — same version is a downgrade in intent.
	err := r.Update(id, got)
	if err == nil {
		t.Fatal("Update with same version should return error, got nil")
	}
	if !errors.Is(err, identity.ErrVersionMismatch) {
		t.Errorf("want ErrVersionMismatch, got %v", err)
	}
}

func TestRegistry_Update_RejectsVersionSkip(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	got := mustGet(t, r, id)
	got.FingerprintVersion += 2 // skip a version (1 → 3, not 2)
	err := r.Update(id, got)
	if err == nil {
		t.Fatal("Update with skipped version should return error, got nil")
	}
	if !errors.Is(err, identity.ErrVersionMismatch) {
		t.Errorf("want ErrVersionMismatch, got %v", err)
	}
}

func TestRegistry_Update_NotFound(t *testing.T) {
	r := identity.NewRegistry()
	fp, _ := identity.NewFingerprint(agentID(99), pubKey(1), nil)
	err := r.Update(agentID(99), fp)
	if !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("want ErrAgentNotFound, got %v", err)
	}
}

func TestRegistry_Update_RecomputesHash(t *testing.T) {
	// The Registry must overwrite the caller's FingerprintHash with its own
	// computation. Set a garbage hash in the incoming fingerprint and verify
	// it gets replaced.
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	got := mustGet(t, r, id)
	got.FingerprintVersion++
	got.FingerprintHash = "garbage-hash-that-should-be-replaced"

	if err := r.Update(id, got); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored := mustGet(t, r, id)
	if stored.FingerprintHash == "garbage-hash-that-should-be-replaced" {
		t.Error("Registry accepted caller-provided FingerprintHash instead of recomputing it")
	}
	if len(stored.FingerprintHash) != 64 {
		t.Errorf("FingerprintHash after Update has len %d, want 64", len(stored.FingerprintHash))
	}
}

// ---------------------------------------------------------------------------
// RecordTaskCompletion
// ---------------------------------------------------------------------------

func TestRecordTaskCompletion_UpdatesAllFields(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	beforeGet := mustGet(t, r, id)
	versionBefore := beforeGet.FingerprintVersion
	hashBefore := beforeGet.FingerprintHash

	if err := r.RecordTaskCompletion(id, 2000, "data.analysis"); err != nil {
		t.Fatalf("RecordTaskCompletion: %v", err)
	}

	got := mustGet(t, r, id)

	if got.TasksCompleted != 1 {
		t.Errorf("TasksCompleted = %d, want 1", got.TasksCompleted)
	}
	if got.TotalValueGenerated != 2000 {
		t.Errorf("TotalValueGenerated = %d, want 2000", got.TotalValueGenerated)
	}
	if got.FingerprintVersion != versionBefore+1 {
		t.Errorf("FingerprintVersion = %d, want %d", got.FingerprintVersion, versionBefore+1)
	}
	if got.FingerprintHash == hashBefore {
		t.Error("FingerprintHash did not change after task completion")
	}
	// Trust limit should have increased from 1000.
	// increase = 500 + 2000/100 = 500 + 20 = 520; new limit = 1000 + 520 = 1520
	if got.OptimisticTrustLimit != 1520 {
		t.Errorf("OptimisticTrustLimit = %d, want 1520 (1000 + 500 + 20)", got.OptimisticTrustLimit)
	}
	// ReputationScore: 1 completion, 0 failures → 1*10000 / (1+0+1) = 5000
	if got.ReputationScore != 5000 {
		t.Errorf("ReputationScore = %d, want 5000", got.ReputationScore)
	}
}

func TestRecordTaskCompletion_AddsNewCapabilityDomain(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	if err := r.RecordTaskCompletion(id, 0, "nlp.translation"); err != nil {
		t.Fatalf("RecordTaskCompletion: %v", err)
	}

	got := mustGet(t, r, id)
	if len(got.Capabilities) != 1 {
		t.Fatalf("Capabilities len = %d, want 1 (new domain added)", len(got.Capabilities))
	}
	if got.Capabilities[0].Domain != "nlp.translation" {
		t.Errorf("Capabilities[0].Domain = %q, want %q", got.Capabilities[0].Domain, "nlp.translation")
	}
	if got.Capabilities[0].EvidenceCount != 1 {
		t.Errorf("Capabilities[0].EvidenceCount = %d, want 1", got.Capabilities[0].EvidenceCount)
	}
}

func TestRecordTaskCompletion_IncrementsExistingCapability(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	fp, _ := identity.NewFingerprint(id, pubKey(1), []identity.Capability{
		{Domain: "code.review", Confidence: 4000, EvidenceCount: 5},
	})
	_ = r.Register(fp)

	// Complete two tasks in the existing domain.
	_ = r.RecordTaskCompletion(id, 0, "code.review")
	_ = r.RecordTaskCompletion(id, 0, "code.review")

	got := mustGet(t, r, id)
	if len(got.Capabilities) != 1 {
		t.Errorf("Capabilities len = %d; existing domain should not be duplicated", len(got.Capabilities))
	}
	if got.Capabilities[0].EvidenceCount != 7 { // 5 + 2
		t.Errorf("EvidenceCount = %d, want 7 (5 original + 2 completions)", got.Capabilities[0].EvidenceCount)
	}
}

func TestRecordTaskCompletion_MultipleCalls_Accumulate(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	for i := 0; i < 5; i++ {
		if err := r.RecordTaskCompletion(id, 1000, "inference"); err != nil {
			t.Fatalf("RecordTaskCompletion %d: %v", i, err)
		}
	}

	got := mustGet(t, r, id)
	if got.TasksCompleted != 5 {
		t.Errorf("TasksCompleted = %d, want 5", got.TasksCompleted)
	}
	if got.TotalValueGenerated != 5000 {
		t.Errorf("TotalValueGenerated = %d, want 5000", got.TotalValueGenerated)
	}
}

func TestRecordTaskCompletion_NotFound(t *testing.T) {
	r := identity.NewRegistry()
	err := r.RecordTaskCompletion(agentID(99), 0, "domain")
	if !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("want ErrAgentNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RecordTaskFailure
// ---------------------------------------------------------------------------

func TestRecordTaskFailure_UpdatesAllFields(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	versionBefore := mustGet(t, r, id).FingerprintVersion

	if err := r.RecordTaskFailure(id, "nlp"); err != nil {
		t.Fatalf("RecordTaskFailure: %v", err)
	}

	got := mustGet(t, r, id)
	if got.TasksFailed != 1 {
		t.Errorf("TasksFailed = %d, want 1", got.TasksFailed)
	}
	if got.FingerprintVersion != versionBefore+1 {
		t.Errorf("FingerprintVersion = %d, want %d", got.FingerprintVersion, versionBefore+1)
	}
	// ReputationScore: 0 completions, 1 failure → 0 * 10000 / (0+1+1) = 0
	if got.ReputationScore != 0 {
		t.Errorf("ReputationScore = %d, want 0 (no completions)", got.ReputationScore)
	}
}

func TestRecordTaskFailure_NotFound(t *testing.T) {
	r := identity.NewRegistry()
	err := r.RecordTaskFailure(agentID(99), "domain")
	if !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("want ErrAgentNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ReputationScore formula — boundary cases
// ---------------------------------------------------------------------------

func TestReputationScore_ZeroTasks(t *testing.T) {
	// New agent: score = 0 * 10000 / (0 + 0 + 1) = 0
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)
	got := mustGet(t, r, id)
	if got.ReputationScore != 0 {
		t.Errorf("new agent ReputationScore = %d, want 0", got.ReputationScore)
	}
}

func TestReputationScore_PerfectRecord(t *testing.T) {
	// N completions, 0 failures: score = N*10000 / (N+1)
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	const n = 9
	for i := 0; i < n; i++ {
		_ = r.RecordTaskCompletion(id, 0, "domain")
	}

	got := mustGet(t, r, id)
	// score = 9 * 10000 / (9 + 0 + 1) = 90000 / 10 = 9000
	want := uint64(n * 10000 / (n + 1))
	if got.ReputationScore != want {
		t.Errorf("ReputationScore = %d, want %d (n=%d, 0 failures)", got.ReputationScore, want, n)
	}
}

func TestReputationScore_AllFailures(t *testing.T) {
	// 0 completions, N failures: score = 0 always
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	for i := 0; i < 10; i++ {
		_ = r.RecordTaskFailure(id, "domain")
	}

	got := mustGet(t, r, id)
	if got.ReputationScore != 0 {
		t.Errorf("all-failure agent ReputationScore = %d, want 0", got.ReputationScore)
	}
}

func TestReputationScore_MixedRecord(t *testing.T) {
	// M completions + F failures: score = M*10000 / (M+F+1)
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	const (
		completions = 6
		failures    = 3
	)
	for i := 0; i < completions; i++ {
		_ = r.RecordTaskCompletion(id, 0, "domain")
	}
	for i := 0; i < failures; i++ {
		_ = r.RecordTaskFailure(id, "domain")
	}

	got := mustGet(t, r, id)
	// score = 6 * 10000 / (6 + 3 + 1) = 60000 / 10 = 6000
	want := uint64(completions * 10000 / (completions + failures + 1))
	if got.ReputationScore != want {
		t.Errorf("ReputationScore = %d, want %d (%d completions, %d failures)",
			got.ReputationScore, want, completions, failures)
	}
}

func TestReputationScore_SingleCompletion(t *testing.T) {
	// 1 completion, 0 failures: score = 10000 / 2 = 5000
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)
	_ = r.RecordTaskCompletion(id, 0, "domain")
	got := mustGet(t, r, id)
	if got.ReputationScore != 5000 {
		t.Errorf("ReputationScore after 1 completion = %d, want 5000", got.ReputationScore)
	}
}

// ---------------------------------------------------------------------------
// OptimisticTrustLimit
// ---------------------------------------------------------------------------

func TestOptimisticTrustLimit_NeverDropsBelowMinimum(t *testing.T) {
	// Even with repeated failures the trust limit must not go below 1000.
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	// New agent starts at 1000 (minTrustLimit).
	// After one failure: 1000 * 85 / 100 = 850 → clamped to 1000.
	for i := 0; i < 20; i++ {
		if err := r.RecordTaskFailure(id, "domain"); err != nil {
			t.Fatalf("RecordTaskFailure %d: %v", i, err)
		}
		got := mustGet(t, r, id)
		if got.OptimisticTrustLimit < 1000 {
			t.Errorf("after %d failures: OptimisticTrustLimit = %d, must not be below 1000",
				i+1, got.OptimisticTrustLimit)
		}
	}
}

func TestOptimisticTrustLimit_CapsAtStakedAmountTimes10(t *testing.T) {
	// With StakedAmount=500, cap is 5000. After 8 completions of 0 value:
	// 1000 + 8*500 = 5000. The 9th should not exceed 5000.
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	// Set StakedAmount via Update.
	fp := mustGet(t, r, id)
	fp.FingerprintVersion++
	fp.StakedAmount = 500
	if err := r.Update(id, fp); err != nil {
		t.Fatalf("Update to set StakedAmount: %v", err)
	}

	// Do enough completions to exceed the cap.
	for i := 0; i < 12; i++ {
		_ = r.RecordTaskCompletion(id, 0, "domain") // each adds 500
	}

	got := mustGet(t, r, id)
	if got.OptimisticTrustLimit > 5000 {
		t.Errorf("OptimisticTrustLimit = %d, must not exceed StakedAmount(%d) × 10 = 5000",
			got.OptimisticTrustLimit, got.StakedAmount)
	}
}

func TestOptimisticTrustLimit_GrowsWithValueGenerated(t *testing.T) {
	// increase = 500 + valueGenerated/100
	// With valueGenerated=10000: increase = 500 + 100 = 600
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	_ = r.RecordTaskCompletion(id, 10000, "domain")

	got := mustGet(t, r, id)
	// 1000 + 500 + 10000/100 = 1000 + 500 + 100 = 1600
	if got.OptimisticTrustLimit != 1600 {
		t.Errorf("OptimisticTrustLimit = %d, want 1600 after valueGenerated=10000", got.OptimisticTrustLimit)
	}
}

func TestOptimisticTrustLimit_FailureReduces15Percent(t *testing.T) {
	// Start at 1000, do a completion to raise above the floor, then fail.
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	// After completion: 1000 + 500 = 1500
	_ = r.RecordTaskCompletion(id, 0, "domain")
	// After failure: 1500 * 85 / 100 = 1275
	_ = r.RecordTaskFailure(id, "domain")

	got := mustGet(t, r, id)
	if got.OptimisticTrustLimit != 1275 {
		t.Errorf("OptimisticTrustLimit = %d, want 1275 (1500 * 85/100)", got.OptimisticTrustLimit)
	}
}

// ---------------------------------------------------------------------------
// CanTransact
// ---------------------------------------------------------------------------

func TestCanTransact_BelowBothLimits_True(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	fp := mustNewFingerprint(t, id)
	fp.StakedAmount = 2000 // set stake before registering
	// Register via Update: register at version 1, then update to set stake
	_ = r.Register(fp)
	// Need to set stake via Update (can't set it in NewFingerprint directly)
	stored := mustGet(t, r, id)
	stored.FingerprintVersion++
	stored.StakedAmount = 2000
	_ = r.Update(id, stored)

	// OptimisticTrustLimit starts at 1000; amount=500 is below both 1000 and 2000.
	ok, err := r.CanTransact(id, 500)
	if err != nil {
		t.Fatalf("CanTransact: %v", err)
	}
	if !ok {
		t.Error("CanTransact(500) = false, want true (below both limits)")
	}
}

func TestCanTransact_AtExactLimit_True(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	fp := mustGet(t, r, id)
	fp.FingerprintVersion++
	fp.StakedAmount = 5000
	_ = r.Update(id, fp)

	// OptimisticTrustLimit is still 1000; amount exactly equals it.
	ok, err := r.CanTransact(id, 1000)
	if err != nil {
		t.Fatalf("CanTransact: %v", err)
	}
	if !ok {
		t.Error("CanTransact(amount == OptimisticTrustLimit) = false, want true")
	}
}

func TestCanTransact_AboveTrustLimit_False(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	// Set stake well above the trust limit so only trust limit binds.
	fp := mustGet(t, r, id)
	fp.FingerprintVersion++
	fp.StakedAmount = 1_000_000
	_ = r.Update(id, fp)

	// Trust limit is 1000; amount=1001 exceeds it.
	ok, err := r.CanTransact(id, 1001)
	if err != nil {
		t.Fatalf("CanTransact: %v", err)
	}
	if ok {
		t.Error("CanTransact(amount > OptimisticTrustLimit) = true, want false")
	}
}

func TestCanTransact_AboveStakedAmount_False(t *testing.T) {
	// Even if amount is within the trust limit, a low stake blocks the transaction.
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	// Set a low stake (100) and grow trust limit above it via completions.
	fp := mustGet(t, r, id)
	fp.FingerprintVersion++
	fp.StakedAmount = 100 // stake < trust limit floor (1000)
	_ = r.Update(id, fp)

	// 100 < 1000 (minTrustLimit), so even amount=100 should be blocked because
	// amount > StakedAmount... wait, 100 == 100, that should pass.
	// Let's use amount=101.
	ok, err := r.CanTransact(id, 101)
	if err != nil {
		t.Fatalf("CanTransact: %v", err)
	}
	if ok {
		t.Error("CanTransact(amount > StakedAmount) = true, want false (stake ceiling)")
	}
}

func TestCanTransact_ZeroStake_BlocksAllNonZeroAmounts(t *testing.T) {
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id) // StakedAmount defaults to 0

	ok, err := r.CanTransact(id, 1)
	if err != nil {
		t.Fatalf("CanTransact: %v", err)
	}
	if ok {
		t.Error("CanTransact with zero stake and amount=1 should be false (no skin in the game)")
	}
}

func TestCanTransact_NotFound(t *testing.T) {
	r := identity.NewRegistry()
	_, err := r.CanTransact(agentID(99), 100)
	if !errors.Is(err, identity.ErrAgentNotFound) {
		t.Errorf("want ErrAgentNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// All() pagination
// ---------------------------------------------------------------------------

func TestAll_Empty(t *testing.T) {
	r := identity.NewRegistry()
	got := r.All(0, 0)
	if len(got) != 0 {
		t.Errorf("All on empty registry: len = %d, want 0", len(got))
	}
}

func TestAll_ReturnsAllWithZeroLimit(t *testing.T) {
	r := identity.NewRegistry()
	for i := 0; i < 5; i++ {
		mustRegister(t, r, agentID(i))
	}
	got := r.All(0, 0)
	if len(got) != 5 {
		t.Errorf("All(0,0): len = %d, want 5", len(got))
	}
}

func TestAll_LimitSlicesResult(t *testing.T) {
	r := identity.NewRegistry()
	for i := 0; i < 5; i++ {
		mustRegister(t, r, agentID(i))
	}
	got := r.All(3, 0)
	if len(got) != 3 {
		t.Errorf("All(3,0): len = %d, want 3", len(got))
	}
}

func TestAll_OffsetSkipsEntries(t *testing.T) {
	r := identity.NewRegistry()
	for i := 0; i < 5; i++ {
		mustRegister(t, r, agentID(i))
	}
	got := r.All(0, 3)
	if len(got) != 2 {
		t.Errorf("All(0,3): len = %d, want 2", len(got))
	}
}

func TestAll_OffsetBeyondLength_ReturnsEmpty(t *testing.T) {
	r := identity.NewRegistry()
	for i := 0; i < 3; i++ {
		mustRegister(t, r, agentID(i))
	}
	got := r.All(0, 10)
	if len(got) != 0 {
		t.Errorf("All(0,10) with 3 agents: len = %d, want 0", len(got))
	}
}

func TestAll_SortedByAgentID(t *testing.T) {
	r := identity.NewRegistry()
	// Register in reverse order to ensure sorting is applied, not insertion order.
	for i := 9; i >= 0; i-- {
		mustRegister(t, r, agentID(i))
	}
	got := r.All(0, 0)
	if len(got) != 10 {
		t.Fatalf("All: len = %d, want 10", len(got))
	}
	ids := make([]string, len(got))
	for i, fp := range got {
		ids[i] = string(fp.AgentID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("All() result is not sorted by AgentID: %v", ids)
	}
}

func TestAll_PaginationCoversAllEntries(t *testing.T) {
	// Two consecutive pages with no overlap and no gaps.
	r := identity.NewRegistry()
	for i := 0; i < 6; i++ {
		mustRegister(t, r, agentID(i))
	}

	page1 := r.All(3, 0) // agents 0,1,2
	page2 := r.All(3, 3) // agents 3,4,5

	if len(page1) != 3 || len(page2) != 3 {
		t.Fatalf("pages have wrong sizes: %d and %d", len(page1), len(page2))
	}

	seen := make(map[crypto.AgentID]bool)
	for _, fp := range append(page1, page2...) {
		if seen[fp.AgentID] {
			t.Errorf("AgentID %s appeared in multiple pages (overlap)", fp.AgentID)
		}
		seen[fp.AgentID] = true
	}
	if len(seen) != 6 {
		t.Errorf("two pages together cover %d unique agents, want 6", len(seen))
	}
}

func TestAll_ReturnsClones(t *testing.T) {
	r := identity.NewRegistry()
	mustRegister(t, r, agentID(1))

	all := r.All(0, 0)
	all[0].TasksCompleted = 99999

	got := mustGet(t, r, agentID(1))
	if got.TasksCompleted == 99999 {
		t.Error("All() returned a live pointer — mutation leaked into registry state")
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestConcurrent_RecordTaskCompletion_Safe(t *testing.T) {
	// 50 goroutines each record one task completion for the same agent.
	// The final TasksCompleted must be exactly 50 with zero races.
	r := identity.NewRegistry()
	id := agentID(1)
	mustRegister(t, r, id)

	const n = 50
	ready := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-ready
			errs[i] = r.RecordTaskCompletion(id, uint64(i*10), fmt.Sprintf("domain-%d", i%5))
		}()
	}
	close(ready)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	got := mustGet(t, r, id)
	if got.TasksCompleted != n {
		t.Errorf("TasksCompleted = %d, want %d after %d concurrent completions", got.TasksCompleted, n, n)
	}
}

func TestConcurrent_MixedReadsWrites_Safe(t *testing.T) {
	// Writers record completions while readers call Get, CanTransact, and All.
	// With -race, any missing lock will be caught.
	r := identity.NewRegistry()
	for i := 0; i < 5; i++ {
		mustRegister(t, r, agentID(i))
	}

	ready := make(chan struct{})
	var wg sync.WaitGroup
	const writers, readers = 20, 20

	// Writers
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-ready
			_ = r.RecordTaskCompletion(agentID(i%5), 0, "domain")
		}()
	}

	// Readers
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			<-ready
			_, _ = r.Get(agentID(0))
			_, _ = r.CanTransact(agentID(0), 100)
			_ = r.All(3, 0)
		}()
	}

	close(ready)
	wg.Wait()
}
