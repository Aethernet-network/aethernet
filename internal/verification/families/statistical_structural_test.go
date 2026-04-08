package families_test

import (
	"context"
	"testing"

	"github.com/Aethernet-network/aethernet/internal/verification"
	"github.com/Aethernet-network/aethernet/internal/verification/families"
)

func TestStatistical_BasicScoring(t *testing.T) {
	a := families.NewStatisticalAnalyzer()
	out, err := a.Analyze(context.Background(), highQualityInput())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out.ScoreBP == 0 {
		t.Error("ScoreBP should be > 0")
	}
	if len(out.ScoreBreakdown) == 0 {
		t.Error("ScoreBreakdown should have entries")
	}
}

func TestStatistical_DeterministicArtifactHash(t *testing.T) {
	a := families.NewStatisticalAnalyzer()
	input := highQualityInput()
	out1, _ := a.Analyze(context.Background(), input)
	out2, _ := a.Analyze(context.Background(), input)
	if out1.ArtifactHash != out2.ArtifactHash {
		t.Error("same input should produce same artifact hash")
	}
}

func TestStatistical_FamilyID(t *testing.T) {
	a := families.NewStatisticalAnalyzer()
	if a.Family() != verification.FamilyStatisticalStructural {
		t.Errorf("Family = %s; want %s", a.Family(), verification.FamilyStatisticalStructural)
	}
}

func TestStatistical_EmptyContent(t *testing.T) {
	a := families.NewStatisticalAnalyzer()
	out, err := a.Analyze(context.Background(), verification.AnalysisInput{
		SubmissionContent: "",
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out.ScoreBP != 0 {
		t.Errorf("ScoreBP = %d; want 0 for empty content", out.ScoreBP)
	}
	if out.Verdict != "fail" {
		t.Errorf("Verdict = %s; want fail for empty content", out.Verdict)
	}
}
