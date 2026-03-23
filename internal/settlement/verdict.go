// Package settlement implements the canonical settlement application layer.
//
// This is Core Protocol code. Settlement events are created by the consensus
// finalization callback and applied by the SettlementApplicator. No other
// code path may mutate ledger state for Transfer, Generation, or Task events.
//
// Layer: Core Protocol — may import Core packages only.
package settlement

import "sort"

// SettlementVerdict is the canonical outcome of a consensus round.
type SettlementVerdict string

const (
	VerdictAccepted SettlementVerdict = "accepted"
	VerdictRejected SettlementVerdict = "rejected"
	VerdictAdjusted SettlementVerdict = "adjusted"
)

// VerificationVotePayload is the body of an EventTypeVerificationVote event.
// Each validator emits one vote per pending event. The vote propagates via
// DAG sync and is consumed by the consensus VotingRound on each node.
type VerificationVotePayload struct {
	TargetEventID string `json:"target_event_id"`
	VoterID       string `json:"voter_id"`
	Verdict       string `json:"verdict"` // SettlementVerdict as string
	VerifiedValue uint64 `json:"verified_value"`
	Timestamp     int64  `json:"timestamp"` // Unix seconds
}

// VoterAttestation records a single voter's contribution to a consensus
// decision. Used in SettlementPayload.Attestations which is sorted by
// VoterID for deterministic serialization.
type VoterAttestation struct {
	VoterID string `json:"voter_id"`
	Verdict string `json:"verdict"`
	Weight  uint64 `json:"weight"` // matches VotingRound.computeWeight: (rep * stake) / 10000
}

// SettlementPayload is the body of an EventTypeSettlement event. Created
// exclusively by the consensus finalization callback when a VotingRound
// reaches supermajority. This is the canonical record that authorizes the
// SettlementApplicator to mutate ledger state.
type SettlementPayload struct {
	TargetEventID  string             `json:"target_event_id"`
	Verdict        string             `json:"verdict"` // SettlementVerdict as string
	VerifiedValue  uint64             `json:"verified_value"`
	ConsensusRound uint64             `json:"consensus_round"` // VoteRecord.FinalOrder
	Attestations   []VoterAttestation `json:"attestations"`    // sorted by VoterID
}

// SortAttestations sorts the Attestations slice by VoterID for deterministic
// serialization. Must be called before marshaling SettlementPayload.
func (sp *SettlementPayload) SortAttestations() {
	sort.Slice(sp.Attestations, func(i, j int) bool {
		return sp.Attestations[i].VoterID < sp.Attestations[j].VoterID
	})
}

// TaskSettlementPayload is the body of an EventTypeTaskSettlement event.
// Contains all canonical inputs required for deterministic settlement
// application by the SettlementApplicator.
type TaskSettlementPayload struct {
	TaskID         string  `json:"task_id"`
	PosterID       string  `json:"poster_id"`
	ClaimerID      string  `json:"claimer_id"`
	Budget         uint64  `json:"budget"`
	AcceptanceHash string  `json:"acceptance_hash"`
	EvidenceHash   string  `json:"evidence_hash"`
	Category       string  `json:"category"`
	Score          float64 `json:"score"`
	HoldGeneration bool    `json:"hold_generation"`
}
