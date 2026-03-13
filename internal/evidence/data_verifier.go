package evidence

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// topicStopWords are excluded when extracting task-specific key terms for
// topic relevance checks. Includes grammatical filler and task-framing verbs
// that describe the assignment itself rather than the subject matter.
var topicStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"that": true, "this": true, "about": true, "what": true, "how": true,
	"are": true, "was": true, "will": true, "can": true, "should": true,
	"write": true, "create": true, "analyze": true, "analyse": true,
	"research": true, "summarize": true, "summarise": true, "explain": true,
	"describe": true, "implement": true, "build": true, "provide": true,
	"calculate": true,
}

// topicTermFraction returns the fraction of task-specific terms (extracted from
// title+description) that appear anywhere in content. Terms shorter than 4 chars
// or in topicStopWords are excluded. Returns 1.0 when no task terms are found
// (vacuously correct — cannot penalise a submission for lack of task context).
func topicTermFraction(content, title, description string) float64 {
	wordRe := regexp.MustCompile(`[a-zA-Z]{4,}`)
	raw := wordRe.FindAllString(strings.ToLower(title+" "+description), -1)
	seen := make(map[string]bool)
	var terms []string
	for _, w := range raw {
		if !topicStopWords[w] && !seen[w] {
			seen[w] = true
			terms = append(terms, w)
		}
	}
	if len(terms) == 0 {
		return 1.0 // no task context — do not penalise
	}
	lower := strings.ToLower(content)
	matched := 0
	for _, term := range terms {
		if strings.Contains(lower, term) {
			matched++
		}
	}
	return float64(matched) / float64(len(terms))
}

// DataVerifier implements VerifierInterface for task categories that require
// analytical data output: "data", "data-analysis", "data-validation", "research".
//
// It evaluates four deterministic dimensions:
//  1. Structure validity  (20%): is the output well-structured (JSON, CSV, tables)?
//  2. Completeness        (30%): is the word count sufficient relative to budget?
//  3. Analytical depth    (30%): does the output show analysis, not just raw data?
//  4. Citation/evidence   (20%): does the output reference sources or data points?
//
// Pass threshold: 0.5
type DataVerifier struct{}

const dataPassThreshold = 0.70

// Verify implements VerifierInterface.
func (dv *DataVerifier) Verify(ev *Evidence, taskTitle, taskDescription string, budget uint64) (*Score, bool) {
	if ev == nil {
		return &Score{}, false
	}
	content := strings.TrimSpace(ev.Summary)
	if ev.OutputPreview != "" {
		content = strings.TrimSpace(content + "\n" + ev.OutputPreview)
	}

	// Use the larger of: counted words in the preview+summary, or estimated
	// from OutputSize (1 word ≈ 6 bytes). This prevents the completeness
	// score from being penalised when the full output far exceeds the preview.
	wordCount := countWords(content)
	if est := int(ev.OutputSize) / 6; est > wordCount {
		wordCount = est
	}

	structure := dv.scoreStructure(content)
	completeness := dv.scoreCompleteness(wordCount, budget)
	depth := dv.scoreAnalyticalDepth(content)
	citation := dv.scoreCitation(content)

	overall := structure*0.20 + completeness*0.30 + depth*0.30 + citation*0.20

	// Cap overall when content is clearly off-topic.
	if topicTermFraction(content, taskTitle, taskDescription) < 0.20 {
		if overall > 0.30 {
			overall = 0.30
		}
	}

	score := &Score{
		Relevance:    depth,        // analytical depth maps to relevance (shows work)
		Completeness: completeness, // word count / substance
		Quality:      (structure + citation) / 2,
		Overall:      overall,
	}
	return score, overall >= dataPassThreshold
}

// scoreStructure evaluates whether the output is well-structured.
func (dv *DataVerifier) scoreStructure(content string) float64 {
	if content == "" {
		return 0.0
	}

	// Try JSON parse — highest structural score.
	if looksLikeJSON(content) {
		var v interface{}
		if json.Unmarshal([]byte(content), &v) == nil {
			return 1.0
		}
		return 0.6
	}

	score := 0.0

	// Check for Markdown tables.
	if strings.Contains(content, "| ") && strings.Contains(content, " |") {
		score += 0.3
	}

	// Check for consistent key-value pairs ("key: value" style).
	kvRe := regexp.MustCompile(`(?m)^\s*\w[\w\s]{1,30}:\s+\S`)
	if len(kvRe.FindAllString(content, -1)) >= 3 {
		score += 0.25
	}

	// Check for numbered lists or bullet points.
	listRe := regexp.MustCompile(`(?m)^[\s]*[-*•]|\d+\.`)
	if len(listRe.FindAllString(content, -1)) >= 3 {
		score += 0.25
	}

	// Check for section headers.
	headerRe := regexp.MustCompile(`(?m)^#{1,3} \w|^[A-Z][A-Za-z\s]{3,40}:`)
	if len(headerRe.FindAllString(content, -1)) >= 2 {
		score += 0.2
	}

	return clamp(score, 0, 1)
}

// scoreCompleteness evaluates word count relative to task budget and complexity.
// wordCount should be pre-computed by Verify using the larger of the actual preview
// word count and the estimated count derived from ev.OutputSize.
func (dv *DataVerifier) scoreCompleteness(wordCount int, budget uint64) float64 {
	if wordCount == 0 {
		return 0.0
	}

	// Budget-scaled minimum: higher budget → more complex task → more words expected.
	minWords := 50
	if budget >= 1_000_000 {
		minWords = 200
	} else if budget >= 500_000 {
		minWords = 100
	}

	if wordCount < minWords/2 {
		return 0.1
	}
	ratio := float64(wordCount) / float64(minWords)
	return clamp(0.3+ratio*0.7, 0, 1)
}

// scoreAnalyticalDepth measures whether the output contains analysis rather than
// raw data dumps.
func (dv *DataVerifier) scoreAnalyticalDepth(content string) float64 {
	lower := strings.ToLower(content)
	score := 0.0

	// Quantitative terms: numbers with units/symbols or decimal-precision values.
	numRe := regexp.MustCompile(`\d+\.?\d*\s*(%|percent|million|billion|thousand|\$|€|£|GB|MB|KB|ms|ns)`)
	numMatches := len(numRe.FindAllString(content, -1))
	// Decimal-precision numbers (e.g. 1.45, 0.89) are strong signals of real data
	// even when units are absent (e.g. JSON with scientific measurements).
	decRe := regexp.MustCompile(`\b\d+\.\d{2,}\b`)
	decMatches := len(decRe.FindAllString(content, -1))
	hasQuantData := numMatches >= 2 || decMatches >= 2

	if hasQuantData {
		score += 0.35
	} else if regexp.MustCompile(`\d+`).MatchString(content) {
		score += 0.15 // has numbers but not with units or decimal precision
	}

	// Analytical vocabulary — requires quantitative backing for the full bonus.
	// Rich analytical language without sufficient data signals superficial filler.
	analyticalTerms := []string{
		"indicates", "suggests", "correlates", "trend", "analysis", "analysed",
		"analyzed", "conclude", "therefore", "finding", "result", "demonstrates",
		"shows", "reveals", "pattern", "distribution", "average", "median",
		"significant", "notable", "compared", "relative",
	}
	termCount := 0
	for _, term := range analyticalTerms {
		if strings.Contains(lower, term) {
			termCount++
		}
	}
	if termCount >= 3 {
		if hasQuantData {
			score += 0.35 // analytical vocabulary with quantitative support
		} else {
			score += 0.15 // analytical language but insufficient quantitative data
		}
	} else if termCount >= 1 {
		score += float64(termCount) * 0.10
	}

	// Conclusions or recommendations section.
	conclusionTerms := []string{"conclusion", "summary", "recommend", "recommendation", "finding", "insight", "takeaway"}
	for _, term := range conclusionTerms {
		if strings.Contains(lower, term) {
			score += 0.30
			break
		}
	}

	return clamp(score, 0, 1)
}

// scoreCitation measures whether the output references external sources or data.
func (dv *DataVerifier) scoreCitation(content string) float64 {
	score := 0.0

	// URLs as data sources.
	urlRe := regexp.MustCompile(`https?://\S+`)
	if len(urlRe.FindAllString(content, -1)) >= 1 {
		score += 0.4
	}

	// Academic-style references.
	refRe := regexp.MustCompile(`\[\d+\]|et al\.|Source:|Reference:|via `)
	if refRe.MatchString(content) {
		score += 0.3
	}

	// Specific data points (number + unit combinations).
	dataPointRe := regexp.MustCompile(`\d+\.?\d*\s*(gb|tb|mb|ms|µs|ns|kg|km|%|usd|\$)`)
	if len(dataPointRe.FindAllString(strings.ToLower(content), -1)) >= 2 {
		score += 0.3
	}

	return clamp(score, 0, 1)
}

// countWords counts space-separated words in text.
func countWords(text string) int {
	count := 0
	inWord := false
	for _, ch := range text {
		if unicode.IsSpace(ch) {
			inWord = false
		} else if !inWord {
			count++
			inWord = true
		}
	}
	return count
}

// Compile-time assertion: DataVerifier must satisfy VerifierInterface.
var _ VerifierInterface = (*DataVerifier)(nil)
