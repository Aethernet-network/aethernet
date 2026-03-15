package validator

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Aethernet-network/aethernet/internal/config"
)

// ---------------------------------------------------------------------------
// In-memory store stub
// ---------------------------------------------------------------------------

type memValidatorStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemValidatorStore() *memValidatorStore {
	return &memValidatorStore{data: make(map[string][]byte)}
}

func (m *memValidatorStore) PutValidator(id string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.data[id] = cp
	return nil
}

func (m *memValidatorStore) GetValidator(id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	blob, ok := m.data[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)
	return cp, nil
}

func (m *memValidatorStore) AllValidators() (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string][]byte, len(m.data))
	for k, v := range m.data {
		cp := make([]byte, len(v))
		copy(cp, v)
		result[k] = cp
	}
	return result, nil
}

func (m *memValidatorStore) DeleteValidator(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, id)
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testCfg() *config.ValidatorConfig {
	return &config.ValidatorConfig{
		StakeBaseMinimum:      10_000_000_000,
		StakeVolumeMultiple:   0.5,
		StakeTaskSizeMultiple: 0.3,
		StakeRecheckPeriod:    1,
		StakeGracePeriod:      7,
		ProbationDuration:     30,
		ProbationMinTasks:     50,
		ProbationMinAccuracy:  0.7,
		ProbationReplayRate:   0.50,
		ProbationCanaryRate:   0.15,
		ProbationMaxCycles:    3,
		ProbationWeightMod:    0.3,
		GenesisSkipProbation:  true,
	}
}

func testRegistry() *ValidatorRegistry {
	return NewValidatorRegistry(testCfg(), nil)
}

const sufficientStake = 10_000_000_000 // == StakeBaseMinimum

// ---------------------------------------------------------------------------
// Test 1: Register with sufficient stake → StatusProbationary
// ---------------------------------------------------------------------------

func TestRegister_SufficientStake(t *testing.T) {
	r := testRegistry()
	v, err := r.Register("agent-1", sufficientStake, []string{"code"}, false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if v.Status != StatusProbationary {
		t.Errorf("expected StatusProbationary, got %s", v.Status)
	}
	if v.AgentID != "agent-1" {
		t.Errorf("expected AgentID=agent-1, got %s", v.AgentID)
	}
	if v.StakeAmount != sufficientStake {
		t.Errorf("expected StakeAmount=%d, got %d", sufficientStake, v.StakeAmount)
	}
	if len(v.Categories) != 1 || v.Categories[0] != "code" {
		t.Errorf("unexpected categories: %v", v.Categories)
	}
	if v.ProbationStartedAt.IsZero() {
		t.Error("ProbationStartedAt should be set for non-genesis")
	}
}

// ---------------------------------------------------------------------------
// Test 2: Register with genesis flag → StatusActive
// ---------------------------------------------------------------------------

func TestRegister_GenesisSkipsProbation(t *testing.T) {
	r := testRegistry()
	v, err := r.Register("agent-genesis", sufficientStake, []string{"code"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Status != StatusActive {
		t.Errorf("expected StatusActive for genesis, got %s", v.Status)
	}
	if !v.ProbationStartedAt.IsZero() {
		t.Error("ProbationStartedAt should be zero for genesis-active validator")
	}
	if !v.IsGenesis {
		t.Error("IsGenesis should be true")
	}
}

// ---------------------------------------------------------------------------
// Test 3: Register with insufficient stake → ErrInsufficientStake
// ---------------------------------------------------------------------------

func TestRegister_InsufficientStake(t *testing.T) {
	r := testRegistry()
	_, err := r.Register("agent-broke", sufficientStake-1, []string{"code"}, false)
	if err == nil {
		t.Fatal("expected ErrInsufficientStake, got nil")
	}
	if !errors.Is(err, ErrInsufficientStake) {
		t.Errorf("expected ErrInsufficientStake, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 4: ComputeRequiredStake — base minimum dominates
// ---------------------------------------------------------------------------

func TestComputeRequiredStake_BaseMinimumDominates(t *testing.T) {
	r := testRegistry()
	// volume=0, count=0, maxTask=0 → all components zero; base minimum wins
	result := r.ComputeRequiredStake(0, 0, 0)
	if result != sufficientStake {
		t.Errorf("expected %d (base minimum), got %d", sufficientStake, result)
	}
}

// ---------------------------------------------------------------------------
// Test 5: ComputeRequiredStake — volume component dominates
// ---------------------------------------------------------------------------

func TestComputeRequiredStake_VolumeComponentDominates(t *testing.T) {
	r := testRegistry()
	// volumeComponent = 0.5 × 1_000_000_000_000 / 1 = 500_000_000_000
	// taskSizeComponent = 0.3 × 0 = 0
	// base = 10_000_000_000
	// → volume dominates
	vol := uint64(1_000_000_000_000)
	result := r.ComputeRequiredStake(vol, 1, 0)
	expected := uint64(float64(0.5) * float64(vol))
	if result != expected {
		t.Errorf("expected %d, got %d", expected, result)
	}
}

// ---------------------------------------------------------------------------
// Test 6: ComputeRequiredStake — task size component dominates
// ---------------------------------------------------------------------------

func TestComputeRequiredStake_TaskSizeComponentDominates(t *testing.T) {
	r := testRegistry()
	// taskSizeComponent = 0.3 × 500_000_000_000 = 150_000_000_000
	// volumeComponent = 0.5 × 10 / 1 = 5 (tiny)
	// base = 10_000_000_000
	// → task size dominates
	maxTask := uint64(500_000_000_000)
	result := r.ComputeRequiredStake(10, 1, maxTask)
	expected := uint64(float64(0.3) * float64(maxTask))
	if result != expected {
		t.Errorf("expected %d, got %d", expected, result)
	}
}

// ---------------------------------------------------------------------------
// Test 7: CheckStakeRequirements — below min within grace → still active
// ---------------------------------------------------------------------------

func TestCheckStakeRequirements_WithinGrace(t *testing.T) {
	r := testRegistry()
	v, _ := r.Register("agent-low-stake", sufficientStake, nil, false)
	id := v.ID

	// Reduce stake below minimum via direct manipulation (allowed in same package).
	r.mu.Lock()
	r.validators[id].StakeAmount = 1
	r.validators[id].LastStakeCheck = time.Now() // fresh timestamp — within grace
	r.mu.Unlock()

	r.CheckStakeRequirements()

	got, _ := r.Get(id)
	if got.Status == StatusSuspended {
		t.Error("expected validator NOT to be suspended within grace period")
	}
}

// ---------------------------------------------------------------------------
// Test 8: CheckStakeRequirements — below min past grace → suspended
// ---------------------------------------------------------------------------

func TestCheckStakeRequirements_PastGrace(t *testing.T) {
	r := testRegistry()
	v, _ := r.Register("agent-past-grace", sufficientStake, nil, false)
	id := v.ID

	// Reduce stake and backdate LastStakeCheck past the grace period.
	r.mu.Lock()
	r.validators[id].StakeAmount = 1
	r.validators[id].LastStakeCheck = time.Now().Add(-8 * 24 * time.Hour) // 8 days ago > 7 day grace
	r.mu.Unlock()

	r.CheckStakeRequirements()

	got, _ := r.Get(id)
	if got.Status != StatusSuspended {
		t.Errorf("expected StatusSuspended, got %s", got.Status)
	}
	if got.SuspensionReason != "stake_below_minimum" {
		t.Errorf("unexpected suspension reason: %s", got.SuspensionReason)
	}
}

// ---------------------------------------------------------------------------
// Test 9: EvaluateProbation — meets requirements → active
// ---------------------------------------------------------------------------

func TestEvaluateProbation_MeetsRequirements(t *testing.T) {
	r := testRegistry()
	v, _ := r.Register("agent-promo", sufficientStake, nil, false)
	id := v.ID

	// Backdate probation start past the duration and inject passing stats.
	r.mu.Lock()
	r.validators[id].ProbationStartedAt = time.Now().Add(-31 * 24 * time.Hour)
	r.validators[id].ProbationTaskCount = 60
	r.validators[id].ProbationAccuracy = 0.85
	r.mu.Unlock()

	if err := r.EvaluateProbation(id); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	got, _ := r.Get(id)
	if got.Status != StatusActive {
		t.Errorf("expected StatusActive, got %s", got.Status)
	}
	if got.ActivatedAt.IsZero() {
		t.Error("ActivatedAt should be set after promotion")
	}
}

// ---------------------------------------------------------------------------
// Test 10: EvaluateProbation — fails requirements → cycle incremented, reset
// ---------------------------------------------------------------------------

func TestEvaluateProbation_FailsRequirements_CycleReset(t *testing.T) {
	r := testRegistry()
	v, _ := r.Register("agent-cycle", sufficientStake, nil, false)
	id := v.ID
	initialCycle := v.ProbationCycle

	// Backdate but do NOT meet task count requirement.
	r.mu.Lock()
	r.validators[id].ProbationStartedAt = time.Now().Add(-31 * 24 * time.Hour)
	r.validators[id].ProbationTaskCount = 10 // below ProbationMinTasks=50
	r.validators[id].ProbationAccuracy = 0.9
	r.mu.Unlock()

	// Should not return an error on plain reset.
	if err := r.EvaluateProbation(id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := r.Get(id)
	if got.Status != StatusProbationary {
		t.Errorf("expected StatusProbationary after cycle reset, got %s", got.Status)
	}
	if got.ProbationCycle != initialCycle+1 {
		t.Errorf("expected cycle %d, got %d", initialCycle+1, got.ProbationCycle)
	}
	if got.ProbationTaskCount != 0 {
		t.Errorf("expected task count reset to 0, got %d", got.ProbationTaskCount)
	}
}

// ---------------------------------------------------------------------------
// Test 11: EvaluateProbation — max cycles exceeded → excluded
// ---------------------------------------------------------------------------

func TestEvaluateProbation_MaxCyclesExceeded_Excluded(t *testing.T) {
	cfg := testCfg()
	cfg.ProbationMaxCycles = 2
	r := NewValidatorRegistry(cfg, nil)
	v, _ := r.Register("agent-maxed", sufficientStake, nil, false)
	id := v.ID

	// Drive through two failed cycles to reach exclusion threshold.
	for cycle := 0; cycle < 2; cycle++ {
		r.mu.Lock()
		r.validators[id].ProbationStartedAt = time.Now().Add(-31 * 24 * time.Hour)
		r.validators[id].ProbationTaskCount = 0
		r.mu.Unlock()

		err := r.EvaluateProbation(id)
		got, _ := r.Get(id)
		if cycle == 1 {
			// On the last call cycle should exceed max.
			if !errors.Is(err, ErrMaxProbationCyclesExceeded) {
				t.Errorf("cycle %d: expected ErrMaxProbationCyclesExceeded, got %v", cycle, err)
			}
			if got.Status != StatusExcluded {
				t.Errorf("cycle %d: expected StatusExcluded, got %s", cycle, got.Status)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test 12: RecordProbationTask — running accuracy mean
// ---------------------------------------------------------------------------

func TestRecordProbationTask_AccuracyMean(t *testing.T) {
	r := testRegistry()
	v, _ := r.Register("agent-record", sufficientStake, nil, false)
	id := v.ID

	// Record: pass, pass, fail → accuracy = (1+1+0)/3 ≈ 0.667
	for _, passed := range []bool{true, true, false} {
		if err := r.RecordProbationTask(id, passed); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	got, _ := r.Get(id)
	if got.ProbationTaskCount != 3 {
		t.Errorf("expected 3 tasks, got %d", got.ProbationTaskCount)
	}
	expected := 2.0 / 3.0
	if diff := got.ProbationAccuracy - expected; diff > 0.001 || diff < -0.001 {
		t.Errorf("expected accuracy ~%.4f, got %.4f", expected, got.ProbationAccuracy)
	}
}

// ---------------------------------------------------------------------------
// Test 13: ResetProbationOnSlash — resets counters, increments cycle
// ---------------------------------------------------------------------------

func TestResetProbationOnSlash(t *testing.T) {
	r := testRegistry()
	v, _ := r.Register("agent-slash", sufficientStake, nil, false)
	id := v.ID
	prevCycle := v.ProbationCycle

	// Build up some task history.
	r.mu.Lock()
	r.validators[id].ProbationTaskCount = 30
	r.validators[id].ProbationAccuracy = 0.80
	r.mu.Unlock()

	if err := r.ResetProbationOnSlash(id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := r.Get(id)
	if got.ProbationTaskCount != 0 {
		t.Errorf("expected task count reset to 0, got %d", got.ProbationTaskCount)
	}
	if got.ProbationAccuracy != 0 {
		t.Errorf("expected accuracy reset to 0, got %f", got.ProbationAccuracy)
	}
	if got.ProbationCycle != prevCycle+1 {
		t.Errorf("expected cycle %d, got %d", prevCycle+1, got.ProbationCycle)
	}
}

// ---------------------------------------------------------------------------
// Test 14: Store round-trip — PutValidator + GetValidator
// ---------------------------------------------------------------------------

func TestStoreRoundTrip(t *testing.T) {
	s := newMemValidatorStore()
	r := NewValidatorRegistry(testCfg(), s)

	v, err := r.Register("agent-store", sufficientStake, []string{"data"}, false)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Retrieve directly from store (bypassing registry cache).
	blob, err := s.GetValidator(v.ID)
	if err != nil {
		t.Fatalf("store.GetValidator: %v", err)
	}
	var loaded Validator
	if err := json.Unmarshal(blob, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.AgentID != "agent-store" {
		t.Errorf("unexpected AgentID: %s", loaded.AgentID)
	}
	if loaded.Status != StatusProbationary {
		t.Errorf("unexpected Status: %s", loaded.Status)
	}
}

// ---------------------------------------------------------------------------
// Test 15: LoadFromStore — reconstructs registry from persisted data
// ---------------------------------------------------------------------------

func TestLoadFromStore(t *testing.T) {
	s := newMemValidatorStore()
	cfg := testCfg()

	// Seed data via a first registry.
	r1 := NewValidatorRegistry(cfg, s)
	v1, _ := r1.Register("agent-a", sufficientStake, []string{"code"}, false)
	v2, _ := r1.Register("agent-b", sufficientStake*2, []string{"data"}, true)

	// Reconstruct from store.
	r2, err := LoadFromStore(cfg, s)
	if err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	got1, err := r2.Get(v1.ID)
	if err != nil {
		t.Fatalf("Get v1: %v", err)
	}
	if got1.AgentID != "agent-a" {
		t.Errorf("expected agent-a, got %s", got1.AgentID)
	}

	got2, err := r2.Get(v2.ID)
	if err != nil {
		t.Fatalf("Get v2: %v", err)
	}
	if got2.Status != StatusActive {
		t.Errorf("expected StatusActive for genesis, got %s", got2.Status)
	}
}

// ---------------------------------------------------------------------------
// Test 16: GetByAgentID — returns correct validator
// ---------------------------------------------------------------------------

func TestGetByAgentID(t *testing.T) {
	r := testRegistry()
	_, err := r.GetByAgentID("nobody")
	if !errors.Is(err, ErrRegistryValidatorNotFound) {
		t.Errorf("expected ErrRegistryValidatorNotFound, got %v", err)
	}

	v, _ := r.Register("agent-lookup", sufficientStake, nil, false)
	got, err := r.GetByAgentID("agent-lookup")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != v.ID {
		t.Errorf("expected ID %s, got %s", v.ID, got.ID)
	}
	if got.AgentID != "agent-lookup" {
		t.Errorf("expected AgentID=agent-lookup, got %s", got.AgentID)
	}
}

// ---------------------------------------------------------------------------
// Additional: IsEligible
// ---------------------------------------------------------------------------

func TestIsEligible(t *testing.T) {
	r := testRegistry()

	// Unknown validator → false.
	if r.IsEligible("nonexistent", "code") {
		t.Error("expected false for nonexistent validator")
	}

	v, _ := r.Register("agent-elig", sufficientStake, []string{"code", "data"}, false)
	id := v.ID

	// Probationary with sufficient stake and matching category → eligible.
	if !r.IsEligible(id, "code") {
		t.Error("expected eligible for probationary validator with matching category")
	}

	// Category not in list → ineligible.
	if r.IsEligible(id, "writing") {
		t.Error("expected ineligible for non-listed category")
	}

	// Empty category → eligible (wildcard).
	if !r.IsEligible(id, "") {
		t.Error("expected eligible with empty category")
	}

	// Suspended → ineligible.
	r.mu.Lock()
	r.validators[id].Status = StatusSuspended
	r.mu.Unlock()
	if r.IsEligible(id, "code") {
		t.Error("expected ineligible for suspended validator")
	}
}

// ---------------------------------------------------------------------------
// Additional: ActiveValidatorsForCategory
// ---------------------------------------------------------------------------

func TestActiveValidatorsForCategory(t *testing.T) {
	r := testRegistry()
	r.Register("agent-c1", sufficientStake, []string{"code"}, false)
	r.Register("agent-c2", sufficientStake, []string{"data"}, false)
	r.Register("agent-c3", sufficientStake, []string{"code", "data"}, true)

	codeVals := r.ActiveValidatorsForCategory("code")
	if len(codeVals) != 2 {
		t.Errorf("expected 2 code validators, got %d", len(codeVals))
	}
	dataVals := r.ActiveValidatorsForCategory("data")
	if len(dataVals) != 2 {
		t.Errorf("expected 2 data validators, got %d", len(dataVals))
	}
	writingVals := r.ActiveValidatorsForCategory("writing")
	if len(writingVals) != 0 {
		t.Errorf("expected 0 writing validators, got %d", len(writingVals))
	}
}
