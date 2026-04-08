package verification

import "context"

// ---------------------------------------------------------------------------
// Family identity
// ---------------------------------------------------------------------------

// FamilyID identifies a methodologically distinct verification family.
// Two analyzers from different developers share a FamilyID only if they use
// the same underlying methodology. The diversity floor counts distinct
// FamilyIDs, not distinct analyzers.
type FamilyID string

const (
	FamilyLLMSemantic            FamilyID = "llm_semantic"
	FamilyDeterministicHeuristic FamilyID = "deterministic_heuristic"
	FamilyEmbeddingSimilarity    FamilyID = "embedding_similarity"
	FamilyStatisticalStructural  FamilyID = "statistical_structural"
)

// Family describes a verification methodology family.
type Family struct {
	ID           FamilyID
	Name         string
	Description  string
	FailureModes []string // what this family can be fooled by
}

// ---------------------------------------------------------------------------
// Analyzer identity
// ---------------------------------------------------------------------------

// AnalyzerID uniquely identifies a specific analyzer instance.
// Format: "<family_id>/<analyzer_name>:<version>"
type AnalyzerID string

// ---------------------------------------------------------------------------
// Analysis I/O
// ---------------------------------------------------------------------------

// AnalysisInput is the data an analyzer scores.
type AnalysisInput struct {
	TaskID            string
	Category          string
	TaskTitle         string
	TaskDescription   string
	SubmissionContent string // full evidence content (from Evidence.ResolveContent)
	EvidenceHash      string
	SubmittedAt       int64
}

// AnalysisOutput is the analyzer's verdict and score.
type AnalysisOutput struct {
	AnalyzerID     AnalyzerID
	Family         FamilyID
	Version        string
	ScoreBP        uint64            // 0-10000 basis points
	ScoreBreakdown map[string]uint64 // dimension → basis points
	Verdict        string            // "pass" | "fail" | "abstain"
	ArtifactHash   string            // SHA-256 of deterministic analysis artifact
	DurationMS     int64
	Warnings       []string
}

// ---------------------------------------------------------------------------
// Analyzer interface
// ---------------------------------------------------------------------------

// Analyzer is the interface every scoring implementation satisfies.
type Analyzer interface {
	// ID returns the unique analyzer identifier.
	ID() AnalyzerID

	// Family returns the methodology family this analyzer belongs to.
	Family() FamilyID

	// Version returns the analyzer's version string.
	Version() string

	// Analyze scores a task submission. Returns an error if the analyzer
	// cannot produce a result (e.g., missing API key, malformed input).
	Analyze(ctx context.Context, input AnalysisInput) (*AnalysisOutput, error)

	// Calibration returns true if this analyzer is in calibration mode for
	// the given category. Calibration-mode votes carry zero weight.
	Calibration(category string) bool
}
