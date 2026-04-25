package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/crypto"
	"github.com/Aethernet-network/aethernet/internal/event"
)

// PayoutRecord is the canonical artifact produced by DeriveSettlement
// for each recipient of a settlement. Schema source of truth:
// docs/architecture/payout-artifact-schema.yaml (locked at Gate 5A.4.a).
//
// Every field is either canonical-frozen (fixed at round seal) or
// canonical-derived (deterministic function of canonical inputs).
// Forbidden shapes per schema: local-live, non-canonical-artifact,
// advisory, float64, time.Time, wall-clock-derived strings,
// unbounded-cardinality collections.
//
// CanonicalID is the SHA-256 hex digest of the RFC 8785 JCS
// canonicalization of the record with the CanonicalID field excluded
// from the preimage. Populated by CanonicalID() at construction time;
// see canonical_id.go.
//
// Uniqueness invariant U-1: the preimage MUST include SettlementKey,
// Recipient, Purpose (both Tag and Ordinal), Amount, Provenance, and
// DerivationVersion. Shrinking the preimage is a halt-worthy regression
// (schema §"UNIQUENESS INVARIANT").
type PayoutRecord struct {
	CanonicalID       string         `json:"canonical_id"`
	DerivationVersion uint32         `json:"derivation_version"`
	SettlementKey     SettlementKey  `json:"settlement_key"`
	Recipient         Recipient      `json:"recipient"`
	Amount            Amount         `json:"amount"`
	Purpose           Purpose        `json:"purpose"`
	Provenance        Provenance     `json:"provenance"`
}

// SettlementKey identifies which settlement a PayoutRecord belongs to.
// All three subfields are canonical-frozen at round seal.
type SettlementKey struct {
	// RoundID is the finalized round's identifier.
	RoundID string `json:"round_id"`

	// TaskID is the task sealed at TaskPosted. Redundant with
	// round_id→task_id lookup but included for direct traceability
	// without a round-store round-trip (schema §settlement_key.task_id
	// rationale).
	TaskID string `json:"task_id"`

	// FundingReference is the canonical Transfer event ID that funded
	// the escrow for TaskID. Derived via EscrowLookup.FundingRef at
	// derivation time; canonical-frozen at RegisterEscrow.
	FundingReference event.EventID `json:"funding_reference"`
}

// RecipientRole is the locked enum of payout recipient categories.
// Extending this enum is a new interface version per F5 5A.2 §7.5
// version-binding, NOT a source-compatible additive change. The locked
// set covers all settlement-emitted payouts today.
type RecipientRole string

const (
	RoleWorker            RecipientRole = "Worker"
	RoleValidator         RecipientRole = "Validator"
	RoleTreasury          RecipientRole = "Treasury"
	RolePosterRefund      RecipientRole = "PosterRefund"
	RoleGenLedgerAncestor RecipientRole = "GenLedgerAncestor"
)

// Recipient identifies the payout destination. Role is canonical-frozen
// (locked enum); ID is canonical-frozen for Worker/Poster/Validator
// (from round fields), construction-time-fixed for Treasury, and
// canonical-derived for GenLedgerAncestor (from 5A.3 BFS result).
type Recipient struct {
	ID   crypto.AgentID `json:"id"`
	Role RecipientRole  `json:"role"`
}

// Currency is the locked enum of payout currencies. Today a single
// value; multi-currency is out of F5 scope.
type Currency string

const (
	CurrencyAET Currency = "AET"
)

// Amount is the payout amount: integer µAET value + currency enum.
// Value is canonical-derived from canonical inputs (escrow.Amount,
// share constants, W projection, BFS weights). Integer arithmetic only.
type Amount struct {
	Value    uint64   `json:"value"`
	Currency Currency `json:"currency"`
}

// PurposeTag is the locked enum of settlement-purpose identifiers.
// Corresponds to TransferFromBucketLabeled "label" semantic in the
// pre-5B ReleaseSettlement callsites. Dotted form per schema lock.
// Tag and RecipientRole are related but distinct: role is the
// recipient's category; tag is the payout's purpose.
type PurposeTag string

const (
	TagWorkerPayout          PurposeTag = "settlement.worker_payout"
	TagPosterRefund          PurposeTag = "settlement.poster_refund"
	TagValidatorDistribution PurposeTag = "settlement.validator_distribution"
	TagGenLedgerRoyalty      PurposeTag = "settlement.gen_ledger_royalty"
	TagTreasuryRemainder     PurposeTag = "settlement.treasury_remainder"
)

// OrdinalAssignmentOrder is the fixed tag-group sequence per schema
// purpose.ordinal.ordinal_assignment_rule step 3. Records are grouped
// by tag and tag-groups processed in this order; within each group,
// records are sorted by Recipient.ID lex order (step 2); ordinal is a
// monotone counter from 0 across the full sequence (step 4).
//
// LOCKED at schema level (Gate 5A.4.a architect decision). 5B implements
// TO this specification; any deviation is a halt-and-surface trigger
// per plan §5 schema-reopen.
var OrdinalAssignmentOrder = []PurposeTag{
	TagWorkerPayout,
	TagPosterRefund,
	TagValidatorDistribution,
	TagGenLedgerRoyalty,
	TagTreasuryRemainder,
}

// Purpose describes the payout's purpose and its canonical ordering
// position within the settlement. Tag is canonical-frozen (locked enum);
// Ordinal is canonical-derived (schema 4-step rule).
type Purpose struct {
	Tag     PurposeTag `json:"tag"`
	Ordinal uint32     `json:"ordinal"`
}

// Verdict is the locked enum of round outcomes carried on provenance.
// Sourced from the round's canonical Outcome.Verdict per F5 5A.2 §0.8
// (NOT from payload.FinalVerdict per F4 C-17 advisory rule).
type Verdict string

const (
	VerdictAccept  Verdict = "VerdictAccept"
	VerdictReject  Verdict = "VerdictReject"
	VerdictDispute Verdict = "VerdictDispute"
)

// Provenance is the auditable metadata attached to each payout. Both
// subfields participate in the canonical_id preimage.
//
// CanonicalCutoffAnchor is EventID | nil (Fix A). Nil iff
// ReputationActivation is NOT a canonical ancestor of
// R.canonical_seal_context — i.e., R settled canonically before the
// locked workstream's snapshot framework was active. JCS serialization
// (see canonical_id.go) encodes nil as JSON null, never omission, to
// avoid canonical_id preimage ambiguity.
type Provenance struct {
	RoundVerdict          Verdict       `json:"round_verdict"`
	CanonicalCutoffAnchor event.EventID `json:"canonical_cutoff_anchor"`

	// CanonicalCutoffAnchorIsNil is the Fix A nil discriminator. When
	// true, canonical_id preimage encodes canonical_cutoff_anchor as
	// JSON null (not as the zero-value EventID string). Not itself in
	// the JSON output — canonical_id.go consults this flag while
	// building the preimage map.
	CanonicalCutoffAnchorIsNil bool `json:"-"`
}
