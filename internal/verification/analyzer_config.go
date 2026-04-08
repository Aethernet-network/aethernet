package verification

import (
	"encoding/json"
	"fmt"
	"os"
)

// ValidatorAnalyzerConfig specifies which analyzer families and analyzers
// a validator runs. Loaded from a per-node JSON configuration file.
type ValidatorAnalyzerConfig struct {
	Families []ValidatorFamilyEntry `json:"families"`
}

// ValidatorFamilyEntry maps a family to a specific analyzer the validator runs.
type ValidatorFamilyEntry struct {
	Family   FamilyID   `json:"family"`
	Analyzer AnalyzerID `json:"analyzer"`
}

// LoadValidatorAnalyzerConfig reads a validator analyzer config from a JSON file.
func LoadValidatorAnalyzerConfig(path string) (ValidatorAnalyzerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ValidatorAnalyzerConfig{}, fmt.Errorf("verification: read config %q: %w", path, err)
	}
	var cfg ValidatorAnalyzerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ValidatorAnalyzerConfig{}, fmt.Errorf("verification: parse config %q: %w", path, err)
	}
	return cfg, nil
}

// Validate checks that the config is well-formed.
func (c ValidatorAnalyzerConfig) Validate() error {
	if len(c.Families) == 0 {
		return fmt.Errorf("verification: config must specify at least one family")
	}
	seen := make(map[AnalyzerID]bool)
	for _, entry := range c.Families {
		if entry.Family == "" {
			return fmt.Errorf("verification: family entry has empty family ID")
		}
		if entry.Analyzer == "" {
			return fmt.Errorf("verification: family entry has empty analyzer ID")
		}
		if seen[entry.Analyzer] {
			return fmt.Errorf("verification: duplicate analyzer %q", entry.Analyzer)
		}
		seen[entry.Analyzer] = true
	}
	return nil
}

// DefaultBootstrapConfig returns the default analyzer config for testnet
// bootstrap mode: deterministic_heuristic + statistical_structural.
// This config works without any external API keys.
func DefaultBootstrapConfig() ValidatorAnalyzerConfig {
	return ValidatorAnalyzerConfig{
		Families: []ValidatorFamilyEntry{
			{Family: FamilyDeterministicHeuristic, Analyzer: "deterministic_heuristic/heuristic:v1"},
			{Family: FamilyStatisticalStructural, Analyzer: "statistical_structural/statistical:v1"},
		},
	}
}
