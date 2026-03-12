// Package verification defines the VerificationService interface and its
// associated request/result types. It is the stable seam between the
// auto-validator and the verification implementation stack.
//
// Layer 2 (this package) handles verification execution. Layer 1 (policy
// evaluation) and Layer 3 (trust proof validation) will be added in
// subsequent iterations without modifying this package.
//
// This package is L3 (Application layer) — it wraps internal/evidence and
// is imported by internal/autovalidator and cmd/node.
package verification

import (
	"context"
	"time"

	"github.com/Aethernet-network/aethernet/internal/evidence"
)

// GateResult is a single named binary check within a DeterministicReport.
type GateResult struct {
	Name   string
	Pass   bool
	Detail string
}

// DeterministicReport holds objective, reproducible checks derived from the
// evidence. HardGates are binary pass/fail tests; NumericScores are continuous
// metrics used for ranking and analytics.
type DeterministicReport struct {
	HardGates     []GateResult
	NumericScores map[string]float64
}

// SubjectiveReport holds continuous quality scores computed by the verifier.
// Fields mirror evidence.Score so callers can map back without loss.
type SubjectiveReport struct {
	Relevance    float64
	Completeness float64
	Quality      float64
	Overall      float64
	ReasonCodes  []string
}

// TrustProof carries attestation material that binds a VerificationResult to
// a specific execution environment. Nil for in-process verifiers; populated
// for TEE or remote attestation flows.
type TrustProof struct {
	ProofType   string // "none", "software-signature", "hardware-attestation"
	Platform    string
	Measurement []byte
	Signature   []byte
}

// VerificationRequest is the input to VerificationService.Verify.
type VerificationRequest struct {
	TaskID   string
	Category string

	// PolicyVersion selects the verification policy to apply. Defaults to "v1"
	// when empty.
	PolicyVersion string

	Title       string
	Description string
	Budget      uint64
	Evidence    *evidence.Evidence

	// ConfidentialityMode controls whether evidence content may be logged or
	// forwarded to remote verifiers. Defaults to "public" when empty.
	ConfidentialityMode string
}

// VerificationResult is the output of VerificationService.Verify.
type VerificationResult struct {
	TaskID              string
	DeterministicReport DeterministicReport
	SubjectiveReport    SubjectiveReport
	Confidence          float64
	PolicyVersion       string
	VerifierID          string
	Timestamp           time.Time
	TrustProof          *TrustProof // nil for in-process verifiers
}

// VerificationService is the interface that any evidence-assessment backend
// must implement. Implementations must be safe for concurrent use.
type VerificationService interface {
	Verify(ctx context.Context, req VerificationRequest) (*VerificationResult, error)
}
