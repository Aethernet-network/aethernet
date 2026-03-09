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
}

// NewVerifierRegistry returns a fully-populated VerifierRegistry with all
// built-in verifiers registered.
func NewVerifierRegistry() *VerifierRegistry {
	code := &CodeVerifier{}
	data := &DataVerifier{}
	content := &ContentVerifier{}

	r := &VerifierRegistry{
		verifiers: make(map[string]VerifierInterface),
		fallback:  NewKeywordVerifier(),
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

// Verify routes the call to the appropriate verifier for the given category.
// If no verifier is registered for the category, the KeywordVerifier is used.
func (r *VerifierRegistry) Verify(ev *Evidence, taskTitle, taskDescription string, budget uint64, category string) (*Score, bool) {
	if v, ok := r.verifiers[category]; ok {
		return v.Verify(ev, taskTitle, taskDescription, budget)
	}
	return r.fallback.Verify(ev, taskTitle, taskDescription, budget)
}
