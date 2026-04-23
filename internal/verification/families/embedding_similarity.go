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

// EmbeddingAnalyzer scores submissions using bag-of-words TF-IDF cosine
// similarity between the task description and submission content. This is
// the bootstrap version — fully deterministic, no external API.
//
// A future version can inject an EmbeddingClient for real vector embeddings.
type EmbeddingAnalyzer struct{}

func NewEmbeddingAnalyzer() *EmbeddingAnalyzer { return &EmbeddingAnalyzer{} }

func (a *EmbeddingAnalyzer) ID() verification.AnalyzerID {
	return "embedding_similarity/embedding:v1"
}

func (a *EmbeddingAnalyzer) Family() verification.FamilyID {
	return verification.FamilyEmbeddingSimilarity
}

func (a *EmbeddingAnalyzer) Version() string { return "v1" }

func (a *EmbeddingAnalyzer) Calibration(_ string) bool { return false }

func (a *EmbeddingAnalyzer) Analyze(_ context.Context, input verification.AnalysisInput) (*verification.AnalysisOutput, error) {
	start := time.Now()

	if input.SubmissionContent == "" {
		return &verification.AnalysisOutput{
			AnalyzerID: a.ID(), Family: a.Family(), Version: a.Version(),
			ScoreBP: 0, Verdict: "fail",
			DurationMS: time.Since(start).Milliseconds(),
		}, nil
	}

	taskText := input.TaskTitle + " " + input.TaskDescription
	similarity := tfidfCosineSimilarity(taskText, input.SubmissionContent)
	topicCoverage := topicTermCoverage(taskText, input.SubmissionContent)
	contentDepth := clampBP(float64(len(strings.Fields(input.SubmissionContent))) / 500.0)

	// Weight: similarity 40%, topic coverage 40%, content depth 20%.
	overall := similarity*0.40 + topicCoverage*0.40 + contentDepth*0.20

	breakdown := map[string]uint64{
		"cosine_similarity": uint64(similarity * 10000),
		"topic_coverage":    uint64(topicCoverage * 10000),
		"content_depth":     uint64(contentDepth * 10000),
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

// tfidfCosineSimilarity computes TF-IDF weighted cosine similarity between
// two documents. Uses simple term frequency with inverse document frequency
// across the two-document corpus.
func tfidfCosineSimilarity(doc1, doc2 string) float64 {
	tf1 := termFrequency(doc1)
	tf2 := termFrequency(doc2)

	// Build vocabulary and compute IDF.
	vocab := make(map[string]struct{})
	for w := range tf1 {
		vocab[w] = struct{}{}
	}
	for w := range tf2 {
		vocab[w] = struct{}{}
	}

	// IDF: log(2 / df) where df is 1 or 2.
	idf := make(map[string]float64, len(vocab))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for w := range vocab {
		df := 0
		if tf1[w] > 0 {
			df++
		}
		if tf2[w] > 0 {
			df++
		}
		idf[w] = math.Log(2.0/float64(df)) + 1.0 // smoothed
	}

	// Compute TF-IDF vectors and cosine similarity.
	var dot, mag1, mag2 float64
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for w := range vocab {
		v1 := tf1[w] * idf[w]
		v2 := tf2[w] * idf[w]
		dot += v1 * v2
		mag1 += v1 * v1
		mag2 += v2 * v2
	}
	if mag1 == 0 || mag2 == 0 {
		return 0
	}
	return dot / (math.Sqrt(mag1) * math.Sqrt(mag2))
}

// termFrequency computes normalized term frequency for a document.
func termFrequency(text string) map[string]float64 {
	words := strings.Fields(strings.ToLower(text))
	counts := make(map[string]int)
	for _, w := range words {
		// Strip punctuation.
		w = strings.Trim(w, ".,;:!?\"'()[]{}—-")
		if len(w) < 2 {
			continue
		}
		counts[w]++
	}
	total := float64(len(words))
	if total == 0 {
		return nil
	}
	freq := make(map[string]float64, len(counts))
	// safe: iteration order does not affect canonical state (non-canonical local surface, or commutative effect)
	for w, c := range counts {
		freq[w] = float64(c) / total
	}
	return freq
}

// topicTermCoverage measures what fraction of task-specific terms appear
// in the submission.
func topicTermCoverage(taskText, submission string) float64 {
	taskWords := strings.Fields(strings.ToLower(taskText))
	subLower := strings.ToLower(submission)

	stopWords := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "from": true,
		"that": true, "this": true, "are": true, "was": true, "will": true,
		"can": true, "about": true, "what": true, "how": true,
	}

	var terms []string
	seen := make(map[string]bool)
	for _, w := range taskWords {
		w = strings.Trim(w, ".,;:!?\"'()[]{}—-")
		if len(w) < 4 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
	}
	if len(terms) == 0 {
		return 1.0 // no task context — do not penalize
	}

	found := 0
	for _, t := range terms {
		if strings.Contains(subLower, t) {
			found++
		}
	}
	return float64(found) / float64(len(terms))
}

// EmbeddingFamily returns the Family descriptor for registration.
func EmbeddingFamily() verification.Family {
	return verification.Family{
		ID:          verification.FamilyEmbeddingSimilarity,
		Name:        "Embedding Similarity",
		Description: "TF-IDF cosine similarity between task description and submission content",
		FailureModes: []string{
			"Topic-relevant but shallow content",
			"Content that matches keywords but lacks depth",
		},
	}
}

var _ verification.Analyzer = (*EmbeddingAnalyzer)(nil)
