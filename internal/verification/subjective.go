package verification

import "github.com/Aethernet-network/aethernet/internal/evidence"

// SubjectiveRater translates a DeterministicReport into a SubjectiveReport.
// The initial implementation is a straight pass-through: it copies the numeric
// scores computed by the deterministic verifier with Confidence=1.0.
//
// This is a deliberately separate role: future implementations can replace or
// augment the pass-through with LLM-based quality assessment, human review
// scores, or multi-validator averaging without touching DeterministicVerifier
// or ConsensusSufficiencyChecker.
type SubjectiveRater struct{}

// Rate translates det's NumericScores into a SubjectiveReport.
// category and ev are accepted for future use (e.g. LLM prompting) but are
// not used in the current pass-through implementation.
func (sr *SubjectiveRater) Rate(_ string, _ *evidence.Evidence, det *DeterministicReport) *SubjectiveReport {
	if det == nil {
		return &SubjectiveReport{}
	}
	return &SubjectiveReport{
		Relevance:    det.NumericScores["relevance"],
		Completeness: det.NumericScores["completeness"],
		Quality:      det.NumericScores["quality"],
		Overall:      det.NumericScores["overall"],
		// ReasonCodes is populated by ConsensusSufficiencyChecker after Rate returns.
	}
}
