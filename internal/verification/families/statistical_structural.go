package families

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"
	"github.com/Aethernet-network/aethernet/internal/verification"
)

// StatisticalAnalyzer scores submissions using statistical and structural
// properties of the text: token entropy, sentence variety, lexical
// sophistication, structural completeness, and citation density.
// Fully deterministic — no external dependencies.
type StatisticalAnalyzer struct{}

func NewStatisticalAnalyzer() *StatisticalAnalyzer { return &StatisticalAnalyzer{} }

func (a *StatisticalAnalyzer) ID() verification.AnalyzerID {
	return "statistical_structural/statistical:v1"
}

func (a *StatisticalAnalyzer) Family() verification.FamilyID {
	return verification.FamilyStatisticalStructural
}

func (a *StatisticalAnalyzer) Version() string { return "v1" }

func (a *StatisticalAnalyzer) Calibration(_ string) bool { return false }

func (a *StatisticalAnalyzer) Analyze(_ context.Context, input verification.AnalysisInput) (*verification.AnalysisOutput, error) {
	start := time.Now()
	content := input.SubmissionContent
	if content == "" {
		return &verification.AnalysisOutput{
			AnalyzerID: a.ID(), Family: a.Family(), Version: a.Version(),
			ScoreBP: 0, Verdict: "fail",
			DurationMS: time.Since(start).Milliseconds(),
		}, nil
	}

	entropy := tokenEntropy(content)
	sentenceVariety := sentenceLengthVariety(content)
	ttr := typeTokenRatio(content)
	sectionCount := countSections(content)
	citationDensity := countCitations(content)

	// Normalize each metric to [0, 10000] BP.
	entropyBP := clampBP(entropy / 5.0)               // 5.0 bits = max realistic entropy
	varietyBP := clampBP(sentenceVariety / 15.0)       // stdev of 15 words = diverse
	ttrBP := clampBP(ttr)                              // already [0,1]
	sectionBP := clampBP(float64(sectionCount) / 5.0)  // 5+ sections = fully structured
	citationBP := clampBP(float64(citationDensity) / 5.0)

	overall := (entropyBP + varietyBP + ttrBP + sectionBP + citationBP) / 5

	breakdown := map[string]uint64{
		"entropy":          uint64(entropyBP * 10000),
		"sentence_variety": uint64(varietyBP * 10000),
		"type_token_ratio": uint64(ttrBP * 10000),
		"sections":         uint64(sectionBP * 10000),
		"citations":        uint64(citationBP * 10000),
	}

	verdict := "fail"
	if overall >= 0.50 {
		verdict = "pass"
	}

	artifact, _ := json.Marshal(breakdown)
	hash := sha256.Sum256(artifact)

	return &verification.AnalysisOutput{
		AnalyzerID:     a.ID(),
		Family:         a.Family(),
		Version:        a.Version(),
		ScoreBP:        uint64(overall * 10000),
		ScoreBreakdown: breakdown,
		Verdict:        verdict,
		ArtifactHash:   hex.EncodeToString(hash[:]),
		DurationMS:     time.Since(start).Milliseconds(),
	}, nil
}

// tokenEntropy computes Shannon entropy of the word distribution.
func tokenEntropy(text string) float64 {
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return 0
	}
	freq := make(map[string]int)
	for _, w := range words {
		freq[w]++
	}
	total := float64(len(words))
	var h float64
	for _, count := range freq {
		p := float64(count) / total
		if p > 0 {
			h -= p * math.Log2(p)
		}
	}
	return h
}

// sentenceLengthVariety computes the standard deviation of sentence lengths.
func sentenceLengthVariety(text string) float64 {
	sentences := splitSentences(text)
	if len(sentences) < 2 {
		return 0
	}
	var sum float64
	lengths := make([]float64, len(sentences))
	for i, s := range sentences {
		lengths[i] = float64(len(strings.Fields(s)))
		sum += lengths[i]
	}
	mean := sum / float64(len(sentences))
	var variance float64
	for _, l := range lengths {
		d := l - mean
		variance += d * d
	}
	variance /= float64(len(sentences))
	return math.Sqrt(variance)
}

// typeTokenRatio is the ratio of unique words to total words.
func typeTokenRatio(text string) float64 {
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return 0
	}
	unique := make(map[string]struct{})
	for _, w := range words {
		unique[w] = struct{}{}
	}
	return float64(len(unique)) / float64(len(words))
}

// countSections counts lines that look like headings (start with # or are
// ALL CAPS short lines or numbered sections).
func countSections(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			count++
		} else if len(trimmed) < 80 && trimmed == strings.ToUpper(trimmed) && len(strings.Fields(trimmed)) <= 6 {
			count++
		}
	}
	return count
}

// countCitations counts patterns that look like references or citations.
func countCitations(text string) int {
	count := 0
	for _, marker := range []string{"[1]", "[2]", "[3]", "[4]", "[5]",
		"(2020)", "(2021)", "(2022)", "(2023)", "(2024)", "(2025)", "(2026)",
		"et al.", "doi:", "http://", "https://", "arXiv:"} {
		count += strings.Count(text, marker)
	}
	return count
}

func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			s := strings.TrimSpace(current.String())
			if len(strings.Fields(s)) >= 3 {
				sentences = append(sentences, s)
			}
			current.Reset()
		}
	}
	if s := strings.TrimSpace(current.String()); len(strings.Fields(s)) >= 3 {
		sentences = append(sentences, s)
	}
	return sentences
}

func clampBP(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// StatisticalFamily returns the Family descriptor for registration.
func StatisticalFamily() verification.Family {
	return verification.Family{
		ID:          verification.FamilyStatisticalStructural,
		Name:        "Statistical Structural",
		Description: "Token entropy, sentence variety, lexical sophistication, structural completeness, citation density",
		FailureModes: []string{
			"Well-structured statistically diverse content that is factually wrong",
			"Content generated to maximize statistical metrics without substance",
		},
	}
}

var _ verification.Analyzer = (*StatisticalAnalyzer)(nil)
