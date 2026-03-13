package canary

import (
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// InjectorConfig controls canary injection behaviour.
type InjectorConfig struct {
	// Enabled must be true for any injection to occur. Default: false.
	// Canary injection is disabled by default so that test/devnet nodes do not
	// pollute production quality metrics.
	Enabled bool

	// InjectionRate is the fraction of tasks that should be canaries (0.0–1.0).
	// Default: 0.02 (2 %).
	InjectionRate float64

	// Categories is the list of task categories eligible for injection.
	// Default: ["code", "research", "writing"].
	Categories []string
}

// DefaultInjectorConfig returns sensible production defaults.
func DefaultInjectorConfig() InjectorConfig {
	return InjectorConfig{
		Enabled:       false,
		InjectionRate: 0.02,
		Categories:    []string{"code", "research", "writing"},
	}
}

// Injector selects canary templates from the corpus and injects them into the
// live task stream. It is safe for concurrent use.
type Injector struct {
	config InjectorConfig
	store  canaryStore
	corpus []CanaryTask
	rng    *rand.Rand
	mu     sync.Mutex
}

// NewInjector returns an Injector backed by store, loaded with the default
// corpus. store must not be nil.
func NewInjector(config InjectorConfig, store canaryStore) *Injector {
	return &Injector{
		config: config,
		store:  store,
		corpus: DefaultCanaryCorpus(),
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // non-crypto RNG is intentional
	}
}

// ShouldInject reports whether the next task should be replaced by a canary.
// Returns false when injection is disabled or the random roll exceeds InjectionRate.
func (inj *Injector) ShouldInject() bool {
	if !inj.config.Enabled {
		return false
	}
	inj.mu.Lock()
	defer inj.mu.Unlock()
	return inj.rng.Float64() < inj.config.InjectionRate
}

// NextCanary selects a corpus template matching category (falling back to the
// full corpus if no match exists), creates a new CanaryTask from it, persists
// the canary to the store, and returns it.
//
// The returned canary has no TaskID yet — the caller is responsible for creating
// the actual protocol task from the canary and then calling LinkTask to bind
// the generated task ID.
func (inj *Injector) NextCanary(category string) *CanaryTask {
	inj.mu.Lock()
	var matches []CanaryTask
	for _, t := range inj.corpus {
		if t.Category == category {
			matches = append(matches, t)
		}
	}
	if len(matches) == 0 {
		matches = inj.corpus // fall back to any category
	}
	template := matches[inj.rng.Intn(len(matches))]
	inj.mu.Unlock()

	c := NewCanaryTask(
		template.Category,
		template.CanaryType,
		template.ExpectedPass,
		template.ExpectedCheckResults,
		template.GroundTruthHash,
	)
	c.ExpectedMinScore = template.ExpectedMinScore
	c.ExpectedMaxScore = template.ExpectedMaxScore

	if err := inj.store.PutCanary(c); err != nil {
		slog.Error("canary: injector failed to persist canary",
			"id", c.ID, "category", category, "err", err)
	}
	return c
}

// LinkTask binds a protocol task ID to a previously created canary. It sets
// canary.TaskID, updates the persisted record, and writes the secondary
// taskID→canaryID index so that IsCanary(taskID) works.
func (inj *Injector) LinkTask(canary *CanaryTask, taskID string) error {
	canary.TaskID = taskID
	if err := inj.store.PutCanary(canary); err != nil {
		slog.Error("canary: link task failed",
			"canary_id", canary.ID, "task_id", taskID, "err", err)
		return err
	}
	return nil
}

// IsCanary reports whether taskID corresponds to an injected canary task.
func (inj *Injector) IsCanary(taskID string) bool {
	_, err := inj.store.GetCanaryByTaskID(taskID)
	return err == nil
}
