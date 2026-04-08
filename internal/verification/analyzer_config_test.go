package verification_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

func TestValidatorConfig_Validate_Empty(t *testing.T) {
	cfg := verification.ValidatorAnalyzerConfig{}
	if err := cfg.Validate(); err == nil {
		t.Error("empty config should fail validation")
	}
}

func TestValidatorConfig_Validate_Valid(t *testing.T) {
	cfg := verification.DefaultBootstrapConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default bootstrap config should be valid: %v", err)
	}
}

func TestValidatorConfig_Validate_DuplicateAnalyzer(t *testing.T) {
	cfg := verification.ValidatorAnalyzerConfig{
		Families: []verification.ValidatorFamilyEntry{
			{Family: verification.FamilyDeterministicHeuristic, Analyzer: "same:v1"},
			{Family: verification.FamilyStatisticalStructural, Analyzer: "same:v1"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("duplicate analyzer should fail validation")
	}
}

func TestValidatorConfig_LoadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-config.json")
	data := `{"families":[{"family":"deterministic_heuristic","analyzer":"deterministic_heuristic/heuristic:v1"}]}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := verification.LoadValidatorAnalyzerConfig(path)
	if err != nil {
		t.Fatalf("LoadValidatorAnalyzerConfig: %v", err)
	}
	if len(cfg.Families) != 1 {
		t.Fatalf("Families = %d; want 1", len(cfg.Families))
	}
	if cfg.Families[0].Family != verification.FamilyDeterministicHeuristic {
		t.Errorf("Family = %s; want %s", cfg.Families[0].Family, verification.FamilyDeterministicHeuristic)
	}
}
