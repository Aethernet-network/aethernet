package derivation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/auth"
)

// ComputeCanonicalID computes the SHA-256 hex digest of the RFC 8785
// JCS canonicalization of a PayoutRecord's preimage — the full record
// with CanonicalID excluded. Per docs/architecture/payout-artifact-
// schema.yaml §"CANONICAL HASH ALGORITHM" (LOCKED at Gate 5A.4.a).
//
// Uniqueness invariant U-1: the preimage MUST include SettlementKey,
// Recipient, Purpose (Tag + Ordinal), Amount, Provenance, and
// DerivationVersion. Shrinking the preimage is a halt-worthy regression
// per schema §"UNIQUENESS INVARIANT".
//
// Fix A nil semantic for Provenance.CanonicalCutoffAnchor: nil is a
// distinct canonical value. Encoded as JSON null, never omitted.
// The CanonicalCutoffAnchorIsNil discriminator on Provenance controls
// whether the canonical_cutoff_anchor slot holds null or the EventID
// string. Field omission would create preimage ambiguity (two different
// absences hashing to the same preimage).
//
// Every other optional field in the canonical_id preimage follows the
// same explicit-null discipline. New fields added to PayoutRecord must
// extend this function to include them in the preimage map in RFC 8785
// JCS canonical key order (the JCS pass sorts; ComputeCanonicalID does
// not itself sort, but it must include every U-1-bearing field).
func ComputeCanonicalID(r PayoutRecord) (string, error) {
	preimage := payoutRecordPreimage(r)

	rawJSON, err := json.Marshal(preimage)
	if err != nil {
		return "", fmt.Errorf("derivation: canonical_id marshal: %w", err)
	}

	canonical, err := auth.CanonicalizeJSON(rawJSON)
	if err != nil {
		return "", fmt.Errorf("derivation: canonical_id jcs: %w", err)
	}

	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// payoutRecordPreimage builds the JSON-Marshalable preimage map for
// ComputeCanonicalID. Every U-1-bearing field is present. Optional
// fields (today: Provenance.CanonicalCutoffAnchor) use explicit null
// when the field is nil; the nil discriminator lives on the record
// struct to preserve the two-valued semantic.
//
// The JCS pass sorts keys, so this function may emit keys in any
// order. We mirror the schema's top-level field order for readability.
func payoutRecordPreimage(r PayoutRecord) map[string]any {
	preimage := map[string]any{
		"derivation_version": r.DerivationVersion,
		"settlement_key": map[string]any{
			"round_id":          r.SettlementKey.RoundID,
			"task_id":           r.SettlementKey.TaskID,
			"funding_reference": string(r.SettlementKey.FundingReference),
		},
		"recipient": map[string]any{
			"id":   string(r.Recipient.ID),
			"role": string(r.Recipient.Role),
		},
		"amount": map[string]any{
			"value":    r.Amount.Value,
			"currency": string(r.Amount.Currency),
		},
		"purpose": map[string]any{
			"tag":     string(r.Purpose.Tag),
			"ordinal": r.Purpose.Ordinal,
		},
		"provenance": provenancePreimage(r.Provenance),
	}
	return preimage
}

// provenancePreimage serializes Provenance with Fix A nil semantic on
// canonical_cutoff_anchor. When IsNil is true the slot is JSON null;
// otherwise it is the EventID string. Field is always present —
// never omitted — to prevent preimage ambiguity.
func provenancePreimage(p Provenance) map[string]any {
	var cutoffValue any
	if p.CanonicalCutoffAnchorIsNil {
		cutoffValue = nil
	} else {
		cutoffValue = string(p.CanonicalCutoffAnchor)
	}
	return map[string]any{
		"round_verdict":           string(p.RoundVerdict),
		"canonical_cutoff_anchor": cutoffValue,
	}
}
