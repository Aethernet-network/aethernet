package evidence

import (
	"strings"
	"testing"
)

func TestContentVerifier_SubstantiveArticlePasses(t *testing.T) {
	cv := &ContentVerifier{}
	ev := &Evidence{
		Summary: `The emergence of transformer architectures has fundamentally reshaped natural language processing.
According to research from Google Brain, the attention mechanism enables models to capture long-range
dependencies that recurrent networks struggle with. Specifically, the self-attention computation scales
quadratically with sequence length, which has motivated innovations like Flash Attention (Dao et al., 2022).

However, the scaling laws governing these models remain poorly understood. Kaplan et al. (2020) demonstrated
that model performance follows power-law relationships with parameter count, dataset size, and compute budget.
This finding has significant implications for infrastructure planning: training GPT-4 required an estimated
$100M in compute costs, while Llama 2 achieved competitive performance at roughly 10x lower cost.

Furthermore, the deployment landscape has shifted dramatically. Quantization techniques like GPTQ and AWQ
reduce memory footprint by 4-8x with minimal quality degradation. This means a 70B parameter model that
previously required 140GB of GPU memory can run on a single A100 80GB card.

In conclusion, the field is converging on a clear trajectory: larger models trained more efficiently,
deployed with aggressive optimization, and evaluated against increasingly specific benchmarks. The key
takeaway for practitioners is that model selection should be driven by deployment constraints, not raw
benchmark scores, as the performance gaps between top models have narrowed significantly in 2024.`,
		OutputPreview: "The emergence of transformer architectures...",
		OutputType:    "text",
		OutputSize:    5000,
	}

	score, passed := cv.Verify(ev, "AI Model Architecture Survey", "Survey the landscape of large language model architectures, training techniques, and deployment strategies. Include specific examples and quantitative comparisons.", 100000)
	if !passed {
		t.Fatalf("substantive article should pass: Overall=%.3f Quality=%.3f Relevance=%.3f Completeness=%.3f",
			score.Overall, score.Quality, score.Relevance, score.Completeness)
	}
	if score.Overall < 0.60 {
		t.Fatalf("Overall should be >= 0.60, got %.3f", score.Overall)
	}
}

func TestContentVerifier_VagueFillerScoresLow(t *testing.T) {
	cv := &ContentVerifier{}
	ev := &Evidence{
		Summary: `We are committed to delivering value through our innovative solutions. Our team leverages
best-in-class methodologies to drive meaningful outcomes. By focusing on synergistic approaches, we
ensure maximum impact across all stakeholders. Our comprehensive framework enables seamless integration
of cutting-edge technologies. We believe in the power of collaboration to unlock transformative results.
Moving forward, we will continue to optimize our processes for enhanced efficiency and effectiveness.`,
		OutputType: "text",
		OutputSize: 500,
	}

	score, _ := cv.Verify(ev, "Market Analysis Report", "Analyze the competitive landscape for cloud computing providers", 50000)
	if score.Quality >= 0.50 {
		t.Fatalf("vague corporate filler should score < 0.50 Quality, got %.3f", score.Quality)
	}
}

func TestContentVerifier_TechnicalCodePasses(t *testing.T) {
	cv := &ContentVerifier{}
	ev := &Evidence{
		Summary: `Implementation of a thread-safe LRU cache with O(1) amortized lookup and eviction.

The cache uses a doubly-linked list for recency tracking and a hash map for O(1) key lookup.
The implementation handles concurrent access via a read-write mutex, allowing multiple readers
but exclusive writers. Cache hits move the accessed node to the head of the list.

` + "```go\ntype LRUCache struct {\n\tcapacity int\n\titems    map[string]*node\n\thead     *node\n\ttail     *node\n\tmu       sync.RWMutex\n}\n\nfunc (c *LRUCache) Get(key string) (interface{}, bool) {\n\tc.mu.RLock()\n\tdefer c.mu.RUnlock()\n\tif n, ok := c.items[key]; ok {\n\t\tc.moveToHead(n)\n\t\treturn n.value, true\n\t}\n\treturn nil, false\n}\n```" + `

Performance benchmarks on an M2 MacBook Pro show:
- Get (cache hit): 45ns average
- Get (cache miss): 12ns average
- Put (no eviction): 89ns average
- Put (with eviction): 112ns average

The implementation passes all 47 test cases including concurrent access stress tests with 100 goroutines.`,
		OutputType: "code",
		OutputSize: 3000,
	}

	score, passed := cv.Verify(ev, "LRU Cache Implementation", "Implement a thread-safe LRU cache in Go with tests and benchmarks", 80000)
	if !passed {
		t.Fatalf("technical code content should pass: Overall=%.3f Quality=%.3f", score.Overall, score.Quality)
	}
}

func TestContentVerifier_LoremIpsumScoresVeryLow(t *testing.T) {
	cv := &ContentVerifier{}
	lorem := strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. ", 20)
	ev := &Evidence{
		Summary:    lorem,
		OutputType: "text",
		OutputSize: uint64(len(lorem)),
	}

	score, _ := cv.Verify(ev, "Research Report", "Analyze trends in renewable energy adoption", 50000)
	if score.Quality >= 0.45 {
		t.Fatalf("lorem ipsum should score < 0.45 Quality, got %.3f", score.Quality)
	}
}

func TestContentVerifier_CompetitiveAnalysisPasses(t *testing.T) {
	cv := &ContentVerifier{}
	ev := &Evidence{
		Summary: `## Cloud Computing Market Analysis 2024

### Market Overview

The global cloud computing market reached $591 billion in 2023, growing 19% year-over-year according to
Gartner's latest forecast. Amazon Web Services maintains market leadership with approximately 32% share,
followed by Microsoft Azure at 23% and Google Cloud Platform at 11%.

### Key Trends

**Multi-cloud adoption** has accelerated significantly. A 2024 Flexera survey found that 87% of enterprises
now use a multi-cloud strategy, up from 81% in 2023. This shift is driven by three factors:

1. Vendor lock-in mitigation — organizations want workload portability
2. Best-of-breed selection — different clouds excel at different services
3. Regulatory compliance — data sovereignty requirements mandate geographic distribution

**AI infrastructure spending** represents the fastest-growing segment. NVIDIA reported that cloud service
provider revenue grew 171% in Q4 2023, driven almost entirely by GPU demand for AI training and inference.
Microsoft's partnership with OpenAI has given Azure a competitive advantage in the AI developer ecosystem.

### Competitive Positioning

AWS continues to lead in breadth of services (200+ offerings) and enterprise adoption. However, Azure is
closing the gap through tight integration with the Microsoft 365 ecosystem and GitHub Copilot. Google Cloud
differentiates on data analytics (BigQuery) and Kubernetes (GKE), leveraging its origins as a search company.

### Outlook

The market is expected to reach $1 trillion by 2027. The primary growth vectors are AI workloads, edge
computing, and industry-specific cloud solutions. Companies that fail to modernize their cloud strategy
risk falling behind competitors by 2-3 years in AI capability deployment.`,
		OutputType: "text",
		OutputSize: 6000,
	}

	score, passed := cv.Verify(ev, "Cloud Market Analysis", "Analyze the competitive landscape for cloud computing providers including AWS, Azure, and GCP. Include market share data and key trends.", 100000)
	if !passed {
		t.Fatalf("competitive analysis should pass: Overall=%.3f Quality=%.3f Relevance=%.3f",
			score.Overall, score.Quality, score.Relevance)
	}
	if score.Overall < 0.60 {
		t.Fatalf("Overall should be >= 0.60, got %.3f", score.Overall)
	}
}

func TestContentVerifier_QualityProfileComputeWeights(t *testing.T) {
	// Substantive content should have quality > filler content by at least 0.15.
	substantive := analyzeContentQuality(`The Federal Reserve raised interest rates by 25 basis points in March 2024,
bringing the federal funds rate to 5.50%. According to Chair Jerome Powell, this decision reflects persistent
inflation above the 2% target. However, labor market data from the Bureau of Labor Statistics shows cooling:
unemployment rose to 3.9% from 3.7% in January. Furthermore, the yield curve inversion that began in July 2022
has deepened, historically a reliable predictor of recession within 12-18 months.`)

	filler := analyzeContentQuality(`We are pleased to share our latest insights on the important topic of economics.
Our team has been working hard to understand the key drivers of the economy. We believe that the economy is
an important part of our lives and we should all pay attention to it. Going forward, we will continue to
monitor the situation and provide updates as needed.`)

	gap := substantive.computeQuality() - filler.computeQuality()
	if gap < 0.10 {
		t.Fatalf("substance vs filler quality gap should be >= 0.10, got %.3f (substance=%.3f filler=%.3f)",
			gap, substantive.computeQuality(), filler.computeQuality())
	}
}

func TestContentVerifier_EmptyContentZeroQuality(t *testing.T) {
	profile := analyzeContentQuality("")
	if profile.computeQuality() != 0 {
		t.Fatalf("empty content should have zero quality, got %.3f", profile.computeQuality())
	}
}

func TestContentVerifier_RepetitiveParagraphsPenalized(t *testing.T) {
	// Same information restated in different words across paragraphs.
	content := `The market for cloud computing services is growing rapidly in recent years.

The cloud computing services market has experienced rapid growth in the recent period.

Cloud computing services have seen their market grow rapidly over the past few years.

The rapid growth of the cloud computing services market has been notable recently.`

	profile := analyzeContentQuality(content)
	if profile.computeQuality() >= 0.50 {
		t.Fatalf("repetitive paragraphs should score < 0.50 Quality, got %.3f", profile.computeQuality())
	}
}

func TestContentVerifier_LongArticleStillPasses(t *testing.T) {
	// Regression: the existing long article test from verifiers_test.go must still pass.
	cv := &ContentVerifier{}
	ev := &Evidence{
		Summary: strings.Repeat(`Quantum computing represents a paradigm shift in computational capability.
Unlike classical computers that process bits as 0 or 1, quantum computers use qubits that can exist
in superposition. IBM's Eagle processor achieved 127 qubits in 2023, while Google's Sycamore demonstrated
quantum supremacy on a specific sampling task. However, practical quantum advantage for real-world
problems remains elusive due to decoherence and error rates. Error correction schemes like the surface
code require thousands of physical qubits per logical qubit, pushing fault-tolerant quantum computing
to the 2030s according to most estimates.

`, 3),
		OutputType: "text",
		OutputSize: 8000,
	}

	score, passed := cv.Verify(ev, "Quantum Computing Survey", "Survey the state of quantum computing hardware, software, and applications", 100000)
	if !passed {
		t.Fatalf("long substantive article should pass: Overall=%.3f Quality=%.3f", score.Overall, score.Quality)
	}
}
