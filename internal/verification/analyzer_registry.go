package verification

import (
	"fmt"
	"sort"
	"sync"
)

// AnalyzerRegistry holds all registered analyzer families and analyzers.
// Thread-safe for concurrent use.
type AnalyzerRegistry struct {
	mu        sync.RWMutex
	families  map[FamilyID]Family
	analyzers map[AnalyzerID]Analyzer
}

// NewAnalyzerRegistry creates an empty registry.
func NewAnalyzerRegistry() *AnalyzerRegistry {
	return &AnalyzerRegistry{
		families:  make(map[FamilyID]Family),
		analyzers: make(map[AnalyzerID]Analyzer),
	}
}

// RegisterFamily adds a family to the registry. Returns an error if the
// family ID is already registered.
func (r *AnalyzerRegistry) RegisterFamily(f Family) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.families[f.ID]; exists {
		return fmt.Errorf("verification: family %q already registered", f.ID)
	}
	r.families[f.ID] = f
	return nil
}

// RegisterAnalyzer adds an analyzer to the registry. The analyzer's family
// must already be registered. Returns an error if the analyzer ID is
// already registered or if the family is unknown.
func (r *AnalyzerRegistry) RegisterAnalyzer(a Analyzer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.families[a.Family()]; !exists {
		return fmt.Errorf("verification: family %q not registered (register family before analyzer)", a.Family())
	}
	if _, exists := r.analyzers[a.ID()]; exists {
		return fmt.Errorf("verification: analyzer %q already registered", a.ID())
	}
	r.analyzers[a.ID()] = a
	return nil
}

// GetAnalyzer returns the analyzer with the given ID, or an error if not found.
func (r *AnalyzerRegistry) GetAnalyzer(id AnalyzerID) (Analyzer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.analyzers[id]
	if !ok {
		return nil, fmt.Errorf("verification: analyzer %q not found", id)
	}
	return a, nil
}

// ListFamilies returns all registered families in sorted order.
// The returned slice is a defensive copy.
func (r *AnalyzerRegistry) ListFamilies() []Family {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Family, 0, len(r.families))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, f := range r.families {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ListAnalyzersByFamily returns all analyzers in a family. Empty if family
// is unknown or has no analyzers.
func (r *AnalyzerRegistry) ListAnalyzersByFamily(id FamilyID) []Analyzer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Analyzer
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for _, a := range r.analyzers {
		if a.Family() == id {
			out = append(out, a)
		}
	}
	return out
}

// ValidatorAnalyzers resolves the analyzers a validator should run based
// on its configuration. Returns an error if any configured analyzer is
// not in the registry.
func (r *AnalyzerRegistry) ValidatorAnalyzers(cfg ValidatorAnalyzerConfig) ([]Analyzer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Analyzer, 0, len(cfg.Families))
	for _, entry := range cfg.Families {
		a, ok := r.analyzers[entry.Analyzer]
		if !ok {
			return nil, fmt.Errorf("verification: configured analyzer %q not found in registry", entry.Analyzer)
		}
		if a.Family() != entry.Family {
			return nil, fmt.Errorf("verification: analyzer %q belongs to family %q, not %q", entry.Analyzer, a.Family(), entry.Family)
		}
		out = append(out, a)
	}
	return out, nil
}
