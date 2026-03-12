package verification

import (
	"context"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// InProcessVerifier implements VerificationService by orchestrating three
// roles in sequence:
//
//  1. DeterministicVerifier — structural hard-gate checks + registry scoring
//  2. SubjectiveRater       — translates scores into a SubjectiveReport
//  3. ConsensusSufficiencyChecker — applies the sufficiency decision
//
// The external behaviour is identical to the previous single-function
// implementation: the same inputs produce the same scores and pass/fail
// verdict. TrustProof is always nil — in-process execution has no attestation.
type InProcessVerifier struct {
	det *DeterministicVerifier
	sub SubjectiveRater
	chk ConsensusSufficiencyChecker
}

// NewInProcessVerifier wraps r. Pass nil to use the keyword-verifier fallback.
func NewInProcessVerifier(r *evidence.VerifierRegistry) *InProcessVerifier {
	return &InProcessVerifier{
		det: NewDeterministicVerifier(r),
	}
}

// Verify implements VerificationService. ctx is accepted for interface
// compliance but ignored — all three underlying roles are synchronous.
func (v *InProcessVerifier) Verify(_ context.Context, req VerificationRequest) (*VerificationResult, error) {
	// 1. Deterministic checks + registry scoring.
	// Pass RequiredChecks so the verifier only runs the specified structural
	// gates (backward-compatible: empty RequiredChecks runs all gates).
	detReport := v.det.Verify(req.Category, req.Evidence, req.Title, req.Description, req.Budget, req.RequiredChecks...)

	// 2. Translate deterministic scores into a subjective report.
	subjReport := v.sub.Rate(req.Category, req.Evidence, detReport)

	// 3. Sufficiency decision.
	pv := req.PolicyVersion
	if pv == "" {
		pv = "v1"
	}
	// Pass ContractHints so the checker enforces RequiredChecks and the
	// challenge window when populated from the task's AcceptanceContract.
	sufficient, reasons := v.chk.Check(detReport, subjReport, pv, ContractHints{
		RequiredChecks:      req.RequiredChecks,
		ChallengeWindowSecs: req.ChallengeWindowSecs,
		SubmittedAt:         req.SubmittedAt,
	})

	// Construct the final DeterministicReport: HardGates[0] is the
	// ConsensusSufficiencyChecker's verdict (threshold gate), followed by the
	// structural gates from DeterministicVerifier. This ensures HardGates[0].Pass
	// reflects the authoritative sufficiency decision for the autovalidator.
	//
	// DeterministicVerifier guarantees at least one gate in its output, but we
	// guard with a bounds check here to prevent a panic if the contract is ever
	// relaxed (NEW-8).
	var thresholdDetail string
	if len(detReport.HardGates) > 0 {
		thresholdDetail = detReport.HardGates[0].Detail
	}
	finalGates := []GateResult{{Name: "threshold", Pass: sufficient, Detail: thresholdDetail}}
	if len(detReport.HardGates) > 1 {
		finalGates = append(finalGates, detReport.HardGates[1:]...)
	}

	return &VerificationResult{
		TaskID: req.TaskID,
		DeterministicReport: DeterministicReport{
			HardGates:     finalGates,
			NumericScores: detReport.NumericScores,
		},
		SubjectiveReport: SubjectiveReport{
			Relevance:    subjReport.Relevance,
			Completeness: subjReport.Completeness,
			Quality:      subjReport.Quality,
			Overall:      subjReport.Overall,
			ReasonCodes:  reasons,
		},
		Confidence:    subjReport.Overall,
		PolicyVersion: pv,
		VerifierID:    "in-process",
		Timestamp:     time.Now(),
		TrustProof:    nil,
	}, nil
}

// Compile-time assertion: InProcessVerifier must satisfy VerificationService.
var _ VerificationService = (*InProcessVerifier)(nil)
