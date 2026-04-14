# Step 1 Implementation Plan — Projection Registry Package

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce `internal/projections/` as a standalone protocol primitive that defines the shape of a Canonical projection entry, provides a thread-safe registry with startup validation (`Register`/`MustRegister`), and an epoch-aware `HealthCheck` that enforces PR-5 against synthetic entries. Zero projections are registered; no existing package is touched. Retrofit and wiring are out of scope for this step.

**Architecture:** A single new package with four source files (`doc.go`, `types.go`, `registry.go`, `health.go`) and matching tests. The registry is a `map[string]CanonicalProjection` guarded by `sync.RWMutex` with monotonic registration timestamps captured via an injected `EpochFn`. `HealthCheck` evaluates each Canonical entry against its eligibility window and optional `StateProbe`.

**Tech Stack:** Go 1.x standard library only. No new dependencies. No external stores. No HTTP.

**Source plan**: `docs/plans/2026-04-12-reputation-and-consensus-integrity.md`. Step 1 implements a slice of §9 (Projection registry). The plan header cites "§7" for the registry; the actual content is in §9 of the shipped plan. All binding cites below use §9.

---

## Scope alignment with the binding plan

**Implemented in this step**:
- Plan §9.2 types (`Classification`, `CanonicalProjection`, `ProjectionRegistry`) with all fields from the §9.2 struct.
- Plan §9.4 invariants PR-1, PR-2, PR-3 (completeness check — structural, not the CI-level existence-of-referenced-test check), PR-4.
- Plan §9.5 invariant PR-5 (runtime health check mechanics); `EligibilityWindow = 3 epochs` per §16 locked constants.
- Plan §2.3 CR-9 named-exception mechanism (`AllowIdleWithJustification` + `IdleJustification` paired validation).
- Plan §0 item 9 (security-by-default): invalid registrations are fatal with a descriptive diagnostic so an operator cannot silently bypass the registry.

**Not implemented in this step (explicit deferral)**:
- PR-3 CI-level existence check for `IntegrationTestRef`. Step 1 validates the field is non-empty; scanning the repo to confirm the referenced symbol exists is Step 3 (CI tooling).
- Plan §9.6 retrofit and initial registry entries (EvidenceStore, ChallengeResolutionStore, CalibrationStore, EscrowStore, LedgerStore, GenerationLedgerCalculator, AgentReputation, BlobServingReputation). Step 2.
- Wiring the registry into `cmd/node/main.go`. Step 2+.
- Plan §9.7 defense items 1 (CI static check), 3 (integration test driving real events), 5 (code-review checklist). Step 3 and beyond.
- Observability endpoint wiring for `Surface`. Step 8.
- Any interaction with the existing reputation/calibration stores. Step 2 (retrofit) and Step 3 (deletion of old store).

**Explicit invariant against scope creep**: this step MUST NOT modify any file outside `internal/projections/` or `docs/plans/`. If implementation reveals a need to touch another package, stop and surface the need to the founder.

**Reservation of `AllowIdleWithJustification`**: per plan §2.3 CR-9, this flag is reserved for `ChallengeResolutionStore` only in the initial workstream (Step 2+). Any future use requires explicit founder approval and a new named exception. This step implements the mechanism; it does not pre-register any idle entry.

---

## Design decisions needing sign-off before code

### D1. `StateProbe` field on `CanonicalProjection` (ASK FOUNDER)

Plan §9.5 specifies "every Canonical projection has non-empty aggregate state consistent with the DAG events the projection should project." It does not specify the mechanism by which `HealthCheck` inspects each store's state. Three candidate shapes:

- **D1-a (recommended)**: add an optional field `StateProbe func(ctx context.Context) (empty bool, err error)` to `CanonicalProjection`. Canonical entries without `AllowIdleWithJustification` must provide a non-nil `StateProbe`; Canonical entries with that exception MAY omit it; Advisory entries MAY omit it. HealthCheck calls `StateProbe` for each Canonical entry past its eligibility window.
- **D1-b**: separate API `RegisterStateProbe(name string, probe StateProbe) error` — decouples entry from probe.
- **D1-c**: caller passes `map[string]StateProbe` into `HealthCheck` — probes live entirely outside the registry.

**Recommendation**: D1-a. It keeps the entry self-contained (Step 2's retrofits carry everything they need in one declaration), lets the registry enforce "Canonical + not-idle-justified ⇒ must have probe" as a structural invariant, and does not leak probe lifecycle to callers. `StateProbe` is a Go function value, not a persistable field — it is added to the in-memory struct only; any future serialization (Step 8) excludes it.

**Justification for addition beyond plan §9.2**: the plan's §9.5 requires the health check mechanism to exist; the mechanism requires a probe; the probe must be attached to the entry to preserve the "one registration, one declaration" locality. Flagging this explicitly per §18 ("If a detail is not in this document, Claude Code should not invent it. Genuine gaps should stop implementation and surface a specific question to the founder for resolution.")

If the founder rejects D1-a, the fallback is D1-c (simpler, but makes Step 2 carry probes in a parallel map). Either is acceptable. **Sign-off required before implementation.**

### D2. Epoch injection (APPROVED — see also Clarification 2 below)

Plan §9.5 references "more than EligibilityWindow epochs" but does not specify how the registry knows the current epoch. Resolution: `NewProjectionRegistry(epochFn func() uint64) *ProjectionRegistry`. The registry captures `registeredAtEpoch` at `Register` time via `epochFn()`. `HealthCheck` also calls `epochFn()` to determine current epoch.

**Clarification 2 — `registeredAtEpoch` is node-local liveness tracking, captured once per registration, never updated.** `registeredAtEpoch` is set at the instant `Register` is called and is immutable for that entry. The registry is an in-memory structure and does not persist across restarts: on process restart, a fresh registry is constructed and entries are re-registered at whatever the current epoch is at that moment. The PR-5 3-epoch window therefore measures "time since this process's registration", not "time since the projection type was first introduced to the protocol". This is the correct semantic because a newly-booted node has no obligation to have populated state instantly — it has 3 epochs of live DAG events to accumulate from before PR-5 demands it. The mandatory test for this semantic is T3.13.

### D3. `Surface` type shape

Step 1 prompt: "keep it simple — an enum or a small struct with an endpoint path; the full observability implementation is step 8." Plan §9.4 PR-4 requires an `ObservabilitySurface == None` case with a justification comment.

**Chosen shape**:
```go
type SurfaceKind uint8

const (
    SurfaceNone SurfaceKind = iota
    SurfaceNodeLocalHTTP
    SurfaceCLI
    SurfaceHealth
    SurfacePublicAggregate
)

type Surface struct {
    Kind          SurfaceKind
    EndpointPath  string // optional, documentation only (e.g. "/v1/reputation/self")
    Justification string // required when Kind == SurfaceNone
}
```

The kinds loosely map to plan §10's three tiers (NodeLocalHTTP + CLI = tier 2; PublicAggregate = tier 3; Health = diagnostic). Step 8 may extend kinds; it will not remove the ones defined here. `EndpointPath` is free-text documentation and is not interpreted in Step 1.

### D4. File layout inside the package

- `doc.go` — package comment explaining the primitive and citing plan §9.
- `types.go` — `Classification`, `SurfaceKind`, `Surface`, `CanonicalProjection`, public constants.
- `registry.go` — `ProjectionRegistry`, `NewProjectionRegistry`, `Register`, `MustRegister`, internal helpers.
- `health.go` — `HealthStatus`, `ProjectionStatus`, `ProjectionHealth` enum, `HealthCheck` method.
- Tests co-located: `types_test.go`, `registry_test.go`, `health_test.go`.

### D5. `Register` vs `MustRegister`

- `Register(CanonicalProjection) error` — validates the entry; returns a descriptive error on any failure. Safe to call at any time; suitable for integration tests.
- `MustRegister(CanonicalProjection)` — wraps `Register`; panics on error. Intended for `cmd/node/main.go` startup where validation failure is a fatal diagnostic per PR-1..PR-4. Step 2 uses this; Step 1 tests exercise both entry points.

### D6. Thread-safety

`sync.RWMutex` guarding `entries` map. `Register`/`MustRegister` take the write lock; `HealthCheck` and getters take the read lock. Registration is expected at startup only but the lock is correct under any cadence.

### D7. No JSON tags (yet)

Step 8 will wire observability endpoints. Adding JSON tags now is speculative; they will be added when serialization has a real consumer. (Per CLAUDE.md "Don't design for hypothetical future requirements.")

### D8. `AllowIdleWithJustification` vs `IdleJustification` coupling

Plan §2.3 CR-9 requires the flag and justification to travel together. Implementation: validation rule — `(AllowIdleWithJustification && IdleJustification == "")` OR `(!AllowIdleWithJustification && IdleJustification != "")` ⇒ registration error. Only the both-set-and-non-empty form is accepted, plus the both-unset form for normal (non-idle) registrations.

**These eight decisions are load-bearing. Pause here for founder sign-off before proceeding to tasks.**

---

## Package structure (post-sign-off)

```
internal/projections/
  doc.go            package overview, cites plan §9 and CR-8/CR-9
  types.go          Classification, SurfaceKind, Surface, CanonicalProjection, StateProbe, public constants
  registry.go       ProjectionRegistry, NewProjectionRegistry, Register, MustRegister, Len, Get, List
  health.go         HealthStatus, ProjectionStatus, ProjectionHealth, HealthCheck
  types_test.go     validates Classification stringer, Surface shape, StateProbe aliasing
  registry_test.go  Register/MustRegister validation matrix, concurrency, idempotence
  health_test.go    HealthCheck across every PR-5 state + CR-9 exception + probe-error path
```

### Key type definitions (binding — all later tasks reference these)

```go
// types.go
package projections

import "context"

type Classification uint8

const (
    Canonical Classification = iota + 1  // deliberate non-zero default; zero value is invalid
    Advisory
)

// EligibilityWindow is the per-plan §16 locked value of 3 epochs.
// A Canonical projection that is still empty more than EligibilityWindow
// after registration fails HealthCheck.
const EligibilityWindow uint64 = 3

type SurfaceKind uint8

const (
    SurfaceNone SurfaceKind = iota
    SurfaceNodeLocalHTTP
    SurfaceCLI
    SurfaceHealth
    SurfacePublicAggregate
)

type Surface struct {
    Kind          SurfaceKind
    EndpointPath  string
    Justification string // required if Kind == SurfaceNone
}

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

// EventType is a string identifier for DAG event types. This package does
// not import internal/event to keep projections/ dependency-free. Step 2's
// registrations pass plain strings matching the event type constants.
type EventType string

// CanonicalProjection describes one registered projection per plan §9.2.
type CanonicalProjection struct {
    Name                 string
    Package              string
    StoreType            string
    Classification       Classification
    SourceEvents         []EventType
    LiveConsumerRef      string
    ReplayConsumerRef    string
    ObservabilitySurface Surface
    IntegrationTestRef   string
    Owner                string
    CreatedAt            string // ISO-8601 date; validated non-empty only

    // CR-9 named exception (plan §2.3). See docs/plans/2026-04-12-reputation-and-consensus-integrity.md §2.3.
    // AllowIdleWithJustification must be paired with a non-empty IdleJustification.
    // Reserved for ChallengeResolutionStore only in the initial workstream.
    AllowIdleWithJustification bool
    IdleJustification          string

    // StateProbe is called by HealthCheck to determine if the projection's
    // persistent state is empty. Required for Canonical entries that do not
    // use AllowIdleWithJustification; optional otherwise. Not in plan §9.2
    // but added in Step 1 per design decision D1 (see plan doc).
    StateProbe StateProbe
}
```

```go
// registry.go
type ProjectionRegistry struct {
    mu      sync.RWMutex
    entries map[string]registryEntry
    epochFn func() uint64
}

type registryEntry struct {
    projection         CanonicalProjection
    registeredAtEpoch  uint64
}

func NewProjectionRegistry(epochFn func() uint64) *ProjectionRegistry
func (r *ProjectionRegistry) Register(p CanonicalProjection) error
func (r *ProjectionRegistry) MustRegister(p CanonicalProjection)
func (r *ProjectionRegistry) Len() int
func (r *ProjectionRegistry) Get(name string) (CanonicalProjection, bool)
func (r *ProjectionRegistry) List() []CanonicalProjection // sorted by Name
```

```go
// health.go
type ProjectionHealth uint8

const (
    HealthOK ProjectionHealth = iota
    HealthNotYetEligible   // within EligibilityWindow — no action
    HealthAllowedIdle      // CR-9 exception — no action
    HealthEmpty            // past window AND probe returned empty — FATAL for Canonical
    HealthProbeFailed      // StateProbe returned an error — FATAL for Canonical
    HealthAdvisory         // Advisory classification — PR-5 does not apply; informational only
)

type ProjectionStatus struct {
    Name   string
    Health ProjectionHealth
    Reason string // human-readable diagnostic
}

type HealthStatus struct {
    Overall  ProjectionHealth // HealthOK unless any Canonical check is HealthEmpty/HealthProbeFailed
    Checks   []ProjectionStatus
}

func (r *ProjectionRegistry) HealthCheck(ctx context.Context) HealthStatus
```

---

## Validation matrix (Register / MustRegister)

Every failure case has an exact error message. These are tested in `registry_test.go`.

| # | Condition | Returned error (fmt string) |
|---|---|---|
| V1 | `Name == ""` | `projection registry: entry rejected: Name is required` |
| V2 | ~~duplicate `Name` already registered~~ **REPLACED by Idempotency Semantics (Clarification 3)** — see section below. Registering the same name twice is no longer a blanket failure. |  |
| V3 | invalid `Classification` (not Canonical or Advisory) | `projection registry: entry %q: Classification must be Canonical or Advisory` |
| V4 | `Classification == Canonical` AND `LiveConsumerRef == ""` | `projection registry: canonical entry %q: LiveConsumerRef is required (PR-1)` |
| V5 | `Classification == Canonical` AND `ReplayConsumerRef == ""` | `projection registry: canonical entry %q: ReplayConsumerRef is required (PR-2)` |
| V6 | `Classification == Canonical` AND `IntegrationTestRef == ""` | `projection registry: canonical entry %q: IntegrationTestRef is required (PR-3)` |
| V7 | `Classification == Canonical` AND `ObservabilitySurface.Kind == SurfaceNone` | `projection registry: canonical entry %q: ObservabilitySurface cannot be None (PR-4)` |
| V8 | `ObservabilitySurface.Kind == SurfaceNone` AND Advisory AND `Justification == ""` | `projection registry: advisory entry %q: ObservabilitySurface=None requires Justification (PR-4)` |
| V9 | `AllowIdleWithJustification == true` AND `IdleJustification == ""` | `projection registry: entry %q: AllowIdleWithJustification requires non-empty IdleJustification (CR-9)` |
| V10 | `AllowIdleWithJustification == false` AND `IdleJustification != ""` | `projection registry: entry %q: IdleJustification provided without AllowIdleWithJustification (CR-9)` |
| V11 | `Classification == Canonical` AND `!AllowIdleWithJustification` AND `StateProbe == nil` | `projection registry: canonical entry %q: StateProbe is required unless AllowIdleWithJustification is set (PR-5)` |
| V12 | `Package == ""` | `projection registry: entry %q: Package is required` |
| V13 | `StoreType == ""` | `projection registry: entry %q: StoreType is required` |
| V14 | `Owner == ""` | `projection registry: entry %q: Owner is required` |
| V15 | `CreatedAt == ""` | `projection registry: entry %q: CreatedAt is required` |
| V16 | `Classification == Canonical` AND `len(SourceEvents) == 0` | `projection registry: canonical entry %q: SourceEvents must be non-empty` |

`MustRegister` panics with `"projection registry: MustRegister failed: " + err.Error()`.

---

## Idempotency semantics (Clarification 3)

`Register` on a name that is already present does **not** always fail. The semantic is:

- **Exact match → no-op success.** If the new entry is byte-identical to the existing entry on every non-function field, `Register` returns `nil` and the existing entry (including its first-registered `StateProbe`) is retained unchanged. `Len()` does not increase. `registeredAtEpoch` is NOT updated.
- **Any non-function field differs → fail with field-naming error.** The error message identifies the specific field that mismatched and shows both values. This catches accidental re-definition at runtime, which is always a bug.
- **`StateProbe` differs but all non-function fields match → no-op success.** Go does not support function-value equality. `StateProbe` is intentionally excluded from the comparison; the first-registered probe is retained, the new probe is silently discarded. Document this in the `Register` doc comment.

**Why**: `cmd/node/main.go` wiring in Step 2 will call `Register` during startup. Replay, restart, or a lifecycle race can cause the same registration path to execute twice; idempotent-under-match prevents spurious startup failures. Fail-under-mismatch catches an accidental attempt to redefine a projection at runtime.

**Comparison algorithm**: field-by-field equality test (not `reflect.DeepEqual` on the whole struct, because that would return false on any StateProbe difference). For each non-function field in this order, use `!=` for scalar/comparable fields and `reflect.DeepEqual` for slice fields (`SourceEvents`):

1. `Package`
2. `StoreType`
3. `Classification`
4. `SourceEvents` (slice; use `reflect.DeepEqual`)
5. `LiveConsumerRef`
6. `ReplayConsumerRef`
7. `ObservabilitySurface` (struct of comparable fields; `!=` works)
8. `IntegrationTestRef`
9. `Owner`
10. `CreatedAt`
11. `AllowIdleWithJustification`
12. `IdleJustification`

The first mismatch returns the error; subsequent fields are not inspected. `Name` is implicitly equal (it is the map key used to find the existing entry).

**Error format** (exact string):
```
projection registry: entry %q: Register called with different %s: existing %q, new %q
```
where `%s` is the field name and `%q` is the stringified value. For non-string fields (`Classification`, `SourceEvents`, `ObservabilitySurface`, `AllowIdleWithJustification`), fall back to `%v` formatting.

---

## HealthCheck algorithm

Input: current epoch from `epochFn()`. For each registered entry, classified as follows:

```
if Classification == Advisory:
    status = HealthAdvisory; reason = "advisory projection; PR-5 does not apply"
    continue

// Canonical:
ageEpochs = currentEpoch - registeredAtEpoch
if ageEpochs < EligibilityWindow:
    status = HealthNotYetEligible; reason = fmt(...)
    continue

if AllowIdleWithJustification:
    status = HealthAllowedIdle; reason = IdleJustification
    continue

// StateProbe guaranteed non-nil (V11)
empty, err = StateProbe(ctx)
if err != nil:
    status = HealthProbeFailed; reason = err.Error()
    continue

if empty:
    status = HealthEmpty; reason = fmt("canonical projection empty after %d epochs (PR-5)", ageEpochs)
    continue

status = HealthOK; reason = ""
```

`Overall`:
- `HealthOK` if no `HealthEmpty` or `HealthProbeFailed` among Canonical entries.
- Else: the worst of the failing statuses (`HealthProbeFailed` ranks above `HealthEmpty` for operator diagnostics).

An empty registry returns `HealthStatus{Overall: HealthOK, Checks: nil}` (trivially healthy; plan §9.5 is about registered projections).

**Underflow safety**: `epochFn()` should never return a value less than `registeredAtEpoch`. If it does (operator reset clock backwards), HealthCheck treats the entry as `HealthNotYetEligible` rather than overflowing the subtraction. Tested.

---

## Test plan

Every invariant (PR-1..PR-5, CR-9 coupling, V1..V16) has at least one dedicated test. Tests use a mutable-counter epoch function: `epoch := uint64(0); epochFn := func() uint64 { return atomic.LoadUint64(&epoch) }`.

### `types_test.go`

- [ ] **T1.1 — Classification zero value is invalid.** Verifies the zero value of `Classification` is neither `Canonical` nor `Advisory`. Ensures validation V3 fires if a caller passes a zero-initialized struct.
- [ ] **T1.2 — Surface shape.** Constructs surfaces with each `SurfaceKind`; verifies `EndpointPath` and `Justification` fields are readable.
- [ ] **T1.3 — EligibilityWindow locked at 3.** Asserts `EligibilityWindow == 3` (plan §16). Prevents silent constant drift.

### `registry_test.go`

- [ ] **T2.1 — Empty registry is well-formed.** `NewProjectionRegistry(...).Len() == 0; .List() returns empty slice; .Get("x") returns (_, false)`.
- [ ] **T2.2 — Register happy path (Canonical).** A fully-populated Canonical entry with a non-nil `StateProbe` registers. `Len == 1; Get(name) returns the entry`.
- [ ] **T2.3 — Register happy path (Advisory with SurfaceNone+justification).** Advisory entry with `SurfaceNone` and non-empty `Justification` registers.
- [ ] **T2.4a — Idempotent re-register with identical fields.** Register a fully-populated Canonical entry at epoch=5. Register the same entry again at epoch=7. Second call returns `nil`. `Len() == 1`. Verify via HealthCheck behavior at a later epoch that `registeredAtEpoch` is still 5 (not updated to 7).
- [ ] **T2.4b — Mismatch on `LiveConsumerRef` fails.** Register; re-register with `LiveConsumerRef` changed. Expect error containing `LiveConsumerRef`, the old value, and the new value.
- [ ] **T2.4c — Mismatch on `ReplayConsumerRef` fails.** Same as T2.4b for `ReplayConsumerRef`.
- [ ] **T2.4d — Mismatch on `IntegrationTestRef` fails.** Same pattern.
- [ ] **T2.4e — Mismatch on `Classification` fails.** Same pattern.
- [ ] **T2.4f — Mismatch on `Owner` fails.** Same pattern.
- [ ] **T2.4g — Mismatch on `AllowIdleWithJustification`/`IdleJustification` pair fails.** Re-register with both flag and justification changed to a valid idle form (or vice versa); expect error naming whichever field differs first in the comparison order (`AllowIdleWithJustification`).
- [ ] **T2.4h — Different `StateProbe`, identical non-function fields, no-op success.** Register with probe A. Re-register with probe B (different closure). Second call returns `nil`. First probe is retained: verify by forcing the entry past the eligibility window with an empty-returning probe A as the first registration; the HealthCheck calls probe A's closure (asserted via a captured counter), not probe B's.
- [ ] **T2.4i — Mismatch on `SourceEvents` fails.** Slice field; test that `reflect.DeepEqual` catches differences.
- [ ] **T2.4j — Mismatch on `Package`/`StoreType`/`CreatedAt`/`ObservabilitySurface.Kind` fails.** One sub-test per remaining non-function field.
- [ ] **T2.5..T2.19 — Each validation row V1..V16 is a test case** (except V2 which is replaced by T2.4a–T2.4j above). Parameterized: construct a "good" base entry, mutate one field to the failing form, assert the exact error message. One test case per row.
- [ ] **T2.20 — MustRegister success does not panic.** A valid entry via MustRegister; Len == 1.
- [ ] **T2.21 — MustRegister panic on invalid.** Invalid entry via MustRegister; recover and verify panic value contains the expected error string.
- [ ] **T2.22 — Concurrent reads while registering.** Goroutine A calls Register in a loop; goroutines B–D call Len/Get/List concurrently. `-race` must pass.
- [ ] **T2.23 — List is sorted by Name.** Register entries in reverse alphabetical order; List returns sorted.
- [ ] **T2.24 — Register stores registeredAtEpoch.** Use a mutable epoch; register at epoch=5; verify via internal accessor (or HealthCheck behavior in T3) that the entry's registration epoch is 5.

### `health_test.go`

Each of the following uses a shared helper `newTestEntry(name, opts...)` that constructs a valid Canonical entry with overrideable fields.

- [ ] **T3.1 — Empty registry is HealthOK.** `HealthCheck(ctx).Overall == HealthOK; Checks == nil`.
- [ ] **T3.2 — Advisory entry returns HealthAdvisory regardless of probe/state.** Past eligibility window, probe returns empty; overall still HealthOK; entry status HealthAdvisory.
- [ ] **T3.3 — Canonical within window returns HealthNotYetEligible.** Register at epoch=0; check at epoch=1 (ageEpochs=1 < 3). Status HealthNotYetEligible. Overall HealthOK.
- [ ] **T3.4 — Canonical exactly at window boundary is still not yet eligible.** Register at epoch=0; check at epoch=2 (ageEpochs=2 < 3). Still HealthNotYetEligible. Confirms "more than EligibilityWindow" is strict inequality, consistent with plan §9.5 "more than EligibilityWindow epochs".
- [ ] **T3.5 — Canonical past window with populated state returns HealthOK.** Register at epoch=0; probe returns (empty=false); check at epoch=4. Status HealthOK.
- [ ] **T3.6 — Canonical past window with empty state returns HealthEmpty and flips Overall.** Register at epoch=0; probe returns (empty=true); check at epoch=4. Status HealthEmpty; Overall HealthEmpty.
- [ ] **T3.7 — Canonical past window with probe error returns HealthProbeFailed and flips Overall.** Probe returns `(false, errors.New("boom"))`. Status HealthProbeFailed; Overall HealthProbeFailed.
- [ ] **T3.8 — AllowIdleWithJustification past window returns HealthAllowedIdle.** Register a Canonical entry with `AllowIdleWithJustification=true, IdleJustification="producer deferred per CR-9 to challenge-path workstream"`. No probe (nil allowed under V11 exception). Status HealthAllowedIdle; Overall HealthOK.
- [ ] **T3.9 — HealthProbeFailed ranks above HealthEmpty in Overall.** Register two entries: one past-window empty, one past-window probe-error. Overall == HealthProbeFailed.
- [ ] **T3.10 — Mixed registry.** One Advisory, one Canonical-NotYetEligible, one Canonical-OK, one AllowIdle. Overall HealthOK; four entries in Checks, each classified correctly.
- [ ] **T3.11 — Clock-backwards safety.** Register at epoch=5; query epoch=3 (operator reset). Verify no integer underflow; status is HealthNotYetEligible.
- [ ] **T3.12 — Probe receives caller context.** Probe stores `ctx` in a closure; test passes a `context.WithValue(...)`; verifies the value reaches the probe.
- [ ] **T3.13 — Mandatory restart semantic (Clarification 2).** Exercises that `registeredAtEpoch` is captured once and resets on a fresh registry. Sequence:
  1. Create registry R1 with synthetic epoch clock at 0.
  2. Register a Canonical projection with a StateProbe that always returns `(empty=true, nil)`.
  3. Advance clock to epoch 2. `R1.HealthCheck(ctx).Overall == HealthOK`; the projection's entry status is `HealthNotYetEligible`.
  4. Advance clock to epoch 4. `R1.HealthCheck(ctx).Overall == HealthEmpty`; the projection's entry status is `HealthEmpty`.
  5. Create a **fresh** registry R2 with the same synthetic clock now at epoch 10. Register the same projection (identical fields, same probe).
  6. Advance clock to epoch 12. `R2.HealthCheck(ctx).Overall == HealthOK`; the projection's entry status is `HealthNotYetEligible`. This proves the 3-epoch window resets per registry and is not tied to protocol-wide time.

---

## Execution — bite-sized task list

Each task is TDD: write failing test, run it, implement, run, commit. The branch is `feat/projections-registry-step-1` created from `main` at the session start.

### Task 0: Create branch and skeleton

- [ ] **Step 0.1 — Create branch.**
  ```bash
  cd ~/aethernet && git switch -c feat/projections-registry-step-1 main
  ```
- [ ] **Step 0.2 — Create directory.**
  ```bash
  mkdir -p internal/projections
  ```
- [ ] **Step 0.3 — Write `doc.go`.**
  ```go
  // Package projections defines the protocol-primitive registry of Canonical
  // and Advisory projections that derive protocol-affecting state from the
  // DAG. It closes the "writer exists, caller doesn't" class of bugs by
  // requiring every durable, consensus-adjacent store to be registered with
  // a live consumer, replay consumer, integration test, and observability
  // surface before the node can claim healthy status.
  //
  // Binding spec: docs/plans/2026-04-12-reputation-and-consensus-integrity.md §9
  // (registry), §2.3 (CR-8/CR-9 named exceptions), §16 (EligibilityWindow=3).
  package projections
  ```
- [ ] **Step 0.4 — Commit skeleton (empty package builds).**
  ```bash
  go build ./internal/projections/...
  git add internal/projections/doc.go
  git commit -m "feat(projections): package skeleton"
  ```

### Task 1: Core types (T1.1–T1.3)

- [ ] **Step 1.1 — Write `types_test.go` with T1.1, T1.2, T1.3.** Tests reference the types that will exist after Step 1.2.
- [ ] **Step 1.2 — Run; expect FAIL** (undefined types).
  ```bash
  go test -race ./internal/projections/... 2>&1 | head -20
  ```
- [ ] **Step 1.3 — Write `types.go`** with `Classification`, `SurfaceKind`, `Surface`, `EventType`, `StateProbe`, `CanonicalProjection`, `EligibilityWindow` per definitions above.
- [ ] **Step 1.4 — Run; expect PASS.**
  ```bash
  go test -race ./internal/projections/...
  ```
- [ ] **Step 1.5 — Commit.**
  ```bash
  git add internal/projections/types.go internal/projections/types_test.go
  git commit -m "feat(projections): add types and EligibilityWindow"
  ```

### Task 2: Registry with validation matrix (T2.1–T2.24, V1–V16)

- [ ] **Step 2.1 — Write `registry_test.go`** covering T2.1–T2.24 with all V1–V16 rows as subtests. Include the `newTestEntry` helper used by both registry and health tests (in a shared `testing_helpers_test.go` or at the top of `registry_test.go`).
- [ ] **Step 2.2 — Run; expect FAIL** (types exist, registry does not).
- [ ] **Step 2.3 — Write `registry.go`** with `NewProjectionRegistry`, `registryEntry`, `Register`, `MustRegister`, `Len`, `Get`, `List`, and the full validation matrix per the table above. Use `errors.New`/`fmt.Errorf`, sorted `List` via `sort.Slice`, `sync.RWMutex`.
- [ ] **Step 2.4 — Run; expect PASS.**
  ```bash
  go test -race ./internal/projections/...
  ```
- [ ] **Step 2.5 — Run `go vet`.**
  ```bash
  go vet ./internal/projections/...
  ```
- [ ] **Step 2.6 — Commit.**
  ```bash
  git add internal/projections/registry.go internal/projections/registry_test.go
  git commit -m "feat(projections): registry with Register/MustRegister validation"
  ```

### Task 3: HealthCheck (T3.1–T3.12)

- [ ] **Step 3.1 — Write `health_test.go`** covering T3.1–T3.12.
- [ ] **Step 3.2 — Run; expect FAIL** (HealthCheck undefined).
- [ ] **Step 3.3 — Write `health.go`** with `ProjectionHealth` enum, `ProjectionStatus`, `HealthStatus`, and the `HealthCheck` method per algorithm above. Include clock-backwards safety.
- [ ] **Step 3.4 — Run; expect PASS.**
  ```bash
  go test -race ./internal/projections/...
  ```
- [ ] **Step 3.5 — Run full build (verify no cross-package breakage).**
  ```bash
  go build ./...
  go vet ./...
  ```
- [ ] **Step 3.6 — Commit.**
  ```bash
  git add internal/projections/health.go internal/projections/health_test.go
  git commit -m "feat(projections): HealthCheck with PR-5 and CR-9 semantics"
  ```

### Task 4: Squash-merge preparation (single final commit)

The per-task commits above are the TDD record. Per the step prompt ("One commit on a fresh branch. Commit message: `feat(projections): add projection registry package primitive`"), the final shipped artifact is one commit.

- [ ] **Step 4.1 — Reset the branch to squash into one commit.**
  ```bash
  git reset --soft main
  git status
  ```
- [ ] **Step 4.2 — Verify staged tree matches expected manifest:**
  ```
  internal/projections/doc.go
  internal/projections/types.go
  internal/projections/types_test.go
  internal/projections/registry.go
  internal/projections/registry_test.go
  internal/projections/health.go
  internal/projections/health_test.go
  docs/plans/2026-04-12-reputation-step-1-projection-registry.md
  ```
  No other paths. If any file outside `internal/projections/` or this plan doc appears, STOP.
- [ ] **Step 4.3 — Create the single commit.**
  ```bash
  git commit -m "$(cat <<'EOF'
  feat(projections): add projection registry package primitive

  Introduces internal/projections/ implementing plan §9 (projection
  registry) as a standalone primitive with no cross-package wiring.
  Provides Classification, CanonicalProjection, Surface types; a
  thread-safe ProjectionRegistry with Register/MustRegister validating
  PR-1..PR-4; and HealthCheck implementing PR-5 with CR-9 named-exception
  handling. Zero projections are registered in this step; retrofit is
  step 2.

  Binding spec: docs/plans/2026-04-12-reputation-and-consensus-integrity.md

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```
- [ ] **Step 4.4 — Final verification.**
  ```bash
  go test -race ./internal/projections/...
  go vet ./internal/projections/...
  go build ./...
  git log --oneline -3
  git diff --stat main..HEAD
  ```
  Expected: one commit on the branch; diff stat shows only `internal/projections/` (7 files) and `docs/plans/2026-04-12-reputation-step-1-projection-registry.md`.

---

## Verification-before-done checklist

Matches Step 1 prompt "Verification before done":

- [ ] `go test -race ./internal/projections/...` passes with zero failures.
- [ ] `go vet ./internal/projections/...` clean.
- [ ] `go build ./...` from repo root passes.
- [ ] Every field of `CanonicalProjection` required by plan §9.2 is present and has a test referencing it.
- [ ] Invariants:
  - PR-1 (V4) — tested.
  - PR-2 (V5) — tested.
  - PR-3 (V6) — tested at the field-completeness level. **Existence-in-codebase check is explicitly deferred to Step 3.**
  - PR-4 (V7, V8) — tested.
  - PR-5 — tested via T3.3–T3.9, T3.11.
- [ ] `AllowIdleWithJustification` + `IdleJustification` coupling (CR-9) — tested V9, V10, T3.8.
- [ ] Plan updated if implementation diverged. If any of D1–D8 is revised during implementation, update this plan doc in Step 4.2 before the final commit.
- [ ] No testnet verification required for this step (no registration, no node wiring).

---

## Explicit deferrals — flagged for future steps

| Item | Deferred to |
|---|---|
| Retrofit existing stores (`EvidenceStore`, `CalibrationStore`, `EscrowStore`, `LedgerStore`, `GenerationLedgerCalculator`, `AgentReputation`, `BlobServingReputation`) | Step 2 |
| Wiring registry into `cmd/node/main.go` | Step 2 |
| CI static check (plan §9.7 item 1, type-graph scan for writer-without-caller) | Step 3 |
| Existence check for `IntegrationTestRef` (scan repo for referenced test symbols; plan §9.4 PR-3 "test reference that does not exist fails CI") | Step 3 |
| Integration tests driving real events through live consumers (plan §9.7 item 3) | Step 2 (per-projection) |
| Code-review checklist (plan §9.7 item 5) | Process / workstream closeout |
| Observability endpoint implementation behind `Surface` | Step 8 |
| `ChallengeResolutionStore` actual registration (CR-9 is exercised in Step 1 via synthetic test entries only) | Step 4 |
| Principle 16 amendment to `docs/design-principles.md` | Step 10 |
| `docs/projection-registry.md` | Step 11 |

---

## Subagent strategy

Per Step 1 prompt and CLAUDE.md §2: the work is contained to one new package with no cross-subsystem touches, so one subagent under the State & Consensus boundary is sufficient. Subagent brief (to be handed to the implementer subagent after sign-off):

1. Read: CLAUDE.md, design-principles.md (principles 5, 6, 11, 12, 14, 15), lessons.md (concurrency/locking section), and this plan doc in full.
2. Also read plan §9, §2.3, §16 of `docs/plans/2026-04-12-reputation-and-consensus-integrity.md`.
3. Implement Tasks 0–4 above in order. TDD each task (failing test → minimal code → passing test → commit).
4. Do not modify any file outside `internal/projections/` or this plan doc.
5. Do not register any projection.
6. Squash to one commit on `feat/projections-registry-step-1` per Task 4.
7. Report back: commit hash, branch name, `go test -race` output (green), and any design divergences from this plan.

---

## Self-review

**Spec coverage (Step 1 prompt):**
- `internal/projections/` package — Tasks 0, 1, 2, 3.
- `Classification` enum — T1.1, types.go.
- `CanonicalProjection` struct per §9.2 including `IntegrationTestRef` — types.go; tested via V6.
- `Surface` type — types.go + T1.2.
- `ProjectionRegistry` map + thread-safe read — registry.go + T2.22.
- Explicit `Register` method — Task 2 + V1–V16.
- `MustRegister` semantics — Task 2 + T2.20, T2.21.
- `HealthCheck()` structured result — Task 3 + T3.1–T3.12.
- PR-5 mechanics with EligibilityWindow — T3.3–T3.9, T3.11.
- `AllowIdleWithJustification` + `IdleJustification` with CR-9 semantics — V9, V10, T3.8.
- Tests in isolation — all tests are `internal/projections/*_test.go`, no external imports.
- Package compiles; `go test -race ./internal/projections/...` passes — Step 3.4, Step 4.4.

**Out-of-scope compliance:**
- Zero projections registered — confirmed by T2.1 and by the lack of any real registration in code.
- Registry not wired into `cmd/node/main.go` — Task 4.2 manifest check.
- No CI tooling — explicit deferral table.
- No integration tests with real events — only synthetic probes in tests.
- No changes outside `internal/projections/` or `docs/plans/` — Task 4.2 manifest check.
- No observability endpoints — `Surface` is data only; confirmed by D3.

**Placeholder scan:** no "TBD", no "implement later", every validation row has an exact error string, every test has explicit inputs and expected outputs.

**Type consistency:** `CanonicalProjection`, `ProjectionRegistry`, `ProjectionHealth`, `ProjectionStatus`, `HealthStatus`, `Surface`, `SurfaceKind`, `Classification`, `EventType`, `StateProbe`, `EligibilityWindow`, `Canonical`, `Advisory`, `SurfaceNone`, `SurfaceNodeLocalHTTP`, `SurfaceCLI`, `SurfaceHealth`, `SurfacePublicAggregate`, `HealthOK`, `HealthNotYetEligible`, `HealthAllowedIdle`, `HealthEmpty`, `HealthProbeFailed`, `HealthAdvisory` — all used consistently across types/registry/health sections.

**Citations to binding plan**: every design decision (D1–D8, validation rows, health algorithm, EligibilityWindow, CR-9 exception) cites its source §. PR-3 CI-side deferral is explicitly flagged.

---

## Sign-off request

Decisions **D1 (StateProbe field)** and **D2 (epoch injection)** go beyond the letter of plan §9.2 and are flagged here per §18. Please confirm or redirect before implementation begins:

1. **D1**: Add `StateProbe func(ctx) (bool, error)` to `CanonicalProjection` (recommended), or prefer a separate `RegisterStateProbe` API, or pass probes via `HealthCheck` parameter?
2. **D2**: `NewProjectionRegistry(epochFn func() uint64)` capturing registration epoch at `Register` time (recommended), or explicit `HealthCheck(currentEpoch)` + `Register(entry, registeredAtEpoch)`?

All other decisions (D3 Surface shape, D4 file layout, D5 Register/MustRegister split, D6 RWMutex, D7 no JSON tags, D8 CR-9 coupling) are implementation details within the plan's authority; please flag if any needs revision.

**No code will be written until sign-off.**
