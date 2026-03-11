package evidence

// VerifierRegistry maps task categories to the appropriate VerifierInterface
// implementation. Unknown categories fall back to the KeywordVerifier.
//
// Category mappings:
//   code, code-review, technical, security → CodeVerifier
//   data, data-analysis, data-validation, research → DataVerifier
//   writing, documentation, translation, content → ContentVerifier
//   everything else → KeywordVerifier (keyword overlap, backward-compatible)
type VerifierRegistry struct {
	verifiers map[string]VerifierInterface
	fallback  *KeywordVerifier

	// Per-category pass thresholds — override the individual verifiers' built-in
	// constants. Initialised to the package defaults (0.5) by NewVerifierRegistry.
	codeThresh    float64
	dataThresh    float64
	contentThresh float64
}

// NewVerifierRegistry returns a fully-populated VerifierRegistry with all
// built-in verifiers registered.
func NewVerifierRegistry() *VerifierRegistry {
	code := &CodeVerifier{}
	data := &DataVerifier{}
	content := &ContentVerifier{}

	r := &VerifierRegistry{
		verifiers:     make(map[string]VerifierInterface),
		fallback:      NewKeywordVerifier(),
		codeThresh:    0.25,
		dataThresh:    0.25,
		contentThresh: 0.25,
	}

	for _, cat := range []string{"code", "code-review", "technical", "security"} {
		r.verifiers[cat] = code
	}
	for _, cat := range []string{"data", "data-analysis", "data-validation", "research"} {
		r.verifiers[cat] = data
	}
	for _, cat := range []string{"writing", "documentation", "translation", "content"} {
		r.verifiers[cat] = content
	}

	return r
}

// SetPassThresholds overrides the per-category pass thresholds used when
// determining the boolean verdict returned by Verify. Values should be in
// [0.0, 1.0]. Call before the auto-validator starts processing tasks.
func (r *VerifierRegistry) SetPassThresholds(code, data, content float64) {
	r.codeThresh = code
	r.dataThresh = data
	r.contentThresh = content
}

// thresholdFor returns the pass threshold for the given category.
func (r *VerifierRegistry) thresholdFor(category string) float64 {
	switch category {
	case "code", "code-review", "technical", "security":
		return r.codeThresh
	case "data", "data-analysis", "data-validation", "research":
		return r.dataThresh
	case "writing", "documentation", "translation", "content":
		return r.contentThresh
	default:
		// KeywordVerifier fallback — use the highest of the three thresholds
		// so that unknown categories are not easier to pass than known ones.
		t := r.codeThresh
		if r.dataThresh > t {
			t = r.dataThresh
		}
		if r.contentThresh > t {
			t = r.contentThresh
		}
		return t
	}
}

// Verify routes the call to the appropriate verifier for the given category.
// If no verifier is registered for the category, the KeywordVerifier is used.
// The boolean verdict is determined by comparing score.Overall against the
// registry's per-category threshold (set via SetPassThresholds), not the
// individual verifier's internal threshold.
func (r *VerifierRegistry) Verify(ev *Evidence, taskTitle, taskDescription string, budget uint64, category string) (*Score, bool) {
	var score *Score
	if v, ok := r.verifiers[category]; ok {
		score, _ = v.Verify(ev, taskTitle, taskDescription, budget)
	} else {
		score, _ = r.fallback.Verify(ev, taskTitle, taskDescription, budget)
	}
	if score == nil {
		return nil, false
	}
	return score, score.Overall >= r.thresholdFor(category)
}
