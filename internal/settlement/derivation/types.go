package derivation

import (
	"github.com/Aethernet-network/aethernet/internal/event"
)

// DerivationVersion is the monotone version of the derivation function
// that produced a given PayoutRecord. Part of canonical record identity:
// records with different derivation_versions hash to different canonical_ids
// even when all other fields are byte-identical.
//
// Bumped only when the record SCHEMA or DERIVATION SEMANTICS change.
// Per F5 5A.4.a schema notes: the implementation swap from stub-W to
// real-W via canonical-position-bound V-1 selection does NOT bump this
// version — V-1 makes the swap transparent to record content.
const DerivationVersion uint32 = 1

// DerivationStatus discriminates whether DeriveSettlement produced
// records (Derived) or deferred pending canonical-state materialization
// (Deferred). Closed enum.
type DerivationStatus int

const (
	// StatusDerived indicates the derivation function completed and
	// produced a records slice. The caller applies the records via the
	// applicator.
	StatusDerived DerivationStatus = iota

	// StatusDeferred indicates canonical state needed by the derivation
	// function is not yet locally materialized. The caller re-enqueues
	// the round for retry; once materialization catches up, retry
	// converges to the byte-identical Derived result (property D-2).
	StatusDeferred
)

// String returns a lowercase identifier for DerivationStatus. Used for
// telemetry only; not in any canonical_id preimage.
func (s DerivationStatus) String() string {
	switch s {
	case StatusDerived:
		return "derived"
	case StatusDeferred:
		return "deferred"
	}
	return "unknown"
}

// DeferredCause names which canonical-state lookup hit
// ErrEventNotFound and triggered deferral. Closed enum; one variant per
// distinct deferral path in DeriveSettlement.
//
// Rationale for typed enum over string (v2 plan-mode review Finding 2):
// allows caller retry/telemetry policy to discriminate without string
// parsing, and guarantees the compiler surfaces any new deferral path
// at the callsite.
type DeferredCause int

const (
	// DeferredCauseV1AncestorCheck: ActivationCheck returned
	// ErrEventNotFound while deciding stub-W vs real-W per V-1.
	DeferredCauseV1AncestorCheck DeferredCause = iota

	// DeferredCauseDAGAncestorBFS: ReadAtAnchor returned
	// ErrEventNotFound while enumerating generation-ledger ancestors.
	DeferredCauseDAGAncestorBFS

	// DeferredCauseWLookup: CanonicalWProjection.Lookup returned
	// ErrEventNotFound for a validator's W at the round's canonical
	// cutoff.
	DeferredCauseWLookup

	// DeferredCauseQualityLookup: CanonicalQualityProjection.Lookup
	// returned ErrEventNotFound for a gen-ledger ancestor's quality
	// at the round's canonical cutoff.
	DeferredCauseQualityLookup
)

// String returns a lowercase identifier for DeferredCause. Telemetry
// only; not canonical.
func (c DeferredCause) String() string {
	switch c {
	case DeferredCauseV1AncestorCheck:
		return "v1_ancestor_check"
	case DeferredCauseDAGAncestorBFS:
		return "dag_ancestor_bfs"
	case DeferredCauseWLookup:
		return "w_lookup"
	case DeferredCauseQualityLookup:
		return "quality_lookup"
	}
	return "unknown"
}

// TerminalStatus names the terminal round outcome that produced the
// settlement. Mirrors the three finalized branches of
// taskverification.RoundState (FinalizedAccept / FinalizedReject /
// Disputed) — the round-expired branch is not a settlement input.
type TerminalStatus int

const (
	TerminalAccept TerminalStatus = iota
	TerminalReject
	TerminalDispute
)

// String returns a lowercase identifier for TerminalStatus. Telemetry
// only; the canonical provenance.round_verdict field is separate and
// uses Verdict (see record.go).
func (t TerminalStatus) String() string {
	switch t {
	case TerminalAccept:
		return "accept"
	case TerminalReject:
		return "reject"
	case TerminalDispute:
		return "dispute"
	}
	return "unknown"
}

// DerivationSummary is observability metadata about a derivation
// invocation. NOT included in any canonical_id hash preimage; never
// feeds back into derivation. Used for diagnostics and for 5D
// verification's cross-node sanity checks (e.g., "every node selected
// real-W for round R" agreement).
type DerivationSummary struct {
	RecordCountByRole      map[string]uint32 // role name → count
	SelectedWMode          string            // "stub" | "real"
	SelectedQualityMode    string            // "stub" | "real"
	GenLedgerTraversalRan  bool              // true iff verdict == Accept and gen-ledger pool > 0
	GenLedgerAncestorCount uint32            // 0 if traversal did not run
	AgreeingValidatorCount uint32            // 0 on dispute path
}

// DerivationResult is the output of DeriveSettlement.
//
// When Status == StatusDerived: Records is populated in the canonical
// ordinal order (schema 4-step rule); TerminalStatus is set; Cause is
// unused; Summary is populated; ResolvedCutoffAnchor and
// ResolvedCutoffAnchorIsNil are always meaningful.
//
// When Status == StatusDeferred: Records is empty; TerminalStatus is
// unused; Cause is populated; Summary may be partially populated for
// debugging; ResolvedCutoffAnchor / ResolvedCutoffAnchorIsNil are
// unset.
type DerivationResult struct {
	Status         DerivationStatus
	Records        []PayoutRecord
	TerminalStatus TerminalStatus

	// ResolvedCutoffAnchor is the canonical_cutoff_anchor used during
	// this derivation (per Fix A semantic). Returned for caller audit
	// and 5D verification. Meaningful only when Status == StatusDerived.
	// EventID value when non-nil; zero value when nil (see
	// ResolvedCutoffAnchorIsNil to discriminate).
	ResolvedCutoffAnchor event.EventID

	// ResolvedCutoffAnchorIsNil reports whether the resolved cutoff
	// anchor is the Fix A nil form. True iff ReputationActivation is
	// NOT a canonical ancestor of R.canonical_seal_context. Meaningful
	// only when Status == StatusDerived.
	ResolvedCutoffAnchorIsNil bool

	// Cause is populated only when Status == StatusDeferred.
	Cause DeferredCause

	// Summary is observability metadata. NOT in any canonical_id hash
	// preimage.
	Summary DerivationSummary
}
