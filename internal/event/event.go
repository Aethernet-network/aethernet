// Package event defines the fundamental units of the AetherNet causal DAG.
//
// AetherNet departs from blockchain architecture by making events — not blocks —
// the atomic unit of the network. Each event directly references the specific prior
// events it depends on and has validated, producing a sparse Directed Acyclic Graph
// (DAG) where causality is explicit and parallel event processing is natural.
//
// The dual-ledger design separates two economically distinct operations:
//   - Transfer events: existing value changing hands (Transfer Ledger)
//   - Generation events: net-new value created by AI computation (Generation Ledger)
//
// Optimistic Capability Settlement (OCS) lets events take effect immediately under
// an optimistic assumption of validity, with asynchronous confirmation via staked
// attestations and validator verifications — mirroring how clearing houses operate.
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EventType distinguishes the semantic role of an event in the causal DAG.
// Each type maps to distinct ledger behavior and settlement mechanics.
type EventType string

const (
	// EventTypeTransfer records value moving between two agents.
	// Writes to the Transfer Ledger. Both agents must exist and the sender
	// must hold sufficient balance in the relevant currency.
	EventTypeTransfer EventType = "Transfer"

	// EventTypeGeneration records value created by AI computation — net-new value
	// entering the system that did not previously exist. Writes to the Generation
	// Ledger. Separating generation from transfer prevents double-counting and
	// makes the provenance of every unit of value traceable to a specific work claim.
	EventTypeGeneration EventType = "Generation"

	// EventTypeAttestation is a stake-backed peer claim about the validity of another
	// event. Attestations are the primary vehicle for moving events from Optimistic
	// to Settled in the OCS model: aggregated stake-weighted confidence drives settlement.
	EventTypeAttestation EventType = "Attestation"

	// EventTypeVerification is a formal verdict issued by a validator node executing
	// Proof of Useful Work. Unlike peer attestations, verifications require the
	// validator to re-execute or cryptographically validate the claimed computation,
	// and carry proportionally greater authority in the settlement calculus.
	EventTypeVerification EventType = "Verification"

	// EventTypeDelegation grants a delegate agent bounded authority to act on behalf
	// of the delegator. Delegation is capability-scoped: the network enforces the
	// declared spending limit and permitted category constraints, enabling safe
	// multi-agent orchestration without transferring private keys.
	EventTypeDelegation EventType = "Delegation"
)

// SettlementState tracks an event's position in the Optimistic Capability Settlement
// (OCS) lifecycle. AetherNet mirrors how real-world clearing houses operate: value
// moves immediately under optimistic assumptions and is confirmed (or unwound)
// asynchronously as stake-weighted evidence accumulates.
type SettlementState string

const (
	// SettlementOptimistic is the initial state of every new event. The event is
	// acted upon immediately — ledger entries are applied, downstream events may
	// reference it — but it remains subject to challenge until Settled.
	SettlementOptimistic SettlementState = "Optimistic"

	// SettlementSettled means sufficient cumulative stake-weighted attestations and/or
	// verifications have confirmed the event beyond the network's challenge threshold.
	// Reversal remains theoretically possible but requires a supermajority challenge.
	SettlementSettled SettlementState = "Settled"

	// SettlementAdjusted means a successful challenge has been upheld. The event
	// remains in the DAG as an immutable historical record (the DAG is append-only),
	// but its ledger entries are neutralized by a compensating adjustment entry.
	// The staked amounts of the losing party are slashed.
	SettlementAdjusted SettlementState = "Adjusted"
)

// EventID is the hex-encoded SHA-256 content hash of an event's canonical fields.
// Content-addressing means the ID uniquely and verifiably identifies its event:
// any mutation to the canonical fields produces a different ID, making tampering
// detectable without external coordination.
type EventID string

// Event is the fundamental unit of the AetherNet causal DAG.
//
// Unlike blockchain systems that batch events into sequentially ordered blocks,
// each AetherNet event is a first-class participant in the DAG. It references only
// the specific prior events it has processed and validated, making causal
// dependencies explicit at the data level and enabling fine-grained parallelism.
type Event struct {
	// ID is the SHA-256 content hash of this event's canonical fields (all fields
	// except ID and Signature, which are excluded to avoid circularity). Computed
	// by ComputeID after all canonical fields are set; immutable thereafter.
	ID EventID `json:"id"`

	// Type determines how Payload is interpreted and which ledger(s) the event
	// writes to. Consumers must type-assert Payload using this field as the key.
	Type EventType `json:"type"`

	// CausalRefs are the EventIDs this event has processed and validated before
	// being created. These are the directed edges of the DAG — by including an
	// event in CausalRefs, the author implicitly attests to having seen, accepted,
	// and built upon that event. An empty slice marks a genesis (root) event.
	CausalRefs []EventID `json:"causal_refs"`

	// Payload holds type-specific structured data. The concrete type depends on
	// the Type field:
	//   EventTypeTransfer     → TransferPayload
	//   EventTypeGeneration   → GenerationPayload
	//   EventTypeAttestation  → AttestationPayload
	//   EventTypeVerification → VerificationPayload
	//   EventTypeDelegation   → DelegationPayload
	Payload interface{} `json:"payload"`

	// AgentID is the capability fingerprint of the originating agent.
	// AetherNet identity is track-record-based rather than purely key-based:
	// the fingerprint encodes the agent's verifiable behavioral history, so
	// an agent that acts well has a reputation that cannot be trivially forked.
	AgentID string `json:"agent_id"`

	// Signature is the cryptographic signature over the event's canonical
	// serialization (all fields except ID and Signature itself). Empty until
	// the event is signed by the originating agent's private key material.
	// Verification uses the public component of AgentID.
	Signature []byte `json:"signature,omitempty"`

	// CausalTimestamp is a Lamport logical clock value derived as:
	//
	//   max(ref.CausalTimestamp for ref in CausalRefs) + 1
	//
	// For genesis events (empty CausalRefs), CausalTimestamp is 1.
	// This establishes a partial order consistent with causality without requiring
	// synchronized wall clocks across distributed nodes — a critical property for
	// a global network of heterogeneous AI agents operating across jurisdictions.
	CausalTimestamp uint64 `json:"causal_timestamp"`

	// StakeAmount is the amount of AET staked on this event, denominated in
	// micro-AET (the smallest indivisible unit, 1 AET = 1,000,000 micro-AET).
	// Stake creates economic skin-in-the-game: agents bear downside risk
	// proportional to the boldness of their claims and attestations.
	StakeAmount uint64 `json:"stake_amount"`

	// SettlementState tracks this event through the OCS lifecycle.
	// All events begin as Optimistic and transition to Settled or Adjusted.
	// SettlementState is excluded from the content hash because it changes
	// over the event's lifetime without changing its causal identity.
	SettlementState SettlementState `json:"settlement_state"`
}

// eventCanonical is the projection of Event fields used to derive the content hash.
//
// Excluded fields and their rationale:
//   - ID: circular — we're computing it
//   - Signature: circular — signing happens after hashing
//   - SettlementState: mutable post-creation metadata, not part of causal identity
type eventCanonical struct {
	Type            EventType   `json:"type"`
	CausalRefs      []EventID   `json:"causal_refs"`
	Payload         interface{} `json:"payload"`
	AgentID         string      `json:"agent_id"`
	CausalTimestamp uint64      `json:"causal_timestamp"`
	StakeAmount     uint64      `json:"stake_amount"`
}

// ComputeID derives the content-addressed EventID from an event's canonical fields.
// All canonical fields must be set on the event before calling this function.
// The result is the hex-encoded SHA-256 of the JSON-marshaled canonical projection.
func ComputeID(e *Event) (EventID, error) {
	canon := eventCanonical{
		Type:            e.Type,
		CausalRefs:      e.CausalRefs,
		Payload:         e.Payload,
		AgentID:         e.AgentID,
		CausalTimestamp: e.CausalTimestamp,
		StakeAmount:     e.StakeAmount,
	}
	data, err := json.Marshal(canon)
	if err != nil {
		return "", fmt.Errorf("event: failed to marshal canonical form: %w", err)
	}
	sum := sha256.Sum256(data)
	return EventID(hex.EncodeToString(sum[:])), nil
}

// ComputeCausalTimestamp derives the Lamport clock value for a new event given its
// causal references and a lookup map of already-known timestamps.
//
// The derivation rule is: timestamp = max(known timestamps for refs) + 1.
// References absent from priorTimestamps are treated as having timestamp 0.
// For genesis events (empty refs), the result is 1 — the logical origin of time.
func ComputeCausalTimestamp(refs []EventID, priorTimestamps map[EventID]uint64) uint64 {
	var maxTS uint64
	for _, ref := range refs {
		if ts, ok := priorTimestamps[ref]; ok && ts > maxTS {
			maxTS = ts
		}
	}
	return maxTS + 1
}

// New constructs a new Event, computing its CausalTimestamp and content-addressed ID.
// SettlementState is initialized to Optimistic. The Signature field is left empty;
// callers must sign the event separately via the originating agent's key material.
//
// priorTimestamps maps known EventIDs to their CausalTimestamp values and is used
// to derive the Lamport clock for this event. Pass nil or an empty map for genesis events.
func New(
	eventType EventType,
	causalRefs []EventID,
	payload interface{},
	agentID string,
	priorTimestamps map[EventID]uint64,
	stakeAmount uint64,
) (*Event, error) {
	if eventType == "" {
		return nil, errors.New("event: type must not be empty")
	}
	if agentID == "" {
		return nil, errors.New("event: agentID must not be empty")
	}

	// Normalize nil to empty slice for consistent JSON serialization and hashing.
	// A nil CausalRefs and an empty CausalRefs are semantically identical (both
	// mean "genesis event"), but JSON encodes them differently: null vs [].
	if causalRefs == nil {
		causalRefs = []EventID{}
	}
	if priorTimestamps == nil {
		priorTimestamps = map[EventID]uint64{}
	}

	e := &Event{
		Type:            eventType,
		CausalRefs:      causalRefs,
		Payload:         payload,
		AgentID:         agentID,
		CausalTimestamp: ComputeCausalTimestamp(causalRefs, priorTimestamps),
		StakeAmount:     stakeAmount,
		SettlementState: SettlementOptimistic,
	}

	id, err := ComputeID(e)
	if err != nil {
		return nil, err
	}
	e.ID = id

	return e, nil
}

// validTransitions declares the permitted settlement state progressions.
// The DAG is append-only so we never delete or rewrite history — we only
// allow forward transitions. Settled events can still be Adjusted (via a
// supermajority late challenge), but nothing moves back to Optimistic.
var validTransitions = map[SettlementState][]SettlementState{
	SettlementOptimistic: {SettlementSettled, SettlementAdjusted},
	SettlementSettled:    {SettlementAdjusted},
	SettlementAdjusted:   {},
}

// Transition advances an event's SettlementState to the target state.
// Returns an error if the transition is not permitted by the OCS lifecycle rules.
func Transition(e *Event, target SettlementState) error {
	allowed, ok := validTransitions[e.SettlementState]
	if !ok {
		return fmt.Errorf("event: unrecognized settlement state %q", e.SettlementState)
	}
	for _, s := range allowed {
		if s == target {
			e.SettlementState = target
			return nil
		}
	}
	return fmt.Errorf("event: cannot transition settlement state from %q to %q", e.SettlementState, target)
}

// ---------------------------------------------------------------------------
// Payload types
// ---------------------------------------------------------------------------

// TransferPayload carries a value transfer between two agents.
// Events of this type write to the Transfer Ledger — they record existing value
// changing hands, not new value being created.
type TransferPayload struct {
	// FromAgent is the capability fingerprint of the sending agent.
	FromAgent string `json:"from_agent"`

	// ToAgent is the capability fingerprint of the receiving agent.
	ToAgent string `json:"to_agent"`

	// Amount is the quantity transferred, denominated in micro-AET
	// (or the smallest unit of Currency if non-AET).
	Amount uint64 `json:"amount"`

	// Currency identifies the token being transferred (e.g., "AET", "USDC").
	// AetherNet is multi-currency; the ledger tracks balances per currency per agent.
	Currency string `json:"currency"`

	// Memo is an optional human-readable annotation. Not used in settlement logic.
	Memo string `json:"memo,omitempty"`
}

// GenerationPayload records value created by AI computation — net-new value
// entering the AetherNet economy that did not previously exist.
//
// Events of this type write to the Generation Ledger. The hard separation of
// generation from transfer prevents inflation attacks: you cannot transfer value
// into existence by relabeling a transfer as a generation. Every generation claim
// must be backed by an EvidenceHash that attestors and verifiers can inspect.
type GenerationPayload struct {
	// GeneratingAgent is the capability fingerprint of the agent that performed
	// the AI work producing the claimed value.
	GeneratingAgent string `json:"generating_agent"`

	// BeneficiaryAgent receives the newly generated value.
	// May be the same as GeneratingAgent when an agent captures its own output.
	BeneficiaryAgent string `json:"beneficiary_agent"`

	// ClaimedValue is the amount of value asserted to have been created, in micro-AET.
	// This claim is optimistic: it becomes authoritative once sufficiently attested.
	ClaimedValue uint64 `json:"claimed_value"`

	// EvidenceHash is the content hash of the work artifact proving the computation
	// (e.g., hash of a model output, inference log, or cryptographic execution proof).
	// Attestors and verifiers retrieve and inspect this artifact off-chain.
	EvidenceHash string `json:"evidence_hash"`

	// TaskDescription is a human-readable summary of the work performed,
	// aiding human reviewers and audit trails.
	TaskDescription string `json:"task_description"`
}

// AttestationPayload is a stake-backed peer claim asserting that a target event is valid.
//
// Attestations are the primary mechanism for moving events from Optimistic to Settled
// in the OCS model. The network accumulates stake-weighted attestation scores; once
// the score exceeds the settlement threshold, the event is marked Settled.
// Incorrect attestations result in stake slashing — the attesting agent loses
// StakedAmount proportional to the degree of incorrectness, creating a strong
// incentive for honest assessment over collusive rubber-stamping.
type AttestationPayload struct {
	// AttestingAgent is the capability fingerprint of the agent making the claim.
	AttestingAgent string `json:"attesting_agent"`

	// TargetEventID is the EventID of the event being attested to.
	TargetEventID EventID `json:"target_event_id"`

	// ClaimedAccuracy is the attesting agent's confidence that the target event's
	// claims are valid, expressed as a value in [0.0, 1.0].
	// 1.0 = certain the event is correct; 0.0 = certain it is fraudulent.
	ClaimedAccuracy float64 `json:"claimed_accuracy"`

	// StakedAmount is the micro-AET amount the attestor puts at risk.
	// Higher stake amplifies the attestation's weight in the settlement calculus.
	StakedAmount uint64 `json:"staked_amount"`
}

// VerificationPayload is a formal verdict issued by a validator node performing
// Proof of Useful Work.
//
// Unlike peer attestations (which are subjective confidence ratings), verifications
// require the verifying node to re-execute or cryptographically validate the
// claimed computation referenced by the target event. This deeper work earns
// verifiers a larger reward and gives their verdict proportionally higher authority
// in the settlement calculus. Incorrect verifications result in stake slashing.
type VerificationPayload struct {
	// VerifyingAgent is the capability fingerprint of the validator node.
	VerifyingAgent string `json:"verifying_agent"`

	// TargetEventID is the EventID of the event under verification.
	TargetEventID EventID `json:"target_event_id"`

	// Verdict is true if the target event's claims are valid, false otherwise.
	// A false verdict triggers the OCS challenge mechanism.
	Verdict bool `json:"verdict"`

	// EvidenceHash is the content hash of the verification artifact — e.g., the
	// re-execution trace, a zk-proof of correct computation, or an inference log.
	EvidenceHash string `json:"evidence_hash"`

	// StakedAmount is the micro-AET stake the verifier puts at risk.
	StakedAmount uint64 `json:"staked_amount"`
}

// DelegationPayload grants a delegate agent bounded authority to act on behalf
// of the delegator within explicitly declared capability limits.
//
// AetherNet delegation is capability-scoped, not key-based: the delegate never
// holds the delegator's private key material. The network enforces SpendingLimit
// and PermittedCategories by rejecting events signed by the delegate that exceed
// the declared scope. This makes safe multi-agent orchestration hierarchies possible
// without requiring full trust transfer between orchestrator and sub-agents.
type DelegationPayload struct {
	// DelegatorAgent is the capability fingerprint of the agent granting authority.
	DelegatorAgent string `json:"delegator_agent"`

	// DelegateAgent is the capability fingerprint of the agent receiving authority.
	DelegateAgent string `json:"delegate_agent"`

	// SpendingLimit caps the cumulative value the delegate may transfer on behalf
	// of the delegator, in micro-AET. Zero means no spending is permitted.
	SpendingLimit uint64 `json:"spending_limit"`

	// PermittedCategories is the explicit allowlist of task categories the delegate
	// may act upon (e.g., ["inference", "storage"]). An empty slice means the
	// delegate may not perform any categorized actions.
	PermittedCategories []string `json:"permitted_categories"`

	// ExpiresAt is the wall-clock time after which this delegation is void.
	// Wall time is appropriate here: delegation expiry is a human-legible,
	// real-world constraint (e.g., "valid for 24 hours") rather than a
	// causal ordering concern — making it one of the few places AetherNet
	// uses wall-clock time directly.
	ExpiresAt time.Time `json:"expires_at"`
}
