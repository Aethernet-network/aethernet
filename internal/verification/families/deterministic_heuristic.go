package families

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// HeuristicAnalyzer wraps the existing evidence.VerifierRegistry into the
// Analyzer interface. This is the same scoring logic the testnet has used
// since day one — word count, structure detection, keyword matching,
// formatting analysis — now queryable through the multi-validator registry.
type HeuristicAnalyzer struct {
	registry  *evidence.VerifierRegistry
	threshold float64 // pass threshold (category-specific in the registry)
}

// NewHeuristicAnalyzer creates a deterministic heuristic analyzer backed
// by the existing evidence verifier registry.
func NewHeuristicAnalyzer() *HeuristicAnalyzer {
	return &HeuristicAnalyzer{
		registry:  evidence.NewVerifierRegistry(),
		threshold: evidence.PassThreshold,
	}
}

func (a *HeuristicAnalyzer) ID() verification.AnalyzerID {
	return "deterministic_heuristic/heuristic:v1"
}

func (a *HeuristicAnalyzer) Family() verification.FamilyID {
	return verification.FamilyDeterministicHeuristic
}

func (a *HeuristicAnalyzer) Version() string { return "v1" }

func (a *HeuristicAnalyzer) Calibration(_ string) bool { return false }

func (a *HeuristicAnalyzer) Analyze(_ context.Context, input verification.AnalysisInput) (*verification.AnalysisOutput, error) {
	start := time.Now()

	// Build an Evidence object from the input.
	ev := &evidence.Evidence{
		Hash:          input.EvidenceHash,
		OutputType:    "text",
		OutputSize:    uint64(len(input.SubmissionContent)),
		Summary:       input.TaskDescription,
		ResultContent: input.SubmissionContent,
	}

	score, passed := a.registry.Verify(ev, input.TaskTitle, input.TaskDescription, 0, input.Category)

	verdict := "fail"
	if passed {
		verdict = "pass"
	}

	breakdown := map[string]uint64{
		"relevance":    uint64(score.Relevance * 10000),
		"completeness": uint64(score.Completeness * 10000),
		"quality":      uint64(score.Quality * 10000),
	}

	scoreBP := uint64(score.Overall * 10000)

	// Deterministic artifact: canonical JSON of the score breakdown.
	artifact, _ := json.Marshal(breakdown)
	hash := sha256.Sum256(artifact)

	return &verification.AnalysisOutput{
		AnalyzerID:     a.ID(),
		Family:         a.Family(),
		Version:        a.Version(),
		ScoreBP:        scoreBP,
		ScoreBreakdown: breakdown,
		Verdict:        verdict,
		ArtifactHash:   hex.EncodeToString(hash[:]),
		DurationMS:     time.Since(start).Milliseconds(),
	}, nil
}

// HeuristicFamily returns the Family descriptor for registration.
func HeuristicFamily() verification.Family {
	return verification.Family{
		ID:          verification.FamilyDeterministicHeuristic,
		Name:        "Deterministic Heuristic",
		Description: "Word count, structure detection, keyword matching, formatting analysis",
		FailureModes: []string{
			"Keyword stuffing",
			"Structurally correct but factually wrong content",
			"Gaming word count without substance",
		},
	}
}

var _ verification.Analyzer = (*HeuristicAnalyzer)(nil)
