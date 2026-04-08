package families_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
	"github.com/Aethernet-network/aethernet/internal/verification/families"
)

func highQualityInput() verification.AnalysisInput {
	return verification.AnalysisInput{
		TaskID:          "task-1",
		Category:        "research",
		TaskTitle:       "Analyze the impact of transformer architectures on NLP",
		TaskDescription: "Research the evolution of transformer models and their impact on natural language processing tasks including translation, summarization, and question answering.",
		SubmissionContent: "The transformer architecture has fundamentally reshaped natural language processing since its introduction in the seminal paper 'Attention Is All You Need' by Vaswani et al. in 2017. " +
			"This analysis examines the evolution, impact, and future directions of transformer-based models. " +
			"Section 1: Historical Context. Prior to transformers, NLP relied heavily on recurrent neural networks (RNNs) and long short-term memory (LSTM) networks. " +
			"Section 2: The Attention Mechanism. The key innovation of transformers is the self-attention mechanism, which allows the model to weigh the importance of different parts of the input. " +
			"Section 3: Impact on Translation. Machine translation saw dramatic improvements with transformer models, with BLEU scores increasing by 30% over previous architectures. " +
			"Section 4: Impact on Summarization. Abstractive summarization became viable at scale with transformer-based models like BART and T5. " +
			"Section 5: Question Answering. Models like BERT achieved human-level performance on SQuAD benchmarks. " +
			"In conclusion, transformer architectures represent the most significant paradigm shift in NLP history, enabling capabilities previously considered decades away.",
		EvidenceHash: "sha256:test",
	}
}

func lowQualityInput() verification.AnalysisInput {
	return verification.AnalysisInput{
		TaskID:            "task-2",
		Category:          "research",
		TaskTitle:         "Analyze the impact of transformer architectures",
		TaskDescription:   "Research transformer models and NLP",
		SubmissionContent: "ok here is my answer. transformers are good.",
		EvidenceHash:      "sha256:low",
	}
}

func TestHeuristic_BasicScoring(t *testing.T) {
	a := families.NewHeuristicAnalyzer()
	out, err := a.Analyze(context.Background(), highQualityInput())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out.ScoreBP == 0 {
		t.Error("ScoreBP should be > 0 for quality content")
	}
	if out.Verdict != "pass" && out.Verdict != "fail" {
		t.Errorf("Verdict = %q; want pass or fail", out.Verdict)
	}
}

func TestHeuristic_DeterministicArtifactHash(t *testing.T) {
	a := families.NewHeuristicAnalyzer()
	input := highQualityInput()
	out1, _ := a.Analyze(context.Background(), input)
	out2, _ := a.Analyze(context.Background(), input)
	if out1.ArtifactHash != out2.ArtifactHash {
		t.Error("same input should produce same artifact hash (deterministic)")
	}
}

func TestHeuristic_FamilyID(t *testing.T) {
	a := families.NewHeuristicAnalyzer()
	if a.Family() != verification.FamilyDeterministicHeuristic {
		t.Errorf("Family = %s; want %s", a.Family(), verification.FamilyDeterministicHeuristic)
	}
}

func TestHeuristic_LowQualityScores(t *testing.T) {
	a := families.NewHeuristicAnalyzer()
	out, err := a.Analyze(context.Background(), lowQualityInput())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out.ScoreBP > 5000 {
		t.Errorf("ScoreBP = %d; want < 5000 for low-quality input", out.ScoreBP)
	}
}
