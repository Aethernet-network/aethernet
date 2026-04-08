package verification_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

type stubAnalyzer struct {
	id     verification.AnalyzerID
	family verification.FamilyID
}

func (s *stubAnalyzer) ID() verification.AnalyzerID          { return s.id }
func (s *stubAnalyzer) Family() verification.FamilyID        { return s.family }
func (s *stubAnalyzer) Version() string                      { return "v1" }
func (s *stubAnalyzer) Calibration(_ string) bool            { return false }
func (s *stubAnalyzer) Analyze(_ context.Context, _ verification.AnalysisInput) (*verification.AnalysisOutput, error) {
	return &verification.AnalysisOutput{ScoreBP: 5000, Verdict: "pass"}, nil
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := verification.NewAnalyzerRegistry()
	f := verification.Family{ID: verification.FamilyLLMSemantic, Name: "LLM"}
	if err := r.RegisterFamily(f); err != nil {
		t.Fatalf("RegisterFamily: %v", err)
	}
	a := &stubAnalyzer{id: "llm_semantic/test:v1", family: verification.FamilyLLMSemantic}
	if err := r.RegisterAnalyzer(a); err != nil {
		t.Fatalf("RegisterAnalyzer: %v", err)
	}
	got, err := r.GetAnalyzer("llm_semantic/test:v1")
	if err != nil {
		t.Fatalf("GetAnalyzer: %v", err)
	}
	if got.ID() != a.ID() {
		t.Errorf("ID = %s; want %s", got.ID(), a.ID())
	}
}

func TestRegistry_RegisterAnalyzer_NoFamily(t *testing.T) {
	r := verification.NewAnalyzerRegistry()
	a := &stubAnalyzer{id: "unknown/test:v1", family: "unknown"}
	if err := r.RegisterAnalyzer(a); err == nil {
		t.Error("expected error for unregistered family")
	}
}

func TestRegistry_RegisterAnalyzer_Duplicate(t *testing.T) {
	r := verification.NewAnalyzerRegistry()
	_ = r.RegisterFamily(verification.Family{ID: verification.FamilyLLMSemantic})
	a := &stubAnalyzer{id: "llm_semantic/test:v1", family: verification.FamilyLLMSemantic}
	_ = r.RegisterAnalyzer(a)
	if err := r.RegisterAnalyzer(a); err == nil {
		t.Error("expected error for duplicate analyzer")
	}
}

func TestRegistry_ListFamilies_Defensive(t *testing.T) {
	r := verification.NewAnalyzerRegistry()
	_ = r.RegisterFamily(verification.Family{ID: verification.FamilyLLMSemantic})
	_ = r.RegisterFamily(verification.Family{ID: verification.FamilyDeterministicHeuristic})
	fams := r.ListFamilies()
	if len(fams) != 2 {
		t.Fatalf("ListFamilies = %d; want 2", len(fams))
	}
	// Mutating the returned slice should not affect the registry.
	fams[0] = verification.Family{ID: "mutated"}
	fams2 := r.ListFamilies()
	if fams2[0].ID == "mutated" {
		t.Error("ListFamilies returned non-defensive copy")
	}
}

func TestRegistry_ValidatorAnalyzers(t *testing.T) {
	r := verification.NewAnalyzerRegistry()
	_ = r.RegisterFamily(verification.Family{ID: verification.FamilyDeterministicHeuristic})
	_ = r.RegisterFamily(verification.Family{ID: verification.FamilyStatisticalStructural})
	a1 := &stubAnalyzer{id: "deterministic_heuristic/h:v1", family: verification.FamilyDeterministicHeuristic}
	a2 := &stubAnalyzer{id: "statistical_structural/s:v1", family: verification.FamilyStatisticalStructural}
	_ = r.RegisterAnalyzer(a1)
	_ = r.RegisterAnalyzer(a2)

	cfg := verification.ValidatorAnalyzerConfig{
		Families: []verification.ValidatorFamilyEntry{
			{Family: verification.FamilyDeterministicHeuristic, Analyzer: "deterministic_heuristic/h:v1"},
			{Family: verification.FamilyStatisticalStructural, Analyzer: "statistical_structural/s:v1"},
		},
	}
	analyzers, err := r.ValidatorAnalyzers(cfg)
	if err != nil {
		t.Fatalf("ValidatorAnalyzers: %v", err)
	}
	if len(analyzers) != 2 {
		t.Errorf("got %d analyzers; want 2", len(analyzers))
	}
}

func TestRegistry_ConcurrentRegistration(t *testing.T) {
	r := verification.NewAnalyzerRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fid := verification.FamilyID("family_" + string(rune('A'+i)))
			_ = r.RegisterFamily(verification.Family{ID: fid})
			_ = r.RegisterAnalyzer(&stubAnalyzer{
				id:     verification.AnalyzerID(string(fid) + "/a:v1"),
				family: fid,
			})
		}(i)
	}
	wg.Wait()
	if len(r.ListFamilies()) != 10 {
		t.Errorf("expected 10 families; got %d", len(r.ListFamilies()))
	}
}

func TestRegistry_BootstrapAllFour(t *testing.T) {
	r := verification.NewAnalyzerRegistry()
	families := []verification.Family{
		{ID: verification.FamilyDeterministicHeuristic, Name: "Heuristic"},
		{ID: verification.FamilyStatisticalStructural, Name: "Statistical"},
		{ID: verification.FamilyEmbeddingSimilarity, Name: "Embedding"},
		{ID: verification.FamilyLLMSemantic, Name: "LLM"},
	}
	for _, f := range families {
		if err := r.RegisterFamily(f); err != nil {
			t.Fatalf("RegisterFamily(%s): %v", f.ID, err)
		}
		_ = r.RegisterAnalyzer(&stubAnalyzer{
			id:     verification.AnalyzerID(string(f.ID) + "/default:v1"),
			family: f.ID,
		})
	}
	if len(r.ListFamilies()) != 4 {
		t.Errorf("ListFamilies = %d; want 4", len(r.ListFamilies()))
	}
	for _, f := range families {
		analyzers := r.ListAnalyzersByFamily(f.ID)
		if len(analyzers) != 1 {
			t.Errorf("ListAnalyzersByFamily(%s) = %d; want 1", f.ID, len(analyzers))
		}
	}
}
