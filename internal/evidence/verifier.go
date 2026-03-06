package evidence

import "strings"

// Verifier scores Evidence against a task specification and returns a Score
// indicating how well the evidence satisfies the task requirements.
type Verifier struct{}

// NewVerifier creates a new Verifier.
func NewVerifier() *Verifier { return &Verifier{} }

// Verify scores the evidence against the task and returns (score, passed).
// passed is true when score.Overall >= PassThreshold.
func (v *Verifier) Verify(ev *Evidence, taskTitle, taskDescription string, claimedValue uint64) (*Score, bool) {
	score := &Score{}
	score.Relevance = v.assessRelevance(ev.Summary, taskTitle, taskDescription)
	score.Completeness = v.assessCompleteness(ev.OutputSize, ev.OutputType, claimedValue)
	score.Quality = v.assessQuality(ev)
	score.ComputeOverall()
	return score, score.Overall >= PassThreshold
}

// assessRelevance measures keyword overlap between the summary and the task spec.
func (v *Verifier) assessRelevance(summary, title, description string) float64 {
	if strings.TrimSpace(summary) == "" {
		return 0.0
	}
	summaryLower := strings.ToLower(summary)
	taskWords := extractWords(strings.ToLower(title) + " " + strings.ToLower(description))
	if len(taskWords) == 0 {
		return 0.5
	}
	matches := 0
	for _, w := range taskWords {
		if strings.Contains(summaryLower, w) {
			matches++
		}
	}
	ratio := float64(matches) / float64(len(taskWords))
	if ratio > 1 {
		ratio = 1
	}
	return ratio
}

// assessCompleteness checks output size relative to type-specific minima and claimed value.
func (v *Verifier) assessCompleteness(outputSize uint64, outputType string, claimedValue uint64) float64 {
	minSizes := map[string]uint64{
		"text":  100,
		"json":  50,
		"code":  200,
		"data":  100,
		"image": 10000,
	}
	minSize, ok := minSizes[outputType]
	if !ok {
		minSize = 50
	}
	if outputSize < minSize {
		return 0.2
	}
	expectedSize := minSize * (claimedValue / 1_000_000)
	if expectedSize < minSize {
		expectedSize = minSize
	}
	ratio := float64(outputSize) / float64(expectedSize)
	if ratio > 1 {
		ratio = 1
	}
	return 0.5 + ratio*0.5
}

// assessQuality awards bonus points for additional evidence quality signals.
func (v *Verifier) assessQuality(ev *Evidence) float64 {
	score := 0.5
	if len(ev.Metrics) > 0 {
		score += 0.15
	}
	if len(ev.OutputPreview) > 100 {
		score += 0.15
	}
	if ev.OutputURL != "" {
		score += 0.10
	}
	if ev.InputHash != "" {
		score += 0.10
	}
	if score > 1 {
		score = 1
	}
	return score
}

// extractWords returns significant words (>3 chars) stripped of punctuation.
func extractWords(text string) []string {
	words := strings.Fields(text)
	var significant []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if len(w) > 3 {
			significant = append(significant, w)
		}
	}
	return significant
}
