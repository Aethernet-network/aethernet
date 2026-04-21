# Canonical payload float-freedom lint

Enforces the invariant that every canonical event payload type in
`internal/event/` is float-free transitively. Defense-in-depth: paired
with a reflection-based backstop at
`internal/event/canonical_payload_reflection_test.go`.

The invariant exists because floating-point arithmetic is non-deterministic
across hardware, and events reach cross-node consensus via content
addressing of canonical bytes. A float field anywhere in a payload can
corrupt integrity. See `docs/design-principles.md` Principle 11 and
`docs/plans/2026-04-20-canonical-distribution-integer-migration-v2.md`
§4.4.

## What the lint rejects

Any path from a canonical payload type to a field of any of these:

- `float32`, `float64`
- `complex64`, `complex128`
- `interface{}` / `any` — can hold any runtime type, including float
- `encoding/json.RawMessage` — can encode floats in JSON bytes
- Generic type parameters — may be instantiated with float

Rejection applies transitively through:

- Nested structs (named or anonymous)
- Pointers
- Slices and arrays
- Map keys and values
- Named type aliases whose underlying type is float

Cycles (self-referential types via slice, pointer, etc.) are handled by
a visited-set; the lint terminates and reports only real float paths.

## How the lint runs

Two independent mechanisms enforce the same invariant:

1. **AST lint** (`internal/event/lint/canonical_float_lint.go`).
   Runs under `go test ./internal/event/lint/` as `TestCanonicalFloatLint`.
   Walks the 17 canonical payload types via `go/types` at CI time; fails
   the test (and therefore the build) on any float-bearing field path.

2. **Reflection test** (`internal/event/canonical_payload_reflection_test.go`).
   Runs under `go test ./internal/event/` as
   `TestCanonicalPayloadTypes_FloatFree`. Walks the same types via
   `reflect` and asserts float-freedom. Uses a different mechanism so a
   bug in one defense does not mask a regression the other catches.

A bug in the type-walker of mechanism 1 does not save a bad type from
mechanism 2, and vice versa.

## The 17 canonical payload types

The authoritative list is hardcoded in two places kept in sync by the
reflection test's drift-check (`TestCanonicalPayloadList_Complete`):

- `internal/event/lint/canonical_float_lint.go` — `canonicalPayloadTypeNames`
- `internal/event/canonical_payload_reflection_test.go` — `canonicalPayloadReflectTypes`

The current list:

```
TransferPayload                    GenerationPayload
AttestationPayload                 VerificationPayload
DelegationPayload                  RegistrationPayload
GenesisFundingPayload              TaskPostedPayload
TaskClaimedPayload                 TaskSubmittedPayload
TaskApprovedPayload                TaskDisputedPayload
TaskVerificationVotePayload        TaskVerificationConsensusPayload
SlashingChallengePayload           PrerequisiteWithholdingPayload
TrajectoryCommitPayload
```

## Adding a new canonical payload type

1. Add the struct declaration in `internal/event/` following the existing
   pattern (integer-only field types; no floats).
2. Add the type name to `canonicalPayloadTypeNames` in
   `internal/event/lint/canonical_float_lint.go`.
3. Add `reflect.TypeOf(NewPayload{})` to `canonicalPayloadReflectTypes` in
   `internal/event/canonical_payload_reflection_test.go`.
4. Update `TestCanonicalPayloadList_Has17Entries` in both test files to
   the new count.
5. Run `go test ./internal/event/...` to verify both defenses accept
   the new type.

The drift-check test
(`TestCanonicalPayloadList_Complete`) catches step 2 omissions: if you
declare a new `*Payload` type in `internal/event/` and forget to add it
to the lists, the test fails with a message pointing to the lists.

## Why the `Event` envelope is not in the list

`internal/event/event.go` declares `Event` with `Payload json.RawMessage`.
The `Event` struct is the generic wire envelope — its `Payload` field
carries the bytes of the concrete payload and is deliberately typed as
`json.RawMessage` for deferred decoding. The float-freedom invariant
applies to the *concrete payload types*, which are the 17 listed above.
`Event` itself is not in the list.

## What fails on violation

`TestCanonicalFloatLint` emits:

```
canonical payload float-freedom violation: VerificationPayload.Confidence: float64
canonical payload float-freedom violation: TaskVerificationVotePayload.Breakdown[<val>]: float64

→ to fix: change the field to an integer type (see docs/plans/2026-04-20-
canonical-distribution-integer-migration-v2.md §4.1 for BasisPoints),
or if the field is genuinely non-canonical, split the type so the
canonical half is float-free.
```

`TestCanonicalPayloadTypes_FloatFree` emits one `t.Errorf` per violation
with the same path format.

## Pattern and dependencies

Follows the established lint pattern of `internal/dispatch/lint/` and
`internal/projections/lint/`: library `Check()` function, test-integrated
CI gate via `go test ./...`, invoked automatically without any Makefile
or build-tag setup. External dependencies: only
`golang.org/x/tools/go/packages`, already used elsewhere in the repo.
