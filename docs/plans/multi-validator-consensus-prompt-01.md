# Prompt 01 Plan — TaskVerificationRound Model and Persistence Scaffolding

**Date:** 2026-04-07
**Status:** Awaiting approval

## Objective

Create `internal/taskverification/` with the `TaskVerificationRound` data model, state machine, canonical serialization, and BadgerDB-backed persistence. Purely additive — no callers, no integration.

## Package Layout

```
internal/taskverification/
  doc.go                  — package documentation
  types.go                — RoundID, RoundState, Verdict enums, constants
  types_test.go           — deterministic ID generation, enum serialization
  errors.go               — sentinel errors
  round.go                — TaskVerificationRound struct, state machine, canonical encoding
  round_test.go           — state transitions, deadline, canonical roundtrip
  store.go                — Store interface
  badger_store.go         — BadgerDB Store implementation
  store_test.go           — persistence round-trip, secondary indexes, concurrency
```

## Types

### RoundID (types.go)

```go
type RoundID string

func NewRoundID(submissionEventID event.EventID) RoundID
```

Constructor: `SHA-256("tvr:" + submissionEventID)` hex-encoded. Deterministic — same submission event ID always produces the same round ID across all nodes. The `"tvr:"` prefix prevents collisions with event IDs.

### RoundState (types.go)

```go
type RoundState int

const (
    RoundStateOpen             RoundState = 0
    RoundStateFinalizedAccept  RoundState = 1
    RoundStateFinalizedReject  RoundState = 2
    RoundStateDisputed         RoundState = 3
    RoundStateExpired          RoundState = 4
)
```

JSON serializes as `"open"`, `"finalized_accept"`, `"finalized_reject"`, `"disputed"`, `"expired"`.

### Verdict (types.go)

```go
type Verdict int

const (
    VerdictPass    Verdict = 0
    VerdictFail    Verdict = 1
    VerdictAbstain Verdict = 2
)
```

JSON serializes as `"pass"`, `"fail"`, `"abstain"`.

### Constants (types.go)

```go
DefaultAcceptanceThresholdBP = 6000   // 0.60
DefaultRoundDeadlineSeconds  = 60
DefaultRoundExtensionSeconds = 60
DefaultDiversityFloor        = 2
```

## Round Struct (round.go)

Fields match the design doc exactly. Uses `crypto.AgentID` (which is `type AgentID string`). The `Event.AgentID` field is plain `string`, but internal models use the typed alias.

### State Machine

Valid transitions (all from `Open` only):
- `Open → FinalizedAccept`
- `Open → FinalizedReject`
- `Open → Disputed`
- `Open → Expired`

All four terminal states have no valid outbound transitions.

### Canonical Encoding

Pattern: `json.Marshal` a canonical projection struct (sorted slices for maps), then `jcs.Canonicalize`. This matches the existing `event.ComputeID` and `crypto.CanonicalBytes` patterns.

For `ParticipatingFamilies` (map): convert to sorted `[]struct{Key,Value}` slice before marshaling.
For `ScoreBreakdown` in vote records: same treatment.
For `Votes` slice: sort by `ValidatorID` (string comparison) for deterministic ordering.

`Canonical() ([]byte, error)` returns JCS-canonicalized JSON bytes.
`RoundFromCanonical(data []byte) (*TaskVerificationRound, error)` deserializes.

## Persistence Schema (badger_store.go)

Key prefixes:
- `tv:round:<round_id>` → canonical round JSON bytes
- `tv:by_sub:<submission_event_id>` → round_id bytes (secondary index)
- `tv:by_task:<task_id>:<round_id>` → round_id bytes (secondary index)
- `tv:by_state:<state_int>:<round_id>` → round_id bytes (secondary index)

All writes use a single Badger `Update` transaction for atomicity.

`SaveRound` detects new-vs-update by checking if the primary key exists. On update, the old state index entry is deleted and the new one is written in the same transaction.

A `sync.Mutex` guards the new-vs-update read-then-write sequence to prevent TOCTOU races between concurrent `SaveRound` calls for the same round.

## Test Strategy

### types_test.go
- `TestRoundID_DeterministicGeneration` — same event ID → same round ID
- `TestRoundID_Different` — different event IDs → different round IDs
- `TestRoundState_StringRoundtrip` — every state String() → parse back
- `TestVerdict_StringRoundtrip` — every verdict String() → parse back
- `TestRoundState_JSONMarshal` — serializes as string, not int
- `TestVerdict_JSONMarshal` — serializes as string, not int

### round_test.go
- `TestRound_StateTransitions_Valid` — table-driven, all 4 valid transitions
- `TestRound_StateTransitions_Invalid` — table-driven, all invalid transitions including terminal→anything
- `TestRound_DeadlineForCurrentPhase_Original` — not extended → DeadlineUnix
- `TestRound_DeadlineForCurrentPhase_Extended` — extended → ExtendedUntilUnix
- `TestRound_DistinctPassFamilies` — empty, one, multiple families
- `TestRound_Canonical_Roundtrip` — encode → decode → re-encode, byte-for-byte equal
- `TestRound_Canonical_DeterministicMapOrder` — maps with different insertion order → same bytes

### store_test.go (using temp Badger dir per test)
- `TestBadgerStore_SaveAndLoadRound` — full field round-trip
- `TestBadgerStore_LoadRoundBySubmissionEvent` — secondary index lookup
- `TestBadgerStore_LoadRoundsByTaskID` — multiple rounds for same task
- `TestBadgerStore_StateTransitionPersistence` — save Open → transition → save again → ListRoundsByState reflects change
- `TestBadgerStore_DeleteRound` — all secondary indexes cleaned up
- `TestBadgerStore_ListOpenRounds` — returns only Open rounds
- `TestBadgerStore_ConcurrentWrites` — two goroutines writing different rounds under `-race`
- `TestBadgerStore_DuplicateRoundID` — saving same round twice updates, not errors

## Dependencies

Only existing codebase dependencies:
- `github.com/dgraph-io/badger/v4`
- `github.com/Aethernet-network/aethernet/internal/event` (for EventID)
- `github.com/Aethernet-network/aethernet/internal/crypto` (for AgentID)
- `github.com/Aethernet-network/aethernet/internal/jcs` (for Canonicalize)

No new external dependencies.

## What Is NOT In Scope

- No event type definitions (prompt 03)
- No recognition consumers (prompt 02)
- No analyzer registry (prompt 04)
- No autovalidator changes (prompt 05)
- No finalization logic (prompt 06)
- No settlement integration (prompt 07)
- No wiring in cmd/node/main.go
