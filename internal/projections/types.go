package projections

import "context"

// Classification distinguishes Canonical projections (whose state feeds
// protocol-affecting decisions) from Advisory projections (informational
// only). Per plan §9.2. The zero value is intentionally invalid so that a
// zero-initialized CanonicalProjection cannot pass Register validation.
type Classification uint8

const (
	// Canonical: any durable state projection whose outputs can affect
	// canonical protocol behavior — directly or transitively. Subject to
	// PR-1..PR-5 invariants per plan §9.4/§9.5.
	Canonical Classification = iota + 1

	// Advisory: informational state with no consensus-affecting output.
	// PR-5 does not apply; ObservabilitySurface.Kind == SurfaceNone is
	// permitted if Justification is non-empty.
	Advisory
)

// EligibilityWindow is the PR-5 locked value from plan §16: a Canonical
// projection that has been registered for strictly more than this many
// epochs and still has an empty aggregate is a fatal health error.
const EligibilityWindow uint64 = 3

// SurfaceKind enumerates the observability surface categories a projection
// may expose. Loosely aligned with plan §10's three tiers — node-local,
// public aggregate — plus diagnostic and CLI surfaces. Step 8 extends this
// set when endpoints are wired; it does not remove the values defined here.
type SurfaceKind uint8

const (
	// SurfaceNone means the projection does not expose observability.
	// Advisory projections may use this with a non-empty Justification.
	// Canonical projections may NOT use this (PR-4).
	SurfaceNone SurfaceKind = iota

	// SurfaceNodeLocalHTTP: tier-2 operator access over a node-local HTTP
	// endpoint (typically bound to 127.0.0.1).
	SurfaceNodeLocalHTTP

	// SurfaceCLI: operator access via the aet CLI (e.g. `aet reputation inspect`).
	SurfaceCLI

	// SurfaceHealth: diagnostic surface on the node's health endpoint.
	SurfaceHealth

	// SurfacePublicAggregate: tier-3 public aggregate statistics with delay
	// and cohort gating per plan §10.1.
	SurfacePublicAggregate
)

// Surface describes a projection's observability surface. EndpointPath is
// optional free-text documentation and is not interpreted by the registry
// in Step 1. Justification is required when Kind == SurfaceNone.
type Surface struct {
	Kind          SurfaceKind
	EndpointPath  string
	Justification string
}

// Subcategory distinguishes the four kinds of replay-derived persisted
// node state that register in the single mandatory inventory per
// invariant C-9 of the F3-B locked design.
//
// The zero value (SubcategoryProjection) is the default for existing
// projection entries, so all pre-Part-C registrations continue to work
// without modification.
type Subcategory uint8

const (
	SubcategoryProjection        Subcategory = iota // existing state projections (default)
	SubcategoryDispatchAdmission                    // dispatcher admission records
	SubcategoryEscrowRegistry                       // escrow registries
	SubcategoryApplicatorApplied                    // applicator applied-sets
)

// EventType is a string identifier for DAG event types. This package does
// not import internal/event to keep projections/ dependency-free. Step 2's
// registrations pass plain strings matching the event type constants.
type EventType string

// StateProbe inspects a projection's persistent state. Returns (empty=true)
// when the store has no records of its projected type. Called by HealthCheck
// only for Canonical projections past the eligibility window that are not
// using the CR-9 idle exception.
//
// Probes MUST be side-effect-free and safe to call concurrently with
// live-path writes. Specifically: do not mutate the projection, the
// registry, or any shared data structure; do not log above debug level;
// do not increment counters or metrics. Do not acquire locks that would
// block writes; tolerate mid-transaction state by returning empty=true
// rather than blocking. PR-5's 3-epoch window gives enough slack that an
// occasional false-empty read recovers on the next epoch-boundary check.
// Violating this invariant is a projection-authoring bug, not a registry
// bug. Enforcement is by convention and code review per CLAUDE.md §4, not
// by the Go type system.
type StateProbe func(ctx context.Context) (empty bool, err error)

// CanonicalProjection describes one registered projection per plan §9.2,
// extended with StateProbe (step 1 design decision D1) and the CR-9 named
// exception fields (plan §2.3).
//
// Field inventory — must match plan §9.2:
//   Name, Package, StoreType, Classification, SourceEvents,
//   LiveConsumerRef, ReplayConsumerRef, ObservabilitySurface,
//   IntegrationTestRef, Owner, CreatedAt.
// Added in step 1:
//   AllowIdleWithJustification, IdleJustification (CR-9),
//   StateProbe (D1).
type CanonicalProjection struct {
	// Name uniquely identifies the projection (e.g. "EvidenceStore").
	// Used as the registry map key. Required.
	Name string

	// Package is the Go package path the store lives in
	// (e.g. "internal/reputation"). Required.
	Package string

	// StoreType is the Go type name (e.g. "EvidenceStore"). Required.
	StoreType string

	// Classification is Canonical or Advisory. Required.
	Classification Classification

	// SourceEvents enumerates the DAG event types this projection
	// derives state from. Required for Canonical.
	SourceEvents []EventType

	// LiveConsumerRef is the fully-qualified symbol path of the
	// live-path consumer (e.g.
	// "internal/recognition.TaskVerificationConsensusConsumer").
	// Required for Canonical (PR-1).
	LiveConsumerRef string

	// ReplayConsumerRef is the fully-qualified symbol path of the
	// replay-path consumer. Required for Canonical (PR-2).
	ReplayConsumerRef string

	// ObservabilitySurface describes how operators inspect this
	// projection's state. Canonical entries may not have Kind ==
	// SurfaceNone (PR-4).
	ObservabilitySurface Surface

	// IntegrationTestRef is the fully-qualified test symbol path
	// (e.g. "internal/integration.TestEvidenceStore_AccumulatesOnConsensus").
	// Required for Canonical (PR-3). Step 1 validates non-empty; Step 3
	// adds a CI check that the symbol actually exists in the codebase.
	IntegrationTestRef string

	// Owner names the team or individual responsible for the projection.
	// Required.
	Owner string

	// CreatedAt is the ISO-8601 date of the registration's first creation
	// (e.g. "2026-04-14"). Required; validated non-empty only.
	CreatedAt string

	// Subcategory distinguishes the four kinds of replay-derived persisted
	// state per invariant C-9. The zero value (SubcategoryProjection) is the
	// default, preserving backward compatibility with pre-Part-C entries.
	Subcategory Subcategory

	// AllowIdleWithJustification permits this Canonical projection to run
	// idle (empty aggregate) past the eligibility window without a PR-5
	// fatal. Reserved for ChallengeResolutionStore in the initial
	// workstream per plan §2.3 CR-9; any other use requires explicit
	// founder approval.
	AllowIdleWithJustification bool

	// IdleJustification must be non-empty if and only if
	// AllowIdleWithJustification is true. Together they form the named
	// exception per CR-9.
	IdleJustification string

	// StateProbe inspects the projection's persistent state for PR-5.
	// Required for Canonical entries unless AllowIdleWithJustification is
	// set. Optional for Advisory. See StateProbe's doc comment for the
	// side-effect-free and concurrency-safety requirements.
	StateProbe StateProbe
}
