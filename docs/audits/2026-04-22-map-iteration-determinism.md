# Map-iteration determinism audit

**Status**: F4A step 7 (Part E Priority 1) — completed 2026-04-22 against `feat/selection-consistency-fix` @ `4658209`.
**Scope**: Priority 1 — logical-key consumers, dispatcher core, settlement (`internal/dispatch/`, `internal/settlement/`).
**Plan reference**: F4 plan v2 §6 (Part E).

Priority 2 (`internal/recognition/`, `internal/store/`, `internal/network/`) and Priority 3 (everything else, ~470 callsites) are tracked for F4A step 11. This document covers Priority 1 only and will be amended when 2 + 3 close.

---

## Classification taxonomy

- **Safe**: iteration order does not affect any observable outcome (canonical state, error semantics, cross-node-visible diagnostic).
- **Safe-with-note**: iteration order affects observable diagnostic only (e.g., which of N equivalent error messages is reported first); no canonical-state effect. Sorting is hygienic, not load-bearing.
- **Unsafe-without-sort**: iteration order can affect canonical state or cross-node-visible behavior. Fix: sort keys before iteration.
- **Unsafe-inherently**: order dependency cannot be removed by sorting; needs structural fix. Tracked for follow-up.

---

## Priority 1 callsites and dispositions

### `internal/dispatch/dispatcher.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 170 | `d.consumers` (`map[string]Consumer`) | **Unsafe-without-sort** | **Fixed in this commit**: lex-sort consumer names before iterating in `snapshotInterestedConsumers`. The returned `[]Consumer` order propagates into the `AdmissionRecord.Consumers` map's first-write order and the sequence of `safeApply` invocations in `invokeConsumers` — observable cross-node when consumers count > 1. With one registered consumer today this is decorative; load-bearing the moment a second registers. |
| 257 | `rec.Consumers` (`map[string]ConsumerStatus`) | **Unsafe-without-sort** | **Fixed**: `invokeConsumers` lex-sorts consumer names before iterating. Sequence of `safeApply` calls now deterministic across nodes. |
| 316 | `records` (`[]*AdmissionRecord` returned from `AllAdmissions`) | **Safe** | BadgerDB iterator returns lexicographic key order — deterministic. Slice range, not map range. |
| 339 | `rec.MissingPrerequisites` (`[]event.EventID`) | **Safe** | Slice range. |
| 388 | `rec.Consumers` (in `Recover`) | **Unsafe-without-sort** | **Fixed**: `Recover` lex-sorts consumer names before invoking `RecoveryProbe`. Probe order is observable when probes share targets. |
| 423 | `rec.Consumers` (in `checkSchemaVersions`) | **Safe-with-note** → **Fixed** | Iteration affects only the FIRST mismatch reported. Lex-sort applied for diagnostic stability — operator triage now reproducible across nodes. |

### `internal/dispatch/deferral.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 21 | `d.deferralIndex` (`map[event.EventID][]string`) | **Safe** | The map is mutated to remove a specific admission key from every prereq's index. Removal is idempotent and order-independent. No state leaves this function as a slice. |
| 23 | `keys` (`[]string`, slice extracted from map value) | **Safe** | Slice range; the inner loop deletes by index from the keys slice — copy semantics, no cross-call ordering. |
| 41 | `records` (slice from `AllAdmissions`) | **Safe** | Slice range, deterministic from BadgerDB. |
| 65 | `keys` (`[]string`) | **Safe** | Slice range. |
| 78 | `rec.MissingPrerequisites` (slice) | **Safe** | Slice range. |

### `internal/dispatch/prerequisites.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 35 | `consumers` (`[]Consumer`) | **Safe** | Slice range. |
| 36 | `c.Prerequisites(ev)` (`[]event.EventID`) | **Safe** | Slice range. |
| 46 | `prereqSet` (`map[event.EventID]struct{}`) | **Safe-with-note** | Used to dedupe prerequisite IDs. The output `[]event.EventID` is then appended to `rec.MissingPrerequisites`. **Action**: sort before append so the persisted record is byte-equal across nodes for the same input prereq set. Tracked as a Priority 1 micro-fix below. |

### `internal/dispatch/types.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 136 | `consumers` (`map[string]ConsumerStatus`) in `computeTopLevelState` | **Safe** | Reduces to a single state via OR-like aggregation. Order-independent. |

### `internal/dispatch/anchor.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 39 | `tips` (`[]event.EventID` from `dag.Tips()`) | **Safe-with-note** | Slice range over `dag.Tips()`. **CORRECTION**: `dag.Tips()` does NOT sort — `internal/dag/dag.go:424` iterates `d.tips` (a map) and returns the slice unsorted. The dispatcher's `currentAnchor()` (`dispatcher.go:160`) picks `tips[0]`, which is therefore non-deterministic across nodes. Per locked invariant C-15 ("admission state is non-canonical node-local"), the anchor choice is recorded only in the per-node `AdmissionRecord` and does NOT affect canonical state. Safe by design — but a hardening fix is queued (E.P2.A1 below). |

### `internal/settlement/applicator.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 400 | `a.deferred` (`map[event.EventID]*SettlementPayload`) | **Safe** | Snapshot copy. Map-to-map copy preserves no order; downstream uses the snapshot, not insertion order. |
| 405 | `snapshot` (same shape) | **Safe** | Each iteration calls `a.applyToTarget(sp, target)` for a UNIQUE target ID. Different targets touch independent ledger surfaces; final state is order-independent (commutative). The applied-set gates re-application, so per-target idempotency holds regardless of order. |
| 427 | `entries` (`map[string][]byte` from `allMeta`) | **Safe** | Populates the applied-set as a `map[event.EventID]struct{}`. Set membership is order-independent. |

### `internal/settlement/funding_transfer_lookup.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 44 | `scanner.All()` (`[]*event.Event`) | **Safe** | Slice range. The scanner returns DAG events in topological order. |

### `internal/settlement/generation_ledger_calculator.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 95 | `ev.CausalRefs` (`[]event.EventID`) | **Safe** | Slice range. |
| 137 | `ancestorEv.CausalRefs` (slice) | **Safe** | Slice range. |
| 154 | `ancestors` (`[]*event.Event`) | **Safe** | Slice range, ordered. |

### `internal/settlement/verification_consensus_settler.go`

| Line | Map | Classification | Disposition |
|---|---|---|---|
| 181, 321, 356, 368, 382, 399, 407 | All slices (`dist.Recipients`, `round.Votes`, `recipients`, `entries`, `m`, `d.Recipients`) | **Safe** | All slice ranges. The settler's input data is already slice-form. No map iteration. |

---

## Micro-fix queued (Priority 1)

`internal/dispatch/prerequisites.go:46` — sort the deduped prerequisite-ID slice before returning. Single 3-line change. Bundled into the next E.P2 commit (step 11) rather than a third edit to the dispatcher in this commit, since it's a hygienic write affecting only the persisted record's byte-form, not consumer behavior. Action item tracked at the bottom of this document.

## Priority 2 / Priority 3 — deferred

Tracked for F4A step 11. Current evidence:
- `internal/recognition/bus.go:163` — `for _, c := range b.consumers` — same shape as dispatcher.go:170; **Unsafe-without-sort**, fix in step 11.
- `internal/recognition/index.go:113`, `:296` — both map iterations; need classification in step 11.
- `internal/recognition/badger_index.go:53` — building a return map from BadgerDB data; map-to-map; safe.
- `internal/recognition/activation.go:40` — `deferred` slice-typed; safe.
- `internal/recognition/replay.go:81` — slice range; safe.
- `internal/recognition/task_verification_consensus_consumer.go:188` — `actions` slice; safe.

Per plan §6.1 Priority 3 ("Classify but don't fix unless Unsafe-without-sort. Schedule any Unsafe-inherently to follow-on workstreams."), the bulk audit happens in step 11; Priority 1 is closed by this commit.

---

## Verification

- `go build ./...` — clean
- `go test -race -count=1 ./internal/dispatch/ ./internal/dispatch/conformance/ ./internal/recognition/ ./internal/settlement/ ./internal/integration/ ./internal/verification/cross_node/...` — all PASS
- Cross-node byte-equality harness (F4A step 1) still passes uniformly on the trivial corpus, so the sort changes did not introduce divergence.

---

## Action items

| ID | Description | Owner / sequencing |
|---|---|---|
| E.P1.A1 | Sort prereqSet output in `dispatch/prerequisites.go:46` | F4A step 11 (bundle with E.P2) |
| E.P2.A1 | Sort `dag.Tips()` output internally OR sort in `dispatcher.currentAnchor()` for stable per-node anchor selection | F4A step 11 (Priority 2 — recognition / DAG layer; not canonical-state-affecting per C-15) |
| E.P1.A3 | Audit Priority 2 + Priority 3 callsites | F4A step 11 |
| E.P1.A4 | Implement E-2 CI lint that fails build on unannotated `range over map` | F4A step 11 |

## FINDING (added to F4A architect-gate report ledger)

| ID | Severity | Surface |
|---|---|---|
| `dag-tips-unsorted` | Low (per C-15 admission state is non-canonical) | `dag.Tips()` returns its slice in random map-iteration order. Consumers depending on deterministic tip selection must sort themselves. Currently only `dispatcher.currentAnchor()` does this and it lands in the per-node admission record only. Hardening queued as E.P2.A1. |

---

**End of E.P1 audit v1.**
