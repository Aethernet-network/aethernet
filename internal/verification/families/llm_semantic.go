package families

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Aethernet-network/aethernet/internal/verification"
)

// LLMClient is the interface for making LLM API calls. Implementations
// wrap Claude, GPT, or other LLM APIs. Tests inject a mock.
type LLMClient interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// LLMSemanticAnalyzer scores submissions by calling an LLM API with a
// structured evaluation prompt. The LLM assesses completeness, factual
// coherence, depth, and category fit, returning structured scores.
type LLMSemanticAnalyzer struct {
	analyzerID verification.AnalyzerID
	version    string
	client     LLMClient
}

// NewLLMSemanticAnalyzer creates an LLM-based semantic analyzer.
// analyzerID should follow the format "llm_semantic/<name>:<version>".
func NewLLMSemanticAnalyzer(id verification.AnalyzerID, version string, client LLMClient) *LLMSemanticAnalyzer {
	return &LLMSemanticAnalyzer{
		analyzerID: id,
		version:    version,
		client:     client,
	}
}

func (a *LLMSemanticAnalyzer) ID() verification.AnalyzerID { return a.analyzerID }

func (a *LLMSemanticAnalyzer) Family() verification.FamilyID {
	return verification.FamilyLLMSemantic
}

func (a *LLMSemanticAnalyzer) Version() string { return a.version }

func (a *LLMSemanticAnalyzer) Calibration(_ string) bool { return false }

func (a *LLMSemanticAnalyzer) Analyze(ctx context.Context, input verification.AnalysisInput) (*verification.AnalysisOutput, error) {
	start := time.Now()

	if a.client == nil {
		return nil, fmt.Errorf("llm_semantic: no LLM client configured (check API key)")
	}

	// Truncate content to avoid excessive API costs while preserving
	// enough for meaningful evaluation.
	content := input.SubmissionContent
	if len(content) > 8000 {
		content = content[:8000]
	}

	prompt := buildEvaluationPrompt(input.Category, input.TaskTitle, input.TaskDescription, content)

	response, err := a.client.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm_semantic: API call failed: %w", err)
	}

	scores, err := parseEvaluationResponse(response)
	if err != nil {
		return &verification.AnalysisOutput{
			AnalyzerID: a.ID(), Family: a.Family(), Version: a.Version(),
			ScoreBP: 5000, Verdict: "abstain",
			ArtifactHash: hashString(response),
			DurationMS:   time.Since(start).Milliseconds(),
			Warnings:     []string{"failed to parse LLM response: " + err.Error()},
		}, nil
	}

	overall := (scores["completeness"] + scores["coherence"] + scores["depth"] + scores["category_fit"]) / 4

	breakdown := map[string]uint64{
		"completeness": scores["completeness"],
		"coherence":    scores["coherence"],
		"depth":        scores["depth"],
		"category_fit": scores["category_fit"],
	}

	verdict := "fail"
	if overall >= 6000 {
		verdict = "pass"
	}

	artifact, _ := json.Marshal(map[string]any{
		"prompt":   prompt[:min(500, len(prompt))],
		"response": response[:min(1000, len(response))],
		"scores":   breakdown,
	})
	hash := sha256.Sum256(artifact)

	return &verification.AnalysisOutput{
		AnalyzerID:     a.ID(),
		Family:         a.Family(),
		Version:        a.Version(),
		ScoreBP:        overall,
		ScoreBreakdown: breakdown,
		Verdict:        verdict,
		ArtifactHash:   hex.EncodeToString(hash[:]),
		DurationMS:     time.Since(start).Milliseconds(),
	}, nil
}

func buildEvaluationPrompt(category, title, description, content string) string {
	return fmt.Sprintf(`Evaluate the following task submission. Score each dimension from 0 to 100.

Task Category: %s
Task Title: %s
Task Description: %s

Submission Content (first 8000 chars):
%s

Respond with ONLY a JSON object with these four integer fields:
{"completeness": <0-100>, "coherence": <0-100>, "depth": <0-100>, "category_fit": <0-100>}`,
		category, title, description, content)
}

func parseEvaluationResponse(response string) (map[string]uint64, error) {
	// Find JSON in the response (LLMs sometimes add commentary).
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in response")
	}
	jsonStr := response[start : end+1]

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	scores := make(map[string]uint64)
	for _, key := range []string{"completeness", "coherence", "depth", "category_fit"} {
		val, ok := raw[key]
		if !ok {
			return nil, fmt.Errorf("missing key %q", key)
		}
		var n uint64
		switch v := val.(type) {
		case float64:
			n = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid value for %q: %v", key, v)
			}
			n = parsed
		default:
			return nil, fmt.Errorf("unexpected type for %q: %T", key, val)
		}
		if n > 100 {
			n = 100
		}
		scores[key] = n * 100 // convert 0-100 to basis points (0-10000)
	}
	return scores, nil
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// LLMSemanticFamily returns the Family descriptor for registration.
func LLMSemanticFamily() verification.Family {
	return verification.Family{
		ID:          verification.FamilyLLMSemantic,
		Name:        "LLM Semantic",
		Description: "LLM API call with structured evaluation prompt for completeness, coherence, depth, and category fit",
		FailureModes: []string{
			"Confident-sounding nonsense",
			"Hallucinated quality assessments",
			"Prompt injection in submission content",
		},
	}
}

var _ verification.Analyzer = (*LLMSemanticAnalyzer)(nil)
