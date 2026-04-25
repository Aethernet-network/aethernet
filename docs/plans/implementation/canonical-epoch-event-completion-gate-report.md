# Canonical Epoch Event sub-spec — completion gate report

**Workstream**: F5 — Canonical Settlement Derivation
**Phase**: 5B — sub-spec absorbed into 5B per secondary-halt resolution.
**Status**: Pre-architect-review of completion gate. Pre-founder-confirmation. Pre-resumption of F5 5B breakpoint-2 (DeriveSettlement body fill-in).
**Branch**: `feat/f5-5b-derivation` (from `51bce89` F4-frozen + uncommitted 5A.4 working tree).
**Date**: 2026-04-24

**Sub-spec doc**: `docs/architecture/canonical-epoch-event.md` (v2.3, 620 lines).
**Predecessor**: F5 Phase 5B Plan v3 §0 decision 2 amended twice during implementation (canonical-epoch absorption + admission-cross-check substrate addition).

---

## 1. Sub-spec scope summary

### 1.1 What was produced

A canonical, byte-identical-across-nodes definition of `epoch_of(R)` rooted in DAG topology rather than a counter primitive whose ordering breaks under concurrent dispatch:

```
epoch_of(R) = CountAncestorsByType(R.CanonicalSealContext, EventTypeEpochBoundary)
```

Every component required to make that line correct:

- **`EventTypeEpochBoundary`** canonical event type with payload schema (`Version`, `Epoch`, `TriggerEventID`).
- **`CountAncestorsByType`** primitive on `*dag.DAG` and on the derivation package's local `AnchorReader`, with all-or-defer semantic.
- **Admission cross-check substrate** (`RegisterAdmissionCrossCheck` + `WhileLockedReader`) on `*dag.DAG` — restricted-API for canonical-state-dependent payload validation at admission time.
- **`BoundaryAdmissionValidator`** — pure-function implementation of sub-spec §1.4 cross-checks.
- **`BoundaryEmitter`** recognition consumer (Candidate A: every-node symmetric emission).
- **`EpochBoundaryLogicalKeyConsumer`** dispatcher consumer (Epoch-keyed dedup, NOT content-hash).
- **`TaskVerificationRound.CanonicalSealContext` + `EpochAtFinalization`** canonical fields populated at terminal-transition by the finalizing consumer.
- **`cmd/node/main.go` wiring** for the three new consumers + the cross-check registration.

### 1.2 What was absorbed (scope expansion beyond Plan v3)

Plan v3 §0 decision 2 stated "5A primitives consumed, not re-designed." Implementation surfaced two infrastructure gaps that required substrate work:

**Absorption 1 — canonical-epoch primitive itself.** Per the **secondary halt** at breakpoint-2 opening: `epoch.RoundCounter` is non-canonical under multi-worker commit-bus dispatch (DefaultWorkers=4; cross-event Apply ordering varies per node). 5A.2 §11.4's "R.RoundCounter is deterministic from canonical state" was an aspirational claim that didn't hold under the concurrent dispatch model. Founder ruled Option 1 (absorb into 5B). The sub-spec defines the new canonical primitive built on EpochBoundary events.

**Absorption 2 — admission-cross-check substrate.** Per the **breakpoint-B halt**: `dag.Add` had only signature + causal-refs validation; no per-type payload-validation hook. Sub-spec v2.1 §1.4 stated "Enforced at dag.Add admission time" — which the substrate didn't support. Architect locked Option A: add a restricted-API `RegisterAdmissionCrossCheck` mechanism to `*dag.DAG`. EpochBoundary is the first user; future canonical primitives (per-epoch snapshots, reputation evidence, slashing challenges, protocol-parameter changes) can register their own validators against the same substrate.

Both absorptions are documented as Plan v3 §0.2 amendments in the journal.

### 1.3 Implementation order

Three breakpoints, each closed with green race tests + an architect ack before proceeding:

1. **Breakpoint A** — Event type + dag primitive + interface extension.
2. **Breakpoint B** — Admission cross-check substrate (interim halt + sub-spec patch v2.2) → admission validator + emitter + LK consumer + cmd/node wiring.
3. **Breakpoint C** — `TaskVerificationRound` field additions + finalizing-consumer wiring + 4-scenario integration tests.

---

## 2. Deliverables

### 2.1 Sub-spec document

`docs/architecture/canonical-epoch-event.md` — v2.3, 620 lines. Sections:

- §0 decisions locked
- §1 event type definition (incl. §1.4 validation rules + §1.4.1 admission-cross-check mechanism + §1.5 content-hash determinism)
- §2 emission mechanism (Candidate A locked; B and C retained as design-rejected alternatives)
- §3 ancestor-counting primitive (incl. all-or-defer semantic + perf notes)
- §4 epoch_of(R) semantic + cutoff_epoch formula
- §5 reputation-workstream coordination (5 confirmation items, all COMPATIBLE; §5.4 reusable-infrastructure note)
- §6 5A.2 §11 cutoff-semantics verification (incl. §6.4 Fix A orthogonality)
- §7 genesis / bootstrap
- §8 R.EpochAtFinalization field source (incl. §8.2 canonical-DAG-view discipline)
- §9 halt triggers (8 triggers including the new §9 admission-cross-check-misimplementation trigger)
- §10 scope boundaries
- §11 risks + dependencies
- §12 hidden-error candidates (6 subsections post-renumbering)
- §13 sign-off sequence + change log (v1 → v2 → v2.1 → v2.2 → v2.3)

### 2.2 Code

| Path | Purpose |
|---|---|
| `internal/event/event.go` | `EventTypeEpochBoundary` + `EpochBoundaryPayload` |
| `internal/event/canonical_payload_reflection_test.go` | Float-freedom invariant — payload registered, count → 19 |
| `internal/event/lint/canonical_float_lint.go` | AST lint registry — payload registered, count → 19 |
| `internal/dag/dag.go` | `CountAncestorsByType` + `AdmissionCrossCheck` type + `WhileLockedReader` interface + `RegisterAdmissionCrossCheck` + `crossChecks` field + `Add` extension + sentinels (`ErrCrossCheckRejected`, `ErrCrossCheckAlreadyRegistered`) |
| `internal/epoch/boundary_validator.go` | `BoundaryAdmissionValidator` pure function + 6 sentinel errors |
| `internal/epoch/boundary_emitter.go` | `BoundaryEmitter` recognition CommitConsumer (Candidate A) |
| `internal/dispatch/epoch_boundary_lk_consumer.go` | `EpochBoundaryLogicalKeyConsumer` (Epoch-keyed dedup) |
| `internal/settlement/derivation/inputs.go` | Local `AnchorReader` extended with `CountAncestorsByType` |
| `internal/taskverification/round.go` | `CanonicalSealContext` + `EpochAtFinalization` fields + `Canonical()` projection |
| `internal/recognition/task_verification_consensus_consumer.go` | `EpochAncestorReader` interface + `dagReader` ctor param + finalize-time field population per §8.2 |
| `cmd/node/main.go` | 3 registrations: validator at dag setup, LK consumer after TVConsensus LK, emitter after RoundCountConsumer |

### 2.3 Tests

| Path | Purpose |
|---|---|
| `internal/dag/count_ancestors_by_type_test.go` | 7 primitive tests: missing descendant, genesis, irreflexive, linear chain, diamond, no-match, determinism-across-runs |
| `internal/dag/count_ancestors_by_type_groundtruth_test.go` | 6 ground-truth tests (architect ask): deep linear sparse-match, diamond on one branch, anchor-at-root, descendant-self irreflexive, multi-branch merge, long no-match chain |
| `internal/dag/admission_cross_check_test.go` | 7 substrate tests: accept, reject, no-validator backward-compat, duplicate-registration, nil-validator, WhileLockedReader Get + CountAncestorsByType, restricted-API documentation assertion |
| `internal/epoch/boundary_validator_test.go` | 8 validator tests: canonical accept, wrong-version reject, epoch-zero reject, wrong-trigger-type reject, threshold-not-crossed reject, second-epoch mismatch, malformed payload, end-to-end EpochBoundary(2) acceptance after first |
| `internal/epoch/boundary_emitter_purity_test.go` | Grep-level §12.1 enforcement: 9 forbidden tokens (RoundCounter, CurrentEpoch, etc.) + affirmative CountAncestorsByType call |
| `internal/epoch/boundary_integration_test.go` | 5 integration tests: end-to-end emission flow, genesis epoch-0 no-emission, deferral-path ErrEventNotFound, multi-emit both-admitted (cross-check perspective), 3-node convergence content-hashes-differ-payloads-equal |
| `internal/dispatch/epoch_boundary_lk_consumer_test.go` | 7 LK-consumer tests: Key extracts decimal Epoch, same-key-different-signers, epoch-zero-rejected, IsComplete=true, Apply=no-op, RecoveryProbe=Completed, dispatcher dedup at LogicalAdmissionKey |

**Total**: 8 production code files (incl. cmd/node) + 7 test files. `go build ./...` clean. `go test -race ./...` green across the touched packages.

### 2.4 Forward notes (in `internal/settlement/derivation/FORWARD_NOTES.md`)

- §1 — `ReputationActivationEventID` const-flip V-1 hole at upgrade time.
- §2 — Signer canonical-validator-snapshot binding deferred until snapshot infrastructure ships (incl. architect's out-of-set-validator-key-participation extension).

### 2.5 Journal entries

`docs/historical/journal.txt` — five appended entries covering: F5 5B Plan v3 founder-approval + breakpoint-1; canonical-epoch sub-spec v1; v1 → v2 multi-AI; v2 → v2.1 coordination; v2.1 → v2.2 admission-cross-check halt + Option A lock + meta-lesson.

---

## 3. Architect sign-offs consolidated

Every architect-locked decision across the sub-spec lifecycle:

### 3.1 v1 architect review (4 items applied)

- §1.5 content-hash precision (Signature excluded, AgentID included; multi-emit produces distinct hashes; downstream consumers reference via CountAncestorsByType, not hash).
- §2.5 Candidate A locked; B and C retained as design-rejected alternatives.
- §9 performance gate concretized: 1ms p99 @ 10^6 ancestors.
- §13 sign-off reordered: reputation-workstream coordination BEFORE v2 revisions.

### 3.2 v2 multi-AI review (9 items applied)

5 structural / load-bearing (ChatGPT) + 2 hidden-error restructuring (Grok primary reframe + ChatGPT retry-storm elevation) + 2 implementation-alertness (Grok). All listed in §13.1 v2 change log.

### 3.3 v2.1 reputation-workstream coordination

5 confirmation items COMPATIBLE against `docs/plans/2026-04-12-reputation-and-consensus-integrity.md`. Two documentation-precision items applied in-place (§5.1 operationalization reference, §5.3 V-1 stub-to-real discontinuity acknowledgement).

### 3.4 v2.1 architect final read (3 residue fixes applied)

- §2 preamble stale "Architect decision required" phrasing.
- §5.2 stale "Action required" paragraph post-coordination.
- §12.3 duplicate of reframed §12.1 deleted; §12.4-§12.7 renumbered to §12.3-§12.6.

### 3.5 v2.2 implementation-time architectural patch

- Halt-and-surface at breakpoint B: `dag.Add` lacked per-type payload-validation hook. Architect locked Option A (substrate change with restricted-API discipline).
- §1.4 intro rewritten; new §1.4.1 mechanism description; new §5.4 reusable-infrastructure note; new §9 admission-cross-check-misimplementation halt trigger.
- Plan v3 §0 decision 2 amended.

### 3.6 v2.3 post-implementation precision patch

- Test-shape discovery at integration test 4: cross-check evaluates from trigger's perspective; multi-emit ALL passes admission; LK dedup converges. One-sentence layer-separation note added to §1.4 epoch-count cross-check bullet. No design change.

### 3.7 Founder approvals

- Plan v3 founder-approved 2026-04-24.
- Sub-spec v2.1 founder-approved 2026-04-24.
- Founder ruling on secondary halt: Option 1 (absorb canonical-epoch scope into 5B) per build standard + mission framing.
- v2.2 + v2.3 architect-locked precision patches; founder-rerun not required.

---

## 4. Multi-AI review notes

### 4.1 Grok validation (v2)

Near-clean "ship it" verdict with 3 implementation-alertness predictions, all integrated into §12.6:

- (i) Logical-key dedup MUST key on Epoch, not content-hash. **Validated at implementation**: `EpochBoundaryLogicalKeyConsumer.Key()` returns decimal-string Epoch; LK-dedup integration test passes.
- (ii) `CountAncestorsByType` performance cliff at genesis replay. **Status**: §9 1ms p99 @ 10^6 ancestors gate codified; benchmark-time decision deferred to actual scale.
- (iii) `TriggerEventID` rollback edge case. **Status**: noted; F5 5B does not exercise DAG fork resolution in scope.

Grok's primary §12.1 reframing — "the emitter or consumer reads local admission state instead of canonical DAG state" — captured in v2 + enforced by `boundary_emitter_purity_test.go` grep test.

### 4.2 ChatGPT validation (v2)

4 substantive structural findings + 1 cross-doc ambiguity catch + 1 operational-risk elevation:

- §3.1 all-or-defer sentence (prevents partial-count misimplementation). **Validated**: `CountAncestorsByType` defensive branch returns ErrEventNotFound on traversal-incomplete; tests cover.
- §1.4 signer canonical-position binding. **Status**: spec'd; implementation deferred per FORWARD_NOTES.md §2.
- §5.2 fifth coord item (epoch 0 acceptability). **Validated**: confirmed COMPATIBLE.
- §8.2 consumer-DAG-view discipline. **Validated**: `cmd/node/main.go` passes `stack.dag` (canonical view); EpochAncestorReader interface narrow-scoped.
- §6.4 Fix A orthogonality. **Validated**: derivation package's PayoutRecord `CanonicalCutoffAnchor` (Fix A) is distinct from `cutoff_epoch` (this sub-spec); both coexist.
- §12.6 stacked-canonical-deferral retry-storm operational risk. **Status**: highest-priority operational risk for 5B implementation; testnet §7 criterion 11b genesis-replay must exercise stacked defers per F5 5B Plan v3.

---

## 5. Forward notes carried to F5 5B completion gate

These items are not blocking for sub-spec closure but are open architectural questions that need resolution before F5 ship or coordination with adjacent workstreams.

### 5.1 ReputationActivationEventID const-flip V-1 hole

**Source**: `internal/settlement/derivation/FORWARD_NOTES.md` §1.

The activation event ID for the V-1 stub-to-real selection is currently a compile-time constant equal to the empty string. Flipping it to a real EventID requires a binary version change, which means rollout windows where some nodes select stub-W and others select real-W for the same canonical round — precisely the V-1 failure mode the invariant forbids. Three resolution paths: canonical-state-sourced activation ID, canonical bootstrap pattern, or coordinated hard-fork. None pre-decided; carry forward to F5 5B completion gate / locked workstream coordination.

### 5.2 Signer canonical-validator-snapshot binding deferral

**Source**: `internal/settlement/derivation/FORWARD_NOTES.md` §2.

`BoundaryAdmissionValidator` implements §1.4 items 1-5 (canonical-state cross-checks) but defers the signer-in-canonical-validator-snapshot-at-trigger-position binding until the canonical snapshot read primitive ships (locked Reputation-and-Consensus-Integrity workstream). Today: `crypto.VerifyEvent` (manifest-key trust) covers signature integrity; the deferred check is an attribution / slashing surface orthogonal to settlement correctness.

### 5.3 Out-of-set validator key participation

**Source**: `internal/settlement/derivation/FORWARD_NOTES.md` §2 (architect extension at breakpoint B closure).

A validator no longer in the canonical seat snapshot but still in the runtime manifest can participate in EpochBoundary emission today. Harmless to canonical semantic (cross-check layer preserves correctness regardless of signer); becomes consequential only when signer-attribution wires for slashing. Same scope as §5.2; resolved by snapshot infrastructure shipping.

### 5.4 Plan v3 §0 decision 2 amendments

The sub-spec's two scope-expansion absorptions (canonical-epoch primitive + admission-cross-check substrate) explicitly amend Plan v3 §0 decision 2's "5A primitives consumed, not re-designed" stance. Both amendments are journaled and acknowledged in §13.1 change log; both close at this completion gate.

---

## 6. Meta-lessons captured for future workstreams

### 6.1 Canonical-cross-check substrate as reusable infrastructure

The `RegisterAdmissionCrossCheck` mechanism (sub-spec §1.4.1) is the substrate addition required to make EpochBoundary's canonical-state cross-check enforceable at admission. It generalizes to any future canonical primitive whose payload validity depends on canonical DAG state: per-epoch snapshots, reputation evidence packets, slashing challenges, canonical protocol-parameter changes. §5.4 lists the candidate consumers explicitly.

The discipline is restricted-API by design — only canonical-state cross-checks, never policy or runtime-state validation. The §9 halt trigger "Admission-cross-check validator mis-implemented" enforces at implementation time.

### 6.2 Plan-to-code gaps surface infrastructure assumptions

Two distinct kinds of plan-to-code gap surfaced during this sub-spec implementation:

- **Abstract-claim gap**: "RoundCounter is canonical" was a Plan v3 §2.3 step 1 reference that turned out to require infrastructure proof (concurrent-dispatch ordering analysis). Resolved by the canonical-epoch primitive itself.
- **Concrete-API gap**: `RegisterAdmissionCrossCheck` didn't exist on `*dag.DAG` despite sub-spec v2.1 §1.4 citing "Enforced at dag.Add admission time." Resolved by the substrate addition.

Future plan reviews should pressure-test both kinds: for every "canonical X" claim, verify the concurrency model; for every "enforced at Y" reference, verify the API surface exists.

### 6.3 Layer separation precision

Test 4's discovery at breakpoint C (`MultiEmit_BothAdmittedByCrossCheck`) revealed an implementer-intuition trap that the sub-spec text already correctly specified but didn't make obvious. Two layers, two responsibilities:

- **Admission cross-check**: enforces canonical math (Payload.Epoch matches CountAncestorsByType from trigger's perspective).
- **LogicalKeyConsumer dedup**: enforces uniqueness (one canonical event per Epoch key).

Both are necessary. Removing either breaks the canonical-epoch guarantee. v2.3's one-sentence precision patch makes the layer separation explicit so future implementers don't make the same first-pass assumption.

### 6.4 Halt-and-surface discipline produces sound architecture under uncertainty

Three halts surfaced during sub-spec implementation: (a) prior `R.canonical_seal_context` + `epoch_of(R)` plan-to-code gap (resolved by Option A: extend `TaskVerificationRound`); (b) secondary halt on counter non-canonicality under concurrent dispatch (resolved by Option 3b: canonical EpochBoundary events); (c) breakpoint-B halt on missing admission-cross-check substrate (resolved by Option A: substrate addition with restricted-API discipline).

Each halt surfaced an option set with explicit blast-radius analysis. Architect + founder rulings selected the architecturally-correct path even when it meant scope expansion. The pattern matches F5 5A precedent and remains the discipline for F5 5B's remaining breakpoints.

---

## 7. Sub-spec readiness statement

All sub-spec deliverables are in place. Sub-spec implementation is complete. The canonical-epoch primitive is canonically-correct, byte-identical-across-nodes, defensively-validated at admission, deduplicated at the dispatcher LK gate, and observable via `epoch_of(R) = CountAncestorsByType(R.CanonicalSealContext, EventTypeEpochBoundary)`.

`go build ./...` clean. `go test -race ./internal/event/... ./internal/dag/... ./internal/epoch/... ./internal/dispatch/... ./internal/recognition/... ./internal/settlement/... ./internal/taskverification/... ./internal/integration/...` green.

No outstanding halt triggers. All §9 halt triggers and Plan v3 §5 halt triggers evaluated and unfired.

**Ready for**: architect review of completion gate report → founder confirmation → sub-spec completion gate closes → F5 5B breakpoint-2 resumes.

**5B breakpoint-2 scope**: `DeriveSettlement` body fill-in per F5 5B Plan v3 §2.3 steps 1-9 + `ReadAtAnchor` helper implementation per F5 5A.3 §2.2. The canonical-epoch primitive is the load-bearing input for `cutoff_epoch = max(epoch_of(R) - 1, 0)` consumed by `CanonicalWProjection.Lookup` and `CanonicalQualityProjection.Lookup`.

Cluster stays frozen at `f4-combined-0e93f48`. No main-merge until F5 closes.

---

**End of canonical-epoch-event sub-spec completion gate report. Ready for architect review.**
