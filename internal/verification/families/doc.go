// Package families provides the four bootstrap analyzer families for
// AetherNet's multi-validator task verification consensus.
//
// Each family represents a structurally independent verification methodology
// with genuinely uncorrelated failure modes:
//
//   - deterministic_heuristic: word count, structure, keyword matching
//   - statistical_structural: entropy, sentence variety, citation density
//   - embedding_similarity: bag-of-words TF-IDF cosine similarity
//   - llm_semantic: LLM API with structured evaluation prompt
//
// The diversity floor enforcement in verification rounds counts distinct
// FamilyIDs. Each family here registers as a distinct Family entry.
package families
