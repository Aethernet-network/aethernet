// Package identity implements AetherNet's capability-based agent identity system.
//
// # Philosophy
//
// In traditional blockchain systems an agent is an address — a public key hash.
// Identity is static: you are your key, nothing more.
//
// In AetherNet, an agent is its track record. The CapabilityFingerprint is a
// living document that grows richer every time an agent completes verified work.
// It encodes not just who an agent is, but what it can demonstrably do and how
// much the network trusts it to operate autonomously under Optimistic Capability
// Settlement (OCS).
//
// This distinction matters for OCS: before approving an optimistic transaction,
// a node queries the Registry for the transacting agent's fingerprint to decide
// whether to extend provisional credit. An agent with a rich fingerprint — high
// reputation, deep capability evidence, significant staked value — earns a higher
// OptimisticTrustLimit, allowing it to move larger amounts without waiting for
// full settlement confirmation.
//
// # Hash integrity
//
// Every CapabilityFingerprint carries a FingerprintHash: the SHA-256 of its
// canonical fields (all fields except FingerprintHash itself). The Registry
// recomputes this hash on every mutation, maintaining the invariant that
// stored fingerprints always have a valid, current hash. This allows peers
// to detect stale or tampered fingerprints during network sync without
// re-executing the full update history.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Aethernet-network/aethernet/internal/crypto"
)

// minTrustLimit is the floor for OptimisticTrustLimit. New agents and agents that
// have suffered failures can never be trusted below this amount — some minimum
// runway is needed for them to begin rebuilding reputation.
const minTrustLimit uint64 = 1000

// maxReputation is the ceiling for ReputationScore in basis points (100% = 10000).
const maxReputation uint64 = 10000

// Capability represents a demonstrated skill domain of an agent, backed by
// on-chain evidence from verified task completions and attestations.
//
// Capability domains use a dot-notation hierarchy (e.g. "nlp.summarization",
// "code.generation") to allow both exact matching and prefix-based queries
// when OCS is deciding whether an agent is suitable for a given task type.
type Capability struct {
	// Domain identifies the skill area using dot-notation hierarchy.
	Domain string `json:"domain"`

	// Confidence is the network's current trust level in this agent for this domain,
	// expressed in basis points (0 = no confidence, 10000 = maximum confidence).
	// Updated by attestations and verifications from the OCS layer, not directly
	// by task completions — evidence accumulates through EvidenceCount first.
	Confidence uint64 `json:"confidence"`

	// EvidenceCount is the number of verified task completions for this domain.
	// Incremented by RecordTaskCompletion. Feeds into the OCS trust calculation
	// that eventually raises Confidence via attestation.
	EvidenceCount uint64 `json:"evidence_count"`

	// LastVerified is the wall-clock time of the most recent verified completion
	// in this domain. Allows the network to detect and discount stale capability
	// claims where an agent hasn't demonstrated the domain recently.
	LastVerified time.Time `json:"last_verified"`
}

// CapabilityFingerprint is the authoritative, network-visible identity record
// for an AetherNet agent. It is a living document: every task completion,
// failure, attestation, and stake change is reflected in a new version.
//
// The Registry is the authoritative store of fingerprints for a node's local
// view of the network. Peers gossip fingerprints during network sync.
type CapabilityFingerprint struct {
	// AgentID is the hex-encoded Ed25519 public key — the agent's immutable
	// cryptographic base identity. All other fields grow and change over time;
	// AgentID is the stable anchor.
	AgentID crypto.AgentID `json:"agent_id"`

	// DisplayName is a human-readable alias for the agent. Unlike AgentID, it
	// is not required to be unique or cryptographically derived — it is purely
	// a convenience label chosen by the agent at registration time (e.g. "my-llm-agent").
	// Agents may be looked up by DisplayName via Registry.GetByDisplayName.
	DisplayName string `json:"display_name,omitempty"`

	// PublicKey is the raw Ed25519 public key bytes (32 bytes), allowing
	// signature verification without decoding AgentID from hex on the hot path.
	PublicKey []byte `json:"public_key"`

	// Capabilities is the ordered list of domains this agent has demonstrated,
	// each backed by on-chain evidence. New domains are appended as the agent
	// completes tasks in previously-unknown areas.
	Capabilities []Capability `json:"capabilities"`

	// TasksCompleted is the cumulative count of tasks this agent has successfully
	// finished (as recorded by the local node). Used in ReputationScore.
	TasksCompleted uint64 `json:"tasks_completed"`

	// TasksFailed is the cumulative count of tasks this agent started but failed
	// to complete satisfactorily. Used in ReputationScore.
	TasksFailed uint64 `json:"tasks_failed"`

	// TotalValueGenerated is the sum of all ClaimedValue from Generation events
	// attributed to this agent (in micro-AET). Feeds into the network's assessment
	// of the agent's economic contribution.
	TotalValueGenerated uint64 `json:"total_value_generated"`

	// TotalValueTransferred is the sum of all Transfer event amounts involving
	// this agent as sender or recipient (in micro-AET).
	TotalValueTransferred uint64 `json:"total_value_transferred"`

	// OptimisticTrustLimit is the maximum amount (in micro-AET) the network will
	// extend to this agent under OCS without waiting for settlement confirmation.
	// It grows with each successful task completion and shrinks on failures.
	// Bounded below by minTrustLimit and above by StakedAmount * 10.
	OptimisticTrustLimit uint64 `json:"optimistic_trust_limit"`

	// ReputationScore is a 0–10000 basis-point score derived from task history:
	//   score = (TasksCompleted * 10000) / (TasksCompleted + TasksFailed + 1)
	// The +1 prevents division by zero for new agents and biases new agents
	// toward 0 until they establish a track record.
	ReputationScore uint64 `json:"reputation_score"`

	// StakedAmount is the amount of micro-AET currently locked as collateral
	// by this agent. It acts as a ceiling on OptimisticTrustLimit (×10 multiplier)
	// and is slashed if the agent behaves maliciously.
	StakedAmount uint64 `json:"staked_amount"`

	// FirstSeen is when this agent was first observed by the local node.
	FirstSeen time.Time `json:"first_seen"`

	// LastActive is the wall-clock time of the most recent recorded activity
	// (task completion or failure). Updated on every mutation.
	LastActive time.Time `json:"last_active"`

	// FingerprintVersion is a monotonically increasing counter. It increments on
	// every mutation (including internal RecordTaskCompletion/Failure operations)
	// and is used by Update to enforce optimistic concurrency control: the incoming
	// version must be exactly current + 1.
	FingerprintVersion uint64 `json:"fingerprint_version"`

	// FingerprintHash is the SHA-256 of the fingerprint's canonical fields
	// (all fields except FingerprintHash itself). The Registry recomputes this on
	// every mutation. Peers can verify fingerprint integrity during sync without
	// replaying the full event history.
	FingerprintHash string `json:"fingerprint_hash"`
}

// fingerprintCanonical is the hash input. FingerprintHash is excluded to avoid
// circularity: the hash cannot include itself. All other fields are included so
// any mutation produces a detectably different hash.
type fingerprintCanonical struct {
	AgentID               crypto.AgentID `json:"agent_id"`
	DisplayName           string         `json:"display_name,omitempty"`
	PublicKey             []byte         `json:"public_key"`
	Capabilities          []Capability   `json:"capabilities"`
	TasksCompleted        uint64         `json:"tasks_completed"`
	TasksFailed           uint64         `json:"tasks_failed"`
	TotalValueGenerated   uint64         `json:"total_value_generated"`
	TotalValueTransferred uint64         `json:"total_value_transferred"`
	OptimisticTrustLimit  uint64         `json:"optimistic_trust_limit"`
	ReputationScore       uint64         `json:"reputation_score"`
	StakedAmount          uint64         `json:"staked_amount"`
	FirstSeen             time.Time      `json:"first_seen"`
	LastActive            time.Time      `json:"last_active"`
	FingerprintVersion    uint64         `json:"fingerprint_version"`
}

// NewFingerprint constructs a fresh CapabilityFingerprint for an agent entering
// the AetherNet network. Initial state reflects a new, untested agent:
//   - ReputationScore = 0 (no history yet)
//   - OptimisticTrustLimit = minTrustLimit (enough to begin participating)
//   - FingerprintVersion = 1
func NewFingerprint(agentID crypto.AgentID, publicKey []byte, capabilities []Capability) (*CapabilityFingerprint, error) {
	if agentID == "" {
		return nil, errors.New("identity: agentID must not be empty")
	}
	if len(publicKey) == 0 {
		return nil, errors.New("identity: publicKey must not be empty")
	}
	if capabilities == nil {
		capabilities = []Capability{}
	}

	now := time.Now().UTC()
	fp := &CapabilityFingerprint{
		AgentID:              agentID,
		PublicKey:            publicKey,
		Capabilities:         capabilities,
		OptimisticTrustLimit: minTrustLimit,
		FirstSeen:            now,
		LastActive:           now,
		FingerprintVersion:   1,
	}

	if err := fp.refreshHash(); err != nil {
		return nil, fmt.Errorf("identity: failed to hash new fingerprint: %w", err)
	}
	return fp, nil
}

// ComputeHash computes and returns the SHA-256 content hash of the fingerprint's
// canonical fields. It does not modify the fingerprint; use refreshHash to also
// store the result in FingerprintHash.
func (fp *CapabilityFingerprint) ComputeHash() (string, error) {
	canon := fingerprintCanonical{
		AgentID:               fp.AgentID,
		DisplayName:           fp.DisplayName,
		PublicKey:             fp.PublicKey,
		Capabilities:          fp.Capabilities,
		TasksCompleted:        fp.TasksCompleted,
		TasksFailed:           fp.TasksFailed,
		TotalValueGenerated:   fp.TotalValueGenerated,
		TotalValueTransferred: fp.TotalValueTransferred,
		OptimisticTrustLimit:  fp.OptimisticTrustLimit,
		ReputationScore:       fp.ReputationScore,
		StakedAmount:          fp.StakedAmount,
		FirstSeen:             fp.FirstSeen,
		LastActive:            fp.LastActive,
		FingerprintVersion:    fp.FingerprintVersion,
	}
	data, err := json.Marshal(canon)
	if err != nil {
		return "", fmt.Errorf("identity: failed to marshal fingerprint canonical form: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// refreshHash recomputes the FingerprintHash and stores it in the fingerprint.
// Called by the Registry after every mutation to maintain hash integrity.
func (fp *CapabilityFingerprint) refreshHash() error {
	h, err := fp.ComputeHash()
	if err != nil {
		return err
	}
	fp.FingerprintHash = h
	return nil
}

// recalcReputationScore derives and sets ReputationScore from current task counts.
//
// Formula: score = (TasksCompleted * 10000) / (TasksCompleted + TasksFailed + 1)
//
// Properties:
//   - New agent (no tasks): 0 / 1 = 0
//   - Perfect record (N completions, 0 failures): 10000·N / (N+1) → 10000 asymptotically
//   - All failures (0 completions, N failures): 0 / (N+1) = 0
//   - The +1 denominator prevents division by zero and slightly penalises agents
//     with very few tasks, rewarding sustained performance over time.
func (fp *CapabilityFingerprint) recalcReputationScore() {
	fp.ReputationScore = (fp.TasksCompleted * maxReputation) / (fp.TasksCompleted + fp.TasksFailed + 1)
}

// clone returns a deep copy of fp. PublicKey ([]byte) and Capabilities ([]Capability)
// are deep-copied; all other fields are value types and are copied by the struct copy.
// Callers that receive a clone may mutate it freely without affecting registry state.
func (fp *CapabilityFingerprint) clone() *CapabilityFingerprint {
	c := *fp // shallow copy of value fields

	c.PublicKey = make([]byte, len(fp.PublicKey))
	copy(c.PublicKey, fp.PublicKey)

	c.Capabilities = make([]Capability, len(fp.Capabilities))
	copy(c.Capabilities, fp.Capabilities)

	return &c
}
