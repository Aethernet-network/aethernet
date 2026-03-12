package verification

import (
	"context"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// InProcessVerifier implements VerificationService by delegating to the
// existing evidence.VerifierRegistry. It replicates the nil-safe fallback
// logic of autovalidator.verifyEvidence() so behaviour is identical whether
// the call comes through the service interface or the legacy direct path.
//
// TrustProof is always nil — in-process execution has no attestation material.
type InProcessVerifier struct {
	registry *evidence.VerifierRegistry
}

// NewInProcessVerifier wraps r. Pass nil to use the keyword-verifier fallback
// (same behaviour as calling autovalidator.verifyEvidence with no registry).
func NewInProcessVerifier(r *evidence.VerifierRegistry) *InProcessVerifier {
	return &InProcessVerifier{registry: r}
}

// Verify implements VerificationService. ctx is accepted for interface
// compliance but ignored — the underlying evidence verifiers are synchronous.
func (v *InProcessVerifier) Verify(_ context.Context, req VerificationRequest) (*VerificationResult, error) {
	var score *evidence.Score
	var passed bool

	if v.registry != nil {
		score, passed = v.registry.Verify(req.Evidence, req.Title, req.Description, req.Budget, req.Category)
	} else {
		score, passed = evidence.NewVerifier().Verify(req.Evidence, req.Title, req.Description, req.Budget)
	}
	if score == nil {
		score = &evidence.Score{}
	}

	pv := req.PolicyVersion
	if pv == "" {
		pv = "v1"
	}

	return &VerificationResult{
		TaskID: req.TaskID,
		DeterministicReport: DeterministicReport{
			HardGates: []GateResult{
				{Name: "threshold", Pass: passed},
			},
			NumericScores: map[string]float64{
				"relevance":    score.Relevance,
				"completeness": score.Completeness,
				"quality":      score.Quality,
				"overall":      score.Overall,
			},
		},
		SubjectiveReport: SubjectiveReport{
			Relevance:    score.Relevance,
			Completeness: score.Completeness,
			Quality:      score.Quality,
			Overall:      score.Overall,
		},
		Confidence:    score.Overall,
		PolicyVersion: pv,
		VerifierID:    "in-process",
		Timestamp:     time.Now(),
		TrustProof:    nil,
	}, nil
}

// Compile-time assertion: InProcessVerifier must satisfy VerificationService.
var _ VerificationService = (*InProcessVerifier)(nil)
