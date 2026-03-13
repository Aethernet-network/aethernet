package canary

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// In-memory canaryStore for tests
// ---------------------------------------------------------------------------

type memCanaryStore struct {
	mu        sync.Mutex
	canaries  map[string][]byte
	taskIndex map[string]string
}

func newMemCanaryStore() *memCanaryStore {
	return &memCanaryStore{
		canaries:  make(map[string][]byte),
		taskIndex: make(map[string]string),
	}
}

func (m *memCanaryStore) PutCanary(c *CanaryTask) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("memCanaryStore: marshal: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.canaries[c.ID] = append([]byte{}, data...)
	if c.TaskID != "" {
		m.taskIndex[c.TaskID] = c.ID
	}
	return nil
}

func (m *memCanaryStore) GetCanary(id string) (*CanaryTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.canaries[id]
	if !ok {
		return nil, fmt.Errorf("canary not found: %s", id)
	}
	var c CanaryTask
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (m *memCanaryStore) GetCanaryByTaskID(taskID string) (*CanaryTask, error) {
	m.mu.Lock()
	canaryID, ok := m.taskIndex[taskID]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("canary task index not found: %s", taskID)
	}
	data, ok2 := m.canaries[canaryID]
	m.mu.Unlock()
	if !ok2 {
		return nil, fmt.Errorf("canary not found: %s", canaryID)
	}
	var c CanaryTask
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (m *memCanaryStore) AllCanaries() ([]*CanaryTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*CanaryTask, 0, len(m.canaries))
	for _, data := range m.canaries {
		var c CanaryTask
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		result = append(result, &c)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestNewCanaryTask_GeneratesUniqueIDs verifies that two calls to NewCanaryTask
// with the same arguments produce different IDs (nanosecond-resolution clock).
func TestNewCanaryTask_GeneratesUniqueIDs(t *testing.T) {
	c1 := NewCanaryTask("code", TypeKnownGood, true, nil, "sha256:abc")
	c2 := NewCanaryTask("code", TypeKnownGood, true, nil, "sha256:abc")

	if c1.ID == "" {
		t.Error("c1.ID must not be empty")
	}
	if c2.ID == "" {
		t.Error("c2.ID must not be empty")
	}
	// IDs should have the "cnr-" prefix.
	if !strings.HasPrefix(c1.ID, "cnr-") {
		t.Errorf("c1.ID = %q; want prefix 'cnr-'", c1.ID)
	}
	if !strings.HasPrefix(c2.ID, "cnr-") {
		t.Errorf("c2.ID = %q; want prefix 'cnr-'", c2.ID)
	}
	// IDs must differ (relies on sub-nanosecond execution being rare; true on
	// modern hardware where two function calls take > 1 ns wall time).
	if c1.ID == c2.ID {
		t.Errorf("NewCanaryTask produced identical IDs %q for two distinct calls", c1.ID)
	}
}

// TestNewCanaryTask_FieldDefaults verifies that NewCanaryTask sets Status,
// PolicyVersion, and CreatedAt correctly.
func TestNewCanaryTask_FieldDefaults(t *testing.T) {
	c := NewCanaryTask("writing", TypeAdversarial, false, map[string]bool{"lint": false}, "sha256:xyz")

	if c.Status != StatusActive {
		t.Errorf("Status = %q; want %q", c.Status, StatusActive)
	}
	if c.PolicyVersion != "v1" {
		t.Errorf("PolicyVersion = %q; want %q", c.PolicyVersion, "v1")
	}
	if c.CreatedAt.IsZero() {
		t.Error("CreatedAt must be set")
	}
	if c.Category != "writing" {
		t.Errorf("Category = %q; want %q", c.Category, "writing")
	}
	if c.CanaryType != TypeAdversarial {
		t.Errorf("CanaryType = %q; want %q", c.CanaryType, TypeAdversarial)
	}
	if c.ExpectedPass {
		t.Error("ExpectedPass = true; want false")
	}
	if len(c.ExpectedCheckResults) != 1 || !c.ExpectedCheckResults["lint"] == false {
		// check the key exists
		if _, ok := c.ExpectedCheckResults["lint"]; !ok {
			t.Error("ExpectedCheckResults missing 'lint' key")
		}
	}
}

// TestStoreRoundTrip_PutAndGetCanary verifies that PutCanary followed by
// GetCanary returns the original canary.
func TestStoreRoundTrip_PutAndGetCanary(t *testing.T) {
	ms := newMemCanaryStore()
	c := NewCanaryTask("code", TypeKnownGood, true, nil, "sha256:ground-truth")
	c.ExpectedMinScore = 0.65
	c.ExpectedMaxScore = 1.0

	if err := ms.PutCanary(c); err != nil {
		t.Fatalf("PutCanary: %v", err)
	}

	got, err := ms.GetCanary(c.ID)
	if err != nil {
		t.Fatalf("GetCanary: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("ID: want %q, got %q", c.ID, got.ID)
	}
	if got.Category != c.Category {
		t.Errorf("Category: want %q, got %q", c.Category, got.Category)
	}
	if got.ExpectedMinScore != c.ExpectedMinScore {
		t.Errorf("ExpectedMinScore: want %v, got %v", c.ExpectedMinScore, got.ExpectedMinScore)
	}
	if got.GroundTruthHash != c.GroundTruthHash {
		t.Errorf("GroundTruthHash: want %q, got %q", c.GroundTruthHash, got.GroundTruthHash)
	}
}

// TestGetCanaryByTaskID_ReturnsCorrectCanary verifies that after PutCanary with
// a TaskID set, GetCanaryByTaskID returns the matching canary.
func TestGetCanaryByTaskID_ReturnsCorrectCanary(t *testing.T) {
	ms := newMemCanaryStore()
	c := NewCanaryTask("research", TypeEdgeCase, true, nil, "sha256:research-edge")
	c.TaskID = "task-canary-42"

	if err := ms.PutCanary(c); err != nil {
		t.Fatalf("PutCanary: %v", err)
	}

	got, err := ms.GetCanaryByTaskID("task-canary-42")
	if err != nil {
		t.Fatalf("GetCanaryByTaskID: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("ID: want %q, got %q", c.ID, got.ID)
	}
	if got.TaskID != "task-canary-42" {
		t.Errorf("TaskID: want %q, got %q", "task-canary-42", got.TaskID)
	}
}

// TestGetCanaryByTaskID_UnknownTaskID verifies that looking up a task ID that
// has no associated canary returns an error.
func TestGetCanaryByTaskID_UnknownTaskID(t *testing.T) {
	ms := newMemCanaryStore()
	_, err := ms.GetCanaryByTaskID("not-a-canary-task")
	if err == nil {
		t.Error("expected error for unknown task ID, got nil")
	}
}

// TestShouldInject_ReturnsFalseWhenDisabled verifies that ShouldInject always
// returns false when Enabled=false, regardless of InjectionRate.
func TestShouldInject_ReturnsFalseWhenDisabled(t *testing.T) {
	ms := newMemCanaryStore()
	cfg := InjectorConfig{
		Enabled:       false,
		InjectionRate: 1.0, // would always inject if enabled
	}
	inj := NewInjector(cfg, ms)
	for i := 0; i < 100; i++ {
		if inj.ShouldInject() {
			t.Errorf("ShouldInject returned true on call %d with Enabled=false", i)
		}
	}
}

// TestShouldInject_RespectsRate verifies that with InjectionRate=1.0 and
// Enabled=true, ShouldInject always returns true.
func TestShouldInject_RespectsRate(t *testing.T) {
	ms := newMemCanaryStore()
	cfg := InjectorConfig{
		Enabled:       true,
		InjectionRate: 1.0,
	}
	inj := NewInjector(cfg, ms)
	for i := 0; i < 10; i++ {
		if !inj.ShouldInject() {
			t.Errorf("ShouldInject returned false on call %d with InjectionRate=1.0", i)
		}
	}
}

// TestShouldInject_ZeroRateNeverInjects verifies that InjectionRate=0.0 with
// Enabled=true never injects.
func TestShouldInject_ZeroRateNeverInjects(t *testing.T) {
	ms := newMemCanaryStore()
	cfg := InjectorConfig{
		Enabled:       true,
		InjectionRate: 0.0,
	}
	inj := NewInjector(cfg, ms)
	for i := 0; i < 100; i++ {
		if inj.ShouldInject() {
			t.Errorf("ShouldInject returned true on call %d with InjectionRate=0.0", i)
		}
	}
}

// TestNextCanary_MatchesRequestedCategory verifies that NextCanary returns a
// canary whose Category matches the requested category.
func TestNextCanary_MatchesRequestedCategory(t *testing.T) {
	ms := newMemCanaryStore()
	cfg := InjectorConfig{Enabled: true, InjectionRate: 1.0}
	inj := NewInjector(cfg, ms)

	for _, cat := range []string{"code", "research", "writing"} {
		c := inj.NextCanary(cat)
		if c == nil {
			t.Fatalf("NextCanary(%q) returned nil", cat)
		}
		if c.Category != cat {
			t.Errorf("NextCanary(%q).Category = %q; want %q", cat, c.Category, cat)
		}
		if c.ID == "" {
			t.Errorf("NextCanary(%q).ID must not be empty", cat)
		}
		if c.Status != StatusActive {
			t.Errorf("NextCanary(%q).Status = %q; want %q", cat, c.Status, StatusActive)
		}
	}
}

// TestNextCanary_PersistsToStore verifies that NextCanary persists the created
// canary so GetCanary succeeds without any extra write.
func TestNextCanary_PersistsToStore(t *testing.T) {
	ms := newMemCanaryStore()
	cfg := InjectorConfig{Enabled: true, InjectionRate: 1.0}
	inj := NewInjector(cfg, ms)

	c := inj.NextCanary("code")
	got, err := ms.GetCanary(c.ID)
	if err != nil {
		t.Fatalf("GetCanary after NextCanary: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("persisted ID: want %q, got %q", c.ID, got.ID)
	}
}

// TestIsCanary_ReturnsTrueForInjectedCanaryTaskID verifies that after
// NextCanary + LinkTask, IsCanary returns true for the linked task ID.
func TestIsCanary_ReturnsTrueForInjectedCanaryTaskID(t *testing.T) {
	ms := newMemCanaryStore()
	cfg := InjectorConfig{Enabled: true, InjectionRate: 1.0}
	inj := NewInjector(cfg, ms)

	c := inj.NextCanary("code")

	// Simulate the caller creating a protocol task and linking it.
	const taskID = "task-injected-canary-1"
	if err := inj.LinkTask(c, taskID); err != nil {
		t.Fatalf("LinkTask: %v", err)
	}

	if !inj.IsCanary(taskID) {
		t.Errorf("IsCanary(%q) = false; want true after LinkTask", taskID)
	}
}

// TestIsCanary_ReturnsFalseForNonCanaryTaskID verifies that IsCanary returns
// false for a task ID that was never linked to a canary.
func TestIsCanary_ReturnsFalseForNonCanaryTaskID(t *testing.T) {
	ms := newMemCanaryStore()
	cfg := InjectorConfig{Enabled: true, InjectionRate: 1.0}
	inj := NewInjector(cfg, ms)

	if inj.IsCanary("task-ordinary-never-injected") {
		t.Error("IsCanary returned true for a task that was never linked to a canary")
	}
}

// TestDefaultCanaryCorpus_HasAtLeastTwentyCases verifies that the built-in
// corpus contains at least 20 canary templates covering all three categories.
func TestDefaultCanaryCorpus_HasAtLeastTwentyCases(t *testing.T) {
	corpus := DefaultCanaryCorpus()
	if len(corpus) < 20 {
		t.Errorf("DefaultCanaryCorpus has %d cases; want >= 20", len(corpus))
	}

	categories := map[string]int{}
	for _, c := range corpus {
		categories[c.Category]++
		if c.CanaryType == "" {
			t.Errorf("corpus entry has empty CanaryType: %+v", c)
		}
		if c.GroundTruthHash == "" {
			t.Errorf("corpus entry has empty GroundTruthHash: %+v", c)
		}
	}
	for _, cat := range []string{"code", "research", "writing"} {
		if categories[cat] == 0 {
			t.Errorf("DefaultCanaryCorpus has no entries for category %q", cat)
		}
	}
}
