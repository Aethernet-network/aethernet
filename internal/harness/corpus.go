package harness

import "github.com/Aethernet-network/aethernet/internal/evidence"

// DefaultCorpus returns the built-in benchmark corpus of 20 labelled test
// cases spanning code, research, and writing categories.
//
// Ground truth (ExpectedPass) reflects human judgment of whether the
// submission genuinely answers the task — not what the current verifier will
// predict. Cases where the verifier disagrees with the ground truth surface
// calibration issues (false positives or false negatives).
func DefaultCorpus() []BenchmarkCase {
	return []BenchmarkCase{
		// ── Code category (8 cases) ─────────────────────────────────────────

		{
			ID:          "code-good-1",
			Title:       "Implement JWT authentication middleware",
			Description: "Write a Go HTTP middleware that validates Bearer JWT tokens and rejects unauthorised requests",
			Category:    "code",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
				OutputType: "code",
				Summary: `package middleware

import (
	"errors"
	"net/http"
	"strings"
)

// JWTConfig holds JWT authentication configuration.
type JWTConfig struct {
	SecretKey string
	Issuer    string
}

// Authenticate returns an HTTP middleware that validates JWT tokens.
func (c *JWTConfig) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "invalid authorization format", http.StatusUnauthorized)
			return
		}
		token, err := parseJWT(parts[1], c.SecretKey)
		if err != nil || !token.Valid {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}`,
				OutputSize: 820,
				Metrics:    map[string]string{"language": "go", "lines": "32"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.65,
			ExpectedMaxScore: 1.0,
			Tags:             []string{"known-good", "code"},
		},

		{
			ID:          "code-good-2",
			Title:       "Parse JSON configuration file",
			Description: "Write a Python function to parse and validate a JSON config file with required fields",
			Category:    "code",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3",
				OutputType: "code",
				Summary: `import json
import os

def parse_config(config_path: str) -> dict:
    """Parse and validate a JSON configuration file.

    Args:
        config_path: Path to the JSON config file.
    Returns:
        Parsed config dictionary with validated fields.
    """
    if not os.path.exists(config_path):
        raise FileNotFoundError(f"Config file not found: {config_path}")
    try:
        with open(config_path) as f:
            config = json.load(f)
    except json.JSONDecodeError as e:
        raise ValueError(f"Invalid JSON in config: {e}")
    required_fields = ["database", "port", "host"]
    for field in required_fields:
        if field not in config:
            raise KeyError(f"Required config field missing: {field}")
    return config`,
				OutputSize: 620,
				Metrics:    map[string]string{"language": "python", "lines": "22"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 0.90,
			Tags:             []string{"known-good", "code"},
		},

		{
			ID:          "code-bad-1",
			Title:       "Implement binary search algorithm",
			Description: "Write an efficient binary search algorithm in Go that returns the index of the target value",
			Category:    "code",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
				OutputType: "code",
				Summary:    "",
				OutputSize: 1, // non-zero required by corpus validity test
				Metrics:    map[string]string{"status": "empty"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.20,
			Tags:             []string{"known-bad", "code"},
		},

		{
			ID:          "code-bad-2",
			Title:       "Build distributed key-value store",
			Description: "Implement a distributed key-value store with consistent hashing and replication in Go",
			Category:    "code",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5",
				OutputType: "code",
				// Bubble sort implementation — completely wrong topic; no error handling,
				// no comments, no relevance to distributed systems.
				Summary: `func bubbleSort(arr []int) []int {
	n := len(arr)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if arr[j] > arr[j+1] {
				arr[j], arr[j+1] = arr[j+1], arr[j]
			}
		}
	}
	return arr
}`,
				OutputSize: 185,
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.30,
			ExpectedMaxScore: 0.65,
			Tags:             []string{"known-bad", "code"},
		},

		{
			ID:          "code-adv-1",
			Title:       "Implement gradient descent optimizer",
			Description: "Write a Python implementation of gradient descent for neural network training",
			Category:    "code",
			// Adversarial: valid Python linked-list code — correct syntax and completeness,
			// but zero overlap with machine-learning terms in the task.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6",
				OutputType: "code",
				Summary: `class ListNode:
    def __init__(self, val=0, next_node=None):
        self.val = val
        self.next = next_node

class LinkedList:
    def __init__(self):
        self.head = None

    def append(self, val):
        node = ListNode(val)
        if self.head is None:
            self.head = node
            return
        current = self.head
        while current.next is not None:
            current = current.next
        current.next = node

    def to_list(self):
        result = []
        current = self.head
        while current:
            result.append(current.val)
            current = current.next
        return result`,
				OutputSize: 520,
				Metrics:    map[string]string{"language": "python", "topic": "data-structure"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.20,
			ExpectedMaxScore: 0.65,
			Tags:             []string{"adversarial", "code"},
		},

		{
			ID:          "code-adv-2",
			Title:       "Parse CSV files",
			Description: "Write a function to parse CSV files and return structured data as a list of records",
			Category:    "code",
			// Adversarial: high word count, no real code — pure filler text.
			// Tests whether the verifier rejects plausible-looking non-code output.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1",
				OutputType: "text",
				Summary: `The solution has been implemented according to the specifications.
The implementation is ready for review and testing.
Please review the implementation at your earliest convenience.
The implementation handles all edge cases as required.
This placeholder demonstrates the solution approach.
The solution has been implemented according to the specifications.
The implementation is ready for review and testing.
Please review the implementation at your earliest convenience.
The implementation handles all edge cases as required.
This placeholder demonstrates the solution approach.`,
				OutputSize: 560,
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.05,
			ExpectedMaxScore: 0.35,
			Tags:             []string{"adversarial", "code"},
		},

		{
			ID:          "code-edge-1",
			Title:       "Validate email format",
			Description: "Check if an email address is valid and contains required format components",
			Category:    "code",
			// Edge case: very short but correctly solves the task.
			// A human expert would accept this. The verifier may penalise it for
			// low completeness (only 6 substantive lines) → potential false negative.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2e3d4c5b6a1f2",
				OutputType: "code",
				Summary: `// ValidateEmail checks if email contains required format components.
func ValidateEmail(email string) bool {
	if len(email) == 0 {
		return false
	}
	parts := strings.Split(email, "@")
	return len(parts) == 2 && len(parts[0]) > 0 && strings.Contains(parts[1], ".")
}`,
				OutputSize: 210,
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.30,
			ExpectedMaxScore: 0.80,
			Tags:             []string{"edge-case", "code"},
		},

		{
			ID:          "code-edge-2",
			Title:       "Process data records",
			Description: "Implement a function to process and transform data records from input to output format",
			Category:    "code",
			// Edge case: high quality Go code with error handling, tests, struct types,
			// but uses "handle" and "transform" rather than "process" in identifiers.
			// Tests whether keyword mismatch causes a false negative despite quality output.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3f4a5d6c7b2e3",
				OutputType: "code",
				Summary: `package transform

import "errors"

// Record holds a single input data entry.
type Record struct {
	ID    string
	Value string
}

// Output holds the transformed result.
type Output struct {
	ID      string
	Encoded string
}

// HandleRecords converts a slice of input Records to Output entries.
// Returns an error if any Record has an empty ID.
func HandleRecords(records []Record) ([]Output, error) {
	if len(records) == 0 {
		return nil, errors.New("input records must not be empty")
	}
	out := make([]Output, 0, len(records))
	for _, r := range records {
		if r.ID == "" {
			return nil, errors.New("record ID must not be empty")
		}
		out = append(out, Output{ID: r.ID, Encoded: "[" + r.Value + "]"})
	}
	return out, nil
}`,
				OutputSize: 620,
				Metrics:    map[string]string{"language": "go", "lines": "30"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.50,
			ExpectedMaxScore: 0.90,
			Tags:             []string{"edge-case", "code"},
		},

		// ── Research category (6 cases) ─────────────────────────────────────

		{
			ID:          "research-good-1",
			Title:       "Analyze blockchain scalability solutions",
			Description: "Research and analyze current blockchain scalability solutions including Layer 2, sharding, and sidechains",
			Category:    "research",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4e5a6b7d8c3f4",
				OutputType: "text",
				Summary: `## Blockchain Scalability: Analysis of Current Solutions

### Layer 2 Networks
Layer 2 solutions process transactions off-chain and settle proofs on the base layer.
The Lightning Network demonstrates 1,000,000 TPS theoretical capacity compared to
Bitcoin's 7 TPS baseline — a 140,000x improvement. Analysis indicates that 85% of
daily transactions are suitable for off-chain settlement.

### Sharding
Ethereum's danksharding proposal distributes state across 64 parallel shards.
Benchmarks show 100,000 TPS aggregate throughput with 12ms average finality.
Results demonstrate significant latency reduction compared to monolithic chains.

### Sidechains and Validiums
Polygon processes 65,000 TPS at $0.001 per transaction, a 99.8% cost reduction
versus Ethereum mainnet. Data shows 3.2 million daily active users as of Q4 2025.

### Finding
Optimistic rollups provide the best security-throughput tradeoff for general-purpose
dApps. ZK-rollups are preferred for payment applications where proof generation
cost is amortised over high-volume settlement.

Source: https://ethereum.org/en/developers/docs/scaling/
Reference: Buterin et al., "Endgame", 2021 [1]
Data point: 99.5% of Ethereum validators upgraded within 30 days of The Merge.`,
				OutputSize: 1180,
				Metrics:    map[string]string{"word_count": "196", "sources": "2"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.70,
			ExpectedMaxScore: 1.0,
			Tags:             []string{"known-good", "research"},
		},

		{
			ID:          "research-good-2",
			Title:       "Summarise quantum computing progress",
			Description: "Write a brief research summary on recent quantum computing achievements in 2025",
			Category:    "research",
			// Known good but brief. A human expert would accept this as a valid summary.
			// The DataVerifier may penalise it for insufficient analytical depth
			// → potential false negative worth surfacing.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5f6a7b8c9d4e5",
				OutputType: "text",
				Summary: `Quantum computing achieved several milestones in 2025. IBM's 1,000-qubit Condor
processor demonstrates 99.5% two-qubit gate fidelity, a significant improvement
over 2023 baselines. Google's Willow chip shows quantum advantage on random circuit
sampling benchmarks, completing in 5 minutes what classical supercomputers estimate
at 10 septillion years.

Key finding: error correction overhead now averages 1,000 physical qubits per
logical qubit, down 40% from 2024. This suggests fault-tolerant quantum computing
is achievable within 5 years at current improvement rates.

Source: https://research.ibm.com/quantum-computing/condor`,
				OutputSize: 630,
				Metrics:    map[string]string{"word_count": "98", "sources": "1"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.40,
			ExpectedMaxScore: 0.85,
			Tags:             []string{"known-good", "research"},
		},

		{
			ID:          "research-bad-1",
			Title:       "Research AI ethics considerations",
			Description: "Investigate and summarise the main ethical concerns surrounding large language model deployment",
			Category:    "research",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6a7b8c9d0e5f6",
				OutputType: "text",
				Summary:    "",
				OutputSize: 1,
				Metrics:    map[string]string{"status": "empty"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.10,
			Tags:             []string{"known-bad", "research"},
		},

		{
			ID:          "research-bad-2",
			Title:       "Analyse cloud computing market trends",
			Description: "Research current cloud computing adoption trends and market share among major providers",
			Category:    "research",
			// Adversarial false-positive trap: well-structured analysis with data,
			// quantitative terms, and citations — but on medieval European history,
			// completely off-topic. The DataVerifier does NOT check topic relevance,
			// so it is likely to PASS this. Expected human verdict: FAIL.
			// The harness should surface this as a false positive.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7b8c9d0e1f6a7",
				OutputType: "text",
				Summary: `## Analysis of Medieval European Trade Networks (1200–1400 CE)

### Trade Volume and Routes
The Hanseatic League controlled 60% of Baltic trade from 1241 to 1669. Analysis
indicates that annual wool exports from England reached 30,000 sacks in 1304,
generating approximately £60,000 revenue. Data shows 40% average annual growth
in Venetian spice trade from 1250 to 1350.

### Key Finding
Results demonstrate that the Black Death of 1347 reduced European population by
an estimated 30–60%, causing a 45% decline in trade volume over the subsequent
decade. However, survivors' increased wages led to a notable productivity rebound.

### Comparative Analysis
Relative to Byzantine trade networks, Western European routes showed 25% lower
merchant mortality rates, suggesting superior route security. Average journey time
from Venice to Constantinople was 45 days, compared to 18 days by 1500.

Source: https://archives.medieval-history.org/hanseatic-trade-data
Reference: Lopez, R.S., "The Commercial Revolution of the Middle Ages" [1]
Data: 74% of English wool exports went through Calais between 1363 and 1400.`,
				OutputSize: 1080,
				Metrics:    map[string]string{"word_count": "172", "topic": "medieval-history"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 1.0,
			Tags:             []string{"known-bad", "research"},
		},

		{
			ID:          "research-adv-1",
			Title:       "Research GPU memory bandwidth trends",
			Description: "Analyse GPU memory bandwidth improvements across hardware generations and their impact on ML training",
			Category:    "research",
			// Adversarial: plausible-looking research content, analytical vocabulary,
			// even some quantitative terms — but the numbers are fabricated and the
			// analysis is superficial filler. A human expert would reject this.
			// The DataVerifier may accept it based on surface signals.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8c9d0e1f2a7b8",
				OutputType: "text",
				Summary: `## GPU Memory Bandwidth Analysis

Analysis indicates that GPU performance has been increasing significantly.
The results demonstrate notable improvements across hardware generations.
Findings suggest that memory bandwidth is an important factor in ML workloads.

### Key Metrics
Performance numbers show a 100% improvement every generation. This significant
trend demonstrates that hardware continues to improve. The analysis reveals that
faster memory bandwidth correlates with better training throughput.

### Conclusion
Overall findings indicate that GPU memory bandwidth trends show notable improvement.
The data suggests continued advancement is likely. Results demonstrate this pattern
is consistent across multiple generations of hardware.

Source: https://gpu-performance-data.example.com/bandwidth
Reference: Smith et al. [1]`,
				OutputSize: 820,
				Metrics:    map[string]string{"word_count": "130", "quality": "low"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.35,
			ExpectedMaxScore: 0.80,
			Tags:             []string{"adversarial", "research"},
		},

		{
			ID:          "research-edge-1",
			Title:       "Calculate server response time statistics",
			Description: "Analyse server response time data and compute median, p95, and p99 latency statistics",
			Category:    "research",
			// Edge case: short but perfectly targeted. Contains quantitative data,
			// analytical vocabulary, and structured output.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9d0e1f2a3b8c9",
				OutputType: "text",
				Summary: `## Server Response Time Statistics — Production API (2025-03-01 to 2025-03-11)

| Percentile | Latency  |
|------------|----------|
| p50 median | 42ms     |
| p75        | 78ms     |
| p95        | 145ms    |
| p99        | 312ms    |
| p99.9      | 891ms    |

Analysis reveals that 95% of requests complete within 145ms, meeting the 200ms SLA.
The p99 result of 312ms indicates outliers driven by database query spikes.
Recommendation: index the user_sessions table to reduce tail latency by an estimated 40%.

Data: 2.3 million requests sampled over 10 days; 0.003% error rate.`,
				OutputSize: 580,
				Metrics:    map[string]string{"word_count": "94", "data_points": "5"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.55,
			ExpectedMaxScore: 0.95,
			Tags:             []string{"edge-case", "research"},
		},

		// ── Writing category (6 cases) ──────────────────────────────────────

		{
			ID:          "writing-good-1",
			Title:       "Write blog post about remote work benefits",
			Description: "Create a 300-word blog post covering the top benefits of remote work for software engineering teams",
			Category:    "writing",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0e1f2a3b4c9d0",
				OutputType: "text",
				Summary: `## The Case for Remote Work in Software Engineering Teams

Remote work has fundamentally changed how software engineering teams operate. The
benefits extend far beyond saved commute time: research consistently shows higher
productivity, better talent retention, and measurable improvements in code quality.

### Productivity and Deep Work

Software engineers require sustained focus to write clean code and debug complex
systems. In remote environments, developers control their own work hours and
eliminate the interruptions endemic to open-plan offices. Studies indicate that
remote engineers spend 35% more time in deep work states compared to office peers.

### Talent Access and Retention

Remote hiring removes geographical constraints, allowing teams to recruit from a
global talent pool. Companies report 25% lower attrition rates among remote
engineers. When combined with flexible hours, remote policies demonstrate a notable
correlation with improved developer satisfaction scores.

### Collaboration Tools and Async Communication

Modern tooling — pull request reviews, async video updates, and shared documentation
— supports distributed teamwork without sacrificing communication quality. Teams
that invest in written communication culture consistently outperform those relying
solely on synchronous meetings.

The evidence suggests remote work is not a temporary accommodation but a sustainable
model for high-performing engineering organisations. Teams that embrace it thoughtfully
see lasting improvements in productivity, hiring reach, and employee retention.`,
				OutputSize: 1280,
				Metrics:    map[string]string{"word_count": "202", "paragraphs": "5"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.60,
			ExpectedMaxScore: 1.0,
			Tags:             []string{"known-good", "writing"},
		},

		{
			ID:          "writing-good-2",
			Title:       "Explain machine learning concepts",
			Description: "Write a clear explanation of supervised and unsupervised machine learning for a technical audience",
			Category:    "writing",
			// Good but draft quality. Shorter than ideal. Should still pass at 0.50 threshold.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1f2a3b4c5d0e1",
				OutputType: "text",
				Summary: `Machine learning divides into two main paradigms based on whether labelled training data is available.

Supervised learning trains models on input-output pairs. The algorithm learns a mapping function
from inputs to outputs, which it applies to unseen examples. Classification and regression are
the primary supervised tasks. Common algorithms include decision trees, logistic regression,
and neural networks.

Unsupervised learning finds structure in unlabelled data. Clustering algorithms group similar
examples; dimensionality reduction techniques compress representations. These techniques are
valuable for exploratory analysis and feature engineering before supervised training.

In practice, most production machine learning systems combine both approaches. Pre-training
large models unsupervised, then fine-tuning with supervised labels, is now the dominant
paradigm for language and vision tasks.`,
				OutputSize: 880,
				Metrics:    map[string]string{"word_count": "142", "paragraphs": "4"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.45,
			ExpectedMaxScore: 0.85,
			Tags:             []string{"known-good", "writing"},
		},

		{
			ID:          "writing-bad-1",
			Title:       "Write product description for noise-canceling headphones",
			Description: "Create a compelling product description for premium over-ear noise-canceling headphones",
			Category:    "writing",
			Evidence: &evidence.Evidence{
				Hash:       "sha256:e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2a3b4c5d6e1f2",
				OutputType: "text",
				Summary:    "",
				OutputSize: 1,
				Metrics:    map[string]string{"status": "empty"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.0,
			ExpectedMaxScore: 0.15,
			Tags:             []string{"known-bad", "writing"},
		},

		{
			ID:          "writing-bad-2",
			Title:       "Write product description for coffee maker",
			Description: "Create a concise product description for a home drip coffee maker with programmable timer",
			Category:    "writing",
			// Wrong topic entirely: an article about DevOps pipelines with no coffee-related terms.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3b4c5d6e7f2a3",
				OutputType: "text",
				Summary: `Continuous integration and deployment pipelines have transformed software delivery.
Modern DevOps practices enable teams to ship code multiple times per day with high
confidence. Automated testing, staging environments, and blue-green deployments
reduce production incidents and accelerate iteration cycles.

Infrastructure as code tools like Terraform and Pulumi allow teams to version their
infrastructure alongside application code. This practice improves reproducibility
and dramatically reduces configuration drift across environments.`,
				OutputSize: 560,
				Metrics:    map[string]string{"word_count": "88", "topic": "devops"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.05,
			ExpectedMaxScore: 0.50,
			Tags:             []string{"known-bad", "writing"},
		},

		{
			ID:          "writing-adv-1",
			Title:       "Write product description for wireless earbuds",
			Description: "Create a compelling product description for premium wireless earbuds with active noise cancellation",
			Category:    "writing",
			// Adversarial: generic boilerplate that uses no earbuds-specific language.
			// Repetitive structure and lacks specific product details.
			// A human expert would reject this for failing to describe the actual product.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4c5d6e7f8a3b4",
				OutputType: "text",
				Summary: `Introducing our premium product. This high-quality item will exceed your expectations
and deliver exceptional value. Our product is designed with the customer in mind.

Experience the difference with our outstanding offering. This premium solution provides
everything you need. Our product stands out from the competition in every way.

Upgrade your life with our exceptional product today. Customers love our premium quality.
This outstanding item delivers exactly what you are looking for. Order now and discover
the premium difference for yourself. Our product is the best choice available.`,
				OutputSize: 620,
				Metrics:    map[string]string{"word_count": "100", "quality": "boilerplate"},
			},
			ExpectedPass:     false,
			ExpectedMinScore: 0.15,
			ExpectedMaxScore: 0.60,
			Tags:             []string{"adversarial", "writing"},
		},

		{
			ID:          "writing-edge-1",
			Title:       "Write technical documentation for REST API",
			Description: "Create documentation explaining the authentication flow for a REST API",
			Category:    "writing",
			// Edge case: valid and useful API documentation but uses an unusual format —
			// numbered steps instead of markdown headers. A human expert would accept this.
			// Tests whether unusual formatting causes a false negative.
			Evidence: &evidence.Evidence{
				Hash:       "sha256:b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5d6e7f8a9b4c5",
				OutputType: "text",
				Summary: `REST API Authentication Flow

1. Register your application at the developer portal to receive a client_id and
   client_secret. Keep the client_secret secure and never expose it in client-side code.

2. Request an access token by sending a POST to /v1/auth/token with your credentials.
   The server validates the credentials and returns a JWT bearer token with a 3600-second
   expiry alongside a refresh token valid for 30 days.

3. Include the access token in subsequent API requests using the Authorization header:
   Authorization: Bearer <access_token>

4. When the access token expires, obtain a new one using the refresh token endpoint
   POST /v1/auth/refresh. This avoids requiring the user to re-authenticate.

5. Revoke tokens explicitly on logout by calling DELETE /v1/auth/token with the token.
   This ensures compromised tokens cannot be used after the session ends.

Errors: 401 Unauthorized is returned for missing or invalid tokens. 403 Forbidden
indicates the token is valid but lacks permission for the requested resource.`,
				OutputSize: 1080,
				Metrics:    map[string]string{"word_count": "178", "format": "numbered-steps"},
			},
			ExpectedPass:     true,
			ExpectedMinScore: 0.40,
			ExpectedMaxScore: 0.85,
			Tags:             []string{"edge-case", "writing"},
		},
	}
}
