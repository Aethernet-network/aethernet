package families

import "github.com/Aethernet-network/aethernet/internal/verification"

// RegisterBootstrapAnalyzers creates an AnalyzerRegistry with all four
// bootstrap analyzer families and their default analyzers registered.
// This is the day-one configuration for the testnet.
func RegisterBootstrapAnalyzers() *verification.AnalyzerRegistry {
	r := verification.NewAnalyzerRegistry()

	// Register all four families.
	_ = r.RegisterFamily(HeuristicFamily())
	_ = r.RegisterFamily(StatisticalFamily())
	_ = r.RegisterFamily(EmbeddingFamily())
	_ = r.RegisterFamily(LLMSemanticFamily())

	// Register default analyzers for each family.
	// LLM analyzer is registered with nil client — it will error at Analyze()
	// time if invoked without an API key. This is by design: validators that
	// don't configure an LLM API key simply don't run the LLM family.
	_ = r.RegisterAnalyzer(NewHeuristicAnalyzer())
	_ = r.RegisterAnalyzer(NewStatisticalAnalyzer())
	_ = r.RegisterAnalyzer(NewEmbeddingAnalyzer())
	_ = r.RegisterAnalyzer(NewLLMSemanticAnalyzer("llm_semantic/claude_semantic:v1", "v1", nil))

	return r
}
