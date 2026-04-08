package families_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
	"github.com/Aethernet-network/aethernet/internal/verification/families"
)

func TestEmbedding_BasicScoring(t *testing.T) {
	a := families.NewEmbeddingAnalyzer()
	out, err := a.Analyze(context.Background(), highQualityInput())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out.ScoreBP == 0 {
		t.Error("ScoreBP should be > 0 for relevant content")
	}
}

func TestEmbedding_DeterministicArtifactHash(t *testing.T) {
	a := families.NewEmbeddingAnalyzer()
	input := highQualityInput()
	out1, _ := a.Analyze(context.Background(), input)
	out2, _ := a.Analyze(context.Background(), input)
	if out1.ArtifactHash != out2.ArtifactHash {
		t.Error("same input should produce same artifact hash")
	}
}

func TestEmbedding_FamilyID(t *testing.T) {
	a := families.NewEmbeddingAnalyzer()
	if a.Family() != verification.FamilyEmbeddingSimilarity {
		t.Errorf("Family = %s; want %s", a.Family(), verification.FamilyEmbeddingSimilarity)
	}
}

func TestEmbedding_IrrelevantContent(t *testing.T) {
	a := families.NewEmbeddingAnalyzer()
	out, _ := a.Analyze(context.Background(), verification.AnalysisInput{
		TaskTitle:         "Analyze transformer architectures",
		TaskDescription:   "Research NLP models",
		SubmissionContent: "The recipe for chocolate cake requires flour, sugar, and cocoa powder. Mix ingredients and bake at 350F for 30 minutes.",
		Category:          "research",
	})
	// Irrelevant content should score low on topic coverage.
	if out.ScoreBreakdown["topic_coverage"] > 3000 {
		t.Errorf("topic_coverage = %d; expected low for irrelevant content", out.ScoreBreakdown["topic_coverage"])
	}
}

func TestEmbedding_EmptyContent(t *testing.T) {
	a := families.NewEmbeddingAnalyzer()
	out, _ := a.Analyze(context.Background(), verification.AnalysisInput{SubmissionContent: ""})
	if out.Verdict != "fail" {
		t.Errorf("Verdict = %s; want fail for empty content", out.Verdict)
	}
}
