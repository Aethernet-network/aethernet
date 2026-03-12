package evidence

import (
	"regexp"
	"strings"
)

// ContentVerifier implements VerifierInterface for task categories that require
// written or translated content output: "writing", "documentation", "translation",
// "content".
//
// It evaluates four deterministic dimensions:
//  1. Language quality   (20%): sentence length distribution, paragraph structure
//  2. Completeness       (30%): word count; respects task-specified minimums
//  3. Topic relevance    (30%): key terms from the task description present in content
//  4. Formatting quality (20%): headings, paragraph breaks, no excessive repetition
//
// Pass threshold: 0.5
type ContentVerifier struct{}

const contentPassThreshold = 0.50

// Verify implements VerifierInterface.
func (cv *ContentVerifier) Verify(ev *Evidence, taskTitle, taskDescription string, budget uint64) (*Score, bool) {
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

	language := cv.scoreLanguageQuality(content)
	completeness := cv.scoreCompleteness(wordCount, taskTitle, taskDescription)
	relevance := cv.scoreTopicRelevance(content, taskTitle, taskDescription)
	formatting := cv.scoreFormatting(content)

	overall := language*0.20 + completeness*0.30 + relevance*0.30 + formatting*0.20

	score := &Score{
		Relevance:    relevance,
		Completeness: completeness,
		Quality:      (language + formatting) / 2,
		Overall:      overall,
	}
	return score, overall >= contentPassThreshold
}

// scoreLanguageQuality evaluates sentence length distribution and paragraph structure.
func (cv *ContentVerifier) scoreLanguageQuality(content string) float64 {
	if content == "" {
		return 0.0
	}

	sentences := splitSentences(content)
	if len(sentences) == 0 {
		return 0.1
	}

	// Score sentence length distribution (target: 10–25 words per sentence).
	goodSentences := 0
	for _, s := range sentences {
		wc := countWords(s)
		if wc >= 5 && wc <= 40 {
			goodSentences++
		}
	}
	sentenceScore := clamp(float64(goodSentences)/float64(len(sentences)), 0, 1)

	// Paragraph structure: at least 2 paragraphs earns a bonus.
	paragraphs := strings.Split(content, "\n\n")
	parScore := 0.0
	if len(paragraphs) >= 3 {
		parScore = 1.0
	} else if len(paragraphs) == 2 {
		parScore = 0.6
	} else if len(sentences) >= 3 {
		parScore = 0.3 // single block but has multiple sentences
	}

	return sentenceScore*0.6 + parScore*0.4
}

// scoreCompleteness evaluates word count relative to task-specified minimums and budget.
// wordCount should be pre-computed by Verify using the larger of the actual preview word
// count and the estimated count derived from ev.OutputSize.
func (cv *ContentVerifier) scoreCompleteness(wordCount int, title, description string) float64 {
	if wordCount == 0 {
		return 0.0
	}

	// Look for explicit word count requirements in the task description.
	targetWords := cv.extractWordCountTarget(title + " " + description)
	if targetWords == 0 {
		// Default minimum: 100 words for content tasks; scale with budget.
		targetWords = 100
	}

	if wordCount < targetWords/4 {
		return 0.05
	}
	if wordCount < targetWords/2 {
		return 0.2
	}
	ratio := float64(wordCount) / float64(targetWords)
	return clamp(0.3+ratio*0.7, 0, 1)
}

// extractWordCountTarget scans task text for patterns like "500 words", "1000-word",
// "at least 300 words". Returns the first found count; 0 if none.
func (cv *ContentVerifier) extractWordCountTarget(text string) int {
	re := regexp.MustCompile(`\b(\d{2,5})\s*(?:-?\s*word|words\b)`)
	m := re.FindStringSubmatch(strings.ToLower(text))
	if m == nil {
		return 0
	}
	n := 0
	for _, ch := range m[1] {
		n = n*10 + int(ch-'0')
	}
	return n
}

// scoreTopicRelevance measures how many key terms from the task appear in the content.
func (cv *ContentVerifier) scoreTopicRelevance(content, title, description string) float64 {
	terms := contentKeyTerms(title + " " + description)
	if len(terms) == 0 {
		return 0.5
	}
	lower := strings.ToLower(content)
	matched := 0
	for _, term := range terms {
		if strings.Contains(lower, term) {
			matched++
		}
	}
	return clamp(float64(matched)/float64(len(terms)), 0, 1)
}

// scoreFormatting evaluates structural formatting signals.
func (cv *ContentVerifier) scoreFormatting(content string) float64 {
	if content == "" {
		return 0.0
	}
	score := 0.0

	// Markdown headings or section labels (e.g. "Introduction:").
	headingRe := regexp.MustCompile(`(?m)^#{1,3} \w|^[A-Z][A-Za-z\s]{3,30}:\s*$`)
	if len(headingRe.FindAllString(content, -1)) >= 2 {
		score += 0.4
	} else if len(headingRe.FindAllString(content, -1)) == 1 {
		score += 0.2
	}

	// Paragraph breaks — at least two blank lines.
	if strings.Count(content, "\n\n") >= 2 {
		score += 0.3
	} else if strings.Count(content, "\n\n") == 1 {
		score += 0.15
	}

	// No excessive repetition: most distinct trigrams.
	score += cv.diversityBonus(content) * 0.3

	return clamp(score, 0, 1)
}

// diversityBonus returns 0–1 based on the ratio of unique 3-word sequences.
// A higher ratio means less copy-paste repetition.
func (cv *ContentVerifier) diversityBonus(content string) float64 {
	words := strings.Fields(content)
	if len(words) < 6 {
		return 0.5
	}
	total := len(words) - 2
	seen := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		key := words[i] + " " + words[i+1] + " " + words[i+2]
		seen[strings.ToLower(key)] = true
	}
	ratio := float64(len(seen)) / float64(total)
	return clamp(ratio, 0, 1)
}

// splitSentences splits text into sentences on '.', '!', '?' boundaries.
func splitSentences(text string) []string {
	re := regexp.MustCompile(`[^.!?]+[.!?]+`)
	parts := re.FindAllString(text, -1)
	var out []string
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	// If no sentence-final punctuation found, treat content as one sentence.
	if len(out) == 0 {
		out = append(out, text)
	}
	return out
}

// contentKeyTerms extracts meaningful words (≥4 chars, alpha) from the task text,
// excluding common stop words so only domain-relevant terms are matched.
func contentKeyTerms(text string) []string {
	stopWords := map[string]bool{
		"that": true, "this": true, "with": true, "from": true, "have": true,
		"will": true, "your": true, "about": true, "which": true, "their": true,
		"should": true, "write": true, "create": true, "please": true, "make": true,
		"task": true, "need": true, "must": true, "using": true, "provide": true,
		"include": true, "content": true, "text": true, "document": true, "article": true,
	}
	re := regexp.MustCompile(`[a-zA-Z]{4,}`)
	raw := re.FindAllString(strings.ToLower(text), -1)
	seen := make(map[string]bool)
	var out []string
	for _, w := range raw {
		if !stopWords[w] && !seen[w] && !isAllSameRune(w) {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// isAllSameRune returns true if all runes in s are identical (e.g. "aaaa").
func isAllSameRune(s string) bool {
	runes := []rune(s)
	if len(runes) == 0 {
		return false
	}
	first := runes[0]
	for _, r := range runes[1:] {
		if r != first {
			return false
		}
	}
	return true
}

// Compile-time assertion: ContentVerifier must satisfy VerifierInterface.
var _ VerifierInterface = (*ContentVerifier)(nil)
