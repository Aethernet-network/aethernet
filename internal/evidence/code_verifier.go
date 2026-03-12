package evidence

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

// CodeVerifier implements VerifierInterface for task categories that require
// code output: "code", "code-review", "technical", "security".
//
// It evaluates four deterministic dimensions:
//  1. Syntax validity     (25%): does the code parse without errors?
//  2. Structural completeness (30%): does it contain real functions and logic?
//  3. Task relevance      (25%): do identifiers match the task description?
//  4. Quality signals     (20%): error handling, comments, type annotations?
//
// Pass threshold: 0.5 (lower than the default 0.6 because static analysis is
// harder to satisfy than keyword matching while being much harder to game).
// The VerifierRegistry applies a higher threshold (0.65) for production use.
type CodeVerifier struct{}

const codePassThreshold = 0.50

// Verify implements VerifierInterface.
func (cv *CodeVerifier) Verify(ev *Evidence, taskTitle, taskDescription string, budget uint64) (*Score, bool) {
	if ev == nil {
		return &Score{}, false
	}
	content := strings.TrimSpace(ev.Summary)
	if ev.OutputPreview != "" {
		content = strings.TrimSpace(content + "\n" + ev.OutputPreview)
	}

	syntax := cv.scoreSyntax(content)
	completeness := cv.scoreCompleteness(content)
	relevance := cv.scoreRelevance(content, taskTitle, taskDescription)
	quality := cv.scoreQuality(content)

	overall := syntax*0.25 + completeness*0.30 + relevance*0.25 + quality*0.20

	score := &Score{
		Relevance:    relevance,
		Completeness: completeness,
		Quality:      (syntax + quality) / 2, // syntax validity + quality signals → Quality field
		Overall:      overall,
	}
	return score, overall >= codePassThreshold
}

// scoreSyntax attempts to parse the content as a known language.
// Returns 1.0 for valid syntax, 0.5 for plausible-but-errored, 0.0–0.3 for other text.
func (cv *CodeVerifier) scoreSyntax(content string) float64 {
	if content == "" {
		return 0.0
	}

	// Try Go parsing first.
	if cv.looksLikeGo(content) {
		return cv.parseGo(content)
	}

	// Try JSON parsing.
	if looksLikeJSON(content) {
		var v interface{}
		if json.Unmarshal([]byte(content), &v) == nil {
			return 1.0
		}
		return 0.4
	}

	// Structural heuristics for Python, JS, etc.
	return cv.heuristicSyntax(content)
}

// looksLikeGo returns true when at least two Go-specific constructs are present.
func (cv *CodeVerifier) looksLikeGo(content string) bool {
	indicators := []string{"package ", "func ", ":= ", "var ", `import "`, "import ("}
	matches := 0
	for _, ind := range indicators {
		if strings.Contains(content, ind) {
			matches++
		}
	}
	return matches >= 2
}

// parseGo tries to parse content as a complete Go source file, then as a
// snippet wrapped in a package declaration.
func (cv *CodeVerifier) parseGo(content string) float64 {
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", content, parser.AllErrors); err == nil {
		return 1.0
	}
	// Wrap common snippet form in a package so the parser can handle it.
	wrapped := "package tmp\n" + content
	if _, err := parser.ParseFile(fset, "", wrapped, 0); err == nil {
		return 0.9
	}
	// Has Go-like structure but parse errors — partial credit.
	return 0.5
}

// heuristicSyntax scores structural validity for brace-delimited languages (JS, Java, C…).
func (cv *CodeVerifier) heuristicSyntax(content string) float64 {
	balance := 0
	hasBlocks := false
	for _, ch := range content {
		switch ch {
		case '{', '(':
			balance++
			hasBlocks = true
		case '}', ')':
			balance--
		}
	}
	if !hasBlocks {
		return cv.scorePythonLike(content)
	}
	switch {
	case balance == 0:
		return 0.9
	case balance < 3:
		return 0.6
	default:
		return 0.3
	}
}

// scorePythonLike scores Python-style (indent-based) code.
func (cv *CodeVerifier) scorePythonLike(content string) float64 {
	pyKeywords := []string{"def ", "class ", "import ", "from ", "return ", "print(", "if __name__"}
	matches := 0
	for _, kw := range pyKeywords {
		if strings.Contains(content, kw) {
			matches++
		}
	}
	switch {
	case matches == 0:
		return 0.2
	case matches >= 3:
		return 0.85
	default:
		return 0.4 + float64(matches)*0.15
	}
}

// scoreCompleteness evaluates whether the output contains substantive code:
// at least 5 non-empty non-comment lines and at least one function/block definition.
func (cv *CodeVerifier) scoreCompleteness(content string) float64 {
	if content == "" {
		return 0.0
	}
	lines := strings.Split(content, "\n")
	nonEmpty := 0
	functionCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip pure comment lines.
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "*") {
			continue
		}
		nonEmpty++
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "func ") ||
			strings.HasPrefix(lower, "def ") ||
			strings.HasPrefix(lower, "function ") ||
			strings.HasPrefix(lower, "async function ") ||
			strings.HasPrefix(lower, "class ") ||
			strings.Contains(lower, ") {") ||
			strings.Contains(lower, ") error") {
			functionCount++
		}
	}

	lineScore := clamp(float64(nonEmpty)/5.0, 0, 1)
	funcScore := 0.0
	if functionCount >= 1 {
		funcScore = clamp(float64(functionCount)/3.0, 0, 1)
	}
	return lineScore*0.5 + funcScore*0.5
}

// scoreRelevance measures how well code identifiers align with the task description.
func (cv *CodeVerifier) scoreRelevance(content, title, description string) float64 {
	terms := codeTerms(strings.ToLower(title + " " + description))
	if len(terms) == 0 {
		return 0.5
	}
	contentLower := strings.ToLower(content)
	matches := 0
	for _, term := range terms {
		if strings.Contains(contentLower, term) {
			matches++
		}
	}
	return clamp(float64(matches)/float64(len(terms)), 0, 1)
}

// scoreQuality checks for quality markers: error handling, comments, type annotations.
func (cv *CodeVerifier) scoreQuality(content string) float64 {
	score := 0.0
	lower := strings.ToLower(content)

	// Error handling.
	for _, p := range []string{"if err != nil", "try:", "except ", "catch", "throw ", "panic(", ".err()"} {
		if strings.Contains(lower, p) || strings.Contains(content, "if err") {
			score += 0.25
			break
		}
	}
	// Comments / documentation.
	for _, p := range []string{"//", "/*", "# ", `"""`, "/**"} {
		if strings.Contains(content, p) {
			score += 0.25
			break
		}
	}
	// Type annotations or type-safe constructs.
	for _, p := range []string{": int", ": str", ": bool", ": error", ": string", "type ", "interface{", "struct{", "[]", "map["} {
		if strings.Contains(content, p) {
			score += 0.25
			break
		}
	}
	// Tests or validation.
	for _, p := range []string{"func Test", "t.Error", "assert", "expect(", "describe("} {
		if strings.Contains(content, p) {
			score += 0.25
			break
		}
	}
	return clamp(score, 0, 1)
}

// codeTerms extracts meaningful technical identifiers (≥4 chars, alpha-start) from text.
func codeTerms(text string) []string {
	re := regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_]{3,}`)
	raw := re.FindAllString(text, -1)
	seen := make(map[string]bool)
	var out []string
	for _, t := range raw {
		lt := strings.ToLower(t)
		if !seen[lt] {
			seen[lt] = true
			out = append(out, lt)
		}
	}
	return out
}

// looksLikeJSON returns true when content appears to be a JSON object or array.
func looksLikeJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Compile-time assertion: CodeVerifier must satisfy VerifierInterface.
var _ VerifierInterface = (*CodeVerifier)(nil)
