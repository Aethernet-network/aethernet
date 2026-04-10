// Package roundprogress implements the signed validator progress control plane.
//
// RoundProgress is NOT a DAG event type. It is a signed control-plane protocol
// with a persisted snapshot store. Progress messages travel over a separate
// transport channel. Only final round outcomes (TaskVerificationConsensus) go
// on the DAG.
package roundprogress

// ProgressPhase is the monotonic state machine for validator round progress.
// Phase transitions are forward-only.
type ProgressPhase uint8

const (
	ProgressPhaseAcknowledged ProgressPhase = iota
	ProgressPhaseFetchingBlob
	ProgressPhaseAnalyzing
	ProgressPhaseScorePending
	ProgressPhaseVoteEmitted
	ProgressPhaseAbstained
	ProgressPhaseFailed
)

// String returns a human-readable name for the phase.
func (p ProgressPhase) String() string {
	switch p {
	case ProgressPhaseAcknowledged:
		return "Acknowledged"
	case ProgressPhaseFetchingBlob:
		return "FetchingBlob"
	case ProgressPhaseAnalyzing:
		return "Analyzing"
	case ProgressPhaseScorePending:
		return "ScorePending"
	case ProgressPhaseVoteEmitted:
		return "VoteEmitted"
	case ProgressPhaseAbstained:
		return "Abstained"
	case ProgressPhaseFailed:
		return "Failed"
	default:
		return "Unknown"
	}
}

// IsTerminal returns true for phases that accept no further transitions.
func (p ProgressPhase) IsTerminal() bool {
	return p == ProgressPhaseVoteEmitted || p == ProgressPhaseAbstained || p == ProgressPhaseFailed
}

// ValidTransition returns true if the transition from → to is allowed.
// Phase transitions are monotonic (forward-only). Terminal phases reject
// all transitions.
func ValidTransition(from, to ProgressPhase) bool {
	if from.IsTerminal() {
		return false
	}
	if to <= from {
		return false // no backwards or self-transitions
	}
	// All forward transitions from non-terminal phases are valid.
	// The valid transition graph:
	//   Acknowledged → FetchingBlob | Analyzing | ScorePending | VoteEmitted | Abstained | Failed
	//   FetchingBlob → Analyzing | ScorePending | VoteEmitted | Abstained | Failed
	//   Analyzing    → ScorePending | VoteEmitted | Abstained | Failed
	//   ScorePending → VoteEmitted | Abstained | Failed
	return true
}

// ProgressUpdate is the signed wire message for validator round progress.
// Transported over the control-plane channel (MsgProgressUpdate), NOT the DAG.
type ProgressUpdate struct {
	RoundID            string        `json:"round_id"`
	ValidatorID        string        `json:"validator_id"`
	AnalyzerFamily     string        `json:"analyzer_family"`
	Phase              ProgressPhase `json:"phase"`
	ProgressGeneration uint64        `json:"progress_generation"`
	ProgressEvidence   [32]byte      `json:"progress_evidence"`
	EstimatedReadyUnix int64         `json:"estimated_ready_unix"`
	ReasonCode         uint16        `json:"reason_code"`
	DiagnosticText     string        `json:"diagnostic_text"`
	TimestampUnix      int64         `json:"timestamp_unix"`
	Signature          []byte        `json:"signature"`
}

// RoundProgressSnapshot is the persisted latest state per (round, validator, family).
// This is what the finalizer reads to make adaptive timing decisions.
type RoundProgressSnapshot struct {
	RoundID            string        `json:"round_id"`
	ValidatorID        string        `json:"validator_id"`
	AnalyzerFamily     string        `json:"analyzer_family"`
	CurrentPhase       ProgressPhase `json:"current_phase"`
	PhaseEnteredUnix   int64         `json:"phase_entered_unix"`
	LastUpdateUnix     int64         `json:"last_update_unix"`
	LeaseExpiryUnix    int64         `json:"lease_expiry_unix"`
	ProgressGeneration uint64        `json:"progress_generation"`
	EstimatedReadyUnix int64         `json:"estimated_ready_unix"`
	ReasonCode         uint16        `json:"reason_code"`
	DiagnosticText     string        `json:"diagnostic_text"`
}

// Enumerated reason codes.
const (
	ReasonCodeUnknown              uint16 = 0
	ReasonCodeStartingRound        uint16 = 1
	ReasonCodeFetchingEvidenceBlob uint16 = 2
	ReasonCodeFetchingManifestBlob uint16 = 3
	ReasonCodeAnalyzerWarmup       uint16 = 4
	ReasonCodeAnalyzerRunning      uint16 = 5
	ReasonCodeExternalDBQuery      uint16 = 6
	ReasonCodeAPICallInProgress    uint16 = 7
	ReasonCodeScoring              uint16 = 8
	ReasonCodeVoteEmitted          uint16 = 9
	ReasonCodeBlobUnavailable      uint16 = 10
	ReasonCodeAnalyzerFailure      uint16 = 11
	ReasonCodeAbstained            uint16 = 12
)
