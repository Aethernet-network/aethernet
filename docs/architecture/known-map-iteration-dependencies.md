# Known map-iteration dependencies

Per F4 plan v2 §6.3 invariant E-1: every `range over map` in production code is classified as Safe or sort-fixed; **Unsafe-inherently** instances must be enumerated here with explicit reasoning.

This file is the registry of map iterations whose order dependency cannot be removed by sorting alone — they require a structural change to fix. New entries land here only with architect-session approval.

## Active entries

**None as of 2026-04-22.**

The complete F4A Part E audit (commits `432a266` for Priority 1, this commit for Priority 2 + Priority 3) classified all 123 production `range over map` callsites as Safe, Safe-with-note, or Unsafe-without-sort. The four Unsafe-without-sort sites were all fixed by adding lex-sort steps before iteration:

- `internal/dispatch/dispatcher.go` (3 sites — Priority 1, fixed in `432a266`)
- `internal/dispatch/prerequisites.go:46` (Priority 1 micro-fix, fixed in this commit)
- `internal/recognition/bus.go:dispatch()` (Priority 2, fixed in this commit)
- `internal/dag/dag.go:Tips/PrimaryTips/LocalTips` (Priority 2, fixed in this commit)
- `internal/escrow/escrow.go:ReleaseSettlement` (Priority 3, fixed in this commit)

No surface required structural redesign; all four were addressed by introducing a `sort.Slice` (or equivalent) on the keyed slice before iteration.

## Why structural-fix sites would land here

A site qualifies as Unsafe-inherently when one of:

1. The iteration produces **out-of-band side effects whose ordering is observable cross-node** (e.g., emitting canonical events whose CausalRefs depend on iteration-time DAG tips), AND those side effects cannot be deferred or batched into a sort-friendly producer/consumer split.
2. The iteration drives a **decision based on first-seen-best**, where "best" is a tie-break against a non-canonical signal (e.g., wall-clock arrival), AND the decision is canonical (recorded into shared state by every node).
3. The iteration produces **a stream whose total length is bounded by a non-deterministic stop condition** (e.g., "iterate until the bucket runs dry"), where the stop point depends on iteration order.

The closest production surface that brushed any of these criteria during the F4A audit was `internal/escrow/escrow.go:ReleaseSettlement`. It does NOT qualify as inherent because the recipient amounts are pre-computed deterministically by the settler, so the bucket cannot run dry mid-iteration — the iteration order only affects the synthetic event-ID counter sequence, which sorts away. (See `2026-04-22-map-iteration-determinism.md` E.P3 finding `escrow-distribution-unsorted` for the full reasoning.)

## How to add an entry

1. Document the site (file, line, surface) and the explicit reasoning that places it under one of the criteria above.
2. Open an architect session before merging the entry; structural fixes carry workstream-level scope and need cross-cutting review.
3. Once added, the corresponding `range` site should carry an inline `// inherently-unsafe: see docs/architecture/known-map-iteration-dependencies.md#<anchor>` comment so the E-2 lint does not flag it (and so future readers find the reasoning without grepping commits).

## Cross-references

- F4 plan v2 §6: Part E — Map-iteration audit and remediation.
- Audit document: `docs/audits/2026-04-22-map-iteration-determinism.md`.
- E-2 CI lint: `internal/dispatch/lint/map_iteration.go` (test gate `TestMapIterationLint`).
