# Forward notes — derivation package

Open architectural questions that 5B implementation cannot resolve on
its own; surfaced here for resolution at the 5B completion gate (before
F5 merge) or at the locked Reputation-and-Consensus-Integrity workstream
coordination point.

Each note documents what the implementation is doing today, why that is
safe in the current window, and what must change before the window
closes.

---

## 1. ReputationActivationEventID as `const event.EventID = ""` — V-1 hole at upgrade time

**Surfaced:** 5B skeleton breakpoint 1 (2026-04-24), flagged by architect.

**Current implementation:** `activation.go` defines
`ReputationActivationEventID` as a compile-time constant equal to the
empty string. `dag.IsAncestor("", R)` returns false for every R, so
every round pre-activation selects `NeutralBPStubW` automatically
without reaching any runtime flag. `const` (not `var`) is chosen to
satisfy the §2.1 DerivationInputs contract — a mutable variable would
be a state-leaking path that could be overwritten at runtime to alter
selection behavior.

**Why this is correct for today:** the locked Reputation-and-Consensus-
Integrity workstream has zero production callers of the real W
implementation. Every round in the F5 ship window settles with
NeutralBPStubW. The empty-string placeholder never triggers a wrong
decision — only a correct one (always use stub).

**The hole:** when the real workstream ships and
`ReputationActivationEventID` must become a real canonical event ID, a
source-level flip of the constant requires a binary-version change.
During the rollout window nodes running the old binary compute
`ActivationCheck("", R) == false` (select stub) and nodes running the
new binary compute `ActivationCheck(realID, R) == true` (select real).
Two correct binaries produce different DerivationResult values for the
same canonical state — property D-1 is violated for the duration of
the rollout.

This is binary-version-bound selection, not canonical-DAG-bound —
precisely the V-1 failure mode the invariant forbids.

**What must be resolved before the constant flips:** one of:

1. **Canonical-state-sourced activation ID.** Read the activation
   event ID from a canonical admin/genesis record (e.g., a
   CanonicalParameterSet event in the DAG) rather than from a binary
   constant. Every node at every binary version computes
   `ActivationCheck(lookupCanonical(), R)` against the same canonical
   state. A single canonical event introduces the activation ID; all
   nodes learn it at the same canonical position; V-1 holds through
   rollout.

2. **Canonical bootstrap pattern.** Define a protocol-level registry
   of named activation events sourced from a canonical bootstrap
   record at chain genesis; upgrades advance the registry via
   canonical events, not binary constants.

3. **Protocol hard-fork discipline.** Accept that the activation flip
   is itself a coordinated hard fork — all nodes upgrade their binary
   at a scheduled canonical position; rounds that straddle the fork
   are handled by the hard-fork protocol. D-1 violations are bounded
   by the fork window and treated as known protocol-upgrade behavior,
   not a silent divergence.

Options 1 and 2 preserve the "no binary-version-bound canonical
behavior" invariant cleanly; option 3 accepts a bounded, documented
violation as the cost of the upgrade. Option 3 is viable only if the
protocol has an explicit hard-fork mechanism to coordinate; otherwise
it is unsafe and reduces to uncoordinated rollout divergence.

**Scope:** coordination required with the locked Reputation-and-Consensus-
Integrity workstream (that workstream owns the real W implementation
and the definition of the activation event). This derivation package
cannot unilaterally decide the mechanism; the workstream and the
protocol-upgrade discipline together must land one of the three
options (or an equivalent) before the real constant is wired.

**Resolution track:** surface to architect at the 5B completion gate.
If the mechanism is not resolved by F5 merge, the flag remains as
stub-universal and the hole is carried forward into F5 post-ship
workstream planning. The derivation package does not require mechanism
resolution to ship — it only requires the mechanism to exist before
the constant flips.

---

## 2. EpochBoundary signer canonical-validator-snapshot binding — deferred until snapshot infrastructure ships

**Surfaced:** sub-spec implementation breakpoint B (2026-04-24).

**Current implementation:** `internal/epoch/boundary_validator.go` `BoundaryAdmissionValidator` enforces sub-spec v2.2 §1.4 items 1-5 (payload-shape checks, TriggerEventID type+existence, canonical epoch-count cross-check, canonical threshold-crossing cross-check). Signature validity is enforced upstream by `dag.Add`'s existing `crypto.VerifyEvent` step before the validator fires.

**The deferred check:** sub-spec §1.4 last bullet — "Signature: EpochBoundary events MUST be signed by a validator seated in the canonical validator-seat snapshot effective at `TriggerEventID`'s canonical position." The validator does NOT bind signer eligibility to a per-canonical-position validator-seat snapshot today.

**Why this is correct for the current window:** the binding requires a canonical validator-seat-snapshot read primitive that doesn't yet exist in the codebase. The same primitive is required by the locked Reputation-and-Consensus-Integrity workstream (snapshot emissions per its §5.2) and would be built there, not here. F5 5B's correctness goals — canonical settlement, no D-1 violation — are met by the canonical-state checks (items 1-5): a Byzantine signer who is in the validator manifest can sign an EpochBoundary, but if the canonical-state cross-check fails the event is rejected at admission and never enters the canonical DAG. The signer-binding check is a slashing/attribution surface (knowing WHO emitted an attempted-but-rejected event), orthogonal to settlement correctness.

**The hole:** an out-of-set Byzantine signer (one no longer in the canonical validator-seat snapshot at the trigger's canonical position but still in the runtime manifest) could pass `crypto.VerifyEvent` and pass the canonical-state cross-check (because they did the math right) — and successfully emit an EpochBoundary. Multi-emit logical-key dedup converges to one canonical EpochBoundary per Epoch regardless, so cross-node consistency is preserved; the only gap is "is this emitter canonically-authorized to emit at this position." Today: no canonical authorization check; a malicious holdover whose seat was canonically removed but whose key is still trusted by the runtime manifest could be the canonical emitter for an Epoch. F5 5B does not ship slashing-for-EpochBoundary so this carries no economic penalty surface, but it does mean the canonical EpochBoundary's emitter identity is not sourced from canonical state.

**Concretely (architect note, breakpoint B closure 2026-04-24):** an out-of-set validator retaining their signing key could participate in EpochBoundary emission under current implementation. Harmless to canonical semantic (cross-check layer preserves correctness regardless of signer). Becomes consequential only when signer-attribution surface is wired for slashing. Scope: locked Reputation-and-Consensus-Integrity workstream's validator-seat-snapshot primitive delivers the canonical read; F5 5B completion gate captures this dependency.

**What must be resolved before the window closes:** the canonical validator-seat-snapshot read primitive ships (locked workstream or adjacent infrastructure). Once available, `BoundaryAdmissionValidator` adds: read snapshot at `TriggerEventID`'s canonical position, verify `ev.AgentID` is in the snapshot's seat set, reject otherwise. The substrate (admission-cross-check mechanism) is in place and the validator function is straightforward to extend.

**Scope:** coordinated with locked Reputation-and-Consensus-Integrity workstream (snapshot infrastructure owner). This forward note exists alongside `internal/settlement/derivation/FORWARD_NOTES.md` §1's V-1 const-flip note as parallel architectural carries — both are issues to resolve at or before the canonical snapshot infrastructure lands.

**Resolution track:** surface to architect at sub-spec implementation completion gate. Implementation discretion at completion gate: (a) accept as carried-forward forward note, document in completion gate report, defer to snapshot-infrastructure ship; (b) halt 5B and require snapshot infrastructure now.

---

## 3. `TestTypeE_SyntheticReplayConformance/PopulatedDAGReplay_PerKey` flake — RESOLVED at #134-followon (Path A)

**Status (2026-04-24):** RESOLVED. Per architect direction at #134 closure: Path A — per-`(consumer, key)` `sync.Map` lock at the dispatcher layer. Same defense-in-depth pattern as `internal/escrow/applicator.go`'s `recordLocks`. Lock spans the read-modify-write region of `admitOneLogicalKey` (`internal/dispatch/logical_key_admit.go`). New `keyLocks sync.Map` field on `Dispatcher` (`internal/dispatch/dispatcher.go`) carries the framing-discipline doc explicitly: lock is intra-node defense-in-depth only; cross-node correctness rests on each LK consumer's `Apply` canonicality + ledger `ErrDuplicateEntry` idempotency + the F4B contract that LK `Apply` is idempotent or no-op for byte-distinct events with the same logical key.

**Verification:**
- New regression test `TestAdmitOneLogicalKey_PerKeyLockEliminatesRace` (`internal/dispatch/logical_key_race_test.go`) deterministically reproduces the race with a 50ms-sleep `Apply` and asserts exactly-1 Apply call. Pre-fix: 3-of-3 deterministic FAIL ("Apply fired 2 times"). Post-fix: green.
- `TestTypeE_SyntheticReplayConformance` re-run 50× via `go test -count=50`: 50/50 green (was 1-in-N intermittent).

**Note (the original investigation, kept for context):**

[Original §3 retained below for historical reference; superseded by the RESOLVED status above.]

---

### Original investigation (pre-resolution)

**Surfaced:** F5 5B settler integration (#132) full-sweep run, 2026-04-24. Pre-existing flake (predates this work); first observed during the post-integration full sweep. Reruns pass; the race manifests intermittently.

**Where:** `internal/dispatch/conformance/logical_key_replay.go:74` — `runLKReplayPerKey` test asserts the per-key Apply guarantee under multi-emit + multi-worker replay. The "fired Apply 2 times; expected exactly 1" failure indicates two workers both invoked `Apply` for the same logical key.

**Root cause:** `internal/dispatch/logical_key_admit.go:109-175` `admitOneLogicalKey` has a state-machine race between line 127 (`if rec.State == StateApplied { return nil }`) and line 169-173 (writes `StateApplied`). Between read and write there is NO lock on the per-`(consumer, key)` admission record. With multiple commit-bus workers (`recognition.DefaultWorkers = 4`) processing two byte-distinct events for the same logical key concurrently, both workers can:

1. Pass the early-exit gate at line 127 (record's State is not yet `StateApplied`).
2. Call `IsComplete` + `DeriveOutcome` + `safeApplyLogicalKey`.
3. Both successfully invoke `Apply` before either writes `StateApplied`.

The race window is the duration of `IsComplete` + `DeriveOutcome` + `Apply`. For lightweight consumers (synthetic test consumers; `EpochBoundaryLogicalKeyConsumer.Apply` is a no-op), the window is microseconds. For heavyweight consumers (post-#132 `TVConsensusLogicalKeyConsumer.Apply` calling `DeriveSettlement` + `ApplySettlementRecords`), the window is much wider. This is why the flake surfaced post-#132 even though the bug is pre-existing.

**Why this is correct for the current window:**
- The flake is intermittent (rerun-passes); does not block CI.
- Per-key Apply duplication for `EpochBoundaryLogicalKeyConsumer` is benign — `Apply` is a no-op (sub-spec §2.2; canonical effect is the DAG admission, already gated by the canonical-cross-check at `dag.Add`).
- Per-key Apply duplication for `TVConsensusLogicalKeyConsumer` is benign per F5 5B's canonical-correctness model — `escrow.ApplySettlementRecords` is idempotent via canonical_id at the ledger layer (`internal/ledger/transfer.go:531` `ErrDuplicateEntry`); a second `Apply` call's `DeriveSettlement` produces byte-identical records (D-1) and the applicator's per-canonical_id locks + ledger dedup absorb the duplication.

So the flake is **correctness-safe** today (no double-pay; no canonical divergence); it's a **test-suite-quality** issue (intermittent failures mask real regressions during 5-node testnet observation).

**The hole:** the dispatcher's per-`(consumer, key)` state machine should be atomic — a record-level mutex around the read-modify-write region in `admitOneLogicalKey` would close it. Without that fix, future heavyweight LK consumers may see the duplication count climb (more workers + slower Apply = wider race window), and any consumer whose Apply is NOT idempotent would be at risk.

**What must be resolved before the window closes:** add per-`(consumer, key)` mutex in the dispatcher (`reserveOrLoadLogical` returns the lock alongside the record; `admitOneLogicalKey` holds it across the read-modify-write region). Compatible with existing `processConsumer` execution model — the lock is per-record, not global.

**Scope:** dispatcher package internal change. No protocol semantic change. Test will go from intermittent to reliable. F5 5B implementation does not BLOCK on this fix because canonical correctness is preserved by downstream idempotency (canonical_id ledger dedup); but the test-suite signal-degradation is real.

**Resolution track:** investigate + fix before F5 5B testnet verification (#135). If the fix is too large for that window, document the test-suite expectation explicitly (mark the test as "known intermittent" with a `t.Skip` gated on a flag) so 5-node deploy verification doesn't false-fail on this surface.

---

## 4. DerivationInputs contract enforcement — RESOLVED at multi-AI Item 1 composite (2026-04-25)

**Status (2026-04-25):** RESOLVED. Per architect direction at multi-AI Item 1 closure: composite shape (Option B + Option A's honest pieces). Both Grok and ChatGPT independently converged on this shape; architect locked it.

**What landed:**

*Part A — function-field surface eliminated.* `ActivationCheck func(...)` field deleted from `DerivationInputs`. Replaced with two canonical-frozen `event.EventID` fields (`reputationActivationEventID`, `qualityActivationEventID`) and a derivation-local `isActivated(reader, activationID, sealCtx)` helper performing the canonical-ancestor check directly via `AnchorReader.IsAncestor`. The closure surface that previously hid runtime-flag capture is no longer syntactically expressible.

*Part B — unexport + constructor + adapters + lint.*
- Every `DerivationInputs` field is unexported. External composite-literal field assignment is prevented by the Go type system.
- `NewDerivationInputs(...)` is the only supported external construction path. Validates: `treasuryID == genesis.BucketTreasury`; required services (`W.Stub`, `Quality.Stub`, `dagReader`, `escrowMgr`) non-nil; activation EventIDs accepted as-is (empty string is the pre-locked-workstream placeholder).
- Adapter wrappers exist for each service interface DerivationInputs surfaces: `escrowDerivationLookup` (escrow), `qScoreFnAsCanonicalW` (W), `dagAnchorReaderAdapter` (DAG). `Quality` is wired with the pure `NeutralQualityStub{}` (no underlying service to wrap until the future quality workstream lands).
- New lint rule (`internal/settlement/lint/derivation_inputs_construction.go`) flags `*ast.CompositeLit` of type `derivation.DerivationInputs` outside the derivation package itself. Catches the residual zero-value `derivation.DerivationInputs{}` pattern that the unexported fields can't prevent at the type level. Hard-fail in `Report.HasFailures()`.

*Part C — test surface migration.* Tests that previously injected synthetic `ActivationCheck` closures now control activation behavior via fake `AnchorReader.IsAncestor` responses + activation EventID values. Mechanism change only; same V-1 invariant exercised by the same canonical primitive used in production.

**Why the composite (B + A's honest pieces) was the right call:**
- Option B alone (restructure ActivationCheck) eliminates the function-field hiding place but leaves field assignment free; future maintainers could still add a non-canonical field unchecked.
- Option A alone (marker-type) was rejected at architect direction: the founder explicitly noted Go's type system can't prove a function doesn't close over runtime state — the marker is review-discipline, not mechanical proof.
- Combined: B eliminates the closure surface (mechanical), A's constructor + adapter + lint discipline catches the remaining surface (mechanical at the lint layer + boundary validation at construction time). The §2.1 contract is now enforced at three layers: unexported fields (type system), construction-time validation (constructor), composite-literal scan (lint).

**Closure verification:**
- `go test -race ./internal/settlement/derivation/... ./internal/settlement/...`: green.
- New constructor tests (`inputs_test.go`): 7 tests covering happy path + each validation rule.
- New lint negative test (`derivation_inputs_construction_test.go`): synthetic module's external `derivation.DerivationInputs{}` literal is flagged; in-derivation-package construction is allowed.
- §5 halt-trigger evaluation (V-1 non-pure, D-1 byte-equality, derivation-impurity): clean. Activation now performed via canonical primitive (`dagReader.IsAncestor`) directly with a canonical-frozen activation EventID — same canonical-position-bound check as before; only the location moved.

---

## How to extend this file

Add a new numbered section per forward-note. Every note should state:
1. When it was surfaced (breakpoint + date).
2. What the implementation does today.
3. Why today's implementation is safe in the current window.
4. The hole / open question.
5. What must change before the window closes.
6. The scope / coordination required.
7. The resolution track (which gate / workstream owns the resolution).

Notes are removed from this file when the resolution lands, with a
journal entry marking the closure.
