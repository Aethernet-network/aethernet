// Package evidence — verifier_v2.go defines the VerifierInterface contract and
// the KeywordVerifier implementation, making the evidence verification layer
// pluggable for mainnet use cases.
//
// On testnet the KeywordVerifier wraps the existing Verifier unchanged.
// On mainnet, richer implementations (LLM judges, crowd-sourced attestation)
// can satisfy VerifierInterface and be swapped in without changing call sites.
package evidence

// VerifierInterface defines the contract for evidence verification implementations.
// Any type that can assess whether submitted evidence demonstrates successful
// task completion should satisfy this interface.
type VerifierInterface interface {
	// Verify assesses the evidence against the task description and budget.
	// Returns a Score and true when the evidence passes the quality threshold
	// (Score.Overall >= PassThreshold), or a Score and false when it does not.
	// Callers treat false as "hold for manual review" rather than "reject".
	Verify(ev *Evidence, taskTitle, taskDescription string, budget uint64) (*Score, bool)
}

// KeywordVerifier is the default VerifierInterface implementation on testnet.
// It wraps the existing Verifier with no additional logic, allowing call sites
// that accept a VerifierInterface to use the standard testnet verifier.
type KeywordVerifier struct {
	inner *Verifier
}

// NewKeywordVerifier returns a KeywordVerifier backed by a freshly-allocated Verifier.
func NewKeywordVerifier() *KeywordVerifier {
	return &KeywordVerifier{inner: NewVerifier()}
}

// Verify implements VerifierInterface by delegating to the inner Verifier.
func (kv *KeywordVerifier) Verify(ev *Evidence, taskTitle, taskDescription string, budget uint64) (*Score, bool) {
	return kv.inner.Verify(ev, taskTitle, taskDescription, budget)
}

// Compile-time assertion: KeywordVerifier must satisfy VerifierInterface.
var _ VerifierInterface = (*KeywordVerifier)(nil)
