package evidence_test

import (
	"strings"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeEvidence(summary, preview string) *evidence.Evidence {
	return &evidence.Evidence{
		Summary:       summary,
		OutputPreview: preview,
		OutputType:    "text",
		OutputSize:    uint64(len(summary) + len(preview)),
	}
}

// ---------------------------------------------------------------------------
// CodeVerifier tests
// ---------------------------------------------------------------------------

func TestCodeVerifier_ValidGoCodePasses(t *testing.T) {
	code := `package main

import "fmt"

// Hello prints a greeting.
func Hello(name string) string {
	if name == "" {
		return "Hello, World!"
	}
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	fmt.Println(Hello("AetherNet"))
}`
	ev := makeEvidence(code, "")
	cv := &evidence.CodeVerifier{}
	score, passed := cv.Verify(ev, "Write a Go greeting function", "implement Hello(name string) string", 100_000)
	if !passed {
		t.Errorf("expected valid Go code to pass, overall=%.2f", score.Overall)
	}
	if score.Quality < 0.4 {
		t.Errorf("expected quality >= 0.4 for commented, error-handling code, got %.2f", score.Quality)
	}
}

func TestCodeVerifier_RandomTextFails(t *testing.T) {
	ev := makeEvidence("lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor", "")
	cv := &evidence.CodeVerifier{}
	score, passed := cv.Verify(ev, "Write sorting code", "implement quicksort algorithm", 50_000)
	if passed {
		t.Errorf("expected random prose to fail code verification, overall=%.2f", score.Overall)
	}
}

func TestCodeVerifier_EmptyFails(t *testing.T) {
	ev := makeEvidence("", "")
	cv := &evidence.CodeVerifier{}
	score, passed := cv.Verify(ev, "Write code", "any code", 10_000)
	if passed {
		t.Errorf("empty content should not pass, overall=%.2f", score.Overall)
	}
	if score.Overall != 0.0 {
		t.Errorf("expected overall=0.0 for empty, got %.2f", score.Overall)
	}
}

func TestCodeVerifier_PythonCodePasses(t *testing.T) {
	python := `import json
from typing import List

def process_data(items: List[str]) -> dict:
	"""Process a list of items and return summary statistics."""
	result = {}
	for item in items:
		result[item] = len(item)
	return result

if __name__ == "__main__":
	data = process_data(["hello", "world"])
	print(json.dumps(data))`
	ev := makeEvidence(python, "")
	cv := &evidence.CodeVerifier{}
	score, passed := cv.Verify(ev, "Process data", "write a Python function to process string lists", 80_000)
	if !passed {
		t.Errorf("expected Python code to pass, overall=%.2f", score.Overall)
	}
}

func TestCodeVerifier_ErrorHandlingBoostsQuality(t *testing.T) {
	code := `package server

import "errors"

// ErrNotFound is returned when the resource is missing.
var ErrNotFound = errors.New("not found")

func GetUser(id string) (*User, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	return fetchUser(id)
}

type User struct {
	ID   string
	Name string
}`
	ev := makeEvidence(code, "")
	cv := &evidence.CodeVerifier{}
	score, passed := cv.Verify(ev, "Implement user retrieval", "GetUser with error handling", 200_000)
	if !passed {
		t.Errorf("expected code with error handling to pass, overall=%.2f", score.Overall)
	}
	if score.Quality < 0.5 {
		t.Errorf("error handling + comments should give quality >= 0.5, got %.2f", score.Quality)
	}
}

// ---------------------------------------------------------------------------
// DataVerifier tests
// ---------------------------------------------------------------------------

func TestDataVerifier_StructuredJSONPasses(t *testing.T) {
	jsonData := `{
	"analysis": "Revenue trends Q1-Q4 2024",
	"summary": "The data indicates a 23% increase in ARR compared to Q1. Average monthly growth rate was 5.2%, significantly above the 3% baseline.",
	"findings": [
		{"metric": "ARR", "value": "4.2M", "change": "+23%"},
		{"metric": "MRR", "value": "350K", "change": "+18%"}
	],
	"conclusion": "Revenue growth is accelerating. Recommend expanding to the enterprise segment.",
	"sources": ["https://dashboard.example.com/q4", "[1] Annual Report 2024"]
}`
	ev := makeEvidence(jsonData, "")
	dv := &evidence.DataVerifier{}
	score, passed := dv.Verify(ev, "Revenue analysis", "analyze Q4 revenue trends and provide recommendations", 500_000)
	if !passed {
		t.Errorf("expected structured JSON with analysis to pass, overall=%.2f", score.Overall)
	}
}

func TestDataVerifier_EmptyFails(t *testing.T) {
	ev := makeEvidence("", "")
	dv := &evidence.DataVerifier{}
	score, passed := dv.Verify(ev, "Data analysis", "analyze user data", 100_000)
	if passed {
		t.Errorf("empty content should not pass, overall=%.2f", score.Overall)
	}
}

func TestDataVerifier_NumbersAndConclusionsHighScore(t *testing.T) {
	content := `Market Research Summary

The analysis indicates strong growth in the AI agent marketplace. Key findings:

| Metric           | Value    | Change  |
|------------------|----------|---------|
| Total agents     | 12,450   | +34%    |
| Avg tasks/agent  | 8.2      | +12%    |
| Settlement rate  | 94.3%    | +2.1%   |

The data shows that agents with reputation scores above 70 complete 45% more tasks
than those below 50. This correlates with the 23% increase in average task budget
over the same period ($1,200 vs $975).

Conclusion: High-reputation agents command premium pricing and higher completion rates.
Recommendation: Invest in reputation-building activities early in agent lifecycle.

Source: https://testnet.aethernet.network/v1/economics
Reference: [1] Q4 2024 network statistics`

	ev := makeEvidence(content, "")
	dv := &evidence.DataVerifier{}
	score, passed := dv.Verify(ev, "Market analysis", "research agent marketplace trends", 1_000_000)
	if !passed {
		t.Errorf("expected analysis with numbers and conclusions to pass, overall=%.2f", score.Overall)
	}
	if score.Relevance < 0.5 {
		t.Errorf("analytical depth (Relevance) should be >= 0.5, got %.2f", score.Relevance)
	}
}

func TestDataVerifier_RawDumpLowScore(t *testing.T) {
	// Raw data with no analysis gets a lower score.
	raw := "1 2 3 4 5"
	ev := makeEvidence(raw, "")
	dv := &evidence.DataVerifier{}
	score, _ := dv.Verify(ev, "Analyse sales data", "provide sales analysis", 500_000)
	if score.Overall > 0.5 {
		t.Errorf("expected raw dump to score low, got %.2f", score.Overall)
	}
}

// ---------------------------------------------------------------------------
// ContentVerifier tests
// ---------------------------------------------------------------------------

func TestContentVerifier_LongArticlePasses(t *testing.T) {
	// Build a 500+ word article with headings and paragraphs.
	article := `# Introduction to AetherNet

AetherNet is a decentralised trust and settlement protocol designed for AI agent commerce.
Unlike traditional blockchain systems, AetherNet uses a causal directed acyclic graph (DAG)
where each event directly references the prior events it has validated.

## How Agents Register

Agents register using cryptographic identity backed by Ed25519 key pairs. The registration
process creates a capability fingerprint that tracks the agent's task history, reputation score,
and specialisation category. This fingerprint is portable across applications and forms the
basis for trust-based task matching.

## Task Lifecycle

When a task poster needs work done, they submit a task with an escrowed budget. The escrow
locks the funds in a dedicated bucket, preventing double-spending while the task is in progress.
A claimer browses the discovery index to find matching tasks, then claims and completes the work.

## Settlement and Evidence

Upon submission, the autovalidator assesses the evidence deterministically. Code tasks are
parsed with the Go AST parser. Data tasks are evaluated for analytical depth and citation quality.
Content tasks are scored on language quality, topic relevance, and formatting. Tasks that meet
the quality threshold are settled automatically; borderline cases are held for manual review.

## Reputation System

Every task completion updates the agent's category-specific reputation score. The formula
combines completion rate with a volume weight that saturates at 100 tasks, preventing Sybil
attacks where reputation is farmed via trivial tasks. High reputation unlocks higher trust limits
and premium pricing in the discovery index.

## Conclusion

AetherNet provides a complete infrastructure layer for autonomous AI agent economies. The
deterministic evidence verification system ensures that payments are released only for genuine
work, building a trustworthy marketplace without requiring human oversight on every transaction.`

	ev := makeEvidence(article, "")
	cv := &evidence.ContentVerifier{}
	score, passed := cv.Verify(ev, "Write AetherNet overview", "500-word article about AetherNet protocol", 300_000)
	if !passed {
		t.Errorf("expected 500-word article to pass, overall=%.2f", score.Overall)
	}
	if score.Completeness < 0.5 {
		t.Errorf("completeness should be >= 0.5 for 500+ word article, got %.2f", score.Completeness)
	}
}

func TestContentVerifier_TenWordsFails(t *testing.T) {
	ev := makeEvidence("This is a very short answer.", "")
	cv := &evidence.ContentVerifier{}
	score, passed := cv.Verify(ev, "Write documentation", "write detailed documentation", 200_000)
	if passed {
		t.Errorf("expected very short content to fail, overall=%.2f", score.Overall)
	}
}

func TestContentVerifier_HeadingsBoostFormatting(t *testing.T) {
	content := `# Overview

This document describes the AetherNet settlement protocol architecture.

## Components

The system comprises three main layers: identity, settlement, and application.

## Integration

Agents integrate via the Python SDK or direct HTTP API calls.`

	ev := makeEvidence(content, "")
	cv := &evidence.ContentVerifier{}
	score, _ := cv.Verify(ev, "Technical documentation", "document AetherNet components", 100_000)
	// Quality includes formatting — headings should push it up.
	if score.Quality < 0.3 {
		t.Errorf("expected headings to improve quality score, got %.2f", score.Quality)
	}
}

func TestContentVerifier_TopicRelevanceMismatch(t *testing.T) {
	// Content about cooking — should score low on relevance for a blockchain task.
	offTopic := strings.Repeat("Boil the pasta until al dente. Add marinara sauce and stir gently. "+
		"Season with salt and pepper to taste. Serve immediately with parmesan cheese. ", 20)
	ev := makeEvidence(offTopic, "")
	cv := &evidence.ContentVerifier{}
	score, _ := cv.Verify(ev, "AetherNet blockchain documentation", "explain DAG settlement protocol cryptography", 150_000)
	if score.Relevance > 0.4 {
		t.Errorf("off-topic content should have low relevance, got %.2f", score.Relevance)
	}
}

// ---------------------------------------------------------------------------
// VerifierRegistry tests
// ---------------------------------------------------------------------------

func TestRegistry_CodeCategoryRoutesToCodeVerifier(t *testing.T) {
	reg := evidence.NewVerifierRegistry()
	// Code includes a comment and error handling to comfortably exceed the 0.5 threshold.
	code := `package main

import (
	"errors"
	"fmt"
)

// Add returns the sum of two integers.
func Add(a, b int) (int, error) {
	if a < 0 || b < 0 {
		return 0, errors.New("negative input")
	}
	return a + b, nil
}

func main() {
	result, err := Add(1, 2)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(result)
}`
	ev := makeEvidence(code, "")
	score, passed := reg.Verify(ev, "Add integers", "write an Add function with error handling", 50_000, "code")
	if !passed {
		t.Errorf("code category: expected valid Go to pass, overall=%.2f", score.Overall)
	}
}

func TestRegistry_ResearchCategoryRoutesToDataVerifier(t *testing.T) {
	reg := evidence.NewVerifierRegistry()
	content := `Research Summary

Analysis indicates that AetherNet agents average 8.2 tasks per week with a 94% settlement rate.
The distribution shows a 23% increase in high-value tasks ($1000+) versus the prior period.

Key finding: agents with 50+ completed tasks earn 45% more per task than new agents.

Conclusion: reputation compounds value. Source: https://testnet.aethernet.network/v1/economics`
	ev := makeEvidence(content, "")
	score, passed := reg.Verify(ev, "Agent research", "research AetherNet agent performance", 500_000, "research")
	if !passed {
		t.Errorf("research category: expected analytical content to pass, overall=%.2f", score.Overall)
	}
}

func TestRegistry_WritingCategoryRoutesToContentVerifier(t *testing.T) {
	reg := evidence.NewVerifierRegistry()
	article := strings.Repeat(
		"AetherNet enables AI agents to transact autonomously in a trustless marketplace. "+
			"The protocol uses cryptographic identity and reputation-weighted consensus to ensure fairness. ",
		15,
	)
	ev := makeEvidence(article, "")
	score, passed := reg.Verify(ev, "Write about AetherNet", "500-word article about AetherNet agent marketplace", 200_000, "writing")
	if !passed {
		t.Errorf("writing category: expected article to pass, overall=%.2f", score.Overall)
	}
}

func TestRegistry_UnknownCategoryFallsBackToKeywordVerifier(t *testing.T) {
	reg := evidence.NewVerifierRegistry()
	// The keyword verifier is lenient — any non-empty content with a few matches passes.
	ev := makeEvidence(
		"The AetherNet protocol implements transfer settlement via optimistic capability settlement "+
			"ensuring agents can transact with escrow-backed trust. The task was completed successfully "+
			"with all required outputs verified and documented in the submission evidence.",
		"",
	)
	score, _ := reg.Verify(ev, "AetherNet task", "complete an AetherNet task", 10_000, "custom-unknown-category")
	// Just ensure it returns a valid score without panicking.
	if score == nil {
		t.Error("expected non-nil score from fallback keyword verifier")
	}
	_ = score.Overall // must be a valid float
}

func TestRegistry_AllCategoriesMapped(t *testing.T) {
	reg := evidence.NewVerifierRegistry()
	categories := []string{
		"code", "code-review", "technical", "security",
		"data", "data-analysis", "data-validation", "research",
		"writing", "documentation", "translation", "content",
	}
	ev := makeEvidence("some output here with relevant content", "")
	for _, cat := range categories {
		score, _ := reg.Verify(ev, "task", "description", 10_000, cat)
		if score == nil {
			t.Errorf("category %q: expected non-nil score", cat)
		}
	}
}
