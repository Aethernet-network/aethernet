package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Aethernet-network/aethernet/internal/auth"
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// CanonicalSyntheticID computes a deterministic content-addressed
// EventID for a synthetic transfer (one that has no canonical Transfer
// event in the DAG and is recorded only in the local ledger projection).
//
// **Algorithm**: SHA-256 hex digest of the RFC 8785 (JCS) canonicalized
// JSON encoding of (fromID, toID, amount, memo, isGenesis). Byte-
// identical inputs produce byte-identical IDs across all nodes —
// required for cross-node ledger consistency on transfers that don't
// have a canonical event of their own.
//
// **Post-F5 5B scope** (architect-confirmed at #134 closure): this is
// a deterministic-synthetic-ID utility for ledger transfers across all
// non-settlement domains. Settlement-path callers use
// `internal/settlement/derivation.DeriveSettlement`, which produces
// full `PayoutRecord` values whose `canonical_id` is the EventID for
// each settlement-emitted transfer (subsumes the inputs here plus
// settlement_key, recipient.role, purpose.tag, purpose.ordinal,
// provenance fields). The 5 settlement-path invocations of this helper
// were removed alongside `escrow.ReleaseSettlement` in F5 5B #133.
//
// **Non-settlement callers** that retain the helper:
//   - `escrow.Refund` — bucket → poster transfer on cancellation.
//   - `escrow.ReleaseNet` — dispute-path cancellation flow (Plan v3
//     §0.7 explicitly excludes this from F5 5B scope).
//   - `staking.Lock` / `staking.Unlock` — validator stake management.
//   - `fees.collector` — validator + treasury fee distribution.
//   - `ledger.TransferFromBucket` — legacy unlabeled-transfer wrapper.
//
// These callers operate outside the settlement-derivation flow and
// have no `DeriveSettlement`-equivalent canonical-record producer in
// their respective domains; they call this helper directly when they
// need a deterministic transfer ID for cross-node ledger consistency.
//
// Pre-5B settlement-callers historical context: the F5 5A.4.b refactor
// introduced this helper as the interim canonical-ID source for
// synthetic transfers. F5 5B's DeriveSettlement subsumes the SETTLEMENT
// callers (5 of them, all in the now-deleted `ReleaseSettlement`); the
// non-settlement callers above were never part of that subsumption.
//
// The hash format aligns with docs/architecture/payout-artifact-
// schema.yaml §"CANONICAL HASH ALGORITHM" (SHA-256 + RFC 8785 JCS), so
// the SAME algorithm produces canonical_ids for both settlement records
// (computed by DeriveSettlement) and non-settlement synthetic
// transfers (computed here). Different inputs → different IDs; no
// collision concern across the two surfaces.
func CanonicalSyntheticID(
	fromID crypto.AgentID,
	toID crypto.AgentID,
	amount uint64,
	memo string,
	isGenesis bool,
) (event.EventID, error) {
	preimage := map[string]any{
		"amount":     amount,
		"from":       string(fromID),
		"is_genesis": isGenesis,
		"memo":       memo,
		"to":         string(toID),
	}
	rawJSON, err := json.Marshal(preimage)
	if err != nil {
		return "", fmt.Errorf("ledger: canonical_synthetic_id marshal: %w", err)
	}
	canonical, err := auth.CanonicalizeJSON(rawJSON)
	if err != nil {
		return "", fmt.Errorf("ledger: canonical_synthetic_id jcs: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return event.EventID(hex.EncodeToString(sum[:])), nil
}
