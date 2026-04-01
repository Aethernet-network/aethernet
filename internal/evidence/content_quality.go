package evidence

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// qualityProfile holds the multi-dimensional substance assessment of content.
// Each dimension is [0, 1]. The weighted composite is the final quality score.
type qualityProfile struct {
	ArgumentStructure    float64 // presence of claims, reasoning, conclusions
	ClaimDensity         float64 // specific factual/quantitative claims per paragraph
	SourceGrounding      float64 // references to named entities, sources, data
	InformationDensity   float64 // unique content words / total words
	Redundancy           float64 // inverse: lower is better (penalty for repetition)
	LexicalSophistication float64 // vocabulary diversity and technical depth
	Length               float64 // length score (normalized)
	Formatting           float64 // structural formatting (headings, paragraphs)
}

// computeQuality returns the weighted composite quality score.
func (p *qualityProfile) computeQuality() float64 {
	// Weights emphasize substance over form. Argument structure and claim
	// density are the primary discriminators between substantive content and filler.
	q := p.ArgumentStructure*0.25 +
		p.ClaimDensity*0.20 +
		p.SourceGrounding*0.15 +
		p.InformationDensity*0.10 +
		(1.0-p.Redundancy)*0.10 + // invert: less redundancy → higher quality
		p.LexicalSophistication*0.05 +
		p.Length*0.10 +
		p.Formatting*0.05
	return clamp(q, 0, 1)
}

// analyzeContentQuality computes a multi-dimensional quality profile for content.
// This replaces the old (language + formatting) / 2 formula which only measured
// structural formatting and couldn't distinguish substantive research from filler.
func analyzeContentQuality(content string) *qualityProfile {
	if content == "" {
		return &qualityProfile{Redundancy: 1.0} // max redundancy = zero quality
	}

	paragraphs := splitParagraphs(content)
	words := strings.Fields(content)
	wordCount := len(words)

	return &qualityProfile{
		ArgumentStructure:    scoreArgumentStructure(content, paragraphs),
		ClaimDensity:         scoreClaimDensity(content, paragraphs),
		SourceGrounding:      scoreSourceGrounding(content),
		InformationDensity:   scoreInformationDensity(words),
		Redundancy:           scoreRedundancy(paragraphs),
		LexicalSophistication: scoreLexicalSophistication(words),
		Length:               clamp(float64(wordCount)/500.0, 0, 1),
		Formatting:           scoreFormattingQuality(content),
	}
}

// scoreArgumentStructure measures presence of reasoning markers:
// claims ("we found", "the data shows"), transitions ("however", "therefore"),
// and conclusions ("in conclusion", "this demonstrates").
func scoreArgumentStructure(content string, paragraphs []string) float64 {
	lower := strings.ToLower(content)

	claimMarkers := []string{
		"we found", "the data shows", "results indicate", "analysis reveals",
		"evidence suggests", "this demonstrates", "findings show",
		"according to", "research shows", "studies indicate",
		"the key finding", "importantly", "notably", "significantly",
		"this means", "in practice", "the implication",
	}

	transitionMarkers := []string{
		"however", "therefore", "consequently", "furthermore", "moreover",
		"in contrast", "nevertheless", "on the other hand", "as a result",
		"specifically", "for example", "in particular", "additionally",
		"while", "although", "despite",
	}

	conclusionMarkers := []string{
		"in conclusion", "to summarize", "in summary", "overall",
		"the key takeaway", "this suggests", "these findings",
		"taken together", "this analysis shows",
	}

	claims := countPhraseMatches(lower, claimMarkers)
	transitions := countPhraseMatches(lower, transitionMarkers)
	conclusions := countPhraseMatches(lower, conclusionMarkers)

	// Normalize: expect at least 3 claims, 4 transitions, 1 conclusion in good content.
	claimScore := clamp(float64(claims)/3.0, 0, 1)
	transitionScore := clamp(float64(transitions)/4.0, 0, 1)
	conclusionScore := clamp(float64(conclusions)/1.0, 0, 1)

	// Distribution bonus: markers spread across paragraphs, not clustered.
	distribution := markerDistribution(paragraphs, append(append(claimMarkers, transitionMarkers...), conclusionMarkers...))

	return claimScore*0.30 + transitionScore*0.30 + conclusionScore*0.15 + distribution*0.25
}

// scoreClaimDensity measures specific factual/quantitative claims per paragraph.
func scoreClaimDensity(content string, paragraphs []string) float64 {
	if len(paragraphs) == 0 {
		return 0
	}

	totalClaims := 0
	for _, para := range paragraphs {
		if len(strings.Fields(para)) < 5 {
			continue // skip tiny paragraphs (headings, etc.)
		}
		sentences := splitSentences(para)
		for _, sent := range sentences {
			if hasQuantitativeClaim(sent) || hasProperNoun(sent) || hasTemporalRef(sent) || hasTechnicalTerm(sent) {
				totalClaims++
			}
		}
	}

	// Good content: at least 1 specific claim per substantive paragraph.
	substantiveParas := 0
	for _, p := range paragraphs {
		if len(strings.Fields(p)) >= 10 {
			substantiveParas++
		}
	}
	if substantiveParas == 0 {
		return 0
	}

	density := float64(totalClaims) / float64(substantiveParas)
	return clamp(density/3.0, 0, 1) // target: 3 claims per paragraph
}

// scoreSourceGrounding measures references to named entities, URLs, citations.
func scoreSourceGrounding(content string) float64 {
	entities := extractUniqueEntities(content)
	urlCount := len(regexp.MustCompile(`https?://\S+`).FindAllString(content, -1))
	citationCount := len(regexp.MustCompile(`\(\d{4}\)|\[\d+\]|et al\.`).FindAllString(content, -1))

	entityScore := clamp(float64(entities)/5.0, 0, 1) // target: 5+ named entities
	urlScore := clamp(float64(urlCount)/2.0, 0, 1)     // target: 2+ URLs
	citScore := clamp(float64(citationCount)/2.0, 0, 1)

	return entityScore*0.50 + urlScore*0.25 + citScore*0.25
}

// scoreInformationDensity measures unique content words / total words.
// Higher = more diverse vocabulary, less filler.
func scoreInformationDensity(words []string) float64 {
	if len(words) < 10 {
		return 0
	}
	contentWords := contentWordSet(words)
	ratio := float64(len(contentWords)) / float64(len(words))
	// Good writing: 40-60% content word ratio. Scale linearly.
	return clamp(ratio/0.55, 0, 1)
}

// scoreRedundancy measures cross-paragraph similarity. High similarity = high redundancy.
func scoreRedundancy(paragraphs []string) float64 {
	if len(paragraphs) < 2 {
		return 0 // can't measure redundancy with one paragraph
	}

	totalSim := 0.0
	pairs := 0
	for i := 0; i < len(paragraphs); i++ {
		for j := i + 1; j < len(paragraphs); j++ {
			if len(strings.Fields(paragraphs[i])) < 5 || len(strings.Fields(paragraphs[j])) < 5 {
				continue
			}
			totalSim += jaccardSimilarity(
				contentWordSet(strings.Fields(paragraphs[i])),
				contentWordSet(strings.Fields(paragraphs[j])),
			)
			pairs++
		}
	}
	if pairs == 0 {
		return 0
	}
	return totalSim / float64(pairs)
}

// scoreLexicalSophistication measures vocabulary diversity via type-token ratio
// and presence of technical/domain terms.
func scoreLexicalSophistication(words []string) float64 {
	if len(words) < 10 {
		return 0
	}

	// Type-token ratio (unique words / total words), adjusted for text length.
	// Longer texts naturally have lower TTR, so we use root TTR (Guiraud's index).
	unique := make(map[string]bool, len(words))
	for _, w := range words {
		unique[strings.ToLower(w)] = true
	}
	guiraud := float64(len(unique)) / math.Sqrt(float64(len(words)))
	ttrScore := clamp(guiraud/8.0, 0, 1) // good writing: Guiraud ≈ 6-10

	// Technical term density.
	techCount := 0
	for _, w := range words {
		if hasTechnicalTerm(w) {
			techCount++
		}
	}
	techScore := clamp(float64(techCount)/float64(len(words))*20, 0, 1) // target: 5%+ technical terms

	return ttrScore*0.60 + techScore*0.40
}

// scoreFormattingQuality evaluates structural formatting (headings, lists, code blocks).
func scoreFormattingQuality(content string) float64 {
	score := 0.0

	headingRe := regexp.MustCompile(`(?m)^#{1,3} \w|^[A-Z][A-Za-z\s]{3,30}:\s*$`)
	headings := len(headingRe.FindAllString(content, -1))
	if headings >= 3 {
		score += 0.40
	} else if headings >= 1 {
		score += 0.20
	}

	if strings.Count(content, "\n\n") >= 3 {
		score += 0.30
	} else if strings.Count(content, "\n\n") >= 1 {
		score += 0.15
	}

	// Lists (bullet points, numbered items).
	listRe := regexp.MustCompile(`(?m)^[\s]*[-*•]\s|^[\s]*\d+[.)]\s`)
	if len(listRe.FindAllString(content, -1)) >= 2 {
		score += 0.15
	}

	// Code blocks.
	if strings.Contains(content, "```") {
		score += 0.15
	}

	return clamp(score, 0, 1)
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func containsAnyPhrase(lower string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func countPhraseMatches(lower string, phrases []string) int {
	count := 0
	for _, p := range phrases {
		count += strings.Count(lower, p)
	}
	return count
}

func markerDistribution(paragraphs []string, markers []string) float64 {
	if len(paragraphs) <= 1 {
		return 0.5
	}
	parasWithMarkers := 0
	for _, para := range paragraphs {
		lower := strings.ToLower(para)
		if containsAnyPhrase(lower, markers) {
			parasWithMarkers++
		}
	}
	return clamp(float64(parasWithMarkers)/float64(len(paragraphs)), 0, 1)
}

func hasQuantitativeClaim(sentence string) bool {
	return regexp.MustCompile(`\d+[%$€£¥]|\d+\.\d+|\d{2,}|increased|decreased|grew|declined|rose|fell`).MatchString(sentence)
}

func hasProperNoun(sentence string) bool {
	words := strings.Fields(sentence)
	for i, w := range words {
		if i == 0 {
			continue // skip sentence-initial caps
		}
		if len(w) >= 2 && unicode.IsUpper(rune(w[0])) && !isCommonWord(strings.ToLower(w)) {
			return true
		}
	}
	return false
}

func isCommonWord(w string) bool {
	common := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
		"were": true, "be": true, "been": true, "being": true, "have": true, "has": true,
		"had": true, "do": true, "does": true, "did": true, "will": true, "would": true,
		"could": true, "should": true, "may": true, "might": true, "shall": true,
		"i": true, "we": true, "you": true, "he": true, "she": true, "it": true, "they": true,
		"this": true, "that": true, "these": true, "those": true,
	}
	return common[w]
}

func hasTemporalRef(sentence string) bool {
	return regexp.MustCompile(`\b(20\d{2}|19\d{2}|january|february|march|april|may|june|july|august|september|october|november|december|q[1-4]|quarter|year|month|week)\b`).MatchString(strings.ToLower(sentence))
}

func hasTechnicalTerm(word string) bool {
	lower := strings.ToLower(word)
	// Length-based heuristic: longer words tend to be more technical.
	if len(lower) >= 10 {
		return true
	}
	technical := map[string]bool{
		"algorithm": true, "framework": true, "analysis": true, "methodology": true,
		"infrastructure": true, "implementation": true, "architecture": true,
		"protocol": true, "mechanism": true, "optimization": true, "parameter": true,
		"benchmark": true, "performance": true, "scalability": true, "latency": true,
		"throughput": true, "consensus": true, "cryptographic": true, "deterministic": true,
		"api": true, "sdk": true, "database": true, "pipeline": true, "deployment": true,
	}
	return technical[lower]
}

func extractUniqueEntities(content string) int {
	// Count capitalized multi-word sequences (likely proper nouns/entity names).
	re := regexp.MustCompile(`[A-Z][a-z]+(?:\s+[A-Z][a-z]+)+`)
	matches := re.FindAllString(content, -1)
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		seen[m] = true
	}
	return len(seen)
}

func splitParagraphs(content string) []string {
	parts := strings.Split(content, "\n\n")
	var out []string
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 && content != "" {
		out = append(out, content)
	}
	return out
}

func contentWordSet(words []string) map[string]bool {
	stops := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
		"were": true, "be": true, "been": true, "have": true, "has": true, "had": true,
		"do": true, "does": true, "did": true, "will": true, "would": true, "can": true,
		"could": true, "should": true, "may": true, "might": true, "to": true, "of": true,
		"in": true, "for": true, "on": true, "with": true, "at": true, "by": true,
		"from": true, "as": true, "or": true, "and": true, "but": true, "not": true,
		"if": true, "then": true, "than": true, "so": true, "it": true, "its": true,
		"this": true, "that": true, "these": true, "those": true, "we": true, "they": true,
		"he": true, "she": true, "you": true, "i": true, "my": true, "your": true,
		"his": true, "her": true, "our": true, "their": true, "what": true, "which": true,
	}
	set := make(map[string]bool, len(words)/2)
	for _, w := range words {
		lower := strings.ToLower(w)
		if len(lower) >= 3 && !stops[lower] {
			set[lower] = true
		}
	}
	return set
}

func jaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
