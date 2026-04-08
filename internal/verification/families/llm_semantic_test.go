package families_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
	"github.com/Aethernet-network/aethernet/internal/verification/families"
)

// mockLLMClient returns a canned JSON response simulating an LLM evaluation.
type mockLLMClient struct {
	response string
	err      error
}

func (m *mockLLMClient) Complete(_ context.Context, _ string) (string, error) {
	return m.response, m.err
}

func TestLLMSemantic_BasicScoring(t *testing.T) {
	mock := &mockLLMClient{
		response: `{"completeness": 85, "coherence": 90, "depth": 75, "category_fit": 80}`,
	}
	a := families.NewLLMSemanticAnalyzer("llm_semantic/claude_semantic:v1", "v1", mock)
	out, err := a.Analyze(context.Background(), highQualityInput())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out.ScoreBP == 0 {
		t.Error("ScoreBP should be > 0")
	}
	if out.Verdict != "pass" {
		t.Errorf("Verdict = %s; want pass for high scores", out.Verdict)
	}
	// Check individual dimensions are in basis points.
	if out.ScoreBreakdown["completeness"] != 8500 {
		t.Errorf("completeness = %d; want 8500", out.ScoreBreakdown["completeness"])
	}
}

func TestLLMSemantic_LowScores(t *testing.T) {
	mock := &mockLLMClient{
		response: `{"completeness": 20, "coherence": 15, "depth": 10, "category_fit": 25}`,
	}
	a := families.NewLLMSemanticAnalyzer("llm_semantic/claude_semantic:v1", "v1", mock)
	out, _ := a.Analyze(context.Background(), lowQualityInput())
	if out.Verdict != "fail" {
		t.Errorf("Verdict = %s; want fail for low scores", out.Verdict)
	}
}

func TestLLMSemantic_APIError(t *testing.T) {
	mock := &mockLLMClient{err: fmt.Errorf("API unavailable")}
	a := families.NewLLMSemanticAnalyzer("llm_semantic/claude_semantic:v1", "v1", mock)
	_, err := a.Analyze(context.Background(), highQualityInput())
	if err == nil {
		t.Error("expected error when API fails")
	}
}

func TestLLMSemantic_NilClient(t *testing.T) {
	a := families.NewLLMSemanticAnalyzer("llm_semantic/claude_semantic:v1", "v1", nil)
	_, err := a.Analyze(context.Background(), highQualityInput())
	if err == nil {
		t.Error("expected error when client is nil")
	}
}

func TestLLMSemantic_FamilyID(t *testing.T) {
	a := families.NewLLMSemanticAnalyzer("llm_semantic/test:v1", "v1", nil)
	if a.Family() != verification.FamilyLLMSemantic {
		t.Errorf("Family = %s; want %s", a.Family(), verification.FamilyLLMSemantic)
	}
}

func TestLLMSemantic_MalformedResponse(t *testing.T) {
	mock := &mockLLMClient{response: "I think the submission is pretty good overall."}
	a := families.NewLLMSemanticAnalyzer("llm_semantic/claude_semantic:v1", "v1", mock)
	out, err := a.Analyze(context.Background(), highQualityInput())
	// Should NOT error — returns abstain with warning.
	if err != nil {
		t.Fatalf("Analyze should not error on malformed response: %v", err)
	}
	if out.Verdict != "abstain" {
		t.Errorf("Verdict = %s; want abstain for unparseable response", out.Verdict)
	}
	if len(out.Warnings) == 0 {
		t.Error("should have a warning about parse failure")
	}
}
