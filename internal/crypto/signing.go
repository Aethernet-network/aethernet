package crypto

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/aethernet/core/internal/event"
)

// signable defines the fields of an event that are covered by the signature.
//
// This struct must mirror the unexported eventCanonical struct in the event package
// exactly — same fields, same JSON tags, same declaration order — so that both
// operations (signing here and ID hashing in event.ComputeID) commit to an
// identical byte sequence for the same event. The test TestCanonicalBytes_MatchesEventComputeID
// verifies this invariant by checking sha256(CanonicalBytes(e)) == e.ID.
//
// Excluded fields and rationale:
//   - ID: computed from these fields (circular)
//   - Signature: the output of signing (circular; included would prevent signing)
//   - SettlementState: mutable OCS lifecycle metadata that evolves after creation
//     without invalidating the event's causal identity or its signature.
type signable struct {
	Type            event.EventType `json:"type"`
	CausalRefs      []event.EventID `json:"causal_refs"`
	Payload         interface{}     `json:"payload"`
	AgentID         string          `json:"agent_id"`
	CausalTimestamp uint64          `json:"causal_timestamp"`
	StakeAmount     uint64          `json:"stake_amount"`
}

// CanonicalBytes returns the deterministic byte sequence of an event that is
// both signed (by SignEvent) and hashed to produce the event ID (by event.ComputeID).
//
// The canonical form is the JSON encoding of the signable projection. Because
// encoding/json serialises struct fields in declaration order and uses deterministic
// key names, the output is identical across machines and Go versions for the same
// event content.
//
// Callers can verify the relationship between signing and ID computation by checking:
//
//	sha256(CanonicalBytes(e)) == e.ID
//
// This invariant guarantees that: if an event's signature verifies, its ID was
// computed from the same content — there is no gap between what was signed and
// what identifies the event in the DAG.
func CanonicalBytes(e *event.Event) ([]byte, error) {
	s := signable{
		Type:            e.Type,
		CausalRefs:      e.CausalRefs,
		Payload:         e.Payload,
		AgentID:         e.AgentID,
		CausalTimestamp: e.CausalTimestamp,
		StakeAmount:     e.StakeAmount,
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to serialise event for signing: %w", err)
	}
	return data, nil
}

// SignEvent computes the canonical bytes of e, signs them with kp, and stores
// the 64-byte Ed25519 signature in e.Signature.
//
// Precondition: e.AgentID must equal string(kp.AgentID()).
//
// This is enforced because AgentID is part of the canonical signed content. If
// AgentID did not match the signing key, the signature would be valid under the
// actual keypair's identity but the event would claim a different AgentID — a
// contradiction that VerifyEvent would catch, but which should be prevented at
// creation time.
//
// The standard workflow is:
//
//	kp, _ := crypto.GenerateKeyPair()
//	e, _ := event.New(type, refs, payload, string(kp.AgentID()), prior, stake)
//	_ = crypto.SignEvent(e, kp)
//
// SignEvent does not recompute e.ID. Signature and SettlementState are excluded
// from the hash, so signing does not change the event's content-addressed identity.
func SignEvent(e *event.Event, kp *KeyPair) error {
	expected := string(kp.AgentID())
	if e.AgentID != expected {
		return fmt.Errorf("crypto: event AgentID %q does not match keypair AgentID %q — "+
			"create the event with string(kp.AgentID()) as the agentID argument",
			e.AgentID, expected)
	}

	data, err := CanonicalBytes(e)
	if err != nil {
		return err
	}

	sig, err := kp.Sign(data)
	if err != nil {
		return err
	}

	e.Signature = sig
	return nil
}

// VerifyEvent verifies the Ed25519 signature stored in e.Signature against the
// event's canonical content. It decodes the public key from e.AgentID (which is
// the hex-encoded Ed25519 public key) and delegates to the package-level Verify.
//
// Returns false — rather than an error — for all invalid cases:
//   - e.Signature is empty (event not yet signed)
//   - e.AgentID is not valid hex or decodes to the wrong byte length
//   - The signature is cryptographically invalid for the content and key
//
// Tamper-detection coverage: any change to Type, CausalRefs, Payload, AgentID,
// CausalTimestamp, or StakeAmount after signing will cause VerifyEvent to return
// false. Changes to SettlementState or ID do not invalidate the signature —
// SettlementState is an OCS lifecycle field designed to evolve post-signing, and
// ID tampering is detectable via event.ComputeID rather than the signature.
func VerifyEvent(e *event.Event) bool {
	if len(e.Signature) == 0 {
		return false
	}

	pubKeyBytes, err := hex.DecodeString(e.AgentID)
	if err != nil {
		return false
	}

	data, err := CanonicalBytes(e)
	if err != nil {
		return false
	}

	return Verify(pubKeyBytes, data, e.Signature)
}
